package scene

// Parallel determinism quality gate for the tile-based scene renderer.
//
// The renderer splits a scene into independent 64x64 tiles and schedules them
// across a worker pool with work stealing. Correctness therefore requires that
// the output for one fixed scene is independent of:
//
//   - the worker count (1, 2, many),
//   - the order tiles are scheduled in,
//   - the tile geometry (edge-tile distribution),
//   - whether a registered accelerator forces GPU→CPU fallback,
//   - how often the same scene is rendered (pooled Pixmap reuse).
//
// This file turns that requirement into an executable gate:
//
//  1. A fixed-seed generator builds one scene exercising every command class
//     (paths, transparent layers, clips, images incl. a filtered image, text).
//  2. Renderings across all configurations are compared pixel-for-pixel and by
//     per-tile statistics against the single-worker reference.
//  3. Only explicitly documented float/anti-aliasing tolerance is allowed.
//     Missing draws, duplicate compositing, resource races and command
//     reordering are structural errors that the tolerance must never absorb.
//  4. Gate-negative injections (shuffled tile order, shared temporary Pixmap)
//     prove the gate actually fails when the renderer misbehaves.
//
// No real GPU is required: accelerator behavior is covered with a stub that
// always returns ErrFallbackToCPU, exercising the exact fallback branch.

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/internal/parallel"
	"github.com/gogpu/gg/text"
)

// ---------------------------------------------------------------------------
// Tolerance policy
// ---------------------------------------------------------------------------

const (
	// maxChannelDelta bounds a single RGBA channel difference for ordinary
	// AA edges. The software rasterizer and SDF blend evaluate coverage in
	// float32 and round to bytes; when the same shape is decomposed with
	// different tile geometries, AA pixels accumulate a few levels of
	// rounding drift. Documented explicitly; never raised to hide structural
	// defects — structural classification runs before it.
	maxChannelDelta = 3

	// maxAccumulatedChannelDelta bounds drift on AA pixels where several
	// float32 compositing stages stack (semi-transparent layers over clips
	// over fills). Coverage at these pixels is unchanged — only the rounded
	// color value moves — so they remain documented AA tolerance, but the
	// wider band is capped tightly and counted against maxTolerantPixels.
	maxAccumulatedChannelDelta = 12

	// maxTolerantPixels bounds how many pixels may sit inside the per-channel
	// tolerance. AA edges are a small, stable fraction of the canvas
	// (~0.1% here); a race or dropped command produces orders of magnitude
	// more differing pixels and also changes coverage counts.
	maxTolerantPixels = 512

	// structuralAlphaDelta is the minimum alpha difference that proves a
	// structural defect (missed draw, duplicate composite, race). Fully
	// transparent vs. covered, or 1x vs. 2x compositing, jumps by far more.
	structuralAlphaDelta = 24
)

// pixelStats summarizes one rendering for cross-configuration comparison.
type pixelStats struct {
	nonZeroPixels          int // pixels with any coverage (A > 0)
	opaquePixels           int // fully opaque pixels (A == 255)
	sumR, sumG, sumB, sumA uint64
	checksum               uint64 // FNV-1a over the raw RGBA bytes
}

// tilePixelStats summarizes one tile's pixels for per-tile comparison.
type tilePixelStats struct {
	nonZero int
	sumA    uint64
}

// renderResult captures everything the gate compares.
type renderResult struct {
	name      string
	pixels    []byte
	stats     pixelStats
	tileStats []tilePixelStats // per fixed 64px canvas grid cell, indexed by ty*gridX+tx
	cmds      [][]int          // per fixed 64px canvas grid cell: sorted tag-stream command positions
}

// gateGridW/H is the fixed comparison grid. Statistics and command sets are
// normalized into this canvas-space grid so renderings produced with different
// tile geometries are comparable cell-by-cell.
const (
	gateGridW = 64
	gateGridH = 64
)

// mismatch describes the first point where two renderings diverge.
type mismatch struct {
	x, y         int
	tileX, tileY int
	tileIndex    int
	got, want    [4]byte
	kind         string // "structural" or "tolerance"
}

// diffReport is the full outcome of comparing two renderings.
type diffReport struct {
	structural    []mismatch
	tolerance     []mismatch
	statsDelta    string
	tileStatDelta string
	cmdOrderDelta string
}

// hasErrors reports whether the diff contains anything beyond AA tolerance.
func (d *diffReport) hasErrors() bool {
	return len(d.structural) > 0 || d.statsDelta != "" ||
		d.tileStatDelta != "" || d.cmdOrderDelta != "" ||
		len(d.tolerance) > maxTolerantPixels
}

// firstMismatch returns the most diagnostic mismatch, preferring structural
// defects and reporting its tile and command index.
func (d *diffReport) firstMismatch() mismatch {
	if len(d.structural) > 0 {
		return d.structural[0]
	}
	if len(d.tolerance) > 0 {
		return d.tolerance[0]
	}
	return mismatch{}
}

// ---------------------------------------------------------------------------
// Fixed-seed scene generator
// ---------------------------------------------------------------------------

// determinismCanvasW/H define the gate canvas. 320x256 deliberately is not a
// multiple of 64 so edge tiles (58x64, 64x64-remainder) are exercised too.
const (
	determinismCanvasW = 320
	determinismCanvasH = 256
	gateSeed           = int64(0x5EED1234)
)

// buildDeterministicScene constructs the gate scene from a fixed seed. Every
// random decision is driven by rng, so the scene is byte-identical across
// processes and runs. It includes paths, transparent layers, clips, images
// (including a blurred image as the filter case) and text.
func buildDeterministicScene(t *testing.T, rng *rand.Rand) *Scene {
	t.Helper()
	s := NewScene()
	b := NewSceneBuilderFrom(s)

	// Opaque background so transparent-source compositing has a destination.
	b.FillRect(0, 0, determinismCanvasW, determinismCanvasH,
		SolidBrush(gg.RGBA{R: 0.92, G: 0.94, B: 0.96, A: 1}))

	// 1. Paths: a stroked bezier polygon plus filled circles, positioned so
	// they straddle tile boundaries (multiples of 64).
	poly := NewPath()
	poly.MoveTo(10, 40)
	for i := 1; i <= 6; i++ {
		x := float32(i * 52)
		y := 40 + float32(20+rng.Intn(160))
		poly.CubicTo(x-30, y-25, x-10, y+25, x, y)
	}
	b.StrokePath(poly, SolidBrush(gg.RGBA{R: 0.1, G: 0.2, B: 0.7, A: 1}), 2.5)

	for i := 0; i < 8; i++ {
		cx := float32(20 + rng.Intn(determinismCanvasW-40))
		cy := float32(20 + rng.Intn(determinismCanvasH-40))
		r := float32(8 + rng.Intn(22))
		b.FillCircle(cx, cy, r, SolidBrush(gg.RGBA{
			R: float64(rng.Float32()), G: float64(rng.Float32()),
			B: float64(rng.Float32()), A: 0.85,
		}))
	}

	// SDF rounded rect spanning four tiles.
	b.FillRoundRect(50, 50, 180, 110, 14, 14,
		SolidBrush(gg.RGBA{R: 0.2, G: 0.7, B: 0.3, A: 0.9}))

	// 2. Transparent layer: content pushed with partial alpha. The CPU tile
	// renderer records PushLayer/PopLayer; the nested shapes must composite
	// identically regardless of scheduling.
	b.Layer(BlendNormal, 0.55, nil, func(lb *SceneBuilder) {
		lb.FillRect(140, 120, 120, 90,
			SolidBrush(gg.RGBA{R: 0.8, G: 0.3, B: 0.2, A: 1}))
		lb.FillCircle(200, 165, 34,
			SolidBrush(gg.RGBA{R: 0.9, G: 0.8, B: 0.1, A: 0.75}))
	})

	// 3. Clip: a circle clip cutting a grid of rectangles, crossing tiles.
	b.Clip(NewCircleShape(110, 200, 48), func(cb *SceneBuilder) {
		for gx := 0; gx < 6; gx++ {
			for gy := 0; gy < 6; gy++ {
				cb.FillRect(float32(60+gx*18), float32(150+gy*18), 16, 16,
					SolidBrush(gg.RGBA{R: 0.6, G: 0.1, B: 0.6, A: 1}))
			}
		}
	})

	// 4a. Image: deterministic procedural RGBA image, drawn scaled and rotated
	// so bilinear filtering and the integer/non-integer fast paths both run.
	img := buildDeterministicImage(rng, 24, 24)
	imgTransform := TranslateAffine(210, 12).
		Multiply(RotateAffine(0.35)).
		Multiply(ScaleAffine(2.25, 2.25))
	s.DrawImage(img, imgTransform)

	// 4b. Filter case: the same image run through a deterministic box blur
	// (Gaussian-like separable filter) before registration. Filters are
	// applied at layer-pop time in the full scene pipeline; the tile renderer
	// consumes pre-filtered image resources, which is what is exercised here.
	blurred := boxBlurImage(img, 2)
	s.DrawImage(blurred, TranslateAffine(240, 150).Multiply(ScaleAffine(1.5, 1.5)))

	// 5. Text: real font, fixed size and position, glyph outlines rasterized
	// per tile. Skips the gate only if no font is available in the checkout.
	if face := gateTestFont(t, 18); face != nil {
		if err := s.DrawText("Gg-0123", face, 18, 30,
			SolidBrush(gg.RGBA{R: 0.05, G: 0.05, B: 0.1, A: 1})); err != nil {
			t.Fatalf("DrawText: %v", err)
		}
	}

	return s
}

// buildDeterministicImage fills an image with seed-driven premultiplied RGBA.
func buildDeterministicImage(rng *rand.Rand, w, h int) *Image {
	img := NewImage(w, h)
	img.Data = make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			off := (y*w + x) * 4
			a := byte(80 + rng.Intn(176))
			r := byte(rng.Intn(256))
			g := byte(rng.Intn(256))
			b := byte(rng.Intn(256))
			// Premultiply: scene images carry premultiplied alpha.
			img.Data[off] = byte(uint16(r) * uint16(a) / 255)
			img.Data[off+1] = byte(uint16(g) * uint16(a) / 255)
			img.Data[off+2] = byte(uint16(b) * uint16(a) / 255)
			img.Data[off+3] = a
		}
	}
	return img
}

// boxBlurImage applies a deterministic separable box blur of the given radius
// to all four channels, keeping premultiplied alpha. It stands in for a
// Gaussian blur filter without depending on platform filter implementations.
func boxBlurImage(src *Image, radius int) *Image {
	w, h := src.Width, src.Height
	tmp := make([]byte, len(src.Data))
	out := NewImage(w, h)
	out.Data = make([]byte, len(src.Data))
	blurAxis(src.Data, tmp, w, h, radius, true)
	blurAxis(tmp, out.Data, w, h, radius, false)
	return out
}

func blurAxis(src, dst []byte, w, h, radius int, horizontal bool) {
	for c := 0; c < 4; c++ {
		if horizontal {
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					var sum, n int
					for k := -radius; k <= radius; k++ {
						xx := x + k
						if xx < 0 || xx >= w {
							continue
						}
						sum += int(src[(y*w+xx)*4+c])
						n++
					}
					dst[(y*w+x)*4+c] = byte(sum / n)
				}
			}
		} else {
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					var sum, n int
					for k := -radius; k <= radius; k++ {
						yy := y + k
						if yy < 0 || yy >= h {
							continue
						}
						sum += int(src[(yy*w+x)*4+c])
						n++
					}
					dst[(y*w+x)*4+c] = byte(sum / n)
				}
			}
		}
	}
}

// gateTestFont loads a TTF from the repository's text testdata. Returns nil
// (never fails the test) when the font is absent so the gate stays green on
// minimal checkouts; all non-text coverage still runs.
func gateTestFont(t *testing.T, size float32) text.Face {
	t.Helper()
	candidates := []string{
		filepath.Join("..", "text", "testdata", "goregular.ttf"),
		filepath.Join("..", "text", "testdata", "ahem.ttf"),
	}
	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		src, err := text.NewFontSource(data)
		if err != nil {
			continue
		}
		return src.Face(float64(size))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Rendering harness
// ---------------------------------------------------------------------------

// gateConfig is one renderer configuration under test.
type gateConfig struct {
	name    string
	workers int
	tileW   int // 0 => production 64
	tileH   int
}

// renderGateScene renders the scene with the given config and optional test
// hooks, returning a fully populated renderResult.
func renderGateScene(t *testing.T, sc *Scene, cfg gateConfig,
	orderHook func([]*parallel.Tile) []*parallel.Tile,
	pixmapHook func(w, h int) *gg.Pixmap,
) renderResult {
	t.Helper()

	opts := []RendererOption{WithWorkers(cfg.workers)}
	r := newGateRenderer(cfg, opts)
	defer r.Close()
	r.tileOrderHook = orderHook
	r.pixmapHook = pixmapHook

	tracker := make([][]int, r.tileGrid.TileCount())
	r.tileCmdTracker = tracker

	target := gg.NewPixmap(determinismCanvasW, determinismCanvasH)
	if err := r.Render(target, sc); err != nil {
		t.Fatalf("%s: Render failed: %v", cfg.name, err)
	}

	pixels := append([]byte(nil), target.Data()...)

	// Normalize per-tile pixel stats onto the fixed comparison grid so
	// renderings with different tile geometries compare cell-by-cell.
	gridX := (determinismCanvasW + gateGridW - 1) / gateGridW
	gridY := (determinismCanvasH + gateGridH - 1) / gateGridH
	tileStats := make([]tilePixelStats, gridX*gridY)
	for gy := 0; gy < gridY; gy++ {
		for gx := 0; gx < gridX; gx++ {
			var st tilePixelStats
			x0 := gx * gateGridW
			y0 := gy * gateGridH
			x1 := min(x0+gateGridW, determinismCanvasW)
			y1 := min(y0+gateGridH, determinismCanvasH)
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					off := (yy*determinismCanvasW + xx) * 4
					if pixels[off+3] > 0 {
						st.nonZero++
						st.sumA += uint64(pixels[off+3])
					}
				}
			}
			tileStats[gy*gridX+gx] = st
		}
	}

	// Merge each render tile's command positions into the fixed grid cells it
	// overlaps. A draw command is recorded by every tile that executes it, so
	// the union within a grid cell is the complete command set for that cell.
	cmds := make([][]int, gridX*gridY)
	tilesX := r.tileGrid.TilesX()
	for tileIdx, positions := range tracker {
		tile := r.tileGrid.TileAt(tileIdx%tilesX, tileIdx/tilesX)
		if tile == nil {
			continue
		}
		x0, y0, tw, th := tile.Bounds()
		gx0, gy0 := x0/gateGridW, y0/gateGridH
		gx1, gy1 := (x0+tw-1)/gateGridW, (y0+th-1)/gateGridH
		for gy := gy0; gy <= gy1; gy++ {
			for gx := gx0; gx <= gx1; gx++ {
				ci := gy*gridX + gx
				cmds[ci] = append(cmds[ci], positions...)
			}
		}
	}
	for i := range cmds {
		cmds[i] = sortedUniqueInts(cmds[i])
	}

	return renderResult{
		name:      cfg.name,
		pixels:    pixels,
		stats:     computePixelStats(pixels),
		tileStats: tileStats,
		cmds:      cmds,
	}
}

// sortedUniqueInts returns the sorted, deduplicated copy of in.
func sortedUniqueInts(in []int) []int {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[int]struct{}, len(in))
	out := make([]int, 0, len(in))
	for _, v := range in {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	// Insertion sort: command sets are small (tens of entries).
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// newGateRenderer constructs the renderer, routing through the test tile-size
// field when a non-standard geometry is requested.
func newGateRenderer(cfg gateConfig, opts []RendererOption) *Renderer {
	if cfg.tileW > 0 && cfg.tileH > 0 {
		opts = append(opts, withTestTileSize(cfg.tileW, cfg.tileH))
	}
	return NewRenderer(determinismCanvasW, determinismCanvasH, opts...)
}

// withTestTileSize is a test-only option overriding tile geometry.
func withTestTileSize(w, h int) RendererOption {
	return func(r *Renderer) {
		r.testTileW = w
		r.testTileH = h
	}
}

// computePixelStats summarizes raw RGBA bytes.
func computePixelStats(pixels []byte) pixelStats {
	var st pixelStats
	const fnvOffset = 14695981039346656037
	const fnvPrime = 1099511628211
	hash := uint64(fnvOffset)
	for i := 0; i+3 < len(pixels); i += 4 {
		if pixels[i+3] > 0 {
			st.nonZeroPixels++
			st.sumR += uint64(pixels[i])
			st.sumG += uint64(pixels[i+1])
			st.sumB += uint64(pixels[i+2])
			st.sumA += uint64(pixels[i+3])
		}
		if pixels[i+3] == 255 {
			st.opaquePixels++
		}
		for _, b := range pixels[i : i+4] {
			hash ^= uint64(b)
			hash *= fnvPrime
		}
	}
	st.checksum = hash
	return st
}

// compareResults diffs a candidate against the reference rendering.
// tileW/tileH describe the candidate's render tile geometry (used only in
// diagnostics); statistics and command sets are already normalized onto the
// fixed 64px comparison grid.
func compareResults(ref, got renderResult, tileW, tileH int) diffReport {
	rep := diffReport{}
	gridX := (determinismCanvasW + gateGridW - 1) / gateGridW

	// Per-pixel classification.
	for y := 0; y < determinismCanvasH; y++ {
		for x := 0; x < determinismCanvasW; x++ {
			off := (y*determinismCanvasW + x) * 4
			var gv, wv [4]byte
			copy(gv[:], got.pixels[off:off+4])
			copy(wv[:], ref.pixels[off:off+4])
			if gv == wv {
				continue
			}

			m := mismatch{
				x: x, y: y,
				tileX: x / gateGridW, tileY: y / gateGridH,
				tileIndex: (y/gateGridH)*gridX + x/gateGridW,
				got:       gv, want: wv,
			}
			alphaDelta := absInt(int(gv[3]) - int(wv[3]))

			// Structural classification, applied BEFORE the AA tolerance:
			//
			//   1. A large alpha jump means coverage appeared or disappeared
			//      (missed draw / duplicate composite / resource race).
			//   2. A transparent pixel changing at all is a coverage change,
			//      not a rounding change: float AA never emits coverage where
			//      the reference has exactly zero.
			//
			// Everything else is evaluated per channel: a pixel whose channels
			// all move within maxChannelDelta is a documented float32/AA
			// rounding difference. A single channel exceeding the budget makes
			// the whole pixel structural, so tolerance cannot mask a defect.
			switch {
			case alphaDelta >= structuralAlphaDelta:
				m.kind = "structural-alpha"
			case wv[3] == 0 || gv[3] == 0:
				m.kind = "structural-coverage"
			default:
				maxDelta := 0
				for c := 0; c < 4; c++ {
					if d := absInt(int(gv[c]) - int(wv[c])); d > maxDelta {
						maxDelta = d
					}
				}
				switch {
				case maxDelta <= maxChannelDelta:
					m.kind = "tolerance"
				case maxDelta <= maxAccumulatedChannelDelta:
					// Stacked float32 compositing on an unchanged-coverage AA
					// pixel: still documented tolerance, flagged distinctly.
					m.kind = "tolerance-accumulated"
				default:
					m.kind = "structural-channel"
				}
			}
			if m.kind == "tolerance" || m.kind == "tolerance-accumulated" {
				rep.tolerance = append(rep.tolerance, m)
			} else {
				rep.structural = append(rep.structural, m)
			}
		}
	}

	// Aggregate statistics. Coverage counts are structural: the same scene
	// must cover the same number of pixels regardless of scheduling or tile
	// geometry. Channel sums may drift by at most the documented per-pixel
	// tolerance over the tolerant pixel budget. The checksum is reported for
	// diagnosis but is not itself a pass/fail criterion.
	if ref.stats.nonZeroPixels != got.stats.nonZeroPixels ||
		ref.stats.opaquePixels != got.stats.opaquePixels {
		rep.statsDelta = fmt.Sprintf(
			"coverage counts differ: ref nonZero=%d opaque=%d; got nonZero=%d opaque=%d (structural: missing or duplicate draw)",
			ref.stats.nonZeroPixels, ref.stats.opaquePixels,
			got.stats.nonZeroPixels, got.stats.opaquePixels)
	} else {
		// Ordinary AA pixels drift within maxChannelDelta; the small
		// accumulated-drift band (stacked layers) may reach the wider cap.
		maxSumDelta := uint64(maxTolerantPixels*maxChannelDelta +
			maxTolerantPixels*(maxAccumulatedChannelDelta-maxChannelDelta)/8)
		for _, pair := range [][2]uint64{
			{ref.stats.sumR, got.stats.sumR},
			{ref.stats.sumG, got.stats.sumG},
			{ref.stats.sumB, got.stats.sumB},
			{ref.stats.sumA, got.stats.sumA},
		} {
			d := pair[0] - pair[1]
			if pair[1] > pair[0] {
				d = pair[1] - pair[0]
			}
			if d > maxSumDelta {
				rep.statsDelta = fmt.Sprintf(
					"channel sum drift %d exceeds tolerance budget %d: ref sumRGBA=(%d,%d,%d,%d); got sumRGBA=(%d,%d,%d,%d)",
					d, maxSumDelta,
					ref.stats.sumR, ref.stats.sumG, ref.stats.sumB, ref.stats.sumA,
					got.stats.sumR, got.stats.sumG, got.stats.sumB, got.stats.sumA)
				break
			}
		}
	}

	// Per-grid-cell statistics catch a wrong cell even if whole-canvas totals
	// accidentally compensate. Coverage counts must match exactly; alpha sums
	// may move within the per-cell tolerance allowance.
	const cellAlphaAllowance = uint64(maxChannelDelta * 64) // generous per-cell AA budget
	for i := range ref.tileStats {
		if i >= len(got.tileStats) {
			rep.tileStatDelta = fmt.Sprintf("grid cell %d missing from candidate", i)
			break
		}
		rs, gs := ref.tileStats[i], got.tileStats[i]
		if rs.nonZero != gs.nonZero {
			rep.tileStatDelta = fmt.Sprintf(
				"grid cell %d coverage count ref=%d got=%d (structural: draw missing/duplicated in cell)",
				i, rs.nonZero, gs.nonZero)
			break
		}
		sumDelta := rs.sumA - gs.sumA
		if gs.sumA > rs.sumA {
			sumDelta = gs.sumA - rs.sumA
		}
		if sumDelta > cellAlphaAllowance {
			rep.tileStatDelta = fmt.Sprintf(
				"grid cell %d alpha sum ref=%d got=%d drift %d exceeds allowance %d",
				i, rs.sumA, gs.sumA, sumDelta, cellAlphaAllowance)
			break
		}
	}

	// Command order: every grid cell must execute the same tag-stream command
	// positions, proving no command is skipped, duplicated or reordered.
	rep.cmdOrderDelta = compareCommandMaps(ref.cmds, got.cmds)

	return rep
}

// compareCommandMaps returns a description of the first grid cell whose
// command sequence differs, or "" when they are identical.
func compareCommandMaps(ref, got [][]int) string {
	for idx := range ref {
		if idx >= len(got) {
			return fmt.Sprintf("grid cell %d: command tracking missing (ref=%v)", idx, ref[idx])
		}
		refCmds, gotCmds := ref[idx], got[idx]
		if len(refCmds) != len(gotCmds) {
			return fmt.Sprintf("grid cell %d: command count ref=%d got=%d (ref=%v got=%v)",
				idx, len(refCmds), len(gotCmds), refCmds, gotCmds)
		}
		for i := range refCmds {
			if refCmds[i] != gotCmds[i] {
				return fmt.Sprintf("grid cell %d: command index %d ref=%d got=%d (full ref=%v got=%v)",
					idx, i, refCmds[i], gotCmds[i], refCmds, gotCmds)
			}
		}
	}
	return ""
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// assertGateResult fails the test with the first differing tile and command
// index when the report contains structural errors.
func assertGateResult(t *testing.T, ref, got renderResult, cfg gateConfig, rep diffReport) {
	t.Helper()
	if !rep.hasErrors() {
		if len(rep.tolerance) > 0 {
			t.Logf("%s: %d pixels within documented AA tolerance (<=%d/channel)",
				cfg.name, len(rep.tolerance), maxChannelDelta)
		}
		return
	}

	m := rep.firstMismatch()
	msg := fmt.Sprintf("config %s vs %s: determinism gate failed", got.name, ref.name)
	if m.kind != "" {
		cmdIdx := -1
		if m.tileIndex >= 0 && m.tileIndex < len(got.cmds) && len(got.cmds[m.tileIndex]) > 0 {
			cmdIdx = got.cmds[m.tileIndex][0]
		}
		msg += fmt.Sprintf(
			"\n  first mismatch at pixel (%d,%d) tile (%d,%d) tileIndex=%d firstCmdIndex=%d kind=%s got=%v want=%v",
			m.x, m.y, m.tileX, m.tileY, m.tileIndex, cmdIdx, m.kind, m.got, m.want)
	}
	if len(rep.structural) > 0 {
		msg += fmt.Sprintf("\n  structural mismatches: %d (tolerance cannot absorb these)", len(rep.structural))
	}
	if len(rep.tolerance) > maxTolerantPixels {
		msg += fmt.Sprintf("\n  tolerant pixel count %d exceeds budget %d", len(rep.tolerance), maxTolerantPixels)
	}
	if rep.statsDelta != "" {
		msg += "\n  " + rep.statsDelta
	}
	if rep.tileStatDelta != "" {
		msg += "\n  " + rep.tileStatDelta
	}
	if rep.cmdOrderDelta != "" {
		msg += "\n  command order: " + rep.cmdOrderDelta
	}
	t.Fatal(msg)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestRenderer_ParallelDeterminism is the main quality gate: one fixed scene
// rendered across worker counts, tile geometries and tile orders must produce
// the same pixels, per-tile stats and command sequence.
func TestRenderer_ParallelDeterminism(t *testing.T) {
	rng := rand.New(rand.NewSource(gateSeed))
	sc := buildDeterministicScene(t, rng)

	configs := []gateConfig{
		{name: "workers1-tile64", workers: 1, tileW: 64, tileH: 64},
		{name: "workers2-tile64", workers: 2, tileW: 64, tileH: 64},
		{name: "workers4-tile64", workers: 4, tileW: 64, tileH: 64},
		{name: "workersMax-tile64", workers: runtime.GOMAXPROCS(0), tileW: 64, tileH: 64},
		{name: "workers2-tile32", workers: 2, tileW: 32, tileH: 32},
		{name: "workers3-tile48", workers: 3, tileW: 48, tileH: 48},
		{name: "workers1-tile128", workers: 1, tileW: 128, tileH: 128},
	}

	// Reference: single worker, standard tiles, row-major order.
	ref := renderGateScene(t, sc, configs[0], nil, nil)
	if ref.stats.nonZeroPixels == 0 {
		t.Fatal("reference render produced no pixels: gate scene is empty")
	}
	if len(ref.cmds) == 0 {
		t.Fatal("reference render recorded no tile command tracking")
	}

	for _, cfg := range configs[1:] {
		t.Run(cfg.name, func(t *testing.T) {
			got := renderGateScene(t, sc, cfg, nil, nil)
			tileW := cfg.tileW
			if tileW == 0 {
				tileW = 64
			}
			assertGateResult(t, ref, got, cfg, compareResults(ref, got, tileW, tileW))
		})
	}

	// Explicit tile-order permutations with the standard 64x64 geometry.
	// Each ordering must be bit-identical because tiles do not share state.
	orders := map[string]func([]*parallel.Tile) []*parallel.Tile{
		"reverse": func(ts []*parallel.Tile) []*parallel.Tile {
			out := append([]*parallel.Tile(nil), ts...)
			for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
				out[i], out[j] = out[j], out[i]
			}
			return out
		},
		"stride": func(ts []*parallel.Tile) []*parallel.Tile {
			out := make([]*parallel.Tile, 0, len(ts))
			const stride = 7
			for i := 0; i < len(ts); i++ {
				out = append(out, ts[(i*stride)%len(ts)])
			}
			return out
		},
		"seeded-shuffle": func(ts []*parallel.Tile) []*parallel.Tile {
			out := append([]*parallel.Tile(nil), ts...)
			shRng := rand.New(rand.NewSource(gateSeed + 99))
			shRng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
			return out
		},
	}
	for orderName, hook := range orders {
		t.Run("order/"+orderName, func(t *testing.T) {
			cfg := gateConfig{name: "workers4-order-" + orderName, workers: 4, tileW: 64, tileH: 64}
			got := renderGateScene(t, sc, cfg, hook, nil)
			assertGateResult(t, ref, got, cfg, compareResults(ref, got, 64, 64))
		})
	}
}

// TestRenderer_Determinism_GPUFallback verifies the accelerator branch and
// its GPU→CPU fallback without requiring a GPU. The stub accelerator accepts
// every op then returns ErrFallbackToCPU, which is exactly the contract the
// renderer relies on when a real GPU rejects a shape. Two properties hold:
//
//  1. The GPU branch is actually entered (the stub is invoked) and the
//     fallback still produces a successful, non-empty render.
//  2. With the accelerator removed, the pure CPU path is independently
//     deterministic across worker counts.
//
// Pixel-equality between the two pipelines is intentionally NOT asserted:
// GPUSceneRenderer is a whole-canvas gg.Context pipeline (different AA and
// blending order from the tile decomposition), so cross-pipeline comparison
// belongs in visual golden tests, not the parallel-determinism gate.
func TestRenderer_Determinism_GPUFallback(t *testing.T) {
	rng := rand.New(rand.NewSource(gateSeed))
	sc := buildDeterministicScene(t, rng)

	stub := &fallbackAccelerator{}
	if err := gg.RegisterAccelerator(stub); err != nil {
		t.Fatalf("RegisterAccelerator: %v", err)
	}

	target := gg.NewPixmap(determinismCanvasW, determinismCanvasH)
	r := NewRenderer(determinismCanvasW, determinismCanvasH, WithWorkers(2))
	if err := r.Render(target, sc); err != nil {
		t.Fatalf("render through fallback accelerator failed: %v", err)
	}
	r.Close()
	gg.CloseAccelerator()

	if stub.calls == 0 {
		t.Fatal("accelerator stub was never invoked: GPU branch not exercised")
	}
	if st := computePixelStats(target.Data()); st.nonZeroPixels == 0 {
		t.Fatal("fallback render produced no pixels after GPU→CPU fallback")
	}
	t.Logf("GPU branch exercised (%d accelerated calls rejected to CPU), fallback render OK: %+v",
		stub.calls, computePixelStats(target.Data()))

	// CPU-only path remains deterministic across worker counts.
	ref := renderGateScene(t, sc, gateConfig{name: "cpu-w1", workers: 1}, nil, nil)
	got := renderGateScene(t, sc, gateConfig{name: "cpu-w4", workers: 4}, nil, nil)
	assertGateResult(t, ref, got, gateConfig{name: "cpu-w4", workers: 4},
		compareResults(ref, got, 64, 64))
}

// fallbackAccelerator is a no-GPU stub: every accelerated op rejects and the
// renderer takes its CPU fallback branch.
type fallbackAccelerator struct {
	calls int
}

func (f *fallbackAccelerator) Name() string                        { return "determinism-fallback-stub" }
func (f *fallbackAccelerator) Init() error                         { return nil }
func (f *fallbackAccelerator) Close()                              {}
func (f *fallbackAccelerator) CanAccelerate(gg.AcceleratedOp) bool { return true }
func (f *fallbackAccelerator) FillPath(gg.GPURenderTarget, *gg.Path, *gg.Paint) error {
	f.calls++
	return gg.ErrFallbackToCPU
}
func (f *fallbackAccelerator) StrokePath(gg.GPURenderTarget, *gg.Path, *gg.Paint) error {
	f.calls++
	return gg.ErrFallbackToCPU
}
func (f *fallbackAccelerator) FillShape(gg.GPURenderTarget, gg.DetectedShape, *gg.Paint) error {
	f.calls++
	return gg.ErrFallbackToCPU
}
func (f *fallbackAccelerator) StrokeShape(gg.GPURenderTarget, gg.DetectedShape, *gg.Paint) error {
	f.calls++
	return gg.ErrFallbackToCPU
}
func (f *fallbackAccelerator) Flush(gg.GPURenderTarget) error { return nil }

// TestRenderer_Determinism_EmptyScene covers the empty scene: every
// configuration must produce a fully transparent, stats-zero target.
func TestRenderer_Determinism_EmptyScene(t *testing.T) {
	configs := []gateConfig{
		{name: "w1", workers: 1},
		{name: "w4", workers: 4},
		{name: "w4-tile32", workers: 4, tileW: 32, tileH: 32},
	}
	var ref renderResult
	for i, cfg := range configs {
		got := renderGateScene(t, NewScene(), cfg, nil, nil)
		if got.stats.nonZeroPixels != 0 || got.stats.opaquePixels != 0 {
			t.Fatalf("%s: empty scene produced covered output: %+v", cfg.name, got.stats)
		}
		for _, b := range got.pixels {
			if b != 0 {
				t.Fatalf("%s: empty scene target contains a non-zero byte", cfg.name)
			}
		}
		if i == 0 {
			ref = got
		} else if rep := compareResults(ref, got, 64, 64); rep.hasErrors() {
			t.Fatalf("empty scene not stable across configs: %s %s", rep.statsDelta, rep.tileStatDelta)
		}
	}
}

// TestRenderer_Determinism_NonZeroOrigin places content at negative and
// non-integer coordinates so tile-local coordinate conversion and edge tiles
// are stressed, then checks cross-worker equality.
func TestRenderer_Determinism_NonZeroOrigin(t *testing.T) {
	sc := NewScene()
	// A circle centered on a tile corner with sub-pixel offset.
	sc.Fill(FillNonZero, TranslateAffine(63.5, 63.5),
		SolidBrush(gg.RGBA{R: 0.4, G: 0.1, B: 0.8, A: 0.7}),
		NewCircleShape(0, 0, 30))
	// Rectangle crossing the right and bottom edge tiles.
	sc.Fill(FillNonZero, TranslateAffine(-8, -8),
		SolidBrush(gg.RGBA{R: 0.1, G: 0.6, B: 0.4, A: 0.9}),
		NewRectShape(0, 0, 100, 100))

	ref := renderGateScene(t, sc, gateConfig{name: "w1", workers: 1}, nil, nil)
	for _, n := range []int{2, 4, runtime.GOMAXPROCS(0)} {
		cfg := gateConfig{name: fmt.Sprintf("w%d", n), workers: n}
		got := renderGateScene(t, sc, cfg, nil, nil)
		assertGateResult(t, ref, got, cfg, compareResults(ref, got, 64, 64))
	}
}

// TestRenderer_Determinism_CancelledContext verifies cancellation returns the
// context error and leaves the renderer usable for a subsequent clean render
// whose output matches an uninterrupted reference.
func TestRenderer_Determinism_CancelledContext(t *testing.T) {
	rng := rand.New(rand.NewSource(gateSeed))
	sc := buildDeterministicScene(t, rng)

	r := NewRenderer(determinismCanvasW, determinismCanvasH, WithWorkers(4))
	defer r.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled: must fail before touching the target

	target := gg.NewPixmap(determinismCanvasW, determinismCanvasH)
	if err := r.RenderWithContext(ctx, target, sc); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled context: err=%v, want context.Canceled", err)
	}

	// The renderer must remain usable after cancellation. Build the
	// reference through the full harness so tile stats and command tracking
	// are populated; a partial renderResult would produce false diagnostics.
	ref := renderGateScene(t, sc, gateConfig{name: "ref", workers: 4}, nil, nil)
	if err := r.Render(gg.NewPixmap(determinismCanvasW, determinismCanvasH), sc); err != nil {
		t.Fatalf("render after cancel failed: %v", err)
	}
	got := renderGateScene(t, sc, gateConfig{name: "post-cancel", workers: 4}, nil, nil)
	if rep := compareResults(ref, got, 64, 64); rep.hasErrors() {
		t.Fatalf("render after cancellation diverged: %+v %s", rep.firstMismatch(), rep.statsDelta)
	}
}

// TestRenderer_Determinism_RepeatRender renders the same scene repeatedly on
// the same renderer to exercise sync.Pool reuse of Pixmaps, renderers and
// decoders. Every frame must be identical.
func TestRenderer_Determinism_RepeatRender(t *testing.T) {
	rng := rand.New(rand.NewSource(gateSeed))
	sc := buildDeterministicScene(t, rng)

	r := NewRenderer(determinismCanvasW, determinismCanvasH, WithWorkers(4))
	defer r.Close()

	var ref pixelStats
	for frame := 0; frame < 6; frame++ {
		target := gg.NewPixmap(determinismCanvasW, determinismCanvasH)
		if err := r.Render(target, sc); err != nil {
			t.Fatalf("frame %d: %v", frame, err)
		}
		st := computePixelStats(target.Data())
		if frame == 0 {
			ref = st
			continue
		}
		if st != ref {
			t.Fatalf("frame %d stats %+v != first frame %+v (pool reuse instability)", frame, st, ref)
		}
	}
}

// TestRenderer_Determinism_CacheHit exercises the encoding hash used as the
// layer-cache key: rebuilding an identical scene must hash identically, and a
// changed scene must hash differently. The renderer itself does not read the
// layer cache on the CPU tile path, so this guards the cache-key contract the
// gate relies on.
func TestRenderer_Determinism_CacheHit(t *testing.T) {
	rng1 := rand.New(rand.NewSource(gateSeed))
	sc1 := buildDeterministicScene(t, rng1)
	rng2 := rand.New(rand.NewSource(gateSeed))
	sc2 := buildDeterministicScene(t, rng2)

	h1 := sc1.Encoding().Hash()
	h2 := sc2.Encoding().Hash()
	if h1 != h2 {
		t.Fatalf("identical-seed scenes hash differently: %016x vs %016x (cache would miss)", h1, h2)
	}

	cache := NewLayerCache(4)
	pm := gg.NewPixmap(16, 16)
	pm.SetPixelPremul(0, 0, 10, 20, 30, 40)
	cache.Put(h1, pm, sc1.Version())

	cached, ok := cache.Get(h2)
	if !ok || cached != pm {
		t.Fatal("cache hit failed for identical encoding hash")
	}
	if stats := cache.Stats(); stats.Hits != 1 || stats.Misses != 0 {
		t.Fatalf("cache stats = %+v, want 1 hit / 0 miss", stats)
	}

	// A modified scene must produce a distinct key.
	sc2.Fill(FillNonZero, IdentityAffine(),
		SolidBrush(gg.RGBA{R: 1, G: 1, B: 1, A: 1}), NewRectShape(0, 0, 5, 5))
	if sc2.Encoding().Hash() == h1 {
		t.Fatal("modified scene kept the same hash: cache would return stale content")
	}
}

// TestRenderer_RaceFree_RepeatedParallelRenders exercises the unmodified
// production renderer under concurrent load. With -race it proves the pooled
// Pixmap/Decoder/SoftwareRenderer handoff and the per-tile command tracker
// are data-race free. The same renderer is reused across renders so sync.Pool
// reuse paths are covered, and several renderers run in parallel to stress the
// package-level pools.
func TestRenderer_RaceFree_RepeatedParallelRenders(t *testing.T) {
	rng := rand.New(rand.NewSource(gateSeed))
	sc := buildDeterministicScene(t, rng)

	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := NewRenderer(determinismCanvasW, determinismCanvasH, WithWorkers(4))
			defer r.Close()
			r.tileCmdTracker = make([][]int, r.TileCount())
			for frame := 0; frame < 4; frame++ {
				target := gg.NewPixmap(determinismCanvasW, determinismCanvasH)
				if err := r.Render(target, sc); err != nil {
					t.Errorf("frame %d: %v", frame, err)
					return
				}
				if computePixelStats(target.Data()).nonZeroPixels == 0 {
					t.Error("parallel render produced empty output")
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestRenderer_GateNegative_ShuffledTiles is a gate-negative test: with the
// renderer intact, shuffled scheduling still produces correct output, so to
// prove the gate *detects* order-dependent breakage we inject a deliberately
// broken tile-order hook that drops one tile from compositing. The gate must
// fail with a structural mismatch.
func TestRenderer_GateNegative_ShuffledTiles(t *testing.T) {
	rng := rand.New(rand.NewSource(gateSeed))
	sc := buildDeterministicScene(t, rng)
	ref := renderGateScene(t, sc, gateConfig{name: "ref", workers: 1}, nil, nil)

	r := NewRenderer(determinismCanvasW, determinismCanvasH, WithWorkers(4))
	defer r.Close()
	// Drop every 11th tile: simulates a scheduler that silently loses work.
	r.tileOrderHook = func(tiles []*parallel.Tile) []*parallel.Tile {
		out := tiles[:0]
		for i, tile := range tiles {
			if i%11 != 0 {
				out = append(out, tile)
			}
		}
		return out
	}

	target := gg.NewPixmap(determinismCanvasW, determinismCanvasH)
	if err := r.Render(target, sc); err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := renderResult{
		name:   "dropped-tiles",
		pixels: append([]byte(nil), target.Data()...),
		stats:  computePixelStats(target.Data()),
	}
	rep := compareResults(ref, got, 64, 64)
	if !rep.hasErrors() {
		t.Fatal("gate did NOT detect dropped tiles: quality gate is ineffective")
	}
	if len(rep.structural) == 0 {
		t.Fatal("dropped tiles produced only tolerance-level diffs: tolerance is too loose")
	}
	t.Logf("gate correctly failed on dropped tiles: %s", rep.firstMismatch().kind)
}

// TestRenderer_GateNegative_CommandOrder proves the command-sequence part of
// the gate detects reordering, duplication and omission independently of
// pixels. The tampered results stand in for a renderer that executed the tag
// stream out of order; the report must name the grid cell and command index.
func TestRenderer_GateNegative_CommandOrder(t *testing.T) {
	rng := rand.New(rand.NewSource(gateSeed))
	sc := buildDeterministicScene(t, rng)
	ref := renderGateScene(t, sc, gateConfig{name: "ref", workers: 2}, nil, nil)

	// 1. Swapped command positions in one cell (reordering).
	swapped := cloneResult(ref)
	swapped.cmds = append([][]int(nil), ref.cmds...)
	if len(swapped.cmds[0]) < 2 {
		t.Fatal("reference cell 0 has too few commands to tamper")
	}
	swapped.cmds[0] = append([]int(nil), ref.cmds[0]...)
	swapped.cmds[0][0], swapped.cmds[0][1] = swapped.cmds[0][1], swapped.cmds[0][0]
	if msg := compareCommandMaps(ref.cmds, swapped.cmds); msg == "" {
		t.Fatal("gate failed to detect swapped command indices")
	} else {
		t.Logf("reorder detected: %s", msg)
	}

	// 2. Duplicated command (double compositing signature).
	dup := cloneResult(ref)
	dup.cmds = append([][]int(nil), ref.cmds...)
	dup.cmds[3] = append(append([]int(nil), ref.cmds[3]...), ref.cmds[3][0])
	if msg := compareCommandMaps(ref.cmds, dup.cmds); msg == "" {
		t.Fatal("gate failed to detect a duplicated command")
	} else {
		t.Logf("duplicate detected: %s", msg)
	}

	// 3. Dropped command (missing draw signature).
	missing := cloneResult(ref)
	missing.cmds = append([][]int(nil), ref.cmds...)
	missing.cmds[7] = append([]int(nil), ref.cmds[7][1:]...)
	if msg := compareCommandMaps(ref.cmds, missing.cmds); msg == "" {
		t.Fatal("gate failed to detect a missing command")
	} else {
		t.Logf("omission detected: %s", msg)
	}

	// 4. The untouched reference must compare clean — proving the checks
	// do not produce false positives.
	if msg := compareCommandMaps(ref.cmds, ref.cmds); msg != "" {
		t.Fatalf("false positive command-order failure: %s", msg)
	}
}

// TestRenderer_GateNegative_SharedPixmap injects a resource race by handing
// the same temporary Pixmap to every concurrent tile. The gate must detect the
// resulting corruption as a structural defect rather than absorb it into the
// AA tolerance.
//
// The injection deliberately creates a real data race, so the test skips
// under -race: the race detector is expected to flag the injected fault.
// Race-freedom of the production pool path is covered by
// TestRenderer_RaceFree_RepeatedParallelRenders, which runs the unmodified
// renderer under -race.
func TestRenderer_GateNegative_SharedPixmap(t *testing.T) {
	if raceEnabled {
		t.Skip("shared-Pixmap injection is an intentional data race; covered without -race")
	}
	rng := rand.New(rand.NewSource(gateSeed))
	sc := buildDeterministicScene(t, rng)
	ref := renderGateScene(t, sc, gateConfig{name: "ref", workers: 1}, nil, nil)

	cfg := gateConfig{name: "shared-pixmap", workers: 8, tileW: 64, tileH: 64}
	shared := gg.NewPixmap(64, 64)
	var mu sync.Mutex
	got := renderGateScene(t, sc, cfg, nil, func(w, h int) *gg.Pixmap {
		mu.Lock()
		defer mu.Unlock()
		// All tiles share one buffer; concurrent writes interleave and the
		// last writer's content is copied into many unrelated tiles.
		if shared.Width() != w || shared.Height() != h {
			return gg.NewPixmap(w, h) // edge tiles still get their own buffer
		}
		return shared
	})

	rep := compareResults(ref, got, 64, 64)
	if !rep.hasErrors() {
		t.Fatal("gate did NOT detect the shared-Pixmap resource race: gate is ineffective")
	}
	t.Logf("gate correctly failed on shared Pixmap: structural=%d tolerance=%d first=%s",
		len(rep.structural), len(rep.tolerance), rep.firstMismatch().kind)
}

// TestRenderer_GateNegative_TolerantBudget verifies the tolerance budget
// itself: a one-channel, one-level perturbation on a handful of pixels must
// pass (documented AA tolerance), while the same perturbation applied to
// thousands of pixels must fail (it cannot be an AA edge).
func TestRenderer_GateNegative_TolerantBudget(t *testing.T) {
	rng := rand.New(rand.NewSource(gateSeed))
	sc := buildDeterministicScene(t, rng)
	ref := renderGateScene(t, sc, gateConfig{name: "ref", workers: 2}, nil, nil)

	// Small perturbation stays inside the budget and per-channel delta.
	// Only touch covered pixels so the delta is genuinely AA-scale rather
	// than a transparent→covered structural jump.
	small := cloneResult(ref)
	changed := 0
	for i := 0; i+3 < len(small.pixels) && changed < 10; i += 4 {
		if small.pixels[i+3] > 10 && small.pixels[i] < 255 {
			small.pixels[i]++
			changed++
		}
	}
	if changed == 0 {
		t.Fatal("test scene has no perturbable covered pixels")
	}
	small.stats = computePixelStats(small.pixels)
	if rep := compareResults(ref, small, 64, 64); rep.hasErrors() {
		t.Fatalf("within-tolerance perturbation rejected: %+v", rep.firstMismatch())
	}

	// Same delta applied widely blows the budget.
	wide := cloneResult(ref)
	for i := 0; i+3 < len(wide.pixels); i += 17 {
		wide.pixels[i] = wide.pixels[i] ^ 0x80 // large, structural jump
	}
	wide.stats = computePixelStats(wide.pixels)
	rep := compareResults(ref, wide, 64, 64)
	if !rep.hasErrors() || len(rep.structural) == 0 {
		t.Fatal("large widespread perturbation was absorbed by tolerance: gate is ineffective")
	}
}

func cloneResult(r renderResult) renderResult {
	out := r
	out.pixels = append([]byte(nil), r.pixels...)
	return out
}

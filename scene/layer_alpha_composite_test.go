package scene

import (
	"context"
	"image"
	"math"
	"testing"

	"github.com/gogpu/gg"
)

// ---------------------------------------------------------------------------
// Independent reference compositor
//
// The reference below deliberately does not reuse any production compositing
// code. It models the documented contract directly in float64 on straight
// (unpremultiplied) RGBA colors:
//
//   - A layer's pixels are premultiplied in the tile buffers. Layer alpha is
//     applied once on pop by scaling premultiplied RGBA, equivalent to
//     multiplying straight alpha (and leaving straight color unchanged).
//   - The scaled layer is then combined with its parent using source-over
//     (BlendNormal/BlendSourceOver), which for premultiplied storage is
//     out = src + dst*(1-srcA).
//   - Nested layers are resolved bottom-up before the parent alpha is applied.
//
// The renderer stores rounded bytes, so comparisons allow a small per-channel
// tolerance rather than requiring bit-exact agreement with the float model.
// ---------------------------------------------------------------------------

// refPixel is a straight-alpha RGBA color with channels normalized to [0,1].
type refPixel struct {
	r, g, b, a float64
}

// refScaleAlpha returns the pixel after a layer alpha has been applied.
// In straight-alpha space layer opacity only modulates alpha; the color
// channels are untouched.
func refScaleAlpha(p refPixel, alpha float64) refPixel {
	return refPixel{r: p.r, g: p.g, b: p.b, a: p.a * alpha}
}

// refSourceOver composites src over dst in straight-alpha space following the
// W3C source-over formula.
func refSourceOver(src, dst refPixel) refPixel {
	outA := src.a + dst.a*(1-src.a)
	if outA <= 0 {
		return refPixel{}
	}
	return refPixel{
		r: (src.r*src.a + dst.r*dst.a*(1-src.a)) / outA,
		g: (src.g*src.a + dst.g*dst.a*(1-src.a)) / outA,
		b: (src.b*src.a + dst.b*dst.a*(1-src.a)) / outA,
		a: outA,
	}
}

// refPremulSourceOver is the same source-over operation expressed on
// premultiplied 0..255 bytes, matching how the tile renderer stores pixels.
// Rounding uses round-half-up on (value*alpha) to mirror clamp255Byte.
func refPremulSourceOver(sr, sg, sb, sa, dr, dg, db, da int, layerAlpha float64) (int, int, int, int) {
	// Apply the layer alpha once to all premultiplied channels.
	ssa := clampInt(int(math.Round(float64(sa)*layerAlpha)), 0, 255)
	ssr := clampInt(int(math.Round(float64(sr)*layerAlpha)), 0, ssa) // premultiplied invariant: color <= alpha
	ssg := clampInt(int(math.Round(float64(sg)*layerAlpha)), 0, ssa)
	ssb := clampInt(int(math.Round(float64(sb)*layerAlpha)), 0, ssa)

	inv := float64(255-ssa) / 255.0
	out := func(s, d int) int {
		v := float64(s) + float64(d)*inv
		return clampInt(int(math.Round(v)), 0, 255)
	}
	return out(ssr, dr), out(ssg, dg), out(ssb, db), out(ssa, da)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// pixelDiff is the maximum per-channel absolute difference across two pixmaps.
func pixelDiff(a, b *gg.Pixmap) (maxDiff int, mismatchAt [2]int, found bool) {
	ad, bd := a.Data(), b.Data()
	n := len(ad)
	if len(bd) < n {
		n = len(bd)
	}
	w := a.Width()
	for i := 0; i+3 < n; i += 4 {
		for k := 0; k < 4; k++ {
			d := int(ad[i+k]) - int(bd[i+k])
			if d < 0 {
				d = -d
			}
			if d > maxDiff {
				maxDiff = d
				mismatchAt = [2]int{(i / 4) % w, (i / 4) / w}
				found = true
			}
		}
	}
	return maxDiff, mismatchAt, found
}

// renderCPU renders a scene with a fresh CPU-only renderer (no accelerator).
func renderCPU(t *testing.T, w, h int, scene *Scene) *gg.Pixmap {
	t.Helper()
	if gg.Accelerator() != nil {
		gg.CloseAccelerator()
	}
	target := gg.NewPixmap(w, h)
	r := NewRenderer(w, h)
	if r == nil {
		t.Fatalf("NewRenderer(%d,%d) returned nil", w, h)
	}
	defer r.Close()
	if err := r.Render(target, scene); err != nil {
		t.Fatalf("Render: %v", err)
	}
	return target
}

// ---------------------------------------------------------------------------
// Sanitization rules (requirement 2)
// ---------------------------------------------------------------------------

func TestSanitizeLayerAlpha_Rules(t *testing.T) {
	nan := float32(math.NaN())
	tests := []struct {
		name string
		in   float32
		want float32
	}{
		{"zero", 0, 0},
		{"one", 1, 1},
		{"half", 0.5, 0.5},
		{"negative", -0.25, 0},
		{"negative infinity", float32(math.Inf(-1)), 0},
		{"above one", 1.5, 1},
		{"positive infinity", float32(math.Inf(1)), 1},
		{"NaN", nan, 0},
		{"smallest non-zero", math.SmallestNonzeroFloat32, math.SmallestNonzeroFloat32},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeLayerAlpha(tt.in)
			if math.IsNaN(float64(got)) || got != tt.want {
				t.Errorf("sanitizeLayerAlpha(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestLayerAlpha_EncodingIsSanitized verifies Scene.PushLayer never emits a
// NaN/Inf/out-of-range alpha into the command stream.
func TestLayerAlpha_EncodingIsSanitized(t *testing.T) {
	tests := []struct {
		name  string
		alpha float32
		want  float32
	}{
		{"nan", float32(math.NaN()), 0},
		{"+inf", float32(math.Inf(1)), 1},
		{"-inf", float32(math.Inf(-1)), 0},
		{"negative", -2, 0},
		{"too large", 2, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewScene()
			s.PushLayer(BlendNormal, tt.alpha, nil)
			s.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.RGBA{R: 1, A: 1}), NewRectShape(0, 0, 10, 10))
			s.PopLayer()

			dec := NewDecoder(s.Encoding())
			found := false
			for dec.Next() {
				if dec.Tag() == TagPushLayer {
					found = true
					_, alpha := dec.PushLayer()
					if math.IsNaN(float64(alpha)) || alpha != tt.want {
						t.Errorf("encoded alpha = %v, want %v", alpha, tt.want)
					}
				}
			}
			if !found {
				t.Fatal("no TagPushLayer found in encoding")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Encoding structure: balanced push/pop across nesting + pool reuse (req. 3)
// ---------------------------------------------------------------------------

// TestLayerEncoding_BalancedPushPop verifies the flattened tag stream keeps
// every PushLayer paired with exactly one PopLayer, including nested layers.
// Previously the push marker was written into the parent encoding while the
// pop landed in the child's, leaving unbalanced streams after flattening.
func TestLayerEncoding_BalancedPushPop(t *testing.T) {
	for _, depth := range []int{1, 2, 3, 5} {
		name := "depth" + itoa(depth)
		t.Run(name, func(t *testing.T) {
			s := NewScene()
			for range depth {
				s.PushLayer(BlendNormal, 0.5, nil)
				s.Fill(FillNonZero, IdentityAffine(),
					SolidBrush(gg.RGBA{R: 1, A: 1}), NewRectShape(0, 0, 10, 10))
			}
			for range depth {
				if !s.PopLayer() {
					t.Fatal("PopLayer returned false before stack drained")
				}
			}

			pushes, pops, maxDepth := 0, 0, 0
			cur := 0
			dec := NewDecoder(s.Encoding())
			for dec.Next() {
				switch dec.Tag() {
				case TagPushLayer:
					dec.PushLayer()
					pushes++
					cur++
					if cur > maxDepth {
						maxDepth = cur
					}
				case TagPopLayer:
					pops++
					cur--
					if cur < 0 {
						t.Fatal("pop without a matching push in flattened stream")
					}
				case TagFill, TagFillRoundRect:
					dec.Fill() // advance the decoder's draw stream
				}
			}
			if pushes != depth || pops != depth {
				t.Errorf("push/pop = %d/%d, want %d/%d", pushes, pops, depth, depth)
			}
			if cur != 0 {
				t.Errorf("unbalanced stream, residual depth %d", cur)
			}
			if maxDepth != depth {
				t.Errorf("max nesting depth = %d, want %d", maxDepth, depth)
			}
		})
	}
}

// TestLayerEncoding_PoolReuseDoesNotCorruptParent guards the backing-array
// aliasing bug: an appended child encoding was returned to the pool and its
// shared arrays overwritten by the next layer, corrupting earlier layer data
// (observed as a nested layer's alpha silently becoming 0).
func TestLayerEncoding_PoolReuseDoesNotCorruptParent(t *testing.T) {
	s := NewScene()
	alphas := []float32{0.3, 0.5, 0.7}
	for _, a := range alphas {
		s.PushLayer(BlendNormal, a, nil)
		s.Fill(FillNonZero, IdentityAffine(),
			SolidBrush(gg.RGBA{R: 1, A: 1}), NewRectShape(0, 0, 10, 10))
	}
	for range alphas {
		s.PopLayer()
	}

	// decodePushAlphas walks a full encoding, consuming every tag's data so
	// the decoder's internal stream indices stay aligned, and returns the
	// alpha recorded by each PushLayer in stream order.
	decodePushAlphas := func(enc *Encoding) []float32 {
		var out []float32
		d := NewDecoder(enc)
		for d.Next() {
			switch d.Tag() {
			case TagPushLayer:
				_, a := d.PushLayer()
				out = append(out, a)
			case TagFill:
				d.Fill()
			case TagFillRoundRect:
				d.FillRoundRect()
			case TagStroke:
				d.Stroke()
			case TagImage:
				d.Image()
			case TagText:
				d.Text()
			case TagBrush:
				d.Brush()
			case TagSetAntiAlias:
				d.AntiAlias()
			case TagTransform:
				d.Transform()
			case TagMoveTo:
				d.MoveTo()
			case TagLineTo:
				d.LineTo()
			case TagQuadTo:
				d.QuadTo()
			case TagCubicTo:
				d.CubicTo()
			}
		}
		return out
	}

	got := decodePushAlphas(s.Encoding())
	if len(got) != len(alphas) {
		t.Fatalf("encoded %d push alphas, want %d", len(got), len(alphas))
	}
	for i := range alphas {
		if got[i] != alphas[i] {
			t.Errorf("alpha[%d] = %v, want %v (parent stream corrupted by pool reuse)",
				i, got[i], alphas[i])
		}
	}

	// A second independent scene built after the first must also be intact,
	// proving pooled layers carry no stale/aliased state across scenes.
	s2 := NewScene()
	s2.PushLayer(BlendNormal, 0.25, nil)
	s2.Fill(FillNonZero, IdentityAffine(),
		SolidBrush(gg.RGBA{B: 1, A: 1}), NewRectShape(0, 0, 10, 10))
	s2.PopLayer()
	if got2 := decodePushAlphas(s2.Encoding()); len(got2) != 1 || got2[0] != 0.25 {
		t.Errorf("second-scene push alphas = %v, want [0.25]", got2)
	}
}

// ---------------------------------------------------------------------------
// Red/green translucent squares against an independent reference (req. 1 & 4)
// ---------------------------------------------------------------------------

func TestLayerAlpha_RedGreenSquares_MatchReference(t *testing.T) {
	// Background: opaque white. Bottom layer content: opaque red square.
	// A PushLayer at alpha 0.5 wraps an opaque green square overlapping the
	// red one. The green must composite at half group opacity.
	const (
		w, h   = 128, 128
		redA   = 0.5
		greenA = 0.25
	)

	build := func() *Scene {
		s := NewScene()
		// Opaque white background.
		s.Fill(FillNonZero, IdentityAffine(),
			SolidBrush(gg.RGBA{R: 1, G: 1, B: 1, A: 1}),
			NewRectShape(0, 0, w, h))
		// Opaque red base square (not in a layer).
		s.Fill(FillNonZero, IdentityAffine(),
			SolidBrush(gg.RGBA{R: 1, G: 0, B: 0, A: 1}),
			NewRectShape(10, 10, 60, 60))

		// Green square inside an alpha layer, partially overlapping red.
		s.PushLayer(BlendNormal, greenA, nil)
		s.Fill(FillNonZero, IdentityAffine(),
			SolidBrush(gg.RGBA{R: 0, G: 1, B: 0, A: 1}),
			NewRectShape(40, 40, 60, 60))
		s.PopLayer()

		// Red translucent layer drawn last, overlapping both regions.
		s.PushLayer(BlendNormal, redA, nil)
		s.Fill(FillNonZero, IdentityAffine(),
			SolidBrush(gg.RGBA{R: 1, G: 0, B: 0, A: 1}),
			NewRectShape(20, 70, 50, 40))
		s.PopLayer()
		return s
	}

	target := renderCPU(t, w, h, build())

	// Reference expectations at representative interior pixels.
	white := refPixel{r: 1, g: 1, b: 1, a: 1}
	red := refPixel{r: 1, g: 0, b: 0, a: 1}
	green := refPixel{r: 0, g: 1, b: 0, a: 1}

	// (20,20): red only over white.
	wantRedOverWhite := refSourceOver(red, white)
	// (50,50): green layer at alpha 0.25 over red over white.
	greenLayer := refScaleAlpha(green, greenA)
	wantOverlap := refSourceOver(greenLayer, refSourceOver(red, white))
	// (30,80): red layer at alpha 0.5 over white.
	redLayer := refScaleAlpha(red, redA)
	wantTranslucentRed := refSourceOver(redLayer, white)
	// (120,120): background untouched.
	wantBackground := white

	cases := []struct {
		name string
		x, y int
		want refPixel
	}{
		{"red interior", 20, 20, wantRedOverWhite},
		{"green/red overlap", 50, 50, wantOverlap},
		{"translucent red layer", 30, 80, wantTranslucentRed},
		{"untouched background", 120, 120, wantBackground},
	}
	const tolerance = 3 // byte rounding across premultiplied storage
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := target.GetPixel(tc.x, tc.y)
			for ch, gv := range [4]float64{got.R, got.G, got.B, got.A} {
				var wv float64
				switch ch {
				case 0:
					wv = tc.want.r
				case 1:
					wv = tc.want.g
				case 2:
					wv = tc.want.b
				default:
					wv = tc.want.a
				}
				d := gv - wv
				if d < 0 {
					d = -d
				}
				if d*255 > float64(tolerance) {
					t.Errorf("pixel(%d,%d) channel %d = %.4f, want %.4f (tol %d/255)",
						tc.x, tc.y, ch, gv, wv, tolerance)
				}
			}
		})
	}
}

// TestLayerAlpha_PremulFormula_Direct checks the renderer's premultiplied
// byte pipeline against refPremulSourceOver using the compositing helpers
// directly, including transparent edge pixels.
func TestLayerAlpha_PremulFormula_Direct(t *testing.T) {
	// Destination: 50% gray at 50% alpha, premultiplied.
	dr, dg, db, da := 64, 64, 64, 128
	// Source: opaque red, premultiplied (255,0,0,255), layer alpha 0.5.
	sr, sg, sb, sa := 255, 0, 0, 255
	const alpha = 0.5

	wr, wg, wb, wa := refPremulSourceOver(sr, sg, sb, sa, dr, dg, db, da, alpha)

	dst := gg.NewPixmap(1, 1)
	dstData := dst.Data()
	dstData[0], dstData[1], dstData[2], dstData[3] = byte(dr), byte(dg), byte(db), byte(da)
	src := gg.NewPixmap(1, 1)
	srcData := src.Data()
	srcData[0], srcData[1], srcData[2], srcData[3] = byte(sr), byte(sg), byte(sb), byte(sa)

	scalePremulAlpha(src, alpha)
	compositeLayerPixmaps(dst, src, BlendNormal)

	got := dst.Data()
	want := [4]byte{byte(wr), byte(wg), byte(wb), byte(wa)}
	for i := range want {
		if int(got[i]) < int(want[i])-1 || int(got[i]) > int(want[i])+1 {
			t.Fatalf("channel %d = %d, want %d±1", i, got[i], want[i])
		}
	}

	// Transparent edge pixel must remain transparent black after scaling
	// (premultiplied invariant), never gain color.
	edge := gg.NewPixmap(1, 1)
	scalePremulAlpha(edge, 0.7)
	if d := edge.Data(); d[0] != 0 || d[1] != 0 || d[2] != 0 || d[3] != 0 {
		t.Fatalf("transparent pixel mutated by alpha scale: %v", d)
	}
}

// ---------------------------------------------------------------------------
// Three-level nested layers (requirement 1)
// ---------------------------------------------------------------------------

func TestLayerAlpha_ThreeLevelNesting(t *testing.T) {
	// Nested structure:
	//   root (white)
	//   └─ layer A alpha 0.5
	//      └─ opaque blue square
	//      └─ layer B alpha 0.5
	//         └─ opaque green square
	//         └─ layer C alpha 0.5
	//            └─ opaque red square
	//
	// Effective alphas where all three squares overlap, evaluated as actual
	// layer pops (each pop scales the whole premultiplied group once):
	//   C pop: red@0.5 over green inside B's buffer
	//   B pop: that result @0.5 over blue inside A's buffer
	//   A pop: that result @0.5 over the transparent root
	build := func() *Scene {
		s := NewScene()
		s.PushLayer(BlendNormal, 0.5, nil) // A
		s.Fill(FillNonZero, IdentityAffine(),
			SolidBrush(gg.RGBA{R: 0, G: 0, B: 1, A: 1}),
			NewRectShape(0, 0, 100, 100))

		s.PushLayer(BlendNormal, 0.5, nil) // B
		s.Fill(FillNonZero, IdentityAffine(),
			SolidBrush(gg.RGBA{R: 0, G: 1, B: 0, A: 1}),
			NewRectShape(10, 10, 80, 80))

		s.PushLayer(BlendNormal, 0.5, nil) // C
		s.Fill(FillNonZero, IdentityAffine(),
			SolidBrush(gg.RGBA{R: 1, G: 0, B: 0, A: 1}),
			NewRectShape(20, 20, 60, 60))
		s.PopLayer() // C
		s.PopLayer() // B
		s.PopLayer() // A
		return s
	}

	target := renderCPU(t, 128, 128, build())

	blue := refPixel{r: 0, g: 0, b: 1, a: 1}
	green := refPixel{r: 0, g: 1, b: 0, a: 1}
	red := refPixel{r: 1, g: 0, b: 0, a: 1}
	transparent := refPixel{}

	// Mirror the renderer's pop sequence with the independent straight-alpha
	// reference instead of guessing "effective" per-fill alphas.
	//
	// C pop -> B content: red scaled 0.5, source-over the green fill.
	bAfterC := refSourceOver(refScaleAlpha(red, 0.5), green)
	// B pop -> A content: the B group scaled 0.5, source-over the blue fill.
	aAfterB := refSourceOver(refScaleAlpha(bAfterC, 0.5), blue)
	// A pop -> root: the A group scaled 0.5 over the transparent background.
	wantNested := refSourceOver(refScaleAlpha(aAfterB, 0.5), transparent)

	// A+B region only: B holds just the green fill.
	abAfterB := refSourceOver(refScaleAlpha(green, 0.5), blue)
	wantAB := refSourceOver(refScaleAlpha(abAfterB, 0.5), transparent)
	// A region only: blue fill, then the single 0.5 group pop.
	wantA := refScaleAlpha(blue, 0.5)

	const tolerance = 4
	cases := []struct {
		name string
		x, y int
		want refPixel
	}{
		{"all three nested", 50, 50, wantNested},
		{"A and B only", 15, 15, wantAB},
		{"A only", 5, 5, wantA},
		{"outside all", 120, 120, transparent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := target.GetPixel(tc.x, tc.y)
			gotv := [4]float64{got.R, got.G, got.B, got.A}
			wantv := [4]float64{tc.want.r, tc.want.g, tc.want.b, tc.want.a}
			for ch := range gotv {
				d := gotv[ch] - wantv[ch]
				if d < 0 {
					d = -d
				}
				if d*255 > float64(tolerance) {
					t.Errorf("pixel(%d,%d) ch%d = %.4f want %.4f", tc.x, tc.y, ch, gotv[ch], wantv[ch])
				}
			}
		})
	}
}

// TestLayerAlpha_NestingOrderMatters verifies that swapping which color sits
// in the outer layer versus the nested layer changes the result — a regression
// guard for the "stacking order changes the result" symptom, now driven by
// correct per-pop compositing. Both variants use identical alphas and the
// same geometry; only the layer assignment of the two colors differs.
func TestLayerAlpha_NestingOrderMatters(t *testing.T) {
	red := gg.RGBA{R: 1, A: 1}
	blue := gg.RGBA{B: 1, A: 1}
	rect := NewRectShape(20, 20, 60, 60)

	// Asymmetric layer alphas (outer 0.3, inner 0.7): with equal alphas the
	// two color swaps are mathematically identical for a single overlap, so
	// differing alphas are needed to make stacking order observable.
	build := func(outer, inner gg.RGBA) *Scene {
		s := NewScene()
		s.PushLayer(BlendNormal, 0.3, nil)
		s.Fill(FillNonZero, IdentityAffine(), SolidBrush(outer), rect)
		s.PushLayer(BlendNormal, 0.7, nil)
		s.Fill(FillNonZero, IdentityAffine(), SolidBrush(inner), rect)
		s.PopLayer()
		s.PopLayer()
		return s
	}

	redOutside := renderCPU(t, 100, 100, build(red, blue))
	blueOutside := renderCPU(t, 100, 100, build(blue, red))
	if d, at, found := pixelDiff(redOutside, blueOutside); !found || d <= 2 {
		t.Fatalf("expected order-dependent pixels, max diff %d at %v", d, at)
	}

	// Independently compute the two expected centers: inner color is scaled
	// 0.7, composited over the opaque outer fill, then the whole group scales
	// by the outer 0.3 pop.
	redP := refPixel{r: 1, a: 1}
	blueP := refPixel{b: 1, a: 1}
	wantRedOutside := refScaleAlpha(refSourceOver(refScaleAlpha(blueP, 0.7), redP), 0.3)
	wantBlueOutside := refScaleAlpha(refSourceOver(refScaleAlpha(redP, 0.7), blueP), 0.3)
	assertRefPixel(t, redOutside, 50, 50, wantRedOutside, 4, "red outside")
	assertRefPixel(t, blueOutside, 50, 50, wantBlueOutside, 4, "blue outside")
}

// assertRefPixel compares one straight-alpha pixmap pixel against a reference.
func assertRefPixel(t *testing.T, pm *gg.Pixmap, x, y int, want refPixel, tolerance int, label string) {
	t.Helper()
	got := pm.GetPixel(x, y)
	gotv := [4]float64{got.R, got.G, got.B, got.A}
	wantv := [4]float64{want.r, want.g, want.b, want.a}
	for ch := range gotv {
		d := gotv[ch] - wantv[ch]
		if d < 0 {
			d = -d
		}
		if d*255 > float64(tolerance) {
			t.Fatalf("%s: pixel(%d,%d) ch%d = %.4f want %.4f", label, x, y, ch, gotv[ch], wantv[ch])
		}
	}
}

// ---------------------------------------------------------------------------
// Different tile fixtures (requirement 2: no cross-row out-of-bounds writes)
// ---------------------------------------------------------------------------

// TestLayerAlpha_TileFixtures renders the same layered scene across surface
// sizes that produce full tiles, edge tiles narrower/shorter than 64px, and a
// surface smaller than one tile, then checks every output against the same
// reference and verifies no row-stride corruption at tile seams.
func TestLayerAlpha_TileFixtures(t *testing.T) {
	sizes := [][2]int{
		{64, 64},   // exactly one full tile
		{65, 65},   // edge tiles of 1px
		{100, 30},  // wide partial last row, short height
		{30, 100},  // narrow partial last column
		{128, 128}, // 2x2 full tiles
		{129, 129}, // 2x2 tiles + 1px edge tiles
		{1, 1},     // single pixel surface
		{63, 127},  // never aligns to the 64px grid
	}

	buildScene := func(w, h, bx0, by0, bx1, by1 int) *Scene {
		s := NewScene()
		// Background fill so layer compositing has a non-empty parent.
		s.Fill(FillNonZero, IdentityAffine(),
			SolidBrush(gg.RGBA{R: 1, G: 1, B: 1, A: 1}),
			NewRectShape(0, 0, float32(w), float32(h)))
		s.PushLayer(BlendNormal, 0.4, nil)
		// Outer layer covers the whole surface so the 0.4 group alpha is
		// present at every pixel.
		s.Fill(FillNonZero, IdentityAffine(),
			SolidBrush(gg.RGBA{R: 1, G: 0, B: 0, A: 1}),
			NewRectShape(0, 0, float32(w), float32(h)))
		s.PushLayer(BlendNormal, 0.6, nil)
		// Integer-aligned blue rect so the scene and reference share exact
		// geometry; only anti-aliased edge pixels differ from a hard mask.
		s.Fill(FillNonZero, IdentityAffine(),
			SolidBrush(gg.RGBA{R: 0, G: 0, B: 1, A: 1}),
			NewRectShape(float32(bx0), float32(by0), float32(bx1-bx0), float32(by1-by0)))
		s.PopLayer()
		s.PopLayer()
		return s
	}

	white := refPixel{r: 1, g: 1, b: 1, a: 1}
	red := refPixel{r: 1, g: 0, b: 0, a: 1}
	blue := refPixel{r: 0, g: 0, b: 1, a: 1}

	// Independent reference colors, mirroring the renderer's pop sequence:
	//   B pop: blue fill scaled 0.6 over the red fill inside A's buffer
	//   A pop: that whole buffer scaled 0.4, then source-over white
	redOnly := refSourceOver(refScaleAlpha(red, 0.4), white)
	blueInsideA := refSourceOver(refScaleAlpha(blue, 0.6), red)
	blueRegion := refSourceOver(refScaleAlpha(blueInsideA, 0.4), white)

	for _, size := range sizes {
		w, h := size[0], size[1]
		t.Run(sizeName(w, h), func(t *testing.T) {
			// Shared integer geometry for the nested blue rect.
			bx0, by0 := w/4, h/4
			bx1, by1 := bx0+w/2, by0+h/2

			target := renderCPU(t, w, h, buildScene(w, h, bx0, by0, bx1, by1))
			data := target.Data()
			if len(data) != w*h*4 {
				t.Fatalf("buffer length = %d, want %d", len(data), w*h*4)
			}

			// Pixels this far from a blue-rect edge are free of AA partial
			// coverage, so they must match the hard-mask reference exactly.
			const margin = 2
			inBlue := func(x, y int) bool {
				return x >= bx0+margin && x < bx1-margin &&
					y >= by0+margin && y < by1-margin
			}
			outBlue := func(x, y int) bool {
				return x < bx0-margin || x >= bx1+margin ||
					y < by0-margin || y >= by1+margin
			}

			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					got := target.GetPixel(x, y)
					gotv := [4]float64{got.R, got.G, got.B, got.A}

					// Every pixel must be well-formed: finite channels in
					// [0,1]. This is the structural check that catches
					// cross-row / out-of-bounds writes regardless of AA.
					for ch, v := range gotv {
						if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
							t.Fatalf("pixel(%d,%d) ch%d invalid = %v", x, y, ch, v)
						}
					}

					var want refPixel
					switch {
					case inBlue(x, y):
						want = blueRegion
					case outBlue(x, y):
						want = redOnly
					default:
						continue // AA edge band: validity checked above only
					}
					wantv := [4]float64{want.r, want.g, want.b, want.a}
					for ch := range gotv {
						d := gotv[ch] - wantv[ch]
						if d < 0 {
							d = -d
						}
						if d*255 > 5 {
							t.Fatalf("pixel(%d,%d) ch%d = %.4f want %.4f", x, y, ch, gotv[ch], wantv[ch])
						}
					}
				}
			}
		})
	}
}

func sizeName(w, h int) string {
	return "w" + itoa(w) + "h" + itoa(h)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// TestLayerAlpha_ParentChildSizeMismatch directly exercises
// compositeLayerPixmaps with differently sized pixmaps: writes must stay
// inside the smaller (destination) bounds and never cross a row.
func TestLayerAlpha_ParentChildSizeMismatch(t *testing.T) {
	dst := gg.NewPixmap(5, 3)
	dst.Clear(gg.RGBA{R: 1, G: 1, B: 1, A: 1})
	src := gg.NewPixmap(8, 7) // larger than dst on both axes
	src.FillRect(image.Rect(0, 0, 8, 7), 255, 0, 0, 255)

	compositeLayerPixmaps(dst, src, BlendNormal)

	// Every dst pixel is covered by the source but no panic/OOB occurred.
	// Spot-check row ends to ensure the last column of each row is valid.
	for y := 0; y < 3; y++ {
		p := dst.GetPixel(4, y)
		if p.A < 0.99 || p.R < 0.99 {
			t.Fatalf("dst(4,%d) = %+v, want opaque red", y, p)
		}
	}
}

// ---------------------------------------------------------------------------
// Empty layers, alpha zero, duplicate pop, cancellation, backend fallback (3)
// ---------------------------------------------------------------------------

// TestLayerAlpha_ZeroAndEmpty leaves no pixels behind.
func TestLayerAlpha_ZeroAndEmpty(t *testing.T) {
	t.Run("alpha zero with content", func(t *testing.T) {
		s := NewScene()
		s.PushLayer(BlendNormal, 0, nil)
		s.Fill(FillNonZero, IdentityAffine(),
			SolidBrush(gg.RGBA{R: 1, A: 1}), NewRectShape(0, 0, 50, 50))
		s.PopLayer()
		target := renderCPU(t, 80, 80, s)
		for i, b := range target.Data() {
			if b != 0 {
				t.Fatalf("alpha-0 layer left pixel data at byte %d: %d", i, b)
			}
		}
	})

	t.Run("empty layer", func(t *testing.T) {
		s := NewScene()
		s.PushLayer(BlendNormal, 0.9, nil)
		s.PopLayer() // no content at all
		target := renderCPU(t, 80, 80, s)
		for i, b := range target.Data() {
			if b != 0 {
				t.Fatalf("empty layer left pixel data at byte %d: %d", i, b)
			}
		}
	})

	t.Run("NaN alpha layer invisible", func(t *testing.T) {
		s := NewScene()
		s.PushLayer(BlendNormal, float32(math.NaN()), nil)
		s.Fill(FillNonZero, IdentityAffine(),
			SolidBrush(gg.RGBA{B: 1, A: 1}), NewRectShape(0, 0, 50, 50))
		s.PopLayer()
		target := renderCPU(t, 80, 80, s)
		for i, b := range target.Data() {
			if b != 0 {
				t.Fatalf("NaN-alpha layer left pixel data at byte %d: %d", i, b)
			}
		}
	})
}

// TestLayerAlpha_DuplicatePopIsHarmless verifies an extra PopLayer (no
// matching push) neither crashes nor modifies pixels.
func TestLayerAlpha_DuplicatePopIsHarmless(t *testing.T) {
	s := NewScene()
	s.Fill(FillNonZero, IdentityAffine(),
		SolidBrush(gg.RGBA{G: 1, A: 1}), NewRectShape(0, 0, 40, 40))
	s.PushLayer(BlendNormal, 0.5, nil)
	s.Fill(FillNonZero, IdentityAffine(),
		SolidBrush(gg.RGBA{R: 1, A: 1}), NewRectShape(10, 10, 40, 40))
	s.PopLayer()
	// Inject a second pop directly into the encoding to simulate a malformed
	// stream; the renderer must ignore it.
	s.Encoding().EncodePopLayer()

	target := renderCPU(t, 80, 80, s)
	if p := target.GetPixel(5, 5); p.G < 0.99 {
		t.Fatalf("base green pixel corrupted by duplicate pop: %+v", p)
	}
	if p := target.GetPixel(60, 60); p.A != 0 {
		t.Fatalf("background should stay transparent: %+v", p)
	}
}

// TestLayerAlpha_CancellationLeavesNoStalePixels renders once to establish
// content, then renders a different layered scene with a pre-cancelled
// context: the call must return ctx.Err() and the target must not contain
// pixels from the new scene (old target content is the caller's to keep;
// tiles are only composited after the cancellation checks pass).
func TestLayerAlpha_CancellationLeavesNoStalePixels(t *testing.T) {
	if gg.Accelerator() != nil {
		gg.CloseAccelerator()
	}
	w, h := 96, 96
	r := NewRenderer(w, h)
	defer r.Close()

	// First render establishes known (red) content.
	first := NewScene()
	first.Fill(FillNonZero, IdentityAffine(),
		SolidBrush(gg.RGBA{R: 1, A: 1}), NewRectShape(0, 0, float32(w), float32(h)))
	target := gg.NewPixmap(w, h)
	if err := r.Render(target, first); err != nil {
		t.Fatalf("first render: %v", err)
	}

	// Second, different scene (blue inside a layer) under an already-canceled
	// context must fail without compositing any blue tile.
	second := NewScene()
	second.PushLayer(BlendNormal, 0.5, nil)
	second.Fill(FillNonZero, IdentityAffine(),
		SolidBrush(gg.RGBA{B: 1, A: 1}), NewRectShape(0, 0, float32(w), float32(h)))
	second.PopLayer()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := r.RenderWithContext(ctx, target, second)
	if err != context.Canceled {
		t.Fatalf("RenderWithContext err = %v, want context.Canceled", err)
	}

	// No blue channel may have been introduced by the canceled render.
	for i := 2; i < len(target.Data()); i += 4 {
		if target.Data()[i] != 0 {
			t.Fatalf("canceled render left blue pixel at byte %d: %d", i, target.Data()[i])
		}
	}
}

// TestLayerAlpha_BackendFailureFallsBackToCPU verifies that when the GPU
// accelerator rejects every operation, rendering still succeeds through the
// CPU tile path with correct layer alpha — software and fallback behavior
// must agree.
func TestLayerAlpha_BackendFailureFallsBackToCPU(t *testing.T) {
	gg.CloseAccelerator()

	// CPU reference output (no accelerator registered).
	build := func() *Scene {
		s := NewScene()
		s.Fill(FillNonZero, IdentityAffine(),
			SolidBrush(gg.RGBA{R: 1, G: 1, B: 1, A: 1}), NewRectShape(0, 0, 100, 100))
		s.PushLayer(BlendNormal, 0.3, nil)
		s.Fill(FillNonZero, IdentityAffine(),
			SolidBrush(gg.RGBA{R: 1, G: 0, B: 0, A: 1}), NewRectShape(10, 10, 50, 50))
		s.PopLayer()
		return s
	}
	cpuTarget := renderCPU(t, 100, 100, build())

	// Register an accelerator whose every op fails, forcing fallback.
	tracker := &fallbackAccelerator{}
	if err := gg.RegisterAccelerator(tracker); err != nil {
		t.Fatalf("RegisterAccelerator: %v", err)
	}
	defer gg.CloseAccelerator()

	fallbackTarget := gg.NewPixmap(100, 100)
	r2 := NewRenderer(100, 100)
	defer r2.Close()
	if err := r2.Render(fallbackTarget, build()); err != nil {
		t.Fatalf("fallback render: %v", err)
	}

	const tolerance = 2
	if d, at, found := pixelDiff(cpuTarget, fallbackTarget); found && d > tolerance {
		t.Fatalf("CPU vs fallback mismatch: max diff %d at %v (tol %d)", d, at, tolerance)
	}
}

// fallbackAccelerator rejects everything, exercising GPU->CPU fallback.
type fallbackAccelerator struct{}

func (fallbackAccelerator) Name() string { return "test-fallback" }
func (fallbackAccelerator) Init() error  { return nil }
func (fallbackAccelerator) Close()       {}
func (fallbackAccelerator) CanAccelerate(op gg.AcceleratedOp) bool {
	return op != 0
}
func (fallbackAccelerator) FillShape(gg.GPURenderTarget, gg.DetectedShape, *gg.Paint) error {
	return gg.ErrFallbackToCPU
}
func (fallbackAccelerator) StrokeShape(gg.GPURenderTarget, gg.DetectedShape, *gg.Paint) error {
	return gg.ErrFallbackToCPU
}
func (fallbackAccelerator) FillPath(gg.GPURenderTarget, *gg.Path, *gg.Paint) error {
	return gg.ErrFallbackToCPU
}
func (fallbackAccelerator) StrokePath(gg.GPURenderTarget, *gg.Path, *gg.Paint) error {
	return gg.ErrFallbackToCPU
}
func (fallbackAccelerator) Flush(gg.GPURenderTarget) error { return nil }

// TestLayerAlpha_GPUSceneRendererParity renders one nested-layer scene through
// both the CPU tile Renderer and the GPUSceneRenderer (which decodes the same
// tags onto a gg.Context using PushLayer/PopLayer). The two decoders must
// apply layer alpha identically, including nested layers and sanitization.
func TestLayerAlpha_GPUSceneRendererParity(t *testing.T) {
	gg.CloseAccelerator()

	const w, h = 128, 128
	build := func() *Scene {
		s := NewScene()
		s.Fill(FillNonZero, IdentityAffine(),
			SolidBrush(gg.RGBA{R: 1, G: 1, B: 1, A: 1}),
			NewRectShape(0, 0, w, h))
		s.PushLayer(BlendNormal, 0.5, nil)
		s.Fill(FillNonZero, IdentityAffine(),
			SolidBrush(gg.RGBA{R: 1, G: 0, B: 0, A: 1}),
			NewRectShape(10, 10, 70, 70))
		s.PushLayer(BlendNormal, 0.25, nil)
		s.Fill(FillNonZero, IdentityAffine(),
			SolidBrush(gg.RGBA{R: 0, G: 1, B: 0, A: 1}),
			NewRectShape(30, 30, 70, 70))
		s.PopLayer()
		s.PopLayer()
		return s
	}

	cpuTarget := renderCPU(t, w, h, build())

	// GPU scene renderer drives a plain gg.Context (no accelerator, so every
	// draw resolves on the CPU inside the context's layer machinery).
	gpuPM := gg.NewPixmap(w, h)
	dc := gg.NewContextForPixmap(gpuPM)
	if dc == nil {
		t.Fatal("NewContextForPixmap returned nil")
	}
	gpuRenderer := NewGPUSceneRenderer(dc)
	if err := gpuRenderer.RenderScene(build()); err != nil {
		t.Fatalf("GPUSceneRenderer.RenderScene: %v", err)
	}
	if err := dc.Close(); err != nil {
		t.Fatalf("dc.Close: %v", err)
	}

	// Interior pixels avoid AA edges; the two paths use different rasterizers,
	// so allow a modest tolerance, but layer alpha must match closely.
	const tolerance = 6
	if d, at, found := pixelDiff(cpuTarget, gpuPM); found && d > tolerance {
		t.Fatalf("CPU vs GPUSceneRenderer layer mismatch: max diff %d at %v (tol %d)",
			d, at, tolerance)
	}
}

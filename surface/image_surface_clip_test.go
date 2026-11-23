// Copyright 2026 The gogpu Authors
// SPDX-License-Identifier: MIT

package surface

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"testing"
)

// --- reference mask helpers ---

// sdCircle returns the signed distance from (x, y) to a circle boundary.
// Negative inside.
func sdCircle(x, y, cx, cy, r float64) float64 {
	return math.Hypot(x-cx, y-cy) - r
}

// sdRect returns the signed distance from (x, y) to an axis-aligned
// rectangle boundary. Negative inside.
func sdRect(x, y, rx, ry, rw, rh float64) float64 {
	cx, cy := rx+rw/2, ry+rh/2
	dx := math.Abs(x-cx) - rw/2
	dy := math.Abs(y-cy) - rh/2
	if dx <= 0 && dy <= 0 {
		return math.Max(dx, dy)
	}
	return math.Hypot(math.Max(dx, 0), math.Max(dy, 0))
}

// assertMaskMatchesSDF verifies a clip mask against a reference signed
// distance field. Pixels deeper than 1.5px inside must have full coverage;
// pixels further than 1.5px outside must have zero coverage. The 1.5px band
// around the boundary is anti-aliasing territory and is not checked.
func assertMaskMatchesSDF(t *testing.T, mask []uint8, w, h int, sd func(x, y float64) float64) {
	t.Helper()
	if len(mask) != w*h {
		t.Fatalf("mask length = %d, want %d", len(mask), w*h)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			d := sd(float64(x)+0.5, float64(y)+0.5)
			m := mask[y*w+x]
			switch {
			case d < -1.5 && m != 255:
				t.Errorf("pixel (%d,%d) deep inside (sd=%.2f): mask = %d, want 255", x, y, d, m)
			case d > 1.5 && m != 0:
				t.Errorf("pixel (%d,%d) deep outside (sd=%.2f): mask = %d, want 0", x, y, d, m)
			}
		}
	}
}

// --- capability and interface ---

// TestImageSurfaceClipCapabilities verifies clipping is now advertised.
func TestImageSurfaceClipCapabilities(t *testing.T) {
	s := NewImageSurface(10, 10)
	defer s.Close()

	caps := s.Capabilities()
	if !caps.SupportsClipping {
		t.Error("ImageSurface should report SupportsClipping")
	}

	var _ ClippableSurface = s // compile-time interface check
}

// --- basic clip behavior ---

// TestImageSurfaceSetClipRect verifies a rectangular clip limits filling,
// including on a fully transparent target.
func TestImageSurfaceSetClipRect(t *testing.T) {
	s := NewImageSurface(100, 100)
	defer s.Close()

	clip := NewPath()
	clip.Rectangle(20, 20, 40, 40)
	s.SetClip(clip)

	if !s.HasClip() {
		t.Fatal("HasClip() = false after SetClip")
	}

	// Fill the whole surface; only the clip region may be affected.
	full := NewPath()
	full.Rectangle(0, 0, 100, 100)
	s.Fill(full, FillStyle{Color: color.RGBA{255, 0, 0, 255}})

	img := s.Snapshot()

	if c := img.RGBAAt(40, 40); c.R != 255 || c.A != 255 {
		t.Errorf("inside pixel = %v, want opaque red", c)
	}
	if c := img.RGBAAt(10, 10); c.A != 0 {
		t.Errorf("outside pixel = %v, want fully transparent (untouched)", c)
	}
	if c := img.RGBAAt(61, 61); c.A != 0 {
		t.Errorf("pixel past clip edge = %v, want fully transparent", c)
	}
}

// TestImageSurfaceClipCircleIntersectRect is the circle-over-rect fixture:
// two SetClip calls must intersect, verified against a reference mask.
func TestImageSurfaceClipCircleIntersectRect(t *testing.T) {
	const w, h = 120, 120
	const cx, cy, r = 60, 60, 40
	const rx, ry, rw, rh = 40, 30, 60, 50

	s := NewImageSurface(w, h)
	defer s.Close()

	circle := NewPath()
	circle.Circle(cx, cy, r)
	s.SetClip(circle)

	rect := NewPath()
	rect.Rectangle(rx, ry, rw, rh)
	s.SetClip(rect)

	ref := func(x, y float64) float64 {
		return math.Max(sdCircle(x, y, cx, cy, r), sdRect(x, y, rx, ry, rw, rh))
	}
	assertMaskMatchesSDF(t, s.clip.mask, w, h, ref)

	// Render through the clip and check the same reference against pixels.
	full := NewPath()
	full.Rectangle(0, 0, w, h)
	s.Fill(full, FillStyle{Color: color.RGBA{0, 0, 255, 255}})

	img := s.Snapshot()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			d := ref(float64(x)+0.5, float64(y)+0.5)
			a := img.RGBAAt(x, y).A
			switch {
			case d < -1.5 && a != 255:
				t.Errorf("pixel (%d,%d) inside intersection: alpha = %d, want 255", x, y, a)
			case d > 1.5 && a != 0:
				t.Errorf("pixel (%d,%d) outside intersection: alpha = %d, want 0", x, y, a)
			}
		}
	}
}

// TestImageSurfaceClipSemiTransparentFill verifies blending through a clip
// onto a transparent target produces exact source-over results inside and
// leaves the outside untouched.
func TestImageSurfaceClipSemiTransparentFill(t *testing.T) {
	s := NewImageSurface(64, 64)
	defer s.Close()

	clip := NewPath()
	clip.Rectangle(16, 16, 32, 32)
	s.SetClip(clip)

	full := NewPath()
	full.Rectangle(0, 0, 64, 64)
	s.Fill(full, FillStyle{Color: color.RGBA{255, 0, 0, 128}})

	img := s.Snapshot()

	inside := img.RGBAAt(32, 32)
	if inside.A != 128 || inside.R != 255 || inside.G != 0 || inside.B != 0 {
		t.Errorf("inside pixel = %v, want (255, 0, 0, 128)", inside)
	}
	if c := img.RGBAAt(8, 8); c.A != 0 {
		t.Errorf("outside pixel = %v, want fully transparent", c)
	}
}

// TestImageSurfaceClipDrawImageOutOfBounds draws an image that extends past
// the surface origin while a clip is active: no panic, clip still applies.
func TestImageSurfaceClipDrawImageOutOfBounds(t *testing.T) {
	s := NewImageSurface(50, 50)
	defer s.Close()

	clip := NewPath()
	clip.Rectangle(0, 0, 5, 5)
	s.SetClip(clip)

	src := image.NewRGBA(image.Rect(0, 0, 20, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 20; x++ {
			src.SetRGBA(x, y, color.RGBA{255, 0, 0, 255})
		}
	}

	// Image spans (-10,-10)-(10,10); clip restricts to (0,0)-(5,5).
	s.DrawImage(src, Pt(-10, -10), nil)

	img := s.Snapshot()
	if c := img.RGBAAt(2, 2); c.R != 255 || c.A != 255 {
		t.Errorf("clipped image pixel = %v, want opaque red", c)
	}
	if c := img.RGBAAt(7, 7); c.A != 0 {
		t.Errorf("pixel inside image but outside clip = %v, want transparent", c)
	}
	if c := img.RGBAAt(20, 20); c.A != 0 {
		t.Errorf("pixel outside image and clip = %v, want transparent", c)
	}
}

// --- stack behavior ---

// TestImageSurfaceClipPushPop verifies nested clips intersect and PopClip
// restores the enclosing state (Save/Restore equivalence).
func TestImageSurfaceClipPushPop(t *testing.T) {
	s := NewImageSurface(100, 100)
	defer s.Close()

	outer := NewPath()
	outer.Rectangle(10, 10, 80, 80)
	s.SetClip(outer)

	s.PushClip()

	inner := NewPath()
	inner.Rectangle(40, 40, 50, 50)
	s.SetClip(inner) // intersect: (40,40)-(90,90) ∩ (10,10)-(90,90)

	full := NewPath()
	full.Rectangle(0, 0, 100, 100)
	s.Fill(full, FillStyle{Color: color.RGBA{255, 0, 0, 255}})

	img := s.Snapshot()
	if c := img.RGBAAt(50, 50); c.A != 255 {
		t.Errorf("nested clip inside = %v, want opaque", c)
	}
	if c := img.RGBAAt(20, 20); c.A != 0 {
		t.Errorf("inside outer but outside inner = %v, want transparent", c)
	}

	s.PopClip()

	// After pop, the outer clip is active again.
	s.Fill(full, FillStyle{Color: color.RGBA{0, 255, 0, 255}})
	img = s.Snapshot()
	if c := img.RGBAAt(20, 20); c.G != 255 || c.A != 255 {
		t.Errorf("after PopClip, outer region = %v, want opaque green", c)
	}
	if c := img.RGBAAt(5, 5); c.A != 0 {
		t.Errorf("after PopClip, outside outer = %v, want transparent", c)
	}
}

// TestImageSurfaceClipPopEmptyStack verifies popping with no matching push
// is a safe no-op.
func TestImageSurfaceClipPopEmptyStack(t *testing.T) {
	s := NewImageSurface(20, 20)
	defer s.Close()

	s.PopClip() // must not panic or change state
	if s.HasClip() {
		t.Error("HasClip() = true after PopClip on empty stack")
	}

	clip := NewPath()
	clip.Rectangle(0, 0, 10, 10)
	s.SetClip(clip)
	s.PopClip() // still no push — clip must remain
	if !s.HasClip() {
		t.Error("HasClip() = false after unmatched PopClip")
	}
}

// TestImageSurfaceClearClip verifies ClearClip removes only the current level.
func TestImageSurfaceClearClip(t *testing.T) {
	s := NewImageSurface(50, 50)
	defer s.Close()

	clip := NewPath()
	clip.Rectangle(0, 0, 10, 10)
	s.SetClip(clip)
	s.ClearClip()

	if s.HasClip() {
		t.Fatal("HasClip() = true after ClearClip")
	}

	full := NewPath()
	full.Rectangle(0, 0, 50, 50)
	s.Fill(full, FillStyle{Color: color.RGBA{255, 0, 0, 255}})

	if c := s.Snapshot().RGBAAt(40, 40); c.A != 255 {
		t.Errorf("pixel after ClearClip = %v, want opaque red", c)
	}
}

// --- geometry edge cases ---

// TestImageSurfaceClipRotatedPath verifies a rotated path clips correctly.
func TestImageSurfaceClipRotatedPath(t *testing.T) {
	const w, h = 100, 100
	s := NewImageSurface(w, h)
	defer s.Close()

	// Square of half-size 25 centered at (50,50), rotated 45°.
	const half = 25.0
	angle := math.Pi / 4
	ca, sa := math.Cos(angle), math.Sin(angle)
	corners := [4][2]float64{{-half, -half}, {half, -half}, {half, half}, {-half, half}}

	rot := NewPath()
	for i, c := range corners {
		x := 50 + c[0]*ca-c[1]*sa
		y := 50 + c[0]*sa+c[1]*ca
		if i == 0 {
			rot.MoveTo(x, y)
		} else {
			rot.LineTo(x, y)
		}
	}
	rot.Close()
	s.SetClip(rot)

	full := NewPath()
	full.Rectangle(0, 0, w, h)
	s.Fill(full, FillStyle{Color: color.RGBA{255, 0, 0, 255}})

	img := s.Snapshot()
	if c := img.RGBAAt(50, 50); c.A != 255 {
		t.Errorf("center of rotated square = %v, want opaque", c)
	}
	// (50, 50±30) is outside the rotated square (vertex reaches 50±25·√2≈±35.4
	// along axes, but at x=50 the boundary is exactly the vertex: y=50±35.4).
	if c := img.RGBAAt(50, 12); c.A != 0 {
		t.Errorf("above rotated square = %v, want transparent", c)
	}
	if c := img.RGBAAt(50, 88); c.A != 0 {
		t.Errorf("below rotated square = %v, want transparent", c)
	}
	// Point on the rotated edge's interior: (50+17.7, 50+17.7) is on the
	// boundary; (55, 55) is well inside.
	if c := img.RGBAAt(55, 55); c.A != 255 {
		t.Errorf("inside rotated square = %v, want opaque", c)
	}
}

// TestImageSurfaceClipNegativeSize verifies a rectangle specified with
// negative width/height still clips to its covered region.
func TestImageSurfaceClipNegativeSize(t *testing.T) {
	s := NewImageSurface(100, 100)
	defer s.Close()

	clip := NewPath()
	clip.Rectangle(60, 60, -40, -40) // covers (20,20)-(60,60)
	s.SetClip(clip)

	full := NewPath()
	full.Rectangle(0, 0, 100, 100)
	s.Fill(full, FillStyle{Color: color.RGBA{255, 0, 0, 255}})

	img := s.Snapshot()
	if c := img.RGBAAt(30, 30); c.A != 255 {
		t.Errorf("inside negative-size rect = %v, want opaque", c)
	}
	if c := img.RGBAAt(10, 10); c.A != 0 {
		t.Errorf("outside negative-size rect = %v, want transparent", c)
	}
	if c := img.RGBAAt(70, 70); c.A != 0 {
		t.Errorf("past negative-size rect = %v, want transparent", c)
	}
}

// TestImageSurfaceClipTangentBoundary verifies a clip intersection whose
// shapes only touch at a tangent line produces an empty (or near-empty)
// clip without instability.
func TestImageSurfaceClipTangentBoundary(t *testing.T) {
	s := NewImageSurface(100, 100)
	defer s.Close()

	circle := NewPath()
	circle.Circle(50, 50, 20) // rightmost x = 70
	s.SetClip(circle)

	rect := NewPath()
	rect.Rectangle(70, 0, 30, 100) // starts exactly at the tangent x = 70
	s.SetClip(rect)

	// The intersection has zero area. Anti-aliasing may leave faint
	// coverage on the tangent column, but nothing substantial.
	strong := 0
	for _, v := range s.clip.mask {
		if v > 128 {
			strong++
		}
	}
	if strong != 0 {
		t.Errorf("tangent intersection: %d pixels with coverage > 128, want 0", strong)
	}
}

// TestImageSurfaceClipEvenOdd verifies the fill rule applies to clips:
// an EvenOdd ring clips out its hole.
func TestImageSurfaceClipEvenOdd(t *testing.T) {
	s := NewImageSurface(100, 100)
	defer s.Close()

	ring := NewPath()
	ring.Circle(50, 50, 30)
	ring.Circle(50, 50, 15)
	s.SetClipWithRule(ring, FillRuleEvenOdd)

	full := NewPath()
	full.Rectangle(0, 0, 100, 100)
	s.Fill(full, FillStyle{Color: color.RGBA{255, 0, 0, 255}})

	img := s.Snapshot()
	if c := img.RGBAAt(50, 25); c.A != 255 {
		t.Errorf("ring band pixel = %v, want opaque", c)
	}
	if c := img.RGBAAt(50, 50); c.A != 0 {
		t.Errorf("ring hole pixel = %v, want transparent", c)
	}
	if c := img.RGBAAt(50, 5); c.A != 0 {
		t.Errorf("outside ring = %v, want transparent", c)
	}
}

// TestImageSurfaceClipStroke verifies strokes respect the clip region.
func TestImageSurfaceClipStroke(t *testing.T) {
	s := NewImageSurface(100, 100)
	defer s.Close()

	s.Clear(color.White)

	clip := NewPath()
	clip.Rectangle(0, 0, 100, 49)
	s.SetClip(clip)

	line := NewPath()
	line.MoveTo(10, 50)
	line.LineTo(90, 50)
	s.Stroke(line, StrokeStyle{Color: color.RGBA{0, 128, 0, 255}, Width: 6})

	img := s.Snapshot()
	if c := img.RGBAAt(50, 48); c.G < 100 {
		t.Errorf("stroke inside clip = %v, want green", c)
	}
	if c := img.RGBAAt(50, 51); c.G != 255 || c.R != 255 {
		t.Errorf("stroke outside clip = %v, want untouched white", c)
	}
}

// TestImageSurfaceClipPathContinueAfterClose verifies a path that keeps
// drawing after Close clips to all of its subpaths.
func TestImageSurfaceClipPathContinueAfterClose(t *testing.T) {
	s := NewImageSurface(100, 100)
	defer s.Close()

	clip := NewPath()
	clip.Rectangle(10, 10, 20, 20)
	clip.Close()
	clip.Rectangle(60, 60, 20, 20) // second subpath after Close
	s.SetClip(clip)

	full := NewPath()
	full.Rectangle(0, 0, 100, 100)
	s.Fill(full, FillStyle{Color: color.RGBA{255, 0, 0, 255}})

	img := s.Snapshot()
	if c := img.RGBAAt(20, 20); c.A != 255 {
		t.Errorf("first subpath = %v, want opaque", c)
	}
	if c := img.RGBAAt(70, 70); c.A != 255 {
		t.Errorf("subpath after Close = %v, want opaque", c)
	}
	if c := img.RGBAAt(45, 45); c.A != 0 {
		t.Errorf("between subpaths = %v, want transparent", c)
	}
}

// --- robustness ---

// TestImageSurfaceClipEmptyPath verifies clipping to an empty path is a
// defined empty clip: nothing is drawn, no panic.
func TestImageSurfaceClipEmptyPath(t *testing.T) {
	s := NewImageSurface(50, 50)
	defer s.Close()

	s.SetClip(NewPath()) // empty path
	if !s.HasClip() {
		t.Fatal("HasClip() = false after SetClip with empty path")
	}

	full := NewPath()
	full.Rectangle(0, 0, 50, 50)
	s.Fill(full, FillStyle{Color: color.RGBA{255, 0, 0, 255}})

	img := s.Snapshot()
	if c := img.RGBAAt(25, 25); c.A != 0 {
		t.Errorf("pixel under empty clip = %v, want transparent", c)
	}

	// nil path behaves the same.
	s.ClearClip()
	s.SetClip(nil)
	s.Fill(full, FillStyle{Color: color.RGBA{255, 0, 0, 255}})
	if c := s.Snapshot().RGBAAt(25, 25); c.A != 0 {
		t.Errorf("pixel under nil-path clip = %v, want transparent", c)
	}
}

// TestImageSurfaceClipNonFinite verifies paths containing NaN/Inf
// coordinates produce a defined empty clip instead of corrupting the
// rasterizer.
func TestImageSurfaceClipNonFinite(t *testing.T) {
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		s := NewImageSurface(50, 50)

		clip := NewPath()
		clip.MoveTo(0, 0)
		clip.LineTo(bad, 10)
		clip.LineTo(10, 10)
		clip.Close()
		s.SetClip(clip) // must not panic or hang

		full := NewPath()
		full.Rectangle(0, 0, 50, 50)
		s.Fill(full, FillStyle{Color: color.RGBA{255, 0, 0, 255}})

		if c := s.Snapshot().RGBAAt(25, 25); c.A != 0 {
			t.Errorf("coord %v: pixel under non-finite clip = %v, want transparent", bad, c)
		}
		s.Close()
	}
}

// TestImageSurfaceClipHugePath verifies coordinates far outside the fixed
// point range are clamped safely and still produce the expected coverage.
func TestImageSurfaceClipHugePath(t *testing.T) {
	s := NewImageSurface(64, 64)
	defer s.Close()

	// A rectangle from -1e9 to 1e9 covers the entire surface.
	clip := NewPath()
	clip.Rectangle(-1e9, -1e9, 2e9, 2e9)
	s.SetClip(clip)

	full := NewPath()
	full.Rectangle(0, 0, 64, 64)
	s.Fill(full, FillStyle{Color: color.RGBA{255, 0, 0, 255}})

	img := s.Snapshot()
	for _, p := range [][2]int{{0, 0}, {32, 32}, {63, 63}} {
		if c := img.RGBAAt(p[0], p[1]); c.A != 255 {
			t.Errorf("pixel %v under huge clip = %v, want opaque", p, c)
		}
	}
}

// TestImageSurfaceClipMemoryBound verifies clip memory stays proportional
// to the surface size regardless of path complexity.
func TestImageSurfaceClipMemoryBound(t *testing.T) {
	const w, h = 32, 32
	s := NewImageSurface(w, h)
	defer s.Close()

	// A path with many segments still rasterizes into one w*h mask.
	clip := NewPath()
	for i := 0; i < 1000; i++ {
		angle := float64(i) * 2 * math.Pi / 1000
		x := 16 + 12*math.Cos(angle)
		y := 16 + 12*math.Sin(angle)
		if i == 0 {
			clip.MoveTo(x, y)
		} else {
			clip.LineTo(x, y)
		}
	}
	clip.Close()
	s.SetClip(clip)

	if got := len(s.clip.mask); got != w*h {
		t.Errorf("clip mask size = %d, want %d (one byte per pixel)", got, w*h)
	}
	if got := len(s.clipScratch); got != w*h {
		t.Errorf("clip scratch size = %d, want %d", got, w*h)
	}

	s.ClearClip()
	if s.clip.mask != nil {
		t.Error("clip mask should be released (nil) after ClearClip")
	}
}

// TestImageSurfaceClipOpsAfterClose verifies all clip entry points are safe
// no-ops after the surface is closed.
func TestImageSurfaceClipOpsAfterClose(t *testing.T) {
	s := NewImageSurface(20, 20)

	clip := NewPath()
	clip.Rectangle(0, 0, 10, 10)
	s.SetClip(clip)
	s.PushClip()

	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	// None of these may panic.
	s.SetClip(clip)
	s.SetClipWithRule(clip, FillRuleEvenOdd)
	s.PushClip()
	s.PopClip()
	s.ClearClip()

	if s.HasClip() {
		t.Error("HasClip() = true after Close")
	}
}

// TestImageSurfaceNoClipOutputUnchanged verifies that when no clip is
// active — including after balanced push/pop and clear cycles — rendering
// output is byte-identical to the legacy unclipped path.
func TestImageSurfaceNoClipOutputUnchanged(t *testing.T) {
	draw := func(s *ImageSurface) {
		s.Clear(color.White)
		circle := NewPath()
		circle.Circle(50, 50, 30)
		s.Fill(circle, FillStyle{Color: color.RGBA{255, 0, 0, 255}})
		rect := NewPath()
		rect.Rectangle(10, 60, 80, 30)
		s.Fill(rect, FillStyle{Color: color.RGBA{0, 0, 255, 255}, Rule: FillRuleEvenOdd})
		line := NewPath()
		line.MoveTo(0, 10)
		line.LineTo(100, 20)
		s.Stroke(line, StrokeStyle{Color: color.RGBA{0, 128, 0, 255}, Width: 3})
	}

	a := NewImageSurface(100, 100)
	defer a.Close()
	draw(a)

	b := NewImageSurface(100, 100)
	defer b.Close()

	// Interleave clip stack operations that net out to no clip.
	clip := NewPath()
	clip.Rectangle(0, 0, 50, 50)
	b.PushClip()
	b.SetClip(clip)
	b.PopClip()
	b.SetClip(clip)
	b.ClearClip()
	b.PushClip()
	b.PopClip()
	draw(b)

	if !bytes.Equal(a.Snapshot().Pix, b.Snapshot().Pix) {
		t.Error("output with balanced clip stack differs from unclipped output")
	}
}

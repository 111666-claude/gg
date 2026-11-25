package raster

// TEMPORARY scratch verification for uid9. All identifiers carry uid9scr
// prefix; this file is deleted before submission.

import (
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/recording"
)

func uid9scrSheet(size int) *image.RGBA {
	h := size / 2
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var c color.RGBA
			switch {
			case x < h && y < h:
				c = color.RGBA{255, 0, 0, 255}
			case x >= h && y < h:
				c = color.RGBA{0, 255, 0, 255}
			case x < h && y >= h:
				c = color.RGBA{0, 0, 255, 255}
			default:
				c = color.RGBA{255, 255, 0, 255}
			}
			img.Set(x, y, c)
		}
	}
	return img
}

func uid9scrPx(img image.Image, x, y int) color.RGBA {
	r, g, b, a := img.At(x, y).RGBA()
	return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}
}

func uid9scrPlay(t *testing.T, rec *recording.Recording) *Backend {
	t.Helper()
	be := NewBackend()
	if err := be.Begin(rec.Width(), rec.Height()); err != nil {
		t.Fatal(err)
	}
	if err := rec.Playback(be); err != nil {
		t.Fatalf("Playback: %v", err)
	}
	return be
}

func TestUID9ScrCrop(t *testing.T) {
	sheet := uid9scrSheet(40)
	rec := recording.NewRecorder(80, 80)
	rec.DrawImageRect(sheet, image.Rect(20, 0, 40, 20), recording.NewRect(10, 10, 20, 20))
	img := uid9scrPlay(t, rec.FinishRecording()).Image()

	if got, want := uid9scrPx(img, 20, 20), (color.RGBA{0, 255, 0, 255}); got != want {
		t.Errorf("center=%v want %v", got, want)
	}
	for _, p := range [][2]int{{5, 5}, {35, 20}, {20, 35}, {60, 60}} {
		if got := uid9scrPx(img, p[0], p[1]); got.A != 0 {
			t.Errorf("outside %v=%v want transparent", p, got)
		}
	}
}

func TestUID9ScrTransparentHole(t *testing.T) {
	sheet := uid9scrSheet(40)
	for y := 5; y < 15; y++ {
		for x := 5; x < 15; x++ {
			sheet.Set(x, y, color.RGBA{0, 255, 0, 0})
		}
	}
	rec := recording.NewRecorder(40, 40)
	rec.DrawImageRect(sheet, image.Rect(0, 0, 40, 20), recording.NewRect(0, 0, 40, 20))
	img := uid9scrPlay(t, rec.FinishRecording()).Image()
	if got := uid9scrPx(img, 5, 5); got.A != 0 {
		t.Errorf("hole=%v want transparent", got)
	}
	if got, want := uid9scrPx(img, 30, 5), (color.RGBA{0, 255, 0, 255}); got != want {
		t.Errorf("green=%v want %v", got, want)
	}
}

func TestUID9ScrNonZeroBounds(t *testing.T) {
	sheet := image.NewRGBA(image.Rect(10, 20, 50, 60))
	for y := 20; y < 60; y++ {
		for x := 10; x < 50; x++ {
			sheet.Set(x, y, color.RGBA{0, 0, 255, 255})
		}
	}
	rec := recording.NewRecorder(60, 60)
	rec.DrawImage(sheet, 10, 10)
	img := uid9scrPlay(t, rec.FinishRecording()).Image()
	if got, want := uid9scrPx(img, 20, 20), (color.RGBA{0, 0, 255, 255}); got != want {
		t.Errorf("nz=%v want %v", got, want)
	}
	if got := uid9scrPx(img, 5, 5); got.A != 0 {
		t.Errorf("before origin=%v want transparent", got)
	}

	// Absolute src rect crop.
	rec = recording.NewRecorder(60, 60)
	rec.DrawImageRect(sheet, image.Rect(30, 40, 50, 60), recording.NewRect(0, 0, 20, 20))
	img = uid9scrPlay(t, rec.FinishRecording()).Image()
	if got, want := uid9scrPx(img, 10, 10), (color.RGBA{0, 0, 255, 255}); got != want {
		t.Errorf("abs crop=%v want %v", got, want)
	}

	// SubImage (linked view) with non-zero Min into a bigger parent.
	parent := uid9scrSheet(40)
	sub := parent.SubImage(image.Rect(20, 0, 40, 20))
	be := NewBackend()
	_ = be.Begin(20, 20)
	be.DrawImage(sub, recording.Rect{MinX: 20, MinY: 0, MaxX: 40, MaxY: 20},
		recording.NewRect(0, 0, 20, 20), recording.DefaultImageOptions())
	if got, want := uid9scrPx(be.Image(), 10, 10), (color.RGBA{0, 255, 0, 255}); got != want {
		t.Errorf("subimage=%v want %v", got, want)
	}
}

func uid9scrAssertParity(t *testing.T, got, want image.Image, pts [][2]int) {
	t.Helper()
	for _, p := range pts {
		g := uid9scrPx(got, p[0], p[1])
		w := uid9scrPx(want, p[0], p[1])
		for i := 0; i < 4; i++ {
			var gv, wv uint8
			switch i {
			case 0:
				gv, wv = g.R, w.R
			case 1:
				gv, wv = g.G, w.G
			case 2:
				gv, wv = g.B, w.B
			default:
				gv, wv = g.A, w.A
			}
			d := int(gv) - int(wv)
			if d < -3 || d > 3 {
				t.Errorf("pt %v ch%d backend=%v software=%v", p, i, g, w)
				break
			}
		}
	}
}

func TestUID9ScrRotation(t *testing.T) {
	sheet := uid9scrSheet(40)
	const ang = math.Pi / 6
	rec := recording.NewRecorder(120, 120)
	rec.Translate(60, 60)
	rec.Rotate(ang)
	rec.Scale(1.5, 1.5)
	rec.DrawImageScaled(sheet, -20, -20, 40, 40)
	got := uid9scrPlay(t, rec.FinishRecording()).Image()

	dc := gg.NewContext(120, 120)
	dc.Translate(60, 60)
	dc.Rotate(ang)
	dc.Scale(1.5, 1.5)
	dc.DrawImageEx(gg.ImageBufFromImage(sheet), gg.DrawImageOptions{
		X: -20, Y: -20, DstWidth: 40, DstHeight: 40,
		Interpolation: gg.InterpBilinear, Opacity: 1,
	})
	uid9scrAssertParity(t, got, dc.Image(), [][2]int{{60, 60}, {52, 52}, {68, 68}, {48, 72}, {72, 48}})

	seen := map[color.RGBA]bool{}
	for y := 30; y < 90; y++ {
		for x := 30; x < 90; x++ {
			p := uid9scrPx(got, x, y)
			if p.A > 200 {
				seen[p] = true
			}
		}
	}
	for _, w := range []color.RGBA{{255, 0, 0, 255}, {0, 255, 0, 255}, {0, 0, 255, 255}, {255, 255, 0, 255}} {
		if !seen[w] {
			t.Errorf("missing quadrant %v; seen=%v", w, seen)
		}
	}
}

func TestUID9ScrMirror(t *testing.T) {
	sheet := uid9scrSheet(40)
	rec := recording.NewRecorder(80, 80)
	rec.DrawImageScaled(sheet, 30, 10, -20, 20)
	got := uid9scrPlay(t, rec.FinishRecording()).Image()

	dc := gg.NewContext(80, 80)
	dc.DrawImageEx(gg.ImageBufFromImage(sheet), gg.DrawImageOptions{
		X: 30, Y: 10, DstWidth: -20, DstHeight: 20,
		Interpolation: gg.InterpBilinear, Opacity: 1,
	})
	uid9scrAssertParity(t, got, dc.Image(), [][2]int{{15, 20}, {25, 20}, {20, 12}, {20, 28}})
}

func TestUID9ScrOutsideCases(t *testing.T) {
	sheet := uid9scrSheet(40)
	be := NewBackend()
	_ = be.Begin(60, 60)
	cases := []recording.Rect{
		recording.NewRect(100, 100, 20, 20),
		recording.NewRect(10, 10, 0, 20),
		recording.NewRect(30, 30, -10, -10),
		recording.NewRect(-40, -40, 20, 20),
	}
	for i, src := range cases {
		be.DrawImage(sheet, src, recording.NewRect(0, 0, 20, 20), recording.DefaultImageOptions())
		img := be.Image()
		for y := 0; y < 60; y++ {
			for x := 0; x < 60; x++ {
				if p := uid9scrPx(img, x, y); p.A != 0 {
					t.Fatalf("case %d painted %v at %d,%d", i, p, x, y)
				}
			}
		}
	}

	be2 := NewBackend()
	_ = be2.Begin(60, 60)
	be2.DrawImage(sheet, recording.NewRect(-20, -20, 40, 40),
		recording.NewRect(0, 0, 20, 20), recording.DefaultImageOptions())
	// Intersection is image [0,20]^2 = red quadrant; src-relative fraction
	// 0.5..1 maps it into destination [10,20]^2 (bottom-right).
	if got, want := uid9scrPx(be2.Image(), 15, 15), (color.RGBA{255, 0, 0, 255}); got != want {
		t.Errorf("partial=%v want %v", got, want)
	}
	if got := uid9scrPx(be2.Image(), 2, 2); got.A != 0 {
		t.Errorf("uncovered=%v want transparent", got)
	}
}

func TestUID9ScrClip(t *testing.T) {
	sheet := uid9scrSheet(40)
	rec := recording.NewRecorder(60, 60)
	rec.DrawRectangle(10, 10, 20, 20)
	rec.Clip()
	rec.DrawImage(sheet, 0, 0)
	img := uid9scrPlay(t, rec.FinishRecording()).Image()
	if got := uid9scrPx(img, 20, 20); got.A == 0 {
		t.Error("inside clip transparent")
	}
	for _, p := range [][2]int{{5, 5}, {35, 20}, {20, 35}} {
		if got := uid9scrPx(img, p[0], p[1]); got.A != 0 {
			t.Errorf("outside clip %v=%v", p, got)
		}
	}
}

func TestUID9ScrConsecutiveFrames(t *testing.T) {
	sheet := uid9scrSheet(40)
	frames := []struct {
		src  image.Rectangle
		want color.RGBA
	}{
		{image.Rect(0, 0, 20, 20), color.RGBA{255, 0, 0, 255}},
		{image.Rect(20, 0, 40, 20), color.RGBA{0, 255, 0, 255}},
		{image.Rect(0, 20, 20, 40), color.RGBA{0, 0, 255, 255}},
		{image.Rect(20, 20, 40, 40), color.RGBA{255, 255, 0, 255}},
	}
	rec := recording.NewRecorder(80, 80)
	for i, f := range frames {
		rec.DrawImageRect(sheet, f.src, recording.NewRect(float64(i*20), 0, 20, 20))
	}
	img := uid9scrPlay(t, rec.FinishRecording()).Image()
	for i, f := range frames {
		if got := uid9scrPx(img, i*20+10, 10); got != f.want {
			t.Errorf("frame %d=%v want %v", i, got, f.want)
		}
	}
}

func TestUID9ScrDirectSetTransformRotation(t *testing.T) {
	sheet := uid9scrSheet(40)
	be := NewBackend()
	_ = be.Begin(120, 120)
	be.SetTransform(recording.Identity())
	be.ctx.Push()
	// Use public matrix path: translate+rotate via SetTransform matrix.
	m := recording.Identity()
	m = m.Multiply(recording.Translate(60, 60))
	m = m.Multiply(recording.Rotate(math.Pi / 6))
	m = m.Multiply(recording.Scale(1.5, 1.5))
	be.SetTransform(m)
	be.DrawImage(sheet, recording.NewRect(0, 0, 40, 40),
		recording.NewRect(-20, -20, 40, 40), recording.DefaultImageOptions())
	be.ctx.Pop()
	if got := uid9scrPx(be.Image(), 60, 60); got.A == 0 {
		t.Errorf("direct transform center transparent: %v", got)
	}
}

func TestUID9ScrNilAndNonFinite(t *testing.T) {
	be := NewBackend()
	_ = be.Begin(10, 10)
	var typedNil *image.RGBA
	be.DrawImage(typedNil, recording.Rect{}, recording.NewRect(0, 0, 4, 4), recording.DefaultImageOptions())
	be.DrawImage(uid9scrSheet(4), recording.Rect{MinX: math.NaN()}, recording.NewRect(0, 0, 4, 4), recording.DefaultImageOptions())
	be.DrawImage(uid9scrSheet(4), recording.NewRect(0, 0, 4, 4),
		recording.Rect{MinX: 0, MinY: 0, MaxX: math.Inf(1), MaxY: 4}, recording.DefaultImageOptions())
}

func TestUID9ScrStateRestored(t *testing.T) {
	be := NewBackend()
	_ = be.Begin(20, 20)
	before := be.ctx.GetTransform()
	be.DrawImage(uid9scrSheet(4), recording.NewRect(0, 0, 4, 4),
		recording.NewRect(0, 0, 8, 8), recording.DefaultImageOptions())
	after := be.ctx.GetTransform()
	if before != after {
		t.Errorf("CTM leaked: before %v after %v", before, after)
	}
}

func TestUID9ScrDeviceScale(t *testing.T) {
	sheet := uid9scrSheet(20)
	be := NewBackendWithScale(2)
	_ = be.Begin(20, 20) // 40x40 physical
	be.DrawImage(sheet, recording.NewRect(0, 0, 20, 20),
		recording.NewRect(0, 0, 10, 10), recording.DefaultImageOptions())
	img := be.Image()
	if b := img.Bounds(); b.Dx() != 40 || b.Dy() != 40 {
		t.Fatalf("physical bounds=%v want 40x40", b)
	}
	// dst world 0..10 -> physical 0..20; green quadrant src(10,0,20,10) maps
	// to world (5,0)-(10,5) -> physical ~(10,0)-(20,10).
	if got, want := uid9scrPx(img, 15, 5), (color.RGBA{0, 255, 0, 255}); got != want {
		t.Errorf("scale2 green=%v want %v", got, want)
	}
	if got := uid9scrPx(img, 30, 30); got.A != 0 {
		t.Errorf("outside physical frame=%v want transparent", got)
	}
}

// FillRect resets the backend CTM via applyDeviceScale; the image command
// carries its own world affine and must not depend on the stale CTM.
func TestUID9ScrFillThenImageUnderTransform(t *testing.T) {
	sheet := uid9scrSheet(40)
	rec := recording.NewRecorder(80, 80)
	rec.Translate(10, 10)
	rec.SetFillRGBA(1, 1, 1, 1)
	rec.FillRectangle(0, 0, 5, 5) // clobbers raster CTM to device scale
	rec.DrawImageRect(sheet, image.Rect(20, 0, 40, 20), recording.NewRect(0, 0, 20, 20))
	img := uid9scrPlay(t, rec.FinishRecording()).Image()
	// frame should be at world (10,10)-(30,30); center (20,20) green.
	if got, want := uid9scrPx(img, 20, 20), (color.RGBA{0, 255, 0, 255}); got != want {
		t.Errorf("center=%v want %v", got, want)
	}
	if got := uid9scrPx(img, 5, 5); got.A != 0 {
		t.Errorf("far corner %v want transparent", got)
	}
}

func TestUID9ScrDirectMirrorFallback(t *testing.T) {
	sheet := uid9scrSheet(40)
	be := NewBackend()
	_ = be.Begin(80, 80)
	// Reversed destination MinX>MaxX, no explicit transform -> fallback path.
	be.DrawImage(sheet, recording.NewRect(0, 0, 40, 40),
		recording.Rect{MinX: 50, MinY: 10, MaxX: 30, MaxY: 30},
		recording.DefaultImageOptions())
	// dst spans x 30..50 with horizontal mirror: src left (red) lands right.
	if got, want := uid9scrPx(be.Image(), 45, 15), (color.RGBA{255, 0, 0, 255}); got != want {
		t.Errorf("mirror-right=%v want red %v", got, want)
	}
	if got, want := uid9scrPx(be.Image(), 35, 15), (color.RGBA{0, 255, 0, 255}); got != want {
		t.Errorf("mirror-left=%v want green %v", got, want)
	}
}

func TestUID9ScrDirectZeroSrcFullImage(t *testing.T) {
	sheet := uid9scrSheet(8)
	be := NewBackend()
	_ = be.Begin(8, 8)
	be.DrawImage(sheet, recording.Rect{}, recording.NewRect(0, 0, 8, 8),
		recording.DefaultImageOptions())
	if got, want := uid9scrPx(be.Image(), 2, 2), (color.RGBA{255, 0, 0, 255}); got != want {
		t.Errorf("red=%v want %v", got, want)
	}
	if got, want := uid9scrPx(be.Image(), 6, 2), (color.RGBA{0, 255, 0, 255}); got != want {
		t.Errorf("green=%v want %v", got, want)
	}
}

func TestUID9ScrNearestFilter(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 1))
	src.SetRGBA(0, 0, color.RGBA{A: 255})
	src.SetRGBA(1, 0, color.RGBA{R: 255, G: 255, B: 255, A: 255})
	be := NewBackend()
	_ = be.Begin(8, 1)
	be.DrawImage(src, recording.NewRect(0, 0, 2, 1), recording.NewRect(0, 0, 8, 1),
		recording.ImageOptions{Interpolation: recording.InterpolationNearest, Alpha: 1})
	out := be.Image().(*image.RGBA)
	if c := out.RGBAAt(0, 0); c.A != 255 {
		t.Errorf("first=%v", c)
	}
	if c := out.RGBAAt(7, 0); c.R != 255 || c.G != 255 {
		t.Errorf("last=%v want white", c)
	}
}

func TestUID9ScrTransparentEdgeOnWhite(t *testing.T) {
	be := NewBackend()
	_ = be.Begin(8, 1)
	be.FillRect(recording.NewRect(0, 0, 8, 1), recording.NewSolidBrush(gg.White))
	src := image.NewRGBA(image.Rect(0, 0, 8, 1))
	for x := 0; x < 4; x++ {
		src.SetRGBA(x, 0, color.RGBA{R: 255, A: 255})
	}
	be.DrawImage(src, recording.NewRect(0, 0, 8, 1), recording.NewRect(0, 0, 8, 1),
		recording.ImageOptions{Interpolation: recording.InterpolationBilinear, Alpha: 1})
	if c := be.Image().(*image.RGBA).RGBAAt(7, 0); c.R != 255 || c.G != 255 || c.B != 255 {
		t.Errorf("halo darkened background to %v", c)
	}
}

func TestUID9ScrHalfAlpha(t *testing.T) {
	be := NewBackend()
	_ = be.Begin(1, 1)
	be.FillRect(recording.NewRect(0, 0, 1, 1), recording.NewSolidBrush(gg.White))
	src := image.NewRGBA(image.Rect(0, 0, 1, 1))
	src.SetRGBA(0, 0, color.RGBA{R: 255, G: 0, B: 0, A: 128})
	be.DrawImage(src, recording.NewRect(0, 0, 1, 1), recording.NewRect(0, 0, 1, 1),
		recording.ImageOptions{Alpha: 0.5})
	c := be.Image().(*image.RGBA).RGBAAt(0, 0)
	// red over white at ~0.5 alpha -> pinkish, not pure red, not white.
	if c.R < 240 || c.G > 200 || c.B > 200 {
		t.Errorf("half-alpha blend unexpected %v", c)
	}
}

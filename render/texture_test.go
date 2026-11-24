// Copyright 2026 The gogpu Authors
// SPDX-License-Identifier: MIT

package render

import (
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/gogpu/gputypes"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// newCheckerboard builds an NxN RGBA checkerboard of opaque black/white cells.
func newCheckerboard(n, cell int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if (x/cell+y/cell)%2 == 0 {
				img.SetRGBA(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
			} else {
				img.SetRGBA(x, y, color.RGBA{R: 0, G: 0, B: 0, A: 255})
			}
		}
	}
	return img
}

// newTranslucentEdgeImage builds an image whose left half is opaque red and
// right half is fully transparent, with a premultiplied semi-transparent red
// column on the boundary. Transparent texels keep RGB zero (premultiplied
// contract) so bilinear filtering cannot produce dark halos.
func newTranslucentEdgeImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	mid := w / 2
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			switch {
			case x < mid-1:
				img.SetRGBA(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
			case x == mid-1:
				// 50% premultiplied red: (128, 0, 0, 128).
				img.SetRGBA(x, y, color.RGBA{R: 128, G: 0, B: 0, A: 128})
			default:
				img.SetRGBA(x, y, color.RGBA{R: 0, G: 0, B: 0, A: 0})
			}
		}
	}
	return img
}

// newPageImage builds a multi-page style atlas: four colored quadrants so a
// source-rect selection can address distinct "pages" independently.
func newPageImage(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	half := size / 2
	quadrants := [4]color.RGBA{
		{R: 255, A: 255},         // top-left red
		{G: 255, A: 255},         // top-right green
		{B: 255, A: 255},         // bottom-left blue
		{R: 255, G: 255, A: 255}, // bottom-right white
	}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			idx := 0
			if x >= half {
				idx++
			}
			if y >= half {
				idx += 2
			}
			img.SetRGBA(x, y, quadrants[idx])
		}
	}
	return img
}

// ---------------------------------------------------------------------------
// Independent reference sampler
// ---------------------------------------------------------------------------

// referenceSample is an independently written sampler (no shared code with
// textureSampler) implementing the same texel-center, clamp-to-edge,
// premultiplied-alpha semantics. Tests cross-check the production sampler
// against it over a grid of coordinates and both filters.
func referenceSample(img *image.RGBA, u, v float64, filter Filter, alpha float64) (r, g, b, a float64) {
	bounds := img.Bounds()
	minX, minY := bounds.Min.X, bounds.Min.Y
	maxX, maxY := bounds.Max.X-1, bounds.Max.Y-1

	fetch := func(x, y int) (float64, float64, float64, float64) {
		if x < minX {
			x = minX
		}
		if x > maxX {
			x = maxX
		}
		if y < minY {
			y = minY
		}
		if y > maxY {
			y = maxY
		}
		i := img.PixOffset(x, y)
		aa := float64(img.Pix[i+3])
		if aa == 0 {
			return 0, 0, 0, 0
		}
		return float64(img.Pix[i]), float64(img.Pix[i+1]), float64(img.Pix[i+2]), aa
	}

	gu := u - 0.5
	gv := v - 0.5
	if filter == FilterNearest {
		r, g, b, a = fetch(int(math.Round(gu)), int(math.Round(gv)))
	} else {
		ix := int(math.Floor(gu))
		iy := int(math.Floor(gv))
		fx := gu - math.Floor(gu)
		fy := gv - math.Floor(gv)
		r00, g00, b00, a00 := fetch(ix, iy)
		r10, g10, b10, a10 := fetch(ix+1, iy)
		r01, g01, b01, a01 := fetch(ix, iy+1)
		r11, g11, b11, a11 := fetch(ix+1, iy+1)
		w00 := (1 - fx) * (1 - fy)
		w10 := fx * (1 - fy)
		w01 := (1 - fx) * fy
		w11 := fx * fy
		r = r00*w00 + r10*w10 + r01*w01 + r11*w11
		g = g00*w00 + g10*w10 + g01*w01 + g11*w11
		b = b00*w00 + b10*w10 + b01*w01 + b11*w11
		a = a00*w00 + a10*w10 + a01*w01 + a11*w11
	}
	r *= alpha
	g *= alpha
	b *= alpha
	a *= alpha
	return
}

func TestTextureSamplerMatchesReference(t *testing.T) {
	img := newCheckerboard(8, 2)
	for _, filter := range []Filter{FilterNearest, FilterBilinear} {
		sampler := newTextureSampler(img, filter, 1.0)
		for iy := 0; iy < 16; iy++ {
			for ix := 0; ix < 16; ix++ {
				u := float64(ix) * 0.5
				v := float64(iy) * 0.5
				got := sampler.sample(u, v)
				rr, rg, rb, ra := referenceSample(img, u, v, filter, 1.0)
				if math.Abs(got.r-rr) > 1.01 || math.Abs(got.g-rg) > 1.01 ||
					math.Abs(got.b-rb) > 1.01 || math.Abs(got.a-ra) > 1.01 {
					t.Fatalf("filter=%d at (%v,%v): sampler=%+v reference=(%v,%v,%v,%v)",
						filter, u, v, got, rr, rg, rb, ra)
				}
			}
		}
	}
}

func TestTextureSamplerAlphaMatchesReference(t *testing.T) {
	img := newTranslucentEdgeImage(8, 4)
	const alpha = 0.25
	sampler := newTextureSampler(img, FilterBilinear, alpha)
	for _, p := range [][2]float64{{1.5, 1.5}, {3.2, 2.7}, {6.8, 3.1}} {
		got := sampler.sample(p[0], p[1])
		rr, rg, rb, ra := referenceSample(img, p[0], p[1], FilterBilinear, alpha)
		if math.Abs(got.r-rr) > 1.01 || math.Abs(got.a-ra) > 1.01 ||
			math.Abs(got.g-rg) > 1.01 || math.Abs(got.b-rb) > 1.01 {
			t.Fatalf("at %v: sampler=%+v reference=(%v,%v,%v,%v)", p, got, rr, rg, rb, ra)
		}
	}
}

// ---------------------------------------------------------------------------
// Capabilities
// ---------------------------------------------------------------------------

func TestSoftwareRendererTextureCapabilities(t *testing.T) {
	caps := NewSoftwareRenderer().Capabilities()
	if !caps.SupportsTextures {
		t.Error("SupportsTextures should be true")
	}
	if caps.MaxTextureSize != maxSoftwareTextureSize {
		t.Errorf("MaxTextureSize = %d, want %d", caps.MaxTextureSize, maxSoftwareTextureSize)
	}
	if caps.IsGPU {
		t.Error("software renderer must not report GPU")
	}
}

// ---------------------------------------------------------------------------
// End-to-end draws
// ---------------------------------------------------------------------------

func renderImageOp(t *testing.T, w, h int, op ImageDrawOp) *image.RGBA {
	t.Helper()
	target := NewPixmapTarget(w, h)
	scene := NewScene()
	scene.DrawImageOp(op)
	if err := NewSoftwareRenderer().Render(target, scene); err != nil {
		t.Fatalf("Render error: %v", err)
	}
	return target.Image()
}

func TestDrawImageIdentityBlit(t *testing.T) {
	img := newCheckerboard(4, 2)
	out := renderImageOp(t, 8, 8, ImageDrawOp{
		Image:     img,
		DstMin:    image.Point{X: 2, Y: 2},
		DstWidth:  4,
		DstHeight: 4,
		Filter:    FilterNearest,
		Alpha:     1,
		Transform: IdentityTransform(),
	})
	// Blitted pixel must equal the source exactly (opaque, no scaling).
	got := out.RGBAAt(2, 2)
	want := img.RGBAAt(0, 0)
	if got != want {
		t.Errorf("blit pixel = %v, want %v", got, want)
	}
	// Untouched area stays transparent.
	if c := out.RGBAAt(0, 0); c.A != 0 {
		t.Errorf("outside pixel alpha = %d, want 0", c.A)
	}
}

func TestDrawImageSourceRectSelectsPage(t *testing.T) {
	img := newPageImage(8)
	// Select the green (top-right) quadrant and blit to the origin.
	out := renderImageOp(t, 4, 4, ImageDrawOp{
		Image:     img,
		SrcRect:   image.Rect(4, 0, 8, 4),
		DstMin:    image.Point{},
		DstWidth:  4,
		DstHeight: 4,
		Filter:    FilterNearest,
		Alpha:     1,
		Transform: IdentityTransform(),
	})
	c := out.RGBAAt(1, 1)
	if c.G != 255 || c.R != 0 || c.B != 0 || c.A != 255 {
		t.Errorf("green page pixel = %v, want opaque green", c)
	}
}

func TestDrawImageNearestUpscale(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
	img.SetRGBA(1, 0, color.RGBA{G: 255, A: 255})
	img.SetRGBA(0, 1, color.RGBA{B: 255, A: 255})
	img.SetRGBA(1, 1, color.RGBA{R: 255, G: 255, A: 255})

	out := renderImageOp(t, 4, 4, ImageDrawOp{
		Image:     img,
		DstMin:    image.Point{},
		DstWidth:  4,
		DstHeight: 4,
		Filter:    FilterNearest,
		Alpha:     1,
		Transform: IdentityTransform(),
	})
	// Each source texel covers a 2x2 block under 2x nearest upscale.
	if c := out.RGBAAt(0, 0); c.R != 255 || c.G != 0 {
		t.Errorf("block(0,0) = %v, want red", c)
	}
	if c := out.RGBAAt(3, 3); c.R != 255 || c.G != 255 {
		t.Errorf("block(1,1) = %v, want yellow", c)
	}
}

func TestDrawImageBilinearSmooths(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.SetRGBA(0, 0, color.RGBA{A: 255}) // black
	img.SetRGBA(1, 0, color.RGBA{R: 255, G: 255, B: 255, A: 255})

	out := renderImageOp(t, 4, 1, ImageDrawOp{
		Image:     img,
		DstMin:    image.Point{},
		DstWidth:  4,
		DstHeight: 1,
		Filter:    FilterBilinear,
		Alpha:     1,
		Transform: IdentityTransform(),
	})
	// Nearest would snap every pixel to one endpoint; bilinear yields an
	// intermediate gray somewhere in the middle.
	mid := out.RGBAAt(2, 0)
	if mid.R == 0 || mid.R == 255 {
		t.Errorf("expected interpolated gray, got %v", mid)
	}
}

func TestDrawImageFlip(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
	img.SetRGBA(1, 0, color.RGBA{G: 255, A: 255})

	// Horizontal flip via negative destination width, anchored at x=2.
	out := renderImageOp(t, 2, 1, ImageDrawOp{
		Image:     img,
		DstMin:    image.Point{X: 2},
		DstWidth:  -2,
		DstHeight: 1,
		Filter:    FilterNearest,
		Alpha:     1,
		Transform: IdentityTransform(),
	})
	if c := out.RGBAAt(0, 0); c.G != 255 {
		t.Errorf("flipped left pixel = %v, want green", c)
	}
	if c := out.RGBAAt(1, 0); c.R != 255 {
		t.Errorf("flipped right pixel = %v, want red", c)
	}
}

func TestDrawImageRotationAndScale(t *testing.T) {
	img := newCheckerboard(8, 4)
	// Rotate 90 degrees about the target center and scale to half size.
	const angle = math.Pi / 2
	cx, cy := 32.0, 32.0
	rot := AffineTransform{
		A: math.Cos(angle) * 0.5,
		B: -math.Sin(angle) * 0.5,
		C: cx - math.Cos(angle)*0.5*cx + math.Sin(angle)*0.5*cy,
		D: math.Sin(angle) * 0.5,
		E: math.Cos(angle) * 0.5,
		F: cy - math.Sin(angle)*0.5*cx - math.Cos(angle)*0.5*cy,
	}
	target := NewPixmapTarget(64, 64)
	scene := NewScene()
	scene.DrawImageOp(ImageDrawOp{
		Image:     img,
		DstMin:    image.Point{},
		DstWidth:  8,
		DstHeight: 8,
		Filter:    FilterBilinear,
		Alpha:     1,
		Transform: rot,
	})
	if err := NewSoftwareRenderer().Render(target, scene); err != nil {
		t.Fatalf("Render error: %v", err)
	}
	// The rotated/quad must paint some opaque pixels and leave corners clear.
	if c := target.Image().RGBAAt(0, 0); c.A != 0 {
		t.Errorf("corner should remain transparent, got %v", c)
	}
	// The rotated/scaled quad (8x8 source at 0.5 scale -> 4x4) must paint a
	// compact opaque region and leave the corners clear.
	opaque := 0
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			if target.Image().RGBAAt(x, y).A == 255 {
				opaque++
			}
		}
	}
	if opaque < 8 || opaque > 40 {
		t.Errorf("rotated quad opaque pixel count = %d, expected roughly 16", opaque)
	}
}

func TestDrawImageTranslucentEdgeNoDarkHalo(t *testing.T) {
	img := newTranslucentEdgeImage(8, 2)
	target := NewPixmapTarget(8, 2)
	// White opaque background so any dark fringe would be visible.
	target.Clear(color.White)
	scene := NewScene()
	scene.DrawImageOp(ImageDrawOp{
		Image:     img,
		DstMin:    image.Point{},
		DstWidth:  8,
		DstHeight: 2,
		Filter:    FilterBilinear,
		Alpha:     1,
		Transform: IdentityTransform(),
	})
	if err := NewSoftwareRenderer().Render(target, scene); err != nil {
		t.Fatalf("Render error: %v", err)
	}
	// The transparent region must keep the white background (premultiplied
	// sampling contributes zero RGB rather than black).
	c := target.Image().RGBAAt(7, 1)
	if c.R != 255 || c.G != 255 || c.B != 255 {
		t.Errorf("transparent region darkened: %v", c)
	}
}

func TestDrawImageOutOfTargetIsSafe(t *testing.T) {
	img := newCheckerboard(4, 2)
	target := NewPixmapTarget(8, 8)
	scene := NewScene()
	// Entirely outside, partially outside, and negative-offset draws.
	scene.DrawImageOp(ImageDrawOp{
		Image: img, DstMin: image.Point{X: 100, Y: 100},
		DstWidth: 4, DstHeight: 4, Filter: FilterNearest, Alpha: 1,
		Transform: IdentityTransform(),
	})
	scene.DrawImageOp(ImageDrawOp{
		Image: img, DstMin: image.Point{X: -2, Y: -2},
		DstWidth: 4, DstHeight: 4, Filter: FilterNearest, Alpha: 1,
		Transform: IdentityTransform(),
	})
	if err := NewSoftwareRenderer().Render(target, scene); err != nil {
		t.Fatalf("Render error: %v", err)
	}
	if c := target.Image().RGBAAt(0, 0); c.A == 0 {
		t.Error("partial out-of-target draw should still paint the in-bounds corner")
	}
}

func TestDrawImageMultiPageAtlas(t *testing.T) {
	img := newPageImage(8)
	target := NewPixmapTarget(8, 4)
	scene := NewScene()
	// Two pages side by side: green quadrant then blue quadrant.
	scene.DrawImageOp(ImageDrawOp{
		Image: img, SrcRect: image.Rect(4, 0, 8, 4),
		DstMin: image.Point{}, DstWidth: 4, DstHeight: 4,
		Filter: FilterNearest, Alpha: 1, Transform: IdentityTransform(),
	})
	scene.DrawImageOp(ImageDrawOp{
		Image: img, SrcRect: image.Rect(0, 4, 4, 8),
		DstMin: image.Point{X: 4}, DstWidth: 4, DstHeight: 4,
		Filter: FilterNearest, Alpha: 1, Transform: IdentityTransform(),
	})
	if err := NewSoftwareRenderer().Render(target, scene); err != nil {
		t.Fatalf("Render error: %v", err)
	}
	if c := target.Image().RGBAAt(1, 1); c.G != 255 || c.R != 0 {
		t.Errorf("first page = %v, want green", c)
	}
	if c := target.Image().RGBAAt(5, 1); c.B != 255 || c.R != 0 {
		t.Errorf("second page = %v, want blue", c)
	}
}

func TestDrawImageNonZeroMinSubImage(t *testing.T) {
	// A SubImage has non-zero Bounds().Min; sampler coordinates must be
	// relative to that origin or the blit reads the wrong texels.
	base := image.NewRGBA(image.Rect(0, 0, 8, 8))
	base.SetRGBA(5, 5, color.RGBA{G: 255, A: 255})
	sub := base.SubImage(image.Rect(4, 4, 8, 8)).(*image.RGBA)

	out := renderImageOp(t, 4, 4, ImageDrawOp{
		Image:     sub,
		SrcRect:   sub.Bounds(),
		DstMin:    image.Point{},
		DstWidth:  4,
		DstHeight: 4,
		Filter:    FilterNearest,
		Alpha:     1,
		Transform: IdentityTransform(),
	})
	// The green texel at sub-local (1,1) must land at target (1,1).
	if c := out.RGBAAt(1, 1); c.G != 255 || c.A != 255 {
		t.Errorf("subimage texel = %v, want opaque green", c)
	}
}

func TestDrawImagePaddedStride(t *testing.T) {
	// Construct an RGBA image whose stride has padding (stride > width*4).
	// The renderer must normalize it rather than index the padding bytes.
	const w, h = 4, 2
	padded := &image.RGBA{
		Pix:    make([]byte, (w*4+8)*h),
		Stride: w*4 + 8,
		Rect:   image.Rect(0, 0, w, h),
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			padded.SetRGBA(x, y, color.RGBA{B: 255, A: 255})
		}
	}

	out := renderImageOp(t, w, h, ImageDrawOp{
		Image:     padded,
		DstMin:    image.Point{},
		DstWidth:  w,
		DstHeight: h,
		Filter:    FilterNearest,
		Alpha:     1,
		Transform: IdentityTransform(),
	})
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if c := out.RGBAAt(x, y); c.B != 255 || c.A != 255 {
				t.Fatalf("padded-stride pixel (%d,%d) = %v, want opaque blue", x, y, c)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Validation / safe degradation
// ---------------------------------------------------------------------------

func TestDrawImageValidationErrors(t *testing.T) {
	good := newCheckerboard(4, 2)
	cases := []struct {
		name string
		op   ImageDrawOp
	}{
		{"nil image", ImageDrawOp{DstWidth: 4, DstHeight: 4, Transform: IdentityTransform()}},
		{"empty image", ImageDrawOp{Image: image.NewRGBA(image.Rect(0, 0, 0, 0)), DstWidth: 4, DstHeight: 4, Transform: IdentityTransform()}},
		{"zero dst width", ImageDrawOp{Image: good, DstWidth: 0, DstHeight: 4, Transform: IdentityTransform()}},
		{"tiny dst", ImageDrawOp{Image: good, DstWidth: 1e-9, DstHeight: 4, Transform: IdentityTransform()}},
		{"non-finite transform", ImageDrawOp{Image: good, DstWidth: 4, DstHeight: 4, Transform: AffineTransform{A: math.NaN(), E: 1}}},
		{"singular transform", ImageDrawOp{Image: good, DstWidth: 4, DstHeight: 4, Transform: AffineTransform{A: 1, E: 0}}},
		{"non-finite destination", ImageDrawOp{Image: good, DstWidth: math.Inf(1), DstHeight: 4, Transform: IdentityTransform()}},
		{"oversized image", ImageDrawOp{Image: image.NewRGBA(image.Rect(0, 0, maxSoftwareTextureSize+1, 4)), DstWidth: 4, DstHeight: 4, Transform: IdentityTransform()}},
		{"out of range src", ImageDrawOp{Image: good, SrcRect: image.Rect(50, 50, 60, 60), DstWidth: 4, DstHeight: 4, Transform: IdentityTransform()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := NewPixmapTarget(8, 8)
			scene := NewScene()
			scene.DrawImageOp(tc.op)
			err := NewSoftwareRenderer().Render(target, scene)
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}
			// Target must remain untouched (all transparent).
			for y := 0; y < 8; y++ {
				for x := 0; x < 8; x++ {
					if target.Image().RGBAAt(x, y).A != 0 {
						t.Fatalf("invalid draw modified target at (%d,%d)", x, y)
					}
				}
			}
		})
	}
}

func TestRenderContinuesAfterBadImage(t *testing.T) {
	good := newCheckerboard(4, 2)
	target := NewPixmapTarget(8, 8)
	scene := NewScene()
	scene.DrawImageOp(ImageDrawOp{
		Image:     nil,
		DstWidth:  4,
		DstHeight: 4,
		Transform: IdentityTransform(),
	})
	scene.DrawImageOp(ImageDrawOp{
		Image:     good,
		DstMin:    image.Point{X: 2, Y: 2},
		DstWidth:  4,
		DstHeight: 4,
		Filter:    FilterNearest,
		Alpha:     1,
		Transform: IdentityTransform(),
	})
	err := NewSoftwareRenderer().Render(target, scene)
	if err == nil {
		t.Fatal("expected error for the bad draw")
	}
	// The good draw must still land.
	if target.Image().RGBAAt(3, 3).A == 0 {
		t.Error("valid draw after invalid draw was skipped")
	}
}

func TestImageReferenceNotMutatedByReplay(t *testing.T) {
	img := newCheckerboard(4, 2)
	op := ImageDrawOp{
		Image:     img,
		DstMin:    image.Point{},
		DstWidth:  4,
		DstHeight: 4,
		Filter:    FilterBilinear,
		Alpha:     0.5,
		Transform: IdentityTransform(),
	}
	scene := NewScene()
	scene.DrawImageOp(op)
	renderer := NewSoftwareRenderer()
	for i := 0; i < 3; i++ {
		target := NewPixmapTarget(4, 4)
		if err := renderer.Render(target, scene); err != nil {
			t.Fatalf("render %d: %v", i, err)
		}
	}
	// Source pixels must be identical after repeated renders.
	want := newCheckerboard(4, 2)
	for i := range img.Pix {
		if img.Pix[i] != want.Pix[i] {
			t.Fatalf("source image mutated at byte %d: %d != %d", i, img.Pix[i], want.Pix[i])
		}
	}
}

func TestDrawImageStrideSafety(t *testing.T) {
	// A target whose backing buffer is shorter than stride*height must be
	// rejected rather than driving out-of-bounds writes.
	img := newCheckerboard(4, 2)
	target := NewPixmapTarget(8, 8)
	scene := NewScene()
	scene.DrawImageOp(ImageDrawOp{
		Image: img, DstMin: image.Point{}, DstWidth: 4, DstHeight: 4,
		Filter: FilterNearest, Alpha: 1, Transform: IdentityTransform(),
	})
	renderer := NewSoftwareRenderer()
	// Normal render succeeds.
	if err := renderer.Render(target, scene); err != nil {
		t.Fatalf("normal render: %v", err)
	}
	// Tamper with the target via a thin wrapper to simulate buffer corruption.
	bad := &shortBufferTarget{inner: target}
	if err := renderer.Render(bad, scene); err == nil {
		t.Fatal("expected error for undersized buffer, got nil")
	}
}

// shortBufferTarget presents an undersized pixel buffer with a large stride.
type shortBufferTarget struct {
	inner RenderTarget
}

func (t *shortBufferTarget) Width() int                     { return t.inner.Width() }
func (t *shortBufferTarget) Height() int                    { return t.inner.Height() }
func (t *shortBufferTarget) Format() gputypes.TextureFormat { return t.inner.Format() }
func (t *shortBufferTarget) TextureView() TextureView       { return t.inner.TextureView() }
func (t *shortBufferTarget) Stride() int                    { return t.inner.Stride() }
func (t *shortBufferTarget) Pixels() []byte {
	return t.inner.Pixels()[:4] // deliberately too short
}

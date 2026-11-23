// Copyright 2026 The gogpu Authors
// SPDX-License-Identifier: MIT

package surface

import (
	"image"
	"image/color"
	"math"
	"testing"
)

// pixel is an 8-bit premultiplied RGBA expectation.
type pixel struct {
	r, g, b, a uint8
}

// customColorImage only implements image.Image via At (no concrete
// *image.RGBA/*image.NRGBA fast path), exercising the color.Color fallback.
type customColorImage struct {
	bounds image.Rectangle
	c      color.Color
}

func (m *customColorImage) ColorModel() color.Model { return color.NRGBAModel }
func (m *customColorImage) Bounds() image.Rectangle { return m.bounds }
func (m *customColorImage) At(_, _ int) color.Color {
	return m.c
}

func newNRGBASolid(c color.NRGBA) *image.NRGBA {
	const size = 4
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

func newRGBASolid(size int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// TestDrawImagePremultipliedTable verifies per-channel output for every
// supported source kind against fixed expected pixel tables.
func TestDrawImagePremultipliedTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		src     image.Image
		opts    *DrawImageOptions
		dst     pixel // surface background before drawing
		want    pixel
		wantAtX int
		wantAtY int
	}{
		{
			name: "nrgba_opaque_red_over_white",
			src:  newNRGBASolid(color.NRGBA{R: 255, G: 0, B: 0, A: 255}),
			dst:  pixel{255, 255, 255, 255},
			want: pixel{255, 0, 0, 255},
		},
		{
			name: "nrgba_half_red_over_white",
			src:  newNRGBASolid(color.NRGBA{R: 255, G: 0, B: 0, A: 128}),
			dst:  pixel{255, 255, 255, 255},
			want: pixel{255, 127, 127, 255},
		},
		{
			name: "nrgba_half_red_over_transparent",
			src:  newNRGBASolid(color.NRGBA{R: 255, G: 0, B: 0, A: 128}),
			dst:  pixel{0, 0, 0, 0},
			want: pixel{128, 0, 0, 128},
		},
		{
			// Straight (200,100,50,128) premultiplies to (100,50,25,128).
			name: "generic_color_nrgba_half_over_white",
			src: &customColorImage{
				bounds: image.Rect(0, 0, 4, 4),
				c:      color.NRGBA{R: 200, G: 100, B: 50, A: 128},
			},
			dst:  pixel{255, 255, 255, 255},
			want: pixel{227, 177, 152, 255},
		},
		{
			name: "rgba64_premul_half_red_over_white",
			src: &customColorImage{
				bounds: image.Rect(0, 0, 4, 4),
				// Premultiplied 16-bit half-strength red.
				c: color.RGBA64{R: 32768, G: 0, B: 0, A: 32768},
			},
			opts: nil,
			dst:  pixel{255, 255, 255, 255},
			want: pixel{255, 127, 127, 255},
		},
		{
			name: "rgba_premul_half_red_over_white",
			src:  newRGBASolid(4, color.RGBA{R: 128, G: 0, B: 0, A: 128}),
			dst:  pixel{255, 255, 255, 255},
			want: pixel{255, 127, 127, 255},
		},
		{
			name: "rgba_premul_half_red_over_transparent",
			src:  newRGBASolid(4, color.RGBA{R: 128, G: 0, B: 0, A: 128}),
			dst:  pixel{0, 0, 0, 0},
			want: pixel{128, 0, 0, 128},
		},
		{
			name: "opacity_half_on_nrgba_half_red",
			src:  newNRGBASolid(color.NRGBA{R: 255, G: 0, B: 0, A: 128}),
			opts: &DrawImageOptions{Alpha: 0.5},
			dst:  pixel{255, 255, 255, 255},
			// (128,0,0,128) scaled by 0.5 -> (64,0,0,64), then over white.
			want: pixel{255, 191, 191, 255},
		},
		{
			name: "opacity_above_one_clamps_to_one",
			src:  newNRGBASolid(color.NRGBA{R: 255, G: 0, B: 0, A: 128}),
			opts: &DrawImageOptions{Alpha: 7.5},
			dst:  pixel{255, 255, 255, 255},
			want: pixel{255, 127, 127, 255},
		},
		{
			name: "opacity_positive_inf_clamps_to_one",
			src:  newNRGBASolid(color.NRGBA{R: 255, G: 0, B: 0, A: 128}),
			opts: &DrawImageOptions{Alpha: math.Inf(1)},
			dst:  pixel{255, 255, 255, 255},
			want: pixel{255, 127, 127, 255},
		},
		{
			name: "opacity_negative_inf_is_noop",
			src:  newNRGBASolid(color.NRGBA{R: 255, G: 0, B: 0, A: 255}),
			opts: &DrawImageOptions{Alpha: math.Inf(-1)},
			dst:  pixel{10, 20, 30, 255},
			want: pixel{10, 20, 30, 255},
		},
		{
			name: "opacity_nan_is_noop",
			src:  newNRGBASolid(color.NRGBA{R: 255, G: 0, B: 0, A: 255}),
			opts: &DrawImageOptions{Alpha: math.NaN()},
			dst:  pixel{10, 20, 30, 255},
			want: pixel{10, 20, 30, 255},
		},
		{
			name: "opacity_negative_is_noop",
			src:  newNRGBASolid(color.NRGBA{R: 255, G: 0, B: 0, A: 255}),
			opts: &DrawImageOptions{Alpha: -0.25},
			dst:  pixel{10, 20, 30, 255},
			want: pixel{10, 20, 30, 255},
		},
		{
			// A zero-valued field means "unset", i.e. fully opaque.
			name: "opacity_zero_value_defaults_to_opaque",
			src:  newNRGBASolid(color.NRGBA{R: 255, G: 0, B: 0, A: 255}),
			opts: &DrawImageOptions{Alpha: 0},
			dst:  pixel{255, 255, 255, 255},
			want: pixel{255, 0, 0, 255},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := NewImageSurface(12, 12)
			defer func() { _ = s.Close() }()
			s.Clear(color.RGBA{R: tc.dst.r, G: tc.dst.g, B: tc.dst.b, A: tc.dst.a})

			s.DrawImage(tc.src, Pt(2, 2), tc.opts)

			got := s.Snapshot().RGBAAt(3, 3)
			if got.R != tc.want.r || got.G != tc.want.g ||
				got.B != tc.want.b || got.A != tc.want.a {
				t.Errorf("pixel = (%d,%d,%d,%d), want (%d,%d,%d,%d)",
					got.R, got.G, got.B, got.A,
					tc.want.r, tc.want.g, tc.want.b, tc.want.a)
			}
		})
	}
}

// TestDrawImageTransparentEdgeNoRGBLeak checks that fully transparent source
// pixels never write RGB, on both white and transparent destinations.
func TestDrawImageTransparentEdgeNoRGBLeak(t *testing.T) {
	t.Parallel()

	for _, bg := range []pixel{
		{255, 255, 255, 255},
		{0, 0, 0, 0},
	} {
		bg := bg
		t.Run("bg", func(t *testing.T) {
			t.Parallel()

			// 2x2 NRGBA: transparent top row (RGB deliberately non-zero),
			// opaque red bottom row.
			src := image.NewNRGBA(image.Rect(0, 0, 2, 2))
			src.SetNRGBA(0, 0, color.NRGBA{R: 255, G: 255, B: 255, A: 0})
			src.SetNRGBA(1, 0, color.NRGBA{R: 123, G: 45, B: 200, A: 0})
			src.SetNRGBA(0, 1, color.NRGBA{R: 255, G: 0, B: 0, A: 255})
			src.SetNRGBA(1, 1, color.NRGBA{R: 255, G: 0, B: 0, A: 255})

			s := NewImageSurface(6, 6)
			defer func() { _ = s.Close() }()
			s.Clear(color.RGBA{R: bg.r, G: bg.g, B: bg.b, A: bg.a})

			s.DrawImage(src, Pt(1, 1), &DrawImageOptions{Alpha: 0.5})

			snap := s.Snapshot()
			for _, p := range []image.Point{{X: 1, Y: 1}, {X: 2, Y: 1}} {
				got := snap.RGBAAt(p.X, p.Y)
				if got.R != bg.r || got.G != bg.g || got.B != bg.b || got.A != bg.a {
					t.Errorf("transparent pixel at %v = (%d,%d,%d,%d), want unchanged (%d,%d,%d,%d)",
						p, got.R, got.G, got.B, got.A, bg.r, bg.g, bg.b, bg.a)
				}
			}
			// Opaque red row at 50% opacity becomes premultiplied
			// (128,0,0,128); the backdrop contributes G/B, never the source.
			got := snap.RGBAAt(1, 2)
			want := pixel{128, 0, 0, 128}
			if bg.a == 255 {
				// Source-over the opaque white backdrop.
				want = pixel{255, 127, 127, 255}
			}
			if got.R != want.r || got.G != want.g || got.B != want.b || got.A != want.a {
				t.Errorf("scaled opaque red = (%d,%d,%d,%d), want (%d,%d,%d,%d)",
					got.R, got.G, got.B, got.A, want.r, want.g, want.b, want.a)
			}
		})
	}
}

// TestDrawImageNonZeroOrigin verifies sources with non-zero bounds origins
// map their top-left source pixel to the destination point.
func TestDrawImageNonZeroOrigin(t *testing.T) {
	t.Parallel()

	src := image.NewNRGBA(image.Rect(10, 20, 13, 23))
	for y := 20; y < 23; y++ {
		for x := 10; x < 13; x++ {
			src.SetNRGBA(x, y, color.NRGBA{R: 0, G: 255, B: 0, A: 255})
		}
	}

	s := NewImageSurface(8, 8)
	defer func() { _ = s.Close() }()
	s.Clear(color.RGBA{R: 255, G: 255, B: 255, A: 255})

	s.DrawImage(src, Pt(4, 5), nil)

	snap := s.Snapshot()
	// Top-left source pixel lands at (4,5).
	if got := snap.RGBAAt(4, 5); got != (color.RGBA{R: 0, G: 255, B: 0, A: 255}) {
		t.Errorf("origin pixel = %v, want opaque green", got)
	}
	// Bottom-right source pixel lands at (6,7).
	if got := snap.RGBAAt(6, 7); got != (color.RGBA{R: 0, G: 255, B: 0, A: 255}) {
		t.Errorf("corner pixel = %v, want opaque green", got)
	}
	// Pixel just outside the blit is untouched.
	if got := snap.RGBAAt(3, 5); got != (color.RGBA{R: 255, G: 255, B: 255, A: 255}) {
		t.Errorf("outside pixel = %v, want white", got)
	}
}

// TestDrawImageRepeatedNoDarkLines draws the same translucent source several
// times; correct premultiplied source-over must never darken the red-only
// channels (G/B stay zero).
func TestDrawImageRepeatedNoDarkLines(t *testing.T) {
	t.Parallel()

	src := newRGBASolid(2, color.RGBA{R: 128, G: 0, B: 0, A: 128})

	s := NewImageSurface(6, 6)
	defer func() { _ = s.Close() }()

	for i := 0; i < 4; i++ {
		s.DrawImage(src, Pt(2, 2), nil)
	}

	snap := s.Snapshot()
	got := snap.RGBAAt(3, 3)
	if got.G != 0 || got.B != 0 {
		t.Errorf("repeated draw introduced dark channels: %v", got)
	}
	if got.R < got.A || got.A != got.R {
		// Premultiplied pure red must keep R == A at every step.
		t.Errorf("premultiplied invariant broken after repeats: %v", got)
	}

	// A neighboring untouched pixel stays fully transparent.
	if got := snap.RGBAAt(0, 0); got != (color.RGBA{}) {
		t.Errorf("untouched pixel = %v, want zero RGBA", got)
	}
}

// TestDrawImageOutOfBoundsClipping checks partial overlaps clip cleanly
// without panics and only touch in-bounds pixels.
func TestDrawImageOutOfBoundsClipping(t *testing.T) {
	t.Parallel()

	src := newRGBASolid(4, color.RGBA{R: 0, G: 0, B: 255, A: 255})

	s := NewImageSurface(6, 6)
	defer func() { _ = s.Close() }()
	s.Clear(color.RGBA{R: 255, G: 255, B: 255, A: 255})

	s.DrawImage(src, Pt(-2, -2), nil) // only source pixel (2,2) maps to (0,0)
	s.DrawImage(src, Pt(4, 4), nil)   // only source pixel (0,0) maps to (4,4)

	snap := s.Snapshot()
	if got := snap.RGBAAt(0, 0); got != (color.RGBA{R: 0, G: 0, B: 255, A: 255}) {
		t.Errorf("top-left overlap = %v, want blue", got)
	}
	if got := snap.RGBAAt(4, 4); got != (color.RGBA{R: 0, G: 0, B: 255, A: 255}) {
		t.Errorf("bottom-right overlap = %v, want blue", got)
	}
	if got := snap.RGBAAt(3, 3); got != (color.RGBA{R: 255, G: 255, B: 255, A: 255}) {
		t.Errorf("unclipped pixel = %v, want white", got)
	}
	if got := snap.RGBAAt(5, 5); got != (color.RGBA{R: 0, G: 0, B: 255, A: 255}) {
		t.Errorf("inside second blit = %v, want blue", got)
	}
}

// TestDrawImageSrcRectAndEmptyInputs covers source rectangles, disjoint
// rects, empty images and nil sources.
func TestDrawImageSrcRectAndEmptyInputs(t *testing.T) {
	t.Parallel()

	src := newRGBASolid(6, color.RGBA{R: 0, G: 255, B: 0, A: 255})

	s := NewImageSurface(10, 10)
	defer func() { _ = s.Close() }()
	s.Clear(color.RGBA{R: 255, G: 255, B: 255, A: 255})

	// Sub-rect 2x2 starting at (3,3) maps to (1,1).
	rect := image.Rect(3, 3, 5, 5)
	s.DrawImage(src, Pt(1, 1), &DrawImageOptions{SrcRect: &rect})
	snap := s.Snapshot()
	if got := snap.RGBAAt(1, 1); got != (color.RGBA{R: 0, G: 255, B: 0, A: 255}) {
		t.Errorf("src rect pixel = %v, want green", got)
	}
	if got := snap.RGBAAt(3, 3); got != (color.RGBA{R: 255, G: 255, B: 255, A: 255}) {
		t.Errorf("outside src-rect blit = %v, want white", got)
	} // SrcRect fully outside the image: no-op.
	disjoint := image.Rect(100, 100, 102, 102)
	s.DrawImage(src, Pt(0, 0), &DrawImageOptions{SrcRect: &disjoint})

	// Empty image: no-op.
	s.DrawImage(image.NewRGBA(image.Rect(0, 0, 0, 0)), Pt(0, 0), nil)

	// Nil image: no-op.
	s.DrawImage(nil, Pt(0, 0), nil)
}

// TestDrawImageClosedSurfaceIsNoOp verifies that drawing after Close leaves
// no state and never panics (the backing image is released on Close).
func TestDrawImageClosedSurfaceIsNoOp(t *testing.T) {
	t.Parallel()

	s := NewImageSurface(4, 4)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	src := newRGBASolid(2, color.RGBA{R: 255, G: 0, B: 0, A: 255})
	s.DrawImage(src, Pt(0, 0), nil) // must not panic on the nil backing image
	s.DrawImage(src, Pt(0, 0), &DrawImageOptions{Alpha: math.NaN()})
}

// TestDrawImageNonFinitePositionIsNoOp guards float-to-int conversion of
// NaN/Inf destination coordinates.
func TestDrawImageNonFinitePositionIsNoOp(t *testing.T) {
	t.Parallel()

	src := newRGBASolid(2, color.RGBA{R: 255, G: 0, B: 0, A: 255})

	for _, at := range []Point{
		{X: math.NaN(), Y: 0},
		{X: 0, Y: math.NaN()},
		{X: math.Inf(1), Y: 0},
		{X: 0, Y: math.Inf(-1)},
	} {
		s := NewImageSurface(4, 4)
		s.Clear(color.RGBA{R: 255, G: 255, B: 255, A: 255})
		s.DrawImage(src, at, nil)
		snap := s.Snapshot()
		if got := snap.RGBAAt(0, 0); got != (color.RGBA{R: 255, G: 255, B: 255, A: 255}) {
			t.Errorf("position %v wrote pixels: got %v", at, got)
		}
		_ = s.Close()
	}
}

// TestClampDrawImageOpacity pins the sanitization table directly.
func TestClampDrawImageOpacity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   float64
		want float64
	}{
		{1, 1},
		{0, 0},
		{0.5, 0.5},
		{-1, 0},
		{-0.0001, 0},
		{2, 1},
		{100, 1},
		{math.Inf(1), 1},
		{math.Inf(-1), 0},
		{math.NaN(), 0},
	}
	for _, tc := range cases {
		if got := clampDrawOpacity(tc.in); got != tc.want {
			t.Errorf("clampDrawOpacity(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

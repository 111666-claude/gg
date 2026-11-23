// Copyright 2026 The gogpu Authors
// SPDX-License-Identifier: MIT

package surface

import (
	"image"
	"image/color"
	"math"
	"testing"
)

// checker2x2 returns a 2×2 semi-transparent checkerboard in straight alpha.
//
//	red    green
//	blue   white
//
// All four pixels use alpha 128 so premultiplied sampling must not change the
// straight RGB values when the alpha is uniform.
func checker2x2() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 128})
	img.SetNRGBA(1, 0, color.NRGBA{G: 255, A: 128})
	img.SetNRGBA(0, 1, color.NRGBA{B: 255, A: 128})
	img.SetNRGBA(1, 1, color.NRGBA{R: 255, G: 255, B: 255, A: 128})
	return img
}

// opaqueChecker2x2 is the same layout with fully opaque colors.
func opaqueChecker2x2() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	img.SetNRGBA(1, 0, color.NRGBA{G: 255, A: 255})
	img.SetNRGBA(0, 1, color.NRGBA{B: 255, A: 255})
	img.SetNRGBA(1, 1, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	return img
}

// opaqueOpts returns DrawImageOptions with fully opaque Alpha. The surface
// API's zero Alpha is historically transparent, so scaling tests must opt in
// to opacity explicitly.
func opaqueOpts(o *DrawImageOptions) *DrawImageOptions {
	if o.Alpha == 0 {
		o.Alpha = 1
	}
	return o
}

func newTransparentSurface(w, h int) *ImageSurface {
	s := NewImageSurface(w, h)
	s.Clear(color.Transparent)
	return s
}

func assertPixelNear(t *testing.T, got color.RGBA, want color.RGBA, tolerance int, label string) {
	t.Helper()
	near := func(a, b uint8) bool {
		d := int(a) - int(b)
		return d <= tolerance && d >= -tolerance
	}
	if !near(got.R, want.R) || !near(got.G, want.G) || !near(got.B, want.B) || !near(got.A, want.A) {
		t.Errorf("%s: pixel = %v, want %v (±%d)", label, got, want, tolerance)
	}
}

// TestDrawImageScaleNearest2x2Checker verifies nearest-neighbor upscaling of
// the semi-transparent 2×2 checkerboard produces exact, blocky texels.
func TestDrawImageScaleNearest2x2Checker(t *testing.T) {
	s := newTransparentSurface(4, 4)
	defer s.Close()

	src := checker2x2()
	dst := image.Rect(0, 0, 4, 4)
	s.DrawImage(src, Pt(0, 0), opaqueOpts(&DrawImageOptions{DstRect: &dst, Filter: FilterNearest}))

	snap := s.Snapshot()
	want := [2][2]color.RGBA{
		{{R: 255, A: 128}, {G: 255, A: 128}},
		{{B: 255, A: 128}, {R: 255, G: 255, B: 255, A: 128}},
	}
	for y := range 4 {
		for x := range 4 {
			assertPixelNear(t, snap.RGBAAt(x, y), want[y/2][x/2], 1, "nearest")
		}
	}
}

// TestDrawImageScaleBilinear2x2Checker checks bilinear interpolation against
// hand-computed premultiplied weights on the semi-transparent checkerboard.
func TestDrawImageScaleBilinear2x2Checker(t *testing.T) {
	s := newTransparentSurface(4, 4)
	defer s.Close()

	dst := image.Rect(0, 0, 4, 4)
	s.DrawImage(checker2x2(), Pt(0, 0), opaqueOpts(&DrawImageOptions{DstRect: &dst, Filter: FilterBilinear}))

	snap := s.Snapshot()

	// For destination pixel (1,1): sx=sy=0.75 -> tx=ty=0.25 with x0=y0=0.
	// RGB is a straight weighted average because every texel shares alpha 128.
	//   R = 255*(0.75*0.75) + 255*(0.25*0.25) = 159.375
	//   G = 255*(0.75*0.25) + 255*(0.25*0.25) = 63.75
	//   B = 255*(0.25*0.75) + 255*(0.25*0.25) = 63.75
	assertPixelNear(t, snap.RGBAAt(1, 1), color.RGBA{R: 159, G: 64, B: 64, A: 128}, 2, "bilinear (1,1)")

	// Corners stay dominated by their source texel.
	assertPixelNear(t, snap.RGBAAt(0, 0), color.RGBA{R: 255, A: 128}, 2, "bilinear top-left")
	assertPixelNear(t, snap.RGBAAt(3, 3), color.RGBA{R: 255, G: 255, B: 255, A: 128}, 2, "bilinear bottom-right")

	// The alpha channel is uniform in the source, so it must remain 128.
	for y := range 4 {
		for x := range 4 {
			if got := snap.RGBAAt(x, y).A; got < 126 || got > 130 {
				t.Errorf("alpha at (%d,%d) = %d, want ~128", x, y, got)
			}
		}
	}
}

// TestDrawImageScaleBicubic2x2Checker exercises bicubic interpolation and
// verifies it stays within the source gamut with a uniform alpha channel.
func TestDrawImageScaleBicubic2x2Checker(t *testing.T) {
	s := newTransparentSurface(6, 6)
	defer s.Close()

	dst := image.Rect(0, 0, 6, 6)
	s.DrawImage(checker2x2(), Pt(0, 0), opaqueOpts(&DrawImageOptions{DstRect: &dst, Filter: FilterBicubic}))

	snap := s.Snapshot()
	for y := range 6 {
		for x := range 6 {
			if c := snap.RGBAAt(x, y); c.A < 126 || c.A > 130 {
				t.Errorf("bicubic alpha at (%d,%d) = %d, want ~128", x, y, c.A)
			}
		}
	}

	// The destination center maps exactly onto the texel lattice crossing.
	// Both bilinear and bicubic weights are symmetric there, so the result is
	// the average of the four source colors: (127.5,127.5,127.5,128).
	// Here the 6×6 grid has no exact center pixel; use a 2→3 scale instead.
	s3 := newTransparentSurface(3, 3)
	defer s3.Close()
	dst3 := image.Rect(0, 0, 3, 3)
	s3.DrawImage(checker2x2(), Pt(0, 0), opaqueOpts(&DrawImageOptions{DstRect: &dst3, Filter: FilterBicubic}))
	// red 255, green 255, blue 255, white 255 average per channel is 127.5,
	// but with equal alpha it unpremultiplies to that average.
	assertPixelNear(t, s3.Snapshot().RGBAAt(1, 1),
		color.RGBA{R: 128, G: 128, B: 128, A: 128}, 2, "bicubic 2->3 center")
}

// TestDrawImageScaleCenterAverage verifies that bilinear and bicubic agree on
// the exact four-texel average at the center of a 2→3 upscale.
func TestDrawImageScaleCenterAverage(t *testing.T) {
	for _, filter := range []Filter{FilterBilinear, FilterBicubic} {
		t.Run(filter.String(), func(t *testing.T) {
			s := newTransparentSurface(3, 3)
			defer s.Close()

			dst := image.Rect(0, 0, 3, 3)
			s.DrawImage(opaqueChecker2x2(), Pt(0, 0), opaqueOpts(&DrawImageOptions{
				DstRect: &dst,
				Filter:  filter,
			}))

			// Average of red, green, blue, white = (127.5, 127.5, 127.5, 255).
			assertPixelNear(t, s.Snapshot().RGBAAt(1, 1),
				color.RGBA{R: 128, G: 128, B: 128, A: 255}, 2, "center")

			// Nearest at the same spot must pick source pixel (1,1) = white.
			sn := newTransparentSurface(3, 3)
			defer sn.Close()
			sn.DrawImage(opaqueChecker2x2(), Pt(0, 0), opaqueOpts(&DrawImageOptions{
				DstRect: &dst,
				Filter:  FilterNearest,
			}))
			assertPixelNear(t, sn.Snapshot().RGBAAt(1, 1), color.RGBA{R: 255, G: 255, B: 255, A: 255}, 0, "nearest center")
		})
	}
}

// TestDrawImageScaleDownBilinear checks minification weights on an opaque
// 4×4 source reduced to 2×2: each output pixel averages a 2×2 source block.
func TestDrawImageScaleDownBilinear(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := range 4 {
		for x := range 4 {
			v := uint8(16 * (y*4 + x))
			src.SetNRGBA(x, y, color.NRGBA{R: v, G: v, B: v, A: 255})
		}
	}

	s := newTransparentSurface(2, 2)
	defer s.Close()
	dst := image.Rect(0, 0, 2, 2)
	s.DrawImage(src, Pt(0, 0), opaqueOpts(&DrawImageOptions{DstRect: &dst, Filter: FilterBilinear}))

	snap := s.Snapshot()
	// Output (0,0) samples source (0.5,0.5) with tx=ty=0.5 -> mean of
	// pixels (0,0),(1,0),(0,1),(1,1) = (0+16+64+80)/4 = 40.
	assertPixelNear(t, snap.RGBAAt(0, 0), color.RGBA{R: 40, G: 40, B: 40, A: 255}, 1, "downscale (0,0)")
	// Output (1,1) samples source (3,3) with tx=ty=0.5 -> mean of
	// pixels (2,2),(3,2),(2,3),(3,3) = (160+176+224+240)/4 = 200.
	assertPixelNear(t, snap.RGBAAt(1, 1), color.RGBA{R: 200, G: 200, B: 200, A: 255}, 1, "downscale (1,1)")
}

// TestDrawImageNonZeroBounds verifies non-zero image.Bounds are honored for
// both the 1:1 path and scaled sampling.
func TestDrawImageNonZeroBounds(t *testing.T) {
	src := image.NewNRGBA(image.Rect(10, 20, 12, 22))
	src.SetNRGBA(10, 20, color.NRGBA{R: 255, A: 255})
	src.SetNRGBA(11, 20, color.NRGBA{G: 255, A: 255})
	src.SetNRGBA(10, 21, color.NRGBA{B: 255, A: 255})
	src.SetNRGBA(11, 21, color.NRGBA{R: 255, G: 255, B: 255, A: 255})

	t.Run("1:1", func(t *testing.T) {
		s := newTransparentSurface(4, 4)
		defer s.Close()
		s.DrawImage(src, Pt(1, 1), nil)
		snap := s.Snapshot()
		assertPixelNear(t, snap.RGBAAt(1, 1), color.RGBA{R: 255, A: 255}, 0, "top-left")
		assertPixelNear(t, snap.RGBAAt(2, 1), color.RGBA{G: 255, A: 255}, 0, "top-right")
		assertPixelNear(t, snap.RGBAAt(1, 2), color.RGBA{B: 255, A: 255}, 0, "bottom-left")
		assertPixelNear(t, snap.RGBAAt(2, 2), color.RGBA{R: 255, G: 255, B: 255, A: 255}, 0, "bottom-right")
	})

	t.Run("scaled", func(t *testing.T) {
		s := newTransparentSurface(4, 4)
		defer s.Close()
		dst := image.Rect(0, 0, 4, 4)
		s.DrawImage(src, Pt(0, 0), opaqueOpts(&DrawImageOptions{DstRect: &dst, Filter: FilterNearest}))
		snap := s.Snapshot()
		assertPixelNear(t, snap.RGBAAt(0, 0), color.RGBA{R: 255, A: 255}, 0, "top-left")
		assertPixelNear(t, snap.RGBAAt(3, 0), color.RGBA{G: 255, A: 255}, 0, "top-right")
		assertPixelNear(t, snap.RGBAAt(0, 3), color.RGBA{B: 255, A: 255}, 0, "bottom-left")
		assertPixelNear(t, snap.RGBAAt(3, 3), color.RGBA{R: 255, G: 255, B: 255, A: 255}, 0, "bottom-right")
	})
}

// TestDrawImageMirror verifies negative destination spans mirror the image
// (the "rectangle before/after rotation" check: a 180° rotation equals both
// axes mirrored, and swapped width/height scales a rotated aspect).
func TestDrawImageMirror(t *testing.T) {
	src := opaqueChecker2x2() // TL=red, TR=green, BL=blue, BR=white
	red := color.RGBA{R: 255, A: 255}
	green := color.RGBA{G: 255, A: 255}
	blue := color.RGBA{B: 255, A: 255}
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}

	tests := []struct {
		name string
		dst  image.Rectangle
		want [4]color.RGBA // row-major output over the 2×2 destination
	}{
		{
			name: "identity",
			dst:  image.Rect(0, 0, 2, 2),
			want: [4]color.RGBA{red, green, blue, white},
		},
		{
			// Span runs from x=2 down to x=0, mirroring horizontally.
			name: "horizontal",
			dst:  image.Rectangle{Min: image.Pt(2, 0), Max: image.Pt(0, 2)},
			want: [4]color.RGBA{green, red, white, blue},
		},
		{
			name: "vertical",
			dst:  image.Rectangle{Min: image.Pt(0, 2), Max: image.Pt(2, 0)},
			want: [4]color.RGBA{blue, white, red, green},
		},
		{
			name: "180",
			dst:  image.Rectangle{Min: image.Pt(2, 2), Max: image.Pt(0, 0)},
			want: [4]color.RGBA{white, blue, green, red},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTransparentSurface(4, 4)
			defer s.Close()
			dst := tt.dst
			s.DrawImage(src, Pt(0, 0), opaqueOpts(&DrawImageOptions{DstRect: &dst, Filter: FilterNearest}))
			snap := s.Snapshot()
			got := [4]color.RGBA{
				snap.RGBAAt(0, 0), snap.RGBAAt(1, 0),
				snap.RGBAAt(0, 1), snap.RGBAAt(1, 1),
			}
			for i := range got {
				assertPixelNear(t, got[i], tt.want[i], 0, tt.name)
			}
		})
	}

	t.Run("swapped-aspect", func(t *testing.T) {
		// A single-row source scaled into a single column (rotation-like
		// aspect swap) must still sample the full source width.
		row := image.NewNRGBA(image.Rect(0, 0, 3, 1))
		row.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
		row.SetNRGBA(1, 0, color.NRGBA{G: 255, A: 255})
		row.SetNRGBA(2, 0, color.NRGBA{B: 255, A: 255})

		s := newTransparentSurface(3, 1)
		defer s.Close()
		dst := image.Rect(0, 0, 3, 1)
		s.DrawImage(row, Pt(0, 0), opaqueOpts(&DrawImageOptions{DstRect: &dst, Filter: FilterNearest}))
		snap := s.Snapshot()
		assertPixelNear(t, snap.RGBAAt(0, 0), red, 0, "row start")
		assertPixelNear(t, snap.RGBAAt(1, 0), green, 0, "row middle")
		assertPixelNear(t, snap.RGBAAt(2, 0), blue, 0, "row end")
	})
}

// TestDrawImageRotatedAspect maps a landscape source onto a portrait
// destination (the rectangle "before and after rotation") and verifies both
// axes are remapped rather than cropped.
func TestDrawImageRotatedAspect(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 4, 2))
	for y := range 2 {
		for x := range 4 {
			src.SetNRGBA(x, y, color.NRGBA{R: uint8(60 * x), G: uint8(120 * y), A: 255})
		}
	}

	s := newTransparentSurface(2, 4)
	defer s.Close()
	dst := image.Rect(0, 0, 2, 4)
	s.DrawImage(src, Pt(0, 0), opaqueOpts(&DrawImageOptions{DstRect: &dst, Filter: FilterNearest}))

	snap := s.Snapshot()
	// Destination x maps to source x∈{1,3}; destination y spans source y 0..1.
	assertPixelNear(t, snap.RGBAAt(0, 0), color.RGBA{R: 60, A: 255}, 0, "portrait top-left")
	assertPixelNear(t, snap.RGBAAt(1, 0), color.RGBA{R: 180, A: 255}, 0, "portrait top-right")
	assertPixelNear(t, snap.RGBAAt(0, 3), color.RGBA{R: 60, G: 120, A: 255}, 0, "portrait bottom-left")
	assertPixelNear(t, snap.RGBAAt(1, 3), color.RGBA{R: 180, G: 120, A: 255}, 0, "portrait bottom-right")
}

// TestDrawImagePremultipliedEdges verifies interpolation happens in
// premultiplied space: a fully transparent neighbor must not darken the edge
// (no black fringe).
func TestDrawImagePremultipliedEdges(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	src.SetNRGBA(0, 0, color.NRGBA{R: 255, G: 255, A: 255})
	src.SetNRGBA(1, 0, color.NRGBA{A: 0})

	s := newTransparentSurface(4, 1)
	defer s.Close()
	dst := image.Rect(0, 0, 4, 1)
	s.DrawImage(src, Pt(0, 0), opaqueOpts(&DrawImageOptions{DstRect: &dst, Filter: FilterBilinear}))

	snap := s.Snapshot()
	// Destination x=1 samples sx=0.75, so 75% opaque yellow + 25% transparent.
	// Premultiplied filtering keeps straight RGB at pure yellow and scales
	// alpha to ~191. Straight-space averaging would yield ~(191,191,0).
	assertPixelNear(t, snap.RGBAAt(1, 0), color.RGBA{R: 255, G: 255, A: 191}, 2, "premultiplied edge")

	// The far side must fade toward transparent, not toward black.
	assertPixelNear(t, snap.RGBAAt(3, 0), color.RGBA{A: 0}, 1, "transparent end")
}

// TestDrawImageSrcRectSubset verifies a SrcRect subset maps to the whole
// destination so only the selected texels appear, including a mirrored subset.
func TestDrawImageSrcRectSubset(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 4, 1))
	for x := range 4 {
		src.SetNRGBA(x, 0, color.NRGBA{R: uint8(40 * x), A: 255})
	}
	// Source column values: 0, 40, 80, 120.

	t.Run("right-half", func(t *testing.T) {
		s := newTransparentSurface(2, 1)
		defer s.Close()
		srcRect := image.Rect(2, 0, 4, 1)
		dst := image.Rect(0, 0, 2, 1)
		s.DrawImage(src, Pt(0, 0), opaqueOpts(&DrawImageOptions{
			SrcRect: &srcRect,
			DstRect: &dst,
			Filter:  FilterNearest,
		}))
		snap := s.Snapshot()
		assertPixelNear(t, snap.RGBAAt(0, 0), color.RGBA{R: 80, A: 255}, 0, "src subset 0")
		assertPixelNear(t, snap.RGBAAt(1, 0), color.RGBA{R: 120, A: 255}, 0, "src subset 1")
	})

	t.Run("mirrored-subset", func(t *testing.T) {
		s := newTransparentSurface(2, 1)
		defer s.Close()
		// Descending source span mirrors the selected columns.
		srcRect := image.Rectangle{Min: image.Pt(4, 0), Max: image.Pt(2, 1)}
		dst := image.Rectangle{Min: image.Pt(2, 0), Max: image.Pt(0, 1)}
		s.DrawImage(src, Pt(0, 0), opaqueOpts(&DrawImageOptions{
			SrcRect: &srcRect,
			DstRect: &dst,
			Filter:  FilterNearest,
		}))
		snap := s.Snapshot()
		// Both source and destination are mirrored, so the pair cancels out.
		assertPixelNear(t, snap.RGBAAt(0, 0), color.RGBA{R: 80, A: 255}, 0, "double mirror 0")
		assertPixelNear(t, snap.RGBAAt(1, 0), color.RGBA{R: 120, A: 255}, 0, "double mirror 1")
	})
}

// TestDrawImageScaleSourceUnchanged verifies the source image is never mutated.
func TestDrawImageScaleSourceUnchanged(t *testing.T) {
	src := image.NewRGBA(image.Rect(3, 7, 5, 9))
	for y := 7; y < 9; y++ {
		for x := 3; x < 5; x++ {
			src.SetRGBA(x, y, color.RGBA{R: uint8(x * 10), G: uint8(y * 10), B: 40, A: 200})
		}
	}
	before := make([]byte, len(src.Pix))
	copy(before, src.Pix)

	s := newTransparentSurface(8, 8)
	defer s.Close()
	dst := image.Rect(0, 0, 8, 8)
	s.DrawImage(src, Pt(0, 0), opaqueOpts(&DrawImageOptions{DstRect: &dst, Filter: FilterBicubic}))

	for i := range before {
		if src.Pix[i] != before[i] {
			t.Fatalf("source pixel buffer mutated at byte %d: got %d want %d", i, src.Pix[i], before[i])
		}
	}
	// The surface itself (destination) must be independent of the source.
	if s.Image() == nil {
		t.Fatal("surface image vanished")
	}
}

// TestDrawImageLeavesRestOfSurface verifies a scaled draw only touches the
// destination rectangle: pixels outside it retain their previous contents.
func TestDrawImageLeavesRestOfSurface(t *testing.T) {
	const w, h = 10, 10
	s := NewImageSurface(w, h)
	defer s.Close()

	bg := color.RGBA{R: 11, G: 22, B: 33, A: 255}
	s.Clear(bg)

	dst := image.Rect(3, 4, 7, 8)
	s.DrawImage(checker2x2(), Pt(0, 0), opaqueOpts(&DrawImageOptions{
		DstRect: &dst,
		Filter:  FilterBilinear,
	}))

	snap := s.Snapshot()
	for y := range h {
		for x := range w {
			inside := x >= dst.Min.X && x < dst.Max.X && y >= dst.Min.Y && y < dst.Max.Y
			got := snap.RGBAAt(x, y)
			if inside {
				if got == bg {
					t.Errorf("inside pixel (%d,%d) was not drawn", x, y)
				}
				continue
			}
			if got != bg {
				t.Errorf("outside pixel (%d,%d) = %v, want background %v", x, y, got, bg)
			}
		}
	}
}

// TestDrawImageEqualSizeUnchanged verifies the legacy 1:1 path is bit-identical
// to the previous implementation for opaque, translucent and SubRect draws.
func TestDrawImageEqualSizeUnchanged(t *testing.T) {
	cases := []struct {
		name string
		img  image.Image
		opts *DrawImageOptions
	}{
		{
			name: "opaque rgba",
			img: func() image.Image {
				img := image.NewRGBA(image.Rect(0, 0, 4, 4))
				for y := range 4 {
					for x := range 4 {
						img.SetRGBA(x, y, color.RGBA{R: uint8(20 * x), G: 90, B: uint8(20 * y), A: 255})
					}
				}
				return img
			}(),
			opts: nil,
		},
		{
			name: "translucent rgba with alpha",
			img: func() image.Image {
				img := image.NewRGBA(image.Rect(0, 0, 4, 4))
				for y := range 4 {
					for x := range 4 {
						img.SetRGBA(x, y, color.RGBA{R: 200, G: 100, B: 50, A: 128})
					}
				}
				return img
			}(),
			opts: &DrawImageOptions{Alpha: 0.5},
		},
		{
			name: "src rect",
			img: func() image.Image {
				img := image.NewNRGBA(image.Rect(0, 0, 6, 6))
				for y := range 6 {
					for x := range 6 {
						img.SetNRGBA(x, y, color.NRGBA{R: uint8(30 * x), G: uint8(30 * y), A: uint8(100 + x*10)})
					}
				}
				return img
			}(),
			opts: &DrawImageOptions{SrcRect: ptrRect(image.Rect(1, 1, 4, 4)), Alpha: 1},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := NewImageSurface(8, 8)
			defer got.Close()
			got.Clear(color.RGBA{R: 9, G: 19, B: 29, A: 255})

			want := NewImageSurface(8, 8)
			defer want.Close()
			want.Clear(color.RGBA{R: 9, G: 19, B: 29, A: 255})

			got.DrawImage(tt.img, Pt(2, 3), tt.opts)
			legacyBlitImage(want, tt.img, Pt(2, 3), tt.opts)

			gs, ws := got.Snapshot(), want.Snapshot()
			for y := range 8 {
				for x := range 8 {
					if gs.RGBAAt(x, y) != ws.RGBAAt(x, y) {
						t.Fatalf("pixel (%d,%d) = %v, legacy %v", x, y, gs.RGBAAt(x, y), ws.RGBAAt(x, y))
					}
				}
			}
		})
	}
}

// legacyBlitImage is a verbatim copy of the pre-scaling DrawImage 1:1 loop,
// used as the regression oracle.
func legacyBlitImage(s *ImageSurface, img image.Image, at Point, opts *DrawImageOptions) {
	if s.closed || img == nil {
		return
	}
	srcBounds := img.Bounds()
	if opts != nil && opts.SrcRect != nil {
		srcBounds = *opts.SrcRect
	}
	dstX := int(at.X)
	dstY := int(at.Y)
	alpha := 1.0
	if opts != nil {
		alpha = opts.Alpha
	}
	for sy := srcBounds.Min.Y; sy < srcBounds.Max.Y; sy++ {
		dy := dstY + (sy - srcBounds.Min.Y)
		if dy < 0 || dy >= s.height {
			continue
		}
		for sx := srcBounds.Min.X; sx < srcBounds.Max.X; sx++ {
			dx := dstX + (sx - srcBounds.Min.X)
			if dx < 0 || dx >= s.width {
				continue
			}
			srcColor := img.At(sx, sy)
			if alpha < 1.0 {
				srcColor = s.applyAlpha(srcColor, alpha)
			}
			s.blendPixel(dx, dy, srcColor)
		}
	}
}

// TestDrawImageSafety covers degenerate and hostile inputs: none may panic or
// allocate based on the destination size.
func TestDrawImageSafety(t *testing.T) {
	t.Run("nil image", func(t *testing.T) {
		s := NewImageSurface(4, 4)
		defer s.Close()
		s.DrawImage(nil, Pt(0, 0), nil)
		s.DrawImage(nil, Pt(math.NaN(), math.Inf(1)), opaqueOpts(&DrawImageOptions{}))
	})

	t.Run("empty image", func(t *testing.T) {
		s := NewImageSurface(4, 4)
		defer s.Close()
		s.Clear(color.White)
		empty := image.NewNRGBA(image.Rectangle{})
		s.DrawImage(empty, Pt(0, 0), opaqueOpts(&DrawImageOptions{
			DstRect: ptrRect(image.Rect(0, 0, 4, 4)),
		}))
		assertPixelNear(t, s.Snapshot().RGBAAt(1, 1), color.RGBA{R: 255, G: 255, B: 255, A: 255}, 0, "empty image no-op")
	})

	t.Run("zero destination", func(t *testing.T) {
		s := NewImageSurface(4, 4)
		defer s.Close()
		s.Clear(color.White)
		src := opaqueChecker2x2()
		s.DrawImage(src, Pt(0, 0), opaqueOpts(&DrawImageOptions{DstRect: ptrRect(image.Rect(1, 1, 1, 3))}))
		s.DrawImage(src, Pt(0, 0), opaqueOpts(&DrawImageOptions{DstRect: ptrRect(image.Rect(1, 1, 3, 1))}))
		assertPixelNear(t, s.Snapshot().RGBAAt(0, 0), color.RGBA{R: 255, G: 255, B: 255, A: 255}, 0, "zero width no-op")
	})

	t.Run("out of bounds src rect", func(t *testing.T) {
		s := NewImageSurface(4, 4)
		defer s.Close()
		s.Clear(color.White)
		src := opaqueChecker2x2()

		// Fully outside: no pixels drawn.
		s.DrawImage(src, Pt(0, 0), opaqueOpts(&DrawImageOptions{
			SrcRect: ptrRect(image.Rect(50, 50, 52, 52)),
			DstRect: ptrRect(image.Rect(0, 0, 4, 4)),
		}))
		assertPixelNear(t, s.Snapshot().RGBAAt(1, 1), color.RGBA{R: 255, G: 255, B: 255, A: 255}, 0, "outside no-op")

		// Partially outside: only the overlapping texels draw.
		s.DrawImage(src, Pt(0, 0), opaqueOpts(&DrawImageOptions{
			SrcRect: ptrRect(image.Rect(1, 1, 3, 3)),
			DstRect: ptrRect(image.Rect(0, 0, 4, 4)),
			Filter:  FilterBilinear,
		}))
		// Source (1,1) is white; the clipped region is entirely white.
		assertPixelNear(t, s.Snapshot().RGBAAt(2, 2), color.RGBA{R: 255, G: 255, B: 255, A: 255}, 2, "clipped overlap")
	})

	t.Run("non finite coordinates", func(t *testing.T) {
		s := NewImageSurface(4, 4)
		defer s.Close()
		s.Clear(color.White)
		src := opaqueChecker2x2()
		for _, at := range []Point{
			Pt(math.NaN(), 0),
			Pt(0, math.NaN()),
			Pt(math.Inf(1), math.Inf(-1)),
			Pt(math.Inf(-1), 0),
		} {
			s.DrawImage(src, at, nil)
			s.DrawImage(src, at, opaqueOpts(&DrawImageOptions{Filter: FilterBilinear}))
		}
		// DstRect is integer-typed, so exercise huge integer spans too.
		s.DrawImage(src, Pt(0, 0), opaqueOpts(&DrawImageOptions{
			DstRect: &image.Rectangle{Min: image.Pt(-1<<62, -1<<62), Max: image.Pt(1<<62, 1<<62)},
		}))
		assertPixelNear(t, s.Snapshot().RGBAAt(2, 2), color.RGBA{R: 255, G: 255, B: 255, A: 255}, 0, "no-op")
	})

	t.Run("huge destination", func(t *testing.T) {
		s := NewImageSurface(8, 8)
		defer s.Close()
		s.Clear(color.Transparent)
		dst := image.Rect(-1<<40, -1<<40, 1<<40, 1<<40)
		s.DrawImage(checker2x2(), Pt(0, 0), opaqueOpts(&DrawImageOptions{DstRect: &dst, Filter: FilterBicubic}))
		if s.Snapshot() == nil {
			t.Fatal("snapshot after huge destination returned nil")
		}
	})

	t.Run("huge destination allocation bounded", func(t *testing.T) {
		// The destination is enormous but the surface is tiny; the draw must
		// scale its work to the visible surface, not the rectangle area.
		s := NewImageSurface(16, 16)
		defer s.Close()
		src := image.NewNRGBA(image.Rect(0, 0, 1024, 1024))
		dst := image.Rect(0, 0, 1<<30, 1<<30)

		allocs := testing.AllocsPerRun(1, func() {
			s.DrawImage(src, Pt(0, 0), opaqueOpts(&DrawImageOptions{
				DstRect: &dst,
				Filter:  FilterBilinear,
			}))
		})
		// Column cache is at most the surface width; a few dozen allocations
		// for the sampler and snapshot-free path is safe.
		if allocs > 20 {
			t.Errorf("huge destination used %v allocations, want bounded (<20)", allocs)
		}
	})

	t.Run("opacity extremes", func(t *testing.T) {
		s := NewImageSurface(2, 2)
		defer s.Close()

		// Explicit Alpha values (not via opaqueOpts) exercise normalization.
		s.Clear(color.Transparent)
		s.DrawImage(opaqueChecker2x2(), Pt(0, 0), &DrawImageOptions{Alpha: math.NaN()})
		if s.Snapshot().RGBAAt(0, 0).A == 0 {
			t.Error("NaN opacity should behave as fully opaque")
		}

		s.Clear(color.Transparent)
		s.DrawImage(opaqueChecker2x2(), Pt(0, 0), &DrawImageOptions{Alpha: math.Inf(1)})
		if s.Snapshot().RGBAAt(0, 0).A != 255 {
			t.Error("+Inf opacity should clamp to opaque")
		}

		s.Clear(color.Transparent)
		s.DrawImage(opaqueChecker2x2(), Pt(0, 0), &DrawImageOptions{Alpha: math.Inf(-1)})
		if s.Snapshot().RGBAAt(0, 0).A != 0 {
			t.Error("-Inf opacity should draw nothing")
		}

		// A zero Alpha preserves the historical fully-transparent behavior.
		s.Clear(color.Transparent)
		s.DrawImage(opaqueChecker2x2(), Pt(0, 0), &DrawImageOptions{})
		if s.Snapshot().RGBAAt(0, 0).A != 0 {
			t.Error("zero Alpha should remain transparent for backward compatibility")
		}

		// Values above 1 clamp to opaque.
		s.Clear(color.Transparent)
		s.DrawImage(opaqueChecker2x2(), Pt(0, 0), &DrawImageOptions{Alpha: 2.5})
		if s.Snapshot().RGBAAt(0, 0).A != 255 {
			t.Error("Alpha > 1 should clamp to opaque")
		}
	})

	t.Run("unknown filter", func(t *testing.T) {
		s := newTransparentSurface(2, 2)
		defer s.Close()
		s.DrawImage(opaqueChecker2x2(), Pt(0, 0), opaqueOpts(&DrawImageOptions{Filter: Filter(99)}))
		assertPixelNear(t, s.Snapshot().RGBAAt(0, 0), color.RGBA{R: 255, A: 255}, 0, "unknown filter falls back to nearest")
	})

	t.Run("reverse src rect", func(t *testing.T) {
		s := newTransparentSurface(2, 2)
		defer s.Close()
		dst := image.Rect(0, 0, 2, 2)
		s.DrawImage(opaqueChecker2x2(), Pt(0, 0), opaqueOpts(&DrawImageOptions{
			SrcRect: &image.Rectangle{Min: image.Pt(2, 2), Max: image.Pt(0, 0)},
			DstRect: &dst,
			Filter:  FilterNearest,
		}))
		assertPixelNear(t, s.Snapshot().RGBAAt(0, 0), color.RGBA{R: 255, G: 255, B: 255, A: 255}, 1, "reversed src")
	})

	t.Run("closed surface", func(t *testing.T) {
		s := NewImageSurface(4, 4)
		_ = s.Close()
		dst := image.Rect(0, 0, 4, 4)
		s.DrawImage(opaqueChecker2x2(), Pt(0, 0), opaqueOpts(&DrawImageOptions{DstRect: &dst, Filter: FilterBilinear}))
	})
}

// TestFilterString verifies filter naming used in diagnostics.
func TestFilterString(t *testing.T) {
	cases := map[Filter]string{
		FilterNearest:  "Nearest",
		FilterBilinear: "Bilinear",
		FilterBicubic:  "Bicubic",
		Filter(200):    "Unknown",
	}
	for f, want := range cases {
		if got := f.String(); got != want {
			t.Errorf("Filter(%d).String() = %q, want %q", f, got, want)
		}
	}
}

func ptrRect(r image.Rectangle) *image.Rectangle {
	return &r
}

// FuzzDrawImageScale asserts that arbitrary rectangles, filters and opacities
// never panic, never touch memory outside the surface, and never mutate the
// source.
func FuzzDrawImageScale(f *testing.F) {
	f.Add(int8(2), int8(2), int8(4), int8(4), uint8(0), int8(0), int8(2), int8(0), int8(2), uint8(0))
	f.Add(int8(-4), int8(3), int8(0), int8(8), uint8(1), int8(1), int8(3), int8(1), int8(3), uint8(2))
	f.Add(int8(1), int8(1), int8(16), int8(1), uint8(2), int8(0), int8(0), int8(1), int8(1), uint8(1))

	f.Fuzz(func(t *testing.T,
		sw, sh int8, dw, dh int8, filter uint8,
		sx0, sy0, sx1, sy1 int8, alphaBits uint8,
	) {
		w := int(sw)
		h := int(sh)
		if w <= 0 || h <= 0 || w > 32 || h > 32 {
			return
		}
		src := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := range h {
			for x := range w {
				src.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 8), G: uint8(y * 8), B: 20, A: uint8(x*7 + y*3)})
			}
		}
		before := make([]byte, len(src.Pix))
		copy(before, src.Pix)

		s := NewImageSurface(12, 12)
		defer s.Close()
		s.Clear(color.RGBA{R: 5, G: 6, B: 7, A: 255})

		fr := image.Rectangle{Min: image.Pt(int(sx0)-8, int(sy0)-8), Max: image.Pt(int(sx1)-1, int(sy1)-1)}
		dr := image.Rectangle{Min: image.Pt(0, 0), Max: image.Pt(int(dw), int(dh))}
		alpha := float64(alphaBits) / 255.0

		s.DrawImage(src, Pt(0, 0), &DrawImageOptions{
			SrcRect: &fr,
			DstRect: &dr,
			Filter:  Filter(filter % 3),
			Alpha:   alpha,
		})

		for i := range before {
			if src.Pix[i] != before[i] {
				t.Fatalf("source mutated at byte %d", i)
			}
		}
		snap := s.Snapshot()
		if snap == nil {
			t.Fatal("Snapshot returned nil")
		}
		if got := snap.Bounds(); got != s.img.Bounds() {
			t.Fatalf("snapshot bounds = %v, want %v", got, s.img.Bounds())
		}
	})
}

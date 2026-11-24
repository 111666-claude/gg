// Copyright 2026 The gogpu Authors
// SPDX-License-Identifier: MIT

package surface

import (
	"image"
	"image/color"
	"testing"
)

func newRGBAWithRect(w, h int) *image.RGBA {
	return image.NewRGBA(image.Rect(0, 0, w, h))
}

func TestImageSurfaceDrawImageBilinearUpscale(t *testing.T) {
	s := NewImageSurface(8, 2)
	defer s.Close()

	src := newRGBAWithRect(2, 1)
	src.SetRGBA(0, 0, color.RGBA{A: 255})
	src.SetRGBA(1, 0, color.RGBA{R: 255, G: 255, B: 255, A: 255})

	s.DrawImage(src, Point{}, &DrawImageOptions{
		DstRect: rectPtr(image.Rect(0, 0, 8, 1)),
		Filter:  FilterBilinear,
		Alpha:   1,
	})

	out := s.Snapshot()
	// Bilinear must produce intermediate grays, not only black/white snaps.
	graySeen := false
	for x := 0; x < 8; x++ {
		c := out.RGBAAt(x, 0)
		if c.R > 0 && c.R < 255 {
			graySeen = true
		}
	}
	if !graySeen {
		t.Error("bilinear upscale produced no interpolated gray pixels")
	}
}

func TestImageSurfaceDrawImageNearestBlocks(t *testing.T) {
	s := NewImageSurface(4, 4)
	defer s.Close()

	src := newRGBAWithRect(2, 2)
	src.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
	src.SetRGBA(1, 0, color.RGBA{G: 255, A: 255})
	src.SetRGBA(0, 1, color.RGBA{B: 255, A: 255})
	src.SetRGBA(1, 1, color.RGBA{R: 255, G: 255, A: 255})

	s.DrawImage(src, Point{}, &DrawImageOptions{
		DstRect: rectPtr(image.Rect(0, 0, 4, 4)),
		Filter:  FilterNearest,
		Alpha:   1,
	})

	out := s.Snapshot()
	if c := out.RGBAAt(0, 0); c.R != 255 || c.G != 0 {
		t.Errorf("block(0,0) = %v, want red", c)
	}
	if c := out.RGBAAt(3, 3); c.R != 255 || c.G != 255 {
		t.Errorf("block(1,1) = %v, want yellow", c)
	}
}

func TestImageSurfaceDrawImageDstRectMapping(t *testing.T) {
	s := NewImageSurface(4, 1)
	defer s.Close()

	src := newRGBAWithRect(2, 1)
	src.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
	src.SetRGBA(1, 0, color.RGBA{G: 255, A: 255})

	// Map the source onto the full 4px destination rectangle (2x upscale).
	dst := image.Rect(0, 0, 4, 1)
	s.DrawImage(src, Point{}, &DrawImageOptions{
		DstRect: &dst,
		Filter:  FilterNearest,
		Alpha:   1,
	})

	out := s.Snapshot()
	if c := out.RGBAAt(0, 0); c.R != 255 {
		t.Errorf("left block = %v, want red", c)
	}
	if c := out.RGBAAt(3, 0); c.G != 255 {
		t.Errorf("right block = %v, want green", c)
	}
}

func TestImageSurfaceDrawImagePremulTransparentEdge(t *testing.T) {
	s := NewImageSurface(8, 1)
	defer s.Close()
	s.Clear(color.White)

	// Left half opaque red, right half transparent (premultiplied zero RGB).
	src := newRGBAWithRect(8, 1)
	for x := 0; x < 4; x++ {
		src.SetRGBA(x, 0, color.RGBA{R: 255, A: 255})
	}
	for x := 4; x < 8; x++ {
		src.SetRGBA(x, 0, color.RGBA{R: 0, G: 0, B: 0, A: 0})
	}

	s.DrawImage(src, Point{}, &DrawImageOptions{Filter: FilterBilinear, Alpha: 1})
	out := s.Snapshot()

	// Transparent region keeps the white background — no dark halo.
	c := out.RGBAAt(7, 0)
	if c.R != 255 || c.G != 255 || c.B != 255 {
		t.Errorf("transparent region darkened to %v", c)
	}
}

func TestImageSurfaceDrawImageSrcRectSubregion(t *testing.T) {
	s := NewImageSurface(2, 2)
	defer s.Close()

	src := newRGBAWithRect(4, 4)
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			if x >= 2 && y >= 2 {
				src.SetRGBA(x, y, color.RGBA{B: 255, A: 255})
			} else {
				src.SetRGBA(x, y, color.RGBA{R: 255, A: 255})
			}
		}
	}

	srcRect := image.Rect(2, 2, 4, 4)
	s.DrawImage(src, Point{}, &DrawImageOptions{
		SrcRect: &srcRect,
		Filter:  FilterNearest,
		Alpha:   1,
	})

	out := s.Snapshot()
	if c := out.RGBAAt(0, 0); c.B != 255 || c.R != 0 {
		t.Errorf("sub-region pixel = %v, want blue", c)
	}
}

func TestImageSurfaceDrawImageAlphaModulation(t *testing.T) {
	s := NewImageSurface(2, 1)
	defer s.Close()
	s.Clear(color.White)

	src := newRGBAWithRect(1, 1)
	src.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})

	s.DrawImage(src, Point{}, &DrawImageOptions{Alpha: 0.5, Filter: FilterNearest})
	c := s.Snapshot().RGBAAt(0, 0)
	// 50% red over white must blend, never stay pure red.
	if c.R == 255 && c.G == 0 {
		t.Errorf("alpha modulation missing, got %v", c)
	}
	if c.G == 0 || c.B == 0 {
		t.Errorf("expected white contribution from 50%% alpha, got %v", c)
	}
}

func TestImageSurfaceDrawImageCorruptInputsNoPanic(t *testing.T) {
	s := NewImageSurface(4, 4)
	defer s.Close()

	src := newRGBAWithRect(4, 4)
	src.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})

	// Empty source rect, out-of-range rect, nil image and zero-alpha.
	s.DrawImage(nil, Point{}, nil)
	empty := image.Rect(0, 0, 0, 0)
	s.DrawImage(src, Point{}, &DrawImageOptions{SrcRect: &empty})
	oor := image.Rect(100, 100, 110, 110)
	s.DrawImage(src, Point{}, &DrawImageOptions{SrcRect: &oor})
	s.DrawImage(src, Point{}, &DrawImageOptions{Alpha: 0})
	s.DrawImage(src, Point{-10, -10}, nil) // mostly off-surface
}

func rectPtr(r image.Rectangle) *image.Rectangle {
	return &r
}

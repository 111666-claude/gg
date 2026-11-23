// Package main demonstrates scaled image drawing on a surface.ImageSurface.
//
// It produces a contact sheet of thumbnails using the three sampling filters,
// plus a mirrored preview. This is the common thumbnail-preview workflow where
// drawing pixel-for-pixel (nearest) at a reduced size looks jagged.
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"

	"github.com/gogpu/gg/surface"
)

func main() {
	const (
		canvasW = 360
		canvasH = 120
		thumbW  = 80
		thumbH  = 80
		margin  = 10
	)

	s := surface.NewImageSurface(canvasW, canvasH)
	defer func() { _ = s.Close() }()
	s.Clear(color.RGBA{R: 32, G: 32, B: 40, A: 255})

	src := makeCheckerboard(64, 64)

	filters := []struct {
		filter surface.Filter
		x      int
	}{
		{surface.FilterNearest, margin},
		{surface.FilterBilinear, margin*2 + thumbW},
		{surface.FilterBicubic, margin*3 + thumbW*2},
	}

	for _, f := range filters {
		// Map the source rectangle onto a smaller destination rectangle.
		dst := image.Rect(f.x, 20, f.x+thumbW, 20+thumbH)
		s.DrawImage(src, surface.Pt(0, 0), &surface.DrawImageOptions{
			DstRect: &dst,
			Filter:  f.filter,
			Alpha:   1,
		})
	}

	// A mirrored preview: a negative destination width flips horizontally.
	mirrored := image.Rectangle{
		Min: image.Pt(margin*4+thumbW*3, 20),
		Max: image.Pt(margin*4+thumbW*3-thumbW, 20+thumbH),
	}
	s.DrawImage(src, surface.Pt(0, 0), &surface.DrawImageOptions{
		DstRect: &mirrored,
		Filter:  surface.FilterBilinear,
		Alpha:   1,
	})

	if err := s.Flush(); err != nil {
		log.Fatalf("flush: %v", err)
	}

	snap := s.Snapshot()
	if snap == nil {
		log.Fatal("snapshot returned nil")
	}
	if err := writePNG("thumbnails.png", snap); err != nil {
		log.Fatalf("write png: %v", err)
	}

	fmt.Println("Wrote thumbnails.png (nearest | bilinear | bicubic | mirrored)")
}

// makeCheckerboard returns a semi-transparent checkerboard with a gradient so
// the difference between filters is visible.
func makeCheckerboard(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			shade := uint8((x + y) * 255 / (w + h))
			a := uint8(255)
			if (x/8+y/8)%2 == 0 {
				a = 140
			}
			img.SetNRGBA(x, y, color.NRGBA{R: 220, G: shade, B: 255 - shade, A: a})
		}
	}
	return img
}

// writePNG encodes an RGBA image to a PNG file.
func writePNG(path string, img *image.RGBA) error {
	f, err := os.Create(path) //nolint:gosec // example output path
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return png.Encode(f, img)
}

package raster

import (
	"image"
	"image/color"
	"testing"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/recording"
)

func newBackendTestImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	return img
}

func TestBackendDrawImagePaintsPixels(t *testing.T) {
	backend := NewBackend()
	if err := backend.Begin(20, 20); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	src := newBackendTestImage(4, 4)
	backend.DrawImage(src,
		recording.NewRect(0, 0, 4, 4),
		recording.NewRect(2, 2, 6, 6),
		recording.DefaultImageOptions())
	if err := backend.End(); err != nil {
		t.Fatalf("End: %v", err)
	}

	out := backend.Image().(*image.RGBA)
	if c := out.RGBAAt(3, 3); c.R != 255 || c.A != 255 {
		t.Errorf("painted pixel = %v, want opaque red", c)
	}
	if c := out.RGBAAt(0, 0); c.A != 0 {
		t.Errorf("untouched pixel alpha = %d, want 0", c.A)
	}
}

func TestBackendDrawImageScaledWithFilters(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 1))
	src.SetRGBA(0, 0, color.RGBA{A: 255})
	src.SetRGBA(1, 0, color.RGBA{R: 255, G: 255, B: 255, A: 255})

	for _, filter := range []recording.InterpolationMode{
		recording.InterpolationNearest,
		recording.InterpolationBilinear,
	} {
		backend := NewBackend()
		_ = backend.Begin(8, 1)
		backend.DrawImage(src,
			recording.NewRect(0, 0, 2, 1),
			recording.NewRect(0, 0, 8, 1),
			recording.ImageOptions{Interpolation: filter, Alpha: 1})
		_ = backend.End()

		out := backend.Image().(*image.RGBA)
		// The endpoint colors must survive scaling under both filters. The
		// gg.Context CPU image pattern quantizes sampling, so interpolation
		// quality is covered by the render/surface sampler tests; here we only
		// assert correct geometry and colors through the full recording path.
		if c := out.RGBAAt(0, 0); c.A != 255 {
			t.Errorf("filter %d: first pixel = %v", filter, c)
		}
		if c := out.RGBAAt(7, 0); c.R != 255 || c.G != 255 {
			t.Errorf("filter %d: last pixel = %v, want white", filter, c)
		}
	}
}

func TestBackendDrawImageSourceCrop(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			if x >= 2 && y >= 2 {
				src.SetRGBA(x, y, color.RGBA{G: 255, A: 255})
			} else {
				src.SetRGBA(x, y, color.RGBA{R: 255, A: 255})
			}
		}
	}

	backend := NewBackend()
	_ = backend.Begin(4, 4)
	// Crop the green 2x2 quadrant and blit it at the origin.
	backend.DrawImage(src,
		recording.NewRect(2, 2, 4, 4),
		recording.NewRect(0, 0, 2, 2),
		recording.DefaultImageOptions())
	_ = backend.End()

	c := backend.Image().(*image.RGBA).RGBAAt(0, 0)
	if c.G != 255 || c.R != 0 {
		t.Errorf("cropped pixel = %v, want green", c)
	}
}

func TestBackendDrawImagePlaybackRoundTrip(t *testing.T) {
	rec := recording.NewRecorder(20, 20)
	rec.DrawImage(newBackendTestImage(4, 4), 4, 4)

	backend := NewBackend()
	if err := rec.FinishRecording().Playback(backend); err != nil {
		t.Fatalf("Playback: %v", err)
	}
	c := backend.Image().(*image.RGBA).RGBAAt(5, 5)
	if c.R != 255 || c.A != 255 {
		t.Errorf("playback pixel = %v, want opaque red", c)
	}
}

func TestBackendDrawImageTransparentEdge(t *testing.T) {
	backend := NewBackend()
	_ = backend.Begin(8, 1)
	// White background so a dark halo would be visible.
	backend.FillRect(recording.NewRect(0, 0, 8, 1), recording.NewSolidBrush(gg.White))

	src := image.NewRGBA(image.Rect(0, 0, 8, 1))
	for x := 0; x < 4; x++ {
		src.SetRGBA(x, 0, color.RGBA{R: 255, A: 255})
	}
	// Right half fully transparent with zero premultiplied RGB.
	backend.DrawImage(src,
		recording.NewRect(0, 0, 8, 1),
		recording.NewRect(0, 0, 8, 1),
		recording.ImageOptions{Interpolation: recording.InterpolationBilinear, Alpha: 1})
	_ = backend.End()

	c := backend.Image().(*image.RGBA).RGBAAt(7, 0)
	if c.R != 255 || c.G != 255 || c.B != 255 {
		t.Errorf("transparent edge darkened background to %v", c)
	}
}

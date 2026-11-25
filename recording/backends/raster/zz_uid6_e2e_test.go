package raster

import (
	"image"
	"image/color"
	"testing"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/recording"
)

func uid6Px(t *testing.T, b *Backend, x, y int) color.RGBA {
	t.Helper()
	img := b.Image().(*image.RGBA)
	return img.RGBAAt(x, y)
}

func TestUid6RasterMultiplyScreen(t *testing.T) {
	b := NewBackend()
	if err := b.Begin(4, 4); err != nil {
		t.Fatal(err)
	}
	// Opaque blue background.
	b.FillRect(recording.NewRect(0, 0, 4, 4),
		recording.SolidBrush{Color: gg.RGBA{R: 0, G: 0, B: 1, A: 1}})
	// Opaque red with multiply -> black.
	b.SetBlendMode(gg.BlendMultiply)
	b.FillRect(recording.NewRect(0, 0, 4, 4),
		recording.SolidBrush{Color: gg.RGBA{R: 1, G: 0, B: 0, A: 1}})
	if got := uid6Px(t, b, 2, 2); got.R > 6 || got.G > 6 || got.B > 6 {
		t.Fatalf("multiply red*blue = %v want black", got)
	}

	// Screen black-ish check on fresh backend.
	b2 := NewBackend()
	_ = b2.Begin(4, 4)
	b2.FillRect(recording.NewRect(0, 0, 4, 4),
		recording.SolidBrush{Color: gg.RGBA{R: 0.2, G: 0.2, B: 0.2, A: 1}})
	b2.SetBlendMode(gg.BlendScreen)
	b2.FillRect(recording.NewRect(0, 0, 4, 4),
		recording.SolidBrush{Color: gg.RGBA{R: 0.5, G: 0.5, B: 0.5, A: 1}})
	got := uid6Px(t, b2, 2, 2)
	// screen(0.2,0.5) = 0.6 -> ~153
	if got.R < 140 || got.R > 165 {
		t.Fatalf("screen = %v want ~153", got)
	}
}

func TestUid6RecorderBlendPlaybackAndRestore(t *testing.T) {
	rec := recording.NewRecorder(6, 6)
	rec.SetRGB(0, 0, 1)
	rec.FillRectangle(0, 0, 6, 6)
	rec.Save() // saves source-over state
	rec.SetBlendMode(gg.BlendMultiply)
	rec.SetRGB(1, 0, 0)
	rec.FillRectangle(0, 0, 3, 6)
	rec.Restore() // back to source-over
	// After restore blend should be source-over again; opaque green over all.
	rec.SetRGB(0, 1, 0)
	rec.FillRectangle(0, 0, 6, 6)

	b := NewBackend()
	if err := rec.FinishRecording().Playback(b); err != nil {
		t.Fatal(err)
	}
	// Left half: blue * red = black, then green source-over covers it -> green.
	if got := uid6Px(t, b, 1, 3); got.G < 240 || got.R > 10 {
		t.Fatalf("left after green over = %v want green", got)
	}
	// Right half similarly green.
	if got := uid6Px(t, b, 5, 3); got.G < 240 {
		t.Fatalf("right = %v want green", got)
	}
}

func TestUid6RasterDrawImageNonZeroBounds(t *testing.T) {
	img := image.NewRGBA(image.Rect(10, 20, 12, 22))
	for y := 20; y < 22; y++ {
		for x := 10; x < 12; x++ {
			img.SetRGBA(x, y, color.RGBA{0, 255, 0, 255})
		}
	}
	b := NewBackend()
	_ = b.Begin(6, 6)
	b.FillRect(recording.NewRect(0, 0, 6, 6),
		recording.SolidBrush{Color: gg.RGBA{R: 1, G: 0, B: 0, A: 1}})
	// Empty src => whole image; dest at (1,1) size 2x2.
	b.DrawImage(img, recording.Rect{}, recording.NewRect(1, 1, 2, 2),
		recording.ImageOptions{Alpha: 1})
	if got := uid6Px(t, b, 2, 2); got.G < 240 {
		t.Fatalf("image dest pixel = %v want green", got)
	}
	if got := uid6Px(t, b, 0, 0); got.R < 240 {
		t.Fatalf("outside image = %v want red", got)
	}
}

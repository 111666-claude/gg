package raster

import (
	"bytes"
	"image"
	"image/color"
	"testing"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/recording"
	"github.com/gogpu/gg/text"
	"golang.org/x/image/font/gofont/goregular"
)

// buildSerializeFixture builds a fixed recording exercising text, images,
// clipping, rounded rectangles, and nested transforms.
func buildSerializeFixture(t *testing.T) *recording.Recording {
	t.Helper()

	src, err := text.NewFontSource(goregular.TTF)
	if err != nil {
		t.Fatalf("NewFontSource: %v", err)
	}
	face := src.Face(18)

	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(x * 32),
				G: uint8(y * 32),
				B: uint8((x + y) * 16),
				A: 255,
			})
		}
	}

	rec := recording.NewRecorder(200, 150)
	rec.ClearWithColor(gg.White)

	// Nested transforms with a filled rounded rectangle.
	rec.Save()
	rec.Translate(40, 30)
	rec.Save()
	rec.Rotate(0.25)
	rec.Scale(1.5, 1.5)
	rec.SetFillRGBA(0.8, 0.2, 0.2, 1)
	rec.DrawRoundedRectangle(0, 0, 60, 40, 8)
	rec.Fill()
	rec.Restore()

	// Rounded-rect clip, then an image inside the clip.
	rec.ClipRoundRect(10, 10, 120, 90, 12)
	rec.DrawImage(img, 20, 20)
	rec.ResetClip()

	// Dashed, stroked rounded rectangle.
	rec.SetStrokeRGBA(0, 0, 0.6, 1)
	rec.SetLineWidth(2.5)
	rec.SetDash(6, 3)
	rec.DrawRoundedRectangle(100, 60, 80, 50, 10)
	rec.Stroke()
	rec.ClearDash()

	// Text with a real font face.
	rec.SetFont(face)
	rec.SetFontSize(18)
	rec.SetFillRGBA(0.1, 0.1, 0.1, 1)
	rec.DrawString("gg", 60, 120)

	rec.Restore()

	return rec.FinishRecording()
}

// renderToNRGBA plays a recording back through the raster backend.
func renderToNRGBA(t *testing.T, rec *recording.Recording) *image.NRGBA {
	t.Helper()
	backend := NewBackend()
	if err := rec.Playback(backend); err != nil {
		t.Fatalf("Playback: %v", err)
	}
	img := backend.Image()
	out := image.NewNRGBA(img.Bounds())
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			out.Set(x, y, img.At(x, y))
		}
	}
	return out
}

// TestSerializePixelRoundTrip verifies that a recording survives
// MarshalBinary → Decode with pixel-identical rendering.
func TestSerializePixelRoundTrip(t *testing.T) {
	rec := buildSerializeFixture(t)

	data, err := rec.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	dec, err := recording.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	want := renderToNRGBA(t, rec)
	got := renderToNRGBA(t, dec)

	if want.Bounds() != got.Bounds() {
		t.Fatalf("bounds = %v, want %v", got.Bounds(), want.Bounds())
	}
	diff := 0
	for y := want.Bounds().Min.Y; y < want.Bounds().Max.Y; y++ {
		for x := want.Bounds().Min.X; x < want.Bounds().Max.X; x++ {
			i := want.PixOffset(x, y)
			if !bytes.Equal(want.Pix[i:i+4], got.Pix[i:i+4]) {
				if diff < 5 {
					t.Errorf("pixel (%d,%d) = %v, want %v", x, y, got.Pix[i:i+4], want.Pix[i:i+4])
				}
				diff++
			}
		}
	}
	if diff > 0 {
		t.Fatalf("%d pixels differ after binary round trip", diff)
	}
}

package gg

import (
	"image"
	"image/color"
	"os"
	"testing"

	"github.com/gogpu/gg/text"
)

// colrTestRune is U+E000, the layered three-square glyph in colr_synth.ttf.
const colrTestRune rune = 0xE000

// TestTextCOLR_RenderOnContextPixmap verifies the COLR layered path on the
// CPU rasterization route available without a GPU adapter: the same
// GlyphMaskRasterizer primitives the GPU glyph-mask pipeline consumes are
// exercised per layer onto a Context-backed RGBA pixmap.
func TestTextCOLR_RenderOnContextPixmap(t *testing.T) {
	data, err := os.ReadFile("text/testdata/colr_synth.ttf")
	if err != nil {
		t.Skipf("fixture unavailable: %v", err)
	}
	src, err := text.NewFontSource(data)
	if err != nil {
		t.Fatalf("NewFontSource: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })

	face := src.Face(48)
	dc := NewContext(200, 120)
	dc.SetRGB(1, 1, 1) // white background
	dc.Clear()

	// DrawWithEmoji composites straight into the context pixmap's buffer.
	// ImageView aliases that buffer, so the write and the pixel read share
	// one backing array (ToImage would allocate a copy on each call).
	view := dc.pixmap.ImageView()
	text.DrawWithEmoji(view, string(colrTestRune), face, 10, 70, color.Black)

	img := view
	colored := 0
	blueFound := false
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := img.RGBAAt(x, y)
			if c.R != 255 || c.G != 255 || c.B != 255 {
				colored++
			}
			if c.B == 255 && c.R == 0 && c.A == 255 {
				blueFound = true
			}
		}
	}
	if colored == 0 {
		t.Fatal("no color glyph pixels on context pixmap")
	}
	if !blueFound {
		t.Error("COLR palette blue absent from context render")
	}
}

// TestTextCOLR_ScaledRenderOnContext verifies ppem/device-style scaling by
// rendering the same glyph at two face sizes onto context pixmaps.
func TestTextCOLR_ScaledRenderOnContext(t *testing.T) {
	data, err := os.ReadFile("text/testdata/colr_pixels.ttf")
	if err != nil {
		t.Skipf("fixture unavailable: %v", err)
	}
	src, err := text.NewFontSource(data)
	if err != nil {
		t.Fatalf("NewFontSource: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })

	inkBox := func(size float64) image.Rectangle {
		face := src.Face(size)
		dst := image.NewRGBA(image.Rect(0, 0, 400, 200))
		text.DrawWithEmoji(dst, "A", face, 10, 120, color.Black)
		return paintedBox(dst)
	}

	small := inkBox(16)
	large := inkBox(96)
	if small.Empty() || large.Empty() {
		t.Fatal("expected ink at both sizes")
	}
	if large.Dx() <= small.Dx() || large.Dy() <= small.Dy() {
		t.Errorf("scaled box did not grow: small=%v large=%v", small, large)
	}
}

func paintedBox(img *image.RGBA) image.Rectangle {
	b := img.Bounds()
	minX, minY := b.Max.X, b.Max.Y
	maxX, maxY := b.Min.X, b.Min.Y
	found := false
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if img.RGBAAt(x, y).A == 0 {
				continue
			}
			found = true
			if x < minX {
				minX = x
			}
			if x+1 > maxX {
				maxX = x + 1
			}
			if y < minY {
				minY = y
			}
			if y+1 > maxY {
				maxY = y + 1
			}
		}
	}
	if !found {
		return image.Rectangle{}
	}
	return image.Rect(minX, minY, maxX, maxY)
}

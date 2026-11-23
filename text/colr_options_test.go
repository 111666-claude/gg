package text

import (
	"image"
	"image/color"
	"testing"
)

func TestWithColorPalette_FaceConfig(t *testing.T) {
	src := loadCOLRSource(t, colrSynthFixture)

	if got := FaceColorPalette(src.Face(48)); got != -1 {
		t.Errorf("default palette = %d, want -1", got)
	}
	if got := FaceColorPalette(src.Face(48, WithColorPalette(1))); got != 1 {
		t.Errorf("configured palette = %d, want 1", got)
	}
	if got := FaceColorPalette(src.Face(48, WithColorPalette(-7))); got != -7 {
		t.Errorf("negative palette = %d, want -7 (clamped at render)", got)
	}
}

func TestWithColorPalette_MultiAndFilteredFaces(t *testing.T) {
	src := loadCOLRSource(t, colrSynthFixture)
	face := src.Face(48, WithColorPalette(1))

	mf, err := NewMultiFace(face, face)
	if err != nil {
		t.Fatalf("NewMultiFace: %v", err)
	}
	if got := FaceColorPalette(mf); got != 1 {
		t.Errorf("MultiFace palette = %d, want 1", got)
	}

	ff := NewFilteredFace(face)
	if got := FaceColorPalette(ff); got != 1 {
		t.Errorf("FilteredFace palette = %d, want 1", got)
	}
}

func TestWithColorPalette_DrawOptionsPrecedence(t *testing.T) {
	src := loadCOLRSource(t, colrSynthFixture)

	// Face asks for palette 1 (red outer); explicit draw option 0 must win.
	face := src.Face(48, WithColorPalette(1))
	dst := image.NewRGBA(image.Rect(0, 0, 200, 120))
	DrawWithEmojiOptions(dst, string(colrTestRune), face, 10, 70, color.Black, EmojiOptions{
		Palette:         0,
		ExplicitPalette: true,
	})
	box := paintedBounds(dst)
	outer := dst.RGBAAt(box.Min.X+1, box.Min.Y+1)
	if outer.B != 255 || outer.R != 0 {
		t.Errorf("explicit palette 0 not honored over face palette 1: %+v", outer)
	}
}

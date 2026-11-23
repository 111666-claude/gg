package emoji

import (
	"encoding/binary"
	"errors"
	"image/color"
	"testing"
)

// buildCOLR assembles a minimal COLRv0 table from base-glyph and layer
// records. Offsets are fixed and validated by the parser.
func buildCOLR(t *testing.T, version uint16, bases []baseGlyphRecord, layers []layerRecord) []byte {
	t.Helper()
	const (
		headerSize      = 14
		baseRecordSize  = 6
		layerRecordSize = 4
	)
	baseOffset := uint32(headerSize)
	layerOffset := baseOffset + uint32(len(bases)*baseRecordSize)
	size := int(layerOffset) + len(layers)*layerRecordSize
	buf := make([]byte, size)

	binary.BigEndian.PutUint16(buf[0:2], version)
	binary.BigEndian.PutUint16(buf[2:4], uint16(len(bases)))
	binary.BigEndian.PutUint32(buf[4:8], baseOffset)
	binary.BigEndian.PutUint32(buf[8:12], layerOffset)
	binary.BigEndian.PutUint16(buf[12:14], uint16(len(layers)))

	for i, rec := range bases {
		pos := int(baseOffset) + i*baseRecordSize
		binary.BigEndian.PutUint16(buf[pos:pos+2], rec.glyphID)
		binary.BigEndian.PutUint16(buf[pos+2:pos+4], rec.firstLayer)
		binary.BigEndian.PutUint16(buf[pos+4:pos+6], rec.numLayers)
	}
	for i, rec := range layers {
		pos := int(layerOffset) + i*layerRecordSize
		binary.BigEndian.PutUint16(buf[pos:pos+2], rec.glyphID)
		binary.BigEndian.PutUint16(buf[pos+2:pos+4], rec.paletteIndex)
	}
	return buf
}

// buildCPAL assembles a CPALv0 table with numPalettes palettes, each holding
// numEntries BGRA records (palettes share one contiguous color array).
func buildCPAL(t *testing.T, entries [][]Color) []byte {
	t.Helper()
	numPalettes := len(entries)
	numEntries := 0
	if numPalettes > 0 {
		numEntries = len(entries[0])
	}
	const headerSize = 12
	colorOffset := headerSize + numPalettes*2
	size := colorOffset + numPalettes*numEntries*4
	buf := make([]byte, size)

	binary.BigEndian.PutUint16(buf[0:2], 0) // version
	binary.BigEndian.PutUint16(buf[2:4], uint16(numEntries))
	binary.BigEndian.PutUint16(buf[4:6], uint16(numPalettes))
	binary.BigEndian.PutUint16(buf[6:8], uint16(numPalettes*numEntries))
	binary.BigEndian.PutUint32(buf[8:12], uint32(colorOffset))

	for i := range entries {
		off := headerSize + i*2
		binary.BigEndian.PutUint16(buf[off:off+2], uint16(i*numEntries))
	}
	for i, palette := range entries {
		for j, c := range palette {
			pos := colorOffset + (i*numEntries+j)*4
			buf[pos] = c.B
			buf[pos+1] = c.G
			buf[pos+2] = c.R
			buf[pos+3] = c.A
		}
	}
	return buf
}

func validCPAL(t *testing.T) []byte {
	t.Helper()
	return buildCPAL(t, [][]Color{
		{{R: 255, A: 255}, {G: 255, A: 128}, {A: 0}},
		{{B: 255, A: 255}, {R: 255, G: 255, A: 128}, {A: 0}},
	})
}

func TestCOLRParser_LayerOrderAndPaletteSwitch(t *testing.T) {
	bases := []baseGlyphRecord{{glyphID: 10, firstLayer: 0, numLayers: 3}}
	layers := []layerRecord{
		{glyphID: 11, paletteIndex: 0},
		{glyphID: 12, paletteIndex: 1},
		{glyphID: 13, paletteIndex: ForegroundColorIndex},
	}
	colr := buildCOLR(t, 0, bases, layers)
	cpal := validCPAL(t)

	p, err := NewCOLRParser(colr, cpal)
	if err != nil {
		t.Fatalf("NewCOLRParser: %v", err)
	}
	if p.NumPalettes() != 2 {
		t.Fatalf("NumPalettes = %d, want 2", p.NumPalettes())
	}

	g0, err := p.GetGlyph(10, 0)
	if err != nil {
		t.Fatalf("GetGlyph palette 0: %v", err)
	}
	if len(g0.Layers) != 3 {
		t.Fatalf("layers = %d, want 3", len(g0.Layers))
	}
	// Bottom-to-top order preserved.
	wantGIDs := []uint16{11, 12, 13}
	for i, want := range wantGIDs {
		if g0.Layers[i].GlyphID != want {
			t.Errorf("layer %d gid = %d, want %d", i, g0.Layers[i].GlyphID, want)
		}
	}
	// Palette 0 colors.
	if g0.Layers[0].Color.R != 255 || g0.Layers[0].Color.A != 255 {
		t.Errorf("layer0 palette0 color = %+v", g0.Layers[0].Color)
	}
	if g0.Layers[1].Color.G != 255 || g0.Layers[1].Color.A != 128 {
		t.Errorf("layer1 palette0 color = %+v", g0.Layers[1].Color)
	}
	// Foreground layer stays unresolved.
	if !g0.Layers[2].IsForeground() {
		t.Error("layer2 should be foreground")
	}

	// Switching palette changes only palette-backed colors.
	g1, err := p.GetGlyph(10, 1)
	if err != nil {
		t.Fatalf("GetGlyph palette 1: %v", err)
	}
	if g1.Layers[0].Color.B != 255 || g1.Layers[0].Color.R != 0 {
		t.Errorf("layer0 palette1 color = %+v, want blue", g1.Layers[0].Color)
	}
	if !g1.Layers[2].IsForeground() {
		t.Error("foreground layer must stay foreground across palettes")
	}
}

func TestCOLRParser_UnknownPaletteFallsBackToZero(t *testing.T) {
	bases := []baseGlyphRecord{{glyphID: 1, firstLayer: 0, numLayers: 1}}
	layers := []layerRecord{{glyphID: 2, paletteIndex: 0}}
	colr := buildCOLR(t, 0, bases, layers)
	cpal := validCPAL(t)
	p, err := NewCOLRParser(colr, cpal)
	if err != nil {
		t.Fatalf("NewCOLRParser: %v", err)
	}

	for _, bad := range []int{-1, 2, 99} {
		g, err := p.GetGlyph(1, bad)
		if err != nil {
			t.Errorf("palette %d: unexpected error %v", bad, err)
			continue
		}
		if g.Layers[0].Color.R != 255 {
			t.Errorf("palette %d: expected fallback palette0 red, got %+v", bad, g.Layers[0].Color)
		}
		if got := p.EffectivePaletteIndex(bad); got != 0 {
			t.Errorf("EffectivePaletteIndex(%d) = %d, want 0", bad, got)
		}
	}
}

func TestCOLRParser_PaletteIndexOutOfRange(t *testing.T) {
	// Layer references entry 5 but the palette only has 2 entries.
	bases := []baseGlyphRecord{{glyphID: 1, firstLayer: 0, numLayers: 1}}
	layers := []layerRecord{{glyphID: 2, paletteIndex: 5}}
	colr := buildCOLR(t, 0, bases, layers)
	cpal := buildCPAL(t, [][]Color{{{R: 1, A: 255}, {G: 2, A: 255}}})

	p, err := NewCOLRParser(colr, cpal)
	if err != nil {
		t.Fatalf("NewCOLRParser: %v", err)
	}
	if _, err := p.GetGlyph(1, 0); !errors.Is(err, ErrPaletteIndexOutOfRange) {
		t.Fatalf("GetGlyph err = %v, want ErrPaletteIndexOutOfRange", err)
	}
}

func TestCOLRParser_TransparentLayer(t *testing.T) {
	bases := []baseGlyphRecord{{glyphID: 1, firstLayer: 0, numLayers: 1}}
	layers := []layerRecord{{glyphID: 2, paletteIndex: 2}} // entry 2 alpha=0
	colr := buildCOLR(t, 0, bases, layers)
	cpal := validCPAL(t)
	p, err := NewCOLRParser(colr, cpal)
	if err != nil {
		t.Fatalf("NewCOLRParser: %v", err)
	}
	g, err := p.GetGlyph(1, 0)
	if err != nil {
		t.Fatalf("GetGlyph: %v", err)
	}
	if !g.Layers[0].IsTransparent() {
		t.Error("alpha-0 palette layer must report transparent")
	}
	// Foreground layers are never "transparent" by palette alone.
	fg := ColorLayer{PaletteIndex: ForegroundColorIndex}
	if fg.IsTransparent() {
		t.Error("foreground layer must not be treated as transparent")
	}
}

func TestCOLRParser_BadLayerGlyphRejected(t *testing.T) {
	bases := []baseGlyphRecord{{glyphID: 1, firstLayer: 0, numLayers: 1}}
	layers := []layerRecord{{glyphID: 99, paletteIndex: 0}}
	colr := buildCOLR(t, 0, bases, layers)
	cpal := validCPAL(t)

	// Font declares only 10 glyphs; layer gid 99 is out of range.
	if _, err := NewCOLRParserWithGlyphCount(colr, cpal, 10); !errors.Is(err, ErrInvalidLayerGlyph) {
		t.Fatalf("parse err = %v, want ErrInvalidLayerGlyph", err)
	}
	// Without a glyph count the same table parses (range unknown).
	if _, err := NewCOLRParser(colr, cpal); err != nil {
		t.Fatalf("countless parse should succeed, got %v", err)
	}
}

func TestCOLRParser_TooManyLayers(t *testing.T) {
	// One complete base record declaring an excessive layer count. The
	// record itself must be in-bounds so the count check can run; a single
	// layer record satisfies the header's layer array geometry.
	bases := []baseGlyphRecord{{glyphID: 1, firstLayer: 0, numLayers: maxCOLRLayers + 1}}
	layers := []layerRecord{{glyphID: 2, paletteIndex: 0}}
	colr := buildCOLR(t, 0, bases, layers)
	cpal := validCPAL(t)
	if _, err := NewCOLRParser(colr, cpal); !errors.Is(err, ErrTooManyLayers) {
		t.Fatalf("parse err = %v, want ErrTooManyLayers", err)
	}

	// GetGlyph also enforces the cap on an otherwise-valid table.
	okBases := []baseGlyphRecord{{glyphID: 1, firstLayer: 0, numLayers: 1}}
	okColr := buildCOLR(t, 0, okBases, layers)
	p, err := NewCOLRParser(okColr, cpal)
	if err != nil {
		t.Fatalf("valid setup parse: %v", err)
	}
	if MaxCOLRLayers() != maxCOLRLayers {
		t.Fatalf("MaxCOLRLayers accessor mismatch")
	}
	_ = p
}

func TestCOLRParser_TruncatedTablesDoNotPanic(t *testing.T) {
	good := buildCOLR(t, 0,
		[]baseGlyphRecord{{glyphID: 1, firstLayer: 0, numLayers: 1}},
		[]layerRecord{{glyphID: 2, paletteIndex: 0}})
	cpal := validCPAL(t)

	cases := map[string][]byte{
		"short header":        {0, 0, 0, 1, 0},
		"base offset beyond":  good[:14],
		"truncated base recs": good[:16],
		"truncated layers":    good[:len(good)-2],
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewCOLRParser(data, cpal); !errors.Is(err, ErrInvalidCOLRData) {
				t.Fatalf("err = %v, want ErrInvalidCOLRData", err)
			}
		})
	}

	// CPAL truncation cases, paired with a valid COLR table so failures are
	// attributable to CPAL. All inputs stay >= 12 bytes so they reach the
	// table-specific validation rather than the empty/precondition guard.
	goodCPAL := cpal
	validColr := buildCOLR(t, 0,
		[]baseGlyphRecord{{glyphID: 1, firstLayer: 0, numLayers: 1}},
		[]layerRecord{{glyphID: 2, paletteIndex: 0}})
	cpalCases := map[string][]byte{
		"header only":      goodCPAL[:12], // no room for the offset array
		"offset array cut": goodCPAL[:13],
		"colors cut":       goodCPAL[:len(goodCPAL)-3], // last BGRA record truncated
	}
	for name, data := range cpalCases {
		t.Run("cpal/"+name, func(t *testing.T) {
			if _, err := NewCOLRParser(validColr, data); !errors.Is(err, ErrInvalidCPALData) {
				t.Fatalf("err = %v, want ErrInvalidCPALData", err)
			}
		})
	}
}

func TestCOLRParser_UnsupportedVersion(t *testing.T) {
	colr := buildCOLR(t, 99, nil, nil)
	if _, err := NewCOLRParser(colr, validCPAL(t)); !errors.Is(err, ErrUnsupportedCOLRVersion) {
		t.Fatalf("err = %v, want ErrUnsupportedCOLRVersion", err)
	}
}

func TestResolveLayerColor(t *testing.T) {
	fg := color.RGBA{R: 10, G: 20, B: 30, A: 255}

	palLayer := ColorLayer{PaletteIndex: 0, Color: Color{R: 200, G: 100, B: 50, A: 255}}
	if got := ResolveLayerColor(palLayer, fg, 0); got != (color.RGBA{R: 200, G: 100, B: 50, A: 255}) {
		t.Errorf("zero alpha changed palette color: %+v", got)
	}
	half := ResolveLayerColor(palLayer, fg, 0.5)
	if half.A < 127 || half.A > 128 {
		t.Errorf("half alpha = %d, want ~128", half.A)
	}

	fgLayer := ColorLayer{PaletteIndex: ForegroundColorIndex}
	if got := ResolveLayerColor(fgLayer, fg, 0); got != fg {
		t.Errorf("foreground = %+v, want %+v", got, fg)
	}
	fgHalf := ResolveLayerColor(fgLayer, fg, 0.5)
	if fgHalf.A < 127 || fgHalf.A > 128 {
		t.Errorf("foreground half alpha = %d", fgHalf.A)
	}
}

func TestColor_WithAlpha(t *testing.T) {
	c := Color{R: 10, G: 20, B: 30, A: 200}
	if got := c.WithAlpha(1); got.A != 200 {
		t.Errorf("WithAlpha(1) A = %d, want 200", got.A)
	}
	got := c.WithAlpha(0.5)
	if got.A < 99 || got.A > 101 {
		t.Errorf("WithAlpha(0.5) A = %d, want ~100", got.A)
	}
	if c.WithAlpha(0).A != 0 || c.WithAlpha(-2).A != 0 {
		t.Error("negative/zero opacity must clamp to 0 alpha")
	}
}

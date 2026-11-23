package text

import (
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gogpu/gg/text/emoji"
)

// colrTestRune is U+E000, the layered three-square base glyph in
// colr_synth.ttf; colrTransparentRune is U+E001, the alpha-0 glyph.
const (
	colrTestRune        rune = 0xE000
	colrTransparentRune rune = 0xE001
)

// These tests exercise the renderer's defensive paths end-to-end using
// hand-built sfnt/COLR byte sequences. Every malformed input must degrade to
// a monochrome outline (or no ink) without panicking.

// buildMinimalColorFont assembles a tiny but complete sfnt with a COLR/CPAL
// pair. layerGlyphs/layerColors are parallel slices describing one base
// glyph (gid 2). glyf contains simple square outlines for every gid.
func buildMinimalColorFont(t *testing.T, layerGlyphs, layerColors []uint16, cpalEntries [][]emoji.Color) []byte {
	t.Helper()
	// Start from the valid synthetic fixture and rebuild only COLR/CPAL,
	// reusing its glyf/loca/maxp/head infrastructure for guaranteed-valid
	// outlines.
	data, err := os.ReadFile(colrSynthFixture)
	if err != nil {
		t.Skipf("fixture read: %v", err)
	}
	font := cloneBytes(data)

	colr := buildRawCOLR(2, layerGlyphs, layerColors)
	cpal := buildRawCPAL(cpalEntries)
	replaceTable(t, font, "COLR", colr)
	replaceTable(t, font, "CPAL", cpal)
	return font
}

// buildRawCOLR writes a COLRv0 table: one base glyph record at gid baseGID
// and one layer record per (gid, colorID) pair.
func buildRawCOLR(baseGID uint16, gids, colorIDs []uint16) []byte {
	const headerSize, baseSize, layerSize = 14, 6, 4
	layerOffset := uint32(headerSize + baseSize)
	total := headerSize + baseSize + len(gids)*layerSize
	buf := make([]byte, total)
	binary.BigEndian.PutUint16(buf[0:2], 0)            // version 0
	binary.BigEndian.PutUint16(buf[2:4], 1)            // numBaseGlyphRecords
	binary.BigEndian.PutUint32(buf[4:8], headerSize)   // baseGlyphRecordOffset
	binary.BigEndian.PutUint32(buf[8:12], layerOffset) // layerRecordOffset
	binary.BigEndian.PutUint16(buf[12:14], uint16(len(gids)))

	pos := headerSize
	binary.BigEndian.PutUint16(buf[pos:pos+2], baseGID)
	binary.BigEndian.PutUint16(buf[pos+2:pos+4], 0)
	binary.BigEndian.PutUint16(buf[pos+4:pos+6], uint16(len(gids)))

	for i := range gids {
		lp := int(layerOffset) + i*layerSize
		binary.BigEndian.PutUint16(buf[lp:lp+2], gids[i])
		binary.BigEndian.PutUint16(buf[lp+2:lp+4], colorIDs[i])
	}
	return buf
}

// buildRawCPAL writes a CPALv0 table with the given palettes (each palette
// is a contiguous BGRA record array).
func buildRawCPAL(palettes [][]emoji.Color) []byte {
	numPalettes := len(palettes)
	numEntries := 0
	if numPalettes > 0 {
		numEntries = len(palettes[0])
	}
	const headerSize = 12
	colorOffset := headerSize + numPalettes*2
	total := colorOffset + numPalettes*numEntries*4
	buf := make([]byte, total)
	binary.BigEndian.PutUint16(buf[0:2], 0)
	binary.BigEndian.PutUint16(buf[2:4], uint16(numEntries))
	binary.BigEndian.PutUint16(buf[4:6], uint16(numPalettes))
	binary.BigEndian.PutUint16(buf[6:8], uint16(numPalettes*numEntries))
	binary.BigEndian.PutUint32(buf[8:12], uint32(colorOffset))
	for i := range palettes {
		off := headerSize + i*2
		binary.BigEndian.PutUint16(buf[off:off+2], uint16(i*numEntries))
	}
	for i, pal := range palettes {
		for j, c := range pal {
			p := colorOffset + (i*numEntries+j)*4
			buf[p] = c.B
			buf[p+1] = c.G
			buf[p+2] = c.R
			buf[p+3] = c.A
		}
	}
	return buf
}

// replaceTable overwrites an sfnt table in place when the new content fits
// inside the original table's slot (the test fonts reserve ample slack via
// the synthetic fixture's layout). It updates the directory length.
func replaceTable(t *testing.T, data []byte, tag string, content []byte) {
	t.Helper()
	off, length, ok := findTableRange(data, tag)
	if !ok {
		t.Fatalf("table %s not found", tag)
	}
	if len(content) > length {
		// Grow into the trailing space between tables is not guaranteed;
		// instead append and repoint the directory. All generated fixtures
		// stay small enough that this branch is not expected.
		newOff := len(data)
		data = append(data, content...)
		numTables := int(uint16(data[4])<<8 | uint16(data[5]))
		for i := 0; i < numTables; i++ {
			rec := 12 + i*16
			if string(data[rec:rec+4]) == tag {
				binary.BigEndian.PutUint32(data[rec+8:rec+12], uint32(newOff))
				binary.BigEndian.PutUint32(data[rec+12:rec+16], uint32(len(content)))
				return
			}
		}
		t.Fatalf("directory record for %s vanished", tag)
	}
	copy(data[off:off+len(content)], content)
	// Shrink the declared length so trailing stale bytes are ignored.
	numTables := int(uint16(data[4])<<8 | uint16(data[5]))
	for i := 0; i < numTables; i++ {
		rec := 12 + i*16
		if string(data[rec:rec+4]) == tag {
			binary.BigEndian.PutUint32(data[rec+12:rec+16], uint32(len(content)))
			return
		}
	}
}

func TestCOLR_BadLayerGlyphFallsBack(t *testing.T) {
	// Layer references gid 999, beyond the font's 7 glyphs. Parsing must
	// surface ErrInvalidLayerGlyph; rendering then uses the monochrome path.
	palettes := [][]emoji.Color{{{R: 255, A: 255}}}
	font := buildMinimalColorFont(t, []uint16{999}, []uint16{0}, palettes)

	src, err := NewFontSource(font)
	if err != nil {
		t.Fatalf("NewFontSource: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })

	cf := src.Parsed().(ColorFont)
	if cf.HasColorTables() {
		t.Error("font with an unparseable COLR must not advertise color tables")
	}
	if got := cf.GlyphType(2); got != GlyphTypeOutline {
		t.Errorf("GlyphType = %v, want Outline fallback", got)
	}

	// Rendering must not panic and must produce monochrome ink.
	face := src.Face(48)
	dst := image.NewRGBA(image.Rect(0, 0, 200, 120))
	DrawWithEmoji(dst, string(colrTestRune), face, 10, 70, color.Black)
}

func TestCOLR_CyclicLayerReferenceDoesNotLoop(t *testing.T) {
	// COLRv0 layers are flat, but a corrupt table could list the same gid
	// repeatedly. The renderer's visited-set guard must stop the loop and
	// fall back rather than paint duplicate layers forever.
	palettes := [][]emoji.Color{
		{{R: 255, A: 255}, {G: 255, A: 255}},
	}
	// The base glyph gid 2 referencing itself twice.
	font := buildMinimalColorFont(t, []uint16{2, 2}, []uint16{0, 1}, palettes)

	src, err := NewFontSource(font)
	if err != nil {
		t.Fatalf("NewFontSource: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })

	face := src.Face(48)
	dst := image.NewRGBA(image.Rect(0, 0, 200, 120))
	done := make(chan struct{})
	go func() {
		defer close(done)
		DrawWithEmoji(dst, string(colrTestRune), face, 10, 70, color.Black)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cyclic layer reference did not terminate")
	}
}

func TestCOLR_MissingTablesDegradesGracefully(t *testing.T) {
	// Strip COLR and CPAL from the fixture entirely.
	data, err := os.ReadFile(colrSynthFixture)
	if err != nil {
		t.Skipf("fixture read: %v", err)
	}
	font := cloneBytes(data)
	zeroTable(font, "COLR")
	zeroTable(font, "CPAL")

	src, err := NewFontSource(font)
	if err != nil {
		t.Fatalf("NewFontSource: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })
	cf := src.Parsed().(ColorFont)
	if cf.HasColorTables() {
		t.Error("font with stripped color tables must report no color support")
	}

	face := src.Face(48)
	dst := image.NewRGBA(image.Rect(0, 0, 200, 120))
	DrawWithEmoji(dst, string(colrTestRune), face, 10, 70, color.Black) // must not panic
}

func TestCOLR_PaletteOversizeRejected(t *testing.T) {
	// A layer color index beyond the palette's entry count must produce the
	// typed error, and the renderer falls back to monochrome.
	palettes := [][]emoji.Color{{{R: 255, A: 255}}}
	font := buildMinimalColorFont(t, []uint16{3}, []uint16{42}, palettes)

	src, err := NewFontSource(font)
	if err != nil {
		t.Fatalf("NewFontSource: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })

	cf := src.Parsed().(ColorFont)
	_, err = cf.COLRGlyph(2, 0)
	if err == nil {
		t.Fatal("expected error for out-of-range palette index")
	}
	// The glyph ID must appear in the message, and errors.Is must still
	// reach the typed sentinel through the wrapping.
	if !errors.Is(err, emoji.ErrPaletteIndexOutOfRange) {
		t.Errorf("err = %v, want wrap of ErrPaletteIndexOutOfRange", err)
	}
	if !strings.Contains(err.Error(), "glyph 2") {
		t.Errorf("error %q missing glyph ID", err.Error())
	}

	face := src.Face(48)
	dst := image.NewRGBA(image.Rect(0, 0, 200, 120))
	DrawWithEmoji(dst, string(colrTestRune), face, 10, 70, color.Black) // fallback, no panic
}

func TestCOLR_ConcurrentRenderingIsSafe(t *testing.T) {
	// Lazy color-table init (sync.Once) and per-call rasterizers must let
	// many goroutines render the same COLR face without a data race.
	src := loadCOLRSource(t, colrSynthFixture)
	face := src.Face(32)

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				dst := image.NewRGBA(image.Rect(0, 0, 200, 120))
				DrawWithEmoji(dst, string(colrTestRune), face, 10, 70, color.Black)
				if countAlphaPixels(dst) == 0 {
					t.Error("concurrent render produced no ink")
					return
				}
			}
		}()
	}
	wg.Wait()
}

// zeroTable replaces a table's directory entry with a zero-length,
// zero-offset record so the parser treats the table as absent.
func zeroTable(data []byte, tag string) {
	numTables := int(uint16(data[4])<<8 | uint16(data[5]))
	for i := 0; i < numTables; i++ {
		rec := 12 + i*16
		if string(data[rec:rec+4]) == tag {
			for j := rec + 8; j < rec+16; j++ {
				data[j] = 0
			}
		}
	}
}

// COLR/CPAL and bitmap color-table support for ownParsedFont.
//
// ownParsedFont satisfies the ColorFont interface by lazily parsing the
// font's color tables (COLR/CPAL for layered vector glyphs, CBDT/CBLC and
// sbix for bitmap glyphs). When a color table is absent or malformed the
// relevant pointer stays nil and the rendering layer falls back to the
// monochrome outline path — no panic, no hard error at load time.
package text

import (
	"fmt"

	"github.com/gogpu/gg/text/emoji"
)

// ensureColorTables lazily parses every available color table exactly once.
// Malformed tables are tolerated: the offending parser stays nil while the
// remaining formats still load, so a broken COLR never blocks CBDT glyphs.
func (f *ownParsedFont) ensureColorTables() {
	f.colorInit.Do(func() {
		colrData, hasCOLR := f.tables["COLR"]
		cpalData, hasCPAL := f.tables["CPAL"]
		if hasCOLR && hasCPAL {
			// numGlyphs enables layer glyph range validation.
			parser, err := emoji.NewCOLRParserWithGlyphCount(colrData, cpalData, f.numGlyphs)
			if err == nil && parser != nil {
				f.colrParser = parser
				f.hasColor = true
			}
		}

		if cbdtData, hasCBDT := f.tables["CBDT"]; hasCBDT {
			if cblcData, hasCBLC := f.tables["CBLC"]; hasCBLC {
				if extractor, err := emoji.NewCBDTExtractor(cbdtData, cblcData); err == nil && extractor != nil {
					f.cbdt = extractor
					f.hasColor = true
				}
			}
		}

		if sbixData, hasSbix := f.tables["sbix"]; hasSbix {
			if parser, err := emoji.NewSBIXParser(sbixData, uint16(f.numGlyphs)); err == nil && parser != nil {
				f.sbix = parser
				f.hasColor = true
			}
		}
	})
}

// HasColorTables reports whether the font carries at least one usable color
// table. Implements ColorFont.
func (f *ownParsedFont) HasColorTables() bool {
	f.ensureColorTables()
	return f.hasColor
}

// GlyphType classifies a glyph for renderer dispatch.
//
// Priority follows the established color-font convention:
//  1. COLR layered vector glyph (scalable, the preferred vector format)
//  2. Bitmap glyph (CBDT then sbix)
//  3. Plain outline for everything else
//
// Implements ColorFont.
func (f *ownParsedFont) GlyphType(glyphID uint16) GlyphType {
	f.ensureColorTables()

	if f.colrParser != nil && f.colrParser.HasGlyph(glyphID) {
		return GlyphTypeCOLR
	}
	if f.cbdt != nil && f.cbdt.HasGlyph(glyphID) {
		return GlyphTypeBitmap
	}
	if f.sbix != nil {
		for i := 0; i < f.sbix.NumStrikes(); i++ {
			if f.sbix.HasGlyph(int(glyphID), i) {
				return GlyphTypeBitmap
			}
		}
	}
	return GlyphTypeOutline
}

// COLRGlyph returns the layered description of a COLR glyph at the requested
// palette. Unknown palette indices fall back to palette 0 inside the parser.
// Every failure is wrapped with the requested glyph ID so callers can report
// precisely which glyph was unusable. Implements ColorFont.
func (f *ownParsedFont) COLRGlyph(glyphID uint16, paletteIndex int) (*emoji.COLRGlyph, error) {
	f.ensureColorTables()
	if f.colrParser == nil {
		return nil, fmt.Errorf("text: COLR glyph %d: %w", glyphID, emoji.ErrGlyphNotInCOLR)
	}
	glyph, err := f.colrParser.GetGlyph(glyphID, paletteIndex)
	if err != nil {
		return nil, fmt.Errorf("text: COLR glyph %d: %w", glyphID, err)
	}
	return glyph, nil
}

// BitmapGlyph returns a bitmap glyph at the best-fitting strike for ppem.
// CBDT is preferred over sbix when both tables exist. Implements ColorFont.
func (f *ownParsedFont) BitmapGlyph(glyphID uint16, ppem uint16) (*emoji.BitmapGlyph, error) {
	f.ensureColorTables()

	if f.cbdt != nil && f.cbdt.HasGlyph(glyphID) {
		g, err := f.cbdt.GetGlyph(glyphID, ppem)
		if err == nil {
			return g, nil
		}
	}
	if f.sbix != nil {
		strike := f.sbix.BestStrikeForPPEM(ppem)
		if strike >= 0 && f.sbix.HasGlyph(int(glyphID), strike) {
			return f.sbix.GetGlyph(int(glyphID), strike)
		}
	}
	return nil, emoji.ErrGlyphNotInBitmap
}

// NumColorPalettes reports the number of CPAL palettes (0 without a COLR
// table). Rendering code uses this to validate caller palette selection.
func (f *ownParsedFont) NumColorPalettes() int {
	f.ensureColorTables()
	if f.colrParser == nil {
		return 0
	}
	return f.colrParser.NumPalettes()
}

// EffectiveColorPalette maps a requested palette index onto one valid for
// this font. Out-of-range requests deterministically fall back to palette 0.
func (f *ownParsedFont) EffectiveColorPalette(paletteIndex int) int {
	f.ensureColorTables()
	if f.colrParser == nil {
		return 0
	}
	return f.colrParser.EffectivePaletteIndex(paletteIndex)
}

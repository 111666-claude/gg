package text

import (
	"image"
	"image/color"
	"image/draw"

	xdraw "golang.org/x/image/draw"

	"github.com/gogpu/gg/text/emoji"
)

// EmojiOptions controls color rendering in [DrawWithEmojiOptions].
//
// The zero value is valid: palette 0 is used, no extra alpha is applied,
// and the text color passed to the draw call paints foreground layers.
type EmojiOptions struct {
	// Palette selects the CPAL palette for COLR glyphs. Negative values and
	// indices beyond the font's palette count fall back to palette 0.
	// A face-level WithColorPalette selection is used when this is left at 0
	// and the face carries one.
	Palette int

	// Alpha is an additional global opacity multiplied into every color
	// layer (palette and foreground alike). The zero value means no extra
	// alpha (fully opaque), matching plain DrawWithEmoji; set a value in
	// (0,1) to fade the glyph toward transparent.
	Alpha float64

	// Foreground overrides the color used for COLR foreground layers
	// (palette index 0xFFFF). When nil, the color.Color argument of the
	// draw call is used.
	Foreground color.Color

	// ExplicitPalette is true when Palette was set by the caller and must
	// take precedence over the face-level WithColorPalette option.
	ExplicitPalette bool
}

// DrawWithEmoji renders text with full color emoji support.
// This function detects glyph types and routes to appropriate renderers:
//   - Outline glyphs: standard per-glyph rasterization
//   - Bitmap glyphs: PNG bitmap scaling and compositing
//   - COLR glyphs: layered vector glyph rendering via COLR/CPAL
//
// Position (x, y) is the baseline origin.
// For fonts without color tables, this behaves identically to [Draw].
func DrawWithEmoji(dst draw.Image, text string, face Face, x, y float64, col color.Color) {
	DrawWithEmojiOptions(dst, text, face, x, y, col, EmojiOptions{})
}

// DrawWithEmojiOptions is [DrawWithEmoji] with explicit control over the
// COLR palette, global alpha and foreground-layer color.
//
// Layer compositing order, subpixel positioning and advances match the
// monochrome [Draw] path: each rune consumes exactly the advance the face
// reports for it, regardless of how many COLR layers it contains, so mixed
// color/monochrome strings keep a single cursor.
func DrawWithEmojiOptions(dst draw.Image, text string, face Face, x, y float64, col color.Color, opts EmojiOptions) {
	if text == "" || face == nil {
		return
	}

	// Resolve the effective palette: explicit option > face option > default.
	if !opts.ExplicitPalette {
		if fp := FaceColorPalette(face); fp >= 0 {
			opts.Palette = fp
		}
	}

	drawColorRunes(dst, text, face, x, y, col, opts, 0)
}

// drawColorRunes renders text one rune at a time, dispatching on the face
// that actually owns each rune. This is what makes MultiFace fallback work:
// a rune is rendered with the same face that supplies its advance.
//
// advanceX is the cursor offset from the call's origin; it is non-zero only
// for the recursive MultiFace/FilteredFace paths.
func drawColorRunes(dst draw.Image, text string, face Face, x, y float64, col color.Color, opts EmojiOptions, advanceX float64) {
	switch f := face.(type) {
	case *sourceFace:
		drawColorSourceFace(dst, text, f, x, y, col, opts, advanceX)
	case *MultiFace:
		drawColorMultiFace(dst, text, f, x, y, col, opts, advanceX)
	case *FilteredFace:
		drawColorFilteredFace(dst, text, f, x, y, col, opts, advanceX)
	default:
		// Unknown face implementation: preserve the pre-existing behavior.
		Draw(dst, text, face, x+advanceX, y, col)
	}
}

// drawColorSourceFace handles the concrete rendering for a source face.
func drawColorSourceFace(dst draw.Image, text string, sf *sourceFace, x, y float64, col color.Color, opts EmojiOptions, advanceX float64) {
	parsed := sf.Source().Parsed()
	colorFont, hasColor := parsed.(ColorFont)
	if !hasColor || !colorFont.HasColorTables() {
		// No color support: the standard path is bit-for-bit unchanged.
		drawSourceFace(dst, text, sf, x+advanceX, y, col)
		return
	}

	ppem := sf.size
	cursor := x + advanceX
	for _, r := range text {
		if r < 0x20 && r != '\t' {
			continue
		}
		glyphID := parsed.GlyphIndex(r)
		advance := parsed.GlyphAdvance(glyphID, ppem)

		switch colorFont.GlyphType(glyphID) {
		case GlyphTypeBitmap:
			drawBitmapGlyph(dst, colorFont, glyphID, uint16(ppem), cursor, y)
		case GlyphTypeCOLR:
			drawCOLRGlyph(dst, sf, colorFont, glyphID, ppem, cursor, y, col, opts)
		default:
			drawSingleRuneOutline(dst, sf, r, cursor, y, col)
		}

		cursor += advance
	}
}

// drawColorMultiFace mirrors [drawMultiFace]'s face selection so the color
// path stays in lockstep with monochrome fallback positioning.
func drawColorMultiFace(dst draw.Image, text string, mf *MultiFace, x, y float64, col color.Color, opts EmojiOptions, advanceX float64) {
	cursor := x + advanceX
	for _, r := range text {
		if r < 0x20 && r != '\t' {
			continue
		}
		faceToUse := mf.faceForRune(r)
		// Recurse with the selected concrete face; its advance drives the
		// cursor exactly as drawMultiFace does.
		drawColorRunes(dst, string(r), faceToUse, cursor, y, col, opts, 0)
		cursor += faceGlyphAdvance(faceToUse, string(r))
	}
}

// drawColorFilteredFace mirrors [drawFilteredFace]: runes outside the
// allowed ranges are skipped entirely, including their advance.
func drawColorFilteredFace(dst draw.Image, text string, ff *FilteredFace, x, y float64, col color.Color, opts EmojiOptions, advanceX float64) {
	cursor := x + advanceX
	for _, r := range text {
		if r < 0x20 && r != '\t' {
			continue
		}
		if !ff.inRanges(r) {
			continue
		}
		runeStr := string(r)
		drawColorRunes(dst, runeStr, ff.face, cursor, y, col, opts, 0)
		cursor += faceGlyphAdvance(ff.face, runeStr)
	}
}

// drawBitmapGlyph renders a bitmap glyph (CBDT or sbix).
func drawBitmapGlyph(dst draw.Image, colorFont ColorFont, glyphID uint16, ppem uint16, x, y float64) {
	bitmap, err := colorFont.BitmapGlyph(glyphID, ppem)
	if err != nil {
		// Silently skip - could render tofu glyph instead.
		return
	}

	// Decode PNG data.
	img, err := bitmap.Decode()
	if err != nil {
		return
	}

	// Calculate scaling factor.
	scale := float64(ppem) / float64(bitmap.PPEM)
	if scale == 0 {
		scale = 1.0
	}

	// Calculate scaled dimensions.
	scaledW := int(float64(bitmap.Width) * scale)
	scaledH := int(float64(bitmap.Height) * scale)

	if scaledW <= 0 || scaledH <= 0 {
		return
	}

	// Calculate destination bounds.
	// OriginX is horizontal bearing (offset from glyph origin).
	// OriginY is vertical bearing (distance from baseline to top of glyph).
	destX := int(x + float64(bitmap.OriginX)*scale)
	destY := int(y - float64(bitmap.OriginY)*scale)

	destRect := image.Rect(
		destX,
		destY,
		destX+scaledW,
		destY+scaledH,
	)

	// Draw with high-quality scaling.
	xdraw.CatmullRom.Scale(dst, destRect, img, img.Bounds(), xdraw.Over, nil)
}

// drawSingleRuneOutline renders one rune through the standard monochrome
// pipeline. The source face path takes the same per-glyph loop as [Draw] so
// hinting and subpixel placement are identical.
func drawSingleRuneOutline(dst draw.Image, sf *sourceFace, r rune, x, y float64, col color.Color) {
	drawSourceFace(dst, string(r), sf, x, y, col)
}

// BitmapGlyphCache caches decoded bitmap glyphs to avoid repeated PNG decoding.
// This is important for performance when rendering the same emoji multiple times.
type BitmapGlyphCache struct {
	entries map[bitmapCacheKey]*CachedBitmap
	maxSize int
}

// bitmapCacheKey uniquely identifies a cached bitmap.
type bitmapCacheKey struct {
	fontID  uintptr // Pointer to font for identity.
	glyphID uint16
	ppem    uint16
}

// CachedBitmap holds a decoded and optionally scaled bitmap.
type CachedBitmap struct {
	Img     image.Image
	OriginX float32
	OriginY float32
	Width   int
	Height  int
}

// NewBitmapGlyphCache creates a new bitmap glyph cache.
// maxSize is the maximum number of cached entries.
func NewBitmapGlyphCache(maxSize int) *BitmapGlyphCache {
	return &BitmapGlyphCache{
		entries: make(map[bitmapCacheKey]*CachedBitmap),
		maxSize: maxSize,
	}
}

// Get retrieves a cached bitmap, or nil if not cached.
func (c *BitmapGlyphCache) Get(fontID uintptr, glyphID, ppem uint16) *CachedBitmap {
	key := bitmapCacheKey{fontID: fontID, glyphID: glyphID, ppem: ppem}
	return c.entries[key]
}

// Put stores a bitmap in the cache.
func (c *BitmapGlyphCache) Put(fontID uintptr, glyphID, ppem uint16, bitmap *emoji.BitmapGlyph, img image.Image) {
	// Simple eviction: clear when full.
	if len(c.entries) >= c.maxSize {
		c.Clear()
	}

	key := bitmapCacheKey{fontID: fontID, glyphID: glyphID, ppem: ppem}
	c.entries[key] = &CachedBitmap{
		Img:     img,
		OriginX: bitmap.OriginX,
		OriginY: bitmap.OriginY,
		Width:   bitmap.Width,
		Height:  bitmap.Height,
	}
}

// Clear removes all entries from the cache.
func (c *BitmapGlyphCache) Clear() {
	c.entries = make(map[bitmapCacheKey]*CachedBitmap)
}

// Size returns the number of cached entries.
func (c *BitmapGlyphCache) Size() int {
	return len(c.entries)
}

package emoji

import (
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/draw"
)

// COLR/CPAL table format errors.
var (
	// ErrNoCOLRTable indicates the font doesn't have a COLR table.
	ErrNoCOLRTable = errors.New("emoji: font has no COLR table")

	// ErrNoCPALTable indicates the font doesn't have a CPAL table.
	ErrNoCPALTable = errors.New("emoji: font has no CPAL table")

	// ErrInvalidCOLRData indicates the COLR table data is malformed.
	ErrInvalidCOLRData = errors.New("emoji: invalid COLR table data")

	// ErrInvalidCPALData indicates the CPAL table data is malformed.
	ErrInvalidCPALData = errors.New("emoji: invalid CPAL table data")

	// ErrGlyphNotInCOLR indicates the glyph is not a color glyph.
	ErrGlyphNotInCOLR = errors.New("emoji: glyph not found in COLR table")

	// ErrUnsupportedCOLRVersion indicates an unsupported COLR version.
	ErrUnsupportedCOLRVersion = errors.New("emoji: unsupported COLR version")

	// ErrPaletteIndexOutOfRange indicates a layer referenced a CPAL entry
	// that does not exist in the selected palette.
	ErrPaletteIndexOutOfRange = errors.New("emoji: palette index out of range")

	// ErrTooManyLayers indicates a base glyph declares more layers than the
	// hard safety limit (maxCOLRLayers).
	ErrTooManyLayers = errors.New("emoji: too many COLR layers")

	// ErrInvalidLayerGlyph indicates a layer references a glyph outside the
	// font's glyph range.
	ErrInvalidLayerGlyph = errors.New("emoji: COLR layer references invalid glyph")
)

// ForegroundColorIndex is the sentinel palette index meaning "use the
// foreground (text) color" rather than a CPAL entry.
const ForegroundColorIndex uint16 = 0xFFFF

// maxCOLRLayers caps the number of layers accepted for a single base glyph.
// Real-world COLRv0 fonts stay well under this (Segoe UI Emoji tops out
// around a few dozen); the cap only guards against malformed tables that
// would otherwise allocate unbounded work per glyph.
const maxCOLRLayers = 256

// MaxCOLRLayers exposes the per-glyph layer safety limit. Renderers use it
// to reject pathological inputs without duplicating the constant.
func MaxCOLRLayers() int { return maxCOLRLayers }

// COLRGlyph represents a color glyph from COLR table.
// It consists of multiple colored layers stacked on top of each other.
type COLRGlyph struct {
	// GlyphID is the original glyph ID for this color glyph.
	GlyphID uint16

	// Layers contains the color layers, bottom to top.
	Layers []ColorLayer

	// Bounds is the bounding box in font units.
	Bounds Rect

	// Version is the COLR table version (0 or 1).
	Version uint16
}

// ColorLayer represents one layer of a color glyph.
// Each layer is a glyph rendered in a specific color.
type ColorLayer struct {
	// GlyphID is the glyph to render for this layer.
	GlyphID uint16

	// PaletteIndex is the index into the CPAL color palette.
	// 0xFFFF indicates foreground color (use the text color).
	PaletteIndex uint16

	// Color is the resolved color from the palette.
	// This is set after calling ResolvePalette.
	Color Color
}

// IsForeground returns true if this layer uses the foreground text color.
func (l ColorLayer) IsForeground() bool {
	return l.PaletteIndex == ForegroundColorIndex
}

// IsTransparent reports whether the resolved palette color is fully
// transparent. Foreground layers are never considered transparent here —
// the caller's text color decides their alpha.
func (l ColorLayer) IsTransparent() bool {
	return !l.IsForeground() && l.Color.A == 0
}

// Color represents an RGBA color from CPAL palette.
type Color struct {
	R, G, B, A uint8
}

// RGBA implements color.Color interface.
func (c Color) RGBA() (r, g, b, a uint32) {
	return uint32(c.R) * 257, uint32(c.G) * 257, uint32(c.B) * 257, uint32(c.A) * 257
}

// ToRGBA returns the color as color.RGBA.
func (c Color) ToRGBA() color.RGBA {
	return color.RGBA{R: c.R, G: c.G, B: c.B, A: c.A}
}

// WithAlpha multiplies the color alpha by opacity, which must be in [0,1].
// Values outside the range are clamped. This implements the global alpha
// applied to every layer of a COLR glyph at draw time.
func (c Color) WithAlpha(opacity float64) Color {
	a := clamp01(opacity) * float64(c.A)
	// Round to nearest to keep 1.0 lossless (255*1 = 255).
	return Color{R: c.R, G: c.G, B: c.B, A: uint8(a + 0.5)}
}

// clamp01 clamps v to the unit interval.
func clamp01(v float64) float64 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 1
	}
	return v
}

// Rect represents a bounding rectangle.
type Rect struct {
	MinX, MinY float64
	MaxX, MaxY float64
}

// Width returns the width of the rectangle.
func (r Rect) Width() float64 {
	return r.MaxX - r.MinX
}

// Height returns the height of the rectangle.
func (r Rect) Height() float64 {
	return r.MaxY - r.MinY
}

// Empty returns true if the rectangle has zero area.
func (r Rect) Empty() bool {
	return r.MinX >= r.MaxX || r.MinY >= r.MaxY
}

// COLRParser parses COLR/CPAL tables from font data.
type COLRParser struct {
	colrData []byte
	cpalData []byte

	// Parsed COLR header
	version    uint16
	numGlyphs  uint16
	numGlyphsU int // numGlyphs as int, retained for range checks
	baseGlyphs []baseGlyphRecord
	layers     []layerRecord

	// numGlyphsInFont is the font's maxp.numGlyphs. Zero disables layer
	// glyph range validation (used when the caller cannot supply it).
	numGlyphsInFont int

	// Parsed CPAL palette
	palettes [][]Color
}

// baseGlyphRecord from COLRv0.
type baseGlyphRecord struct {
	glyphID    uint16
	firstLayer uint16
	numLayers  uint16
}

// layerRecord from COLRv0.
type layerRecord struct {
	glyphID      uint16
	paletteIndex uint16
}

// NewCOLRParser creates a new COLR parser from table data.
// colrData is the raw COLR table, cpalData is the raw CPAL table.
func NewCOLRParser(colrData, cpalData []byte) (*COLRParser, error) {
	return NewCOLRParserWithGlyphCount(colrData, cpalData, 0)
}

// NewCOLRParserWithGlyphCount is NewCOLRParser plus the font's total glyph
// count (maxp.numGlyphs). When non-zero, layer glyph references are validated
// against the range during parsing and bad references surface as
// ErrInvalidLayerGlyph instead of producing unrenderable layers.
func NewCOLRParserWithGlyphCount(colrData, cpalData []byte, numGlyphs int) (*COLRParser, error) {
	if len(colrData) == 0 {
		return nil, ErrNoCOLRTable
	}
	if len(cpalData) == 0 {
		return nil, ErrNoCPALTable
	}

	p := &COLRParser{
		colrData:        colrData,
		cpalData:        cpalData,
		numGlyphsInFont: numGlyphs,
	}

	if err := p.parseCOLRHeader(); err != nil {
		return nil, err
	}

	if err := p.parseCPAL(); err != nil {
		return nil, err
	}

	return p, nil
}

// parseCOLRHeader parses the COLR table header and records.
func (p *COLRParser) parseCOLRHeader() error {
	data := p.colrData
	if len(data) < 14 {
		return ErrInvalidCOLRData
	}

	p.version = binary.BigEndian.Uint16(data[0:2])

	// We support COLRv0 and basic COLRv1
	if p.version > 1 {
		return ErrUnsupportedCOLRVersion
	}

	p.numGlyphs = binary.BigEndian.Uint16(data[2:4])
	p.numGlyphsU = int(p.numGlyphs)
	baseGlyphOffset := binary.BigEndian.Uint32(data[4:8])
	layerRecordOffset := binary.BigEndian.Uint32(data[8:12])
	numLayers := binary.BigEndian.Uint16(data[12:14])

	// Offset sanity: both record arrays must start inside the table.
	if int(baseGlyphOffset) >= len(data) || int(layerRecordOffset) >= len(data) {
		return ErrInvalidCOLRData
	}

	// Parse base glyph records
	if err := p.parseBaseGlyphs(baseGlyphOffset); err != nil {
		return err
	}

	// Parse layer records
	return p.parseLayers(layerRecordOffset, numLayers)
}

// parseBaseGlyphs parses the base glyph records.
func (p *COLRParser) parseBaseGlyphs(offset uint32) error {
	data := p.colrData
	recordSize := 6 // glyphID (2) + firstLayer (2) + numLayers (2)

	// Guard the full record array up front so the per-record bounds check
	// below cannot overflow on a huge numGlyphs value.
	end := int(offset) + p.numGlyphsU*recordSize
	// Validate each declared layer count before the array bounds check:
	// a hostile table can combine a huge count with a truncated body, and
	// the count violation is the more precise error to report.
	for i := 0; i < p.numGlyphsU; i++ {
		pos := int(offset) + i*recordSize
		if pos+recordSize > len(data) {
			break // truncated body handled by the end check below
		}
		if int(binary.BigEndian.Uint16(data[pos+4:pos+6])) > maxCOLRLayers {
			return ErrTooManyLayers
		}
	}
	if end < int(offset) || end > len(data) {
		return ErrInvalidCOLRData
	}

	for i := 0; i < p.numGlyphsU; i++ {
		pos := int(offset) + i*recordSize

		record := baseGlyphRecord{
			glyphID:    binary.BigEndian.Uint16(data[pos : pos+2]),
			firstLayer: binary.BigEndian.Uint16(data[pos+2 : pos+4]),
			numLayers:  binary.BigEndian.Uint16(data[pos+4 : pos+6]),
		}

		// Reject implausible layer counts before any allocation happens.
		if int(record.numLayers) > maxCOLRLayers {
			return ErrTooManyLayers
		}
		p.baseGlyphs = append(p.baseGlyphs, record)
	}

	return nil
}

// parseLayers parses the layer records.
func (p *COLRParser) parseLayers(offset uint32, numLayers uint16) error {
	data := p.colrData
	recordSize := 4 // glyphID (2) + paletteIndex (2)

	end := int(offset) + int(numLayers)*recordSize
	if end < int(offset) || end > len(data) {
		return ErrInvalidCOLRData
	}

	for i := 0; i < int(numLayers); i++ {
		pos := int(offset) + i*recordSize

		record := layerRecord{
			glyphID:      binary.BigEndian.Uint16(data[pos : pos+2]),
			paletteIndex: binary.BigEndian.Uint16(data[pos+2 : pos+4]),
		}

		// Validate the layer glyph against the font range when known.
		// The foreground sentinel applies to the palette index, not the
		// glyph id, so every layer must name a real glyph.
		if p.numGlyphsInFont > 0 && int(record.glyphID) >= p.numGlyphsInFont {
			return ErrInvalidLayerGlyph
		}
		p.layers = append(p.layers, record)
	}

	return nil
}

// parseCPAL parses the CPAL (Color Palette) table.
func (p *COLRParser) parseCPAL() error {
	data := p.cpalData
	if len(data) < 12 {
		return ErrInvalidCPALData
	}

	version := binary.BigEndian.Uint16(data[0:2])
	if version > 1 {
		// We handle v0 and v1 the same for basic color extraction
		_ = version
	}

	numEntries := int(binary.BigEndian.Uint16(data[2:4]))
	numPalettes := int(binary.BigEndian.Uint16(data[4:6]))
	// numColorRecords := binary.BigEndian.Uint16(data[6:8]) // unused for now
	colorRecordsOffset := int(binary.BigEndian.Uint32(data[8:12]))

	if numEntries == 0 || numPalettes == 0 {
		return ErrInvalidCPALData
	}

	// Parse palette offsets. Check the full array up front to avoid
	// int wraparound on hostile numPalettes values.
	offsetsEnd := 12 + numPalettes*2
	if offsetsEnd < 12 || offsetsEnd > len(data) {
		return ErrInvalidCPALData
	}

	paletteOffsets := make([]uint16, numPalettes)
	for i := 0; i < numPalettes; i++ {
		pos := 12 + i*2
		paletteOffsets[i] = binary.BigEndian.Uint16(data[pos : pos+2])
	}

	// Parse color records for each palette. Every palette references
	// numEntries consecutive color records starting at its offset.
	p.palettes = make([][]Color, numPalettes)
	for i := 0; i < numPalettes; i++ {
		palette := make([]Color, numEntries)
		for j := 0; j < numEntries; j++ {
			colorIndex := int(paletteOffsets[i]) + j
			pos := colorRecordsOffset + colorIndex*4
			if pos < colorRecordsOffset || pos+4 > len(data) {
				return ErrInvalidCPALData
			}

			// CPAL stores colors as BGRA
			palette[j] = Color{
				B: data[pos],
				G: data[pos+1],
				R: data[pos+2],
				A: data[pos+3],
			}
		}
		p.palettes[i] = palette
	}

	return nil
}

// HasGlyph returns true if the glyph ID is a color glyph.
func (p *COLRParser) HasGlyph(glyphID uint16) bool {
	_, found := p.findBaseGlyph(glyphID)
	return found
}

// GetGlyph returns the COLRGlyph for the given glyph ID.
// Returns ErrGlyphNotInCOLR if the glyph is not a color glyph.
//
// Palette resolution rules:
//   - paletteIndex 0 selects the font's first palette; any out-of-range
//     palette index falls back to palette 0 (the spec's default palette).
//   - A layer's palette index 0xFFFF means foreground: the caller's text
//     color is used at draw time and Color is left zero.
//   - A non-foreground palette index that exceeds the palette entry count
//     yields ErrPaletteIndexOutOfRange so the caller can decide on a
//     monochrome fallback instead of silently painting the wrong color.
func (p *COLRParser) GetGlyph(glyphID uint16, paletteIndex int) (*COLRGlyph, error) {
	record, found := p.findBaseGlyph(glyphID)
	if !found {
		return nil, ErrGlyphNotInCOLR
	}

	if int(record.numLayers) > maxCOLRLayers {
		return nil, ErrTooManyLayers
	}

	// Unknown palette: documented, deterministic fallback to palette 0.
	if paletteIndex < 0 || paletteIndex >= len(p.palettes) {
		paletteIndex = 0
	}
	palette := p.palettes[paletteIndex]

	glyph := &COLRGlyph{
		GlyphID: glyphID,
		Layers:  make([]ColorLayer, 0, record.numLayers),
		Version: p.version,
	}

	// Extract layers
	for i := 0; i < int(record.numLayers); i++ {
		layerIdx := int(record.firstLayer) + i
		if layerIdx < 0 || layerIdx >= len(p.layers) {
			return nil, ErrInvalidCOLRData
		}

		layer := p.layers[layerIdx]

		// Validate the referenced glyph against the font range when known.
		if p.numGlyphsInFont > 0 && int(layer.glyphID) >= p.numGlyphsInFont {
			return nil, ErrInvalidLayerGlyph
		}

		cl := ColorLayer{
			GlyphID:      layer.glyphID,
			PaletteIndex: layer.paletteIndex,
		}

		// Resolve color from palette. The foreground sentinel keeps a zero
		// Color; the renderer substitutes the caller's text color.
		if cl.PaletteIndex != ForegroundColorIndex {
			if int(cl.PaletteIndex) >= len(palette) {
				return nil, ErrPaletteIndexOutOfRange
			}
			cl.Color = palette[cl.PaletteIndex]
		}

		glyph.Layers = append(glyph.Layers, cl)
	}

	return glyph, nil
}

// findBaseGlyph finds the base glyph record for a glyph ID.
// Uses binary search since glyphs are sorted.
func (p *COLRParser) findBaseGlyph(glyphID uint16) (baseGlyphRecord, bool) {
	// Binary search
	lo, hi := 0, len(p.baseGlyphs)
	for lo < hi {
		mid := (lo + hi) / 2
		if p.baseGlyphs[mid].glyphID < glyphID {
			lo = mid + 1
		} else {
			hi = mid
		}
	}

	if lo < len(p.baseGlyphs) && p.baseGlyphs[lo].glyphID == glyphID {
		return p.baseGlyphs[lo], true
	}

	return baseGlyphRecord{}, false
}

// NumPalettes returns the number of color palettes.
func (p *COLRParser) NumPalettes() int {
	return len(p.palettes)
}

// EffectivePaletteIndex maps a caller-requested palette index to one that is
// valid for this font: out-of-range requests (including negative values)
// fall back to palette 0, the OpenType-specified default palette.
func (p *COLRParser) EffectivePaletteIndex(paletteIndex int) int {
	if paletteIndex < 0 || paletteIndex >= len(p.palettes) {
		return 0
	}
	return paletteIndex
}

// PaletteColors returns the colors in a palette.
func (p *COLRParser) PaletteColors(paletteIndex int) []Color {
	if paletteIndex < 0 || paletteIndex >= len(p.palettes) {
		return nil
	}
	return p.palettes[paletteIndex]
}

// ResolveLayerColor determines the RGBA color for a layer given the
// foreground (caller-supplied text) color and a global opacity.
//
// Foreground layers (palette index 0xFFFF) use foreground; palette layers
// use their resolved CPAL color. An opacity <= 0 means "no extra alpha" and
// leaves the layer's own alpha untouched; values in (0,1) multiply alpha.
// Transparent palette colors stay transparent.
func ResolveLayerColor(layer ColorLayer, foreground color.Color, opacity float64) color.RGBA {
	var base color.RGBA
	if layer.IsForeground() {
		base = toRGBA(foreground)
	} else {
		base = layer.Color.ToRGBA()
	}
	// Zero (or negative) is the EmojiOptions zero value: it must mean
	// "fully opaque / no additional alpha", not "invisible".
	if opacity <= 0 || opacity >= 1 {
		return base
	}
	scaled := float64(base.A) * clamp01(opacity)
	base.A = uint8(scaled + 0.5)
	return base
}

// toRGBA normalizes any color.Color to a straight-alpha color.RGBA.
func toRGBA(c color.Color) color.RGBA {
	if c == nil {
		return color.RGBA{A: 255}
	}
	if rgba, ok := c.(color.RGBA); ok {
		return rgba
	}
	r, g, b, a := c.RGBA()
	if a == 0 {
		return color.RGBA{}
	}
	// color.Color.RGBA returns premultiplied 16-bit values; un-premultiply
	// to get straight 8-bit channels.
	return color.RGBA{
		R: uint8((uint64(r)*255 + uint64(a)/2) / uint64(a) >> 8),
		G: uint8((uint64(g)*255 + uint64(a)/2) / uint64(a) >> 8),
		B: uint8((uint64(b)*255 + uint64(a)/2) / uint64(a) >> 8),
		A: uint8(a >> 8),
	}
}

// RenderCOLRToImage renders a COLR glyph to an RGBA image.
// This is a simplified renderer that composites layers.
// For production use, each layer glyph should be rasterized
// at the given size and composited with its color.
//
// Parameters:
//   - glyph: The COLR glyph to render
//   - renderLayer: Function to render a single layer glyph to an alpha mask
//   - width, height: Size of the output image
//   - foreground: Color to use for foreground (text) layers
func RenderCOLRToImage(
	glyph *COLRGlyph,
	renderLayer func(glyphID uint16) *image.Alpha,
	width, height int,
	foreground color.RGBA,
) *image.RGBA {
	if glyph == nil || len(glyph.Layers) == 0 {
		return nil
	}

	result := image.NewRGBA(image.Rect(0, 0, width, height))

	for _, layer := range glyph.Layers {
		// Render the layer glyph to an alpha mask
		mask := renderLayer(layer.GlyphID)
		if mask == nil {
			continue
		}

		// Determine layer color
		var layerColor color.RGBA
		if layer.IsForeground() {
			layerColor = foreground
		} else {
			layerColor = layer.Color.ToRGBA()
		}

		// Create a uniform color image
		colorImg := image.NewUniform(layerColor)

		// Composite layer onto result
		draw.DrawMask(result, result.Bounds(), colorImg, image.Point{}, mask, image.Point{}, draw.Over)
	}

	return result
}

// GlyphRenderer is a function type for rendering a glyph ID to an alpha mask.
type GlyphRenderer func(glyphID uint16, size float64) *image.Alpha

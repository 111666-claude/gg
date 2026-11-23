// Layered COLR/CPAL glyph rendering.
//
// A COLRv0 base glyph is an ordered list of layer records, each pairing a
// glyph outline with a CPAL color (or the foreground-text sentinel). The
// renderer rasterizes every layer outline through the same
// GlyphMaskRasterizer used by the monochrome pipeline and composites the
// colored masks bottom-to-top with source-over alpha blending.
//
// Correctness properties:
//   - Layer outlines share the font, ppem and subpixel origin, so the stack
//     stays aligned under any face size or fractional draw position.
//   - The base glyph's own hmtx advance (computed by the caller) is the only
//     advance consumed; layers never move the cursor.
//   - Every malformed-input case degrades to a monochrome outline instead of
//     panicking. Typed parser errors (which name the offending glyph ID) are
//     available to callers via ColorFont.COLRGlyph when they need to log.
package text

import (
	"image"
	"image/color"
	"image/draw"
	"math"

	"github.com/gogpu/gg/text/emoji"
)

// colrRasterizer renders COLR layered glyphs. It wraps a GlyphMaskRasterizer
// and is NOT safe for concurrent use — each drawing goroutine should own one,
// matching GlyphMaskRasterizer's own contract.
type colrRasterizer struct {
	mask *GlyphMaskRasterizer
}

func newCOLRRasterizer() *colrRasterizer {
	return &colrRasterizer{mask: NewGlyphMaskRasterizer()}
}

// drawCOLRGlyph renders one COLR base glyph at baseline origin (x, y).
//
// On any color-table failure the function falls back to a monochrome outline
// of the base glyph id — the caller never sees an error and rendering never
// panics. The fallback covers missing/over-long layer lists, bad layer glyph
// references and out-of-range palette entries.
func drawCOLRGlyph(
	dst draw.Image,
	sf *sourceFace,
	colorFont ColorFont,
	glyphID uint16,
	ppem float64,
	x, y float64,
	col color.Color,
	opts EmojiOptions,
) {
	parsed := sf.Source().Parsed()
	palette := effectivePalette(parsed, opts.Palette)

	glyph, err := colorFont.COLRGlyph(glyphID, palette)
	if err != nil || glyph == nil || len(glyph.Layers) == 0 {
		fallbackMonochrome(dst, sf, glyphID, x, y, col)
		return
	}

	if len(glyph.Layers) > emoji.MaxCOLRLayers() {
		fallbackMonochrome(dst, sf, glyphID, x, y, col)
		return
	}

	foreground := opts.Foreground
	if foreground == nil {
		foreground = col
	}
	opacity := opts.Alpha

	r := newCOLRRasterizer()
	// Visited set guards against any cyclic layer references (defensive:
	// COLRv0 cannot express cycles, but a corrupt table could).
	visited := make(map[uint16]struct{}, len(glyph.Layers))

	intX := math.Floor(x)
	intY := math.Floor(y)
	subpixelX := x - intX
	subpixelY := y - intY
	hinting := sf.config.hinting

	for _, layer := range glyph.Layers {
		if layer.IsTransparent() {
			continue // fully transparent palette entry: no pixels, no work
		}
		if _, seen := visited[layer.GlyphID]; seen {
			fallbackMonochrome(dst, sf, glyphID, x, y, col)
			return
		}
		visited[layer.GlyphID] = struct{}{}

		layerColor := emoji.ResolveLayerColor(layer, foreground, opacity)
		if layerColor.A == 0 {
			continue // global alpha can also drive a layer to zero
		}

		if !r.compositeLayer(dst, parsed, layer.GlyphID, ppem,
			intX, intY, subpixelX, subpixelY, hinting, layerColor) {
			// A layer glyph outside the font's range or with no usable
			// outline: skip the layer rather than failing the whole glyph.
			continue
		}
	}
}

// effectivePalette resolves a requested palette index against the font's
// actual palette count, falling back to 0 for unknown indices.
func effectivePalette(parsed ParsedFont, requested int) int {
	if cf, ok := parsed.(interface {
		EffectiveColorPalette(int) int
	}); ok {
		return cf.EffectiveColorPalette(requested)
	}
	if requested < 0 {
		return 0
	}
	return requested
}

// compositeLayer rasterizes one layer glyph and paints its colored mask onto
// dst. Returns false when the layer has no renderable outline.
func (r *colrRasterizer) compositeLayer(
	dst draw.Image,
	parsed ParsedFont,
	layerGID uint16,
	ppem float64,
	intX, intY, subpixelX, subpixelY float64,
	hinting Hinting,
	layerColor color.RGBA,
) bool {
	if int(layerGID) >= parsed.NumGlyphs() {
		return false
	}

	result, err := r.mask.RasterizeHinted(parsed, GlyphID(layerGID), ppem,
		subpixelX, subpixelY, hinting)
	if err != nil || result == nil {
		return false
	}

	maskImg := &image.Alpha{
		Pix:    result.Mask,
		Stride: result.Width,
		Rect:   image.Rect(0, 0, result.Width, result.Height),
	}

	dstX := int(intX) + int(math.Round(float64(result.BearingX)))
	dstY := int(intY) - int(math.Round(float64(result.BearingY)))
	destRect := image.Rect(dstX, dstY, dstX+result.Width, dstY+result.Height)

	// DrawMask clips to dst.Bounds on its own; an empty intersection simply
	// means the rasterized layer sits entirely off-screen.
	if destRect.Intersect(dst.Bounds()).Empty() {
		return true
	}

	src := image.NewUniform(layerColor)
	draw.DrawMask(dst, destRect, src, image.Point{}, maskImg, image.Point{}, draw.Over)
	return true
}

// fallbackMonochrome renders the base glyph as a plain outline. Used for
// every COLR failure mode so a corrupt color font degrades gracefully.
func fallbackMonochrome(dst draw.Image, sf *sourceFace, glyphID uint16, x, y float64, col color.Color) {
	// Rasterize the glyph directly with the shared per-glyph pipeline at the
	// same subpixel origin the color path would have used.
	parsed := sf.Source().Parsed()
	rast := NewGlyphMaskRasterizer()

	intX := math.Floor(x)
	intY := math.Floor(y)
	result, err := rast.RasterizeHinted(parsed, GlyphID(glyphID), sf.size,
		x-intX, y-intY, sf.config.hinting)
	if err != nil || result == nil {
		return
	}
	maskImg := &image.Alpha{
		Pix:    result.Mask,
		Stride: result.Width,
		Rect:   image.Rect(0, 0, result.Width, result.Height),
	}
	dstX := int(intX) + int(math.Round(float64(result.BearingX)))
	dstY := int(intY) - int(math.Round(float64(result.BearingY)))
	destRect := image.Rect(dstX, dstY, dstX+result.Width, dstY+result.Height)
	draw.DrawMask(dst, destRect, image.NewUniform(col), image.Point{}, maskImg, image.Point{}, draw.Over)
}

package svg

import (
	"math"
	"strings"

	"github.com/gogpu/gg"
)

// renderUse instantiates a referenced element. The instance is treated like a
// group: its transform, x/y, presentation attributes and opacity are applied in
// an isolated state so the referenced definition is never mutated and sibling
// instances cannot influence one another.
func renderUse(dc *gg.Context, use *UseElement, state *renderState) {
	if use == nil || use.Ref == nil {
		return
	}
	placement := usePlacementMatrix(use)

	child := *state
	child.matrix = state.matrix.Multiply(placement)
	child.opacity = state.opacity * use.Attrs.Opacity
	if use.Attrs.Fill != "" {
		child.parentFill = use.Attrs.Fill
	}
	if use.Attrs.Stroke != "" {
		child.parentStroke = use.Attrs.Stroke
	}

	dc.Push()
	dc.Transform(placement)
	renderUseTarget(dc, use, &child)
	dc.Pop()
}

// renderUseTarget dispatches on the referenced element kind. Symbols and nested
// svgs establish a viewBox viewport; every other target is rendered directly.
func renderUseTarget(dc *gg.Context, use *UseElement, state *renderState) {
	switch target := use.Ref.(type) {
	case *SymbolElement:
		renderSymbol(dc, target, use, state)
	default:
		renderElement(dc, target, state)
	}
}

// renderSymbol maps the symbol's viewBox onto the instance viewport and renders
// its children inside that coordinate system.
func renderSymbol(dc *gg.Context, symbol *SymbolElement, use *UseElement, state *renderState) {
	viewportWidth, viewportHeight := symbolViewportSize(use)

	child := *state
	if symbol.Attrs.Fill != "" {
		child.parentFill = symbol.Attrs.Fill
	}
	if symbol.Attrs.Stroke != "" {
		child.parentStroke = symbol.Attrs.Stroke
	}
	child.opacity = state.opacity * symbol.Attrs.Opacity

	mapping := viewBoxMapping(symbol.ViewBox, viewportWidth, viewportHeight, symbol.PreserveAspectRatio)
	child.matrix = state.matrix.Multiply(mapping)

	dc.Push()
	dc.Transform(mapping)
	renderElements(dc, symbol.Children, &child)
	dc.Pop()
}

// usePlacementMatrix combines the instance transform with the x/y translation.
// Per SVG the x/y attributes are equivalent to an additional translate applied
// before the element's own transform list.
func usePlacementMatrix(use *UseElement) gg.Matrix {
	placement := gg.Translate(use.X, use.Y)
	if use.Attrs.Transform != "" {
		if parsed, err := transformMatrix(use.Attrs.Transform); err == nil {
			placement = parsed.Multiply(placement)
		}
	}
	return placement
}

// symbolViewportSize resolves the viewport established by a <use> for a symbol
// reference. Explicit width/height win; otherwise the symbol's viewBox
// dimensions are used so zero-dimensioned instances stay invisible rather than
// collapsing to a divide-by-zero scale.
func symbolViewportSize(use *UseElement) (float64, float64) {
	width, height := use.Width, use.Height
	if !use.hasWidth || width == 0 {
		if symbol, ok := use.Ref.(*SymbolElement); ok && symbol.ViewBox.Width > 0 {
			width = symbol.ViewBox.Width
		}
	}
	if !use.hasHeight || height == 0 {
		if symbol, ok := use.Ref.(*SymbolElement); ok && symbol.ViewBox.Height > 0 {
			height = symbol.ViewBox.Height
		}
	}
	return width, height
}

// viewBoxMapping maps a source viewBox into a target viewport. An empty or
// degenerate viewBox/viewport yields the identity transform. preserveAspectRatio
// follows the SVG grammar: "none" stretches independently; any other value
// (default "xMidYMid meet") uniformly scales and aligns the content.
func viewBoxMapping(vb ViewBox, targetWidth, targetHeight float64, preserveAspectRatio string) gg.Matrix {
	if vb.Width <= 0 || vb.Height <= 0 || targetWidth <= 0 || targetHeight <= 0 {
		return gg.Identity()
	}

	scaleX := targetWidth / vb.Width
	scaleY := targetHeight / vb.Height

	align, meet := parsePreserveAspectRatio(preserveAspectRatio)
	if align == "none" {
		return gg.Scale(scaleX, scaleY).
			Multiply(gg.Translate(-vb.MinX, -vb.MinY))
	}

	scale := math.Min(scaleX, scaleY)
	if !meet {
		// slice fills the viewport, clipping the overflow.
		scale = math.Max(scaleX, scaleY)
	}

	contentWidth := vb.Width * scale
	contentHeight := vb.Height * scale
	offsetX := alignOffset(align[0:1], targetWidth, contentWidth)
	offsetY := alignOffset(align[1:2], targetHeight, contentHeight)

	// Translate the aligned viewport position, then apply the uniform scale
	// and the viewBox origin offset.
	return gg.Translate(offsetX, offsetY).
		Multiply(gg.Scale(scale, scale)).
		Multiply(gg.Translate(-vb.MinX, -vb.MinY))
}

// parsePreserveAspectRatio extracts the alignment token and whether the mode is
// "meet". Unrecognized values fall back to the SVG default xMidYMid meet.
func parsePreserveAspectRatio(value string) (align string, meet bool) {
	meet = true
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) == 0 {
		return "xMidYMid", true
	}
	align = fields[0]
	if len(fields) >= 2 && fields[1] == "slice" {
		meet = false
	}
	switch align {
	case "none", "xMinYMin", "xMidYMin", "xMaxYMin",
		"xMinYMid", "xMidYMid", "xMaxYMid",
		"xMinYMax", "xMidYMax", "xMaxYMax":
		return align, meet
	default:
		return "xMidYMid", true
	}
}

// alignOffset maps an x/y alignment token (Min, Mid, Max) to the translation
// needed to position scaled content of contentSize inside viewportSize.
func alignOffset(token string, viewportSize, contentSize float64) float64 {
	switch token {
	case "Min":
		return 0
	case "Max":
		return viewportSize - contentSize
	default: // Mid, including any malformed token.
		return (viewportSize - contentSize) / 2
	}
}

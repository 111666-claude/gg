package svg

import (
	"math"

	"github.com/gogpu/gg"
	"image/color"
)

// renderState is shared by immediate and retained traversal. Matrix is the
// complete logical transform at the current nesting level.
type renderState struct {
	doc           *Document
	viewBox       ViewBox
	overrideColor color.Color
	parentFill    string
	parentStroke  string
	opacity       float64
	matrix        gg.Matrix
	targetWidth   float64
	targetHeight  float64
	deviceScale   float64
	outerMatrix   gg.Matrix
}

type resolvedFill struct {
	present bool
	rule    gg.FillRule
	color   gg.RGBA
}

type resolvedStroke struct {
	present bool
	width   float64
	cap     gg.LineCap
	join    gg.LineJoin
	color   gg.RGBA
}

type resolvedStyle struct {
	fill   resolvedFill
	stroke resolvedStroke
}

func renderElements(dc *gg.Context, elements []Element, state *renderState) {
	for _, elem := range elements {
		renderElement(dc, elem, state)
	}
}

func renderElement(dc *gg.Context, elem Element, state *renderState) {
	switch e := elem.(type) {
	case *PathElement:
		if e.D == "" {
			return
		}
		path, err := gg.ParseSVGPath(e.D)
		if err != nil {
			return
		}
		renderGeometry(dc, path, &e.Attrs, state, true)
	case *CircleElement:
		path := gg.NewPath()
		path.Circle(e.CX, e.CY, e.R)
		renderGeometry(dc, path, &e.Attrs, state, true)
	case *RectElement:
		path := gg.NewPath()
		if e.RX > 0 || e.RY > 0 {
			r := e.RX
			if e.RY > r {
				r = e.RY
			}
			path.RoundedRectangle(e.X, e.Y, e.W, e.H, r)
		} else {
			path.Rectangle(e.X, e.Y, e.W, e.H)
		}
		renderGeometry(dc, path, &e.Attrs, state, true)
	case *EllipseElement:
		path := gg.NewPath()
		path.Ellipse(e.CX, e.CY, e.RX, e.RY)
		renderGeometry(dc, path, &e.Attrs, state, true)
	case *LineElement:
		path := gg.NewPath()
		path.MoveTo(e.X1, e.Y1)
		path.LineTo(e.X2, e.Y2)
		renderGeometry(dc, path, &e.Attrs, state, false)
	case *PolygonElement:
		if path := pointsPath(e.Points, true); path != nil {
			renderGeometry(dc, path, &e.Attrs, state, true)
		}
	case *PolylineElement:
		if path := pointsPath(e.Points, false); path != nil {
			renderGeometry(dc, path, &e.Attrs, state, true)
		}
	case *GroupElement:
		renderGroup(dc, e, state)
	}
}

func renderGeometry(dc *gg.Context, path *gg.Path, attrs *Attrs, state *renderState, allowFill bool) {
	local := elementTransformMatrix(attrs)
	complete := state.matrix.Multiply(local)
	style := resolveStyle(attrs, state)
	if !allowFill {
		style.fill.present = false
	}
	if !style.fill.present && !style.stroke.present {
		return
	}

	// Resolve gradient paint servers (fill/stroke="url(#id)"). The brush is
	// sampled in device space, so it needs the complete CTM and the local
	// object bounding box for objectBoundingBox units.
	bounds, _ := pathFloatBounds(path)
	var fillBrush, strokeBrush gg.Brush
	if state.overrideColor == nil && state.doc != nil {
		if ref, _, isRef := parsePaintRef(resolveFill(attrs, state)); isRef {
			if g, ok := state.doc.Gradient(ref); ok {
				alpha := clampOpacity(attrs.FillOpacity * attrs.Opacity * state.opacity)
				fillBrush = buildGradientBrush(g, complete, bounds, state.viewBox, alpha)
			}
		}
		if ref, _, isRef := parsePaintRef(resolveStroke(attrs, state)); isRef {
			if g, ok := state.doc.Gradient(ref); ok {
				alpha := clampOpacity(attrs.StrokeOpacity * attrs.Opacity * state.opacity)
				strokeBrush = buildGradientBrush(g, complete, bounds, state.viewBox, alpha)
			}
		}
	}

	dc.Push()
	dc.Transform(local)
	if style.fill.present {
		dc.SetFillRule(style.fill.rule)
		if fillBrush != nil {
			dc.SetFillBrush(fillBrush)
		} else {
			setResolvedColor(dc, style.fill.color)
		}
		dc.DrawPath(path)
		_ = dc.Fill()
	}
	if style.stroke.present {
		policy := newStrokeHintPolicy(state.targetWidth, state.targetHeight, state.deviceScale, complete)
		strokePath := hintStrokePath(path, policy, style.stroke.width)
		dc.SetLineWidth(style.stroke.width)
		dc.SetLineCap(style.stroke.cap)
		dc.SetLineJoin(style.stroke.join)
		if strokeBrush != nil {
			dc.SetStrokeBrush(strokeBrush)
		} else {
			setResolvedColor(dc, style.stroke.color)
		}
		dc.DrawPath(strokePath)
		_ = dc.Stroke()
	}
	dc.Pop()
}

func renderGroup(dc *gg.Context, group *GroupElement, state *renderState) {
	local := elementTransformMatrix(&group.Attrs)
	child := *state
	child.matrix = state.matrix.Multiply(local)
	child.opacity *= group.Attrs.Opacity
	if group.Attrs.Fill != "" {
		child.parentFill = group.Attrs.Fill
	}
	if group.Attrs.Stroke != "" {
		child.parentStroke = group.Attrs.Stroke
	}

	dc.Push()
	dc.Transform(local)
	renderElements(dc, group.Children, &child)
	dc.Pop()
}

func resolveStyle(attrs *Attrs, state *renderState) resolvedStyle {
	fillString := resolveFill(attrs, state)
	strokeString := resolveStroke(attrs, state)

	fillRule := gg.FillRuleNonZero
	rule := attrs.FillRule
	if rule == "" {
		rule = attrs.ClipRule
	}
	if rule == "evenodd" {
		fillRule = gg.FillRuleEvenOdd
	}

	style := resolvedStyle{
		fill: resolvedFill{
			present: fillString != colorNone,
			rule:    fillRule,
		},
		stroke: resolvedStroke{
			present: strokeString != "" && strokeString != colorNone,
			width:   attrs.StrokeWidth,
			cap:     resolveLineCap(attrs.StrokeCap),
			join:    resolveLineJoin(attrs.StrokeJoin),
		},
	}

	fillColor, fillOK := resolvePaintColor(fillString, color.Black, state.overrideColor,
		attrs.FillOpacity*attrs.Opacity*state.opacity)
	style.fill.color = fillColor
	style.fill.present = style.fill.present && fillOK

	strokeColor, strokeOK := resolvePaintColor(strokeString, nil, state.overrideColor,
		attrs.StrokeOpacity*attrs.Opacity*state.opacity)
	style.stroke.color = strokeColor
	style.stroke.present = style.stroke.present && strokeOK

	// Valid gradient paint servers must stay "present" even though the plain
	// color parser cannot understand url(#id). Give the retained/scene path a
	// representative solid color; the immediate renderer installs the real
	// sampling brush afterwards. A resolved gradient with no stops paints
	// nothing (transparent), matching the SVG degenerate case.
	if state.overrideColor == nil && state.doc != nil {
		if id, _, isRef := parsePaintRef(fillString); isRef {
			if g, ok := state.doc.Gradient(id); ok {
				if len(g.Stops) == 0 {
					style.fill.present = false
				} else {
					style.fill.present = true
					if c, ok := representativeGradientColor(g); ok {
						c.A = clampOpacity(c.A * attrs.FillOpacity * attrs.Opacity * state.opacity)
						style.fill.color = c
					}
				}
			}
		}
		if id, _, isRef := parsePaintRef(strokeString); isRef {
			if g, ok := state.doc.Gradient(id); ok {
				if len(g.Stops) == 0 {
					style.stroke.present = false
				} else {
					style.stroke.present = true
					if c, ok := representativeGradientColor(g); ok {
						c.A = clampOpacity(c.A * attrs.StrokeOpacity * attrs.Opacity * state.opacity)
						style.stroke.color = c
					}
				}
			}
		}
	}
	return style
}

// representativeGradientColor picks a stable solid stand-in for a gradient
// when only solid paints are available (retained scene encoding). It uses the
// final stop, matching the degenerate-gradient flat color.
func representativeGradientColor(g *Gradient) (gg.RGBA, bool) {
	if g == nil || len(g.Stops) == 0 {
		return gg.RGBA{}, false
	}
	return g.Stops[len(g.Stops)-1].Color, true
}

// clampOpacity clamps a composed opacity product to [0,1].
func clampOpacity(v float64) float64 {
	if v < 0 || math.IsNaN(v) {
		return 0
	}
	if v > 1 || math.IsInf(v, 1) {
		return 1
	}
	return v
}

func resolveFill(attrs *Attrs, state *renderState) string {
	if attrs.Fill != "" {
		return attrs.Fill
	}
	if state.parentFill != "" {
		return state.parentFill
	}
	return "" // SVG's default fill is black.
}

func resolveStroke(attrs *Attrs, state *renderState) string {
	if attrs.Stroke != "" {
		return attrs.Stroke
	}
	return state.parentStroke
}

func resolvePaintColor(value string, fallback color.Color, override color.Color, opacity float64) (gg.RGBA, bool) {
	var c color.Color
	if override != nil && value != colorNone {
		c = override
	} else {
		parsed, err := parseColor(value)
		if err != nil || parsed == nil {
			c = fallback
		} else {
			c = parsed
		}
	}
	if c == nil {
		return gg.RGBA{}, false
	}
	if opacity < 0 {
		opacity = 0
	} else if opacity > 1 {
		opacity = 1
	}
	result := gg.FromColor(c)
	result.A *= opacity
	return result, true
}

func resolveLineCap(value string) gg.LineCap {
	switch value {
	case "round":
		return gg.LineCapRound
	case "square":
		return gg.LineCapSquare
	default:
		return gg.LineCapButt
	}
}

func resolveLineJoin(value string) gg.LineJoin {
	switch value {
	case "round":
		return gg.LineJoinRound
	case "bevel":
		return gg.LineJoinBevel
	default:
		return gg.LineJoinMiter
	}
}

func setResolvedColor(dc *gg.Context, c gg.RGBA) {
	dc.SetRGBA(c.R, c.G, c.B, c.A)
}

func elementTransformMatrix(attrs *Attrs) gg.Matrix {
	if attrs == nil || attrs.Transform == "" {
		return gg.Identity()
	}
	matrix, _ := transformMatrix(attrs.Transform)
	return matrix
}

func pointsPath(points []float64, closed bool) *gg.Path {
	if len(points) < 4 {
		return nil
	}
	path := gg.NewPath()
	for i := 0; i+1 < len(points); i += 2 {
		if i == 0 {
			path.MoveTo(points[i], points[i+1])
		} else {
			path.LineTo(points[i], points[i+1])
		}
	}
	if closed {
		path.Close()
	}
	return path
}

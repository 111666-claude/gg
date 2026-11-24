package svg

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

type textCoords struct {
	x, y, dx, dy             float64
	xs, ys, dxs, dys         []float64
	xSet, ySet, dxSet, dySet bool
	errs                     []error
}

func parseTextCoords(se xml.StartElement) textCoords {
	var c textCoords
	for _, attr := range se.Attr {
		var dst *[]float64
		var scalar *float64
		var set *bool
		switch attr.Name.Local {
		case "x":
			dst, scalar, set = &c.xs, &c.x, &c.xSet
		case "y":
			dst, scalar, set = &c.ys, &c.y, &c.ySet
		case "dx":
			dst, scalar, set = &c.dxs, &c.dx, &c.dxSet
		case "dy":
			dst, scalar, set = &c.dys, &c.dy, &c.dySet
		default:
			continue
		}
		*set = true
		values, err := parseNumberList(attr.Value)
		if err != nil {
			c.errs = append(c.errs, fmt.Errorf("%w %s=%q: %v", ErrInvalidCoordinate, attr.Name.Local, attr.Value, err))
		}
		*dst = values
		if len(values) > 0 {
			*scalar = values[0]
		}
	}
	return c
}

func parseNumberList(value string) ([]float64, error) {
	value = strings.ReplaceAll(value, ",", " ")
	fields := strings.Fields(value)
	out := make([]float64, 0, len(fields))
	var firstErr error
	for _, field := range fields {
		v, err := parseSVGCoordinate(field)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		out = append(out, v)
	}
	return out, firstErr
}

func parseSVGCoordinate(value string) (float64, error) {
	return parseDimension(strings.TrimSpace(value))
}

func parseSVGFontSize(value string) (float64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("empty font-size")
	}
	negative := strings.HasPrefix(value, "-")
	v, err := parseDimension(value)
	if err != nil {
		return 0, err
	}
	if negative && v > 0 {
		v = -v
	}
	if v < 0 {
		return 0, fmt.Errorf("negative font-size %q", value)
	}
	return v, nil
}

func parseFontWeightValue(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "bold", "bolder":
		return 700
	case "normal", "lighter", "":
		return 400
	default:
		if v, err := strconv.Atoi(value); err == nil {
			if v < 100 {
				v *= 100
			}
			if v < 100 {
				return 100
			}
			if v > 900 {
				return 900
			}
			return v
		}
		return 400
	}
}

func parseStyleProperties(style string) map[string]string {
	props := make(map[string]string)
	for _, declaration := range strings.Split(style, ";") {
		declaration = strings.TrimSpace(declaration)
		if declaration == "" {
			continue
		}
		name, value, ok := strings.Cut(declaration, ":")
		if !ok {
			continue
		}
		name = strings.ToLower(strings.TrimSpace(name))
		value = strings.TrimSpace(value)
		if name != "" {
			props[name] = value
		}
	}
	return props
}

func applyInlineStyle(a *Attrs) {
	for name, value := range parseStyleProperties(a.Style) {
		applyPresentationAttribute(a, name, value)
	}
}

func applyPresentationAttribute(a *Attrs, name, value string) {
	switch name {
	case "fill":
		a.Fill = value
	case "fill-rule":
		a.FillRule = value
	case "fill-opacity":
		if v, err := strconv.ParseFloat(value, 64); err == nil {
			a.FillOpacity = v
		}
	case "stroke":
		a.Stroke = value
	case "stroke-width":
		if v, err := parseDimension(value); err == nil {
			a.StrokeWidth = v
		}
	case "stroke-linecap":
		a.StrokeCap = value
	case "stroke-linejoin":
		a.StrokeJoin = value
	case "stroke-opacity":
		if v, err := strconv.ParseFloat(value, 64); err == nil {
			a.StrokeOpacity = v
		}
	case "opacity":
		if v, err := strconv.ParseFloat(value, 64); err == nil {
			a.Opacity = v
		}
	case "transform":
		a.Transform = value
	case "clip-rule":
		a.ClipRule = value
	case "font-family":
		a.FontFamily = value
	case "font-size":
		a.FontSizeRaw = strings.TrimSpace(value)
		a.FontSizeSet = true
		if size, err := parseSVGFontSize(value); err == nil {
			a.FontSize = size
		}
	case "font-weight":
		a.FontWeight = strings.TrimSpace(value)
		a.FontWeightSet = true
		a.FontWeightValue = parseFontWeightValue(value)
	case "font-style":
		a.FontStyle = strings.TrimSpace(value)
	case "text-anchor":
		a.TextAnchor = strings.TrimSpace(value)
	case "direction":
		a.Direction = strings.TrimSpace(value)
	case "xml-space":
		a.XMLSpace = strings.TrimSpace(value)
	}
}

func parseTextElement(decoder *xml.Decoder, se xml.StartElement) (*TextElement, error) {
	coords := parseTextCoords(se)
	elem := &TextElement{
		Attrs:       parseAttrs(se),
		X:           coords.x,
		Y:           coords.y,
		DX:          coords.dx,
		DY:          coords.dy,
		XValues:     coords.xs,
		YValues:     coords.ys,
		DXValues:    coords.dxs,
		DYValues:    coords.dys,
		XSet:        coords.xSet,
		YSet:        coords.ySet,
		DXSet:       coords.dxSet,
		DYSet:       coords.dySet,
		ParseErrors: coords.errs,
	}

	var direct strings.Builder
	for {
		tok, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("svg: error reading <text>: %w", err)
		}
		switch t := tok.(type) {
		case xml.CharData:
			s := normalizeSVGCharData(string(t), elem.Attrs.XMLSpace)
			if s == "" {
				continue
			}
			direct.WriteString(s)
			run := &TextRun{Content: s, Text: s, Value: s}
			elem.Children = append(elem.Children, run)
		case xml.StartElement:
			if t.Name.Local == "tspan" {
				span, err := parseTSpanElement(decoder, t)
				if err != nil {
					return nil, err
				}
				elem.TSpans = append(elem.TSpans, span)
				elem.Spans = append(elem.Spans, *span)
				elem.Children = append(elem.Children, span)
			} else if err := decoder.Skip(); err != nil {
				return nil, fmt.Errorf("svg: error skipping <%s> inside <text>: %w", t.Name.Local, err)
			}
		case xml.EndElement:
			elem.DirectContent = strings.TrimSpace(direct.String())
			elem.Content = collectTextContent(elem.Children)
			elem.Text = elem.Content
			elem.Value = elem.Content
			return elem, nil
		}
	}
}

func parseTSpanElement(decoder *xml.Decoder, se xml.StartElement) (*TSpanElement, error) {
	coords := parseTextCoords(se)
	elem := &TSpanElement{
		Attrs:    parseAttrs(se),
		X:        coords.x,
		Y:        coords.y,
		DX:       coords.dx,
		DY:       coords.dy,
		XValues:  coords.xs,
		YValues:  coords.ys,
		DXValues: coords.dxs,
		DYValues: coords.dys,
		XSet:     coords.xSet,
		YSet:     coords.ySet,
		DXSet:    coords.dxSet,
		DYSet:    coords.dySet,
	}

	var direct strings.Builder
	for {
		tok, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("svg: error reading <tspan>: %w", err)
		}
		switch t := tok.(type) {
		case xml.CharData:
			s := normalizeSVGCharData(string(t), elem.Attrs.XMLSpace)
			if s == "" {
				continue
			}
			direct.WriteString(s)
			run := &TextRun{Content: s, Text: s, Value: s}
			elem.Children = append(elem.Children, run)
		case xml.StartElement:
			if t.Name.Local == "tspan" {
				child, err := parseTSpanElement(decoder, t)
				if err != nil {
					return nil, err
				}
				elem.TSpans = append(elem.TSpans, child)
				elem.Spans = append(elem.Spans, *child)
				elem.Children = append(elem.Children, child)
			} else if err := decoder.Skip(); err != nil {
				return nil, fmt.Errorf("svg: error skipping <%s> inside <tspan>: %w", t.Name.Local, err)
			}
		case xml.EndElement:
			_ = direct.String()
			elem.Content = collectTextContent(elem.Children)
			elem.Text = elem.Content
			elem.Value = elem.Content
			return elem, nil
		}
	}
}

func collectTextContent(children []Element) string {
	var b strings.Builder
	for _, child := range children {
		switch c := child.(type) {
		case *TextRun:
			b.WriteString(c.Content)
		case *TSpanElement:
			b.WriteString(c.Content)
		case *TextElement:
			b.WriteString(c.Content)
		}
	}
	return strings.TrimSpace(b.String())
}

// normalizeSVGCharData applies the common SVG/XML whitespace rules. Decoded
// XML entities arrive here as UTF-8 runes; raw tags never arrive as CharData.
func normalizeSVGCharData(s, xmlSpace string) string {
	if strings.EqualFold(xmlSpace, "preserve") {
		return s
	}
	whitespaceOnly := strings.TrimFunc(s, unicode.IsSpace) == ""
	if whitespaceOnly && strings.ContainsAny(s, "\r\n") {
		return ""
	}

	var b strings.Builder
	pendingSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			pendingSpace = b.Len() > 0 || pendingSpace
			continue
		}
		if pendingSpace {
			b.WriteByte(' ')
			pendingSpace = false
		}
		b.WriteRune(r)
	}
	// Preserve a meaningful trailing separator between an initial text run and
	// a following tspan. Formatting-only newline runs were removed above.
	if pendingSpace && b.Len() > 0 {
		b.WriteByte(' ')
	}
	return b.String()
}

func applyTextInheritance(elements []Element, parent Attrs, parentSize float64) {
	for _, elem := range elements {
		if elem == nil {
			continue
		}
		attrs := elem.attrs()
		mergeInheritedTextAttrs(attrs, parent, parentSize)
		childSize := parentSize
		if attrs.FontSizeSet && attrs.FontSize > 0 {
			childSize = attrs.FontSize
		}
		switch e := elem.(type) {
		case *GroupElement:
			applyTextInheritance(e.Children, *attrs, childSize)
		case *TextElement:
			applyTextInheritance(e.Children, *attrs, childSize)
			for _, span := range e.TSpans {
				if span != nil {
					mergeInheritedTextAttrs(&span.Attrs, *attrs, childSize)
					applyTextInheritance(span.Children, span.Attrs, childSize)
				}
			}
		case *TSpanElement:
			applyTextInheritance(e.Children, *attrs, childSize)
			for _, span := range e.TSpans {
				if span != nil {
					mergeInheritedTextAttrs(&span.Attrs, *attrs, childSize)
					applyTextInheritance(span.Children, span.Attrs, childSize)
				}
			}
		}
	}
}

func mergeInheritedTextAttrs(a *Attrs, parent Attrs, parentSize float64) {
	if a.FontFamily == "" {
		a.FontFamily = parent.FontFamily
	}
	switch {
	case a.FontSizeSet || a.FontSizeRaw != "":
		size := resolveFontSize(a.FontSizeRaw, a.FontSize, parentSize)
		a.FontSize = size
		a.FontSizeRaw = strconv.FormatFloat(size, 'f', -1, 64)
		a.FontSizeSet = true
	case parent.FontSizeSet:
		a.FontSize = parent.FontSize
		a.FontSizeRaw = parent.FontSizeRaw
		a.FontSizeSet = true
	}
	if !a.FontWeightSet && parent.FontWeightSet {
		a.FontWeight = parent.FontWeight
		a.FontWeightValue = parent.FontWeightValue
		a.FontWeightSet = true
	}
	if a.FontStyle == "" {
		a.FontStyle = parent.FontStyle
	}
	if a.TextAnchor == "" {
		a.TextAnchor = parent.TextAnchor
	}
	if a.Direction == "" {
		a.Direction = parent.Direction
	}
}

func validateTextParseErrors(elements []Element, strict bool) error {
	if !strict {
		return nil
	}
	var errs []error
	var walk func([]Element)
	walk = func(els []Element) {
		for _, el := range els {
			switch e := el.(type) {
			case *TextElement:
				errs = append(errs, e.ParseErrors...)
				walk(e.Children)
			case *TSpanElement:
				walk(e.Children)
			case *GroupElement:
				walk(e.Children)
			}
		}
	}
	walk(elements)
	return errorsJoin(errs...)
}

func errorsJoin(errs ...error) error {
	var nonNil []error
	for _, err := range errs {
		if err != nil {
			nonNil = append(nonNil, err)
		}
	}
	if len(nonNil) == 0 {
		return nil
	}
	if len(nonNil) == 1 {
		return nonNil[0]
	}
	return fmt.Errorf("%w: %v", ErrInvalidCoordinate, nonNil)
}

package svg

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
)

// Parse parses SVG XML data into a reusable [Document].
// The Document can be rendered multiple times at different sizes.
//
// Only the SVG subset needed for icon rendering is supported.
// Unsupported elements are silently skipped.
//
// When gradient markup contains a validation problem (dangling reference,
// cycle, negative radius, non-finite or oversized parameters, ...), Parse
// returns a joined error whose items are [*GradientError] carrying the
// source element path. The returned Document is still non-nil and renders
// with deterministic fallbacks; callers may ignore the error and render it.
func Parse(data []byte) (*Document, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))

	doc := &Document{
		ViewBox: ViewBox{Width: 16, Height: 16}, // sensible default
	}
	ps := &parseState{allIDs: make(map[string]bool)}

	// Find the root <svg> element.
	for {
		tok, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("svg: failed to find <svg> root element: %w", err)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "svg" {
			if err := parseSVGRoot(doc, se); err != nil {
				return nil, err
			}
			// Parse children.
			children, err := parseChildren(decoder, ps, "/svg")
			if err != nil {
				return nil, err
			}
			doc.Elements = children
			gradErr := finalizeGradients(doc, ps)
			return doc, gradErr
		}
	}
}

// parseState carries document-wide parse collections through recursion.
type parseState struct {
	defs   []*gradientDef
	allIDs map[string]bool
}

// elementPath builds an XPath-like location for a child element. Elements with
// an id use an @id predicate (stable and recognizable); siblings without id
// get a 1-based positional index among same-named siblings.
func elementPath(parent, local string, se xml.StartElement, sameTagIndex int) string {
	id := attrLocal(se, "id")
	base := parent + "/" + local
	if id != "" {
		return fmt.Sprintf(`%s[@id=%q]`, base, id)
	}
	return fmt.Sprintf("%s[%d]", base, sameTagIndex)
}

func attrLocal(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// parseSVGRoot extracts viewBox, width, height from the <svg> element attributes.
func parseSVGRoot(doc *Document, se xml.StartElement) error {
	for _, attr := range se.Attr {
		switch attr.Name.Local {
		case "viewBox":
			vb, err := parseViewBox(attr.Value)
			if err != nil {
				return err
			}
			doc.ViewBox = vb
		case "width":
			v, err := parseDimension(attr.Value)
			if err == nil {
				doc.Width = v
			}
		case "height":
			v, err := parseDimension(attr.Value)
			if err == nil {
				doc.Height = v
			}
		case "fill":
			doc.RootFill = attr.Value
		}
	}

	// If width/height not set, use viewBox.
	if doc.Width == 0 {
		doc.Width = doc.ViewBox.Width
	}
	if doc.Height == 0 {
		doc.Height = doc.ViewBox.Height
	}
	return nil
}

// parseViewBox parses a "minX minY width height" string.
func parseViewBox(s string) (ViewBox, error) {
	s = strings.ReplaceAll(s, ",", " ")
	parts := strings.Fields(s)
	if len(parts) != 4 {
		return ViewBox{}, fmt.Errorf("svg: invalid viewBox %q (expected 4 values)", s)
	}
	var vb ViewBox
	var err error
	vb.MinX, err = strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return ViewBox{}, fmt.Errorf("svg: invalid viewBox minX: %w", err)
	}
	vb.MinY, err = strconv.ParseFloat(parts[1], 64)
	if err != nil {
		return ViewBox{}, fmt.Errorf("svg: invalid viewBox minY: %w", err)
	}
	vb.Width, err = strconv.ParseFloat(parts[2], 64)
	if err != nil {
		return ViewBox{}, fmt.Errorf("svg: invalid viewBox width: %w", err)
	}
	vb.Height, err = strconv.ParseFloat(parts[3], 64)
	if err != nil {
		return ViewBox{}, fmt.Errorf("svg: invalid viewBox height: %w", err)
	}
	return vb, nil
}

// parseDimension parses an SVG dimension value, stripping optional units.
func parseDimension(s string) (float64, error) {
	// Strip common units (px, em, pt, etc.)
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "px")
	s = strings.TrimSuffix(s, "pt")
	s = strings.TrimSuffix(s, "em")
	s = strings.TrimSuffix(s, "%")
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
}

// parseChildren parses child elements until the matching end element is found.
func parseChildren(decoder *xml.Decoder, ps *parseState, parentPath string) ([]Element, error) {
	var elements []Element
	tagCounts := make(map[string]int)

	for {
		tok, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("svg: error reading XML: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			tagCounts[t.Name.Local]++
			if id := attrLocal(t, "id"); id != "" {
				ps.allIDs[id] = true
			}
			path := elementPath(parentPath, t.Name.Local, t, tagCounts[t.Name.Local])
			elem, err := parseElement(decoder, ps, t, path)
			if err != nil {
				return nil, err
			}
			if elem != nil {
				elem.attrs().Path = path
				elements = append(elements, elem)
			}

		case xml.EndElement:
			return elements, nil
		}
	}
}

// parseElement parses a single SVG element from a start element.
// Returns nil for unsupported/definition elements (silently skipped or
// registered as paint servers).
func parseElement(decoder *xml.Decoder, ps *parseState, se xml.StartElement, path string) (Element, error) {
	switch se.Name.Local {
	case "path":
		return parsePathElement(decoder, se)
	case "circle":
		return parseCircleElement(decoder, se)
	case "rect":
		return parseRectElement(decoder, se)
	case "ellipse":
		return parseEllipseElement(decoder, se)
	case "line":
		return parseLineElement(decoder, se)
	case "polygon":
		return parsePolygonElement(decoder, se)
	case "polyline":
		return parsePolylineElement(decoder, se)
	case "g":
		return parseGroupElement(decoder, ps, se, path)
	case "defs":
		// Definitions never render directly, but their gradients must be
		// registered and any nested supported elements parsed for traversal.
		if _, err := parseChildren(decoder, ps, path); err != nil {
			return nil, err
		}
		return nil, nil //nolint:nilnil // defs itself is not a render node
	case "linearGradient":
		return parseGradientElement(decoder, ps, se, path, LinearGradient)
	case "radialGradient":
		return parseGradientElement(decoder, ps, se, path, RadialGradient)
	default:
		// Skip unsupported elements (clipPath, use, text, filter, etc.).
		if err := decoder.Skip(); err != nil {
			return nil, fmt.Errorf("svg: error skipping <%s>: %w", se.Name.Local, err)
		}
		return nil, nil //nolint:nilnil // nil element means "skip this element"
	}
}

// parseGradientElement parses <linearGradient>/<radialGradient> and their
// <stop> children, registering the raw definition in ps. The gradient itself
// is not a render Element.
func parseGradientElement(decoder *xml.Decoder, ps *parseState, se xml.StartElement, path string, kind GradientKind) (Element, error) {
	def := &gradientDef{
		id:    attrLocal(se, "id"),
		path:  path,
		kind:  kind,
		attrs: make(map[string]string, len(se.Attr)),
	}
	for _, a := range se.Attr {
		key := a.Name.Local
		def.attrs[key] = a.Value
		// Expose namespaced xlink:href under both keys for inheritance.
		if key == "href" && a.Name.Space != "" {
			def.attrs["xlink:href"] = a.Value
		}
	}

	stopIndex := 0
	for {
		tok, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("svg: error reading <%s>: %w", kind, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "stop" {
				stopIndex++
				sp := fmt.Sprintf("%s/stop[%d]", path, stopIndex)
				sd := gradientStopDef{path: sp}
				style := ""
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "offset":
						sd.offsetRaw = a.Value
					case "stop-color":
						sd.colorRaw = a.Value
					case "stop-opacity":
						sd.opacityRaw = a.Value
					case "style":
						style = a.Value
					}
				}
				// Inline style wins over presentation attributes.
				if style != "" {
					decls := parseStyleDeclarations(style)
					if v, ok := decls["stop-color"]; ok {
						sd.colorRaw = v
					}
					if v, ok := decls["stop-opacity"]; ok {
						sd.opacityRaw = v
					}
					if v, ok := decls["offset"]; ok {
						sd.offsetRaw = v
					}
				}
				def.stops = append(def.stops, sd)
			}
			// Consume the whole child element (stop is empty in practice;
			// other children are ignored).
			if err := decoder.Skip(); err != nil {
				return nil, fmt.Errorf("svg: error reading child of <%s>: %w", kind, err)
			}
		case xml.EndElement:
			ps.defs = append(ps.defs, def)
			return nil, nil //nolint:nilnil // paint servers are not render nodes
		}
	}
}

// parseStyleDeclarations parses a minimal inline CSS declaration list
// ("fill: url(#g); fill-opacity: 0.5") into a lower-cased property map.
func parseStyleDeclarations(style string) map[string]string {
	out := make(map[string]string)
	for _, decl := range strings.Split(style, ";") {
		kv := strings.SplitN(decl, ":", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(kv[0]))
		val := strings.TrimSpace(kv[1])
		if key != "" {
			out[key] = val
		}
	}
	return out
}

// parseAttrs extracts common SVG presentation attributes from an element.
func parseAttrs(se xml.StartElement) Attrs {
	a := Attrs{
		FillOpacity:   1.0,
		StrokeOpacity: 1.0,
		StrokeWidth:   1.0,
		Opacity:       1.0,
	}
	var style string
	for _, attr := range se.Attr {
		switch attr.Name.Local {
		case "fill":
			a.Fill = attr.Value
		case "fill-rule":
			a.FillRule = attr.Value
		case "fill-opacity":
			if v, err := strconv.ParseFloat(attr.Value, 64); err == nil {
				a.FillOpacity = v
			}
		case "stroke":
			a.Stroke = attr.Value
		case "stroke-width":
			if v, err := strconv.ParseFloat(attr.Value, 64); err == nil {
				a.StrokeWidth = v
			}
		case "stroke-linecap":
			a.StrokeCap = attr.Value
		case "stroke-linejoin":
			a.StrokeJoin = attr.Value
		case "stroke-opacity":
			if v, err := strconv.ParseFloat(attr.Value, 64); err == nil {
				a.StrokeOpacity = v
			}
		case "opacity":
			if v, err := strconv.ParseFloat(attr.Value, 64); err == nil {
				a.Opacity = v
			}
		case "transform":
			a.Transform = attr.Value
		case "clip-rule":
			a.ClipRule = attr.Value
		case "style":
			style = attr.Value
		}
	}

	// Inline style declarations override presentation attributes (CSS rule).
	if style != "" {
		decls := parseStyleDeclarations(style)
		if v, ok := decls["fill"]; ok {
			a.Fill = v
		}
		if v, ok := decls["stroke"]; ok {
			a.Stroke = v
		}
		if v, ok := decls["fill-opacity"]; ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				a.FillOpacity = f
			}
		}
		if v, ok := decls["stroke-opacity"]; ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				a.StrokeOpacity = f
			}
		}
		if v, ok := decls["opacity"]; ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				a.Opacity = f
			}
		}
		if v, ok := decls["stroke-width"]; ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				a.StrokeWidth = f
			}
		}
	}
	return a
}

// parsePathElement parses a <path> element.
func parsePathElement(decoder *xml.Decoder, se xml.StartElement) (*PathElement, error) {
	elem := &PathElement{Attrs: parseAttrs(se)}
	for _, attr := range se.Attr {
		if attr.Name.Local == "d" {
			elem.D = attr.Value
		}
	}
	if err := decoder.Skip(); err != nil {
		return nil, fmt.Errorf("svg: error reading <path>: %w", err)
	}
	return elem, nil
}

// parseCircleElement parses a <circle> element.
func parseCircleElement(decoder *xml.Decoder, se xml.StartElement) (*CircleElement, error) {
	elem := &CircleElement{Attrs: parseAttrs(se)}
	for _, attr := range se.Attr {
		switch attr.Name.Local {
		case "cx":
			elem.CX, _ = strconv.ParseFloat(attr.Value, 64)
		case "cy":
			elem.CY, _ = strconv.ParseFloat(attr.Value, 64)
		case "r":
			elem.R, _ = strconv.ParseFloat(attr.Value, 64)
		}
	}
	if err := decoder.Skip(); err != nil {
		return nil, fmt.Errorf("svg: error reading <circle>: %w", err)
	}
	return elem, nil
}

// parseRectElement parses a <rect> element.
func parseRectElement(decoder *xml.Decoder, se xml.StartElement) (*RectElement, error) {
	elem := &RectElement{Attrs: parseAttrs(se)}
	for _, attr := range se.Attr {
		switch attr.Name.Local {
		case "x":
			elem.X, _ = strconv.ParseFloat(attr.Value, 64)
		case "y":
			elem.Y, _ = strconv.ParseFloat(attr.Value, 64)
		case "width":
			elem.W, _ = strconv.ParseFloat(attr.Value, 64)
		case "height":
			elem.H, _ = strconv.ParseFloat(attr.Value, 64)
		case "rx":
			elem.RX, _ = strconv.ParseFloat(attr.Value, 64)
		case "ry":
			elem.RY, _ = strconv.ParseFloat(attr.Value, 64)
		}
	}
	if err := decoder.Skip(); err != nil {
		return nil, fmt.Errorf("svg: error reading <rect>: %w", err)
	}
	return elem, nil
}

// parseEllipseElement parses an <ellipse> element.
func parseEllipseElement(decoder *xml.Decoder, se xml.StartElement) (*EllipseElement, error) {
	elem := &EllipseElement{Attrs: parseAttrs(se)}
	for _, attr := range se.Attr {
		switch attr.Name.Local {
		case "cx":
			elem.CX, _ = strconv.ParseFloat(attr.Value, 64)
		case "cy":
			elem.CY, _ = strconv.ParseFloat(attr.Value, 64)
		case "rx":
			elem.RX, _ = strconv.ParseFloat(attr.Value, 64)
		case "ry":
			elem.RY, _ = strconv.ParseFloat(attr.Value, 64)
		}
	}
	if err := decoder.Skip(); err != nil {
		return nil, fmt.Errorf("svg: error reading <ellipse>: %w", err)
	}
	return elem, nil
}

// parseLineElement parses a <line> element.
func parseLineElement(decoder *xml.Decoder, se xml.StartElement) (*LineElement, error) {
	elem := &LineElement{Attrs: parseAttrs(se)}
	for _, attr := range se.Attr {
		switch attr.Name.Local {
		case "x1":
			elem.X1, _ = strconv.ParseFloat(attr.Value, 64)
		case "y1":
			elem.Y1, _ = strconv.ParseFloat(attr.Value, 64)
		case "x2":
			elem.X2, _ = strconv.ParseFloat(attr.Value, 64)
		case "y2":
			elem.Y2, _ = strconv.ParseFloat(attr.Value, 64)
		}
	}
	if err := decoder.Skip(); err != nil {
		return nil, fmt.Errorf("svg: error reading <line>: %w", err)
	}
	return elem, nil
}

// parsePolygonElement parses a <polygon> element.
func parsePolygonElement(decoder *xml.Decoder, se xml.StartElement) (*PolygonElement, error) {
	elem := &PolygonElement{Attrs: parseAttrs(se)}
	for _, attr := range se.Attr {
		if attr.Name.Local == "points" {
			pts, err := parsePoints(attr.Value)
			if err != nil {
				return nil, fmt.Errorf("svg: <polygon> points: %w", err)
			}
			elem.Points = pts
		}
	}
	if err := decoder.Skip(); err != nil {
		return nil, fmt.Errorf("svg: error reading <polygon>: %w", err)
	}
	return elem, nil
}

// parsePolylineElement parses a <polyline> element.
func parsePolylineElement(decoder *xml.Decoder, se xml.StartElement) (*PolylineElement, error) {
	elem := &PolylineElement{Attrs: parseAttrs(se)}
	for _, attr := range se.Attr {
		if attr.Name.Local == "points" {
			pts, err := parsePoints(attr.Value)
			if err != nil {
				return nil, fmt.Errorf("svg: <polyline> points: %w", err)
			}
			elem.Points = pts
		}
	}
	if err := decoder.Skip(); err != nil {
		return nil, fmt.Errorf("svg: error reading <polyline>: %w", err)
	}
	return elem, nil
}

// parseGroupElement parses a <g> element and its children recursively.
func parseGroupElement(decoder *xml.Decoder, ps *parseState, se xml.StartElement, path string) (*GroupElement, error) {
	elem := &GroupElement{Attrs: parseAttrs(se)}
	children, err := parseChildren(decoder, ps, path)
	if err != nil {
		return nil, err
	}
	elem.Children = children
	return elem, nil
}

// parsePoints parses an SVG points attribute value.
// Supports both "x1,y1 x2,y2" and "x1 y1 x2 y2" formats.
func parsePoints(s string) ([]float64, error) {
	s = strings.ReplaceAll(s, ",", " ")
	parts := strings.Fields(s)
	if len(parts)%2 != 0 {
		return nil, fmt.Errorf("odd number of coordinate values (%d)", len(parts))
	}
	points := make([]float64, len(parts))
	for i, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid coordinate %q: %w", p, err)
		}
		points[i] = v
	}
	return points, nil
}

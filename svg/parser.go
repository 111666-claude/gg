package svg

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/gogpu/gg"
)

// MaxSVGDepth bounds how deeply nested SVG/XML elements may be before Parse
// rejects the document. Real icons rarely nest more than a few dozen levels;
// the limit exists so adversarial documents cannot exhaust the goroutine
// stack through recursive group/clip parsing.
const MaxSVGDepth = 256

// maxClipRefDepth bounds a non-circular chain of clipPath-to-clipPath
// references as a second nesting guard.
const maxClipRefDepth = 256

// Parse parses SVG XML data into a reusable [Document].
// The Document can be rendered multiple times at different sizes.
//
// In addition to the icon subset supported by the renderer, Parse builds the
// id-keyed [ClipPath] table from <defs> (and clipPath elements anywhere in
// the document) and validates all clip-path references. Missing ids,
// circular references, illegal geometry, duplicate ids, overly deep nesting
// and malformed XML result in errors that carry the offending element path.
// Clip problems are never silently downgraded to "no clipping".
//
// Unsupported non-clip elements (for example <text>) are still silently
// skipped, and unreferenced definitions never change [Document.Elements]
// ordering.
func Parse(data []byte) (*Document, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))

	doc := &Document{
		ViewBox:   ViewBox{Width: 16, Height: 16}, // sensible default
		ClipPaths: map[string]*ClipPath{},
	}

	p := &svgParser{
		dec:   decoder,
		doc:   doc,
		clips: doc.ClipPaths,
		ids:   make(map[string]idRecord),
	}

	// Find the root <svg> element.
	for {
		tok, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("svg: failed to find <svg> root element: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "svg" {
			continue
		}
		p.frames = []pathFrame{{tag: "svg", counts: map[string]int{}}}
		if id := attrValue(se, "id"); id != "" {
			p.frames[0].id = id
			if err := p.registerID(id); err != nil {
				return nil, err
			}
		}
		if err := parseSVGRoot(doc, se); err != nil {
			return nil, err
		}
		// Parse children.
		children, err := p.parseChildren()
		if err != nil {
			return nil, err
		}
		doc.Elements = children
		if err := p.validateClipReferences(); err != nil {
			return nil, err
		}
		return doc, nil
	}
}

// idRecord stores where an id was first declared.
type idRecord struct {
	path string
}

// refRecord is one clip-path reference found on an element.
type refRecord struct {
	raw  string
	path string
}

// pathFrame is one level of the element-path stack used in parse errors.
type pathFrame struct {
	tag    string
	id     string
	index  int // 1-based index among same-tag siblings
	counts map[string]int
}

type svgParser struct {
	dec       *xml.Decoder
	doc       *Document
	clips     map[string]*ClipPath
	ids       map[string]idRecord
	refs      []refRecord
	frames    []pathFrame
	clipDepth int // >0 while parsing clipPath geometry
}

// elementPath renders the current element path, e.g.
// "svg/defs/clipPath#rounded/rect[1]".
func (p *svgParser) elementPath() string {
	var b strings.Builder
	for i, f := range p.frames {
		if i > 0 {
			b.WriteByte('/')
		}
		b.WriteString(f.tag)
		switch {
		case f.id != "":
			fmt.Fprintf(&b, "#%s", f.id)
		case f.index > 0:
			fmt.Fprintf(&b, "[%d]", f.index)
		}
	}
	return b.String()
}

// errorf builds a parse error carrying the current element path.
func (p *svgParser) errorf(format string, args ...any) error {
	return &ParseError{Path: p.elementPath(), Detail: fmt.Sprintf(format, args...)}
}

// ParseError is returned for SVG documents that violate the strict parsing
// rules. Path is the slash-separated element path of the offending node.
type ParseError struct {
	// Path is the best-known element path, e.g.
	// "svg/defs/clipPath#c/rect[2]".
	Path string
	// Detail is the human-readable reason.
	Detail string
}

func (e *ParseError) Error() string {
	if e.Path == "" {
		return "svg: " + e.Detail
	}
	return fmt.Sprintf("svg: at %s: %s", e.Path, e.Detail)
}

// registerID records an id and rejects duplicates.
func (p *svgParser) registerID(id string) error {
	if id == "" {
		return nil
	}
	path := p.elementPath()
	if first, ok := p.ids[id]; ok {
		return p.errorf("duplicate id %q (already defined at %s)", id, first.path)
	}
	p.ids[id] = idRecord{path: path}
	return nil
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
func (p *svgParser) parseChildren() ([]Element, error) {
	var elements []Element

	for {
		tok, err := p.dec.Token()
		if err != nil {
			return nil, fmt.Errorf("svg: error reading XML near %s: %w", p.elementPath(), err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			tag := t.Name.Local

			// Depth guard before recursing.
			if len(p.frames)+1 > MaxSVGDepth {
				return nil, p.depthError(tag)
			}

			parent := &p.frames[len(p.frames)-1]
			if parent.counts == nil {
				parent.counts = map[string]int{}
			}
			parent.counts[tag]++
			frame := pathFrame{
				tag:    tag,
				index:  parent.counts[tag],
				id:     attrValue(t, "id"),
				counts: map[string]int{},
			}
			p.frames = append(p.frames, frame)

			elem, perr := p.parseElement(t)
			if perr != nil {
				return nil, perr
			}
			if elem != nil {
				if err := p.trackElement(elem); err != nil {
					return nil, err
				}
				elements = append(elements, elem)
			}

			p.frames = p.frames[:len(p.frames)-1]

		case xml.EndElement:
			return elements, nil
		}
	}
}

// trackElement records parse-time metadata for a parsed render element:
// its element path, duplicate-id registration and clip-path references.
func (p *svgParser) trackElement(elem Element) error {
	a := elem.attrs()
	a.elementPath = p.elementPath()
	if a.ID != "" {
		if err := p.registerID(a.ID); err != nil {
			return err
		}
	}
	if ref := strings.TrimSpace(a.ClipPathRef); ref != "" && !strings.EqualFold(ref, "none") {
		p.refs = append(p.refs, refRecord{raw: ref, path: a.elementPath})
	}
	return nil
}

// depthError reports an over-deep document with the would-be element path.
func (p *svgParser) depthError(nextTag string) error {
	path := p.elementPath() + "/" + nextTag
	return &ParseError{
		Path:   path,
		Detail: fmt.Sprintf("element nesting depth exceeds the maximum of %d (too deeply nested)", MaxSVGDepth),
	}
}

// parseElement parses a single SVG element from a start element.
// Returns nil for unsupported/definition elements (silently skipped from the
// render tree).
func (p *svgParser) parseElement(se xml.StartElement) (Element, error) {
	switch se.Name.Local {
	case "defs":
		return p.parseDefs(se)
	case "clipPath":
		return p.parseClipPath(se)
	case "path":
		return p.parsePathElement(se)
	case "circle":
		return p.parseCircleElement(se)
	case "rect":
		return p.parseRectElement(se)
	case "ellipse":
		return p.parseEllipseElement(se)
	case "line":
		return p.parseLineElement(se)
	case "polygon":
		return p.parsePolygonElement(se)
	case "polyline":
		return p.parsePolylineElement(se)
	case "g":
		return p.parseGroupElement(se)
	default:
		// Skip unsupported elements (use, text, etc.).
		if err := p.dec.Skip(); err != nil {
			return nil, p.errorf("error skipping <%s>: %v", se.Name.Local, err)
		}
		return nil, nil //nolint:nilnil // nil element means "skip this element"
	}
}

// parseDefs parses a <defs> container. Its children are validated and
// registered (clipPath definitions, ids) but never enter the render tree.
func (p *svgParser) parseDefs(_ xml.StartElement) (Element, error) {
	if _, err := p.parseChildren(); err != nil {
		return nil, err
	}
	return nil, nil //nolint:nilnil // definitions are not rendered
}

// parseClipPath parses a reusable <clipPath> definition. clipPath elements
// may appear inside <defs> or anywhere else in the document.
func (p *svgParser) parseClipPath(se xml.StartElement) (Element, error) {
	attrs := parseAttrs(se)
	id := attrs.ID
	if id == "" {
		return nil, p.errorf("clipPath definition is missing required id attribute")
	}
	// Reflect the id in the current path frame for child error paths.
	p.frames[len(p.frames)-1].id = id
	if err := p.registerID(id); err != nil {
		return nil, err
	}

	units := ""
	for _, a := range se.Attr {
		if a.Name.Local == "clipPathUnits" {
			units = strings.TrimSpace(a.Value)
		}
	}
	switch units {
	case "", "userSpaceOnUse":
		units = "userSpaceOnUse"
	case "objectBoundingBox":
		// supported
	default:
		// Lenient fallback matching the SVG initial value; an unsupported
		// unit token must not silently disable clipping either.
		units = "userSpaceOnUse"
	}

	p.clipDepth++
	children, err := p.parseChildren()
	p.clipDepth--
	if err != nil {
		return nil, err
	}

	cp := &ClipPath{
		Attrs:       attrs,
		ID:          id,
		Units:       units,
		Children:    children,
		ElementPath: p.elementPath(),
	}
	p.clips[id] = cp

	if ref := strings.TrimSpace(attrs.ClipPathRef); ref != "" && !strings.EqualFold(ref, "none") {
		p.refs = append(p.refs, refRecord{raw: ref, path: cp.ElementPath})
	}
	return nil, nil //nolint:nilnil // definitions never enter the render element tree
}

// attrValue returns the first attribute with the given local name.
func attrValue(se xml.StartElement, local string) string {
	for _, a := range se.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// attrFloat parses a float presentation attribute, falling back to def when
// the value is malformed.
func attrFloat(value string, def float64) float64 {
	if v, err := strconv.ParseFloat(value, 64); err == nil {
		return v
	}
	return def
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
		case "id":
			a.ID = attr.Value
		case "fill":
			a.Fill = attr.Value
		case "fill-rule":
			a.FillRule = attr.Value
		case "fill-opacity":
			a.FillOpacity = attrFloat(attr.Value, a.FillOpacity)
		case "stroke":
			a.Stroke = attr.Value
		case "stroke-width":
			a.StrokeWidth = attrFloat(attr.Value, a.StrokeWidth)
		case "stroke-linecap":
			a.StrokeCap = attr.Value
		case "stroke-linejoin":
			a.StrokeJoin = attr.Value
		case "stroke-opacity":
			a.StrokeOpacity = attrFloat(attr.Value, a.StrokeOpacity)
		case "opacity":
			a.Opacity = attrFloat(attr.Value, a.Opacity)
		case "transform":
			a.Transform = attr.Value
		case "clip-rule":
			a.ClipRule = attr.Value
		case "clip-path":
			a.ClipPathRef = attr.Value
		case "style":
			style = attr.Value
		}
	}
	// Minimal CSS-style fallback: style="clip-path:url(#x);clip-rule:evenodd".
	if style != "" {
		applyStyleDeclarations(&a, style)
	}
	return a
}

// applyStyleDeclarations extracts the small set of presentation properties
// supported from an inline style attribute. Explicit XML attributes win.
func applyStyleDeclarations(a *Attrs, style string) {
	for _, decl := range strings.Split(style, ";") {
		kv := strings.SplitN(decl, ":", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])
		switch key {
		case "clip-path":
			if a.ClipPathRef == "" {
				a.ClipPathRef = val
			}
		case "clip-rule":
			if a.ClipRule == "" {
				a.ClipRule = val
			}
		case "fill-rule":
			if a.FillRule == "" {
				a.FillRule = val
			}
		}
	}
}

// parseGeomNumber parses a geometry number, additionally accepting common
// SVG unit suffixes ("12px", "50%", "2em", ...).
func parseGeomNumber(raw string) (float64, bool) {
	if v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil {
		return v, true
	}
	if v, err := parseDimension(raw); err == nil {
		return v, true
	}
	return 0, false
}

// setGeomAttr parses a numeric geometry attribute into dst. Missing
// attributes leave *dst unchanged. When strict is true (geometry inside a
// clipPath definition), malformed/non-finite/negative-extent values produce
// a path-aware [ParseError]; otherwise the historical best-effort parse
// behavior is preserved.
func setGeomAttr(se xml.StartElement, local, path string, dst *float64, nonNegative, strict bool) error {
	raw := attrValue(se, local)
	if raw == "" {
		return nil
	}
	v, ok := parseGeomNumber(raw)
	if !ok {
		if !strict {
			return nil
		}
		return &ParseError{Path: path, Detail: fmt.Sprintf("invalid geometry: attribute %q has bad number %q", local, raw)}
	}
	if strict {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return &ParseError{Path: path, Detail: fmt.Sprintf("invalid geometry: attribute %q must be finite, got %q", local, raw)}
		}
		if nonNegative && v < 0 {
			return &ParseError{Path: path, Detail: fmt.Sprintf("invalid geometry: attribute %q must not be negative, got %v", local, v)}
		}
	}
	*dst = v
	return nil
}

// parsePathElement parses a <path> element.
func (p *svgParser) parsePathElement(se xml.StartElement) (*PathElement, error) {
	elem := &PathElement{Attrs: parseAttrs(se)}
	elem.Attrs.elementPath = p.elementPath()
	for _, attr := range se.Attr {
		if attr.Name.Local == "d" {
			elem.D = attr.Value
		}
	}
	// Geometry inside clipPath definitions must be valid: a silently dropped
	// clip shape would change the clipping region. Validate d there.
	if p.clipDepth > 0 && strings.TrimSpace(elem.D) != "" {
		if _, err := gg.ParseSVGPath(elem.D); err != nil {
			return nil, p.errorf("invalid geometry: malformed path data %q: %v", truncateForError(elem.D, 80), err)
		}
	}
	if err := p.dec.Skip(); err != nil {
		return nil, p.errorf("error reading <path>: %v", err)
	}
	return elem, nil
}

func truncateForError(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// geomAttrSpec names one numeric geometry attribute and its destination.
type geomAttrSpec struct {
	name   string
	dst    *float64
	nonNeg bool
}

// applyGeomAttrs parses the named geometry attributes, enforcing strict
// validation while parsing clipPath content.
func (p *svgParser) applyGeomAttrs(se xml.StartElement, specs ...geomAttrSpec) error {
	path := p.elementPath()
	strict := p.clipDepth > 0
	for _, s := range specs {
		if err := setGeomAttr(se, s.name, path, s.dst, s.nonNeg, strict); err != nil {
			return err
		}
	}
	return nil
}

// parseCircleElement parses a <circle> element.
func (p *svgParser) parseCircleElement(se xml.StartElement) (*CircleElement, error) {
	elem := &CircleElement{Attrs: parseAttrs(se)}
	if err := p.applyGeomAttrs(se,
		geomAttrSpec{"cx", &elem.CX, false},
		geomAttrSpec{"cy", &elem.CY, false},
		geomAttrSpec{"r", &elem.R, true},
	); err != nil {
		return nil, err
	}
	if err := p.dec.Skip(); err != nil {
		return nil, p.errorf("error reading <circle>: %v", err)
	}
	return elem, nil
}

// parseRectElement parses a <rect> element.
func (p *svgParser) parseRectElement(se xml.StartElement) (*RectElement, error) {
	elem := &RectElement{Attrs: parseAttrs(se)}
	if err := p.applyGeomAttrs(se,
		geomAttrSpec{"x", &elem.X, false},
		geomAttrSpec{"y", &elem.Y, false},
		geomAttrSpec{"width", &elem.W, true},
		geomAttrSpec{"height", &elem.H, true},
		geomAttrSpec{"rx", &elem.RX, true},
		geomAttrSpec{"ry", &elem.RY, true},
	); err != nil {
		return nil, err
	}
	if err := p.dec.Skip(); err != nil {
		return nil, p.errorf("error reading <rect>: %v", err)
	}
	return elem, nil
}

// parseEllipseElement parses an <ellipse> element.
func (p *svgParser) parseEllipseElement(se xml.StartElement) (*EllipseElement, error) {
	elem := &EllipseElement{Attrs: parseAttrs(se)}
	if err := p.applyGeomAttrs(se,
		geomAttrSpec{"cx", &elem.CX, false},
		geomAttrSpec{"cy", &elem.CY, false},
		geomAttrSpec{"rx", &elem.RX, true},
		geomAttrSpec{"ry", &elem.RY, true},
	); err != nil {
		return nil, err
	}
	if err := p.dec.Skip(); err != nil {
		return nil, p.errorf("error reading <ellipse>: %v", err)
	}
	return elem, nil
}

// parseLineElement parses a <line> element.
func (p *svgParser) parseLineElement(se xml.StartElement) (*LineElement, error) {
	elem := &LineElement{Attrs: parseAttrs(se)}
	if err := p.applyGeomAttrs(se,
		geomAttrSpec{"x1", &elem.X1, false},
		geomAttrSpec{"y1", &elem.Y1, false},
		geomAttrSpec{"x2", &elem.X2, false},
		geomAttrSpec{"y2", &elem.Y2, false},
	); err != nil {
		return nil, err
	}
	if err := p.dec.Skip(); err != nil {
		return nil, p.errorf("error reading <line>: %v", err)
	}
	return elem, nil
}

// parsePolygonElement parses a <polygon> element.
func (p *svgParser) parsePolygonElement(se xml.StartElement) (*PolygonElement, error) {
	elem := &PolygonElement{Attrs: parseAttrs(se)}
	for _, attr := range se.Attr {
		if attr.Name.Local == "points" {
			pts, err := parsePoints(attr.Value)
			if err != nil {
				return nil, p.errorf("invalid <polygon> points: %v", err)
			}
			elem.Points = pts
		}
	}
	if err := p.dec.Skip(); err != nil {
		return nil, p.errorf("error reading <polygon>: %v", err)
	}
	return elem, nil
}

// parsePolylineElement parses a <polyline> element.
func (p *svgParser) parsePolylineElement(se xml.StartElement) (*PolylineElement, error) {
	elem := &PolylineElement{Attrs: parseAttrs(se)}
	for _, attr := range se.Attr {
		if attr.Name.Local == "points" {
			pts, err := parsePoints(attr.Value)
			if err != nil {
				return nil, p.errorf("invalid <polyline> points: %v", err)
			}
			elem.Points = pts
		}
	}
	if err := p.dec.Skip(); err != nil {
		return nil, p.errorf("error reading <polyline>: %v", err)
	}
	return elem, nil
}

// parseGroupElement parses a <g> element and its children recursively.
func (p *svgParser) parseGroupElement(se xml.StartElement) (*GroupElement, error) {
	elem := &GroupElement{Attrs: parseAttrs(se)}
	children, err := p.parseChildren()
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
	for i, pt := range parts {
		v, err := strconv.ParseFloat(pt, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid coordinate %q: %w", pt, err)
		}
		points[i] = v
	}
	return points, nil
}

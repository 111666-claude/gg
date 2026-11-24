package svg

// Document represents a parsed SVG document.
// It can be rendered multiple times at different sizes, making it suitable
// for caching parsed SVG icons.
type Document struct {
	// ViewBox defines the SVG coordinate system (minX, minY, width, height).
	ViewBox ViewBox

	// Width and Height are the explicit width/height from the SVG root element.
	// If zero, ViewBox dimensions are used.
	Width, Height float64

	// RootFill is the fill attribute from the <svg> root element.
	// In SVG, presentation attributes on <svg> are inherited by children.
	// Common in expui icons: fill="none" on root.
	RootFill string

	// Elements contains the top-level SVG elements.
	Elements []Element

	// ClipPaths contains the reusable <clipPath> definitions keyed by their
	// id attribute. The definitions are fully parsed and validated by Parse
	// and must be treated as read-only; the same Document can be rendered at
	// arbitrary sizes. ClipPaths never participate in Elements ordering, so
	// unreferenced definitions leave the rendered element order unchanged.
	ClipPaths map[string]*ClipPath
}

// ClipPath is a parsed, reusable SVG <clipPath> definition.
//
// Children holds the clip geometry (path, rect, circle, ellipse, line,
// polygon, polyline and nested groups). Geometry is interpreted according to
// Units and the definition's own Attrs.Transform. A clipPath may itself
// reference another clipPath through Attrs.ClipPathRef; the two regions then
// intersect.
type ClipPath struct {
	// Attrs carries the clipPath element's presentation attributes,
	// including Transform, ClipRule and ClipPathRef.
	Attrs Attrs

	// ID is the clipPath element's id attribute (map key).
	ID string

	// Units is the parsed clipPathUnits value:
	// "userSpaceOnUse" (default) or "objectBoundingBox".
	Units string

	// Children are the parsed geometry/group children.
	Children []Element

	// ElementPath is the slash-separated document path of the definition,
	// for example "svg/defs/clipPath#rounded". Used in parse errors.
	ElementPath string
}

// ClipPathElement is an alias for [ClipPath], matching the *Element naming
// convention used by the other parsed SVG nodes.
type ClipPathElement = ClipPath

// LookupClipPath returns the read-only clipPath definition for id.
func (d *Document) LookupClipPath(id string) (*ClipPath, bool) {
	if d == nil || d.ClipPaths == nil {
		return nil, false
	}
	cp, ok := d.ClipPaths[id]
	return cp, ok
}

// ViewBox represents the SVG viewBox attribute.
type ViewBox struct {
	MinX, MinY, Width, Height float64
}

// Attrs holds common SVG presentation attributes shared by all elements.
type Attrs struct {
	ID            string  // id attribute, used for clip-path references
	Fill          string  // "#hex", "none", "rgb(...)", named color, ""
	FillRule      string  // "evenodd", "nonzero", ""
	FillOpacity   float64 // 0-1, default 1
	Stroke        string  // "#hex", "none", "rgb(...)", named color, ""
	StrokeWidth   float64 // default 1
	StrokeCap     string  // "round", "square", "butt", ""
	StrokeJoin    string  // "round", "bevel", "miter", ""
	StrokeOpacity float64 // 0-1, default 1
	Opacity       float64 // element-level opacity, default 1
	Transform     string  // raw transform string
	ClipRule      string  // "evenodd", "nonzero", ""
	ClipPathRef   string  // raw clip-path attribute value, e.g. "url(#id)"

	// elementPath is the parse-time document path used in validation errors.
	elementPath string
}

// Element is the interface implemented by all SVG element types.
type Element interface {
	// attrs returns the common presentation attributes for this element.
	attrs() *Attrs
}

// PathElement represents an SVG <path> element.
type PathElement struct {
	Attrs Attrs
	D     string // SVG path data string
}

func (e *PathElement) attrs() *Attrs { return &e.Attrs }

// CircleElement represents an SVG <circle> element.
type CircleElement struct {
	Attrs  Attrs
	CX, CY float64
	R      float64
}

func (e *CircleElement) attrs() *Attrs { return &e.Attrs }

// RectElement represents an SVG <rect> element.
type RectElement struct {
	Attrs      Attrs
	X, Y, W, H float64
	RX, RY     float64 // corner radii
}

func (e *RectElement) attrs() *Attrs { return &e.Attrs }

// EllipseElement represents an SVG <ellipse> element.
type EllipseElement struct {
	Attrs  Attrs
	CX, CY float64
	RX, RY float64
}

func (e *EllipseElement) attrs() *Attrs { return &e.Attrs }

// LineElement represents an SVG <line> element.
type LineElement struct {
	Attrs          Attrs
	X1, Y1, X2, Y2 float64
}

func (e *LineElement) attrs() *Attrs { return &e.Attrs }

// PolygonElement represents an SVG <polygon> element.
type PolygonElement struct {
	Attrs  Attrs
	Points []float64 // alternating x, y values
}

func (e *PolygonElement) attrs() *Attrs { return &e.Attrs }

// PolylineElement represents an SVG <polyline> element.
type PolylineElement struct {
	Attrs  Attrs
	Points []float64 // alternating x, y values
}

func (e *PolylineElement) attrs() *Attrs { return &e.Attrs }

// GroupElement represents an SVG <g> element with children.
type GroupElement struct {
	Attrs    Attrs
	Children []Element
}

func (e *GroupElement) attrs() *Attrs { return &e.Attrs }

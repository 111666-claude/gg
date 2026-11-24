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

	// defs maps every element carrying an id attribute to that id. Elements
	// inside <defs> never appear in Elements but remain reachable through this
	// registry, which <use> elements resolve against after parsing.
	defs map[string]Element
}

// ViewBox represents the SVG viewBox attribute.
type ViewBox struct {
	MinX, MinY, Width, Height float64
}

// Attrs holds common SVG presentation attributes shared by all elements.
type Attrs struct {
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

// SymbolElement represents an SVG <symbol> (or nested <svg>) element. Symbols
// are never rendered directly: they define reusable viewport-scoped content
// instantiated through <use>. When referenced, ViewBox is mapped onto the
// instance viewport according to PreserveAspectRatio.
type SymbolElement struct {
	Attrs Attrs

	// ID is the symbol's document-unique identifier (may be empty for
	// unreferenced symbols).
	ID string

	// ViewBox is the symbol's own coordinate system. A zero Width or Height
	// means the symbol has no viewBox and its children use the instance
	// coordinate system directly.
	ViewBox ViewBox

	// PreserveAspectRatio holds the raw preserveAspectRatio attribute,
	// defaulting to "xMidYMid meet" when empty.
	PreserveAspectRatio string

	Children []Element
}

func (e *SymbolElement) attrs() *Attrs { return &e.Attrs }

// UseElement represents an SVG <use> element that instantiates a referenced
// element by document order. Ref is resolved during Parse and points at the
// referenced element in the same document; the element itself is never
// mutated, so each instance keeps independent style inheritance and geometry.
type UseElement struct {
	Attrs Attrs

	// HRef is the raw reference (typically "#id").
	HRef string

	// X, Y place the instance; width/height size the viewport for symbol and
	// nested-svg references and are ignored for other targets.
	X, Y          float64
	Width, Height float64

	// hasWidth/hasHeight distinguish an omitted attribute from an explicit zero.
	hasWidth, hasHeight bool

	// Ref is the post-parse resolution of HRef.
	Ref Element

	pos SourcePos
}

func (e *UseElement) attrs() *Attrs { return &e.Attrs }

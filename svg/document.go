package svg

import "github.com/gogpu/gg/text"

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

	// RootAttrs contains all presentation/style attributes on the root <svg>.
	RootAttrs Attrs

	// Elements contains the top-level SVG elements.
	Elements []Element

	// Font is an optional face used when an SVG text element does not name a
	// resolvable family. Source-backed faces are automatically resized to each
	// element's font-size. MultiFace values are used through gg's fallback
	// renderer when shaped source runs are unavailable.
	Font text.Face

	// FontFace is an alias for Font for callers that prefer the explicit name.
	FontFace text.Face

	// TextFont is an additional alias used by some text rendering APIs.
	TextFont text.Face

	// FontResolver, when non-nil, resolves font-family names. If it is nil the
	// package's best-effort system resolver is used.
	FontResolver FontResolver

	// FallbackFonts are tried after the family resolver and before/alongside
	// system fallback fonts.
	FallbackFonts []text.Face

	// NamedFonts maps a normalized font-family name to a face. It offers a
	// simple way to supply external fonts without implementing FontResolver.
	NamedFonts map[string]text.Face

	// TextOptions configures bounds and diagnostics for <text>/<tspan>.
	TextOptions TextOptions

	// Warnings contains non-fatal parse/render diagnostics collected when
	// TextOptions.RecordWarnings is enabled.
	Warnings []error
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

	// Font and text presentation properties. Raw values are retained so
	// relative sizes (em/%) and inherited weight/style can be resolved while
	// traversing the document.
	FontFamily      string
	FontSize        float64
	FontSizeRaw     string
	FontSizeSet     bool
	FontWeight      string
	FontWeightValue int
	FontWeightSet   bool
	FontStyle       string
	TextAnchor      string
	Direction       string
	Style           string
	XMLSpace        string
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

// TextElement represents an SVG <text> element.
//
// Content contains all decoded character data below the element, including
// text nested in tspans. DirectContent contains only character data that is a
// direct child of <text>. TSpans contains explicit <tspan> children. Children
// preserves document order and may contain *TextRun and *TSpanElement values.
type TextElement struct {
	Attrs

	X  float64
	Y  float64
	DX float64
	DY float64

	XValues  []float64
	YValues  []float64
	DXValues []float64
	DYValues []float64

	XSet  bool
	YSet  bool
	DXSet bool
	DYSet bool

	Content       string
	Text          string // alias for Content
	Value         string // alias for Content
	DirectContent string
	TSpans        []*TSpanElement
	Spans         []TSpanElement // value alias for callers expecting values
	Children      []Element
	ParseErrors   []error
}

func (e *TextElement) attrs() *Attrs { return &e.Attrs }

// TSpanElement represents an SVG <tspan> element.
type TSpanElement struct {
	Attrs

	X  float64
	Y  float64
	DX float64
	DY float64

	XValues  []float64
	YValues  []float64
	DXValues []float64
	DYValues []float64

	XSet  bool
	YSet  bool
	DXSet bool
	DYSet bool

	Content  string
	Text     string // alias for Content
	Value    string // alias for Content
	TSpans   []*TSpanElement
	Spans    []TSpanElement // value alias for callers expecting values
	Children []Element
}

func (e *TSpanElement) attrs() *Attrs { return &e.Attrs }

// TSpan is an alias for TSpanElement.
type TSpan = TSpanElement

// Text is an alias for TextElement for callers that prefer the short name.
type Text = TextElement

// SVGTextElement is an alternate descriptive alias.
type SVGTextElement = TextElement

// TextNode is an alias for decoded character runs.
type TextNode = TextRun

// TextRun is decoded character data in its original document position.
type TextRun struct {
	Attrs
	Content string
	Text    string
	Value   string
}

func (r *TextRun) attrs() *Attrs { return &r.Attrs }

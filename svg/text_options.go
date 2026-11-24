package svg

import (
	"errors"
	"fmt"
	"image"
	"strings"
	"sync"

	"github.com/gogpu/gg/text"
)

// Default limits for SVG text. They are intentionally conservative: SVG icons
// use short labels, while unbounded strings could make shaping/rendering O(n)
// vector work unexpectedly expensive.
const (
	// DefaultMaxTextBytes is the default UTF-8 byte budget for one <text>.
	DefaultMaxTextBytes = 65535
	// DefaultMaxTextRunes is the default rune budget for one <text>. SVG
	// labels are short; this keeps pathological documents bounded.
	DefaultMaxTextRunes = 4096
)

// Text diagnostics and fallback errors. Callers can use errors.Is to detect
// unavailable fonts, missing glyphs, invalid coordinates and oversized text.
var (
	ErrNoFont            = errors.New("svg: no usable font for text")
	ErrExternalFont      = errors.New("svg: external font unavailable")
	ErrMissingGlyph      = errors.New("svg: font is missing one or more glyphs")
	ErrInvalidCoordinate = errors.New("svg: invalid text coordinate")
	ErrTextTooLong       = errors.New("svg: text exceeds configured length limit")
)

// TextOptions controls optional text safety and diagnostics behavior.
type TextOptions struct {
	// MaxBytes limits the UTF-8 length of one <text> element. Zero means
	// DefaultMaxTextBytes; a negative value disables the byte limit.
	MaxBytes int

	// MaxRunes limits the rune count of one <text> element. Zero means
	// DefaultMaxTextRunes; a negative value disables the rune limit.
	MaxRunes int

	// DisableSystemFonts prevents platform font discovery. Explicit fonts and
	// FontResolver values are still used.
	DisableSystemFonts bool

	// StrictCoordinates makes Parse return invalid text coordinate errors
	// instead of falling back to the valid prefix/zero.
	StrictCoordinates bool

	// RecordWarnings appends non-fatal text diagnostics to Document.Warnings.
	RecordWarnings bool

	// ErrorHandler receives non-fatal fallback diagnostics during rendering.
	ErrorHandler func(error)
}

// FontQuery is passed to FontResolver implementations.
type FontQuery struct {
	// Family is the first requested family.
	Family string
	// Families is the parsed CSS font-family preference list.
	Families []string
	Weight   int
	Italic   bool
	Size     float64
	Language string
}

// FontResolver resolves an SVG font-family declaration to one or more faces.
// Returning multiple faces enables script/glyph fallback while retaining the
// ability to shape each source-backed run independently.
type FontResolver interface {
	ResolveFonts(query FontQuery) []text.Face
}

// FontResolverFunc adapts a function to FontResolver.
type FontResolverFunc func(query FontQuery) []text.Face

// ResolveFonts implements FontResolver.
func (f FontResolverFunc) ResolveFonts(query FontQuery) []text.Face {
	if f == nil {
		return nil
	}
	return f(query)
}

// SimpleFontResolver is the narrow single-face resolution interface. Values
// implementing it are automatically adapted by WithFontResolver.
type SimpleFontResolver interface {
	ResolveFont(family string, size float64) (text.Face, error)
}

// Option configures parsing/rendering. Options mutate a Document's public
// text fields and can be passed to Parse, Render, Document.Render or RenderTo.
type Option func(*Document)

// Package-level optional font configuration. These are convenience seams for
// callers that render many SVG documents with the same application font.
var (
	Font                text.Face
	TextFont            text.Face
	DefaultFont         text.Face
	DefaultFontFace     text.Face
	DefaultTextFont     text.Face
	DefaultFontResolver FontResolver
)

// SetDefaultFont sets the package-wide fallback face.
func SetDefaultFont(face text.Face) {
	Font = face
	TextFont = face
	DefaultFont = face
	DefaultFontFace = face
	DefaultTextFont = face
}

// SetDefaultFontResolver sets the package-wide family resolver.
func SetDefaultFontResolver(resolver FontResolver) {
	DefaultFontResolver = resolver
}

// ParseWithFont parses SVG and installs face as the preferred text face.
func ParseWithFont(data []byte, face text.Face) (*Document, error) {
	return Parse(data, WithFont(face))
}

// RenderWithFont parses and renders SVG using the preferred text face.
func RenderWithFont(data []byte, width, height int, face text.Face) (*image.RGBA, error) {
	doc, err := ParseWithFont(data, face)
	if err != nil {
		return nil, err
	}
	return doc.Render(width, height), nil
}

// TextReport collects recoverable text diagnostics during one RenderTo call.
type TextReport struct {
	Errors []error
}

// Err returns all collected diagnostics as one joined error.
func (r *TextReport) Err() error {
	if r == nil || len(r.Errors) == 0 {
		return nil
	}
	return errors.Join(r.Errors...)
}

// WithTextReport installs a diagnostic collector for one render call.
func WithTextReport(report *TextReport) Option {
	return func(d *Document) {
		if report == nil {
			return
		}
		previous := d.TextOptions.ErrorHandler
		d.TextOptions.ErrorHandler = func(err error) {
			report.Errors = append(report.Errors, err)
			if previous != nil {
				previous(err)
			}
		}
	}
}

// WithFont sets the preferred text face.
func WithFont(face text.Face) Option {
	return func(d *Document) {
		d.Font = face
		d.FontFace = face
		d.TextFont = face
	}
}

// WithFontFace is an alias for WithFont.
func WithFontFace(face text.Face) Option { return WithFont(face) }

// WithTextFont is an alias for WithFont.
func WithTextFont(face text.Face) Option { return WithFont(face) }

// LoadFontFile loads a TTF/OTF/TTC source. The returned face uses size 1 as a
// placeholder; SVG rendering resizes source-backed faces to font-size.
func LoadFontFile(path string) (text.Face, error) {
	source, err := text.NewFontSourceFromFile(path)
	if err != nil {
		return nil, err
	}
	return source.Face(16), nil
}

// WithFontSource installs a source-backed face.
func WithFontSource(source *text.FontSource) Option {
	if source == nil {
		return func(*Document) {}
	}
	return WithFont(source.Face(16))
}

// WithFontResolver sets the font-family resolver.
func WithFontResolver(resolver FontResolver) Option {
	return func(d *Document) { d.FontResolver = resolver }
}

// WithSimpleFontResolver adapts a narrow ResolveFont method to FontResolver.
func WithSimpleFontResolver(resolver SimpleFontResolver) Option {
	return func(d *Document) {
		if resolver == nil {
			d.FontResolver = nil
			return
		}
		d.FontResolver = simpleResolverAdapter{resolver: resolver}
	}
}

type simpleResolverAdapter struct {
	resolver SimpleFontResolver
}

func (a simpleResolverAdapter) ResolveFonts(query FontQuery) []text.Face {
	family := query.Family
	if family == "" && len(query.Families) > 0 {
		family = query.Families[0]
	}
	face, err := a.resolver.ResolveFont(family, query.Size)
	if err != nil || face == nil {
		return nil
	}
	return []text.Face{face}
}

func (a simpleResolverAdapter) ResolveFont(family string, size float64) (text.Face, error) {
	return a.resolver.ResolveFont(family, size)
}

// WithFallbackFonts appends explicit fallback faces.
func WithFallbackFonts(faces ...text.Face) Option {
	return func(d *Document) { d.FallbackFonts = append(d.FallbackFonts, faces...) }
}

// WithNamedFont registers a face for a normalized family name.
func WithNamedFont(family string, face text.Face) Option {
	return func(d *Document) {
		if d.NamedFonts == nil {
			d.NamedFonts = make(map[string]text.Face)
		}
		d.NamedFonts[normalizeFamily(family)] = face
	}
}

// WithMaxTextLength sets both byte and rune limits.
func WithMaxTextLength(max int) Option {
	return func(d *Document) {
		d.TextOptions.MaxBytes = max
		d.TextOptions.MaxRunes = max
	}
}

// WithTextLimits sets separate byte and rune limits.
func WithTextLimits(maxBytes, maxRunes int) Option {
	return func(d *Document) {
		d.TextOptions.MaxBytes = maxBytes
		d.TextOptions.MaxRunes = maxRunes
	}
}

// WithStrictTextCoordinates causes Parse to reject invalid text coordinates.
func WithStrictTextCoordinates() Option {
	return func(d *Document) { d.TextOptions.StrictCoordinates = true }
}

// WithTextWarnings enables recording and returns the option for convenience.
func WithTextWarnings() Option {
	return func(d *Document) { d.TextOptions.RecordWarnings = true }
}

// WithTextErrorHandler installs a diagnostic callback.
func WithTextErrorHandler(handler func(error)) Option {
	return func(d *Document) { d.TextOptions.ErrorHandler = handler }
}

// WithTextOptions replaces the text safety/diagnostic options.
func WithTextOptions(options TextOptions) Option {
	return func(d *Document) { d.TextOptions = options }
}

// WithoutSystemFonts disables platform font discovery.
func WithoutSystemFonts() Option {
	return func(d *Document) { d.TextOptions.DisableSystemFonts = true }
}

// RenderOptions is a plain-struct alternative to functional Option values.
// Field names intentionally cover the common font configuration spellings.
type RenderOptions struct {
	Font              text.Face
	FontFace          text.Face
	TextFont          text.Face
	FontResolver      FontResolver
	Resolver          FontResolver
	FallbackFonts     []text.Face
	TextOptions       TextOptions
	MaxTextLength     int
	StrictCoordinates bool
	RecordWarnings    bool
	ErrorHandler      func(error)
}

// ParseOptions is an alias for RenderOptions.
type ParseOptions = RenderOptions

// Options is an alias for RenderOptions.
type Options = RenderOptions

func (d *Document) applyOptions(opts ...any) *Document {
	if d == nil {
		return nil
	}
	for _, opt := range opts {
		d.applyOption(opt)
	}
	return d
}

func (d *Document) applyOption(opt any) {
	if opt == nil {
		return
	}
	switch o := opt.(type) {
	case Option:
		if o != nil {
			o(d)
		}
	case func(*Document):
		if o != nil {
			o(d)
		}
	case text.Face:
		if o != nil {
			WithFont(o)(d)
		}
	case *text.FontSource:
		if o != nil {
			WithFontSource(o)(d)
		}
	case FontResolver:
		d.FontResolver = o
	case FontResolverFunc:
		d.FontResolver = o
	case SimpleFontResolver:
		WithSimpleFontResolver(o)(d)
	case func(FontQuery) []text.Face:
		d.FontResolver = FontResolverFunc(o)
	case func(string, float64) (text.Face, error):
		WithSimpleFontResolver(simpleResolverFunc(o))(d)
	case RenderOptions:
		d.applyRenderOptions(&o)
	case *RenderOptions:
		if o != nil {
			d.applyRenderOptions(o)
		}
	case TextOptions:
		d.TextOptions = o
	case *TextOptions:
		if o != nil {
			d.TextOptions = *o
		}
	}
}

type simpleResolverFunc func(string, float64) (text.Face, error)

func (f simpleResolverFunc) ResolveFont(family string, size float64) (text.Face, error) {
	return f(family, size)
}

func (d *Document) applyRenderOptions(o *RenderOptions) {
	if o.Font != nil {
		WithFont(o.Font)(d)
	}
	if o.FontFace != nil {
		d.FontFace = o.FontFace
	}
	if o.TextFont != nil {
		d.TextFont = o.TextFont
	}
	if o.FontResolver != nil {
		d.FontResolver = o.FontResolver
	}
	if o.Resolver != nil {
		d.FontResolver = o.Resolver
	}
	if len(o.FallbackFonts) > 0 {
		d.FallbackFonts = append(d.FallbackFonts, o.FallbackFonts...)
	}
	d.TextOptions = o.TextOptions
	if o.MaxTextLength > 0 {
		d.TextOptions.MaxBytes = o.MaxTextLength
		d.TextOptions.MaxRunes = o.MaxTextLength
	}
	if o.StrictCoordinates {
		d.TextOptions.StrictCoordinates = true
	}
	if o.RecordWarnings {
		d.TextOptions.RecordWarnings = true
	}
	if o.ErrorHandler != nil {
		d.TextOptions.ErrorHandler = o.ErrorHandler
	}
}

func (o TextOptions) byteLimit() int {
	switch {
	case o.MaxBytes < 0:
		return -1
	case o.MaxBytes > 0:
		return o.MaxBytes
	default:
		return DefaultMaxTextBytes
	}
}

func (o TextOptions) runeLimit() int {
	switch {
	case o.MaxRunes < 0:
		return -1
	case o.MaxRunes > 0:
		return o.MaxRunes
	default:
		return DefaultMaxTextRunes
	}
}

func (d *Document) reportTextError(err error) {
	if err == nil || d == nil {
		return
	}
	if d.TextOptions.RecordWarnings {
		d.Warnings = append(d.Warnings, err)
	}
	if d.TextOptions.ErrorHandler != nil {
		d.TextOptions.ErrorHandler(err)
	}
}

// TextError describes a recoverable SVG text fallback.
type TextError struct {
	Code error
	Err  error
	Text string
}

func (e *TextError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("svg text %s: %v", e.Text, e.Err)
	}
	return fmt.Sprintf("svg text %s", e.Text)
}

func (e *TextError) Unwrap() []error { return []error{e.Code, e.Err} }

var (
	systemFontOnce sync.Once
	systemFont     *systemResolver
)

type systemResolver struct {
	mu      sync.Mutex
	sources map[string]*text.FontSource
}

func defaultSystemResolver() *systemResolver {
	systemFontOnce.Do(func() {
		systemFont = &systemResolver{sources: make(map[string]*text.FontSource)}
	})
	return systemFont
}

func normalizeFamily(family string) string {
	family = strings.TrimSpace(family)
	family = strings.Trim(family, `"'`)
	return strings.ToLower(strings.TrimSpace(family))
}

func parseFontFamilies(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		f := normalizeFamily(part)
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

func (r *systemResolver) source(path string) *text.FontSource {
	if path == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if src, ok := r.sources[path]; ok {
		return src
	}
	src, err := text.NewFontSourceFromFile(path)
	if err != nil {
		r.sources[path] = nil
		return nil
	}
	r.sources[path] = src
	return src
}

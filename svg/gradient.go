package svg

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/gogpu/gg"
)

// GradientKind selects linear versus radial gradient geometry.
type GradientKind int

const (
	// LinearGradient is an SVG <linearGradient>.
	LinearGradient GradientKind = iota
	// RadialGradient is an SVG <radialGradient>.
	RadialGradient
)

// String returns the SVG element name.
func (k GradientKind) String() string {
	if k == RadialGradient {
		return "radialGradient"
	}
	return "linearGradient"
}

// GradientUnits selects how gradient geometry coordinates are interpreted.
type GradientUnits int

const (
	// ObjectBoundingBox maps the unit square to the painted element's bbox.
	ObjectBoundingBox GradientUnits = iota
	// UserSpaceOnUse interprets coordinates in the referencing user space.
	UserSpaceOnUse
)

// SpreadMethod controls sampling outside the [0,1] gradient interval.
type SpreadMethod int

const (
	// SpreadPad clamps to the edge stops (SVG default).
	SpreadPad SpreadMethod = iota
	// SpreadReflect mirrors the gradient repeatedly.
	SpreadReflect
	// SpreadRepeat repeats the gradient.
	SpreadRepeat
)

// GradientLength is an SVG coordinate that may be expressed as a percentage.
type GradientLength struct {
	Value   float64
	Percent bool
	Set     bool
}

// GradientStop is one resolved <stop>.
type GradientStop struct {
	// Offset is the normalized, monotonic offset in [0,1].
	Offset float64
	// Color is the straight-alpha stop color with stop-opacity applied.
	Color gg.RGBA
}

// Gradient is a resolved, href-merged and normalized SVG paint server.
//
// Instances are safe to use for rendering. When the source markup contained a
// validation problem, the matching [GradientError] is reported through
// [Document.GradientErrors] and the gradient fields here still carry
// deterministic, finite fallback values.
type Gradient struct {
	ID        string
	Kind      GradientKind
	Path      string
	Units     GradientUnits
	Spread    SpreadMethod
	Transform gg.Matrix

	// Geometry. Linear uses X1/Y1/X2/Y2; radial uses CX/CY/R and FX/FY/FR.
	X1, Y1, X2, Y2 GradientLength
	CX, CY, R      GradientLength
	FX, FY, FR     GradientLength

	// Stops are normalized (clamped offsets, monotonic, opacity baked in).
	Stops []GradientStop

	// Degenerate marks a gradient whose geometry cannot produce a varying
	// parameter (zero-length vector, negative radius clamped to zero, ...).
	Degenerate bool
}

// GradientError describes one malformed gradient definition or dangling paint
// reference. Path is an XPath-like location such as
// /svg/defs/linearGradient[@id='g3']/stop[2], so callers can locate the source.
type GradientError struct {
	// Path is the XPath-like element location.
	Path string
	// ID is the affected gradient id or referenced id when available.
	ID string
	// Kind is a short stable machine-readable category, for example
	// "missing-reference", "cyclic-href" or "non-finite-parameter".
	Kind string
	// Message is a human readable explanation.
	Message string
}

func (e *GradientError) Error() string {
	if e == nil {
		return "svg: gradient error"
	}
	where := e.Path
	if where == "" {
		where = "?"
	}
	if e.ID != "" && !strings.Contains(where, "@id=") {
		return fmt.Sprintf("svg: gradient error at %s (id=%s, %s): %s", where, e.ID, e.Kind, e.Message)
	}
	return fmt.Sprintf("svg: gradient error at %s (%s): %s", where, e.Kind, e.Message)
}

// maxGradientValue bounds every raw numeric gradient parameter. Values beyond
// this are treated as an "oversized range" validation error and clamped. The
// bound is far above any realistic icon coordinate while keeping matrix and
// quadratic math safely away from float64 overflow.
const maxGradientValue = 1e9

// --- raw parse model -------------------------------------------------------

type gradientStopDef struct {
	path       string
	offsetRaw  string
	colorRaw   string
	opacityRaw string
}

type gradientDef struct {
	id    string
	path  string
	kind  GradientKind
	attrs map[string]string
	stops []gradientStopDef
}

func (d *Document) addIssue(path, id, kind, format string, args ...any) {
	d.gradientErrors = append(d.gradientErrors, &GradientError{
		Path:    path,
		ID:      id,
		Kind:    kind,
		Message: fmt.Sprintf(format, args...),
	})
}

// parsePaintRef decodes `fill="url(#id)"` (also with a fallback color and
// single/double quotes). Returns ok=false for ordinary color values.
func parsePaintRef(value string) (id, fallback string, ok bool) {
	s := strings.TrimSpace(value)
	// CSS-ish "url(#id) fallback".
	lower := strings.ToLower(s)
	if !strings.HasPrefix(lower, "url(") {
		return "", "", false
	}
	close := strings.IndexByte(s, ')')
	if close < 0 {
		return "", "", false
	}
	inner := strings.TrimSpace(s[len("url("):close])
	inner = strings.Trim(inner, `"'`)
	inner = strings.TrimSpace(inner)
	if strings.HasPrefix(inner, "#") {
		id = inner[1:]
	} else {
		id = inner
	}
	if rest := strings.TrimSpace(s[close+1:]); rest != "" {
		fallback = rest
	}
	return id, fallback, id != ""
}

// finalizeGradients resolves href chains, validates and normalizes every
// gradient, then checks all fill/stroke url(#id) references. It always
// populates d.GradientList/gradientByID with usable fallbacks.
func finalizeGradients(d *Document, ps *parseState) error {
	defs := ps.defs
	d.gradientByID = make(map[string]*Gradient, len(defs))

	// Duplicate ids: keep the first definition (getElementById semantics)
	// and report later duplicates.
	seen := make(map[string]bool, len(defs))
	merged := make(map[string]*Gradient, len(defs))
	for _, def := range defs {
		if def.id != "" {
			if seen[def.id] {
				d.addIssue(def.path, def.id, "duplicate-id",
					"duplicate gradient id %q; keeping the first definition", def.id)
				continue
			}
			seen[def.id] = true
		}
		g, issues := resolveGradient(d, def, defs)
		d.gradientErrors = append(d.gradientErrors, issues...)
		if g == nil {
			continue
		}
		merged[def.id] = g
		if def.id != "" {
			d.gradientByID[def.id] = g
		}
		d.GradientList = append(d.GradientList, g)
	}

	validatePaintReferences(d, ps.allIDs)

	if len(d.gradientErrors) == 0 {
		return nil
	}
	errs := make([]error, len(d.gradientErrors))
	for i, ge := range d.gradientErrors {
		errs[i] = ge
	}
	return errors.Join(errs...)
}

// validatePaintReferences walks every element and reports url(#id) paints
// that do not resolve to a parsed gradient. ids present elsewhere in the
// document (pattern, mask, ...) are not "missing": those paints degrade via
// the ordinary fallback path rather than being reported.
func validatePaintReferences(d *Document, allIDs map[string]bool) {
	knownID := func(id string) bool {
		if _, ok := d.gradientByID[id]; ok {
			return true
		}
		return allIDs != nil && allIDs[id]
	}
	// The <svg> root may itself carry an inherited fill="url(#id)".
	if id, _, isURL := parsePaintRef(d.RootFill); isURL {
		if !knownID(id) {
			d.addIssue("/svg", id, "missing-reference",
				"root fill references missing paint server %q; degrading to the default black fill", id)
		}
	}
	var walk func(elems []Element)
	walk = func(elems []Element) {
		for _, e := range elems {
			if e == nil {
				continue
			}
			a := e.attrs()
			if a == nil {
				continue
			}
			for _, pair := range [2]struct {
				value, prop string
			}{{a.Fill, "fill"}, {a.Stroke, "stroke"}} {
				id, fallback, isURL := parsePaintRef(pair.value)
				if !isURL {
					continue
				}
				if knownID(id) {
					continue
				}
				// A CSS fallback color makes the degradation explicit and safe.
				msg := fmt.Sprintf("%s references missing gradient %q", pair.prop, id)
				if fallback != "" {
					msg += fmt.Sprintf("; using fallback color %q", fallback)
				} else if pair.prop == "fill" {
					msg += "; degrading to the default black fill"
				} else {
					msg += "; stroke will not be painted"
				}
				d.addIssue(a.Path, id, "missing-reference", "%s", msg)
			}
			if g, ok := e.(*GroupElement); ok {
				walk(g.Children)
			}
		}
	}
	walk(d.Elements)
}

// defByID builds a lookup including duplicate handling.
func defByID(defs []*gradientDef) map[string]*gradientDef {
	m := make(map[string]*gradientDef, len(defs))
	for _, def := range defs {
		if def.id == "" {
			continue
		}
		if _, exists := m[def.id]; !exists {
			m[def.id] = def
		}
	}
	return m
}

// resolveGradient merges xlink:href inheritance and produces the normalized
// runtime Gradient. Cycle detection is per resolution chain.
func resolveGradient(d *Document, def *gradientDef, all []*gradientDef) (*Gradient, []*GradientError) {
	byID := defByID(all)

	// Build the inheritance chain from leaf to root, detecting cycles and
	// missing href targets.
	type node struct{ def *gradientDef }
	var chain []*gradientDef
	visited := make(map[string]bool)
	cur := def
	for cur != nil {
		if cur.id != "" {
			if visited[cur.id] {
				issue := &GradientError{
					Path: cur.path, ID: cur.id, Kind: "cyclic-href",
					Message: fmt.Sprintf("href inheritance cycle detected at %q", cur.id),
				}
				// Return a best-effort gradient built from the leaf only.
				g, issues := buildGradient(d, def, nil)
				return g, append([]*GradientError{issue}, issues...)
			}
			visited[cur.id] = true
		}
		chain = append(chain, cur)
		href := strings.TrimSpace(cur.attrs["href"])
		if href == "" {
			href = strings.TrimSpace(cur.attrs["xlink:href"])
		}
		if href == "" {
			break
		}
		href = strings.TrimPrefix(href, "#")
		parent, ok := byID[href]
		if !ok {
			issue := &GradientError{
				Path: cur.path, ID: href, Kind: "missing-href",
				Message: fmt.Sprintf("href references missing gradient %q", href),
			}
			g, issues := buildGradient(d, def, nil)
			return g, append([]*GradientError{issue}, issues...)
		}
		cur = parent
	}

	// Merge attributes with nearest-element-wins semantics: the chain starts
	// at the referencing element and walks toward the href root, so the first
	// non-empty value seen for a key is the authoritative one.
	merged := map[string]string{}
	for _, node := range chain {
		for k, v := range node.attrs {
			if strings.TrimSpace(v) == "" {
				continue
			}
			if _, exists := merged[k]; !exists {
				merged[k] = v
			}
		}
	}

	// Stops: the first element in leaf→root order that actually has stops wins
	// (SVG inheritance replaces the whole stop list).
	var stopDefs []gradientStopDef
	for _, node := range chain {
		if len(node.stops) > 0 {
			stopDefs = node.stops
			break
		}
	}

	return buildGradient(d, def, &mergedOverride{attrs: merged, stops: stopDefs})
}

type mergedOverride struct {
	attrs map[string]string
	stops []gradientStopDef
}

// attr reads a merged attribute with fallback to the leaf raw map.
func attrValue(def *gradientDef, ov *mergedOverride, key string) string {
	if ov != nil {
		if v, ok := ov.attrs[key]; ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return def.attrs[key]
}

func buildGradient(d *Document, def *gradientDef, ov *mergedOverride) (*Gradient, []*GradientError) {
	var issues []*GradientError
	issue := func(kind, format string, args ...any) {
		issues = append(issues, &GradientError{
			Path: def.path, ID: def.id, Kind: kind,
			Message: fmt.Sprintf(format, args...),
		})
	}

	g := &Gradient{
		ID:   def.id,
		Kind: def.kind,
		Path: def.path,
	}

	// gradientUnits.
	switch strings.TrimSpace(attrValue(def, ov, "gradientUnits")) {
	case "", "objectBoundingBox":
		g.Units = ObjectBoundingBox
	case "userSpaceOnUse":
		g.Units = UserSpaceOnUse
	default:
		issue("unknown-units", "unknown gradientUnits %q; using objectBoundingBox",
			attrValue(def, ov, "gradientUnits"))
		g.Units = ObjectBoundingBox
	}

	// spreadMethod.
	switch strings.TrimSpace(attrValue(def, ov, "spreadMethod")) {
	case "", "pad":
		g.Spread = SpreadPad
	case "reflect":
		g.Spread = SpreadReflect
	case "repeat":
		g.Spread = SpreadRepeat
	default:
		issue("unknown-spread", "unknown spreadMethod %q; using pad",
			attrValue(def, ov, "spreadMethod"))
		g.Spread = SpreadPad
	}

	// gradientTransform: parser is best-effort; validate finiteness/range.
	if tr := strings.TrimSpace(attrValue(def, ov, "gradientTransform")); tr != "" {
		m, err := transformMatrix(tr)
		if err != nil {
			issue("invalid-transform", "invalid gradientTransform %q: %v; using partial transform", tr, err)
		}
		if !matrixFinite(m) {
			issue("non-finite-parameter", "gradientTransform %q produced non-finite matrix; ignoring it", tr)
			m = gg.Identity()
		} else if matrixOversized(m) {
			issue("oversized-range", "gradientTransform %q has oversized entries; ignoring it", tr)
			m = gg.Identity()
		}
		g.Transform = m
	} else {
		g.Transform = gg.Identity()
	}

	// Geometry defaults per SVG.
	if def.kind == LinearGradient {
		g.X1 = parseLengthAttr(def, ov, "x1", "0%", &issues)
		g.Y1 = parseLengthAttr(def, ov, "y1", "0%", &issues)
		g.X2 = parseLengthAttr(def, ov, "x2", "100%", &issues)
		g.Y2 = parseLengthAttr(def, ov, "y2", "0%", &issues)
		if g.X1.Value == g.X2.Value && g.Y1.Value == g.Y2.Value &&
			g.X1.Percent == g.X2.Percent && g.Y1.Percent == g.Y2.Percent {
			g.Degenerate = true
		}
	} else {
		g.CX = parseLengthAttr(def, ov, "cx", "50%", &issues)
		g.CY = parseLengthAttr(def, ov, "cy", "50%", &issues)
		g.R = parseLengthAttr(def, ov, "r", "50%", &issues)
		// fx/fy default to cx/cy after inheritance merging.
		g.FX = parseOptionalLengthAttr(def, ov, "fx", g.CX, &issues)
		g.FY = parseOptionalLengthAttr(def, ov, "fy", g.CY, &issues)
		g.FR = parseLengthAttr(def, ov, "fr", "0", &issues)

		if g.R.Value < 0 {
			issue("negative-radius", "negative gradient radius r=%s; clamping to 0 (solid fallback)",
				formatLength(g.R))
			g.R.Value = 0
			g.Degenerate = true
		}
		if g.FR.Value < 0 {
			issue("negative-radius", "negative focal radius fr=%s; clamping to 0", formatLength(g.FR))
			g.FR.Value = 0
		}
		if g.R.Value == 0 {
			g.Degenerate = true
		}
	}

	// Stops.
	rawStops := def.stops
	if ov != nil && ov.stops != nil {
		rawStops = ov.stops
	}
	stops, stopIssues := normalizeStops(def, rawStops)
	issues = append(issues, stopIssues...)
	g.Stops = stops

	return g, issues
}

func parseLengthAttr(def *gradientDef, ov *mergedOverride, key, defVal string, issues *[]*GradientError) GradientLength {
	raw := strings.TrimSpace(attrValue(def, ov, key))
	if raw == "" {
		raw = defVal
	}
	l, err := parseGradientLength(raw)
	if err != nil {
		*issues = append(*issues, &GradientError{
			Path: def.path, ID: def.id, Kind: "invalid-parameter",
			Message: fmt.Sprintf("invalid %s=%q: %v; using default %q", key, raw, err, defVal),
		})
		l, _ = parseGradientLength(defVal)
		return l
	}
	if !math.IsNaN(l.Value) && !math.IsInf(l.Value, 0) && math.Abs(l.Value) > maxGradientValue {
		*issues = append(*issues, &GradientError{
			Path: def.path, ID: def.id, Kind: "oversized-range",
			Message: fmt.Sprintf("%s=%s exceeds safe range ±%.0e; clamping", key, raw, maxGradientValue),
		})
		l.Value = clampMag(l.Value, maxGradientValue)
	}
	if math.IsNaN(l.Value) || math.IsInf(l.Value, 0) {
		*issues = append(*issues, &GradientError{
			Path: def.path, ID: def.id, Kind: "non-finite-parameter",
			Message: fmt.Sprintf("%s=%q is not finite; using default %q", key, raw, defVal),
		})
		l, _ = parseGradientLength(defVal)
	}
	l.Set = true
	return l
}

func parseOptionalLengthAttr(def *gradientDef, ov *mergedOverride, key string, fallback GradientLength, issues *[]*GradientError) GradientLength {
	raw := strings.TrimSpace(attrValue(def, ov, key))
	if raw == "" {
		return fallback
	}
	return parseLengthAttr(def, ov, key, formatLength(fallback), issues)
}

func formatLength(l GradientLength) string {
	if l.Percent {
		return strconv.FormatFloat(l.Value*100, 'f', -1, 64) + "%"
	}
	return strconv.FormatFloat(l.Value, 'f', -1, 64)
}

func clampMag(v, bound float64) float64 {
	if v > bound {
		return bound
	}
	if v < -bound {
		return -bound
	}
	return v
}

// parseGradientLength parses an SVG length/number/percentage. Unit suffixes
// such as px are accepted and treated as user units.
func parseGradientLength(raw string) (GradientLength, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return GradientLength{}, fmt.Errorf("empty length")
	}
	l := GradientLength{Set: true}
	if strings.HasSuffix(s, "%") {
		l.Percent = true
		s = strings.TrimSuffix(s, "%")
	} else {
		// Strip common absolute units; gradients only need user-unit scale.
		for _, u := range []string{"px", "pt", "em", "ex", "in", "cm", "mm"} {
			if strings.HasSuffix(strings.ToLower(s), u) {
				s = s[:len(s)-len(u)]
				break
			}
		}
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return GradientLength{}, err
	}
	l.Value = v
	if l.Percent {
		l.Value = v / 100
	}
	return l, nil
}

// normalizeStops parses stop colors/opacities, clamps offsets, reports
// duplicate and non-monotonic offsets with element paths, and enforces the
// SVG monotonic-offset rule.
func normalizeStops(def *gradientDef, raw []gradientStopDef) ([]GradientStop, []*GradientError) {
	var issues []*GradientError
	add := func(path, id, kind, format string, args ...any) {
		issues = append(issues, &GradientError{
			Path: path, ID: id, Kind: kind, Message: fmt.Sprintf(format, args...),
		})
	}
	if len(raw) == 0 {
		return nil, nil
	}
	type parsed struct {
		offset float64
		path   string
	}
	offs := make([]parsed, len(raw))
	prev := 0.0
	var prevRaw float64
	havePrevRaw := false
	for i, s := range raw {
		o := 0.0
		trimmed := strings.TrimSpace(s.offsetRaw)
		if trimmed != "" {
			l, err := parseGradientLength(trimmed)
			switch {
			case err != nil || math.IsNaN(l.Value) || math.IsInf(l.Value, 0):
				add(s.path, def.id, "non-finite-parameter",
					"stop offset %q is invalid; using 0", s.offsetRaw)
				o = 0
			default:
				o = l.Value // percentages already normalized to fraction
			}
		}
		if math.Abs(o) > maxGradientValue {
			add(s.path, def.id, "oversized-range",
				"stop offset %q is oversized; clamping to [0,1]", s.offsetRaw)
		}
		clamped := clamp01Safe(o)
		if i > 0 && havePrevRaw {
			if o+offsetEpsilon < prevRaw {
				add(s.path, def.id, "non-monotonic-offset",
					"stop offset %s is before the previous offset %s; enforcing monotonic order",
					formatOffset(o), formatOffset(prevRaw))
			}
			if o == prevRaw {
				add(s.path, def.id, "duplicate-stop",
					"duplicate stop offset %s; keeping both stops for the hard transition",
					formatOffset(o))
			}
		}
		prevRaw = o
		havePrevRaw = true
		// SVG monotonic rule: offset_i = max(offset_i, offset_{i-1}) after clamp.
		if clamped < prev {
			clamped = prev
		}
		prev = clamped
		offs[i] = parsed{offset: clamped, path: s.path}
	}

	stops := make([]GradientStop, 0, len(raw))
	for i, s := range raw {
		c := gg.Black
		if pc, err := parseColor(firstNonEmpty(s.colorRaw, "black")); err == nil && pc != nil {
			c = gg.FromColor(pc)
		} else if s.colorRaw != "" {
			add(s.path, def.id, "invalid-color",
				"unsupported stop-color %q; using black", s.colorRaw)
		}
		op := 1.0
		if strings.TrimSpace(s.opacityRaw) != "" {
			if v, err := strconv.ParseFloat(strings.TrimSpace(s.opacityRaw), 64); err == nil {
				op = v
			} else {
				add(s.path, def.id, "invalid-parameter",
					"invalid stop-opacity %q; using 1", s.opacityRaw)
			}
		}
		op = clamp01Safe(op)
		c.A *= op
		stops = append(stops, GradientStop{Offset: offs[i].offset, Color: c})
	}

	// Stable sort by offset while preserving source order for equal offsets
	// (sort.SliceStable), so hard transitions are deterministic.
	sort.SliceStable(stops, func(i, j int) bool { return stops[i].Offset < stops[j].Offset })
	return stops, issues
}

func formatOffset(o float64) string {
	return strconv.FormatFloat(o, 'f', -1, 64)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func clamp01Safe(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 1
	}
	return v
}

func matrixFinite(m gg.Matrix) bool {
	return allFinite(m.A, m.B, m.C, m.D, m.E, m.F)
}

func matrixOversized(m gg.Matrix) bool {
	for _, v := range []float64{m.A, m.B, m.C, m.D, m.E, m.F} {
		if math.Abs(v) > maxGradientValue {
			return true
		}
	}
	return false
}

func allFinite(vs ...float64) bool {
	for _, v := range vs {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

// safeInvert inverts m with a relative-tolerant determinant check. gg.Matrix.
// Invert falls back to identity for abs(det)<1e-10, which is wrong for
// legitimately tiny scales, so the SVG layer uses this instead.
func safeInvert(m gg.Matrix) (gg.Matrix, bool) {
	det := m.A*m.E - m.B*m.D
	scale := math.Max(1, math.Abs(m.A*m.E)+math.Abs(m.B*m.D))
	if math.Abs(det) <= 1e-18*scale || !allFinite(det) {
		return gg.Identity(), false
	}
	invDet := 1.0 / det
	inv := gg.Matrix{
		A: m.E * invDet,
		B: -m.B * invDet,
		C: (m.B*m.F - m.C*m.E) * invDet,
		D: -m.D * invDet,
		E: m.A * invDet,
		F: (m.C*m.D - m.A*m.F) * invDet,
	}
	if !matrixFinite(inv) {
		return gg.Identity(), false
	}
	return inv, true
}

// offsetEpsilon absorbs float noise when checking monotonicity.
const offsetEpsilon = 1e-9

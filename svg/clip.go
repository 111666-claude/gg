package svg

import (
	"math"
	"sort"
	"strings"

	"github.com/gogpu/gg"
)

// parseClipReference extracts the target id from a clip-path value.
//
// Supported forms:
//
//	url(#id), url('#id'), url("#id"), url(http://x/y.svg#id), #id and a
//	bare id. The values "", "none" and "inherit" report ok=false.
func parseClipReference(raw string) (id string, ok bool) {
	s := strings.TrimSpace(raw)
	if s == "" || strings.EqualFold(s, "none") || strings.EqualFold(s, "inherit") {
		return "", false
	}
	if i := strings.Index(s, "url("); i >= 0 {
		rest := s[i+4:]
		j := strings.Index(rest, ")")
		if j < 0 {
			return "", false
		}
		ref := strings.Trim(strings.TrimSpace(rest[:j]), `"'`)
		if h := strings.Index(ref, "#"); h >= 0 {
			ref = ref[h+1:]
		}
		ref = urlUnescape(strings.TrimSpace(ref))
		if ref == "" {
			return "", false
		}
		return ref, true
	}
	if strings.HasPrefix(s, "#") {
		id = urlUnescape(strings.TrimPrefix(s, "#"))
		return strings.TrimSpace(id), true
	}
	return s, true
}

// urlUnescape is a tiny percent-decoder for fragment identifiers; it avoids
// pulling net/url into the common ASCII path.
func urlUnescape(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			hi, ok1 := hexVal(s[i+1])
			lo, ok2 := hexVal(s[i+2])
			if ok1 && ok2 {
				b.WriteByte(byte(hi<<4 | lo))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func hexVal(c byte) (int, bool) {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0'), true
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10, true
	}
	return 0, false
}

func quote(s string) string {
	return "\"" + s + "\""
}

// validateClipReferences performs all post-parse clip validation:
// resolvability of every reference plus cycle/depth checking of the
// clipPath-to-clipPath graph.
func (p *svgParser) validateClipReferences() error {
	// Every reference must resolve to an actual clipPath definition.
	for _, ref := range p.refs {
		id, ok := parseClipReference(ref.raw)
		if !ok {
			return &ParseError{Path: ref.path, Detail: "invalid clip-path value " + quote(ref.raw) + " (expected url(#id))"}
		}
		if _, isClip := p.clips[id]; isClip {
			continue
		}
		if _, idExists := p.ids[id]; idExists {
			return &ParseError{Path: ref.path, Detail: "clip-path reference " + quote(ref.raw) + " points to id " + quote(id) + " which is not a <clipPath> definition"}
		}
		return &ParseError{Path: ref.path, Detail: "missing id: clip-path reference " + quote(ref.raw) + " does not match any clipPath id " + quote(id)}
	}

	// Cycle / depth validation over the definition graph. Edges include a
	// clipPath element's own clip-path reference AND references carried by
	// shapes/groups nested inside it. Ids are traversed in sorted order for
	// deterministic errors.
	ids := make([]string, 0, len(p.clips))
	for id := range p.clips {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	const (
		unvisited = 0
		visiting  = 1
		finished  = 2
	)
	color := make(map[string]int, len(ids))
	var chain []string

	var dfs func(id string, depth int) error
	dfs = func(id string, depth int) error {
		cp, ok := p.clips[id]
		if !ok {
			// Dangling definition refs are reported through p.refs, where
			// every clip-path attribute is recorded.
			return nil
		}
		if depth > maxClipRefDepth {
			return &ParseError{Path: cp.ElementPath, Detail: "clip-path nesting chain exceeds the maximum depth (too deeply nested)"}
		}
		switch color[id] {
		case visiting:
			cycle := append(append([]string{}, chain...), id)
			return &ParseError{
				Path:   cp.ElementPath,
				Detail: "circular clip-path reference cycle (cyclic chain: " + strings.Join(cycle, " -> ") + ")",
			}
		case finished:
			return nil
		}
		color[id] = visiting
		chain = append(chain, id)
		for _, edge := range clipGraphEdges(cp) {
			if _, ok := parseClipReference(edge.raw); !ok {
				continue
			}
			rid, _ := parseClipReference(edge.raw)
			if err := dfs(rid, depth+1); err != nil {
				return err
			}
		}
		chain = chain[:len(chain)-1]
		color[id] = finished
		return nil
	}

	for _, id := range ids {
		if err := dfs(id, 0); err != nil {
			return err
		}
	}
	return nil
}

// clipRefEdge is one clip-path attribute inside a clipPath definition.
type clipRefEdge struct {
	raw      string
	fromPath string
}

// clipGraphEdges returns every clip-path reference that participates in a
// clipPath's region: the clipPath element's own attribute plus attributes on
// all descendant nodes.
func clipGraphEdges(cp *ClipPath) []clipRefEdge {
	var edges []clipRefEdge
	if ref := strings.TrimSpace(cp.Attrs.ClipPathRef); ref != "" && !strings.EqualFold(ref, "none") {
		edges = append(edges, clipRefEdge{raw: ref, fromPath: cp.ElementPath})
	}
	var walk func(elems []Element)
	walk = func(elems []Element) {
		for _, e := range elems {
			a := e.attrs()
			if ref := strings.TrimSpace(a.ClipPathRef); ref != "" && !strings.EqualFold(ref, "none") {
				edges = append(edges, clipRefEdge{raw: ref, fromPath: a.elementPath})
			}
			if g, ok := e.(*GroupElement); ok {
				walk(g.Children)
			}
		}
	}
	walk(cp.Children)
	return edges
}

// box is an axis-aligned bounding rectangle in arbitrary local coordinates.
type box struct {
	x0, y0, x1, y1 float64
	valid          bool
}

func (b *box) addPoint(x, y float64) {
	if !b.valid {
		b.x0, b.x1 = x, x
		b.y0, b.y1 = y, y
		b.valid = true
		return
	}
	b.x0 = math.Min(b.x0, x)
	b.x1 = math.Max(b.x1, x)
	b.y0 = math.Min(b.y0, y)
	b.y1 = math.Max(b.y1, y)
}

func (b *box) addAxis(x0, x1, y0, y1 float64) {
	b.addPoint(x0, y0)
	b.addPoint(x1, y1)
}

func (b *box) include(other box) {
	if !other.valid {
		return
	}
	if !b.valid {
		*b = other
		return
	}
	b.x0 = math.Min(b.x0, other.x0)
	b.y0 = math.Min(b.y0, other.y0)
	b.x1 = math.Max(b.x1, other.x1)
	b.y1 = math.Max(b.y1, other.y1)
}

// elementGeometryPath builds the raw local-coordinate geometry for a leaf
// element. It returns an empty path for degenerate shapes and nil for nodes
// without fill geometry (groups).
func elementGeometryPath(e Element) *gg.Path {
	switch el := e.(type) {
	case *PathElement:
		if strings.TrimSpace(el.D) == "" {
			return gg.NewPath()
		}
		p, err := gg.ParseSVGPath(el.D)
		if err != nil {
			return nil
		}
		return p
	case *CircleElement:
		p := gg.NewPath()
		if el.R > 0 {
			p.Circle(el.CX, el.CY, el.R)
		}
		return p
	case *RectElement:
		p := gg.NewPath()
		if el.W <= 0 || el.H <= 0 {
			return p
		}
		if el.RX > 0 || el.RY > 0 {
			r := el.RX
			if el.RY > r {
				r = el.RY
			}
			p.RoundedRectangle(el.X, el.Y, el.W, el.H, r)
		} else {
			p.Rectangle(el.X, el.Y, el.W, el.H)
		}
		return p
	case *EllipseElement:
		p := gg.NewPath()
		if el.RX > 0 && el.RY > 0 {
			p.Ellipse(el.CX, el.CY, el.RX, el.RY)
		}
		return p
	case *LineElement:
		p := gg.NewPath()
		p.MoveTo(el.X1, el.Y1)
		p.LineTo(el.X2, el.Y2)
		return p
	case *PolygonElement:
		return pointsPath(el.Points, true)
	case *PolylineElement:
		return pointsPath(el.Points, false)
	}
	return nil
}

// groupLocalBox returns the union of descendant geometry bounds expressed in
// the group's local user space, accounting for child transforms.
func groupLocalBox(g *GroupElement) box {
	var b box
	var walk func(elems []Element, m gg.Matrix)
	walk = func(elems []Element, m gg.Matrix) {
		for _, e := range elems {
			full := m.Multiply(elementTransformMatrix(e.attrs()))
			if sub, ok := e.(*GroupElement); ok {
				walk(sub.Children, full)
				continue
			}
			if p := elementGeometryPath(e); p != nil {
				b.include(pathFloatBounds(p.Transform(full)))
			}
		}
	}
	walk(g.Children, gg.Identity())
	return b
}

// pathFloatBounds computes tight float64 bounds for a path, solving curve
// extrema.
func pathFloatBounds(p *gg.Path) box {
	var b box
	if p == nil {
		return b
	}
	var cx, cy, sx, sy float64
	havePen := false
	p.Iterate(func(v gg.PathVerb, c []float64) {
		switch v {
		case gg.MoveTo:
			cx, cy = c[0], c[1]
			sx, sy = cx, cy
			havePen = true
			b.addPoint(cx, cy)
		case gg.LineTo:
			cx, cy = c[0], c[1]
			b.addPoint(cx, cy)
		case gg.QuadTo:
			qx, qy, ex, ey := c[0], c[1], c[2], c[3]
			x0, x1 := scalarRange(cx, qx, ex, 0, 2)
			y0, y1 := scalarRange(cy, qy, ey, 0, 2)
			b.addAxis(x0, x1, y0, y1)
			cx, cy = ex, ey
		case gg.CubicTo:
			x1, y1, x2, y2, ex, ey := c[0], c[1], c[2], c[3], c[4], c[5]
			x0, xmax := scalarRange(cx, x1, x2, ex, 3)
			y0, ymax := scalarRange(cy, y1, y2, ey, 3)
			b.addAxis(x0, xmax, y0, ymax)
			cx, cy = ex, ey
		case gg.Close:
			if havePen {
				cx, cy = sx, sy
			}
		}
	})
	return b
}

// scalarRange returns the min/max of a quadratic (degree=2, using
// p0,p1,p2) or cubic (degree=3, using p0..p3) polynomial segment at t in
// [0,1], including the endpoints and interior derivative roots.
func scalarRange(p0, p1, p2, p3 float64, degree int) (float64, float64) {
	end := p3
	if degree == 2 {
		end = p2
	}
	lo := math.Min(p0, end)
	hi := math.Max(p0, end)
	consider := func(v float64) {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return
		}
		lo = math.Min(lo, v)
		hi = math.Max(hi, v)
	}
	if degree == 2 {
		// B'(t) = 2[(1-t)(p1-p0) + t(p2-p1)] = 0 -> B + t*A = 0.
		a := p2 - 2*p1 + p0
		bb := p1 - p0
		if math.Abs(a) > 1e-15 {
			t := -bb / a
			if t > 0 && t < 1 {
				mt := 1 - t
				consider(mt*mt*p0 + 2*mt*t*p1 + t*t*p2)
			}
		}
		return lo, hi
	}
	// Cubic derivative: a t^2 + b t + c = 0.
	c := p1 - p0
	bb := 2 * (p2 - 2*p1 + p0)
	a := p3 - 3*p2 + 3*p1 - p0
	roots := solveQuadratic(a, bb, c)
	for _, t := range roots {
		if t > 0 && t < 1 {
			mt := 1 - t
			consider(mt*mt*mt*p0 + 3*mt*mt*t*p1 + 3*mt*t*t*p2 + t*t*t*p3)
		}
	}
	return lo, hi
}

// solveQuadratic returns the real roots of a*t^2+b*t+c=0.
func solveQuadratic(a, b, c float64) []float64 {
	if math.Abs(a) < 1e-15 {
		if math.Abs(b) < 1e-15 {
			return nil
		}
		return []float64{-c / b}
	}
	disc := b*b - 4*a*c
	if disc < 0 {
		return nil
	}
	if disc == 0 {
		return []float64{-b / (2 * a)}
	}
	sq := math.Sqrt(disc)
	return []float64{(-b - sq) / (2 * a), (-b + sq) / (2 * a)}
}

// ruleHint records which explicit clip/fill rules the merged clip geometry
// declares.
type ruleHint struct {
	evenOdd bool
	nonZero bool
}

func (h *ruleHint) add(rule string) {
	switch normalizeRule(rule) {
	case gg.FillRuleEvenOdd:
		h.evenOdd = true
	case gg.FillRuleNonZero:
		h.nonZero = true
	}
}

func normalizeRule(v string) gg.FillRule {
	if strings.EqualFold(strings.TrimSpace(v), "evenodd") {
		return gg.FillRuleEvenOdd
	}
	return gg.FillRuleNonZero
}

func ruleName(v string) string {
	switch normalizeRule(v) {
	case gg.FillRuleEvenOdd:
		return "evenodd"
	default:
		return "nonzero"
	}
}

// clipRefCallback is invoked for each clip-path reference carried by a node
// inside a clipPath. pre maps that node's local user space into the current
// dc user space; target is the node's local bounding box.
type clipRefCallback func(rawRef string, pre gg.Matrix, target box, rule string)

// appendClipGeometry merges all geometry under elems into out, baking parent
// and element transforms. inheritedRule carries an explicit clip-rule
// through groups ("" when none was declared).
func appendClipGeometry(elems []Element, m gg.Matrix, out *gg.Path, hint *ruleHint, inheritedRule string, onRef clipRefCallback) {
	for _, e := range elems {
		a := e.attrs()
		rule := strings.TrimSpace(a.ClipRule)
		if rule == "" {
			rule = inheritedRule
		}
		full := m.Multiply(elementTransformMatrix(a))

		if g, ok := e.(*GroupElement); ok {
			if ref := strings.TrimSpace(a.ClipPathRef); ref != "" && onRef != nil {
				onRef(ref, full, groupLocalBox(g), rule)
			}
			appendClipGeometry(g.Children, full, out, hint, rule, onRef)
			continue
		}
		p := elementGeometryPath(e)
		if p == nil {
			continue
		}
		if ref := strings.TrimSpace(a.ClipPathRef); ref != "" && onRef != nil {
			onRef(ref, full, pathFloatBounds(p), rule)
		}
		if len(p.Verbs()) == 0 {
			continue
		}
		ownRule := strings.TrimSpace(a.ClipRule)
		if ownRule == "" {
			ownRule = strings.TrimSpace(a.FillRule) // hole paths often use fill-rule
		}
		if ownRule == "" {
			ownRule = inheritedRule
		}
		if ownRule != "" {
			hint.add(ownRule)
		}
		out.Append(p.Transform(full))
	}
}

// applyAttrClips applies the clip-path referenced by attrs to dc. target is
// the referencing element's bounding box in the current dc user space (used
// for objectBoundingBox units). It must be called inside a dc.Push scope;
// the pushed clips are removed by the matching dc.Pop.
func applyAttrClips(dc *gg.Context, clips map[string]*ClipPath, attrs *Attrs, target box) {
	if dc == nil || attrs == nil || clips == nil {
		return
	}
	raw := strings.TrimSpace(attrs.ClipPathRef)
	if raw == "" || strings.EqualFold(raw, "none") {
		return
	}
	id, ok := parseClipReference(raw)
	if !ok {
		return // Parse rejects this; defensive no-op only for hand-built docs.
	}
	forcedRule := ""
	if strings.TrimSpace(attrs.ClipRule) != "" {
		forcedRule = ruleName(attrs.ClipRule)
	}
	pushClipRef(dc, clips, id, target, forcedRule, map[string]bool{}, gg.Identity())
}

// pushClipRef pushes the region of one clipPath id, including nested
// clip-path references (intersection). forcedRule, when non-empty, overrides
// the definition/geometry rules. pre maps the referenced definition's user
// space into the current dc user space (identity for a direct attribute
// reference; an accumulated geometry transform for references carried by
// shapes inside another clipPath).
func pushClipRef(dc *gg.Context, clips map[string]*ClipPath, id string, target box, forcedRule string, chain map[string]bool, pre gg.Matrix) {
	cp, ok := clips[id]
	if !ok {
		// Parse guarantees resolution. For hand-built Documents an unknown
		// definition clips everything out rather than silently unclipping.
		dc.ClearPath()
		dc.Clip()
		return
	}
	if chain[id] {
		// Runtime cycle guard (Parse rejects cycles): clip all out.
		dc.ClearPath()
		dc.Clip()
		return
	}
	chain[id] = true
	defer delete(chain, id)

	// objectBoundingBox maps unit clip coordinates onto the target bbox.
	m := pre
	if cp.Units == "objectBoundingBox" {
		if !target.valid || target.x1 == target.x0 || target.y1 == target.y0 {
			// Degenerate bounding box: OBB clipping is disabled, matching
			// SVG behavior for zero-area target geometry.
			return
		}
		obb := gg.Translate(target.x0, target.y0).
			Multiply(gg.Scale(target.x1-target.x0, target.y1-target.y0))
		m = m.Multiply(obb)
	}
	if t := strings.TrimSpace(cp.Attrs.Transform); t != "" {
		if tm, err := transformMatrix(t); err == nil && matrixFinite(tm) {
			m = m.Multiply(tm)
		}
	}

	// A clipPath element may itself carry clip-path: regions intersect.
	if nested := strings.TrimSpace(cp.Attrs.ClipPathRef); nested != "" && !strings.EqualFold(nested, "none") {
		if nid, ok := parseClipReference(nested); ok {
			pushClipRef(dc, clips, nid, target, "", chain, pre)
		}
	}

	merged := gg.NewPath()
	hint := &ruleHint{}
	appendClipGeometry(cp.Children, m, merged, hint, strings.TrimSpace(cp.Attrs.ClipRule),
		func(rawRef string, shapePre gg.Matrix, shapeBox box, rule string) {
			nid, ok := parseClipReference(rawRef)
			if !ok {
				return
			}
			pushClipRef(dc, clips, nid, shapeBox, rule, chain, shapePre)
		})

	rule := gg.FillRuleNonZero
	switch {
	case forcedRule != "":
		rule = normalizeRule(forcedRule)
	case strings.TrimSpace(cp.Attrs.ClipRule) != "":
		rule = normalizeRule(cp.Attrs.ClipRule)
	case hint.evenOdd && !hint.nonZero:
		rule = gg.FillRuleEvenOdd
	}

	dc.SetFillRule(rule)
	if len(merged.Verbs()) == 0 {
		// Empty clipPath hides the clipped content.
		dc.ClearPath()
		dc.Clip()
		return
	}
	dc.DrawPath(merged)
	dc.Clip()
}

// matrixFinite reports whether every matrix entry is finite.
func matrixFinite(m gg.Matrix) bool {
	for _, v := range []float64{m.A, m.B, m.C, m.D, m.E, m.F} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

package svg

import (
	"math"
	"sort"

	"github.com/gogpu/gg"
)

// floatBounds is a precise float64 bounding box in element-local user space.
type floatBounds struct {
	minX, minY, maxX, maxY float64
}

func (b floatBounds) empty() bool {
	return b.maxX < b.minX || b.maxY < b.minY
}

// pathFloatBounds returns the control-point bounding box of p. For the shape
// primitives gg emits (rect, circle/ellipse kappa curves, rounded rects) the
// control extrema equal the geometry extrema, which is exactly the object
// bounding box fixtures use. For arbitrary paths this is the conservative
// outward bbox, matching the common renderer fallback.
func pathFloatBounds(p *gg.Path) (floatBounds, bool) {
	if p == nil || len(p.Verbs()) == 0 {
		return floatBounds{}, false
	}
	coords := p.Coords()
	b := floatBounds{
		minX: math.Inf(1), minY: math.Inf(1),
		maxX: math.Inf(-1), maxY: math.Inf(-1),
	}
	any := false
	for i := 0; i+1 < len(coords); i += 2 {
		x, y := coords[i], coords[i+1]
		if !allFinite(x, y) {
			continue
		}
		any = true
		b.minX = math.Min(b.minX, x)
		b.maxX = math.Max(b.maxX, x)
		b.minY = math.Min(b.minY, y)
		b.maxY = math.Max(b.maxY, y)
	}
	if !any {
		return floatBounds{}, false
	}
	return b, true
}

// gradientBrush evaluates an SVG gradient at device pixel centers. The
// renderer asks for colors in device space; sampleMatrix maps a device point
// all the way back into the gradient's own geometry space:
//
//	device --inv(CTM)--> local user --inv(bbox)--> object units
//	         --inv(gradientTransform)--> gradient geometry
type gradientBrush struct {
	g            *Gradient
	sampleMatrix gg.Matrix
	alphaMul     float64
}

// colorAt is a gg.ColorFunc-compatible sampler.
func (b *gradientBrush) colorAt(x, y float64) gg.RGBA {
	if b == nil || b.g == nil || len(b.g.Stops) == 0 {
		return gg.Transparent
	}
	p := b.sampleMatrix.TransformPoint(gg.Point{X: x, Y: y})
	if !allFinite(p.X, p.Y) {
		return b.g.Stops[0].Color
	}

	var t float64
	if b.g.Kind == LinearGradient {
		t = linearT(b.g, p.X, p.Y)
	} else {
		t = radialT(b.g, p.X, p.Y)
	}
	if math.IsNaN(t) || math.IsInf(t, 0) {
		// Stable degradation for numerically impossible samples.
		t = 0
	}
	t = applySpread(t, b.g.Spread)
	c := sampleGradientStops(b.g.Stops, t)
	c.A = clamp01Safe(c.A * b.alphaMul)
	return c
}

// buildGradientBrush constructs the device-space brush for g painted by an
// element whose CTM from local geometry to device pixels is ctm.
//
// viewBox is used to resolve percentage lengths for userSpaceOnUse gradients.
func buildGradientBrush(g *Gradient, ctm gg.Matrix, bounds floatBounds, vb ViewBox, alphaMul float64) gg.Brush {
	if g == nil || len(g.Stops) == 0 {
		return nil
	}

	// solid returns the explicit flat-color degradation used for degenerate
	// geometry (zero bbox, non-invertible transforms, zero-length vectors).
	solid := func(c gg.RGBA) gg.Brush {
		return gg.NewCustomBrush(func(_, _ float64) gg.RGBA {
			c.A = clamp01Safe(c.A * alphaMul)
			return c
		})
	}

	// Degenerate geometry is a flat final-stop color regardless of spread.
	if g.Degenerate {
		return solid(g.Stops[len(g.Stops)-1].Color)
	}

	invCTM, ok := safeInvert(ctm)
	if !ok {
		return solid(g.Stops[len(g.Stops)-1].Color)
	}

	geom := g
	if g.Units == UserSpaceOnUse {
		geom = materializeUserSpace(g, vb)
	}

	var toGeometry gg.Matrix
	if g.Units == ObjectBoundingBox {
		if bounds.empty() {
			return solid(g.Stops[len(g.Stops)-1].Color)
		}
		bw := bounds.maxX - bounds.minX
		bh := bounds.maxY - bounds.minY
		if !allFinite(bounds.minX, bounds.minY, bw, bh) ||
			math.Abs(bw) > maxGradientValue*1e3 || math.Abs(bh) > maxGradientValue*1e3 {
			return solid(g.Stops[0].Color)
		}
		// Pseudo-inverse of the unit-square -> bbox mapping. A zero-sized
		// axis (a horizontal/vertical line's bbox) maps to unit coordinate
		// 0.5 instead of making the whole matrix singular, so a gradient
		// along the surviving axis keeps varying.
		sx, ox := 0.0, 0.5
		if bw > 0 {
			sx, ox = 1/bw, -bounds.minX/bw
		}
		sy, oy := 0.0, 0.5
		if bh > 0 {
			sy, oy = 1/bh, -bounds.minY/bh
		}
		invBBox := gg.Matrix{A: sx, B: 0, C: ox, D: 0, E: sy, F: oy}
		invGT, gok := safeInvert(g.Transform)
		if !gok {
			invGT = gg.Identity()
		}
		// geom = invGT * invBBox * invCTM * device
		toGeometry = invGT.Multiply(invBBox).Multiply(invCTM)
	} else {
		invGT, gok := safeInvert(g.Transform)
		if !gok {
			invGT = gg.Identity()
		}
		// geom = invGT * invCTM * device
		toGeometry = invGT.Multiply(invCTM)
	}

	gb := &gradientBrush{g: geom, sampleMatrix: toGeometry, alphaMul: alphaMul}
	return gg.NewCustomBrush(gb.colorAt)
}

// lenValue returns a materialized length's coordinate value.
func lenValue(l GradientLength) float64 {
	if !l.Set {
		return 0
	}
	return l.Value
}

// linearT computes the un-spread linear gradient parameter at (x,y).
func linearT(g *Gradient, x, y float64) float64 {
	x1, y1 := lenValue(g.X1), lenValue(g.Y1)
	x2, y2 := lenValue(g.X2), lenValue(g.Y2)
	dx := x2 - x1
	dy := y2 - y1
	lenSq := dx*dx + dy*dy
	if !(lenSq > 0) {
		return 1 // degenerate callers normally get the solid brush directly
	}
	return ((x-x1)*dx + (y-y1)*dy) / lenSq
}

// materializeUserSpace returns a copy of g with percentage geometry resolved
// against the SVG viewport (viewBox) into absolute user units.
func materializeUserSpace(g *Gradient, vb ViewBox) *Gradient {
	cp := *g
	w := vb.Width
	h := vb.Height
	if !(w > 0) {
		w = 1
	}
	if !(h > 0) {
		h = 1
	}
	// SVG normalized diagonal reference for radial percentage radii.
	diag := math.Sqrt(w*w+h*h) / math.Sqrt2

	px := func(l GradientLength) GradientLength {
		if l.Percent {
			l.Value = vb.MinX + l.Value*w
			l.Percent = false
		}
		return l
	}
	py := func(l GradientLength) GradientLength {
		if l.Percent {
			l.Value = vb.MinY + l.Value*h
			l.Percent = false
		}
		return l
	}
	pr := func(l GradientLength) GradientLength {
		if l.Percent {
			l.Value = l.Value * diag
			l.Percent = false
		}
		return l
	}

	cp.X1, cp.X2 = px(g.X1), px(g.X2)
	cp.Y1, cp.Y2 = py(g.Y1), py(g.Y2)
	cp.CX, cp.FX = px(g.CX), px(g.FX)
	cp.CY, cp.FY = py(g.CY), py(g.FY)
	cp.R, cp.FR = pr(g.R), pr(g.FR)
	return &cp
}

// radialParams holds resolved radial geometry in gradient space.
type radialParams struct {
	cx, cy, r    float64
	fx, fy, fr   float64
	fdx, fdy, dd float64 // center - focus
}

func radialParameters(g *Gradient) radialParams {
	cx, cy := lenValue(g.CX), lenValue(g.CY)
	r := max(0, lenValue(g.R))
	fr := max(0, lenValue(g.FR))
	fx, fy := cx, cy
	if g.FX.Set {
		fx = lenValue(g.FX)
	}
	if g.FY.Set {
		fy = lenValue(g.FY)
	}

	// Clamp the focal circle inside the end circle (SVG focal clamping).
	dx := fx - cx
	dy := fy - cy
	dist := math.Hypot(dx, dy)
	maxDist := math.Max(0, r-fr)
	if dist > maxDist && dist > 0 && allFinite(dist) {
		scale := maxDist / dist
		fx = cx + dx*scale
		fy = cy + dy*scale
	}
	return radialParams{
		cx: cx, cy: cy, r: r, fr: fr,
		fx: fx, fy: fy,
		fdx: cx - fx, fdy: cy - fy,
		dd: (cx-fx)*(cx-fx) + (cy-fy)*(cy-fy),
	}
}

// radialT computes the un-spread radial gradient parameter using the
// two-circle interpolation quadratic from the SVG specification.
func radialT(g *Gradient, x, y float64) float64 {
	p := radialParameters(g)
	if !(p.r > p.fr) || g.Degenerate && p.r == 0 {
		// Equal radii / zero radius: flat final stop.
		return 1
	}

	// Vector from focal center to sample point.
	ax := x - p.fx
	ay := y - p.fy

	a := p.dd - (p.r-p.fr)*(p.r-p.fr)
	b := -2 * (ax*p.fdx + ay*p.fdy + p.fr*(p.r-p.fr))
	c := ax*ax + ay*ay - p.fr*p.fr

	if math.Abs(a) < 1e-18*math.Max(1, math.Abs(p.dd)) {
		// Linear case: b*t + c = 0.
		if math.Abs(b) < 1e-18 {
			return 0
		}
		return -c / b
	}

	disc := b*b - 4*a*c
	if disc < 0 {
		// No interpolated circle reaches the point: treat as outside.
		return 1
	}
	sq := math.Sqrt(math.Max(0, disc))
	t1 := (-b - sq) / (2 * a)
	t2 := (-b + sq) / (2 * a)

	// Prefer the first non-negative root along the focal ray.
	const eps = 1e-12
	candidates := make([]float64, 0, 2)
	if t1 >= -eps && allFinite(t1) {
		candidates = append(candidates, max(0, t1))
	}
	if t2 >= -eps && allFinite(t2) {
		candidates = append(candidates, max(0, t2))
	}
	if len(candidates) == 0 {
		// Both roots behind the focus: return the larger one so spread modes
		// still receive a defined negative-ish parameter.
		return math.Max(t1, t2)
	}
	smallest := candidates[0]
	for _, t := range candidates[1:] {
		if t < smallest {
			smallest = t
		}
	}
	return smallest
}

// applySpread maps t into [0,1] according to the SVG spread method.
func applySpread(t float64, mode SpreadMethod) float64 {
	switch mode {
	case SpreadRepeat:
		t = math.Mod(t, 1)
		if t < 0 {
			t++
		}
		if t >= 1 {
			t = 0
		}
		return t
	case SpreadReflect:
		q := math.Mod(t, 2)
		if q < 0 {
			q += 2
		}
		if q > 1 {
			q = 2 - q
		}
		return q
	default:
		return clamp01Safe(t)
	}
}

// sampleGradientStops evaluates the stop ramp at t in [0,1] using
// premultiplied-alpha linear interpolation in sRGB space, matching SVG and
// the common tiny-skia/Cairo behavior for transparent stops.
func sampleGradientStops(stops []GradientStop, t float64) gg.RGBA {
	n := len(stops)
	if n == 0 {
		return gg.Transparent
	}
	if n == 1 {
		return stops[0].Color
	}
	if t <= stops[0].Offset {
		return stops[0].Color
	}
	if t >= stops[n-1].Offset {
		return stops[n-1].Color
	}

	idx := sort.Search(n, func(i int) bool { return stops[i].Offset >= t })
	if idx <= 0 {
		return stops[0].Color
	}
	if idx >= n {
		return stops[n-1].Color
	}
	s1 := stops[idx-1]
	s2 := stops[idx]
	span := s2.Offset - s1.Offset
	if span <= offsetEpsilon {
		// Coincident stops: deterministic tie rule — the incoming (first)
		// stop owns the exact boundary; samples just past it reach the
		// later stop through the following segment.
		return s1.Color
	}
	lt := (t - s1.Offset) / span
	if lt < 0 {
		lt = 0
	} else if lt > 1 {
		lt = 1
	}

	// Premultiply, lerp, unpremultiply.
	a1 := s1.Color.A
	a2 := s2.Color.A
	pr1, pg1, pb1 := s1.Color.R*a1, s1.Color.G*a1, s1.Color.B*a1
	pr2, pg2, pb2 := s2.Color.R*a2, s2.Color.G*a2, s2.Color.B*a2

	pa := a1 + (a2-a1)*lt
	if pa <= 1e-6 {
		return gg.RGBA{R: 0, G: 0, B: 0, A: 0}
	}
	pr := pr1 + (pr2-pr1)*lt
	pg := pg1 + (pg2-pg1)*lt
	pb := pb1 + (pb2-pb1)*lt
	return gg.RGBA{
		R: clamp01Safe(pr / pa),
		G: clamp01Safe(pg / pa),
		B: clamp01Safe(pb / pa),
		A: clamp01Safe(pa),
	}
}

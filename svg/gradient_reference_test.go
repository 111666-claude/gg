package svg

// Independent sampling reference for SVG gradients.
//
// The reference math in this file is deliberately self-contained (its own
// affine helpers, stop interpolation, spread and radial solver). It does not
// call the production gradient sampler, so agreement between rendered fixture
// pixels and these values is a genuine cross-check rather than a tautology.

import (
	"image"
	"math"
	"os"
	"path/filepath"
	"testing"
)

type refRGBA struct{ r, g, b, a float64 } // all in [0,1], straight alpha

func refHex(h uint32) refRGBA {
	return refRGBA{
		r: float64((h>>16)&255) / 255,
		g: float64((h>>8)&255) / 255,
		b: float64(h&255) / 255,
		a: 1,
	}
}

func refClamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

// refRamp samples a stop ramp at t in [0,1] with premultiplied-alpha linear
// interpolation (SVG/tiny-skia convention).
func refRamp(stops []struct {
	at float64
	c  refRGBA
}, t float64) refRGBA {
	if len(stops) == 1 {
		return stops[0].c
	}
	if t <= stops[0].at {
		return stops[0].c
	}
	last := stops[len(stops)-1]
	if t >= last.at {
		return last.c
	}
	for i := 1; i < len(stops); i++ {
		if t <= stops[i].at {
			l, r := stops[i-1], stops[i]
			u := (t - l.at) / (r.at - l.at)
			pa := l.c.a + (r.c.a-l.c.a)*u
			if pa <= 1e-6 {
				return refRGBA{a: 0}
			}
			pr := l.c.r*l.c.a + (r.c.r*r.c.a-l.c.r*l.c.a)*u
			pg := l.c.g*l.c.a + (r.c.g*r.c.a-l.c.g*l.c.a)*u
			pb := l.c.b*l.c.a + (r.c.b*r.c.a-l.c.b*l.c.a)*u
			return refRGBA{r: refClamp01(pr / pa), g: refClamp01(pg / pa), b: refClamp01(pb / pa), a: pa}
		}
	}
	return last.c
}

func refOver(src, dst refRGBA) refRGBA {
	ia := 1 - src.a
	return refRGBA{
		r: src.r*src.a + dst.r*dst.a*ia,
		g: src.g*src.a + dst.g*dst.a*ia,
		b: src.b*src.a + dst.b*dst.a*ia,
		a: src.a + dst.a*ia,
	}
}

func refSpread(t float64, mode int) float64 {
	switch mode {
	case 1: // repeat
		t = math.Mod(t, 1)
		if t < 0 {
			t++
		}
		return t
	case 2: // reflect
		q := math.Mod(t, 2)
		if q < 0 {
			q += 2
		}
		if q > 1 {
			q = 2 - q
		}
		return q
	default:
		return refClamp01(t)
	}
}

// refRadialT independently solves the two-circle interpolation equation.
func refRadialT(px, py, cx, cy, r, fx, fy, fr float64) float64 {
	dx := fx - cx
	dy := fy - cy
	if d := math.Hypot(dx, dy); d > r-fr && d > 0 {
		s := (r - fr) / d
		fx = cx + dx*s
		fy = cy + dy*s
	}
	ax := px - fx
	ay := py - fy
	ddx := cx - fx
	ddy := cy - fy
	aa := ddx*ddx + ddy*ddy - (r-fr)*(r-fr)
	bb := -2 * (ax*ddx + ay*ddy + fr*(r-fr))
	cc := ax*ax + ay*ay - fr*fr
	if math.Abs(aa) < 1e-18 {
		if math.Abs(bb) < 1e-18 {
			return 0
		}
		return -cc / bb
	}
	disc := bb*bb - 4*aa*cc
	if disc < 0 {
		return 1
	}
	sq := math.Sqrt(disc)
	t1 := (-bb - sq) / (2 * aa)
	t2 := (-bb + sq) / (2 * aa)
	best := math.Inf(1)
	for _, t := range []float64{t1, t2} {
		if t >= -1e-9 && t < best {
			best = math.Max(0, t)
		}
	}
	if math.IsInf(best, 1) {
		return math.Max(t1, t2)
	}
	return best
}

// refAffine is an independent 2D affine transform (column-vector convention
// matching SVG: x'=a*x+c*y+e).
type refAffine struct{ a, b, c, d, e, f float64 }

func refRotate(rad float64, cx, cy float64) refAffine {
	k := math.Cos(rad)
	s := math.Sin(rad)
	// translate(cx,cy) * rotate * translate(-cx,-cy)
	return refAffine{
		a: k, c: -s, e: cx - k*cx + s*cy,
		b: s, d: k, f: cy - s*cx - k*cy,
	}
}

func (m refAffine) inverseApply(x, y float64) (float64, float64) {
	det := m.a*m.d - m.c*m.b
	ia := m.d / det
	ic := -m.c / det
	ie := (m.c*m.f - m.e*m.d) / det
	ib := -m.b / det
	id := m.a / det
	jf := (m.e*m.b - m.a*m.f) / det
	return ia*x + ic*y + ie, ib*x + id*y + jf
}

type refStop = struct {
	at float64
	c  refRGBA
}

type sampleReport struct {
	name      string
	checked   int
	maxDiff   int
	sumDiff   int64
	worstX    int
	worstY    int
	worstGot  [4]uint32
	worstWant [4]int
}

func (r *sampleReport) observe(x, y int, got [4]byte, want refRGBA) {
	w := [4]int{
		int(math.Round(want.r * 255)),
		int(math.Round(want.g * 255)),
		int(math.Round(want.b * 255)),
		int(math.Round(want.a * 255)),
	}
	mx := 0
	var sum int
	for i := 0; i < 4; i++ {
		d := int(got[i]) - w[i]
		if d < 0 {
			d = -d
		}
		sum += d
		if d > mx {
			mx = d
		}
	}
	r.checked++
	r.sumDiff += int64(sum)
	if mx > r.maxDiff {
		r.maxDiff = mx
		r.worstX, r.worstY = x, y
		r.worstGot = [4]uint32{uint32(got[0]), uint32(got[1]), uint32(got[2]), uint32(got[3])}
		r.worstWant = w
	}
}

func renderFixture(t *testing.T, name string) (*image.RGBA, []byte) {
	t.Helper()
	path := filepath.Join("testdata", "gradient", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("fixture %s parse: %v", name, err)
	}
	b := doc.ViewBox
	img := doc.Render(int(b.Width), int(b.Height))
	return img, data
}

func pixelAt(img *image.RGBA, x, y int) [4]byte {
	i := img.PixOffset(x, y)
	return [4]byte{img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3]}
}

// TestGradientFixturesAgainstIndependentReference renders every required
// fixture category and compares interior pixels against independently derived
// expected colors.
func TestGradientFixturesAgainstIndependentReference(t *testing.T) {
	t.Run("linear_rect", func(t *testing.T) {
		img, _ := renderFixture(t, "linear_rect.svg")
		stops := []refStop{
			{0, refHex(0xff0000)}, {0.5, refHex(0x00ff00)}, {1, refHex(0x0000ff)},
		}
		rep := &sampleReport{name: "linear_rect"}
		for y := 12; y < 68; y++ {
			for x := 12; x < 108; x++ {
				ux := float64(x) + 0.5
				tv := refClamp01((ux - 10) / 100)
				rep.observe(x, y, pixelAt(img, x, y), refRamp(stops, tv))
			}
		}
		assertReport(t, rep, 4, 1.0)
	})

	t.Run("radial_circle_focus", func(t *testing.T) {
		img, _ := renderFixture(t, "radial_circle.svg")
		stops := []refStop{
			{0, refHex(0xffffff)}, {0.55, refHex(0x808080)}, {1, refHex(0x000000)},
		}
		const cx, cy, r = 60.0, 60.0, 48.0
		const fx, fy, fr = 45.6, 45.6, 0.0 // bbox 12..108, .35 focus
		rep := &sampleReport{name: "radial"}
		for y := 14; y < 106; y++ {
			for x := 14; x < 106; x++ {
				ux, uy := float64(x)+0.5, float64(y)+0.5
				if math.Hypot(ux-cx, uy-cy) > r-3 {
					continue // erode edge against coverage differences
				}
				tv := refSpread(refRadialT(ux, uy, cx, cy, r, fx, fy, fr), 0)
				rep.observe(x, y, pixelAt(img, x, y), refRamp(stops, tv))
			}
		}
		// Radial interpolation across many 8-bit bands accumulates rounding.
		assertReport(t, rep, 4, 1.0)
	})

	t.Run("stroke_circle", func(t *testing.T) {
		img, _ := renderFixture(t, "stroke_circle.svg")
		stops := []refStop{{0, refHex(0xff2020)}, {1, refHex(0x2040ff)}}
		rep := &sampleReport{name: "stroke"}
		for y := 8; y < 112; y++ {
			for x := 8; x < 112; x++ {
				ux, uy := float64(x)+0.5, float64(y)+0.5
				d := math.Hypot(ux-60, uy-60)
				if d < 34 || d > 46 { // band is 32..48; erode 2px
					continue
				}
				tv := refClamp01((ux - 20) / 80) // geometry bbox [20,100]
				rep.observe(x, y, pixelAt(img, x, y), refRamp(stops, tv))
			}
		}
		assertReport(t, rep, 4, 1.0)
	})

	t.Run("rotate_group_and_gradientTransform", func(t *testing.T) {
		img, _ := renderFixture(t, "rotate_rect.svg")
		stops := []refStop{{0, refHex(0xff8800)}, {1, refHex(0x0088ff)}}
		group := refRotate(30*math.Pi/180, 60, 60)
		rep := &sampleReport{name: "rotate"}
		for y := 0; y < 120; y++ {
			for x := 0; x < 120; x++ {
				ux, uy := float64(x)+0.5, float64(y)+0.5
				lx, ly := group.inverseApply(ux, uy) // root -> rect local
				if lx < 22 || lx > 98 || ly < 42 || ly > 78 {
					continue // inside 80x40 rect with 2px erosion
				}
				// gradientTransform rotate(90) turns the horizontal vector
				// into a top-to-bottom one in rect space.
				tv := refClamp01((ly - 40) / 40)
				rep.observe(x, y, pixelAt(img, x, y), refRamp(stops, tv))
			}
		}
		assertReport(t, rep, 4, 1.0)
	})

	t.Run("transparent_multilayer_stops", func(t *testing.T) {
		img, _ := renderFixture(t, "transparent_stops.svg")
		bg := refHex(0xf0f0f0)
		stops := []refStop{
			{0, withAlpha(refHex(0xff0000), 0.9)},
			{0.5, withAlpha(refHex(0x00ff00), 0.15)},
			{1, withAlpha(refHex(0x0000ff), 0.8)},
		}
		const cx, cy, r = 60.0, 60.0, 46.0
		rep := &sampleReport{name: "transparent"}
		for y := 16; y < 104; y++ {
			for x := 16; x < 104; x++ {
				ux, uy := float64(x)+0.5, float64(y)+0.5
				if math.Hypot(ux-cx, uy-cy) > r-3 {
					continue
				}
				tv := refClamp01((uy - 14) / 92) // bbox [14,106]
				src := refRamp(stops, tv)
				src.a *= 0.9 // fill-opacity
				want := refOver(src, bg)
				want.a = 1
				rep.observe(x, y, pixelAt(img, x, y), want)
			}
		}
		assertReport(t, rep, 4, 1.0)
	})

	t.Run("userSpaceOnUse", func(t *testing.T) {
		img, _ := renderFixture(t, "userspace_linear.svg")
		stops := []refStop{{0, refHex(0x111111)}, {1, refHex(0xffcc00)}}
		rep := &sampleReport{name: "userSpace"}
		for y := 22; y < 98; y++ {
			for x := 22; x < 98; x++ {
				ux, uy := float64(x)+0.5, float64(y)+0.5
				tv := refClamp01(((ux - 20) + (uy - 20)) / 160)
				rep.observe(x, y, pixelAt(img, x, y), refRamp(stops, tv))
			}
		}
		assertReport(t, rep, 4, 1.0)
	})

	t.Run("spread_repeat_with_href", func(t *testing.T) {
		img, _ := renderFixture(t, "spread_href.svg")
		stops := []refStop{{0, refHex(0x00aaaa)}, {1, refHex(0xaaaa00)}}
		rep := &sampleReport{name: "spread"}
		for y := 3; y < 27; y++ { // repeating top rect
			for x := 1; x < 119; x++ {
				ux := float64(x) + 0.5
				tv := refSpread(ux/30, 1)
				rep.observe(x, y, pixelAt(img, x, y), refRamp(stops, tv))
			}
		}
		for y := 33; y < 57; y++ { // padded bottom rect
			for x := 1; x < 119; x++ {
				ux := float64(x) + 0.5
				tv := refSpread(ux/30, 0)
				rep.observe(x, y, pixelAt(img, x, y), refRamp(stops, tv))
			}
		}
		assertReport(t, rep, 4, 1.0)
	})
}

func withAlpha(c refRGBA, a float64) refRGBA { c.a = a; return c }

func assertReport(t *testing.T, r *sampleReport, maxTol int, meanTol float64) {
	t.Helper()
	if r.checked == 0 {
		t.Fatalf("%s: no interior samples checked", r.name)
	}
	mean := float64(r.sumDiff) / float64(r.checked*4)
	if r.maxDiff > maxTol || mean > meanTol {
		t.Errorf("%s: checked=%d maxChannelDiff=%d (tol %d) mean=%.2f (tol %.1f); worst at (%d,%d) got=%v want=%v",
			r.name, r.checked, r.maxDiff, maxTol, mean, meanTol,
			r.worstX, r.worstY, r.worstGot, r.worstWant)
		return
	}
	t.Logf("%s: %d samples, max diff %d, mean %.3f", r.name, r.checked, r.maxDiff, mean)
}

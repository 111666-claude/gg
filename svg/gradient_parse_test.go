package svg

import (
	"errors"
	"strings"
	"testing"
)

func joinErrors(err error) []*GradientError {
	var out []*GradientError
	if err == nil {
		return out
	}
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		for _, e := range joined.Unwrap() {
			var ge *GradientError
			if errors.As(e, &ge) {
				out = append(out, ge)
			}
		}
		return out
	}
	var ge *GradientError
	if errors.As(err, &ge) {
		out = append(out, ge)
	}
	return out
}

func kindsAndPaths(t *testing.T, svg string) (map[string]*GradientError, *Document) {
	t.Helper()
	doc, err := Parse([]byte(svg))
	if err == nil {
		t.Fatalf("expected a validation error, got nil")
	}
	issues := joinErrors(err)
	if len(issues) == 0 {
		t.Fatalf("error %v contains no *GradientError", err)
	}
	byKind := make(map[string]*GradientError, len(issues))
	for _, ge := range issues {
		byKind[ge.Kind] = ge
		if ge.Path == "" || !strings.HasPrefix(ge.Path, "/svg") {
			t.Errorf("issue %q has no element path rooted at /svg", ge.Error())
		}
	}
	return byKind, doc
}

const validGradientSVGHeader = `<svg xmlns="http://www.w3.org/2000/svg" width="100" height="100" viewBox="0 0 100 100">`

func TestParseGradientStopsAndAttributes(t *testing.T) {
	src := []byte(validGradientSVGHeader + `
<defs>
<linearGradient id="g" gradientUnits="userSpaceOnUse" spreadMethod="repeat" gradientTransform="translate(2 3) rotate(45)">
<stop offset="0%" stop-color="#ff0000" stop-opacity="0.5"/>
<stop offset="1" stop-color="rgb(0, 255, 0)"/>
</linearGradient>
</defs>
<rect width="10" height="10" fill="url(#g)"/>
</svg>`)
	doc, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	g, ok := doc.Gradient("g")
	if !ok {
		t.Fatal("gradient g not indexed")
	}
	if g.Kind != LinearGradient {
		t.Errorf("Kind = %v, want linear", g.Kind)
	}
	if g.Units != UserSpaceOnUse {
		t.Errorf("Units = %v, want userSpaceOnUse", g.Units)
	}
	if g.Spread != SpreadRepeat {
		t.Errorf("Spread = %v, want repeat", g.Spread)
	}
	if g.Transform.C != 2 || g.Transform.F != 3 {
		t.Errorf("gradientTransform translation = (%v,%v), want (2,3)", g.Transform.C, g.Transform.F)
	}
	if len(g.Stops) != 2 {
		t.Fatalf("stops = %d, want 2", len(g.Stops))
	}
	if g.Stops[0].Offset != 0 || g.Stops[1].Offset != 1 {
		t.Errorf("offsets = %v,%v", g.Stops[0].Offset, g.Stops[1].Offset)
	}
	if a := g.Stops[0].Color.A; a < 0.49 || a > 0.51 {
		t.Errorf("stop-opacity not applied: A=%v, want 0.5", a)
	}
	if g.Stops[1].Color.G < 0.99 {
		t.Errorf("rgb stop not parsed: %+v", g.Stops[1].Color)
	}
}

func TestParseRadialGradientFocus(t *testing.T) {
	doc, err := Parse([]byte(validGradientSVGHeader + `
<radialGradient id="r" cx="30%" cy="40%" r="25%" fx="10%" fy="20%" fr="5%">
<stop offset="0" stop-color="#fff"/>
<stop offset="1" stop-color="#000"/>
</radialGradient>
<circle cx="50" cy="50" r="50" fill="url(#r)"/></svg>`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	g, ok := doc.Gradient("r")
	if !ok {
		t.Fatal("missing r")
	}
	if g.Kind != RadialGradient {
		t.Fatal("not radial")
	}
	if g.CX.Value != 0.3 || g.CY.Value != 0.4 || g.R.Value != 0.25 {
		t.Errorf("cx/cy/r = %v %v %v", g.CX.Value, g.CY.Value, g.R.Value)
	}
	if g.FX.Value != 0.1 || g.FY.Value != 0.2 || g.FR.Value != 0.05 {
		t.Errorf("fx/fy/fr = %v %v %v", g.FX.Value, g.FY.Value, g.FR.Value)
	}
}

func TestGradientHrefInheritanceAndXLink(t *testing.T) {
	src := []byte(`<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="10" height="10">
<defs>
<linearGradient id="base" x1="0" y1="0" x2="1" y2="0">
<stop offset="0" stop-color="red"/><stop offset="1" stop-color="blue"/>
</linearGradient>
<linearGradient id="child" xlink:href="#base" spreadMethod="reflect"/>
<radialGradient id="rc" href="#base"/>
</defs>
<rect width="10" height="10" fill="url(#child)"/>
</svg>`)
	doc, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	child, ok := doc.Gradient("child")
	if !ok {
		t.Fatal("child missing")
	}
	if child.Spread != SpreadReflect {
		t.Error("child spread not resolved")
	}
	if child.X2.Value != 1 || len(child.Stops) != 2 {
		t.Errorf("href attributes/stops not inherited: x2=%v stops=%d", child.X2.Value, len(child.Stops))
	}
	rc, ok := doc.Gradient("rc")
	if !ok || rc.Kind != RadialGradient {
		t.Fatal("radial href wrapper missing/wrong kind")
	}
	if len(rc.Stops) != 2 {
		t.Error("radial wrapper did not inherit linear stops")
	}
}

func TestMissingReferenceReportsElementPath(t *testing.T) {
	src := validGradientSVGHeader + `
<rect x="1" y="1" width="10" height="10" fill="url(#does-not-exist)"/>
<circle cx="50" cy="50" r="10" stroke="url(#also-missing)" stroke-width="2"/>
</svg>`
	byKind, doc := kindsAndPaths(t, src)
	if byKind["missing-reference"] == nil {
		t.Fatal("no missing-reference issue")
	}
	_, perr := Parse([]byte(src))
	var sawRect, sawCircle bool
	for _, ge := range joinErrors(perr) {
		if ge.Kind != "missing-reference" {
			continue
		}
		if strings.Contains(ge.Path, "rect[1]") {
			sawRect = true
		}
		if strings.Contains(ge.Path, "circle[1]") {
			sawCircle = true
		}
	}
	if !sawRect || !sawCircle {
		t.Errorf("missing refs must name rect[1] and circle[1], rect=%v circle=%v", sawRect, sawCircle)
	}
	// Document must still render with explicit degradation.
	img := doc.Render(100, 100)
	if img == nil {
		t.Fatal("nil render despite degradation")
	}
}

func TestCyclicHref(t *testing.T) {
	src := validGradientSVGHeader + `
<defs>
<linearGradient id="a" href="#b"><stop offset="0" stop-color="red"/></linearGradient>
<linearGradient id="b" href="#a"/>
</defs>
<rect width="10" height="10" fill="url(#a)"/></svg>`
	byKind, doc := kindsAndPaths(t, src)
	if byKind["cyclic-href"] == nil {
		t.Fatalf("expected cyclic-href, got %#v", byKind)
	}
	if doc == nil {
		t.Fatal("document should still be returned for degradation")
	}
	_ = doc.Render(20, 20) // must not hang or panic
}

func TestMissingHrefTarget(t *testing.T) {
	src := validGradientSVGHeader + `
<defs><linearGradient id="a" href="#ghost"/></defs>
<rect width="10" height="10" fill="url(#a)"/></svg>`
	byKind, _ := kindsAndPaths(t, src)
	if byKind["missing-href"] == nil {
		t.Fatalf("expected missing-href issue, got %#v", byKind)
	}
}

func TestNegativeRadius(t *testing.T) {
	src := validGradientSVGHeader + `
<radialGradient id="r" cx="5" cy="5" r="-4">
<stop offset="0" stop-color="white"/><stop offset="1" stop-color="black"/>
</radialGradient>
<circle cx="50" cy="50" r="40" fill="url(#r)"/></svg>`
	byKind, doc := kindsAndPaths(t, src)
	if byKind["negative-radius"] == nil {
		t.Fatalf("expected negative-radius, got %#v", byKind)
	}
	g, _ := doc.Gradient("r")
	if g.R.Value != 0 || !g.Degenerate {
		t.Errorf("negative radius not clamped/degenerate: r=%v deg=%v", g.R.Value, g.Degenerate)
	}
	_ = doc.Render(40, 40)
}

func TestNonMonotonicAndDuplicateStops(t *testing.T) {
	src := validGradientSVGHeader + `
<linearGradient id="g" x1="0" x2="1">
<stop offset="0.8" stop-color="red"/>
<stop offset="0.2" stop-color="lime"/>
<stop offset="0.2" stop-color="blue"/>
</linearGradient>
<rect width="10" height="10" fill="url(#g)"/></svg>`
	byKind, doc := kindsAndPaths(t, src)
	if byKind["non-monotonic-offset"] == nil {
		t.Error("expected non-monotonic-offset issue")
	}
	dup := byKind["duplicate-stop"]
	if dup == nil {
		t.Error("expected duplicate-stop issue")
	} else if !strings.Contains(dup.Path, "stop[3]") && !strings.Contains(dup.Path, "stop[2]") {
		t.Errorf("duplicate issue path should locate a stop, got %q", dup.Path)
	}
	g, _ := doc.Gradient("g")
	// Offsets must be monotonic after normalization.
	for i := 1; i < len(g.Stops); i++ {
		if g.Stops[i].Offset < g.Stops[i-1].Offset {
			t.Fatalf("stops not monotonic: %v", g.Stops)
		}
	}
	_ = doc.Render(20, 20)
}

func TestNonFiniteParameter(t *testing.T) {
	src := validGradientSVGHeader + `
<radialGradient id="r" cx="NaN" cy="50" r="Inf">
<stop offset="0" stop-color="white"/><stop offset="1" stop-color="black"/>
</radialGradient>
<rect width="10" height="10" fill="url(#r)"/></svg>`
	byKind, doc := kindsAndPaths(t, src)
	if byKind["non-finite-parameter"] == nil {
		t.Fatalf("expected non-finite-parameter, got %#v", byKind)
	}
	_ = doc.Render(20, 20)
}

func TestOversizedRange(t *testing.T) {
	src := validGradientSVGHeader + `
<linearGradient id="g" x1="0" y1="0" x2="1e30" y2="0">
<stop offset="0" stop-color="red"/><stop offset="1" stop-color="blue"/>
</linearGradient>
<rect width="10" height="10" fill="url(#g)"/></svg>`
	byKind, doc := kindsAndPaths(t, src)
	if byKind["oversized-range"] == nil {
		t.Fatalf("expected oversized-range, got %#v", byKind)
	}
	_ = doc.Render(20, 20)
}

func TestDuplicateGradientID(t *testing.T) {
	src := validGradientSVGHeader + `
<defs>
<linearGradient id="g"><stop offset="0" stop-color="red"/></linearGradient>
<radialGradient id="g"><stop offset="0" stop-color="blue"/></radialGradient>
</defs>
<rect width="10" height="10" fill="url(#g)"/></svg>`
	byKind, _ := kindsAndPaths(t, src)
	if byKind["duplicate-id"] == nil {
		t.Fatalf("expected duplicate-id, got %#v", byKind)
	}
}

func TestUnknownUnitsAndSpreadDegrade(t *testing.T) {
	src := validGradientSVGHeader + `
<linearGradient id="g" gradientUnits="funnyUnits" spreadMethod="wat">
<stop offset="0" stop-color="red"/><stop offset="1" stop-color="blue"/>
</linearGradient>
<rect width="10" height="10" fill="url(#g)"/></svg>`
	byKind, doc := kindsAndPaths(t, src)
	if byKind["unknown-units"] == nil || byKind["unknown-spread"] == nil {
		t.Fatalf("expected unknown-units/spread issues, got %#v", byKind)
	}
	g, _ := doc.Gradient("g")
	if g.Units != ObjectBoundingBox || g.Spread != SpreadPad {
		t.Error("unknown values did not degrade to defaults")
	}
}

func TestValidDocumentHasNoErrors(t *testing.T) {
	doc, err := Parse([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 16 16">
<rect width="16" height="16" fill="#fff"/>
<path d="M0 0h16v16H0z" fill="url(#g)"/>
<defs><linearGradient id="g"><stop offset="0" stop-color="#f00"/><stop offset="1" stop-color="#00f"/></linearGradient></defs>
</svg>`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(doc.GradientErrors()) != 0 {
		t.Fatalf("unexpected issues: %+v", doc.GradientErrors())
	}
}

func TestExistingNonGradientIDNotReportedMissing(t *testing.T) {
	src := []byte(validGradientSVGHeader + `
<defs><pattern id="p" width="4" height="4"><rect width="4" height="4" fill="#fff"/></pattern></defs>
<rect width="10" height="10" fill="url(#p)"/></svg>`)
	doc, err := Parse(src)
	if err != nil {
		t.Fatalf("pattern reference should not be a gradient validation error: %v", err)
	}
	for _, ge := range doc.GradientErrors() {
		if ge.Kind == "missing-reference" {
			t.Fatalf("existing pattern id must not be called missing: %v", ge)
		}
	}
}

func TestSpreadMapping(t *testing.T) {
	cases := []struct {
		t    float64
		mode SpreadMethod
		want float64
	}{
		{0.5, SpreadPad, 0.5},
		{-1, SpreadPad, 0},
		{2, SpreadPad, 1},
		{0.25, SpreadRepeat, 0.25},
		{1.25, SpreadRepeat, 0.25},
		{-0.25, SpreadRepeat, 0.75},
		{0.25, SpreadReflect, 0.25},
		{1.25, SpreadReflect, 0.75},
		{-0.25, SpreadReflect, 0.25},
	}
	for _, c := range cases {
		if got := applySpread(c.t, c.mode); absF(got-c.want) > 1e-9 {
			t.Errorf("applySpread(%v,%d)=%v want %v", c.t, c.mode, got, c.want)
		}
	}
}

func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

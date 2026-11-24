package svg

import (
	"errors"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/scene"
)

// loadUseFixture reads a fixture from testdata/use.
func loadUseFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "use", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

// svgErrAt extracts the positioned error, failing when the error is not one of
// the package's svgError values.
func svgErrAt(t *testing.T, err error) *svgError {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var se *svgError
	if !errors.As(err, &se) {
		t.Fatalf("error %v is not a positioned svgError", err)
	}
	return se
}

// countColorPixels counts pixels whose straight (un-premultiplied) color is
// close to the wanted RGBA triple and whose alpha exceeds a small threshold.
func countColorPixels(img *image.RGBA, want color.RGBA, tolerance uint32) int {
	count := 0
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			if a < 0x1000 { // coverage below ~1/16 is anti-aliased fringe
				continue
			}
			got := color.RGBA{
				R: uint8(r * 255 / a),
				G: uint8(g * 255 / a),
				B: uint8(b * 255 / a),
			}
			if channelClose(got.R, want.R, tolerance) &&
				channelClose(got.G, want.G, tolerance) &&
				channelClose(got.B, want.B, tolerance) {
				count++
			}
		}
	}
	return count
}

func channelClose(a, b uint8, tolerance uint32) bool {
	d := int(a) - int(b)
	if d < 0 {
		d = -d
	}
	return uint32(d) <= tolerance
}

// TestUseThreeInstances renders one symbol three times with different
// transforms, colors and opacity and checks per-instance pixels and bounds.
func TestUseThreeInstances(t *testing.T) {
	doc, err := Parse(loadUseFixture(t, "three_instances.svg"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Elements) != 3 {
		t.Fatalf("top-level elements = %d, want 3 use instances in document order", len(doc.Elements))
	}
	for i, elem := range doc.Elements {
		if _, ok := elem.(*UseElement); !ok {
			t.Fatalf("Elements[%d] is %T, want *UseElement", i, elem)
		}
	}

	img := doc.Render(48, 16)
	if b := img.Bounds(); b.Dx() != 48 || b.Dy() != 16 {
		t.Fatalf("Bounds = %v, want 48x16", b)
	}

	// Instance 1: solid red circle centered at (8, 8).
	if r, _, _, a := img.At(8, 8).RGBA(); a == 0 || r < 0xC000 {
		t.Errorf("instance 1 center pixel not solid red: r=%d a=%d", r>>8, a>>8)
	}
	// Instance 3: blue circle at 50% opacity centered at (40, 8). Its alpha
	// must be noticeably below full opacity and its blue channel dominant.
	_, _, b, a3 := img.At(40, 8).RGBA()
	if a3 == 0 || b < 0x6000 {
		t.Errorf("instance 3 center pixel not blue: b=%d a=%d", b>>8, a3>>8)
	}
	if a3 > 0x9000 {
		t.Errorf("instance 3 alpha = %d, want roughly half opacity", a3>>8)
	}

	// Instance 2 is rotated 45 degrees around its center (24, 8); the exact
	// center pixel stays green.
	r, g, _, a2 := img.At(24, 8).RGBA()
	if a2 == 0 || g < 0xC000 || r > 0x2000 {
		t.Errorf("instance 2 center pixel not green: r=%d g=%d a=%d", r>>8, g>>8, a2>>8)
	}

	// Each instance color must appear with a substantial pixel population.
	redPixels := countColorPixels(img, color.RGBA{R: 255, G: 0, B: 0}, 12)
	greenPixels := countColorPixels(img, color.RGBA{R: 0, G: 255, B: 0}, 12)
	bluePixels := countColorPixels(img, color.RGBA{R: 0, G: 0, B: 255}, 12)
	if redPixels < 40 || greenPixels < 40 || bluePixels < 40 {
		t.Errorf("instance pixel populations red=%d green=%d blue=%d, want >= 40 each",
			redPixels, greenPixels, bluePixels)
	}
}

// TestUseInstanceIndependence verifies that per-instance presentation
// attributes never leak into the definition or into sibling instances, and
// that repeated renders of the same Document stay consistent.
func TestUseInstanceIndependence(t *testing.T) {
	doc, err := Parse(loadUseFixture(t, "three_instances.svg"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	symbol, ok := doc.defs["dot"].(*SymbolElement)
	if !ok {
		t.Fatalf("registry entry dot = %T, want *SymbolElement", doc.defs["dot"])
	}
	if symbol.Attrs.Fill != "" {
		t.Errorf("symbol definition fill = %q, want empty (instances must not mutate it)", symbol.Attrs.Fill)
	}

	first := doc.Render(48, 16)
	second := doc.Render(48, 16)
	if !imagesEqual(first, second) {
		t.Error("repeated renders of the same Document diverged")
	}

	// The three instances keep distinct colors even though they share one
	// definition: red region, green region, blue region.
	if red := countColorPixels(first, color.RGBA{R: 255}, 12); red == 0 {
		t.Error("no red pixels from instance 1")
	}
	if green := countColorPixels(first, color.RGBA{G: 255}, 12); green == 0 {
		t.Error("no green pixels from instance 2")
	}
	if blue := countColorPixels(first, color.RGBA{B: 255}, 12); blue == 0 {
		t.Error("no blue pixels from instance 3")
	}

	// A color-override render must not alter later plain renders.
	_ = doc.RenderWithColor(48, 16, color.White)
	third := doc.Render(48, 16)
	if !imagesEqual(first, third) {
		t.Error("RenderWithColor mutated shared document state")
	}
}

// TestUseNestedAndXLINK covers nested <use> inside a referenced group, both
// href and xlink:href syntax, and stroke inheritance into instances.
func TestUseNestedAndXLINK(t *testing.T) {
	doc, err := Parse(loadUseFixture(t, "nested_use.svg"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Two top-level uses plus the xlink:href nested inside the defs group.
	uses := collectUses(doc.Elements)
	for _, def := range doc.defs {
		uses = append(uses, collectUses([]Element{def})...)
	}
	if len(uses) != 3 {
		t.Fatalf("expected 3 use elements (2 top-level + 1 nested xlink), got %d", len(uses))
	}

	img := doc.Render(32, 16)
	assertNonEmpty(t, img, "nested use render")

	// Instance 1 inherits a red stroke onto paths that only define fills.
	redStroke := countColorPixels(img, color.RGBA{R: 204, G: 51, B: 51}, 16)
	if redStroke == 0 {
		t.Error("instance 1 stroke inheritance produced no red pixels")
	}
	// Instance 2 overrides the inherited fill with green.
	greenFill := countColorPixels(img, color.RGBA{R: 51, G: 204, B: 102}, 16)
	if greenFill == 0 {
		t.Error("instance 2 green fill override produced no green pixels")
	}
	// Instance 1 keeps the path's own blue fill as well.
	blueFill := countColorPixels(img, color.RGBA{R: 51, G: 102, B: 204}, 16)
	if blueFill == 0 {
		t.Error("nested leaf fill produced no blue pixels")
	}
}

// TestUseEmptySymbol verifies an empty symbol instance renders nothing while
// the rest of the document remains intact.
func TestUseEmptySymbol(t *testing.T) {
	doc, err := Parse(loadUseFixture(t, "empty_symbol.svg"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	img := doc.Render(16, 16)
	bounds := img.Bounds()
	if bounds.Dx() != 16 || bounds.Dy() != 16 {
		t.Fatalf("Bounds = %v, want 16x16", bounds)
	}
	// Only the light-gray background rect should be visible: no red pixels.
	if red := countColorPixels(img, color.RGBA{R: 255}, 12); red != 0 {
		t.Errorf("empty symbol instance produced %d red pixels, want 0", red)
	}
	r, g, b, a := img.At(8, 8).RGBA()
	if a == 0 {
		t.Fatal("background rect missing: center pixel is transparent")
	}
	if r != g || g != b {
		t.Errorf("center pixel (%d,%d,%d) is not grayscale background", r>>8, g>>8, b>>8)
	}
}

// TestUseViewBoxMapping checks uniform meet scaling against non-uniform
// preserveAspectRatio="none" stretching.
func TestUseViewBoxMapping(t *testing.T) {
	doc, err := Parse(loadUseFixture(t, "viewbox_mapping.svg"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	img := doc.Render(32, 16)

	// Left instance: 10x10 viewBox mapped with xMidYMid meet into a 12x16
	// viewport => uniform scale 1.2, content 12x12 centered with 2-unit
	// vertical gutters. The top gutter is transparent while the center is red.
	if _, _, _, a := img.At(6, 0).RGBA(); a != 0 {
		t.Errorf("meet-aligned symbol top gutter painted: alpha=%d", a>>8)
	}
	if r, _, _, a := img.At(6, 8).RGBA(); a == 0 || r < 0xC000 {
		t.Errorf("meet symbol center not red: r=%d a=%d", r>>8, a>>8)
	}

	// Right instance: preserveAspectRatio="none" stretches the 10x10 box over
	// the full 16x16 viewport, so the extreme corner is blue with no gutter.
	if _, _, bl, a := img.At(31, 1).RGBA(); a == 0 || bl < 0xC000 {
		t.Errorf("stretched symbol corner not blue: b=%d a=%d", bl>>8, a>>8)
	}
	// The meet-aligned instance's own corner gutter stays empty, proving the
	// two instances received independent viewBox mappings.
	if _, _, _, a := img.At(6, 15).RGBA(); a != 0 {
		t.Errorf("meet instance bottom gutter painted: alpha=%d", a>>8)
	}
}

// TestUseViewBoxNonZeroOrigin verifies symbols whose viewBox does not start at
// (0,0), under a non-uniform scale, land in the right place. This guards the
// transform ordering (viewport scale outside the viewBox origin offset), which
// only differs from the reversed order when the mapping scale is not 1.
func TestUseViewBoxNonZeroOrigin(t *testing.T) {
	const src = `<svg xmlns="http://www.w3.org/2000/svg" width="16" height="12" viewBox="0 0 16 12">
  <defs>
    <symbol id="none" viewBox="2 3 8 6" preserveAspectRatio="none">
      <rect x="2" y="3" width="8" height="6" fill="#0000FF"/>
    </symbol>
  </defs>
  <use href="#none" x="0" y="0" width="16" height="12"/>
</svg>`
	doc, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	img := doc.Render(16, 12)

	// viewBox (2,3,8,6) maps with scale (2,2) onto the 16x12 viewport. The
	// rect covers the whole viewBox, so after a correct mapping it must cover
	// the whole viewport: point (1,1) is solidly inside it. Reversing the
	// scale/origin order would instead leave a (2,3)-sized empty margin.
	if _, _, bl, a := img.At(1, 1).RGBA(); a < 0x8000 || bl < 0x8000 {
		t.Errorf("offset viewBox mis-mapped at (1,1): b=%d a=%d, want full-viewport blue", bl>>8, a>>8)
	}
	// The far corner is likewise inside the correctly mapped rect.
	if _, _, bl, a := img.At(14, 10).RGBA(); a < 0x8000 || bl < 0x8000 {
		t.Errorf("offset viewBox mis-mapped at (14,10): b=%d a=%d", bl>>8, a>>8)
	}

	// Same check for the default meet alignment with a non-uniform viewport so
	// the uniform-scale path is exercised at scale != 1 as well.
	const meetSrc = `<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 16 16">
  <defs>
    <symbol id="meet" viewBox="2 3 8 6">
      <rect x="2" y="3" width="8" height="6" fill="#FF0000"/>
    </symbol>
  </defs>
  <use href="#meet" x="0" y="0" width="16" height="16"/>
</svg>`
	meetDoc, err := Parse([]byte(meetSrc))
	if err != nil {
		t.Fatalf("Parse meet: %v", err)
	}
	meetImg := meetDoc.Render(16, 16)
	// meet scales uniformly by min(2, 16/6≈2.67)=2 and centers vertically.
	// The rect's mapped center is at (8,8); it must be red, not transparent.
	if r, _, _, a := meetImg.At(8, 8).RGBA(); a < 0x8000 || r < 0x8000 {
		t.Errorf("meet offset viewBox center mis-mapped: r=%d a=%d", r>>8, a>>8)
	}
}

// TestUseDocumentOrder verifies instances paint in document order: a later
// instance overlaps an earlier one and wins the shared pixels.
func TestUseDocumentOrder(t *testing.T) {
	const src = `<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 16 16">
  <defs>
    <symbol id="s" viewBox="0 0 8 8"><rect width="8" height="8"/></symbol>
  </defs>
  <use href="#s" x="0" y="0" width="8" height="8" fill="#FF0000"/>
  <use href="#s" x="4" y="4" width="8" height="8" fill="#0000FF"/>
</svg>`
	doc, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	img := doc.Render(16, 16)
	// Shared region center (7,7) belongs to the later blue instance.
	r, _, bl, a := img.At(7, 7).RGBA()
	if a == 0 || bl <= r {
		t.Errorf("document order violated at overlap: r=%d b=%d a=%d, want blue on top", r>>8, bl>>8, a>>8)
	}
	// Non-overlapped corner of the first instance stays red.
	if rr, _, _, a := img.At(1, 1).RGBA(); a == 0 || rr < 0xC000 {
		t.Errorf("first instance corner not red: r=%d a=%d", rr>>8, a>>8)
	}
}

// TestUseErrorFixtures exercises every required error class and checks that
// each error carries source position context.
func TestUseErrorFixtures(t *testing.T) {
	tests := []struct {
		name     string
		fixture  string
		contains string
		line     int
	}{
		{"missing reference", "missing_ref.svg", "does not match", 5},
		{"duplicate id", "duplicate_id.svg", "duplicate id", 4},
		{"cross-document reference", "cross_document.svg", "cross-document", 2},
		{"circular reference", "circular.svg", "circular reference", 4},
		{"invalid size", "invalid_size.svg", "invalid width", 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(loadUseFixture(t, tt.fixture))
			se := svgErrAt(t, err)
			if !strings.Contains(se.Error(), tt.contains) {
				t.Errorf("error %q does not contain %q", se.Error(), tt.contains)
			}
			if se.pos.Line != tt.line {
				t.Errorf("error line = %d, want %d (full error: %v)", se.pos.Line, tt.line, se)
			}
			if se.pos.Column <= 0 {
				t.Errorf("error column = %d, want 1-based positive column", se.pos.Column)
			}
		})
	}
}

// TestUseMissingHrefAndEmptyFragment covers the malformed-reference edge cases.
func TestUseMissingHrefAndEmptyFragment(t *testing.T) {
	if _, err := Parse([]byte(`<svg viewBox="0 0 16 16"><use x="0" y="0"/></svg>`)); err == nil {
		t.Error("use without href: expected error")
	} else if !strings.Contains(err.Error(), "missing href") {
		t.Errorf("error %q does not mention missing href", err)
	}

	se := svgErrAt(t, ParseErr(t, `<svg viewBox="0 0 16 16"><use href="#"/></svg>`))
	if !strings.Contains(se.Error(), "empty fragment") {
		t.Errorf("error %q does not mention empty fragment", se.Error())
	}
}

// TestUseDepthLimit generates instance chains around the depth limit: a chain
// at the limit renders, one beyond it is rejected.
func TestUseDepthLimit(t *testing.T) {
	build := func(depth int) []byte {
		var b strings.Builder
		b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 16 16"><defs>`)
		b.WriteString(`<rect id="n0" width="2" height="2" fill="red"/>`)
		for i := 1; i <= depth; i++ {
			b.WriteString(`<g id="n` + itoa(i) + `"><use href="#n` + itoa(i-1) + `"/></g>`)
		}
		b.WriteString(`</defs><use href="#n` + itoa(depth) + `"/></svg>`)
		return []byte(b.String())
	}

	// A chain whose expansion depth stays within the limit renders; one group
	// deeper is rejected. Depth counts <use> hops along the chain, so the
	// deepest chain allowed contains maxUseDepth nested instances.
	if _, err := Parse(build(maxUseDepth - 1)); err != nil {
		t.Errorf("chain at the depth limit rejected: %v", err)
	}
	err := ParseErr(t, string(build(maxUseDepth)))
	se := svgErrAt(t, err)
	if !strings.Contains(se.Error(), "depth exceeds limit") {
		t.Errorf("error %q does not mention depth limit", se.Error())
	}
}

// TestUseHRefPrecedence checks that when both the SVG2 href and the legacy
// xlink:href attributes are present, the namespaceless href wins.
func TestUseHRefPrecedence(t *testing.T) {
	const src = `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" viewBox="0 0 16 16">
  <defs>
    <circle id="primary" cx="4" cy="8" r="3" fill="#FF0000"/>
    <circle id="legacy" cx="12" cy="8" r="3" fill="#0000FF"/>
  </defs>
  <use href="#primary" xlink:href="#legacy"/>
</svg>`
	doc, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	img := doc.Render(16, 16)
	// href="#primary" must win: red on the left, nothing blue on the right.
	if r, _, _, a := img.At(4, 8).RGBA(); a == 0 || r < 0xC000 {
		t.Errorf("href winner not rendered: r=%d a=%d", r>>8, a>>8)
	}
	if _, _, bl, a := img.At(12, 8).RGBA(); a != 0 || bl != 0 {
		t.Errorf("xlink:href fallback was rendered despite href: b=%d a=%d", bl>>8, a>>8)
	}
}

// TestUseUnknownAttributesIgnored keeps the package's historical rule:
// attributes the renderer does not understand are silently ignored.
func TestUseUnknownAttributesIgnored(t *testing.T) {
	const src = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16">
  <defs><circle id="c" cx="8" cy="8" r="6" data-track="ignored" role="img"/></defs>
  <use href="#c" x="0" y="0" aria-label="dot" class="primary"/>
</svg>`
	doc, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	img := doc.Render(16, 16)
	assertNonEmpty(t, img, "use with unknown attributes")
}

// TestUseSceneRenderer verifies retained-mode rendering handles instances and
// emits vector commands for the referenced geometry.
func TestUseSceneRenderer(t *testing.T) {
	doc, err := Parse(loadUseFixture(t, "three_instances.svg"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s := scene.NewScene()
	doc.RenderToScene(s, 0, 0, 48, 16)
	enc := s.Encoding()
	if enc.IsEmpty() {
		t.Fatal("scene encoding is empty after rendering symbol instances")
	}
	// Three circle instances (plus transforms) must be retained as shapes.
	if n := enc.ShapeCount(); n < 3 {
		t.Errorf("ShapeCount = %d, want >= 3 retained circle instances", n)
	}

	// Scene output must agree with the immediate renderer that an empty symbol
	// instance emits no commands.
	emptyDoc, err := Parse(loadUseFixture(t, "empty_symbol.svg"))
	if err != nil {
		t.Fatalf("Parse empty symbol: %v", err)
	}
	emptyScene := scene.NewScene()
	emptyDoc.RenderToScene(emptyScene, 0, 0, 16, 16)
	// Exactly one shape (the background rect), nothing from the empty symbol.
	if n := emptyScene.Encoding().ShapeCount(); n != 1 {
		t.Errorf("empty symbol scene ShapeCount = %d, want 1 (background only)", n)
	}
}

// TestUseGDefinitionAndForwardReference ensures a <g> with an id is usable as a
// definition and that references declared before their target still resolve.
func TestUseGDefinitionAndForwardReference(t *testing.T) {
	const src = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16">
  <use href="#group" x="4" y="4" fill="#00AA00"/>
  <g id="group">
    <rect x="0" y="0" width="4" height="4"/>
    <circle cx="2" cy="2" r="1" fill="none" stroke="black"/>
  </g>
</svg>`
	doc, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	img := doc.Render(16, 16)
	// Instance translates by (4,4), so the 4x4 rect occupies 4..8 in both
	// axes. Its corner (4,4) is clear of the small stroked circle and must
	// carry the instance green fill (#00AA00 → green 170), overriding the
	// inherited default black.
	_, g, _, a := img.At(4, 4).RGBA()
	if a < 0x8000 || g>>8 != 170 {
		t.Errorf("forward-referenced group instance not #00AA00 green: g=%d a=%d", g>>8, a>>8)
	}
	// The original group is also still rendered in place (its origin corner).
	if _, _, _, a := img.At(1, 1).RGBA(); a == 0 {
		t.Error("in-place group with id was removed from the render tree")
	}
}

// TestUseRendersThroughGGContext checks the RenderTo entry point used by
// embedding applications.
func TestUseRendersThroughGGContext(t *testing.T) {
	doc, err := Parse(loadUseFixture(t, "three_instances.svg"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	dc := gg.NewContext(48, 16)
	doc.RenderTo(dc, 0, 0, 48, 16)
	if !imageHasAlpha(dc) {
		t.Fatal("RenderTo produced a fully transparent image")
	}
}

// --- small helpers kept local to this file ---

// ParseErr parses source and fails the test on success, returning the parse
// error otherwise.
func ParseErr(t *testing.T, src string) error {
	t.Helper()
	_, err := Parse([]byte(src))
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	return err
}

func collectUses(elements []Element) []*UseElement {
	var out []*UseElement
	var visit func([]Element)
	visit = func(elems []Element) {
		for _, elem := range elems {
			if use, ok := elem.(*UseElement); ok {
				out = append(out, use)
			}
			visit(targetElements(elem))
		}
	}
	visit(elements)
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

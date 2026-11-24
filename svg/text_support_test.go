package svg

import (
	"errors"
	"image"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/scene"
	"github.com/gogpu/gg/text"
)

type textTestFonts struct {
	latin  *text.FontSource
	cjk    *text.FontSource
	arabic *text.FontSource
}

func loadTextTestFonts(t *testing.T) textTestFonts {
	t.Helper()
	fonts := textTestFonts{}
	var err error
	fonts.latin, err = text.NewFontSourceFromFile("../text/testdata/goregular.ttf")
	if err != nil {
		t.Fatalf("load latin test font: %v", err)
	}
	fonts.cjk, err = text.NewFontSourceFromFile("../text/testdata/notoseriftc_autohint_metrics.ttf")
	if err != nil {
		t.Fatalf("load CJK test font: %v", err)
	}
	for _, path := range []string{
		"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
		"/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf",
	} {
		if _, statErr := os.Stat(path); statErr != nil {
			continue
		}
		fonts.arabic, err = text.NewFontSourceFromFile(path)
		if err == nil {
			break
		}
	}
	return fonts
}

func (f textTestFonts) options(includeArabic bool) []any {
	opts := []any{
		WithoutSystemFonts(),
		WithFont(f.latin.Face(20)),
		WithFallbackFonts(f.cjk.Face(20)),
	}
	if includeArabic && f.arabic != nil {
		opts = append(opts, WithFallbackFonts(f.arabic.Face(20)))
	}
	return opts
}

func readTextFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/text/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func alphaBounds(img *image.RGBA, threshold uint8) (image.Rectangle, int) {
	bounds := image.Rect(-1, -1, -1, -1)
	count := 0
	r := img.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			a := img.Pix[(y-img.Rect.Min.Y)*img.Stride+(x-img.Rect.Min.X)*4+3]
			if a > threshold {
				count++
				if bounds.Min.X < 0 {
					bounds.Min = image.Pt(x, y)
					bounds.Max = image.Pt(x+1, y+1)
				} else {
					if x < bounds.Min.X {
						bounds.Min.X = x
					}
					if y < bounds.Min.Y {
						bounds.Min.Y = y
					}
					if x+1 > bounds.Max.X {
						bounds.Max.X = x + 1
					}
					if y+1 > bounds.Max.Y {
						bounds.Max.Y = y + 1
					}
				}
			}
		}
	}
	return bounds, count
}

func rowAlphaCount(img *image.RGBA, y0, y1 int) int {
	count := 0
	r := img.Bounds()
	if y0 < r.Min.Y {
		y0 = r.Min.Y
	}
	if y1 > r.Max.Y {
		y1 = r.Max.Y
	}
	for y := y0; y < y1; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			a := img.Pix[(y-r.Min.Y)*img.Stride+(x-r.Min.X)*4+3]
			if a > 24 {
				count++
			}
		}
	}
	return count
}

func TestSVGTextParseAttributesAndInheritance(t *testing.T) {
	data := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 20">
	  <g font-family="Inherited Family" font-size="17" font-weight="700" font-style="italic" text-anchor="middle" direction="rtl">
	    <text x="3" y="9" dx="1" dy="2">
	      <tspan x="4" y="10" dx="2" dy="3">A &amp; B</tspan>
	      <tspan font-weight="400">Two</tspan>
	    </text>
	  </g>
	</svg>`)
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	g, ok := doc.Elements[0].(*GroupElement)
	if !ok {
		t.Fatalf("element = %T, want *GroupElement", doc.Elements[0])
	}
	text, ok := g.Children[0].(*TextElement)
	if !ok {
		t.Fatalf("child = %T, want *TextElement", g.Children[0])
	}
	if text.X != 3 || text.Y != 9 || text.DX != 1 || text.DY != 2 {
		t.Errorf("text coordinates = (%v,%v d%v,%v), want (3,9 dx1,dy2)", text.X, text.Y, text.DX, text.DY)
	}
	if text.FontFamily != "Inherited Family" || text.TextAnchor != "middle" || text.Direction != "rtl" {
		t.Errorf("inherited text style = %+v", text.Attrs)
	}
	if text.FontSize != 17 || text.FontWeightValue != 700 || !text.FontWeightSet {
		t.Errorf("inherited size/weight = %v/%d set=%v", text.FontSize, text.FontWeightValue, text.FontWeightSet)
	}
	if len(text.TSpans) != 2 {
		t.Fatalf("tspans = %d, want 2", len(text.TSpans))
	}
	first := text.TSpans[0]
	if first.Content != "A & B" {
		t.Errorf("first content = %q, want decoded entity %q", first.Content, "A & B")
	}
	if first.X != 4 || first.Y != 10 || first.DX != 2 || first.DY != 3 {
		t.Errorf("tspan coords = (%v,%v d%v,%v), want (4,10 dx2,dy3)", first.X, first.Y, first.DX, first.DY)
	}
	if first.FontFamily != "Inherited Family" {
		t.Errorf("tspan family = %q, want inherited", first.FontFamily)
	}
	if text.TSpans[1].FontWeightValue != 400 {
		t.Errorf("second weight = %d, want 400", text.TSpans[1].FontWeightValue)
	}
	if got := collectTextContent(text.Children); strings.Contains(got, "<tspan>") || strings.Contains(got, "</tspan>") {
		t.Errorf("markup leaked into text content: %q", got)
	}
}

func TestSVGTextFixtureLayoutMatchesShaping(t *testing.T) {
	fonts := loadTextTestFonts(t)
	data := readTextFixture(t, "latin.svg")
	doc, err := Parse(data, fonts.options(false)...)
	if err != nil {
		t.Fatal(err)
	}
	textElem := doc.Elements[0].(*TextElement)
	chunks := buildTextChunks(textElem, defaultFontState(), newTextLimiter(doc.TextOptions))
	if len(chunks) == 0 {
		t.Fatal("no text chunks")
	}
	var shapedWidth float64
	for _, chunk := range chunks {
		runs, width, _, _ := layoutChunk(chunk, doc)
		for _, run := range runs {
			for _, piece := range run.pieces {
				if piece.face != nil && piece.face.Source() == fonts.latin {
					shapedWidth = width
				}
			}
		}
	}
	face := fonts.latin.Face(30, text.WithLanguage("en"))
	wantGlyphs := text.Shape("Latin Ag09", face)
	var wantWidth float64
	for _, g := range wantGlyphs {
		if end := g.X + g.XAdvance; end > wantWidth {
			wantWidth = end
		}
	}
	if shapedWidth <= 0 {
		t.Fatal("layout width = 0")
	}
	if math.Abs(shapedWidth-wantWidth) > 0.75 {
		t.Fatalf("SVG layout width = %.3f, text.Shape width = %.3f", shapedWidth, wantWidth)
	}
}

func TestSVGTextScriptLayoutUsesShapedFaces(t *testing.T) {
	fonts := loadTextTestFonts(t)
	for _, tc := range []struct {
		file   string
		arabic bool
	}{
		{"cjk.svg", false},
		{"arabic.svg", true},
	} {
		t.Run(tc.file, func(t *testing.T) {
			if tc.arabic && fonts.arabic == nil {
				t.Skip("Arabic system font unavailable")
			}
			doc, err := Parse(readTextFixture(t, tc.file), fonts.options(tc.arabic)...)
			if err != nil {
				t.Fatal(err)
			}
			textElem := doc.Elements[0].(*TextElement)
			chunks := buildTextChunks(textElem, defaultFontState(), newTextLimiter(doc.TextOptions))
			shapedPieces, rtlRuns, width := 0, 0, 0.0
			for _, chunk := range chunks {
				runs, chunkWidth, _, _ := layoutChunk(chunk, doc)
				width += chunkWidth
				for _, run := range runs {
					if run.rtl {
						rtlRuns++
					}
					for _, piece := range run.pieces {
						if piece.face != nil && piece.face.Source() != nil && len(piece.glyphs) > 0 {
							shapedPieces++
							var want float64
							for _, g := range text.Shape(piece.text, piece.face) {
								if end := g.X + g.XAdvance; end > want {
									want = end
								}
							}
							if math.Abs(want-piece.width) > 0.75 {
								t.Fatalf("piece width %.3f, text.Shape width %.3f for %q", piece.width, want, piece.text)
							}
						}
					}
				}
			}
			if shapedPieces == 0 || width <= 0 {
				t.Fatalf("shapedPieces=%d width=%.3f", shapedPieces, width)
			}
			if tc.arabic && rtlRuns == 0 {
				t.Fatal("Arabic fixture produced no RTL run")
			}
		})
	}
}

func TestSVGTextFixturesRenderPixels(t *testing.T) {
	fonts := loadTextTestFonts(t)
	cases := []struct {
		name   string
		file   string
		w, h   int
		arabic bool
		minInk int
	}{
		{name: "latin", file: "latin.svg", w: 440, h: 140, minInk: 500},
		{name: "cjk", file: "cjk.svg", w: 440, h: 140, minInk: 500},
		{name: "arabic", file: "arabic.svg", w: 480, h: 160, arabic: true, minInk: 300},
		{name: "tspan lines", file: "tspan_lines.svg", w: 440, h: 180, minInk: 800},
		{name: "anchor", file: "anchor.svg", w: 440, h: 200, minInk: 800},
		{name: "entities", file: "entities_utf8.svg", w: 520, h: 140, minInk: 500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.arabic && fonts.arabic == nil {
				t.Skip("DejaVu Arabic font unavailable")
			}
			data := readTextFixture(t, tc.file)
			opts := fonts.options(tc.arabic)
			doc, err := Parse(data, opts...)
			if err != nil {
				t.Fatal(err)
			}
			img, rerr := doc.RenderChecked(tc.w, tc.h)
			if rerr != nil && tc.name != "entities" {
				t.Fatalf("RenderChecked: %v", rerr)
			}
			bounds, ink := alphaBounds(img, 24)
			if ink < tc.minInk {
				t.Fatalf("ink pixels = %d, want >= %d (bounds=%v, err=%v)", ink, tc.minInk, bounds, rerr)
			}
			if bounds.Empty() || bounds.Min.X < 0 || bounds.Max.X > tc.w || bounds.Max.Y > tc.h {
				t.Fatalf("bad ink bounds %v for image %dx%d", bounds, tc.w, tc.h)
			}
			if tc.file == "tspan_lines.svg" {
				top := rowAlphaCount(img, 20, 80)
				bottom := rowAlphaCount(img, 100, 170)
				if top < 100 || bottom < 100 {
					t.Fatalf("multiline ink top=%d bottom=%d, want both visible", top, bottom)
				}
			}
		})
	}
}

func TestSVGTextAnchorPixelAlignment(t *testing.T) {
	fonts := loadTextTestFonts(t)
	for _, anchor := range []string{"start", "middle", "end"} {
		t.Run(anchor, func(t *testing.T) {
			data := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 200 80">
			  <text x="100" y="44" text-anchor="` + anchor + `" font-size="24" fill="#000">Anchor</text>
			</svg>`)
			doc, err := Parse(data, fonts.options(false)...)
			if err != nil {
				t.Fatal(err)
			}
			img := doc.Render(400, 160)
			bounds, ink := alphaBounds(img, 40)
			if ink < 100 {
				t.Fatalf("too little ink: %d bounds=%v", ink, bounds)
			}
			center := float64(bounds.Min.X+bounds.Max.X) / 2
			switch anchor {
			case "start":
				if math.Abs(float64(bounds.Min.X)-200) > 18 {
					t.Fatalf("start minX=%d, want near 200", bounds.Min.X)
				}
			case "middle":
				if math.Abs(center-200) > 14 {
					t.Fatalf("middle center=%.1f, want near 200 (bounds=%v)", center, bounds)
				}
			case "end":
				if math.Abs(float64(bounds.Max.X)-200) > 18 {
					t.Fatalf("end maxX=%d, want near 200", bounds.Max.X)
				}
			}
		})
	}
}

func TestSVGTextRenderToUsesContextFontAndTransform(t *testing.T) {
	fonts := loadTextTestFonts(t)
	data := []byte(`<svg viewBox="0 0 100 40"><text x="2" y="28" font-size="20">Gg</text></svg>`)
	doc, err := Parse(data, WithoutSystemFonts())
	if err != nil {
		t.Fatal(err)
	}
	dc := gg.NewContext(200, 80)
	dc.SetFont(fonts.latin.Face(20))
	doc.RenderTo(dc, 0, 0, 200, 80)
	img := dc.Image().(*image.RGBA)
	if _, ink := alphaBounds(img, 40); ink < 100 {
		t.Fatalf("context-font RenderTo ink=%d", ink)
	}

	transformed, err := Parse([]byte(`<svg viewBox="0 0 160 40"><g transform="translate(60,0)"><text x="2" y="28" font-size="20">Gg</text></g></svg>`),
		WithoutSystemFonts(), WithFont(fonts.latin.Face(20)))
	if err != nil {
		t.Fatal(err)
	}
	plain := ParseForTestRender(t, []byte(`<svg viewBox="0 0 160 40"><text x="2" y="28" font-size="20">Gg</text></svg>`), fonts)
	shifted := transformed.Render(320, 80)
	plainImg := plain.Render(320, 80)
	plainBounds, _ := alphaBounds(plainImg, 40)
	shiftBounds, _ := alphaBounds(shifted, 40)
	if shiftBounds.Min.X <= plainBounds.Min.X+80 {
		t.Fatalf("transform did not shift ink: plain=%v shifted=%v", plainBounds, shiftBounds)
	}
}

func ParseForTestRender(t *testing.T, data []byte, fonts textTestFonts) *Document {
	t.Helper()
	doc, err := Parse(data, fonts.options(false)...)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestSVGTextRenderToScene(t *testing.T) {
	fonts := loadTextTestFonts(t)
	doc, err := Parse([]byte(`<svg viewBox="0 0 100 40"><text x="4" y="28" font-size="20">Scene</text></svg>`), fonts.options(false)...)
	if err != nil {
		t.Fatal(err)
	}
	target := scene.NewScene()
	doc.RenderToScene(target, 0, 0, 200, 80)
	if target.IsEmpty() {
		t.Fatal("expected text scene to contain encoded commands")
	}
}

type errFontResolver struct{}

func (errFontResolver) ResolveFont(string, float64) (text.Face, error) {
	return nil, os.ErrNotExist
}

func TestSVGTextFallbackEmptyInvalidLongAndExternal(t *testing.T) {
	fonts := loadTextTestFonts(t)

	empty, err := Parse([]byte(`<svg viewBox="0 0 10 10"><text x="0" y="8"></text></svg>`))
	if err != nil {
		t.Fatal(err)
	}
	dc := gg.NewContext(20, 20)
	if err := empty.RenderToChecked(dc, 0, 0, 20, 20); err != nil {
		t.Fatalf("empty text diagnostics: %v", err)
	}

	bad, err := Parse([]byte(`<svg viewBox="0 0 50 20"><text x="bad" y="9">x</text></svg>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(bad.Elements[0].(*TextElement).ParseErrors) == 0 {
		t.Fatal("expected coordinate parse diagnostic")
	}
	dc = gg.NewContext(100, 40)
	err = bad.RenderToChecked(dc, 0, 0, 100, 40, WithFont(fonts.latin.Face(20)), WithoutSystemFonts())
	if !errors.Is(err, ErrInvalidCoordinate) {
		t.Fatalf("render error=%v, want ErrInvalidCoordinate", err)
	}
	if _, err := Parse([]byte(`<svg><text x="bad">x</text></svg>`), WithStrictTextCoordinates()); !errors.Is(err, ErrInvalidCoordinate) {
		t.Fatalf("strict parse error=%v, want ErrInvalidCoordinate", err)
	}

	long := ParseForTestRender(t, []byte(`<svg viewBox="0 0 200 40"><text x="2" y="28" font-size="20">`+repeatRunes("AB", 100)+`</text></svg>`), fonts)
	err = long.RenderToChecked(gg.NewContext(400, 80), 0, 0, 400, 80, WithMaxTextLength(16))
	if !errors.Is(err, ErrTextTooLong) {
		t.Fatalf("long text error=%v, want ErrTextTooLong", err)
	}

	missing := ParseForTestRender(t, []byte(`<svg viewBox="0 0 100 40"><text x="2" y="28" font-size="20">AB&#xE000;</text></svg>`), fonts)
	err = missing.RenderToChecked(gg.NewContext(200, 80), 0, 0, 200, 80, WithoutSystemFonts())
	if !errors.Is(err, ErrMissingGlyph) {
		t.Fatalf("missing glyph error=%v, want ErrMissingGlyph", err)
	}

	external, err := Parse([]byte(`<svg viewBox="0 0 10 10"><text>x</text></svg>`),
		WithSimpleFontResolver(errFontResolver{}), WithoutSystemFonts())
	if err != nil {
		t.Fatal(err)
	}
	err = external.RenderToChecked(gg.NewContext(20, 20), 0, 0, 20, 20)
	if !errors.Is(err, ErrExternalFont) {
		t.Fatalf("external font error=%v, want ErrExternalFont", err)
	}
	if _, err := text.NewFontSourceFromFile("/nonexistent-svg-font.ttf"); err == nil {
		t.Fatal("expected unavailable external font file error")
	}
}

func repeatRunes(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

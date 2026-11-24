package svg

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/gogpu/gg"
)

func ggRGBA(r, g, b, a float64) gg.RGBA { return gg.RGBA{R: r, G: g, B: b, A: a} }

func TestStopSamplingBoundaries(t *testing.T) {
	stops := []GradientStop{
		{Offset: 0, Color: ggRGBA(1, 0, 0, 1)},
		{Offset: 1, Color: ggRGBA(0, 0, 1, 1)},
	}
	if c := sampleGradientStops(stops, -0.5); c != stops[0].Color {
		t.Errorf("below range must equal first stop, got %+v", c)
	}
	if c := sampleGradientStops(stops, 0); c != stops[0].Color {
		t.Errorf("t=0 must equal first stop, got %+v", c)
	}
	if c := sampleGradientStops(stops, 1); c != stops[1].Color {
		t.Errorf("t=1 must equal last stop, got %+v", c)
	}
	if c := sampleGradientStops(stops, 2.5); c != stops[1].Color {
		t.Errorf("above range must equal last stop, got %+v", c)
	}

	// Coincident stops form a deterministic hard transition; the exact
	// boundary uses the incoming (first) stop and the result is stable.
	hard := []GradientStop{
		{Offset: 0.5, Color: ggRGBA(1, 0, 0, 1)},
		{Offset: 0.5, Color: ggRGBA(0, 1, 0, 1)},
	}
	at := sampleGradientStops(hard, 0.5)
	if at.R < 0.99 {
		t.Errorf("exact coincident boundary should use first (incoming) stop, got %+v", at)
	}
	if sampleGradientStops(hard, 0.5) != at {
		t.Error("coincident boundary sampling is not stable")
	}

	// Empty / single stop stability.
	if c := sampleGradientStops(nil, 0.3); c.A != 0 {
		t.Errorf("empty ramp must be transparent, got %+v", c)
	}
	one := []GradientStop{{Offset: 0.4, Color: ggRGBA(0.2, 0.3, 0.4, 0.5)}}
	if c := sampleGradientStops(one, 9); c != one[0].Color {
		t.Errorf("single stop must be constant, got %+v", c)
	}
}

func TestTransparentStopInterpolation(t *testing.T) {
	// Red opaque -> transparent: premultiplied interpolation keeps the
	// midpoint red channel at full contribution with half alpha and no
	// dark fringe.
	stops := []GradientStop{
		{Offset: 0, Color: ggRGBA(1, 0, 0, 1)},
		{Offset: 1, Color: ggRGBA(1, 0, 0, 0)},
	}
	c := sampleGradientStops(stops, 0.5)
	if math.Abs(c.A-0.5) > 1e-9 {
		t.Errorf("mid alpha = %v, want 0.5", c.A)
	}
	if math.Abs(c.R-1) > 1e-6 {
		t.Errorf("premultiplied red should stay 1 after unpremultiply, got %v", c.R)
	}
}

func TestRenderingIsDeterministicAndFinite(t *testing.T) {
	cases := map[string]string{
		"zero_width_rect": `<svg xmlns="http://www.w3.org/2000/svg" width="40" height="40" viewBox="0 0 40 40">
<defs><linearGradient id="g"><stop offset="0" stop-color="red"/><stop offset="1" stop-color="blue"/></linearGradient></defs>
<rect x="10" y="10" width="0" height="20" fill="url(#g)"/></svg>`,
		"zero_radius_circle": `<svg xmlns="http://www.w3.org/2000/svg" width="40" height="40" viewBox="0 0 40 40">
<defs><radialGradient id="g"><stop offset="0" stop-color="white"/><stop offset="1" stop-color="black"/></radialGradient></defs>
<circle cx="20" cy="20" r="0" fill="url(#g)"/></svg>`,
		"zero_length_linear": `<svg xmlns="http://www.w3.org/2000/svg" width="40" height="40" viewBox="0 0 40 40">
<defs><linearGradient id="g" x1="0.3" y1="0.3" x2="0.3" y2="0.3"><stop offset="0" stop-color="red"/><stop offset="1" stop-color="blue"/></linearGradient></defs>
<rect width="40" height="40" fill="url(#g)"/></svg>`,
		"extreme_transform": `<svg xmlns="http://www.w3.org/2000/svg" width="40" height="40" viewBox="0 0 40 40">
<defs><linearGradient id="g" gradientTransform="matrix(1e12 0 0 1e12 1e30 0)"><stop offset="0" stop-color="red"/><stop offset="1" stop-color="blue"/></linearGradient></defs>
<rect width="40" height="40" fill="url(#g)"/></svg>`,
		"empty_stops": `<svg xmlns="http://www.w3.org/2000/svg" width="40" height="40" viewBox="0 0 40 40">
<defs><linearGradient id="g"></linearGradient></defs>
<rect width="40" height="40" fill="url(#g)"/></svg>`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			doc, _ := Parse([]byte(src))
			if doc == nil {
				t.Fatal("nil document")
			}
			a := doc.Render(40, 40)
			b := doc.Render(40, 40)
			if !bytes.Equal(a.Pix, b.Pix) {
				t.Fatal("rendering is not deterministic")
			}
			for i, v := range a.Pix {
				if math.IsNaN(float64(v)) { // bytes can't be NaN; kept for symmetry
					t.Fatalf("NaN byte at %d", i)
				}
			}
		})
	}
}

func TestGroupInheritedGradientFill(t *testing.T) {
	src := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="100" height="40" viewBox="0 0 100 40">
<defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="0">
<stop offset="0" stop-color="#ff0000"/><stop offset="1" stop-color="#0000ff"/>
</linearGradient></defs>
<g fill="url(#g)"><rect x="0" y="0" width="100" height="40"/></g>
</svg>`)
	doc, err := Parse(src)
	if err != nil {
		t.Fatalf("inherited group ref should be valid: %v", err)
	}
	img := doc.Render(100, 40)
	left := img.RGBAAt(8, 20)
	right := img.RGBAAt(91, 20)
	if left.R < 200 || left.B > 60 {
		t.Errorf("left should be reddish, got %+v", left)
	}
	if right.B < 200 || right.R > 60 {
		t.Errorf("right should be bluish, got %+v", right)
	}
}

func TestLineStrokeGradientZeroHeightBBox(t *testing.T) {
	src := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="100" height="20" viewBox="0 0 100 20">
<defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="0">
<stop offset="0" stop-color="#ff0000"/><stop offset="1" stop-color="#0000ff"/>
</linearGradient></defs>
<line x1="10" y1="10" x2="90" y2="10" stroke="url(#g)" stroke-width="6" stroke-linecap="butt"/>
</svg>`)
	doc, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	img := doc.Render(100, 20)
	left := img.RGBAAt(14, 10)
	right := img.RGBAAt(85, 10)
	if left.R < 180 || left.B > 80 {
		t.Errorf("line start should be reddish, got %+v", left)
	}
	if right.B < 180 || right.R > 80 {
		t.Errorf("line end should be bluish, got %+v", right)
	}
}

func TestRenderConvenienceReturnsImageAndGradientError(t *testing.T) {
	src := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 20 20">
<defs><linearGradient id="a" href="#cycle"><stop offset="0" stop-color="red"/></linearGradient>
<linearGradient id="cycle" href="#a"/></defs>
<rect width="20" height="20" fill="url(#a)"/></svg>`)
	img, err := Render(src, 20, 20)
	if err == nil {
		t.Fatal("expected cyclic gradient error")
	}
	if img == nil {
		t.Fatal("expected degraded image alongside the error")
	}
	if _, ok := err.(interface{ Unwrap() []error }); !ok {
		// errors.Join result exposes Unwrap() []error.
		t.Fatalf("expected joined error, got %T", err)
	}
}

func TestRepeatedRenderAtDifferentSizes(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "gradient", "radial_circle.svg"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{16, 48, 120, 300} {
		img := doc.Render(size, size)
		if img == nil || img.Bounds().Dx() != size {
			t.Fatalf("bad render at size %d", size)
		}
		// Re-rendering the same size must stay deterministic.
		again := doc.Render(size, size)
		if !bytes.Equal(img.Pix, again.Pix) {
			t.Fatalf("non-deterministic render at size %d", size)
		}
	}
}

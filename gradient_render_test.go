package gg

import (
	"math"
	"testing"
)

// gradientRenderEpsilon is the tolerance for comparing rendered pixels
// against CPU reference sampling. 8-bit quantization plus premultiplied
// round-tripping allows up to ~2/255 error.
const gradientRenderEpsilon = 0.03

// pixelMatches reports whether the rendered pixel at (x, y) matches the
// reference color within tolerance.
func pixelMatches(t *testing.T, dc *Context, x, y int, want RGBA, eps float64) {
	t.Helper()
	got := dc.pixmap.GetPixel(x, y)
	if !colorsEqual(got, want, eps) {
		t.Errorf("pixel (%d, %d) = %+v, want %+v", x, y, got, want)
	}
}

// sampleGradientAtDevice returns the reference color for a device pixel
// center by mapping it back to user space through the inverse of the
// context's total transform — the same mapping the renderer must apply.
func sampleGradientAtDevice(dc *Context, brush Brush, x, y int) RGBA {
	inv := dc.totalMatrix().Invert()
	pt := inv.TransformPoint(Pt(float64(x)+0.5, float64(y)+0.5))
	return brush.ColorAt(pt.X, pt.Y)
}

// --- Requirement: linear/radial/sweep fill matches CPU reference sampling ---

func TestGradientFillLinearMatchesCPUSampling(t *testing.T) {
	dc := NewContext(100, 100)
	grad := NewLinearGradientBrush(0, 0, 100, 0).
		AddColorStop(0, Red).
		AddColorStop(0.5, Green).
		AddColorStop(1, Blue)
	dc.SetFillBrush(grad)
	dc.DrawRectangle(0, 0, 100, 100)
	if err := dc.Fill(); err != nil {
		t.Fatal(err)
	}

	for _, x := range []int{5, 25, 50, 75, 95} {
		for _, y := range []int{10, 50, 90} {
			want := grad.ColorAt(float64(x)+0.5, float64(y)+0.5)
			pixelMatches(t, dc, x, y, want, gradientRenderEpsilon)
		}
	}
}

func TestGradientFillRadialMatchesCPUSampling(t *testing.T) {
	dc := NewContext(100, 100)
	grad := NewRadialGradientBrush(50, 50, 10, 40).
		AddColorStop(0, White).
		AddColorStop(1, Black)
	dc.SetFillBrush(grad)
	dc.DrawRectangle(0, 0, 100, 100)
	if err := dc.Fill(); err != nil {
		t.Fatal(err)
	}

	for _, p := range [][2]int{{50, 50}, {70, 50}, {50, 80}, {20, 20}, {95, 95}} {
		want := grad.ColorAt(float64(p[0])+0.5, float64(p[1])+0.5)
		pixelMatches(t, dc, p[0], p[1], want, gradientRenderEpsilon)
	}
}

func TestGradientFillSweepMatchesCPUSampling(t *testing.T) {
	dc := NewContext(100, 100)
	grad := NewSweepGradientBrush(50, 50, 0).
		AddColorStop(0, Red).
		AddColorStop(0.5, Green).
		AddColorStop(1, Red)
	dc.SetFillBrush(grad)
	dc.DrawRectangle(0, 0, 100, 100)
	if err := dc.Fill(); err != nil {
		t.Fatal(err)
	}

	// Right of center (angle 0) is red-ish, left of center (angle Pi) is green-ish.
	right := dc.pixmap.GetPixel(90, 50)
	if right.R < 0.8 {
		t.Errorf("sweep right pixel = %+v, want mostly red", right)
	}
	left := dc.pixmap.GetPixel(10, 50)
	if left.G < 0.7 {
		t.Errorf("sweep left pixel = %+v, want mostly green", left)
	}
	for _, p := range [][2]int{{75, 75}, {25, 75}, {50, 10}} {
		want := grad.ColorAt(float64(p[0])+0.5, float64(p[1])+0.5)
		pixelMatches(t, dc, p[0], p[1], want, gradientRenderEpsilon)
	}
}

// --- Requirement: device points map to gradient space via current transform ---

func TestGradientFillUnderScaleTransform(t *testing.T) {
	dc := NewContext(200, 200)
	dc.Scale(2, 2)
	grad := NewLinearGradientBrush(0, 0, 100, 0).
		AddColorStop(0, Red).
		AddColorStop(1, Blue)
	dc.SetFillBrush(grad)
	dc.DrawRectangle(0, 0, 100, 100)
	if err := dc.Fill(); err != nil {
		t.Fatal(err)
	}

	// Device (100, y) is user (50, y/2): mid-gradient, not the end color.
	for _, p := range [][2]int{{20, 100}, {100, 100}, {180, 100}, {100, 40}} {
		want := sampleGradientAtDevice(dc, grad, p[0], p[1])
		pixelMatches(t, dc, p[0], p[1], want, gradientRenderEpsilon)
	}

	mid := dc.pixmap.GetPixel(100, 100)
	if mid.B > 0.9 || mid.R > 0.9 {
		t.Errorf("mid-gradient pixel = %+v, want a blend of red and blue", mid)
	}
}

func TestGradientFillUnderRotatedPath(t *testing.T) {
	dc := NewContext(200, 200)
	// Rotate 90 degrees around the canvas center.
	dc.Translate(100, 100)
	dc.Rotate(math.Pi / 2)
	dc.Translate(-100, -100)

	grad := NewLinearGradientBrush(0, 0, 200, 0). // left-to-right in user space
							AddColorStop(0, Red).
							AddColorStop(1, Blue)
	dc.SetFillBrush(grad)
	dc.DrawRectangle(0, 0, 200, 200)
	if err := dc.Fill(); err != nil {
		t.Fatal(err)
	}

	// After a 90-degree rotation the gradient runs top-to-bottom on device.
	for _, p := range [][2]int{{100, 20}, {100, 100}, {100, 180}, {50, 60}} {
		want := sampleGradientAtDevice(dc, grad, p[0], p[1])
		pixelMatches(t, dc, p[0], p[1], want, gradientRenderEpsilon)
	}

	top := dc.pixmap.GetPixel(100, 20)
	bottom := dc.pixmap.GetPixel(100, 180)
	if math.Abs(top.R-bottom.R) < 0.3 {
		t.Errorf("rotated gradient: top=%+v bottom=%+v, want distinct colors", top, bottom)
	}
}

func TestGradientFillUnderInvertedTransform(t *testing.T) {
	dc := NewContext(100, 100)
	// Mirror X: user x=0 maps to device x=100.
	dc.Translate(100, 0)
	dc.Scale(-1, 1)

	grad := NewLinearGradientBrush(0, 0, 100, 0).
		AddColorStop(0, Red).
		AddColorStop(1, Blue)
	dc.SetFillBrush(grad)
	dc.DrawRectangle(0, 0, 100, 100)
	if err := dc.Fill(); err != nil {
		t.Fatal(err)
	}

	for _, p := range [][2]int{{10, 50}, {50, 50}, {90, 50}} {
		want := sampleGradientAtDevice(dc, grad, p[0], p[1])
		pixelMatches(t, dc, p[0], p[1], want, gradientRenderEpsilon)
	}

	// Mirrored: device left shows the gradient end (blue), device right the start (red).
	left := dc.pixmap.GetPixel(10, 50)
	right := dc.pixmap.GetPixel(90, 50)
	if left.B < 0.8 || right.R < 0.8 {
		t.Errorf("inverted transform: left=%+v right=%+v, want blue/red", left, right)
	}
}

func TestGradientFillNonZeroBounds(t *testing.T) {
	// Gradient anchored away from the origin; shape offset from gradient origin.
	dc := NewContext(200, 200)
	grad := NewLinearGradientBrush(50, 50, 150, 50).
		AddColorStop(0, Red).
		AddColorStop(1, Blue)
	dc.SetFillBrush(grad)
	dc.DrawRectangle(50, 50, 100, 100)
	if err := dc.Fill(); err != nil {
		t.Fatal(err)
	}

	for _, p := range [][2]int{{60, 100}, {100, 100}, {140, 100}} {
		want := grad.ColorAt(float64(p[0])+0.5, float64(p[1])+0.5)
		pixelMatches(t, dc, p[0], p[1], want, gradientRenderEpsilon)
	}
}

// --- Requirement: semi-transparent stops composite over a checkerboard ---

func TestGradientTransparentStopsOverCheckerboard(t *testing.T) {
	dc := NewContext(64, 64)

	// Checkerboard background: 8x8 squares of opaque black and white.
	const sq = 8
	for y := 0; y < 64/sq; y++ {
		for x := 0; x < 64/sq; x++ {
			if (x+y)%2 == 0 {
				dc.SetFillBrush(Solid(White))
			} else {
				dc.SetFillBrush(Solid(Black))
			}
			dc.DrawRectangle(float64(x*sq), float64(y*sq), sq, sq)
			if err := dc.Fill(); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Semi-transparent gradient: opaque red -> fully transparent.
	grad := NewLinearGradientBrush(0, 0, 64, 0).
		AddColorStop(0, RGBA{R: 1, G: 0, B: 0, A: 1}).
		AddColorStop(0.5, RGBA{R: 0, G: 1, B: 0, A: 0.5}).
		AddColorStop(1, RGBA{R: 0, G: 0, B: 1, A: 0})
	dc.SetFillBrush(grad)
	dc.DrawRectangle(0, 0, 64, 64)
	if err := dc.Fill(); err != nil {
		t.Fatal(err)
	}

	// Verify compositing against a straight-alpha source-over reference.
	srcOver := func(src, dst RGBA) RGBA {
		outA := src.A + dst.A*(1-src.A)
		if outA <= 0 {
			return RGBA{}
		}
		return RGBA{
			R: (src.R*src.A + dst.R*dst.A*(1-src.A)) / outA,
			G: (src.G*src.A + dst.G*dst.A*(1-src.A)) / outA,
			B: (src.B*src.A + dst.B*dst.A*(1-src.A)) / outA,
			A: outA,
		}
	}

	for _, p := range [][2]int{{4, 4}, {12, 12}, {32, 20}, {52, 40}, {60, 60}} {
		bg := Black
		if ((p[0]/sq)+(p[1]/sq))%2 == 0 {
			bg = White
		}
		src := grad.ColorAt(float64(p[0])+0.5, float64(p[1])+0.5)
		want := srcOver(src, bg)
		pixelMatches(t, dc, p[0], p[1], want, 0.05)
	}

	// The transparent end must let the checkerboard show through.
	got := dc.pixmap.GetPixel(62, 4)
	bg := White
	if ((62/sq)+(4/sq))%2 != 0 {
		bg = Black
	}
	if !colorsEqual(got, bg, 0.1) {
		t.Errorf("transparent end pixel = %+v, want background %+v", got, bg)
	}
}

// --- Requirement: gradients compose with stroke, clip, and opacity ---

func TestGradientStrokeUnderTransform(t *testing.T) {
	dc := NewContext(200, 200)
	dc.Scale(2, 2)
	grad := NewLinearGradientBrush(0, 0, 100, 0).
		AddColorStop(0, Red).
		AddColorStop(1, Blue)
	dc.SetStrokeBrush(grad)
	dc.SetLineWidth(8)
	dc.MoveTo(0, 50)
	dc.LineTo(100, 50)
	if err := dc.Stroke(); err != nil {
		t.Fatal(err)
	}

	// Stroke center line is device y=100; sample along it.
	for _, x := range []int{20, 100, 180} {
		want := sampleGradientAtDevice(dc, grad, x, 100)
		pixelMatches(t, dc, x, 100, want, gradientRenderEpsilon)
	}
}

func TestGradientFillWithClip(t *testing.T) {
	dc := NewContext(100, 100)
	grad := NewLinearGradientBrush(0, 0, 100, 0).
		AddColorStop(0, Red).
		AddColorStop(1, Blue)

	// Clip to the left half, then fill the whole canvas.
	dc.DrawRectangle(0, 0, 50, 100)
	dc.Clip()

	dc.SetFillBrush(grad)
	dc.DrawRectangle(0, 0, 100, 100)
	if err := dc.Fill(); err != nil {
		t.Fatal(err)
	}

	// Inside the clip: gradient. Outside: untouched (transparent black).
	pixelMatches(t, dc, 25, 50, grad.ColorAt(25.5, 50.5), gradientRenderEpsilon)
	pixelMatches(t, dc, 75, 50, RGBA{}, gradientRenderEpsilon)
}

func TestGradientFillWithOpacityLayer(t *testing.T) {
	dc := NewContext(100, 100)
	// Opaque white background.
	dc.SetFillBrush(Solid(White))
	dc.DrawRectangle(0, 0, 100, 100)
	if err := dc.Fill(); err != nil {
		t.Fatal(err)
	}

	grad := NewLinearGradientBrush(0, 0, 100, 0).
		AddColorStop(0, Black).
		AddColorStop(1, Black)

	dc.PushLayer(BlendNormal, 0.5)
	dc.SetFillBrush(grad)
	dc.DrawRectangle(0, 0, 100, 100)
	if err := dc.Fill(); err != nil {
		t.Fatal(err)
	}
	dc.PopLayer()

	// Black at 50% opacity over white = 50% gray everywhere.
	got := dc.pixmap.GetPixel(50, 50)
	gray := RGBA{R: 0.5, G: 0.5, B: 0.5, A: 1}
	if !colorsEqual(got, gray, 0.05) {
		t.Errorf("opacity layer pixel = %+v, want %+v", got, gray)
	}
}

// --- Requirement: degenerate and invalid input handling ---

func TestGradientDegenerateRadiusRendersSolid(t *testing.T) {
	cases := []struct {
		name                 string
		startR, endR         float64
	}{
		{"zero radius", 0, 0},
		{"equal radii", 30, 30},
		{"reversed radii", 50, 10},
		{"negative end radius", 10, -5},
		{"negative start radius", -5, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dc := NewContext(60, 60)
			grad := NewRadialGradientBrush(30, 30, tc.startR, tc.endR).
				AddColorStop(0, Red).
				AddColorStop(1, Blue)
			dc.SetFillBrush(grad)
			dc.DrawRectangle(0, 0, 60, 60)
			if err := dc.Fill(); err != nil {
				t.Fatal(err)
			}
			// Degenerate geometry renders the first stop as a stable solid —
			// no stripes, no out-of-range values.
			for _, p := range [][2]int{{5, 5}, {30, 30}, {55, 55}} {
				pixelMatches(t, dc, p[0], p[1], Red, gradientRenderEpsilon)
			}
		})
	}
}

func TestGradientEmptyStopsLeavesBackground(t *testing.T) {
	dc := NewContext(20, 20)
	dc.SetFillBrush(Solid(White))
	dc.DrawRectangle(0, 0, 20, 20)
	if err := dc.Fill(); err != nil {
		t.Fatal(err)
	}

	// Gradient with no stops samples Transparent — fill is a no-op.
	dc.SetFillBrush(NewLinearGradientBrush(0, 0, 20, 0))
	dc.DrawRectangle(0, 0, 20, 20)
	if err := dc.Fill(); err != nil {
		t.Fatal(err)
	}
	pixelMatches(t, dc, 10, 10, White, gradientRenderEpsilon)
}

func TestGradientInvalidStopOffsets(t *testing.T) {
	// NaN/Inf offsets are dropped; remaining stops still form a valid ramp.
	grad := NewLinearGradientBrush(0, 0, 100, 0).
		AddColorStop(0, Red).
		AddColorStop(math.NaN(), Green).
		AddColorStop(math.Inf(1), Green).
		AddColorStop(1, Blue)

	if got := grad.ColorAt(0, 0); !colorsEqual(got, Red, gradientEpsilon) {
		t.Errorf("ColorAt(0,0) = %+v, want Red", got)
	}
	if got := grad.ColorAt(100, 0); !colorsEqual(got, Blue, gradientEpsilon) {
		t.Errorf("ColorAt(100,0) = %+v, want Blue", got)
	}
}

func TestGradientNonFiniteCoordinates(t *testing.T) {
	// Non-finite gradient geometry degrades to the first stop color.
	lin := NewLinearGradientBrush(math.NaN(), 0, 100, 0).
		AddColorStop(0, Red).
		AddColorStop(1, Blue)
	if got := lin.ColorAt(50, 50); !colorsEqual(got, Red, gradientEpsilon) {
		t.Errorf("linear NaN start = %+v, want Red", got)
	}

	rad := NewRadialGradientBrush(math.Inf(1), 50, 0, 50).
		AddColorStop(0, Red).
		AddColorStop(1, Blue)
	if got := rad.ColorAt(50, 50); !colorsEqual(got, Red, gradientEpsilon) {
		t.Errorf("radial Inf center = %+v, want Red", got)
	}

	sweep := NewSweepGradientBrush(math.NaN(), 50, 0).
		AddColorStop(0, Red).
		AddColorStop(1, Blue)
	if got := sweep.ColorAt(50, 50); !colorsEqual(got, Red, gradientEpsilon) {
		t.Errorf("sweep NaN center = %+v, want Red", got)
	}

	// Sampling at non-finite points must not produce NaN colors.
	finite := NewLinearGradientBrush(0, 0, 100, 0).
		AddColorStop(0, Red).
		AddColorStop(1, Blue)
	got := finite.ColorAt(math.NaN(), math.Inf(-1))
	if math.IsNaN(got.R) || math.IsNaN(got.G) || math.IsNaN(got.B) || math.IsNaN(got.A) {
		t.Errorf("ColorAt(NaN, -Inf) = %+v, want finite color", got)
	}
}

func TestGradientHugeRange(t *testing.T) {
	dc := NewContext(20, 20)
	// Gradient endpoints far outside the canvas: pad extend clamps, no overflow.
	grad := NewLinearGradientBrush(-1e9, 0, 1e9, 0).
		AddColorStop(0, Red).
		AddColorStop(1, Blue)
	dc.SetFillBrush(grad)
	dc.DrawRectangle(0, 0, 20, 20)
	if err := dc.Fill(); err != nil {
		t.Fatal(err)
	}
	// t ≈ 0.5 across the whole canvas → uniform mid color, no stripes.
	want := grad.ColorAt(10.5, 10.5)
	for _, p := range [][2]int{{2, 2}, {10, 10}, {18, 18}} {
		pixelMatches(t, dc, p[0], p[1], want, gradientRenderEpsilon)
	}
}

func TestGradientUnknownExtendModeDegradesToPad(t *testing.T) {
	grad := NewLinearGradientBrush(0, 0, 100, 0).
		AddColorStop(0, Red).
		AddColorStop(1, Blue)
	grad.SetExtend(ExtendMode(99))

	// Unknown spread mode clamps like ExtendPad.
	if got := grad.ColorAt(-50, 0); !colorsEqual(got, Red, gradientEpsilon) {
		t.Errorf("unknown extend before start = %+v, want Red", got)
	}
	if got := grad.ColorAt(150, 0); !colorsEqual(got, Blue, gradientEpsilon) {
		t.Errorf("unknown extend after end = %+v, want Blue", got)
	}
}

func TestGradientTinyGeometry(t *testing.T) {
	dc := NewContext(20, 20)
	grad := NewLinearGradientBrush(0, 0, 20, 0).
		AddColorStop(0, Red).
		AddColorStop(1, Blue)
	dc.SetFillBrush(grad)
	// 1x1 pixel shape: must render within the gradient range, no panic.
	dc.DrawRectangle(10, 10, 1, 1)
	if err := dc.Fill(); err != nil {
		t.Fatal(err)
	}
	got := dc.pixmap.GetPixel(10, 10)
	if got.A < 0.5 {
		t.Errorf("tiny geometry pixel = %+v, want substantial coverage", got)
	}
	// Color must be a red-blue blend (no out-of-range values).
	if got.G > 0.1 || got.B < 0.2 || got.R < 0.2 {
		t.Errorf("tiny geometry pixel = %+v, want a red-blue blend", got)
	}
}

// --- Requirement: default solid behavior unchanged ---

func TestSolidFillUnchangedByGradientTransform(t *testing.T) {
	dc := NewContext(50, 50)
	dc.Scale(2, 2)
	dc.SetFillBrush(Solid(RGBA{R: 0.25, G: 0.5, B: 0.75, A: 1}))
	dc.DrawRectangle(0, 0, 25, 25)
	if err := dc.Fill(); err != nil {
		t.Fatal(err)
	}
	pixelMatches(t, dc, 25, 25, RGBA{R: 0.25, G: 0.5, B: 0.75, A: 1}, gradientRenderEpsilon)
}

package capgate

import (
	"image"
	"image/color"
	"testing"

	"github.com/gogpu/gg/recording"
	"github.com/gogpu/gg/recording/backends/raster"
)

// RasterSubject returns a gate subject wrapping the real CPU raster backend
// through the recording instrumentation. It never touches a GPU.
func RasterSubject(scale float64) Subject {
	if scale <= 0 {
		scale = 1
	}
	return Subject{
		Name: "raster-cpu",
		New: func() recording.Backend {
			return NewInstrumented("raster-cpu", raster.NewBackendWithScale(scale))
		},
	}
}

// TestRasterBackendPassesCapabilityGate runs the full matrix against the real
// pure-CPU raster backend and asserts there are no error-severity findings.
func TestRasterBackendPassesCapabilityGate(t *testing.T) {
	subj := RasterSubject(1)
	rep := Run([]Subject{subj}, BuildMatrix(MatrixOptions{}), nil)
	if len(rep.Results) == 0 {
		t.Fatal("no results")
	}
	for _, res := range rep.Results {
		for _, f := range res.Findings {
			if f.Severity == SeverityError {
				t.Errorf("raster case %s: [%s] %s", res.CaseID, f.Code, f.Message)
			}
		}
	}
	for _, st := range rep.Stability {
		if !st.Stable {
			t.Errorf("raster capability profile unstable: %v", st.Profiles)
		}
	}
	if rep.Summary.ErrorFindings != 0 {
		t.Fatalf("raster gate has %d error findings", rep.Summary.ErrorFindings)
	}
}

// TestRasterDrawImagePixels is a pixel-level regression for the class of
// "call logged but command silently produces no output" defect: the raster
// DrawImage implementation must actually paint the image.
func TestRasterDrawImagePixels(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: 220, G: 20, B: 20, A: 255})
		}
	}
	rec := recording.NewRecorder(64, 64)
	rec.DrawImageScaled(img, 10, 10, 24, 24)

	b := raster.NewBackend()
	if err := rec.FinishRecording().Playback(b); err != nil {
		t.Fatalf("playback: %v", err)
	}
	rgba := toRGBA(t, b.Image())
	var red int
	for y := 10; y < 34; y++ {
		for x := 10; x < 34; x++ {
			c := rgba.RGBAAt(x, y)
			if c.R > 150 && c.G < 90 && c.B < 90 && c.A > 200 {
				red++
			}
		}
	}
	if red == 0 {
		t.Fatal("DrawImage produced no red pixels: silent image drop")
	}
}

func toRGBA(t *testing.T, img image.Image) *image.RGBA {
	t.Helper()
	if r, ok := img.(*image.RGBA); ok {
		return r
	}
	b := img.Bounds()
	out := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			out.Set(x, y, img.At(x, y))
		}
	}
	return out
}

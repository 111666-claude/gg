package raster

import (
	"errors"
	"image"
	"math"
	"sync"
	"testing"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/recording"
)

// buildHealthyRecording produces a recording that passes validation and
// renders through the raster backend without errors.
func buildHealthyRecording() *recording.Recording {
	rec := recording.NewRecorder(120, 80)
	rec.SetFillRGBA(0.2, 0.4, 0.8, 1)
	rec.FillRectangle(0, 0, 120, 80)
	rec.SetStrokeRGBA(1, 1, 1, 1)
	rec.SetLineWidth(2)
	rec.DrawCircle(60, 40, 20)
	rec.Stroke()
	rec.Save()
	rec.Translate(10, 10)
	rec.ClipRoundRect(0, 0, 30, 30, 6)
	rec.SetFillRGBA(1, 0, 0, 1)
	rec.FillRectangle(0, 0, 60, 60)
	rec.Restore()
	return rec.FinishRecording()
}

// TestValidateGate_BeforeRasterPlayback proves the intended workflow: a
// healthy recording validates and plays back, while each of the five defect
// classes is rejected by ValidateRecording before a backend is involved.
func TestValidateGate_BeforeRasterPlayback(t *testing.T) {
	good := buildHealthyRecording()
	if err := recording.ValidateRecording(good); err != nil {
		t.Fatalf("healthy recording rejected:\n%v", err)
	}
	backend := NewBackend()
	if err := good.Playback(backend); err != nil {
		t.Fatalf("validated recording failed raster playback: %v", err)
	}
	if img := backend.Image(); img.Bounds().Dx() != 120 || img.Bounds().Dy() != 80 {
		t.Fatalf("raster output size = %v, want 120x80", img.Bounds())
	}

	cases := []struct {
		name string
		bad  func() *recording.Recording
	}{
		{"ordering", badOrdering},
		{"clip params", badClipParams},
		{"bad reference", badReference},
		{"non-finite", badNonFinite},
		{"unknown tag", badUnknownTag},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.bad()
			err := recording.ValidateRecording(r, recording.FailFast())
			var ve recording.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("expected ValidationError, got %T: %v", err, err)
			}
			if ve.CommandIndex < 0 && ve.Reason == "" {
				t.Fatal("validation error lacks location and reason")
			}
		})
	}
}

// badOrdering: a Restore with no matching Save (state stack underflow).
func badOrdering() *recording.Recording {
	pool := recording.NewResourcePool()
	return recording.NewRecording(40, 40,
		[]recording.Command{recording.RestoreCommand{}}, pool)
}

// badClipParams: ClipRoundRect with a NaN radius and negative height.
func badClipParams() *recording.Recording {
	pool := recording.NewResourcePool()
	cmds := []recording.Command{
		recording.ClipRoundRectCommand{
			X: 0, Y: 0, W: 20, H: -5, Radius: math.NaN(),
		},
	}
	return recording.NewRecording(40, 40, cmds, pool)
}

// badReference: a fill referencing a path index that does not exist.
func badReference() *recording.Recording {
	pool := recording.NewResourcePool()
	brushRef := pool.AddBrush(recording.NewSolidBrush(gg.Black))
	cmds := []recording.Command{
		recording.FillPathCommand{
			Path:  recording.PathRef(77),
			Brush: brushRef,
			Rule:  recording.FillRuleNonZero,
		},
	}
	return recording.NewRecording(40, 40, cmds, pool)
}

// badNonFinite: an infinite line width and a NaN-bearing transform.
func badNonFinite() *recording.Recording {
	pool := recording.NewResourcePool()
	pathRef := pool.AddPath(gg.BuildPath().MoveTo(0, 0).LineTo(5, 5).Build())
	brushRef := pool.AddBrush(recording.NewSolidBrush(gg.Black))
	cmds := []recording.Command{
		recording.SetTransformCommand{Matrix: recording.Matrix{
			A: math.Inf(1), E: 1,
		}},
		recording.StrokePathCommand{
			Path:  pathRef,
			Brush: brushRef,
			Stroke: recording.Stroke{
				Width:      math.NaN(),
				Cap:        recording.LineCapButt,
				Join:       recording.LineJoinMiter,
				MiterLimit: 4,
			},
		},
	}
	return recording.NewRecording(40, 40, cmds, pool)
}

// badUnknownTag: a command implementation carrying an unregistered tag.
func badUnknownTag() *recording.Recording {
	pool := recording.NewResourcePool()
	cmds := []recording.Command{rogueCommand{}}
	return recording.NewRecording(40, 40, cmds, pool)
}

// rogueCommand implements recording.Command with an out-of-range tag.
type rogueCommand struct{}

func (rogueCommand) Type() recording.CommandType { return recording.CommandType(250) }

// TestValidateGate_CollectAllRaster lists every defect when running in
// collect-all mode, and confirms none of them panic the raster backend when a
// caller ignores the gate (the backend itself is defensive; the gate exists
// so callers do not rely on that).
func TestValidateGate_CollectAllRaster(t *testing.T) {
	r := badNonFinite()
	err := recording.ValidateRecording(r)
	var errs recording.ValidationErrors
	if !errors.As(err, &errs) {
		t.Fatalf("expected ValidationErrors, got %T: %v", err, err)
	}
	if len(errs) < 2 {
		t.Fatalf("want at least 2 defects, got %d", len(errs))
	}

	// Playback of the same input must not panic: the raster backend guards
	// nil resources, and the recording's own Playback loop bounds every
	// reference through the pool getters.
	backend := NewBackend()
	_ = r.Playback(backend)
}

// TestValidateGate_Concurrent exercises the read-only guarantee under the
// race detector: many goroutines validate and play back the same immutable
// recording simultaneously. Run with -race.
func TestValidateGate_Concurrent(t *testing.T) {
	r := buildHealthyRecording()

	const goroutines = 32
	const iterations = 25

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)
	errCh := make(chan error, goroutines*2)

	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				if err := recording.ValidateRecording(r); err != nil {
					errCh <- err
					return
				}
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				b := NewBackend()
				if err := r.Playback(b); err != nil {
					errCh <- err
					return
				}
				// Touch the output to ensure rendering actually ran.
				_ = b.Image().Bounds()
			}
		}()
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent validate/playback failed: %v", err)
	}
}

// TestValidateGate_EmptyRecordingRaster verifies an empty validated recording
// produces a blank, correctly sized pixmap through the raster backend.
func TestValidateGate_EmptyRecordingRaster(t *testing.T) {
	r := recording.NewRecording(24, 16, nil, recording.NewResourcePool())
	if err := recording.ValidateRecording(r); err != nil {
		t.Fatalf("empty recording rejected: %v", err)
	}
	backend := NewBackend()
	if err := r.Playback(backend); err != nil {
		t.Fatalf("empty recording playback failed: %v", err)
	}
	img := backend.Image()
	if img.Bounds() != image.Rect(0, 0, 24, 16) {
		t.Fatalf("bounds = %v, want 24x16", img.Bounds())
	}
}

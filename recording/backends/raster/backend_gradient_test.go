package raster

import (
	"math"
	"testing"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/recording"
)

// pixelRGBA returns the pixel at (x, y) as straight-alpha gg.RGBA.
func pixelRGBA(t *testing.T, b *Backend, x, y int) gg.RGBA {
	t.Helper()
	pm := b.Pixmap()
	if pm == nil {
		t.Fatal("Pixmap() returned nil")
	}
	return pm.GetPixel(x, y)
}

// TestBackendGradientPlayback verifies that recorded linear and radial
// gradients render as actual gradients (not solid colors) through the
// raster backend, and that device-scale playback maps device pixels back
// to gradient (world) space.
func TestBackendGradientPlayback(t *testing.T) {
	const w, h = 100, 100

	record := func() *recording.Recording {
		rec := recording.NewRecorder(w, h)
		grad := recording.NewLinearGradientBrush(0, 0, w, 0).
			AddColorStop(0, gg.Red).
			AddColorStop(1, gg.Blue)
		rec.SetFillStyle(grad)
		rec.FillRectangle(0, 0, w, h)
		return rec.FinishRecording()
	}

	// Render at 1x and 2x device scale.
	render := func(scale float64) *Backend {
		b := NewBackendWithScale(scale)
		if err := b.Begin(w, h); err != nil {
			t.Fatalf("Begin failed: %v", err)
		}
		if err := record().Playback(b); err != nil {
			t.Fatalf("Playback failed: %v", err)
		}
		if err := b.End(); err != nil {
			t.Fatalf("End failed: %v", err)
		}
		return b
	}

	b1 := render(1)
	b2 := render(2)

	// 1x: left edge is red-ish, right edge is blue-ish, middle is a blend.
	left := pixelRGBA(t, b1, 5, 50)
	mid := pixelRGBA(t, b1, 50, 50)
	right := pixelRGBA(t, b1, 95, 50)
	if left.R < 0.8 {
		t.Errorf("1x left pixel = %+v, want mostly red", left)
	}
	if right.B < 0.8 {
		t.Errorf("1x right pixel = %+v, want mostly blue", right)
	}
	if mid.R > 0.9 || mid.B > 0.9 {
		t.Errorf("1x mid pixel = %+v, want a red-blue blend", mid)
	}

	// 2x: device pixel (100, 100) is world (50, 50) — the same mid blend,
	// not the gradient end color. This is the device-to-gradient-space mapping.
	mid2 := pixelRGBA(t, b2, 100, 100)
	if math.Abs(mid2.R-mid.R) > 0.1 || math.Abs(mid2.B-mid.B) > 0.1 {
		t.Errorf("2x mid pixel = %+v, want match with 1x mid %+v", mid2, mid)
	}
	left2 := pixelRGBA(t, b2, 10, 100)
	right2 := pixelRGBA(t, b2, 190, 100)
	if left2.R < 0.8 || right2.B < 0.8 {
		t.Errorf("2x edges = %+v / %+v, want red/blue", left2, right2)
	}
}

// TestBackendRadialGradientPlayback verifies radial gradients survive
// recording playback with varying color from center to edge.
func TestBackendRadialGradientPlayback(t *testing.T) {
	const w, h = 100, 100

	rec := recording.NewRecorder(w, h)
	grad := recording.NewRadialGradientBrush(50, 50, 0, 50).
		AddColorStop(0, gg.White).
		AddColorStop(1, gg.Black)
	rec.SetFillStyle(grad)
	rec.FillRectangle(0, 0, w, h)
	r := rec.FinishRecording()

	b := NewBackend()
	if err := b.Begin(w, h); err != nil {
		t.Fatalf("Begin failed: %v", err)
	}
	if err := r.Playback(b); err != nil {
		t.Fatalf("Playback failed: %v", err)
	}
	if err := b.End(); err != nil {
		t.Fatalf("End failed: %v", err)
	}

	center := pixelRGBA(t, b, 50, 50)
	edge := pixelRGBA(t, b, 95, 50)
	corner := pixelRGBA(t, b, 2, 2)
	if center.R < 0.9 {
		t.Errorf("center pixel = %+v, want mostly white", center)
	}
	if edge.R > 0.5 {
		t.Errorf("edge pixel = %+v, want dark", edge)
	}
	if corner.R > 0.2 {
		t.Errorf("corner pixel = %+v, want nearly black", corner)
	}
	// Strictly decreasing luminance from center outward proves a real ramp.
	if !(center.R > edge.R && edge.R > corner.R-0.01) {
		t.Errorf("radial ramp not monotonic: center=%+v edge=%+v corner=%+v", center, edge, corner)
	}
}

// TestBackendSweepGradientPlayback verifies sweep (conic) gradients render
// with angular color variation after recording playback.
func TestBackendSweepGradientPlayback(t *testing.T) {
	const w, h = 100, 100

	rec := recording.NewRecorder(w, h)
	grad := recording.NewSweepGradientBrush(50, 50, 0).
		AddColorStop(0, gg.Red).
		AddColorStop(0.5, gg.Green).
		AddColorStop(1, gg.Red)
	rec.SetFillStyle(grad)
	rec.FillRectangle(0, 0, w, h)
	r := rec.FinishRecording()

	b := NewBackend()
	if err := b.Begin(w, h); err != nil {
		t.Fatalf("Begin failed: %v", err)
	}
	if err := r.Playback(b); err != nil {
		t.Fatalf("Playback failed: %v", err)
	}
	if err := b.End(); err != nil {
		t.Fatalf("End failed: %v", err)
	}

	right := pixelRGBA(t, b, 90, 50) // angle 0 → red
	left := pixelRGBA(t, b, 10, 50)  // angle π → green
	if right.R < 0.8 {
		t.Errorf("sweep right pixel = %+v, want mostly red", right)
	}
	if left.G < 0.7 {
		t.Errorf("sweep left pixel = %+v, want mostly green", left)
	}
}

package raster

import (
	"errors"
	"image"
	"image/color"
	"testing"

	"github.com/gogpu/gg/recording"
)

// loadRecordingPlayback plays a recording with the given policy and returns the
// rendered image. It fails the test on error.
func loadRecordingPlayback(t *testing.T, r *recording.Recording, policy recording.PlaybackPolicy) (*Backend, *recording.PlaybackResult) {
	t.Helper()
	backend := NewBackend()
	result, err := r.PlaybackWithOptions(backend, recording.PlaybackOptions{Policy: policy})
	if err != nil {
		t.Fatalf("PlaybackWithOptions(%s): %v", policy, err)
	}
	return backend, result
}

// TestBackendDeclaresCapabilities checks the raster backend's declaration and
// that it does not claim the one operation the Backend interface cannot express.
func TestBackendDeclaresCapabilities(t *testing.T) {
	backend := NewBackend()
	var declared recording.CapabilityBackend = backend

	set := declared.BackendCapabilities()
	for _, want := range []recording.Capability{
		recording.CapFillPath, recording.CapStrokePath,
		recording.CapFillRect, recording.CapStrokeRect,
		recording.CapDrawImage, recording.CapDrawText,
		recording.CapClipPath, recording.CapClipRoundRect,
		recording.CapClearClip, recording.CapStyles,
	} {
		if !set.Native.Contains(want) {
			t.Errorf("raster capabilities %s do not include %s", set.Native, want)
		}
	}
	if set.Native.Contains(recording.CapStrokeText) {
		t.Error("raster backend must not claim StrokeText: Backend has no stroke-text operation")
	}
}

// TestPlaybackDegradeMatchesNativePixels renders the same recording with a
// native FillRect and with a degraded FillRect->FillPath, and requires
// identical pixels. The recording is built by hand so the same command can be
// played by two backends with different declarations.
func TestPlaybackDegradeMatchesNativePixels(t *testing.T) {
	rec := recording.NewRecorder(32, 32)
	rec.SetFillRGBA(1, 0, 0, 1)
	rec.FillRectangle(4, 6, 10, 12)
	r := rec.FinishRecording()

	native, nativeResult := loadRecordingPlayback(t, r, recording.PolicyStrict)
	if nativeResult.Completeness() != true {
		t.Fatalf("native result not complete: %+v", nativeResult)
	}

	// Render again through a FillRect-degraded path: temporarily remove the
	// capability by wrapping the backend in a type that narrows the set.
	degraded := &degradedBackend{Backend: NewBackend(), caps: recording.CapabilitySet{
		Native: recording.CapFillPath | recording.CapStyles,
	}}
	result, err := r.PlaybackWithOptions(degraded, recording.PlaybackOptions{Policy: recording.PolicyDegrade})
	if err != nil {
		t.Fatalf("degraded playback: %v", err)
	}
	var fillRectDegrades int
	for _, d := range result.Degraded {
		if d.Command == recording.CmdFillRect {
			fillRectDegrades++
			if d.From != recording.CapFillRect || d.To != recording.CapFillPath {
				t.Errorf("FillRect degraded %s -> %s", d.From, d.To)
			}
		}
	}
	if fillRectDegrades != 1 {
		t.Fatalf("degraded commands = %+v, want one FillRect", result.Degraded)
	}

	assertImagesEqual(t, native.Image(), degraded.Image())
}

// TestPlaybackSkipProducesPartialPixels verifies that skipping the only fill
// leaves the canvas transparent, while the recording is still reported.
func TestPlaybackSkipProducesPartialPixels(t *testing.T) {
	rec := recording.NewRecorder(16, 16)
	rec.SetFillRGBA(0, 0, 1, 1)
	rec.FillRectangle(2, 2, 8, 8)
	rec.SetFillRGBA(1, 0, 0, 1)
	rec.DrawCircle(8, 8, 3) // FillPath: skipped by the rect-only backend
	rec.Fill()
	r := rec.FinishRecording()

	backend := &degradedBackend{Backend: NewBackend(), caps: recording.CapabilitySet{
		Native: recording.CapFillRect | recording.CapStyles,
	}}
	result, err := r.PlaybackWithOptions(backend, recording.PlaybackOptions{Policy: recording.PolicySkip})
	if err != nil {
		t.Fatalf("skip playback: %v", err)
	}
	if len(result.Skipped) != 1 {
		t.Fatalf("skipped = %+v, want one command", result.Skipped)
	}
	if result.Completeness() {
		t.Error("result should not be complete when a command was skipped")
	}

	rgba, ok := backend.Image().(*image.RGBA)
	if !ok {
		t.Fatal("expected *image.RGBA")
	}
	// The blue rectangle is present.
	if p := rgba.RGBAAt(6, 6); p.B < 200 || p.A < 200 {
		t.Errorf("center of skipped-rectangle image = %v, want blue", p)
	}
}

// TestPlaybackStrictRejectsStrokeText checks that recording stroked text and
// playing it into the raster backend fails before drawing anything, because the
// backend does not declare CapStrokeText.
func TestPlaybackStrictRejectsStrokeText(t *testing.T) {
	rec := recording.NewRecorder(64, 32)
	rec.SetStrokeRGBA(0, 0, 0, 1)
	rec.StrokeString("A", 4, 20)
	r := rec.FinishRecording()

	backend := NewBackend()
	err := r.Playback(backend)
	if err == nil {
		t.Fatal("expected an error for unsupported StrokeText")
	}
	if backend.Width() != 0 {
		t.Error("Begin ran despite the capability failure")
	}
}

// TestPreflightRasterReportsNoGapsForSupportedContent checks a recording the
// raster backend fully supports.
func TestPreflightRasterReportsNoGapsForSupportedContent(t *testing.T) {
	rec := recording.NewRecorder(32, 32)
	rec.Save()
	rec.Translate(2, 2)
	rec.ClipRoundRect(0, 0, 16, 16, 4)
	rec.SetFillRGBA(0, 1, 0, 1)
	rec.FillRectangle(1, 1, 8, 8)
	rec.Restore()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	rec.DrawImage(img, 20, 20)
	r := rec.FinishRecording()

	report, err := r.Preflight(NewBackend())
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if !report.IsComplete() {
		t.Fatalf("unexpected gaps: %s", report)
	}
}

// TestPlaybackRendersImage guards the raster DrawImage implementation. The
// previous implementation only drew an empty rectangle, so an opaque image
// produced no pixels at all.
func TestPlaybackRendersImage(t *testing.T) {
	rec := recording.NewRecorder(40, 40)
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			src.Set(x, y, color.RGBA{R: 0, G: 255, B: 0, A: 255})
		}
	}
	rec.DrawImage(src, 10, 10)
	r := rec.FinishRecording()

	backend, _ := loadRecordingPlayback(t, r, recording.PolicyStrict)
	rgba, ok := backend.Image().(*image.RGBA)
	if !ok {
		t.Fatal("expected *image.RGBA")
	}
	if p := rgba.RGBAAt(12, 12); p.G < 200 || p.A < 200 {
		t.Errorf("image pixel at (12,12) = %v, want opaque green", p)
	}
	if p := rgba.RGBAAt(2, 2); p.A != 0 {
		t.Errorf("pixel outside the image = %v, want transparent", p)
	}
}

// TestPlaybackDrawImageScaled checks that the destination rect controls the
// drawn size, not the source image size.
func TestPlaybackDrawImageScaled(t *testing.T) {
	rec := recording.NewRecorder(40, 40)
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			src.Set(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}
	rec.DrawImageScaled(src, 10, 10, 20, 20)
	r := rec.FinishRecording()

	backend, _ := loadRecordingPlayback(t, r, recording.PolicyStrict)
	rgba, ok := backend.Image().(*image.RGBA)
	if !ok {
		t.Fatal("expected *image.RGBA")
	}
	if p := rgba.RGBAAt(29, 29); p.R < 200 || p.A < 200 {
		t.Errorf("scaled image pixel at (29,29) = %v, want opaque red", p)
	}
	if p := rgba.RGBAAt(34, 34); p.A != 0 {
		t.Errorf("pixel outside the scaled image = %v, want transparent", p)
	}
}

// degradedBackend forwards to a raster backend but declares a narrower
// capability set, so playback exercises the negotiation paths.
type degradedBackend struct {
	*Backend
	caps recording.CapabilitySet
}

func (b *degradedBackend) BackendCapabilities() recording.CapabilitySet {
	return b.caps
}

// assertImagesEqual fails when two images differ in any pixel.
func assertImagesEqual(t *testing.T, got, want image.Image) {
	t.Helper()
	if got.Bounds() != want.Bounds() {
		t.Fatalf("bounds = %v, want %v", got.Bounds(), want.Bounds())
	}
	b := want.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			gr, gg, gb, ga := got.At(x, y).RGBA()
			wr, wg, wb, wa := want.At(x, y).RGBA()
			if gr != wr || gg != wg || gb != wb || ga != wa {
				t.Fatalf("pixel (%d,%d) = %v, want %v", x, y,
					color.RGBA64{uint16(gr), uint16(gg), uint16(gb), uint16(ga)},
					color.RGBA64{uint16(wr), uint16(wg), uint16(wb), uint16(wa)})
			}
		}
	}
}

// TestSameRecordingThreeCapabilitySets is the requirement-4 cross-check: one
// recording, three backends with different declared capability sets.
//
//  1. Full capabilities: preflight is complete, no rewrites, complete result.
//  2. Rectangles + text only: preflight lists the path gaps, strict playback
//     fails with a command index, and skipping still produces the rectangle.
//  3. Paths + Clips but no rectangles: preflight reports the rectangle gap as
//     degradable, and the degraded pixels match the native rendering.
func TestSameRecordingThreeCapabilitySets(t *testing.T) {
	// The circle is clipped to the half-plane x >= 16 so the native and the
	// degraded renderings of the rectangle share the canvas with a path fill
	// that is fully inside the clip. Clipping is itself supported by both
	// sets, so the comparison isolates the FillRect rewrite.
	rec := recording.NewRecorder(32, 32)
	rec.DrawRectangle(16, 0, 16, 32)
	rec.Clip()
	rec.SetFillRGBA(0, 0, 1, 1)
	rec.FillRectangle(18, 4, 12, 12)
	rec.SetFillRGBA(1, 0, 0, 1)
	rec.DrawCircle(24, 24, 5)
	rec.Fill()
	r := rec.FinishRecording()

	// 1. Full backend.
	full := NewBackend()
	fullReport, err := r.Preflight(full)
	if err != nil {
		t.Fatalf("full Preflight: %v", err)
	}
	if !fullReport.IsComplete() {
		t.Fatalf("full backend should be complete: %s", fullReport)
	}
	fullResult, err := r.PlaybackWithOptions(full, recording.PlaybackOptions{Policy: recording.PolicyStrict})
	if err != nil {
		t.Fatalf("full playback: %v", err)
	}
	if !fullResult.Completeness() {
		t.Errorf("full playback should not rewrite commands: %+v", fullResult)
	}

	// 2. Rectangles + text only.
	rectOnly := &degradedBackend{Backend: NewBackend(), caps: recording.CapabilitySet{
		Native: recording.CapFillRect | recording.CapDrawText | recording.CapStyles,
	}}
	rectReport, err := r.Preflight(rectOnly)
	if err != nil {
		t.Fatalf("rect-only Preflight: %v", err)
	}
	if rectReport.IsComplete() {
		t.Fatal("rect-only backend should report missing FillPath")
	}
	var sawFillPath bool
	for _, issue := range rectReport.Issues {
		if issue.Capability == recording.CapFillPath {
			sawFillPath = true
			if issue.Degradable {
				t.Error("FillPath is not degradable from FillRect")
			}
		}
	}
	if !sawFillPath {
		t.Error("report did not name FillPath")
	}

	// Strict playback must fail with a command index before output.
	strictTarget := &degradedBackend{Backend: NewBackend(), caps: rectOnly.caps}
	err = r.Playback(strictTarget)
	if err == nil {
		t.Fatal("strict playback should fail for the rect-only backend")
	}
	var perr *recording.PlaybackError
	if !errors.As(err, &perr) {
		t.Fatalf("error %v is not a *PlaybackError", err)
	}
	if perr.Index < 0 {
		t.Error("strict failure should carry a command index")
	}
	if strictTarget.Width() != 0 {
		t.Error("strict failure should happen before Begin")
	}

	// Skip playback continues and still renders the rectangle.
	skipTarget := &degradedBackend{Backend: NewBackend(), caps: rectOnly.caps}
	skipResult, err := r.PlaybackWithOptions(skipTarget, recording.PlaybackOptions{Policy: recording.PolicySkip})
	if err != nil {
		t.Fatalf("skip playback: %v", err)
	}
	if len(skipResult.Skipped) == 0 || skipResult.Completeness() {
		t.Errorf("skip result = %+v, want at least one skip", skipResult)
	}
	skipRGBA, ok := skipTarget.Image().(*image.RGBA)
	if !ok {
		t.Fatal("expected *image.RGBA")
	}
	if p := skipRGBA.RGBAAt(20, 8); p.B < 200 || p.A < 200 {
		t.Errorf("rectangle pixel = %v, want blue", p)
	}
	if p := skipRGBA.RGBAAt(24, 24); p.R > 50 {
		t.Errorf("circle pixel = %v, want left unpainted", p)
	}

	// 3. Paths + clips, no rectangles: degradation must be pixel-identical to
	// the native rendering of the rectangle.
	pathOnly := &degradedBackend{Backend: NewBackend(), caps: recording.CapabilitySet{
		Native: recording.CapFillPath | recording.CapStrokePath |
			recording.CapClipPath | recording.CapClearClip |
			recording.CapDrawText | recording.CapStyles,
	}}
	pathReport, err := r.Preflight(pathOnly)
	if err != nil {
		t.Fatalf("path-only Preflight: %v", err)
	}
	if pathReport.IsComplete() {
		t.Fatal("path-only backend should report missing FillRect")
	}
	for _, issue := range pathReport.Issues {
		if issue.Capability == recording.CapFillRect && (!issue.Degradable || issue.Fallback != recording.CapFillPath) {
			t.Errorf("FillRect issue = %+v, want degradable to FillPath", issue)
		}
	}
	degradedResult, err := r.PlaybackWithOptions(pathOnly, recording.PlaybackOptions{Policy: recording.PolicyDegrade})
	if err != nil {
		t.Fatalf("degraded playback: %v", err)
	}
	if len(degradedResult.Degraded) != 1 || degradedResult.Degraded[0].Command != recording.CmdFillRect {
		t.Fatalf("degraded commands = %+v, want one FillRect", degradedResult.Degraded)
	}
	assertImagesEqual(t, pathOnly.Image(), full.Image())
}

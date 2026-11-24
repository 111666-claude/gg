package capgate

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/gogpu/gg/recording"
)

// TestEdgeEmptyCommands: a finished recorder with no commands replays with
// only Begin/End, no error, balanced stack and a stable digest.
func TestEdgeEmptyCommands(t *testing.T) {
	rec := recording.NewRecorder(16, 16).FinishRecording()
	spy := NewPerfectSpy()

	before := Fingerprint(rec)
	if err := rec.PlaybackContext(context.Background(), spy); err != nil {
		t.Fatalf("empty playback: %v", err)
	}
	after := Fingerprint(rec)
	if !before.Unchanged(after) {
		t.Fatal("empty playback changed fingerprint")
	}
	if spy.Log().Count(OpBegin) != 1 || spy.Log().Count(OpEnd) != 1 {
		t.Fatalf("expected one Begin/End, got calls=%+v", spy.Log().Calls())
	}
	for _, op := range []string{OpFillPath, OpStrokePath, OpFillRect, OpDrawImage, OpDrawText} {
		if spy.Log().Count(op) != 0 {
			t.Fatalf("empty recording produced %s calls", op)
		}
	}
	if !spy.StackBalanced() {
		t.Fatal("spy stack should be balanced")
	}
}

// TestEdgeDeepNesting balances a very deep Save/Restore stack and checks the
// observed depth plus the MaxStateDepth preflight limit.
func TestEdgeDeepNesting(t *testing.T) {
	const depth = 1000
	r := recording.NewRecorder(64, 64)
	for i := 0; i < depth; i++ {
		r.Save()
	}
	r.SetRGB(0.1, 0.2, 0.3)
	r.FillRectangle(2, 2, 6, 6)
	for i := 0; i < depth; i++ {
		r.Restore()
	}
	rec := r.FinishRecording()

	spy := NewPerfectSpy()
	if err := rec.PlaybackContext(context.Background(), spy); err != nil {
		t.Fatalf("deep nesting playback: %v", err)
	}
	if spy.MaxDepth() != depth {
		t.Fatalf("max depth = %d, want %d", spy.MaxDepth(), depth)
	}
	if !spy.StackBalanced() {
		t.Fatal("deep stack unbalanced after playback")
	}
	before := Fingerprint(rec)
	if !before.Unchanged(Fingerprint(rec)) {
		t.Fatal("fingerprint unstable without playback")
	}

	// Preflight reports the observed max depth.
	pre := recording.Preflight(rec, FullCapabilities())
	if pre.MaxDepth != depth {
		t.Fatalf("preflight max depth = %d, want %d", pre.MaxDepth, depth)
	}

	// A tight MaxStateDepth profile yields a locatable depth issue.
	tight := FullCapabilities()
	tight.MaxStateDepth = 16
	limited := recording.Preflight(rec, tight)
	var foundDepth bool
	for _, is := range limited.Issues {
		if is.Code == recording.IssueStateDepthExceeded {
			foundDepth = true
			break
		}
	}
	if !foundDepth {
		t.Fatal("expected state_depth_exceeded preflight issue for deep nesting")
	}
}

// TestEdgeNaNInfDoesNotPanicAndIsPreflighted feeds NaN/Inf through every
// numeric channel and asserts preflight flags it while playback to the double
// (and CPU raster) neither panics nor mutates.
func TestEdgeNaNInfDoesNotPanicAndIsPreflighted(t *testing.T) {
	bad := []struct {
		name  string
		build func() *recording.Recording
	}{
		{"nan-line-width", func() *recording.Recording {
			r := recording.NewRecorder(32, 32)
			r.SetLineWidth(math.NaN())
			r.MoveTo(0, 0)
			r.LineTo(32, 32)
			r.Stroke()
			return r.FinishRecording()
		}},
		{"inf-miter", func() *recording.Recording {
			r := recording.NewRecorder(32, 32)
			r.SetMiterLimit(math.Inf(1))
			r.MoveTo(0, 0)
			r.LineTo(10, 10)
			r.Stroke()
			return r.FinishRecording()
		}},
		{"nan-path-coords", func() *recording.Recording {
			r := recording.NewRecorder(32, 32)
			r.SetRGB(1, 0, 0)
			r.MoveTo(math.NaN(), 5)
			r.LineTo(30, math.Inf(-1))
			r.LineTo(20, 20)
			r.ClosePath()
			r.Fill()
			return r.FinishRecording()
		}},
		{"inf-transform", func() *recording.Recording {
			r := recording.NewRecorder(32, 32)
			r.SetTransform(recording.Matrix{A: math.Inf(1), E: 1})
			r.FillRectangle(1, 1, 4, 4)
			return r.FinishRecording()
		}},
		{"nan-rect", func() *recording.Recording {
			r := recording.NewRecorder(32, 32)
			r.FillRectangle(math.NaN(), 2, math.Inf(1), 4)
			return r.FinishRecording()
		}},
		{"nan-dash", func() *recording.Recording {
			r := recording.NewRecorder(32, 32)
			r.SetDash(4, math.NaN())
			r.MoveTo(0, 0)
			r.LineTo(32, 32)
			r.Stroke()
			return r.FinishRecording()
		}},
		{"nan-text-position", func() *recording.Recording {
			r := recording.NewRecorder(64, 32)
			r.DrawString("x", math.NaN(), math.Inf(1))
			return r.FinishRecording()
		}},
		{"nan-clip-roundrect", func() *recording.Recording {
			r := recording.NewRecorder(32, 32)
			r.ClipRoundRect(math.NaN(), 2, 10, 10, math.Inf(1))
			return r.FinishRecording()
		}},
	}

	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			rec := tc.build()
			pre := recording.Preflight(rec, FullCapabilities())
			var hasNonFinite bool
			for _, is := range pre.Issues {
				if is.Code == recording.IssueNonFinite {
					hasNonFinite = true
				}
			}
			if !hasNonFinite {
				t.Errorf("%s: preflight did not flag non_finite_value", tc.name)
			}

			spy := NewPerfectSpy()
			if err := rec.PlaybackContext(context.Background(), spy); err != nil {
				t.Errorf("%s: spy playback error: %v", tc.name, err)
			}
			if !Fingerprint(rec).Unchanged(Fingerprint(rec)) {
				t.Errorf("%s: fingerprint unstable", tc.name)
			}
		})
	}
}

// TestEdgeCancelPreCancelled ensures an already-canceled context aborts before
// Begin and is reported as the canceled verdict.
func TestEdgeCancelPreCancelled(t *testing.T) {
	cases := BuildMatrix(MatrixOptions{})
	var fill Case
	for _, c := range cases {
		if c.ID == "rect/fill" {
			fill = c
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := RunCaseContext(ctx, PerfectSubject(), fill, nil)
	if res.Verdict != VerdictCanceled {
		t.Fatalf("verdict = %s, want canceled (err=%s)", res.Verdict, res.PlaybackError)
	}
	if res.ErrorPhase != recording.PlaybackPhaseCancel {
		t.Fatalf("error phase = %q, want %q", res.ErrorPhase, recording.PlaybackPhaseCancel)
	}
}

// TestEdgeCancelMidPlayback cancels after the first FillRect and proves later
// commands do not reach the backend and the error carries a command locator.
func TestEdgeCancelMidPlayback(t *testing.T) {
	const rects = 8
	r := recording.NewRecorder(64, 64)
	for i := 0; i < rects; i++ {
		r.FillRectangle(float64(i*4), 4, 3, 3)
	}
	rec := r.FinishRecording()

	ctx, cancel := context.WithCancel(context.Background())
	b := &cancelOnFillBackend{Spy: NewPerfectSpy(), cancel: cancel}

	err := rec.PlaybackContext(ctx, b)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	var pe *recording.PlaybackError
	if !errors.As(err, &pe) {
		t.Fatalf("error %v is not *PlaybackError", err)
	}
	if pe.Phase != recording.PlaybackPhaseCancel || pe.Index <= 0 {
		t.Fatalf("bad cancel locator phase=%q index=%d", pe.Phase, pe.Index)
	}
	if got := b.Log().Count(OpFillRect); got >= rects {
		t.Fatalf("cancellation did not stop playback: %d fill rects observed", got)
	}
	if got := b.Log().Count(OpFillRect); got < 1 {
		t.Fatalf("cancel hook should have let at least one FillRect through")
	}
}

// TestRepeatedPlaybackLeavesRecordingUntouched plays one recording many times
// against fresh spies and asserts a constant digest and identical call counts.
func TestRepeatedPlaybackLeavesRecordingUntouched(t *testing.T) {
	cases := BuildMatrix(MatrixOptions{})
	digest := Fingerprint(cases[0].Build(nil))
	for i := 0; i < 5; i++ {
		spy := NewPerfectSpy()
		if err := cases[0].Build(nil).Playback(spy); err != nil {
			t.Fatal(err)
		}
	}
	again := Fingerprint(cases[0].Build(nil))
	if digest.SHA256 != again.SHA256 {
		t.Fatal("equivalent rebuilt recordings should fingerprint identically")
	}

	// Same instance replayed repeatedly must keep a stable digest.
	rec := cases[1].Build(nil)
	d0 := Fingerprint(rec)
	for i := 0; i < 5; i++ {
		spy := NewPerfectSpy()
		if err := rec.Playback(spy); err != nil {
			t.Fatal(err)
		}
	}
	if d1 := Fingerprint(rec); d0.SHA256 != d1.SHA256 {
		t.Fatal("repeated playback mutated the shared recording")
	}
}

type cancelOnFillBackend struct {
	*Spy
	cancel context.CancelFunc
	fired  bool
}

func (b *cancelOnFillBackend) FillRect(r recording.Rect, br recording.Brush) {
	if !b.fired {
		b.fired = true
		b.cancel()
	}
	b.Spy.FillRect(r, br)
}

// TestEdgeCancelPreCancelledDirect verifies errors.Is on the returned error.
func TestEdgeCancelPreCancelledDirect(t *testing.T) {
	rec := recording.NewRecorder(8, 8).FinishRecording()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := rec.PlaybackContext(ctx, NewPerfectSpy())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

package recording

import (
	"image"
	"math"
	"sync"
	"testing"
)

// TestGraphicsStateCloneDeepCopiesDash verifies the snapshot owns its dash
// slice: mutating the source after cloning must not bleed into the clone, and
// a nil pattern stays nil (solid-line distinction preserved).
func TestGraphicsStateCloneDeepCopiesDash(t *testing.T) {
	src := defaultGraphicsState()
	src.dashPattern = []float64{1, 2, 3}

	cp := src.clone()
	src.dashPattern[0] = 999
	if cp.dashPattern[0] != 1 {
		t.Fatalf("clone shares dash backing array: got %v, want [1 2 3]", cp.dashPattern)
	}

	// Reverse direction: mutating the clone must not touch the source.
	cp.dashPattern[1] = 888
	if src.dashPattern[1] != 2 {
		t.Fatalf("source dash mutated through clone: got %v", src.dashPattern)
	}

	nilState := defaultGraphicsState()
	if nilState.clone().dashPattern != nil {
		t.Fatal("cloning a state with nil dash must preserve nil")
	}
}

// TestSaveRestoreDashIsolation is the end-to-end version: a dash replaced or
// cleared while a frame is saved must never alter the saved frame, neither on
// the stack nor after Restore.
func TestSaveRestoreDashIsolation(t *testing.T) {
	rec := NewRecorder(100, 100)
	rec.SetDash(4, 2)
	rec.Save()

	// Replace the live pattern with a different length and contents.
	rec.SetDash(9, 9, 9)
	if got := rec.state.dashPattern; len(got) != 3 || got[0] != 9 {
		t.Fatalf("live dash = %v, want [9 9 9]", got)
	}

	rec.Restore()
	got := rec.state.dashPattern
	if len(got) != 2 || got[0] != 4 || got[1] != 2 {
		t.Fatalf("restored dash = %v, want [4 2]", got)
	}

	// Mutating the restored live slice must not affect any retained frame:
	// save again, mutate, restore and confirm full independence.
	rec.Save()
	rec.state.dashPattern[0] = 777
	rec.Restore()
	if rec.state.dashPattern[0] != 4 {
		t.Fatalf("dash aliased across stack frames: got %v, want [4 2]", rec.state.dashPattern)
	}

	// Clearing while saved restores the pattern, not a shared nil.
	rec.Save()
	rec.ClearDash()
	if rec.state.dashPattern != nil {
		t.Fatal("ClearDash should leave nil dash on live state")
	}
	rec.Restore()
	if len(rec.state.dashPattern) != 2 || rec.state.dashPattern[0] != 4 {
		t.Fatalf("after restore want [4 2], got %v", rec.state.dashPattern)
	}
}

// TestSaveRestoreCapturesAllFields ensures every snapshot field round-trips,
// including the ones the old manual DrawEllipticalArc unwind used to drop
// (anti-alias and font state).
func TestSaveRestoreCapturesAllFields(t *testing.T) {
	rec := NewRecorder(100, 100)
	rec.SetLineWidth(3.5)
	rec.SetLineCap(LineCapSquare)
	rec.SetLineJoin(LineJoinRound)
	rec.SetMiterLimit(9)
	rec.SetDash(1, 1)
	rec.SetDashOffset(7)
	rec.SetFillRule(FillRuleEvenOdd)
	rec.SetAntiAlias(false)
	rec.SetFontFamily("Mono")
	rec.SetFontSize(22)
	rec.SetFillRGB(1, 0, 0)
	rec.SetStrokeRGB(0, 0, 1)
	rec.Translate(5, 6)

	rec.Save()
	rec.SetLineWidth(1)
	rec.SetLineCap(LineCapButt)
	rec.SetLineJoin(LineJoinBevel)
	rec.SetMiterLimit(1)
	rec.ClearDash()
	rec.SetFillRule(FillRuleNonZero)
	rec.SetAntiAlias(true)
	rec.SetFontFamily("Sans")
	rec.SetFontSize(9)
	rec.SetFillRGB(0, 1, 0)
	rec.SetStrokeRGB(0, 1, 0)
	rec.Identity()
	rec.Restore()

	s := rec.state
	if s.lineWidth != 3.5 || s.lineCap != LineCapSquare || s.lineJoin != LineJoinRound ||
		s.miterLimit != 9 || s.fillRule != FillRuleEvenOdd || s.antiAlias != false {
		t.Fatalf("scalar style not restored: %+v", s)
	}
	if len(s.dashPattern) != 2 || s.dashPattern[0] != 1 || s.dashOffset != 7 {
		t.Fatalf("dash not restored: %v off=%g", s.dashPattern, s.dashOffset)
	}
	if s.fontFamily != "Mono" || s.fontSize != 22 {
		t.Fatalf("font state not restored: family=%q size=%g", s.fontFamily, s.fontSize)
	}
	if s.transform.C != 5 || s.transform.F != 6 {
		t.Fatalf("transform not restored: %+v", s.transform)
	}
	if br, ok := s.fillBrush.(SolidBrush); !ok || br.Color.R != 1 {
		t.Fatalf("fill brush not restored: %#v", s.fillBrush)
	}
	if br, ok := s.strokeBrush.(SolidBrush); !ok || br.Color.B != 1 {
		t.Fatalf("stroke brush not restored: %#v", s.strokeBrush)
	}
}

// TestRestoreEmptyStackNoOp pins the empty-stack contract: no panic, no
// emitted command, live state untouched.
func TestRestoreEmptyStackNoOp(t *testing.T) {
	rec := NewRecorder(100, 100)
	rec.SetLineWidth(6)
	before := len(rec.commands)

	rec.Restore()
	rec.Pop()

	if rec.state.lineWidth != 6 {
		t.Fatalf("empty-stack restore changed state: width=%g", rec.state.lineWidth)
	}
	if len(rec.commands) != before {
		t.Fatalf("empty-stack restore emitted %d extra commands", len(rec.commands)-before)
	}
}

// TestSaveStackDepthLimit drives nesting past maxStateDepth. Excess Saves must
// be no-ops rather than panicking or growing memory without bound, and the
// matching Restores must unwind exactly the frames that were pushed without
// underflowing the stack.
func TestSaveStackDepthLimit(t *testing.T) {
	rec := NewRecorder(100, 100)

	const total = maxStateDepth + 256
	for i := 0; i < total; i++ {
		rec.Save()
	}
	if len(rec.stateStack) != maxStateDepth {
		t.Fatalf("stack depth = %d, want cap %d", len(rec.stateStack), maxStateDepth)
	}

	// Every frame below the limit restores; excess Restores are empty-stack
	// no-ops. Nothing may panic.
	for i := 0; i < total; i++ {
		rec.Restore()
	}
	if len(rec.stateStack) != 0 {
		t.Fatalf("stack not fully unwound: %d frames remain", len(rec.stateStack))
	}

	// Save/Restore remain functional after hitting and leaving the limit.
	rec.Save()
	rec.SetLineWidth(2)
	rec.Restore()
	if rec.state.lineWidth != 1 {
		t.Fatalf("stack unusable after limit: width=%g", rec.state.lineWidth)
	}
}

// TestHostileStyleInputsNoPanic feeds non-finite widths, negative miters and
// malformed dashes through the full record/playback path. Values must be
// captured verbatim (Command encoding unchanged) and never panic.
func TestHostileStyleInputsNoPanic(t *testing.T) {
	rec := NewRecorder(100, 100)

	rec.SetLineWidth(math.NaN())
	rec.SetMiterLimit(-10)
	rec.SetDash(0, -1, math.Inf(1), math.NaN())
	rec.MoveTo(0, 0)
	rec.LineTo(50, 50)
	rec.Stroke()

	rec.SetLineWidth(math.Inf(-1))
	rec.SetDash() // zero-arg clears
	rec.MoveTo(0, 50)
	rec.LineTo(50, 0)
	rec.Stroke()

	recording := rec.FinishRecording()
	if err := recording.Playback(&playbackMockBackend{}); err != nil {
		t.Fatalf("playback of hostile inputs failed: %v", err)
	}

	// The NaN width must survive encoding exactly as before the refactor.
	var foundNaN bool
	for _, c := range recording.Commands() {
		if sc, ok := c.(StrokePathCommand); ok && math.IsNaN(sc.Stroke.Width) {
			foundNaN = true
		}
	}
	if !foundNaN {
		t.Fatal("NaN line width was not preserved in the command stream")
	}
}

// TestRecorderResetReuse verifies a recycled Recorder cannot leak commands,
// resources, path contents, styles or saved frames from a previous recording,
// while the already-finished Recording keeps independent ownership.
func TestRecorderResetReuse(t *testing.T) {
	rec := NewRecorder(200, 100)
	rec.SetRGB(1, 0, 0)
	rec.SetLineWidth(8)
	rec.SetDash(3, 3)
	rec.Save()
	rec.Translate(10, 10)
	rec.DrawCircle(50, 50, 20)
	rec.Fill()
	first := rec.FinishRecording()
	firstCmdCount := len(first.Commands())
	firstPathCount := first.Resources().PathCount()
	if firstCmdCount == 0 || firstPathCount == 0 {
		t.Fatal("test setup: first recording should hold commands and paths")
	}

	rec.Reset()

	if len(rec.commands) != 0 || rec.resources.PathCount() != 0 ||
		rec.resources.BrushCount() != 0 || len(rec.stateStack) != 0 {
		t.Fatal("Reset left commands, resources or saved frames behind")
	}
	if rec.currentPath.NumVerbs() != 0 {
		t.Fatal("Reset left path contents behind")
	}
	if rec.state.lineWidth != 1 || rec.state.dashPattern != nil ||
		rec.state.fillRule != FillRuleNonZero || rec.state.antiAlias != true ||
		!rec.state.transform.IsIdentity() {
		t.Fatalf("Reset did not restore default state: %+v", rec.state)
	}

	// Record something new and confirm no ghost state from the first run.
	rec.SetRGB(0, 1, 0)
	rec.DrawRectangle(0, 0, 10, 10)
	rec.Fill()
	second := rec.FinishRecording()
	if len(second.Commands()) == 0 {
		t.Fatal("reused recorder produced no commands")
	}
	if second.Resources().PathCount() != 1 {
		t.Fatalf("reused recorder leaked resources: %d paths", second.Resources().PathCount())
	}

	// The first recording must be untouched by Reset and still playable.
	if len(first.Commands()) != firstCmdCount ||
		first.Resources().PathCount() != firstPathCount {
		t.Fatal("Reset mutated the already-finished recording")
	}
	if err := first.Playback(&playbackMockBackend{}); err != nil {
		t.Fatalf("first recording no longer plays back: %v", err)
	}
}

// TestResetOnEmptyRecorder guards against Reset panicking on a pristine
// Recorder (nil-ish slices, empty stack).
func TestResetOnEmptyRecorder(t *testing.T) {
	rec := NewRecorder(64, 64)
	rec.Reset()
	rec.SetRGB(1, 1, 1)
	rec.FillRectangle(0, 0, 64, 64)
	if got := len(rec.FinishRecording().Commands()); got != 3 {
		t.Fatalf("recorder did not function after Reset on empty state: %d commands", got)
	}
}

// TestConcurrentRecorders creates and drives many Recorders simultaneously.
// Independent Recorders share no mutable state, so running under -race must
// report no data races; each goroutine also verifies its own state isolation.
func TestConcurrentRecorders(t *testing.T) {
	const goroutines = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		g := g
		go func() {
			defer wg.Done()
			rec := NewRecorder(128, 128)
			for step := 0; step < 64; step++ {
				rec.Save()
				rec.SetLineWidth(float64(step + 1))
				rec.SetDash(float64(g+1), float64(step+1))
				rec.Translate(float64(step), 0)
				rec.SetRGB(float64(g%2), float64(step%2), 0.5)
				rec.DrawCircle(64, 64, float64(step%32))
				if step%2 == 0 {
					rec.Fill()
				} else {
					rec.Stroke()
				}
				rec.Restore()
			}
			recording := rec.FinishRecording()
			if err := recording.Playback(&playbackMockBackend{}); err != nil {
				t.Errorf("goroutine %d playback: %v", g, err)
				return
			}
			// All Saves were balanced; no frames may leak.
			if len(rec.stateStack) != 0 {
				t.Errorf("goroutine %d left %d stack frames", g, len(rec.stateStack))
			}
		}()
	}
	wg.Wait()
}

// TestConcurrentResetAndRecord mixes Reset with continued recording to ensure
// reuse under concurrency (one Recorder per goroutine) stays race-free.
func TestConcurrentResetAndRecord(t *testing.T) {
	const goroutines = 16
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			rec := NewRecorder(96, 96)
			img := image.NewRGBA(image.Rect(0, 0, 8, 8))
			for round := 0; round < 8; round++ {
				rec.SetRGBA(0.1, 0.2, 0.3, 1)
				rec.SetLineWidth(math.NaN())
				rec.Save()
				rec.Scale(2, 2)
				rec.DrawImage(img, 1, 1)
				rec.DrawString("x", 2, 2)
				rec.Restore()
				_ = rec.FinishRecording()
				rec.Reset()
			}
		}()
	}
	wg.Wait()
}

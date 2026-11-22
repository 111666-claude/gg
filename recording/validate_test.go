package recording

import (
	"errors"
	"image"
	"math"
	"strings"
	"testing"

	"github.com/gogpu/gg"
)

// validPool builds a small, fully-populated resource pool used by the happy
// path and as a base for corrupted variants.
func validPool() *ResourcePool {
	pool := NewResourcePool()
	pool.AddPath(gg.BuildPath().MoveTo(0, 0).LineTo(10, 10).Build())
	pool.AddBrush(NewSolidBrush(gg.Black))
	pool.AddImage(image.NewRGBA(image.Rect(0, 0, 4, 4)))
	return pool
}

// validRecording returns a structurally valid recording exercising every
// command tag the validator understands.
func validRecording() *Recording {
	pool := validPool()
	stroke := DefaultStroke()
	cmds := []Command{
		SaveCommand{},
		SetTransformCommand{Matrix: Identity()},
		SetFillStyleCommand{Brush: BrushRef(0)},
		SetStrokeStyleCommand{Brush: BrushRef(0)},
		SetLineWidthCommand{Width: 2},
		SetLineCapCommand{Cap: LineCapRound},
		SetLineJoinCommand{Join: LineJoinBevel},
		SetMiterLimitCommand{Limit: 4},
		SetDashCommand{Pattern: []float64{3, 2}, Offset: 1},
		SetFillRuleCommand{Rule: FillRuleEvenOdd},
		SetAntiAliasCommand{Enabled: false},
		SetClipCommand{Path: PathRef(0), Rule: FillRuleNonZero},
		ClearClipCommand{},
		ClipRoundRectCommand{X: 0, Y: 0, W: 10, H: 10, Radius: 2},
		FillPathCommand{Path: PathRef(0), Brush: BrushRef(0), Rule: FillRuleNonZero},
		StrokePathCommand{Path: PathRef(0), Brush: BrushRef(0), Stroke: stroke},
		FillRectCommand{Rect: NewRect(0, 0, 5, 5), Brush: BrushRef(0)},
		StrokeRectCommand{Rect: NewRect(0, 0, 5, 5), Brush: BrushRef(0), Stroke: stroke},
		DrawImageCommand{
			Image:   ImageRef(0),
			SrcRect: NewRect(0, 0, 4, 4),
			DstRect: NewRect(0, 0, 8, 8),
			Options: DefaultImageOptions(),
		},
		DrawTextCommand{Text: "", X: 1, Y: 2, FontSize: 12, Face: nil, Brush: BrushRef(0)},
		StrokeTextCommand{Text: "", X: 1, Y: 2, FontSize: 12, Face: nil, Brush: BrushRef(0), Stroke: stroke},
		RestoreCommand{},
	}
	return NewRecording(100, 100, cmds, pool)
}

func TestValidateRecording_ValidRecording(t *testing.T) {
	if err := ValidateRecording(validRecording()); err != nil {
		t.Fatalf("valid recording rejected:\n%v", err)
	}
}

func TestValidateRecording_EmptyRecording(t *testing.T) {
	// A legal empty recording — no commands, empty pool — must pass.
	r := NewRecording(64, 48, nil, NewResourcePool())
	if err := ValidateRecording(r); err != nil {
		t.Fatalf("empty recording rejected: %v", err)
	}

	// Recorder-produced empty recording as well.
	rec := NewRecorder(32, 32)
	if err := rec.FinishRecording().Validate(); err != nil {
		t.Fatalf("recorder-produced empty recording rejected: %v", err)
	}
}

func TestValidateRecording_DoesNotModifyInput(t *testing.T) {
	r := validRecording()
	cmdsBefore := len(r.Commands())
	poolBefore := r.Resources().PathCount()
	brushBefore := r.Resources().brushes[0]

	if err := ValidateRecording(r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.Commands()) != cmdsBefore {
		t.Errorf("command count changed: %d -> %d", cmdsBefore, len(r.Commands()))
	}
	if r.Resources().PathCount() != poolBefore {
		t.Errorf("path count changed: %d -> %d", poolBefore, r.Resources().PathCount())
	}
	if r.Resources().brushes[0] != brushBefore {
		t.Errorf("brush pool entry was modified")
	}
}

func TestValidateRecording_NilAndDimensions(t *testing.T) {
	if err := ValidateRecording(nil); err == nil {
		t.Fatal("nil recording should fail validation")
	}

	r := NewRecording(0, 10, nil, NewResourcePool())
	err := ValidateRecording(r, FailFast())
	var ve ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	if ve.CommandIndex != -1 {
		t.Errorf("CommandIndex = %d, want -1", ve.CommandIndex)
	}
	if !strings.Contains(ve.Reason, "positive") {
		t.Errorf("Reason = %q, want dimension complaint", ve.Reason)
	}
}

// TestValidateRecording_BadRecordings injects the five classes of defective
// recordings and proves the gate rejects each with a structured error.
func TestValidateRecording_BadRecordings(t *testing.T) {
	nan := math.NaN()
	inf := math.Inf(1)

	tests := []struct {
		name        string
		mutate      func(r *Recording)
		wantKind    ResourceKind
		wantIndex   int
		wantReason  string
		wantCmdType CommandType
	}{
		{
			name: "1a Restore without Save (ordering)",
			mutate: func(r *Recording) {
				r.commands = append(r.commands, RestoreCommand{})
			},
			wantKind:    ResourceCommand,
			wantIndex:   0,
			wantReason:  "Restore without a matching Save",
			wantCmdType: CmdRestore,
		},
		{
			name: "1b Save without matching Restore (ordering)",
			mutate: func(r *Recording) {
				r.commands = append([]Command{SaveCommand{}}, r.commands...)
			},
			wantKind:    ResourceCommand,
			wantIndex:   0,
			wantReason:  "Save without a matching Restore",
			wantCmdType: CmdSave,
		},
		{
			name: "2a ClipRoundRect negative size",
			mutate: func(r *Recording) {
				r.commands = append(r.commands,
					ClipRoundRectCommand{X: 0, Y: 0, W: -10, H: 10, Radius: 1})
			},
			wantKind:    ResourceCommand,
			wantReason:  "clip size must be finite and positive",
			wantCmdType: CmdClipRoundRect,
		},
		{
			name: "2b ClipRoundRect oversized radius",
			mutate: func(r *Recording) {
				r.commands = append(r.commands,
					ClipRoundRectCommand{X: 0, Y: 0, W: 10, H: 10, Radius: 99})
			},
			wantKind:    ResourceCommand,
			wantReason:  "exceeds half the smaller rect dimension",
			wantCmdType: CmdClipRoundRect,
		},
		{
			name: "2c ClipRoundRect NaN radius",
			mutate: func(r *Recording) {
				r.commands = append(r.commands,
					ClipRoundRectCommand{X: 0, Y: 0, W: 10, H: 10, Radius: nan})
			},
			wantKind:    ResourceCommand,
			wantReason:  "clip radius must be finite",
			wantCmdType: CmdClipRoundRect,
		},
		{
			name: "3a oversized Path ref",
			mutate: func(r *Recording) {
				r.commands = append(r.commands,
					FillPathCommand{Path: PathRef(9999), Brush: BrushRef(0), Rule: FillRuleNonZero})
			},
			wantKind:    ResourcePath,
			wantIndex:   9999,
			wantReason:  "out of range",
			wantCmdType: CmdFillPath,
		},
		{
			name: "3b invalid Brush ref sentinel",
			mutate: func(r *Recording) {
				r.commands = append(r.commands,
					FillRectCommand{Rect: NewRect(0, 0, 1, 1), Brush: BrushRef(InvalidRef)})
			},
			wantKind:    ResourceBrush,
			wantIndex:   int(InvalidRef),
			wantReason:  "explicit invalid reference",
			wantCmdType: CmdFillRect,
		},
		{
			name: "3c oversized Image ref",
			mutate: func(r *Recording) {
				r.commands = append(r.commands, DrawImageCommand{
					Image:   ImageRef(4242),
					SrcRect: NewRect(0, 0, 1, 1),
					DstRect: NewRect(0, 0, 1, 1),
					Options: DefaultImageOptions(),
				})
			},
			wantKind:    ResourceImage,
			wantIndex:   4242,
			wantReason:  "out of range",
			wantCmdType: CmdDrawImage,
		},
		{
			name: "3d nil pooled path",
			mutate: func(r *Recording) {
				r.resources.paths[0] = nil
			},
			wantKind:    ResourcePath,
			wantIndex:   0,
			wantReason:  "pooled path is nil",
			wantCmdType: CmdSetClip,
		},
		{
			name: "3e text with nil font face",
			mutate: func(r *Recording) {
				r.commands = append(r.commands, DrawTextCommand{
					Text: "glyphs", X: 0, Y: 0, FontSize: 12, Face: nil, Brush: BrushRef(0),
				})
			},
			wantKind:    ResourceFont,
			wantReason:  "nil font face",
			wantCmdType: CmdDrawText,
		},
		{
			name: "4a NaN in transform matrix",
			mutate: func(r *Recording) {
				r.commands = append(r.commands,
					SetTransformCommand{Matrix: Matrix{A: nan, E: 1}})
			},
			wantKind:    ResourceCommand,
			wantReason:  "transform matrix contains NaN or Inf",
			wantCmdType: CmdSetTransform,
		},
		{
			name: "4b Inf stroke width",
			mutate: func(r *Recording) {
				r.commands = append(r.commands,
					StrokePathCommand{
						Path: PathRef(0), Brush: BrushRef(0),
						Stroke: Stroke{Width: inf, Cap: LineCapButt, Join: LineJoinMiter, MiterLimit: 4},
					})
			},
			wantKind:    ResourceCommand,
			wantReason:  "stroke width must be finite",
			wantCmdType: CmdStrokePath,
		},
		{
			name: "4c NaN dash pattern entry",
			mutate: func(r *Recording) {
				r.commands = append(r.commands,
					SetDashCommand{Pattern: []float64{1, nan}, Offset: 0})
			},
			wantKind:    ResourceCommand,
			wantReason:  "dash pattern[1] must be finite",
			wantCmdType: CmdSetDash,
		},
		{
			name: "5a unknown command tag",
			mutate: func(r *Recording) {
				r.commands = append(r.commands, unknownCommand{})
			},
			wantKind:   ResourceCommand,
			wantReason: "unknown command tag",
		},
		{
			name: "5b nil command entry",
			mutate: func(r *Recording) {
				r.commands = append(r.commands, nil)
			},
			wantKind:   ResourceCommand,
			wantReason: "nil command",
		},
		{
			name: "5c invalid fill rule enum",
			mutate: func(r *Recording) {
				r.commands = append(r.commands,
					SetFillRuleCommand{Rule: FillRule(99)})
			},
			wantKind:    ResourceCommand,
			wantReason:  "invalid fill rule tag",
			wantCmdType: CmdSetFillRule,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validRecording()
			tt.mutate(r)

			// Collect-all mode: defect present, structured fields populated.
			err := ValidateRecording(r)
			if err == nil {
				t.Fatal("validation passed on defective recording")
			}
			var errs ValidationErrors
			if !errors.As(err, &errs) {
				t.Fatalf("expected ValidationErrors, got %T: %v", err, err)
			}
			matched := false
			for _, e := range errs {
				if e.Kind != tt.wantKind {
					continue
				}
				if tt.wantIndex != 0 && e.ResourceIndex != tt.wantIndex {
					continue
				}
				if !strings.Contains(e.Reason, tt.wantReason) {
					continue
				}
				if tt.wantCmdType != 0 && e.tag != tt.wantCmdType {
					continue
				}
				matched = true
				if e.CommandIndex < 0 && tt.wantKind != ResourceCommand {
					t.Errorf("resource error missing command index: %+v", e)
				}
			}
			if !matched {
				t.Fatalf("no error matching kind=%s idx=%d reason=%q in:\n%v",
					tt.wantKind, tt.wantIndex, tt.wantReason, err)
			}

			// Fail-fast mode: a single ValidationError is returned.
			ffErr := ValidateRecording(r, FailFast())
			var ff ValidationError
			if !errors.As(ffErr, &ff) {
				t.Fatalf("fail-fast expected ValidationError, got %T: %v", ffErr, ffErr)
			}
		})
	}
}

// unknownCommand implements Command with a tag outside the registered range,
// simulating a plugin that wrote garbage into the command slice.
type unknownCommand struct{}

func (unknownCommand) Type() CommandType { return CommandType(200) }

func TestValidateRecording_FailFastStopsAtFirst(t *testing.T) {
	r := validRecording()
	// Two defects: bad ref then NaN.
	r.commands = append(r.commands,
		FillPathCommand{Path: PathRef(77), Brush: BrushRef(0), Rule: FillRuleNonZero},
		SetTransformCommand{Matrix: Matrix{A: math.NaN()}},
	)

	err := ValidateRecording(r, FailFast())
	var ve ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected ValidationError, got %T", err)
	}
	if ve.Kind != ResourcePath {
		t.Errorf("Kind = %s, want path (first defect)", ve.Kind)
	}

	// Collect-all finds both.
	all := ValidateRecording(r)
	var errs ValidationErrors
	errors.As(all, &errs)
	if len(errs) < 2 {
		t.Fatalf("collect-all found %d errors, want >= 2", len(errs))
	}
}

func TestValidateRecording_CollectsMultipleInOneCommand(t *testing.T) {
	pool := NewResourcePool()
	// Empty pool: every reference is out of range.
	r := NewRecording(10, 10, []Command{
		FillPathCommand{Path: PathRef(0), Brush: BrushRef(0), Rule: FillRule(9)},
	}, pool)

	var errs ValidationErrors
	errors.As(ValidateRecording(r), &errs)
	if len(errs) < 3 {
		t.Fatalf("want path + brush + enum errors, got %d: %v", len(errs), errs)
	}
	for _, e := range errs {
		if e.CommandIndex != 0 {
			t.Errorf("CommandIndex = %d, want 0", e.CommandIndex)
		}
	}
}

func TestValidateRecording_ErrorCapBindsAllocation(t *testing.T) {
	// 20k defective commands, cap at 16: the error slice must not grow with
	// input size and the walk must terminate (no infinite loop).
	cmds := make([]Command, 20000)
	for i := range cmds {
		cmds[i] = FillPathCommand{
			Path: PathRef(999999), Brush: BrushRef(999999), Rule: FillRuleNonZero,
		}
	}
	r := NewRecording(10, 10, cmds, NewResourcePool())

	const cap_ = 16
	err := ValidateRecording(r, MaxValidationErrors(cap_))
	var errs ValidationErrors
	errors.As(err, &errs)
	// cap defects + one truncation notice.
	if len(errs) != cap_+1 {
		t.Fatalf("error count = %d, want %d (cap + truncation notice)", len(errs), cap_+1)
	}
	if !strings.Contains(errs[len(errs)-1].Reason, "error limit") {
		t.Errorf("last error = %q, want truncation notice", errs[len(errs)-1].Reason)
	}

	// Allocation bound: the per-run allocation count must be driven by the
	// error cap, not the input length. Without the saturation guard a
	// 40k-command recording costs tens of thousands of allocations; with it
	// the count stays a small multiple of the cap regardless of input size.
	// (Exact counts are not asserted because race/coverage builds add their
	// own allocator noise; the invariant is sub-linear scaling.)
	measure := func(n int) float64 {
		big := make([]Command, n)
		for j := range big {
			big[j] = FillPathCommand{
				Path: PathRef(999999), Brush: BrushRef(999999), Rule: FillRuleNonZero,
			}
		}
		bigRec := NewRecording(10, 10, big, NewResourcePool())
		return testing.AllocsPerRun(3, func() {
			_ = ValidateRecording(bigRec, MaxValidationErrors(cap_))
		})
	}
	allocs20k := measure(20000)
	allocs40k := measure(40000)
	for n, allocs := range map[int]float64{20000: allocs20k, 40000: allocs40k} {
		// At most ~4 allocations per capped defect plus constant overhead —
		// three orders of magnitude below the per-command rate.
		if allocs > float64(8*cap_) {
			t.Errorf("input %d: allocs = %v, want <= %d (cap-bounded, not linear in input)",
				n, allocs, 8*cap_)
		}
	}
}

func TestValidateRecording_DeepStateStack(t *testing.T) {
	// 50k unmatched Saves: the validator tracks them without hanging and
	// reports each unclosed save (capped).
	cmds := make([]Command, 50000)
	for i := range cmds {
		cmds[i] = SaveCommand{}
	}
	r := NewRecording(10, 10, cmds, NewResourcePool())

	err := ValidateRecording(r, MaxValidationErrors(8))
	var errs ValidationErrors
	errors.As(err, &errs)
	if len(errs) == 0 {
		t.Fatal("deep unclosed stack was not reported")
	}
	for _, e := range errs[:len(errs)-1] {
		if e.tag != CmdSave || !strings.Contains(e.Reason, "Save without a matching Restore") {
			t.Fatalf("unexpected error for deep stack: %+v", e)
		}
	}

	// Balanced deep stack is legal and must pass.
	balanced := make([]Command, 0, 10000)
	for i := 0; i < 5000; i++ {
		balanced = append(balanced, SaveCommand{})
	}
	for i := 0; i < 5000; i++ {
		balanced = append(balanced, RestoreCommand{})
	}
	good := NewRecording(10, 10, balanced, NewResourcePool())
	if err := ValidateRecording(good); err != nil {
		t.Fatalf("balanced deep stack rejected: %v", err)
	}
}

func TestValidateRecording_BrushGeometry(t *testing.T) {
	pool := NewResourcePool()
	nan := math.NaN()

	badSolid := BrushRef(pool.AddBrush(NewSolidBrush(gg.RGBA{R: nan})))
	badLinear := BrushRef(pool.AddBrush(&LinearGradientBrush{
		Start: gg.Point{X: 0, Y: 0},
		End:   gg.Point{X: math.Inf(1), Y: 0},
		Stops: []GradientStop{{Offset: 0, Color: gg.Black}},
	}))
	badStops := BrushRef(pool.AddBrush(&RadialGradientBrush{
		Center:      gg.Point{X: 0, Y: 0},
		Focus:       gg.Point{X: 0, Y: 0},
		StartRadius: 1,
		EndRadius:   2,
		Stops:       []GradientStop{{Offset: nan, Color: gg.Black}},
	}))

	r := NewRecording(10, 10, []Command{
		FillRectCommand{Rect: NewRect(0, 0, 1, 1), Brush: badSolid},
		FillRectCommand{Rect: NewRect(0, 0, 1, 1), Brush: badLinear},
		FillRectCommand{Rect: NewRect(0, 0, 1, 1), Brush: badStops},
	}, pool)

	var errs ValidationErrors
	errors.As(ValidateRecording(r), &errs)
	if len(errs) < 3 {
		t.Fatalf("want 3 brush geometry errors, got:\n%v", errs)
	}
	for _, e := range errs {
		if e.Kind != ResourceBrush {
			t.Errorf("Kind = %s, want brush", e.Kind)
		}
	}
}

func TestValidateRecording_MissingPool(t *testing.T) {
	// Hand-built recording with a nil pool (bypassing NewRecording's
	// normalization), mimicking corrupted external input.
	r := &Recording{
		width: 10, height: 10,
		commands: []Command{
			FillPathCommand{Path: PathRef(0), Brush: BrushRef(0), Rule: FillRuleNonZero},
		},
	}
	err := ValidateRecording(r, FailFast())
	var ve ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	if !strings.Contains(ve.Reason, "missing resource pool") {
		t.Errorf("Reason = %q, want missing pool", ve.Reason)
	}
}

func TestValidationError_ErrorFormat(t *testing.T) {
	e := ValidationError{
		CommandIndex:  7,
		Kind:          ResourcePath,
		ResourceIndex: 3,
		Reason:        "index out of range",
		tag:           CmdFillPath,
	}
	msg := e.Error()
	for _, want := range []string{"command[7]", "FillPath", "path ref 3", "index out of range"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

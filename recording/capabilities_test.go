package recording

import (
	"errors"
	"image"
	"math"
	"strings"
	"testing"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/text"
)

// --------------------------------------------------------------------------
// Test backend with a declarable capability set
// --------------------------------------------------------------------------

// capBackend records the calls Playback makes and reports a configurable
// capability set.
type capBackend struct {
	caps CapabilitySet

	began, ended bool
	beginCalls   int
	endCalls     int
	ops          []string

	saveDepth   int
	maxSave     int
	unbalanced  bool
	transform   Matrix
	transformN  int
	clipCount   int
	fillRects   []Rect
	fillPaths   []*gg.Path
	strokePaths []*gg.Path
	images      []image.Image
	texts       []string
}

func newCapBackend(native Capability) *capBackend {
	return &capBackend{caps: CapabilitySet{Native: native}}
}

func (b *capBackend) BackendCapabilities() CapabilitySet { return b.caps }

func (b *capBackend) Begin(_, _ int) error {
	b.began = true
	b.beginCalls++
	b.transform = Identity()
	return nil
}

func (b *capBackend) End() error {
	b.ended = true
	b.endCalls++
	if b.saveDepth != 0 {
		b.unbalanced = true
	}
	return nil
}

func (b *capBackend) Save() {
	b.ops = append(b.ops, "save")
	b.saveDepth++
	if b.saveDepth > b.maxSave {
		b.maxSave = b.saveDepth
	}
}

func (b *capBackend) Restore() {
	b.ops = append(b.ops, "restore")
	if b.saveDepth > 0 {
		b.saveDepth--
	}
}

func (b *capBackend) SetTransform(m Matrix) {
	b.ops = append(b.ops, "transform")
	b.transformN++
	b.transform = m
}

func (b *capBackend) SetClip(_ *gg.Path, _ FillRule) {
	b.ops = append(b.ops, "clip")
	b.clipCount++
}

func (b *capBackend) ClearClip() { b.ops = append(b.ops, "clearclip") }

func (b *capBackend) FillPath(p *gg.Path, _ Brush, _ FillRule) {
	b.ops = append(b.ops, "fillpath")
	b.fillPaths = append(b.fillPaths, p)
}

func (b *capBackend) StrokePath(p *gg.Path, _ Brush, _ Stroke) {
	b.ops = append(b.ops, "strokepath")
	b.strokePaths = append(b.strokePaths, p)
}

func (b *capBackend) FillRect(r Rect, _ Brush) {
	b.ops = append(b.ops, "fillrect")
	b.fillRects = append(b.fillRects, r)
}

func (b *capBackend) DrawImage(img image.Image, _, _ Rect, _ ImageOptions) {
	b.ops = append(b.ops, "drawimage")
	b.images = append(b.images, img)
}

func (b *capBackend) DrawText(s string, _, _ float64, _ text.Face, _ Brush) {
	b.ops = append(b.ops, "drawtext")
	b.texts = append(b.texts, s)
}

// count returns the number of recorded operations named op.
func (b *capBackend) count(op string) int {
	n := 0
	for _, o := range b.ops {
		if o == op {
			n++
		}
	}
	return n
}

// mixedRecording builds a recording that exercises paths, rounded clips,
// rectangles, an image, text, and style commands, with a Save/Restore around a
// transform so state-stack handling is observable.
func mixedRecording(t *testing.T) *Recording {
	t.Helper()

	rec := NewRecorder(64, 64)

	// Style commands.
	rec.SetFillRGB(1, 0, 0)
	rec.SetLineWidth(3)
	rec.SetDash(4, 2)

	// Path fill and stroke.
	rec.DrawCircle(20, 20, 8)
	rec.Fill()
	rec.MoveTo(2, 40)
	rec.LineTo(30, 50)
	rec.Stroke()

	// Rounded clip around a transformed rectangle fill.
	rec.Save()
	rec.Translate(5, 5)
	rec.ClipRoundRect(0, 0, 10, 10, 2)
	rec.FillRectangle(1, 1, 4, 4)
	rec.Restore()

	// Image and text.
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			img.Set(x, y, gg.RGB(0, 1, 0))
		}
	}
	rec.DrawImage(img, 40, 40)
	rec.DrawString("hi", 5, 60)

	return rec.FinishRecording()
}

// --------------------------------------------------------------------------
// Capability set declarations
// --------------------------------------------------------------------------

func TestCapabilityContainsAndString(t *testing.T) {
	if !CapAll.Contains(CapFillPath | CapDrawText) {
		t.Error("CapAll should contain every capability")
	}
	if (CapFillPath | CapStrokePath).Contains(CapDrawText) {
		t.Error("fill+stroke should not contain DrawText")
	}
	if CapNone.Contains(CapFillPath) {
		t.Error("CapNone must be empty")
	}
	if CapNone.String() != "None" {
		t.Errorf("CapNone.String() = %q", CapNone.String())
	}
	if got, want := (CapFillRect | CapDrawText).String(), "FillRect|DrawText"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestCapabilityIsStyle(t *testing.T) {
	if !CapStyleDash.IsStyle() {
		t.Error("CapStyleDash should be a style capability")
	}
	if CapFillPath.IsStyle() {
		t.Error("CapFillPath is not a style capability")
	}
	if (CapStyleDash | CapStyleFill).IsStyle() {
		t.Error("a multi-bit set is not a single style capability")
	}
	if !CapStyles.Contains(CapStyleAntiAlias) {
		t.Error("CapStyles should include SetAntiAlias")
	}
}

// --------------------------------------------------------------------------
// Preflight
// --------------------------------------------------------------------------

func TestPreflightFullBackendIsComplete(t *testing.T) {
	r := mixedRecording(t)
	backend := newCapBackend(CapAll)

	report, err := r.Preflight(backend)
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if !report.Declared {
		t.Error("Declared = false, want true for a CapabilityBackend")
	}
	if !report.IsComplete() {
		t.Errorf("report not complete: %s", report)
	}
	if len(report.Missing) != 0 {
		t.Errorf("Missing = %v, want none", report.Missing)
	}
	if backend.beginCalls != 0 {
		t.Error("Preflight must not call Begin")
	}
}

func TestPreflightTextOnlyBackendReportsMissing(t *testing.T) {
	r := mixedRecording(t)
	// A minimal text backend: rectangles and text only, no paths, clips,
	// images, or style commands.
	backend := newCapBackend(CapFillRect | CapDrawText)

	report, err := r.Preflight(backend)
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	// mixedRecording's rounded clip resolves through the built-in
	// ClipRoundRect -> ClipPath fallback, so ClipPath itself is not missing.
	want := []Capability{
		CapFillPath, CapStrokePath, CapDrawImage, CapClipRoundRect,
		CapStyleFill, CapStyleLineWidth, CapStyleDash,
	}
	if len(report.Missing) != len(want) {
		t.Fatalf("Missing = %v, want %v", report.Missing, want)
	}
	for i := range want {
		if report.Missing[i] != want[i] {
			t.Errorf("Missing[%d] = %s, want %s", i, report.Missing[i], want[i])
		}
	}

	// Every missing capability should name at least one command, in order.
	for _, issue := range report.Issues {
		if len(issue.Commands) == 0 {
			t.Errorf("%s has no command indexes", issue.Capability)
		}
		for j := 1; j < len(issue.Commands); j++ {
			if issue.Commands[j] <= issue.Commands[j-1] {
				t.Errorf("%s commands are not ascending: %v", issue.Capability, issue.Commands)
			}
		}
	}
}

func TestPreflightRectOnlyBackendDegradability(t *testing.T) {
	r := mixedRecording(t)
	backend := newCapBackend(CapFillRect | CapStrokeRect | CapDrawText |
		CapClipPath | CapClearClip | CapStyles)

	report, err := r.Preflight(backend)
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}

	for _, issue := range report.Issues {
		switch issue.Capability {
		case CapFillPath:
			// A FillRect cannot stand in for an arbitrary circle path.
			if issue.Degradable {
				t.Error("FillPath should not be degradable from FillRect")
			}
		case CapStrokePath:
			if issue.Degradable {
				t.Error("StrokePath should not be degradable from StrokeRect")
			}
		case CapClipRoundRect:
			if !issue.Degradable || issue.Fallback != CapClipPath {
				t.Errorf("ClipRoundRect fallback = %s (degradable %v), want ClipPath",
					issue.Fallback, issue.Degradable)
			}
		case CapStrokeRect:
			if !issue.Degradable || issue.Fallback != CapStrokePath {
				t.Errorf("StrokeRect fallback = %s, want StrokePath", issue.Fallback)
			}
		case CapDrawImage:
			if issue.Degradable {
				t.Error("DrawImage has no equivalent base command")
			}
		}
	}
}

func TestPreflightUndeclaredBackendIsAssumedComplete(t *testing.T) {
	r := mixedRecording(t)
	backend := &playbackMockBackend{}

	report, err := r.Preflight(backend)
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if report.Declared {
		t.Error("Declared = true for a backend without CapabilityBackend")
	}
	if !report.IsComplete() || report.Native != CapAll {
		t.Errorf("undeclared backend should be CapAll: %s", report)
	}
}

func TestPreflightNilBackend(t *testing.T) {
	r := mixedRecording(t)
	if _, err := r.Preflight(nil); !errors.Is(err, ErrNilBackend) {
		t.Fatalf("Preflight(nil) error = %v, want ErrNilBackend", err)
	}

	var typedNil *capBackend
	if _, err := r.Preflight(typedNil); !errors.Is(err, ErrNilBackend) {
		t.Fatalf("Preflight(typed nil) error = %v, want ErrNilBackend", err)
	}
}

func TestPreflightDeclarationConflict(t *testing.T) {
	r := mixedRecording(t)
	backend := newCapBackend(CapFillPath)
	backend.caps.Fallbacks = map[Capability]Capability{
		// FillPath is native, so a fallback for it is a contradiction.
		CapFillPath: CapFillRect,
	}

	_, err := r.Preflight(backend)
	if !errors.Is(err, ErrCapabilityConflict) {
		t.Fatalf("Preflight error = %v, want ErrCapabilityConflict", err)
	}
	var perr *PlaybackError
	if !errors.As(err, &perr) {
		t.Fatalf("error %v is not a *PlaybackError", err)
	}
	if perr.Index != -1 {
		t.Errorf("PlaybackError.Index = %d, want -1 for a declaration error", perr.Index)
	}
}

func TestPreflightFallbackRecursion(t *testing.T) {
	r := mixedRecording(t)
	backend := newCapBackend(CapDrawText)
	backend.caps.Fallbacks = map[Capability]Capability{
		CapFillRect:   CapStrokeRect,
		CapStrokeRect: CapFillRect,
	}
	// Make FillRect missing so its chain is walked.
	backend.caps.Native &^= CapFillRect

	_, err := r.Preflight(backend)
	if !errors.Is(err, ErrFallbackRecursion) {
		t.Fatalf("Preflight error = %v, want ErrFallbackRecursion", err)
	}
}

func TestPreflightUnknownCommand(t *testing.T) {
	backend := newCapBackend(CapAll)
	r := &Recording{width: 4, height: 4, resources: NewResourcePool()}
	r.commands = []Command{SaveCommand{}, mysteryCommand{}}

	_, err := r.Preflight(backend)
	if !errors.Is(err, ErrUnknownCommand) {
		t.Fatalf("Preflight error = %v, want ErrUnknownCommand", err)
	}
	var perr *PlaybackError
	if !errors.As(err, &perr) {
		t.Fatalf("error %v is not a *PlaybackError", err)
	}
	if perr.Index != 1 {
		t.Errorf("PlaybackError.Index = %d, want 1", perr.Index)
	}
}

// mysteryCommand is a command type the package does not know.
type mysteryCommand struct{}

func (mysteryCommand) Type() CommandType { return CommandType(200) }

// --------------------------------------------------------------------------
// Playback policies
// --------------------------------------------------------------------------

func TestPlaybackStrictFailsBeforeOutput(t *testing.T) {
	r := mixedRecording(t)
	backend := newCapBackend(CapFillRect | CapDrawText | CapClips | CapStyles)

	err := r.Playback(backend)
	if !errors.Is(err, ErrUnsupportedCapability) {
		t.Fatalf("Playback error = %v, want ErrUnsupportedCapability", err)
	}
	var perr *PlaybackError
	if !errors.As(err, &perr) {
		t.Fatalf("error %v is not a *PlaybackError", err)
	}
	if perr.Index < 0 {
		t.Error("strict failure should carry a command index")
	}
	if backend.beginCalls != 0 || backend.endCalls != 0 {
		t.Errorf("Begin/End calls = %d/%d, want 0/0 after a preflight failure",
			backend.beginCalls, backend.endCalls)
	}
	if len(backend.ops) != 0 {
		t.Errorf("backend received %v operations before the failure", backend.ops)
	}

	var uerr *UnsupportedError
	if !errors.As(err, &uerr) {
		t.Fatalf("error %v does not unwrap to *UnsupportedError", err)
	}
	if uerr.Report == nil || len(uerr.Report.Missing) == 0 {
		t.Error("UnsupportedError should carry the preflight report")
	}
}

func TestPlaybackSkipContinuesAndBalancesState(t *testing.T) {
	r := mixedRecording(t)
	backend := newCapBackend(CapFillRect | CapDrawText | CapClips | CapStyles)

	var skipped []SkippedCommand
	result, err := r.PlaybackWithOptions(backend, PlaybackOptions{
		Policy: PolicySkip,
		OnSkip: func(s SkippedCommand) { skipped = append(skipped, s) },
	})
	if err != nil {
		t.Fatalf("PlaybackWithOptions: %v", err)
	}
	if len(result.Skipped) == 0 {
		t.Fatal("expected skipped commands")
	}
	if len(skipped) != len(result.Skipped) {
		t.Errorf("OnSkip called %d times, result has %d", len(skipped), len(result.Skipped))
	}
	if result.Completeness() {
		t.Error("a recording with skips should not be complete")
	}

	// FillPath, StrokePath, DrawImage and ClipRoundRect were skipped; the
	// rectangle fill and text still ran.
	if backend.count("fillrect") != 1 {
		t.Errorf("fillrect = %d, want 1", backend.count("fillrect"))
	}
	if backend.count("drawtext") != 1 {
		t.Errorf("drawtext = %d, want 1", backend.count("drawtext"))
	}
	if backend.count("fillpath") != 0 {
		t.Errorf("fillpath = %d, want 0 (skipped)", backend.count("fillpath"))
	}
	if backend.count("drawimage") != 0 {
		t.Errorf("drawimage = %d, want 0 (skipped)", backend.count("drawimage"))
	}

	// State stack must stay balanced: the Save command still ran, and so did
	// its Restore, even though the clip in between was skipped.
	if backend.unbalanced {
		t.Error("backend state stack was left unbalanced")
	}
	if backend.count("save") != backend.count("restore") {
		t.Errorf("save/restore = %d/%d", backend.count("save"), backend.count("restore"))
	}

	for _, s := range result.Skipped {
		if s.Capability == CapNone {
			t.Errorf("skipped command %d reports CapNone", s.Index)
		}
	}
}

func TestPlaybackSkipKeepsReturnedResultWithoutError(t *testing.T) {
	r := mixedRecording(t)
	backend := newCapBackend(CapFillRect | CapDrawText | CapClips | CapStyles)

	result, err := r.PlaybackWithOptions(backend, PlaybackOptions{Policy: PolicySkip})
	if err != nil {
		t.Fatalf("PlaybackWithOptions: %v", err)
	}
	if !backend.began || !backend.ended {
		t.Error("a successful skip playback should Begin and End")
	}
	// The result reports gaps even though err is nil.
	if result.Report == nil || len(result.Report.Missing) == 0 {
		t.Error("result should carry the missing-capability report")
	}
}

func TestPlaybackDegradeRewritesRectanglesAndClips(t *testing.T) {
	// A backend with paths but no FillRect/StrokeRect/ClipRoundRect, and no
	// images or path fills for the circle.
	native := CapFillPath | CapStrokePath | CapClipPath | CapClearClip |
		CapDrawText | CapStyles | CapDrawImage | CapStrokeText
	backend := newCapBackend(native)
	r := mixedRecording(t)

	result, err := r.PlaybackWithOptions(backend, PlaybackOptions{Policy: PolicyDegrade})
	if err != nil {
		t.Fatalf("PlaybackWithOptions: %v", err)
	}

	// The FillRectangle inside the clip and the ClipRoundRect must be rewrites.
	var sawFillRectDegrade, sawClipDegrade bool
	for _, d := range result.Degraded {
		switch d.Command {
		case CmdFillRect:
			sawFillRectDegrade = true
			if d.From != CapFillRect || d.To != CapFillPath {
				t.Errorf("FillRect degraded %s -> %s, want FillRect -> FillPath", d.From, d.To)
			}
		case CmdClipRoundRect:
			sawClipDegrade = true
			if d.From != CapClipRoundRect || d.To != CapClipPath {
				t.Errorf("ClipRoundRect degraded %s -> %s", d.From, d.To)
			}
		}
	}
	if !sawFillRectDegrade {
		t.Error("FillRect was not degraded")
	}
	if !sawClipDegrade {
		t.Error("ClipRoundRect was not degraded")
	}
	if backend.count("fillrect") != 0 {
		t.Errorf("backend saw %d native FillRect calls, want 0", backend.count("fillrect"))
	}
	if backend.count("clip") != 1 {
		t.Errorf("backend saw %d SetClip calls, want 1", backend.count("clip"))
	}
	// Degraded fill uses the path form.
	if backend.count("fillpath") == 0 {
		t.Error("degraded FillRect did not use FillPath")
	}
	if backend.unbalanced {
		t.Error("degraded playback left the state stack unbalanced")
	}
}

func TestPlaybackDegradeFailsOnNonEquivalentCommand(t *testing.T) {
	// A text-only backend cannot substitute anything for the circle fill.
	backend := newCapBackend(CapFillRect | CapDrawText)
	r := mixedRecording(t)

	_, err := r.PlaybackWithOptions(backend, PlaybackOptions{Policy: PolicyDegrade})
	if !errors.Is(err, ErrUnsupportedCapability) {
		t.Fatalf("error = %v, want ErrUnsupportedCapability", err)
	}
	if backend.beginCalls != 0 {
		t.Error("degradation failure must be detected before Begin")
	}
}

func TestPlaybackInvalidPolicy(t *testing.T) {
	r := mixedRecording(t)
	backend := newCapBackend(CapAll)

	_, err := r.PlaybackWithOptions(backend, PlaybackOptions{Policy: PlaybackPolicy(99)})
	if !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("error = %v, want ErrInvalidPolicy", err)
	}
}

func TestPlaybackNilBackend(t *testing.T) {
	r := mixedRecording(t)
	var err error

	if err = r.Playback(nil); !errors.Is(err, ErrNilBackend) {
		t.Errorf("Playback(nil) = %v, want ErrNilBackend", err)
	}
	if _, err = r.PlaybackWithOptions(nil, PlaybackOptions{Policy: PolicySkip}); !errors.Is(err, ErrNilBackend) {
		t.Errorf("PlaybackWithOptions(nil) = %v, want ErrNilBackend", err)
	}
}

// --------------------------------------------------------------------------
// Errors carry command numbers and no partial success
// --------------------------------------------------------------------------

// recordingWithBadRefs returns a recording whose command at index has an
// invalid reference.
func recordingWithBadRefs(t *testing.T, cmd Command) *Recording {
	t.Helper()
	rec := NewRecorder(16, 16)
	rec.SetFillRGB(1, 0, 0)
	rec.FillRectangle(0, 0, 8, 8)
	r := rec.FinishRecording()
	r.commands = append(r.commands, cmd)
	return r
}

func TestPlaybackMissingResources(t *testing.T) {
	tests := []struct {
		name string
		cmd  Command
	}{
		{"path", FillPathCommand{Path: PathRef(99), Brush: BrushRef(0)}},
		{"brush", FillPathCommand{Path: PathRef(0), Brush: BrushRef(99)}},
		{"image", DrawImageCommand{Image: ImageRef(99)}},
		{"stroke brush", StrokeRectCommand{Brush: BrushRef(99)}},
		{"nil image ref", DrawImageCommand{Image: ImageRef(InvalidRef)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := recordingWithBadRefs(t, tt.cmd)
			backend := newCapBackend(CapAll)

			err := r.Playback(backend)
			if !errors.Is(err, ErrMissingResource) {
				t.Fatalf("error = %v, want ErrMissingResource", err)
			}
			var perr *PlaybackError
			if !errors.As(err, &perr) {
				t.Fatalf("error %v is not a *PlaybackError", err)
			}
			if want := len(r.commands) - 1; perr.Index != want {
				t.Errorf("PlaybackError.Index = %d, want %d", perr.Index, want)
			}
			if !perr.HasCommand || perr.Command != tt.cmd.Type() {
				t.Errorf("PlaybackError command = %v/%v", perr.Command, perr.HasCommand)
			}
			if backend.beginCalls != 0 {
				t.Error("missing resources must be detected before Begin")
			}
		})
	}
}

func TestPlaybackUnknownCommandNoPartialOutput(t *testing.T) {
	r := mixedRecording(t)
	r.commands = append(r.commands, mysteryCommand{})
	backend := newCapBackend(CapAll)

	err := r.Playback(backend)
	if !errors.Is(err, ErrUnknownCommand) {
		t.Fatalf("error = %v, want ErrUnknownCommand", err)
	}
	var perr *PlaybackError
	if !errors.As(err, &perr) {
		t.Fatalf("error %v is not a *PlaybackError", err)
	}
	if want := len(r.commands) - 1; perr.Index != want {
		t.Errorf("Index = %d, want %d", perr.Index, want)
	}
	if backend.beginCalls != 0 || len(backend.ops) != 0 {
		t.Error("unknown command must be rejected before any output")
	}
}

func TestPlaybackBeginErrorWrapped(t *testing.T) {
	r := mixedRecording(t)
	backend := &failingBeginBackend{capBackend: newCapBackend(CapAll)}

	err := r.Playback(backend)
	if err == nil {
		t.Fatal("expected an error from Begin")
	}
	var perr *PlaybackError
	if !errors.As(err, &perr) {
		t.Fatalf("error %v is not a *PlaybackError", err)
	}
	if perr.Index != -1 {
		t.Errorf("Index = %d, want -1", perr.Index)
	}
	if backend.endCalls != 0 {
		t.Error("End should not run when Begin fails")
	}
}

func TestPlaybackEndErrorWrapped(t *testing.T) {
	r := mixedRecording(t)
	backend := &failingEndBackend{capBackend: newCapBackend(CapAll)}

	err := r.Playback(backend)
	if err == nil {
		t.Fatal("expected an error from End")
	}
	var perr *PlaybackError
	if !errors.As(err, &perr) {
		t.Fatalf("error %v is not a *PlaybackError", err)
	}
	if perr.Index != -1 {
		t.Errorf("Index = %d, want -1", perr.Index)
	}
}

type failingBeginBackend struct{ *capBackend }

func (b *failingBeginBackend) Begin(_, _ int) error { return errors.New("boom") }

type failingEndBackend struct{ *capBackend }

func (b *failingEndBackend) End() error { return errors.New("boom") }

// --------------------------------------------------------------------------
// Validate
// --------------------------------------------------------------------------

func TestValidateCleanRecording(t *testing.T) {
	if err := mixedRecording(t).Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateReportsCommandNumber(t *testing.T) {
	r := recordingWithBadRefs(t, DrawImageCommand{Image: ImageRef(42)})
	err := r.Validate()
	if !errors.Is(err, ErrMissingResource) {
		t.Fatalf("Validate error = %v, want ErrMissingResource", err)
	}
	var perr *PlaybackError
	if !errors.As(err, &perr) {
		t.Fatalf("error %v is not a *PlaybackError", err)
	}
	if perr.Index != len(r.commands)-1 {
		t.Errorf("Index = %d, want %d", perr.Index, len(r.commands)-1)
	}
}

// --------------------------------------------------------------------------
// RequiredCapabilities
// --------------------------------------------------------------------------

func TestRequiredCapabilities(t *testing.T) {
	r := mixedRecording(t)
	required, err := r.RequiredCapabilities()
	if err != nil {
		t.Fatalf("RequiredCapabilities: %v", err)
	}
	for _, want := range []Capability{
		CapFillPath, CapStrokePath, CapFillRect, CapClipRoundRect,
		CapDrawImage, CapDrawText, CapStyleFill, CapStyleLineWidth, CapStyleDash,
	} {
		if !required.Contains(want) {
			t.Errorf("required capabilities %s do not include %s", required, want)
		}
	}
	if required.Contains(CapStrokeRect) {
		t.Error("recording uses no StrokeRect")
	}
}

// --------------------------------------------------------------------------
// Capability declaration validation is order-independent
// --------------------------------------------------------------------------

func TestValidateCapabilitySetRejectsUnknownBits(t *testing.T) {
	set := CapabilitySet{Native: CapAll | Capability(1<<30)}
	if err := validateCapabilitySet(set); !errors.Is(err, ErrCapabilityConflict) {
		t.Fatalf("error = %v, want ErrCapabilityConflict", err)
	}
}

func TestValidateCapabilitySetRejectsNonEquivalentFallback(t *testing.T) {
	set := CapabilitySet{
		Native:    CapDrawText,
		Fallbacks: map[Capability]Capability{CapFillPath: CapDrawText},
	}
	if err := validateCapabilitySet(set); !errors.Is(err, ErrCapabilityConflict) {
		t.Fatalf("error = %v, want ErrCapabilityConflict", err)
	}
}

// TestDegradationChainThroughIntermediateCapability verifies that a backend
// can route FillRect -> FillPath -> (its own fallback).
func TestDegradationChainThroughIntermediateCapability(t *testing.T) {
	set := CapabilitySet{
		Native: CapStrokePath,
		Fallbacks: map[Capability]Capability{
			// FillPath is not native either; route it through StrokePath.
			CapFillPath: CapStrokePath,
		},
	}
	// FillRect -> FillPath, and FillPath -> StrokePath. The second hop is not
	// an equivalent rewrite, so the chain must be rejected rather than produce
	// wrong output.
	target, ok, err := resolveDegradation(CapFillRect, set.Native, set)
	if err == nil && ok {
		t.Errorf("resolved FillRect to %s; a non-equivalent hop must fail", target)
	}
	if err != nil && !errors.Is(err, ErrCapabilityConflict) {
		t.Errorf("error = %v, want ErrCapabilityConflict", err)
	}
}

func TestResolveDegradationDirectAndDrop(t *testing.T) {
	set := CapabilitySet{Native: CapFillPath | CapStrokePath}

	target, ok, err := resolveDegradation(CapFillRect, set.Native, set)
	if err != nil || !ok || target != CapFillPath {
		t.Errorf("FillRect degraded to %s (%v, %v), want FillPath", target, ok, err)
	}

	target, ok, err = resolveDegradation(CapStyleDash, set.Native, set)
	if err != nil || !ok || target != CapNone {
		t.Errorf("style degraded to %s (%v, %v), want CapNone drop", target, ok, err)
	}

	if _, ok, _ := resolveDegradation(CapDrawImage, set.Native, set); ok {
		t.Error("DrawImage should have no degradation")
	}
}

// --------------------------------------------------------------------------
// Deterministic report ordering
// --------------------------------------------------------------------------

func TestReportOrderIsDeclarationOrder(t *testing.T) {
	rec := NewRecorder(8, 8)
	rec.DrawString("x", 1, 1)
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	rec.DrawImage(img, 1, 1)
	rec.DrawCircle(4, 4, 2)
	rec.Fill()
	r := rec.FinishRecording()

	backend := newCapBackend(CapNone)
	report, err := r.Preflight(backend)
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	want := []Capability{CapFillPath, CapDrawImage, CapDrawText}
	if len(report.Missing) != len(want) {
		t.Fatalf("Missing = %v, want %v", report.Missing, want)
	}
	for i := range want {
		if report.Missing[i] != want[i] {
			t.Errorf("Missing[%d] = %s, want %s", i, report.Missing[i], want[i])
		}
	}
}

// --------------------------------------------------------------------------
// Degraded FillRect is pixel-equivalent to native FillRect
// --------------------------------------------------------------------------

func TestDegradedRectGeometryMatchesNative(t *testing.T) {
	rec := NewRecorder(32, 32)
	rec.FillRectangle(4, 6, 10, 12)
	r := rec.FinishRecording()

	nativeBackend := newCapBackend(CapFillRect)
	if _, err := r.PlaybackWithOptions(nativeBackend, PlaybackOptions{Policy: PolicyStrict}); err != nil {
		t.Fatalf("native playback: %v", err)
	}

	degradedBackend := newCapBackend(CapFillPath)
	if _, err := r.PlaybackWithOptions(degradedBackend, PlaybackOptions{Policy: PolicyDegrade}); err != nil {
		t.Fatalf("degraded playback: %v", err)
	}

	if len(nativeBackend.fillRects) != 1 {
		t.Fatalf("native fillrect calls = %d, want 1", len(nativeBackend.fillRects))
	}
	if len(degradedBackend.fillPaths) != 1 {
		t.Fatalf("degraded fillpath calls = %d, want 1", len(degradedBackend.fillPaths))
	}
	rect := nativeBackend.fillRects[0]
	path := degradedBackend.fillPaths[0]
	got := path.Bounds()
	want := image.Rect(int(rect.MinX), int(rect.MinY), int(math.Ceil(rect.MaxX)), int(math.Ceil(rect.MaxY)))
	if got != want {
		t.Errorf("degraded path bounds = %v, want %v", got, want)
	}
}

func TestValidateCapabilitySetRejectsDroppingDrawingCommand(t *testing.T) {
	set := CapabilitySet{
		Native:    CapFillPath,
		Fallbacks: map[Capability]Capability{CapDrawImage: CapNone},
	}
	if err := validateCapabilitySet(set); !errors.Is(err, ErrCapabilityConflict) {
		t.Fatalf("error = %v, want ErrCapabilityConflict for dropping DrawImage", err)
	}
}

func TestCapabilityStringUnknownBits(t *testing.T) {
	got := (CapFillPath | Capability(1<<29)).String()
	if got != "FillPath|Unknown(0x20000000)" {
		t.Errorf("String() = %q", got)
	}
}

func TestPlaybackResultCompletenessNil(t *testing.T) {
	var r *PlaybackResult
	if !r.Completeness() {
		t.Error("nil PlaybackResult should be complete")
	}
	var report *CapabilityReport
	if !report.IsComplete() {
		t.Error("nil CapabilityReport should be complete")
	}
	if report.String() == "" {
		t.Error("nil CapabilityReport.String() should not be empty")
	}
}

func TestUnsupportedErrorWithoutReport(t *testing.T) {
	err := &UnsupportedError{}
	if !errors.Is(err, ErrUnsupportedCapability) {
		t.Error("UnsupportedError should unwrap to ErrUnsupportedCapability")
	}
	if err.Error() == "" {
		t.Error("empty UnsupportedError should still format")
	}
}

func TestPlaybackPolicyString(t *testing.T) {
	tests := map[PlaybackPolicy]string{
		PolicyStrict:       "strict",
		PolicySkip:         "skip",
		PolicyDegrade:      "degrade",
		PlaybackPolicy(42): "PlaybackPolicy(42)",
	}
	for policy, want := range tests {
		if got := policy.String(); got != want {
			t.Errorf("PlaybackPolicy(%d).String() = %q, want %q", policy, got, want)
		}
	}
}

func TestCapabilityReportString(t *testing.T) {
	// No issues: "complete".
	complete := &CapabilityReport{Declared: true}
	if got, want := complete.String(), "capability report: complete"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	// Undeclared backend.
	undeclared := &CapabilityReport{}
	if got := undeclared.String(); got == "" || got == complete.String() {
		t.Errorf("undeclared String() = %q", got)
	}
	// A mix of undegradable, dropped and rewritten issues, with several
	// command indexes per issue.
	report := &CapabilityReport{
		Declared: true,
		Missing:  []Capability{CapFillPath, CapFillRect, CapStyleDash},
		Issues: []CapabilityIssue{
			{Capability: CapFillPath, Commands: []int{2, 5}, Degradable: false},
			{Capability: CapFillRect, Commands: []int{7}, Fallback: CapFillPath, Degradable: true},
			{Capability: CapStyleDash, Commands: []int{9}, Degradable: true},
		},
	}
	got := report.String()
	for _, want := range []string{"FillPath", "commands 2,5", "FillRect", "-> FillPath", "SetDash", "-> drop"} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, missing %q", got, want)
		}
	}
	if report.IsComplete() {
		t.Error("report with missing capabilities should not be complete")
	}
}

func TestPlaybackErrorFormatting(t *testing.T) {
	if got := (&PlaybackError{}).Error(); got == "" {
		t.Error("zero PlaybackError should format")
	}
	withCommand := &PlaybackError{Index: 3, Command: CmdFillPath, HasCommand: true, Err: ErrMissingResource}
	if got := withCommand.Error(); !strings.Contains(got, "command 3") || !strings.Contains(got, "FillPath") {
		t.Errorf("Error() = %q", got)
	}
	withoutCommand := &PlaybackError{Index: 4, Err: ErrMissingResource}
	if got := withoutCommand.Error(); !strings.Contains(got, "command 4") {
		t.Errorf("Error() = %q", got)
	}
	if err := withCommand.Unwrap(); !errors.Is(err, ErrMissingResource) {
		t.Errorf("Unwrap() = %v", err)
	}
	var nilErr *PlaybackError
	if nilErr.Error() == "" || nilErr.Unwrap() != nil {
		t.Error("nil PlaybackError should format and unwrap to nil")
	}
}

// TestPlaybackUnsupportedErrorNamesAllGaps exercises the report attached to the
// strict-mode error, including the command indexes.
func TestPlaybackUnsupportedErrorNamesAllGaps(t *testing.T) {
	r := mixedRecording(t)
	backend := newCapBackend(CapNone)

	err := r.Playback(backend)
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	for _, want := range []string{"FillPath", "DrawImage", "DrawText"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %s", msg, want)
		}
	}
}

// collisionBackend declares CapClipRoundRect but not CapClipPath. A
// ClipRoundRect command is therefore native, and playback must hand the
// rounded path to SetClip directly.
type collisionBackend struct {
	*capBackend
	clipCalled bool
}

func (b *collisionBackend) BackendCapabilities() CapabilitySet {
	return CapabilitySet{Native: CapClipRoundRect | CapClearClip}
}

func (b *collisionBackend) SetClip(_ *gg.Path, _ FillRule) { b.clipCalled = true }

func TestRoundRectClipWithoutPathClipIsServedNatively(t *testing.T) {
	rec := NewRecorder(16, 16)
	rec.ClipRoundRect(2, 2, 12, 12, 3)
	r := rec.FinishRecording()

	backend := &collisionBackend{capBackend: newCapBackend(CapNone)}
	result, err := r.PlaybackWithOptions(backend, PlaybackOptions{Policy: PolicyStrict})
	if err != nil {
		t.Fatalf("PlaybackWithOptions: %v", err)
	}
	if !result.Completeness() {
		t.Errorf("ClipRoundRect should be native for this backend: %+v", result)
	}
	if !backend.clipCalled {
		t.Error("SetClip was not called for the native rounded clip")
	}
}

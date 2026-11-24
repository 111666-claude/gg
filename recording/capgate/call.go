// Package capgate implements the rendering-backend capability-consistency
// quality gate.
//
// The gate replays a deterministic command Matrix (paths, rounded rectangles,
// clipping, images, text, gradients and every blend mode) against a backend
// and cross-checks four sources of truth per case:
//
//  1. the backend's declared recording.Capabilities,
//  2. the recording.Preflight result,
//  3. the actual Backend methods observed during playback (CallLog),
//  4. any declared/observed fallback path.
//
// It produces locatable findings for:
//
//   - declared_supported_error: capability declared, playback returned an error,
//   - undeclared_draw: capability missing from the declaration but drawing ran,
//   - capability_list_unstable: repeated Capabilities() probes disagree,
//   - midrun_failure: the backend failed at a specific command index,
//   - silent_drop: declared support, no error, but the expected call never arrived,
//   - recording_mutated: playback rewrote the Recording/resources,
//   - stack_unbalanced: Save/Restore calls were not balanced.
//
// The package is pure-Go and never initializes a GPU. The real CPU raster
// backend is exercised through the same gate in tests.
package capgate

import (
	"errors"
	"image"
	"strconv"
	"sync"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/recording"
	"github.com/gogpu/gg/text"
)

// ErrInjectedMidRun is the sticky error used by SpyConfig.FailAfterCalls.
var ErrInjectedMidRun = errors.New("capgate: injected mid-run backend failure")

func itoa(i int) string { return strconv.Itoa(i) }

// Op names recorded by the instrumentation. They match the Backend method
// names plus Begin/End.
const (
	OpBegin          = "Begin"
	OpEnd            = "End"
	OpSave           = "Save"
	OpRestore        = "Restore"
	OpSetTransform   = "SetTransform"
	OpSetClip        = "SetClip"
	OpClearClip      = "ClearClip"
	OpFillPath       = "FillPath"
	OpStrokePath     = "StrokePath"
	OpFillRect       = "FillRect"
	OpDrawImage      = "DrawImage"
	OpDrawText       = "DrawText"
	OpSetBlendMode   = "SetBlendMode"
	OpFallbackMarker = "Fallback"
)

// CallRecord is one observed backend method invocation.
type CallRecord struct {
	// Seq is the 0-based order among all recorded calls.
	Seq int `json:"seq"`
	// Op is one of the Op* constants.
	Op string `json:"op"`
	// CommandIndex is the Recording command that triggered the call, when
	// known (-1 for lifecycle calls). The Instrumented wrapper sets this
	// from the gate's indexed replay; plain Playback leaves it -1.
	CommandIndex int `json:"command_index"`
	// Detail is an optional stable description (brush kind, blend mode, ...).
	Detail string `json:"detail,omitempty"`
}

// CallLog is a goroutine-safe ordered list of observed calls.
type CallLog struct {
	mu    sync.Mutex
	calls []CallRecord
}

// NewCallLog returns an empty call log.
func NewCallLog() *CallLog { return &CallLog{} }

func (l *CallLog) add(op, detail string, cmdIndex int) CallRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec := CallRecord{Seq: len(l.calls), Op: op, Detail: detail, CommandIndex: cmdIndex}
	l.calls = append(l.calls, rec)
	return rec
}

// Calls returns a copy of the recorded calls.
func (l *CallLog) Calls() []CallRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]CallRecord, len(l.calls))
	copy(out, l.calls)
	return out
}

// Count returns how many calls with the given op were recorded.
func (l *CallLog) Count(op string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, c := range l.calls {
		if c.Op == op {
			n++
		}
	}
	return n
}

// Has reports whether at least one call with one of the given ops exists.
func (l *CallLog) Has(ops ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range l.calls {
		for _, op := range ops {
			if c.Op == op {
				return true
			}
		}
	}
	return false
}

// Reset empties the log.
func (l *CallLog) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = l.calls[:0]
}

// Backend is the union of interfaces the gate subjects may implement.
type Backend = recording.Backend

// ensure *Spy implements all optional recording interfaces the gate audits.
var (
	_ recording.Backend          = (*Spy)(nil)
	_ recording.BlendModeBackend = (*Spy)(nil)
	_ recording.CapableBackend   = (*Spy)(nil)
	_ recording.ErrorReporter    = (*Spy)(nil)
)

// SpyConfig configures a Spy test double.
//
// Fault injection fields are intentionally simple so JSONL samples stay
// reproducible: configure one fault per subject.
type SpyConfig struct {
	// Name labels the subject in reports.
	Name string

	// Caps is the fixed capability profile returned by Capabilities.
	Caps recording.Capabilities

	// CapsSequence, when non-empty, makes Capabilities() return successive
	// profiles (cyclically). Used to inject unstable declarations.
	CapsSequence []recording.Capabilities

	// FailBegin, when non-nil, is returned by Begin.
	FailBegin error
	// FailEnd, when non-nil, is returned by End.
	FailEnd error

	// FailOn maps an op name to a sticky error: the first matching call
	// stores the error, which Err then reports to PlaybackContext.
	FailOn map[string]error

	// FailAfterCalls, when > 0, makes the Spy store ErrMidRun after that
	// many recorded calls.
	FailAfterCalls int

	// MutatePaths makes FillPath/StrokePath/SetClip append a line to the
	// received *gg.Path, simulating a backend/adapter that rewrites pooled
	// resources. The gate must flag recording_mutated.
	MutatePaths bool

	// RestoreAlwaysUnderflow makes every Restore count as an underflow,
	// simulating a backend whose state stack is not balanced.
	RestoreAlwaysUnderflow bool
}

// Spy is a recording.Backend test double that records every invocation and
// can inject capability/error/mutation faults.
//
// It also tracks its Save/Restore state stack depth and Restore underflow.
type Spy struct {
	log *CallLog
	cfg SpyConfig

	mu        sync.Mutex
	capsCalls int
	depth     int
	maxDepth  int
	underflow int
	err       error
}

// NewSpy creates a configured spy.
func NewSpy(cfg SpyConfig) *Spy {
	if cfg.Name == "" {
		cfg.Name = "spy"
	}
	return &Spy{log: NewCallLog(), cfg: cfg}
}

// Log returns the spy's call log.
func (s *Spy) Log() *CallLog { return s.log }

// CallLog implements CallRecorder.
func (s *Spy) CallLog() *CallLog { return s.log }

// Err implements recording.ErrorReporter.
func (s *Spy) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// StackBalanced reports whether Save/Restore ended at depth zero without
// Restore underflow.
func (s *Spy) StackBalanced() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.depth == 0 && s.underflow == 0
}

// MaxDepth returns the maximum Save nesting observed.
func (s *Spy) MaxDepth() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxDepth
}

// Underflow returns the number of Restore calls made at empty depth.
func (s *Spy) Underflow() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.underflow
}

func (s *Spy) record(op, detail string) {
	s.log.add(op, detail, -1)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.FailAfterCalls > 0 && s.err == nil && len(s.log.calls) >= s.cfg.FailAfterCalls {
		s.err = ErrInjectedMidRun
	}
	if err, ok := s.cfg.FailOn[op]; ok && s.err == nil {
		s.err = err
	}
}

// Capabilities implements recording.CapableBackend.
func (s *Spy) Capabilities() recording.Capabilities {
	s.mu.Lock()
	seq := s.cfg.CapsSequence
	idx := s.capsCalls
	s.capsCalls++
	s.mu.Unlock()
	if len(seq) > 0 {
		return seq[idx%len(seq)]
	}
	return s.cfg.Caps
}

// Begin implements recording.Backend.
func (s *Spy) Begin(width, height int) error {
	s.record(OpBegin, "")
	return s.cfg.FailBegin
}

// End implements recording.Backend.
func (s *Spy) End() error {
	s.record(OpEnd, "")
	return s.cfg.FailEnd
}

// Save implements recording.Backend.
func (s *Spy) Save() {
	s.record(OpSave, "")
	s.mu.Lock()
	s.depth++
	if s.depth > s.maxDepth {
		s.maxDepth = s.depth
	}
	s.mu.Unlock()
}

// Restore implements recording.Backend.
func (s *Spy) Restore() {
	s.record(OpRestore, "")
	s.mu.Lock()
	if s.cfg.RestoreAlwaysUnderflow {
		s.underflow++
	} else if s.depth == 0 {
		s.underflow++
	} else {
		s.depth--
	}
	s.mu.Unlock()
}

// SetTransform implements recording.Backend.
func (s *Spy) SetTransform(m recording.Matrix) {
	s.record(OpSetTransform, "")
}

// SetClip implements recording.Backend.
func (s *Spy) SetClip(path *gg.Path, rule recording.FillRule) {
	detail := ""
	if path != nil {
		detail = "verbs=" + itoa(path.NumVerbs())
		if s.cfg.MutatePaths {
			path.LineTo(12345.0, 67890.0)
		}
	}
	s.record(OpSetClip, detail)
}

// ClearClip implements recording.Backend.
func (s *Spy) ClearClip() { s.record(OpClearClip, "") }

// FillPath implements recording.Backend.
func (s *Spy) FillPath(path *gg.Path, brush recording.Brush, rule recording.FillRule) {
	detail := BrushKind(brush)
	if path != nil {
		detail += ";verbs=" + itoa(path.NumVerbs())
		if s.cfg.MutatePaths {
			path.LineTo(12345.0, 67890.0)
		}
	}
	s.record(OpFillPath, detail)
}

// StrokePath implements recording.Backend.
func (s *Spy) StrokePath(path *gg.Path, brush recording.Brush, stroke recording.Stroke) {
	detail := BrushKind(brush)
	if path != nil {
		detail += ";verbs=" + itoa(path.NumVerbs())
		if s.cfg.MutatePaths {
			path.LineTo(12345.0, 67890.0)
		}
	}
	s.record(OpStrokePath, detail)
}

// FillRect implements recording.Backend.
func (s *Spy) FillRect(rect recording.Rect, brush recording.Brush) {
	s.record(OpFillRect, BrushKind(brush))
}

// DrawImage implements recording.Backend.
func (s *Spy) DrawImage(img image.Image, src, dst recording.Rect, opts recording.ImageOptions) {
	detail := "nil"
	if img != nil {
		b := img.Bounds()
		detail = itoa(b.Dx()) + "x" + itoa(b.Dy())
	}
	s.record(OpDrawImage, detail)
}

// DrawText implements recording.Backend.
func (s *Spy) DrawText(str string, x, y float64, face text.Face, brush recording.Brush) {
	s.record(OpDrawText, "text="+str)
}

// SetBlendMode implements recording.BlendModeBackend.
func (s *Spy) SetBlendMode(mode recording.BlendMode) {
	s.record(OpSetBlendMode, "mode="+itoa(int(mode)))
}

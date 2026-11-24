package capgate

import (
	"errors"

	"github.com/gogpu/gg/recording"
)

// FaultSample pairs a deliberately defective subject with the matrix case
// that exposes it and the finding code the gate must produce.
type FaultSample struct {
	// Name is the stable sample identifier used in JSONL.
	Name string `json:"name"`
	// ExpectCode is the finding code that must be observed.
	ExpectCode string `json:"expect_code"`
	// CaseID is the matrix case to run ("" for subject-level samples).
	CaseID string `json:"case_id,omitempty"`
	// Subject is the defective gate subject.
	Subject Subject `json:"-"`
	// StabilityOnly marks subject-level (capability list) samples.
	StabilityOnly bool `json:"-"`
}

// FaultSamples returns the deterministic fault-injection fleet.
//
// It covers the four required locatable report classes plus the silent
// dropped-draw and resource-mutation defects:
//
//   - declared_supported_error
//   - undeclared_draw
//   - capability_list_unstable (subject-level)
//   - backend_midrun_failure
//   - silent_drop (image and blend variants)
//   - recording_mutated
//   - stack_unbalanced
func FaultSamples() []FaultSample {
	full := FullCapabilities()
	empty := recording.Capabilities{Name: "empty"}

	return []FaultSample{
		{
			Name:       "declared-capability-returns-error",
			ExpectCode: CodeDeclaredSupportedError,
			CaseID:     "path/fill-triangle",
			Subject: Subject{
				Name:    "fault-declared-error",
				Profile: profilePtr(full),
				New: func() recording.Backend {
					return NewSpy(SpyConfig{
						Name:      "fault-declared-error",
						Caps:      full,
						FailBegin: errors.New("simulated device initialization failure"),
					})
				},
			},
		},
		{
			Name:       "declared-unsupported-yet-draws",
			ExpectCode: CodeUndeclaredDraw,
			CaseID:     "rect/fill",
			Subject: Subject{
				Name:    "fault-undeclared-draw",
				Profile: profilePtr(empty),
				New: func() recording.Backend {
					return NewSpy(SpyConfig{Name: "fault-undeclared-draw", Caps: empty})
				},
			},
		},
		{
			Name:          "missing-capability-list-unstable",
			ExpectCode:    CodeCapListUnstable,
			StabilityOnly: true,
			Subject: Subject{
				Name: "fault-unstable-caps",
				// No static Profile: probes hit the Spy's cycling Capabilities.
				New: func() recording.Backend {
					return NewSpy(SpyConfig{
						Name: "fault-unstable-caps",
						CapsSequence: []recording.Capabilities{
							full,
							{Name: "partial", Features: []recording.Capability{recording.CapFillPath}},
							empty,
						},
					})
				},
			},
		},
		{
			Name:       "backend-fails-mid-playback",
			ExpectCode: CodeMidRunFailure,
			CaseID:     "path/fill-triangle",
			Subject: Subject{
				Name:    "fault-midrun",
				Profile: profilePtr(full),
				New: func() recording.Backend {
					return NewSpy(SpyConfig{
						Name: "fault-midrun",
						Caps: full,
						FailOn: map[string]error{
							OpFillPath: errors.New("simulated rasterizer crash"),
						},
					})
				},
			},
		},
		{
			Name:       "silent-drop-image-command",
			ExpectCode: CodeSilentDrop,
			CaseID:     "image/draw",
			Subject: Subject{
				Name:    "fault-silent-image",
				Profile: profilePtr(full),
				New:     func() recording.Backend { return NewSilentDropBackend(OpDrawImage) },
			},
		},
		{
			Name:       "silent-drop-blend-command",
			ExpectCode: CodeSilentDrop,
			CaseID:     "blend/multiply-legacy",
			Subject: Subject{
				Name:    "fault-silent-blend",
				Profile: profilePtr(full),
				New:     func() recording.Backend { return NewSilentDropBackend(OpSetBlendMode) },
			},
		},
		{
			Name:       "declares-blend-but-lacks-optional-interface",
			ExpectCode: CodeMissingOptionalIface,
			CaseID:     "blend/screen-legacy",
			Subject: Subject{
				Name:    "fault-no-blend-interface",
				Profile: profilePtr(full),
				New:     func() recording.Backend { return NewNoBlendBackend() },
			},
		},
		{
			Name:       "backend-rewrites-pooled-path",
			ExpectCode: CodeRecordingMutated,
			CaseID:     "path/fill-triangle",
			Subject: Subject{
				Name:    "fault-mutating",
				Profile: profilePtr(full),
				New: func() recording.Backend {
					return NewSpy(SpyConfig{Name: "fault-mutating", Caps: full, MutatePaths: true})
				},
			},
		},
		{
			Name:       "backend-state-stack-unbalanced",
			ExpectCode: CodeStackUnbalanced,
			CaseID:     "state/save-restore",
			Subject: Subject{
				Name:    "fault-stack",
				Profile: profilePtr(full),
				New: func() recording.Backend {
					return NewSpy(SpyConfig{
						Name:                   "fault-stack",
						Caps:                   full,
						RestoreAlwaysUnderflow: true,
					})
				},
			},
		},
	}
}

// NoBlendBackend implements recording.Backend (delegating to a perfect spy)
// but deliberately does NOT implement recording.BlendModeBackend, while its
// profile still declares blend_modes. It models a declaration/interface
// mismatch that silently prevents blend delivery.
type NoBlendBackend struct {
	recording.Backend
	spy *Spy
}

// NewNoBlendBackend constructs the mismatch backend.
func NewNoBlendBackend() *NoBlendBackend {
	s := NewPerfectSpy()
	return &NoBlendBackend{Backend: s, spy: s}
}

// CallLog exposes the delegated spy log.
func (b *NoBlendBackend) CallLog() *CallLog { return b.spy.CallLog() }

// Capabilities returns the full (incorrectly optimistic) profile.
func (b *NoBlendBackend) Capabilities() recording.Capabilities { return FullCapabilities() }

// Note: SetBlendMode is intentionally absent, so a type assertion to
// recording.BlendModeBackend fails even though the embedded dynamic value is
// itself a BlendModeBackend (interface promotion uses the static
// recording.Backend field type).
var _ recording.Backend = (*NoBlendBackend)(nil)

package capgate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/gogpu/gg/recording"
	"github.com/gogpu/gg/text"
)

// SchemaVersion is the JSONL schema identifier.
const SchemaVersion = "capgate/v1"

// Verdict codes for a single matrix case.
const (
	VerdictPass              = "pass"
	VerdictPassFallback      = "pass_via_fallback"
	VerdictUnsupportedSilent = "unsupported_silent"
	VerdictSilentDrop        = "silent_drop"
	VerdictDeclaredError     = "declared_supported_error"
	VerdictUndeclaredDraw    = "undeclared_draw"
	VerdictMidRunFailure     = "backend_midrun_failure"
	VerdictMutated           = "recording_mutated"
	VerdictStackUnbalanced   = "stack_unbalanced"
	VerdictCanceled          = "canceled"
	VerdictPlaybackError     = "playback_error"
)

// Finding codes. They double as stable JSONL counters.
const (
	CodeDeclaredSupportedError = "declared_supported_error"
	CodeUndeclaredDraw         = "undeclared_draw"
	CodeCapListUnstable        = "capability_list_unstable"
	CodeMidRunFailure          = "backend_midrun_failure"
	CodeSilentDrop             = "silent_drop"
	CodeRecordingMutated       = "recording_mutated"
	CodeStackUnbalanced        = "stack_unbalanced"
	CodeMissingOptionalIface   = "missing_optional_interface"
)

// Severity levels.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// CallRecorder is implemented by backends that expose their observed call log.
type CallRecorder interface {
	CallLog() *CallLog
}

// StackBalanceReporter is implemented by doubles that track Save/Restore balance.
type StackBalanceReporter interface {
	StackBalanced() bool
}

// Finding is a locatable capability-consistency deviation.
type Finding struct {
	// Code is one of the Code* constants.
	Code string `json:"code"`
	// Severity is error or warning.
	Severity string `json:"severity"`
	// Subject is the backend/subject name.
	Subject string `json:"subject"`
	// CaseID is the matrix case identifier, when case-scoped.
	CaseID string `json:"case_id,omitempty"`
	// CommandIndex locates the failing Recording command, when known.
	CommandIndex int `json:"command_index,omitempty"`
	// Phase is the playback phase (begin/command/end/canceled).
	Phase string `json:"phase,omitempty"`
	// Message is a human-readable, fully locatable explanation.
	Message string `json:"message"`
}

// Subject binds a named backend factory to an optional explicit profile.
type Subject struct {
	// Name is the stable report label.
	Name string `json:"name"`
	// New returns a fresh backend for one case or stability probe.
	New func() recording.Backend `json:"-"`
	// Profile optionally overrides capability probing. When nil the gate
	// type-asserts CapableBackend on the created backend.
	Profile *recording.Capabilities `json:"profile,omitempty"`
	// StabilityProbes is the number of Capabilities() calls compared.
	// Defaults to 5.
	StabilityProbes int `json:"stability_probes,omitempty"`
}

func (s Subject) probes() int {
	if s.StabilityProbes <= 0 {
		return 5
	}
	return s.StabilityProbes
}

func (s Subject) resolveCaps(b recording.Backend) recording.Capabilities {
	if s.Profile != nil {
		return s.Profile.Normalize()
	}
	if cb, ok := b.(recording.CapableBackend); ok {
		return cb.Capabilities().Normalize()
	}
	return recording.Capabilities{}
}

// CaseResult is the cross-checked verdict for one subject × matrix case.
type CaseResult struct {
	Type string `json:"type"` // always "case_result"

	Schema  string `json:"schema"`
	Subject string `json:"subject"`
	CaseID  string `json:"case_id"`
	Area    string `json:"area"`
	Feature string `json:"feature"`
	Blend   int    `json:"blend_mode,omitempty"`

	// Declared is whether the subject's profile declares the case feature
	// (for blend cases: whether the exact mode is listed).
	Declared bool `json:"declared"`

	// Preflight cross-check.
	PreflightOK      bool     `json:"preflight_ok"`
	PreflightMissing []string `json:"preflight_missing,omitempty"`
	PreflightIssues  int      `json:"preflight_issues"`

	// Observed behavior.
	Verdict       string       `json:"verdict"`
	ExpectedCalls []string     `json:"expected_calls,omitempty"`
	MatchHint     string       `json:"match_hint,omitempty"`
	Calls         []CallRecord `json:"calls,omitempty"`

	// Error location.
	PlaybackError string `json:"playback_error,omitempty"`
	ErrorPhase    string `json:"error_phase,omitempty"`
	ErrorIndex    int    `json:"error_index,omitempty"`

	// Integrity checks.
	RecordingUnmodified bool   `json:"recording_unmodified"`
	DigestBefore        string `json:"digest_before,omitempty"`
	DigestAfter         string `json:"digest_after,omitempty"`
	StackChecked        bool   `json:"stack_checked"`
	StackBalanced       bool   `json:"stack_balanced"`

	Findings []Finding `json:"findings,omitempty"`
}

// StabilityResult reports repeated Capabilities() probe consistency.
type StabilityResult struct {
	Type string `json:"type"` // "capability_stability"

	Schema  string `json:"schema"`
	Subject string `json:"subject"`
	Probes  int    `json:"probes"`
	Stable  bool   `json:"stable"`
	// Profiles lists the distinct normalized profiles observed, as canonical
	// JSON strings. Sorted for reproducibility.
	Profiles []string `json:"profiles,omitempty"`
	// MissingLists lists the distinct canonical "missing capability" lists
	// computed against FullCapabilities for the observed profiles. This is
	// the direct evidence for an unstable missing-capability manifest.
	MissingLists []string `json:"missing_lists,omitempty"`
	Finding      *Finding `json:"finding,omitempty"`
}

// Summary is the reproducible JSONL summary record.
type Summary struct {
	Type   string `json:"type"` // "summary"
	Schema string `json:"schema"`

	Subjects      int `json:"subjects"`
	MatrixCases   int `json:"matrix_cases"`
	CaseResults   int `json:"case_results"`
	StabilityRuns int `json:"stability_runs"`
	StableRuns    int `json:"stable_runs"`

	ByVerdict       map[string]int `json:"by_verdict"`
	FindingsByCode  map[string]int `json:"findings_by_code"`
	ErrorFindings   int            `json:"error_findings"`
	WarningFindings int            `json:"warning_findings"`

	// GatePass is true only when there are zero error-severity findings.
	GatePass bool `json:"gate_pass"`
}

// Report is the full gate output.
type Report struct {
	Schema    string            `json:"schema"`
	Cases     int               `json:"matrix_cases"`
	Results   []CaseResult      `json:"results"`
	Stability []StabilityResult `json:"stability"`
	Findings  []Finding         `json:"findings"`
	Summary   Summary           `json:"summary"`
}

// Run executes stability audits and the full matrix for every subject.
func Run(subjects []Subject, cases []Case, face text.Face) *Report {
	return RunContext(context.Background(), subjects, cases, face)
}

// RunContext is Run with a context used for cancellation.
func RunContext(ctx context.Context, subjects []Subject, cases []Case, face text.Face) *Report {
	rep := &Report{Schema: SchemaVersion, Cases: len(cases)}
	rep.Summary.ByVerdict = map[string]int{}
	rep.Summary.FindingsByCode = map[string]int{}

	for _, subj := range subjects {
		stab := AuditStability(subj)
		rep.Stability = append(rep.Stability, stab)
		if stab.Finding != nil {
			rep.Findings = append(rep.Findings, *stab.Finding)
		}

		for _, c := range cases {
			res := RunCaseContext(ctx, subj, c, face)
			rep.Results = append(rep.Results, res)
			rep.Findings = append(rep.Findings, res.Findings...)
		}
	}

	summarize(rep, len(subjects))
	return rep
}

func summarize(rep *Report, numSubjects int) {
	s := Summary{
		Type:           "summary",
		Schema:         SchemaVersion,
		Subjects:       numSubjects,
		MatrixCases:    rep.Cases,
		CaseResults:    len(rep.Results),
		StabilityRuns:  len(rep.Stability),
		ByVerdict:      map[string]int{},
		FindingsByCode: map[string]int{},
	}
	for _, st := range rep.Stability {
		if st.Stable {
			s.StableRuns++
		}
	}
	for _, r := range rep.Results {
		s.ByVerdict[r.Verdict]++
	}
	for _, f := range rep.Findings {
		s.FindingsByCode[f.Code]++
		switch f.Severity {
		case SeverityWarning:
			s.WarningFindings++
		default:
			s.ErrorFindings++
		}
	}
	s.GatePass = s.ErrorFindings == 0
	rep.Summary = s
}

// AuditStability probes Capabilities() repeatedly and checks that normalized
// profiles are identical across probes.
func AuditStability(subj Subject) StabilityResult {
	n := subj.probes()
	st := StabilityResult{
		Type: "capability_stability", Schema: SchemaVersion,
		Subject: subj.Name, Probes: n, Stable: true,
	}
	if subj.New == nil {
		st.Stable = false
		f := Finding{Code: CodeCapListUnstable, Severity: SeverityError, Subject: subj.Name,
			Message: "subject has no backend factory"}
		st.Finding = &f
		st.Profiles = []string{"<nil factory>"}
		return st
	}
	b := subj.New()
	profiles := map[string]struct{}{}
	missingLists := map[string]struct{}{}
	var distinct []string
	var distinctMissing []string
	full := FullCapabilities()
	for i := 0; i < n; i++ {
		caps := subj.resolveCaps(b)
		norm := caps.Normalize()
		canon := canonicalCaps(norm)
		if _, ok := profiles[canon]; !ok {
			profiles[canon] = struct{}{}
			distinct = append(distinct, canon)

			missing := norm.MissingCapabilities(full.Features)
			for _, m := range full.BlendModes {
				if !norm.SupportsBlendMode(m) {
					missing = append(missing, recording.Capability("blend_mode:"+itoa(int(m))))
				}
			}
			sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
			missCanon := canonicalCaps(recording.Capabilities{Features: dedupCaps(missing)})
			if _, ok := missingLists[missCanon]; !ok {
				missingLists[missCanon] = struct{}{}
				distinctMissing = append(distinctMissing, missCanon)
			}
		}
	}
	sort.Strings(distinct)
	sort.Strings(distinctMissing)
	st.Profiles = distinct
	st.MissingLists = distinctMissing
	if len(distinct) > 1 || len(distinctMissing) > 1 {
		st.Stable = false
		f := Finding{
			Code: CodeCapListUnstable, Severity: SeverityError, Subject: subj.Name,
			Message: fmt.Sprintf(
				"Capabilities() for %q returned %d distinct profiles / %d distinct missing-capability lists across %d probes; the capability manifest is not stable",
				subj.Name, len(distinct), len(distinctMissing), n),
		}
		st.Finding = &f
	}
	return st
}

func dedupCaps(in []recording.Capability) []recording.Capability {
	seen := map[recording.Capability]struct{}{}
	var out []recording.Capability
	for _, c := range in {
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// RunCase runs one subject × case with a background context.
func RunCase(subj Subject, c Case, face text.Face) CaseResult {
	return RunCaseContext(context.Background(), subj, c, face)
}

// RunCaseContext contains the core four-way cross-check.
func RunCaseContext(ctx context.Context, subj Subject, c Case, face text.Face) CaseResult {
	res := CaseResult{
		Type: "case_result", Schema: SchemaVersion,
		Subject: subj.Name, CaseID: c.ID, Area: c.Category,
		Feature: string(c.Feature), ExpectedCalls: c.WantOps, MatchHint: c.MatchHint,
		ErrorIndex: -1,
	}
	if c.Category == "blend" {
		res.Blend = int(c.Blend)
	}
	addFinding := func(code, severity, msg string) {
		res.Findings = append(res.Findings, Finding{
			Code: code, Severity: severity, Subject: subj.Name, CaseID: c.ID,
			CommandIndex: res.ErrorIndex, Phase: res.ErrorPhase, Message: msg,
		})
	}

	if subj.New == nil {
		res.Verdict = VerdictPlaybackError
		res.PlaybackError = "subject has no backend factory"
		addFinding(CodeDeclaredSupportedError, SeverityError, res.PlaybackError)
		return res
	}

	rec := c.Build(face)
	b := subj.New()
	caps := subj.resolveCaps(b)

	// 1) Declaration.
	if c.Category == "blend" {
		res.Declared = caps.SupportsBlendMode(c.Blend)
	} else {
		res.Declared = caps.Has(c.Feature)
	}

	// 2) Preflight.
	pre := recording.Preflight(rec, caps)
	res.PreflightOK = pre.OK
	res.PreflightMissing = capsListToStrings(pre.Missing)
	res.PreflightIssues = len(pre.Issues)

	// 3) Input fingerprint before playback.
	before := Fingerprint(rec)
	res.DigestBefore = before.SHA256

	// 4) Actual playback.
	err := rec.PlaybackContext(ctx, b)

	after := Fingerprint(rec)
	res.DigestAfter = after.SHA256
	res.RecordingUnmodified = before.Unchanged(after)
	if !res.RecordingUnmodified {
		addFinding(CodeRecordingMutated, SeverityError,
			fmt.Sprintf("backend %q rewrote the input Recording/resources for case %q (digest %s -> %s)",
				subj.Name, c.ID, before.SHA256[:12], after.SHA256[:12]))
	}

	if cl, ok := b.(CallRecorder); ok && cl.CallLog() != nil {
		res.Calls = cl.CallLog().Calls()
	}
	if sr, ok := b.(StackBalanceReporter); ok {
		res.StackChecked = true
		res.StackBalanced = sr.StackBalanced()
		if !res.StackBalanced {
			addFinding(CodeStackUnbalanced, SeverityError,
				fmt.Sprintf("backend %q Save/Restore stack unbalanced or underflowed for case %q",
					subj.Name, c.ID))
		}
	}

	// Cancellation is a separate, expected verdict for edge tests.
	if err != nil && errors.Is(err, context.Canceled) {
		res.Verdict = VerdictCanceled
		res.PlaybackError = err.Error()
		var pe *recording.PlaybackError
		if errors.As(err, &pe) {
			res.ErrorPhase = pe.Phase
			res.ErrorIndex = pe.Index
		}
		return res
	}

	// Error location extraction.
	var pe *recording.PlaybackError
	if errors.As(err, &pe) {
		res.ErrorPhase = pe.Phase
		res.ErrorIndex = pe.Index
	}
	if err != nil {
		res.PlaybackError = err.Error()
	}

	// Observed-call matching.
	drawObserved := false
	if c.Match != nil {
		drawObserved = c.Match(res.Calls)
	} else {
		for _, wanted := range c.WantOps {
			for _, got := range res.Calls {
				if got.Op == wanted {
					drawObserved = true
					break
				}
			}
			if drawObserved {
				break
			}
		}
	}

	// 5) Four-way consistency classification.
	switch {
	case err != nil && pe != nil && pe.Phase == recording.PlaybackPhaseCommand:
		res.Verdict = VerdictMidRunFailure
		addFinding(CodeMidRunFailure, SeverityError,
			fmt.Sprintf("backend %q failed midway for case %q at command #%d (%s): %v",
				subj.Name, c.ID, pe.Index, pe.Command, pe.Err))
		if res.Declared {
			addFinding(CodeDeclaredSupportedError, SeverityError,
				fmt.Sprintf("feature %q is declared by %q but playback returned an error: %v",
					c.Feature, subj.Name, pe.Err))
		}

	case err != nil && res.Declared:
		res.Verdict = VerdictDeclaredError
		addFinding(CodeDeclaredSupportedError, SeverityError,
			fmt.Sprintf("capability %q declared by %q for case %q but playback returned an error (phase=%s): %v",
				c.Feature, subj.Name, c.ID, res.ErrorPhase, err))

	case err != nil:
		res.Verdict = VerdictPlaybackError

	case res.Declared && drawObserved:
		res.Verdict = VerdictPass

	case res.Declared && !drawObserved:
		res.Verdict = VerdictSilentDrop
		hint := ""
		if c.MatchHint != "" {
			hint = " (" + c.MatchHint + ")"
		}
		addFinding(CodeSilentDrop, SeverityError,
			fmt.Sprintf("backend %q declares %q for case %q and returned no error, but no expected call %v was observed%s: drawing was silently dropped",
				subj.Name, c.Feature, c.ID, c.WantOps, hint))
		// Declaring blend support without the optional interface is a
		// distinct, structurally locatable defect.
		if c.Category == "blend" {
			if _, ok := b.(recording.BlendModeBackend); !ok {
				addFinding(CodeMissingOptionalIface, SeverityError,
					fmt.Sprintf("backend %q declares blend_modes but does not implement recording.BlendModeBackend, so SetBlendModeCommand cannot be delivered",
						subj.Name))
			}
		}

	case !res.Declared && drawObserved:
		fallback := caps.FallbackFor(c.Feature)
		carrierOK := false
		if c.Feature == recording.CapClipRoundRect && caps.Has(recording.CapClipPath) {
			carrierOK = true
		}
		if c.Feature == recording.CapStrokeText && caps.Has(recording.CapDrawText) {
			carrierOK = true
		}
		if fallback != "" || carrierOK {
			res.Verdict = VerdictPassFallback
		} else {
			res.Verdict = VerdictUndeclaredDraw
			addFinding(CodeUndeclaredDraw, SeverityError,
				fmt.Sprintf("backend %q does not declare %q for case %q, yet the draw call %v occurred and no fallback/carrier is declared",
					subj.Name, c.Feature, c.ID, c.WantOps))
		}

	default:
		res.Verdict = VerdictUnsupportedSilent
	}

	return res
}

// WriteJSONL writes stability records, case results and the summary as one
// JSON object per line. Output is reproducible: no timestamps and sorted
// map keys (encoding/json sorts map keys).
func (r *Report) WriteJSONL(w io.Writer) error {
	enc := json.NewEncoder(w)
	for _, s := range r.Stability {
		if err := enc.Encode(s); err != nil {
			return err
		}
	}
	for _, cr := range r.Results {
		if err := enc.Encode(cr); err != nil {
			return err
		}
	}
	return enc.Encode(r.Summary)
}

func canonicalCaps(c recording.Capabilities) string {
	b, err := json.Marshal(c.Normalize())
	if err != nil {
		return err.Error()
	}
	return string(b)
}

func capsListToStrings(in []recording.Capability) []string {
	out := make([]string, len(in))
	for i, c := range in {
		out[i] = string(c)
	}
	return out
}

package recording

import (
	"fmt"
	"math"
	"sort"
)

// Capability is a stable, machine-readable identifier for a feature that a
// Backend may declare or be preflighted against.
//
// Capability values are part of the capability-gate contract: tests and
// tooling may rely on their string form in JSONL reports.
type Capability string

// Backend capability identifiers.
const (
	// CapSaveRestore covers Save/Restore state-stack handling.
	CapSaveRestore Capability = "save_restore"
	// CapSetTransform covers the SetTransform method.
	CapSetTransform Capability = "set_transform"
	// CapFillPath covers the FillPath method.
	CapFillPath Capability = "fill_path"
	// CapStrokePath covers the StrokePath method (and StrokeRect lowering).
	CapStrokePath Capability = "stroke_path"
	// CapFillRect covers the optimized FillRect method.
	CapFillRect Capability = "fill_rect"
	// CapRoundRect declares faithful rendering of rounded-corner path
	// geometry carried by FillPath/StrokePath.
	CapRoundRect Capability = "round_rect"
	// CapClipPath covers the SetClip method.
	CapClipPath Capability = "clip_path"
	// CapClipRoundRect declares native/f faithful rounded-rect clip handling.
	// Core playback lowers the command to SetClip with a curve path, so
	// CapClipPath is the delivery carrier when this capability is absent.
	CapClipRoundRect Capability = "clip_round_rect"
	// CapClearClip covers the ClearClip method.
	CapClearClip Capability = "clear_clip"
	// CapDrawImage covers the DrawImage method.
	CapDrawImage Capability = "draw_image"
	// CapDrawText covers the DrawText method.
	CapDrawText Capability = "draw_text"
	// CapStrokeText declares native outline stroking of text. Core playback
	// currently lowers StrokeTextCommand to DrawText, which is lossy.
	CapStrokeText Capability = "stroke_text"
	// CapGradientLinear declares correct linear-gradient brush handling.
	CapGradientLinear Capability = "gradient_linear"
	// CapGradientRadial declares correct radial-gradient brush handling.
	CapGradientRadial Capability = "gradient_radial"
	// CapGradientSweep declares correct sweep-gradient brush handling.
	CapGradientSweep Capability = "gradient_sweep"
	// CapBlendModes declares configurable blend-mode support; the exact
	// modes are listed in Capabilities.BlendModes.
	CapBlendModes Capability = "blend_modes"
)

// Capabilities describes the features a Backend declares support for.
//
// Declarations are audited by the capability gate against preflight results
// and observed playback calls. The zero value declares nothing.
type Capabilities struct {
	// Features is the list of declared capability identifiers. Order does
	// not matter; Normalize returns a sorted, deduplicated copy.
	Features []Capability `json:"features,omitempty"`

	// BlendModes lists the blend modes the backend declares support for.
	// Only meaningful when CapBlendModes is in Features.
	BlendModes []BlendMode `json:"blend_modes,omitempty"`

	// MaxStateDepth limits Save nesting depth. Zero means unlimited.
	MaxStateDepth int `json:"max_state_depth,omitempty"`

	// Fallbacks maps a capability that is not natively supported to a stable
	// fallback identifier the backend actually implements
	// (for example "solid_flatten" for a gradient).
	Fallbacks map[Capability]string `json:"fallbacks,omitempty"`

	// Name optionally labels the capability profile in reports.
	Name string `json:"name,omitempty"`
}

// Has reports whether c declares the given capability.
func (c Capabilities) Has(capability Capability) bool {
	for _, f := range c.Features {
		if f == capability {
			return true
		}
	}
	return false
}

// SupportsBlendMode reports whether the given blend mode is listed.
func (c Capabilities) SupportsBlendMode(mode BlendMode) bool {
	if !c.Has(CapBlendModes) {
		return false
	}
	for _, m := range c.BlendModes {
		if m == mode {
			return true
		}
	}
	return false
}

// FallbackFor returns the declared fallback identifier for a capability,
// or the empty string when none is declared.
func (c Capabilities) FallbackFor(capability Capability) string {
	if c.Fallbacks == nil {
		return ""
	}
	return c.Fallbacks[capability]
}

// Normalize returns a sorted, deduplicated version of the profile.
// It never mutates the receiver.
func (c Capabilities) Normalize() Capabilities {
	out := c
	out.Features = sortedUniqueCaps(c.Features)
	if len(c.BlendModes) > 1 {
		modes := append([]BlendMode(nil), c.BlendModes...)
		sort.Slice(modes, func(i, j int) bool { return modes[i] < modes[j] })
		dedup := modes[:0]
		for i, m := range modes {
			if i == 0 || m != modes[i-1] {
				dedup = append(dedup, m)
			}
		}
		out.BlendModes = dedup
	} else if c.BlendModes != nil {
		out.BlendModes = append([]BlendMode(nil), c.BlendModes...)
	}
	if c.Fallbacks != nil {
		out.Fallbacks = make(map[Capability]string, len(c.Fallbacks))
		for k, v := range c.Fallbacks {
			out.Fallbacks[k] = v
		}
	}
	return out
}

func sortedUniqueCaps(in []Capability) []Capability {
	if len(in) == 0 {
		return nil
	}
	cp := append([]Capability(nil), in...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	out := cp[:0]
	for i, v := range cp {
		if i == 0 || v != cp[i-1] {
			out = append(out, v)
		}
	}
	return append([]Capability(nil), out...)
}

// MissingCapabilities returns the sorted unique required capabilities that
// the profile does not declare.
func (c Capabilities) MissingCapabilities(required []Capability) []Capability {
	var missing []Capability
	for _, req := range sortedUniqueCaps(required) {
		if !c.Has(req) {
			missing = append(missing, req)
		}
	}
	return missing
}

// CapableBackend is an optional Backend interface for backends that publish
// a Capabilities declaration.
type CapableBackend interface {
	Backend

	// Capabilities returns the backend's current capability profile.
	// Repeated calls for the same backend instance and configuration must
	// return equal profiles; the gate audits this stability.
	Capabilities() Capabilities
}

// Preflight issue codes.
const (
	// IssueMissingCapability: a command needs an undeclared capability and
	// no fallback is declared.
	IssueMissingCapability = "missing_capability"
	// IssueUnsupportedBlendMode: a SetBlendModeCommand uses a mode absent
	// from Capabilities.BlendModes.
	IssueUnsupportedBlendMode = "unsupported_blend_mode"
	// IssueStateDepthExceeded: Save nesting exceeds MaxStateDepth.
	IssueStateDepthExceeded = "state_depth_exceeded"
	// IssueNonFinite: a command contains NaN or +/-Inf.
	IssueNonFinite = "non_finite_value"
	// IssueInvalidResourceRef: a command references a missing resource.
	IssueInvalidResourceRef = "invalid_resource_ref"
)

// PreflightIssue is a command-locatable preflight problem.
type PreflightIssue struct {
	// Index is the index of the offending command in Recording.Commands.
	Index int `json:"index"`
	// Command is the CommandType.String() of the offending command.
	Command string `json:"command"`
	// Code is one of the Issue* constants.
	Code string `json:"code"`
	// Capability is the capability involved, when applicable.
	Capability Capability `json:"capability,omitempty"`
	// BlendMode is the offending blend mode, when applicable.
	BlendMode BlendMode `json:"blend_mode,omitempty"`
	// Message is a human-readable, locatable explanation.
	Message string `json:"message"`
}

// PreflightReport is the stable result of Preflight.
type PreflightReport struct {
	// OK is true when no hard issue was found.
	OK bool `json:"ok"`
	// Width and Height are the recording dimensions.
	Width int `json:"width"`
	// Height is the recording height.
	Height int `json:"height"`
	// Commands is the number of scanned commands.
	Commands int `json:"commands"`
	// MaxDepth is the maximum Save nesting observed.
	MaxDepth int `json:"max_depth"`
	// Required is the sorted unique capability set the recording needs.
	Required []Capability `json:"required"`
	// Missing is the sorted unique capability set the profile lacks.
	Missing []Capability `json:"missing"`
	// Issues are command-locatable findings in command order.
	Issues []PreflightIssue `json:"issues"`
}

// Preflight scans a Recording and reports which capabilities it requires,
// which of those the profile misses, and command-locatable validity issues.
//
// The returned Missing list and Issues are deterministically ordered, making
// repeated runs directly comparable (the capability gate uses this to detect
// unstable declarations).
func Preflight(r *Recording, caps Capabilities) PreflightReport {
	rep := PreflightReport{OK: true}
	if r == nil {
		rep.OK = false
		rep.Issues = append(rep.Issues, PreflightIssue{
			Index:   -1,
			Command: "<nil>",
			Code:    IssueInvalidResourceRef,
			Message: "recording is nil",
		})
		return rep
	}
	rep.Width = r.width
	rep.Height = r.height
	rep.Commands = len(r.commands)

	required := map[Capability]struct{}{}
	depth := 0

	addIssue := func(i int, cmd Command, code string, cap Capability, msg string) {
		rep.OK = false
		name := "Unknown"
		if cmd != nil {
			name = cmd.Type().String()
		}
		rep.Issues = append(rep.Issues, PreflightIssue{
			Index:      i,
			Command:    name,
			Code:       code,
			Capability: cap,
			Message:    msg,
		})
	}
	requireCap := func(i int, cmd Command, cap Capability) {
		required[cap] = struct{}{}
		if !caps.Has(cap) && caps.FallbackFor(cap) == "" {
			addIssue(i, cmd, IssueMissingCapability, cap,
				fmt.Sprintf("command #%d %s requires %q, which the backend neither declares nor provides a fallback for",
					i, cmd.Type().String(), cap))
		}
	}

	for i, cmd := range r.commands {
		switch c := cmd.(type) {
		case SaveCommand:
			depth++
			if depth > rep.MaxDepth {
				rep.MaxDepth = depth
			}
			requireCap(i, cmd, CapSaveRestore)
			if caps.MaxStateDepth > 0 && depth > caps.MaxStateDepth {
				addIssue(i, cmd, IssueStateDepthExceeded, CapSaveRestore,
					fmt.Sprintf("state nesting depth %d exceeds MaxStateDepth %d at command #%d",
						depth, caps.MaxStateDepth, i))
			}
		case RestoreCommand:
			if depth > 0 {
				depth--
			}
			requireCap(i, cmd, CapSaveRestore)
		case SetTransformCommand:
			requireCap(i, cmd, CapSetTransform)
			if !finiteMatrix(c.Matrix) {
				addIssue(i, cmd, IssueNonFinite, CapSetTransform,
					fmt.Sprintf("transform matrix contains NaN/Inf at command #%d", i))
			}
		case SetClipCommand:
			requireCap(i, cmd, CapClipPath)
			if !c.Path.IsValid() || r.resources == nil || r.resources.GetPath(c.Path) == nil {
				addIssue(i, cmd, IssueInvalidResourceRef, CapClipPath,
					fmt.Sprintf("SetClip references missing PathRef %d at command #%d", uint32(c.Path), i))
			} else if !pathFinite(r.resources.GetPath(c.Path)) {
				addIssue(i, cmd, IssueNonFinite, CapClipPath,
					fmt.Sprintf("clip path contains NaN/Inf coordinates at command #%d", i))
			}
		case ClearClipCommand:
			requireCap(i, cmd, CapClearClip)
		case ClipRoundRectCommand:
			// The recording carries the dedicated rounded-rect clip command;
			// core playback lowers it to SetClip. Require either the semantic
			// cap or its delivery carrier.
			required[CapClipRoundRect] = struct{}{}
			if !caps.Has(CapClipRoundRect) && !caps.Has(CapClipPath) && caps.FallbackFor(CapClipRoundRect) == "" {
				addIssue(i, cmd, IssueMissingCapability, CapClipRoundRect,
					fmt.Sprintf("command #%d %s requires %q or %q", i, cmd.Type().String(), CapClipRoundRect, CapClipPath))
			}
			if !finiteFloats(c.X, c.Y, c.W, c.H, c.Radius) {
				addIssue(i, cmd, IssueNonFinite, CapClipRoundRect,
					fmt.Sprintf("rounded-rect clip contains NaN/Inf at command #%d", i))
			}
		case FillPathCommand:
			requireCap(i, cmd, CapFillPath)
			if !c.Path.IsValid() || r.resources == nil || r.resources.GetPath(c.Path) == nil {
				addIssue(i, cmd, IssueInvalidResourceRef, CapFillPath,
					fmt.Sprintf("FillPath references missing PathRef %d at command #%d", uint32(c.Path), i))
			} else if !pathFinite(r.resources.GetPath(c.Path)) {
				addIssue(i, cmd, IssueNonFinite, CapFillPath,
					fmt.Sprintf("fill path contains NaN/Inf coordinates at command #%d", i))
			}
			if cap, ok := gradientBrushCap(r.resources, c.Brush); ok {
				requireCap(i, cmd, cap)
			}
		case StrokePathCommand:
			requireCap(i, cmd, CapStrokePath)
			if !c.Path.IsValid() || r.resources == nil || r.resources.GetPath(c.Path) == nil {
				addIssue(i, cmd, IssueInvalidResourceRef, CapStrokePath,
					fmt.Sprintf("StrokePath references missing PathRef %d at command #%d", uint32(c.Path), i))
			} else if !pathFinite(r.resources.GetPath(c.Path)) {
				addIssue(i, cmd, IssueNonFinite, CapStrokePath,
					fmt.Sprintf("stroke path contains NaN/Inf coordinates at command #%d", i))
			}
			if !strokeFinite(c.Stroke) {
				addIssue(i, cmd, IssueNonFinite, CapStrokePath,
					fmt.Sprintf("stroke style contains NaN/Inf at command #%d", i))
			}
			if cap, ok := gradientBrushCap(r.resources, c.Brush); ok {
				requireCap(i, cmd, cap)
			}
		case FillRectCommand:
			requireCap(i, cmd, CapFillRect)
			if !rectFinite(c.Rect) {
				addIssue(i, cmd, IssueNonFinite, CapFillRect,
					fmt.Sprintf("fill rectangle contains NaN/Inf at command #%d", i))
			}
		case StrokeRectCommand:
			// Core playback lowers this to StrokePath.
			required[CapStrokePath] = struct{}{}
			if !caps.Has(CapStrokePath) && caps.FallbackFor(CapStrokePath) == "" {
				addIssue(i, cmd, IssueMissingCapability, CapStrokePath,
					fmt.Sprintf("command #%d StrokeRect requires delivery carrier %q", i, CapStrokePath))
			}
			if !rectFinite(c.Rect) || !strokeFinite(c.Stroke) {
				addIssue(i, cmd, IssueNonFinite, CapStrokePath,
					fmt.Sprintf("stroke rectangle/style contains NaN/Inf at command #%d", i))
			}
		case DrawImageCommand:
			requireCap(i, cmd, CapDrawImage)
			if !c.Image.IsValid() || r.resources == nil || r.resources.GetImage(c.Image) == nil {
				addIssue(i, cmd, IssueInvalidResourceRef, CapDrawImage,
					fmt.Sprintf("DrawImage references missing ImageRef %d at command #%d", uint32(c.Image), i))
			}
			if !rectFinite(c.SrcRect) || !rectFinite(c.DstRect) || !finiteFloats(c.Options.Alpha) {
				addIssue(i, cmd, IssueNonFinite, CapDrawImage,
					fmt.Sprintf("DrawImage rect/options contain NaN/Inf at command #%d", i))
			}
		case DrawTextCommand:
			requireCap(i, cmd, CapDrawText)
			if !finiteFloats(c.X, c.Y, c.FontSize) {
				addIssue(i, cmd, IssueNonFinite, CapDrawText,
					fmt.Sprintf("DrawText position/size contains NaN/Inf at command #%d", i))
			}
		case StrokeTextCommand:
			required[CapStrokeText] = struct{}{}
			// Core playback lowers to DrawText, so the carrier suffices.
			if !caps.Has(CapStrokeText) && !caps.Has(CapDrawText) && caps.FallbackFor(CapStrokeText) == "" {
				addIssue(i, cmd, IssueMissingCapability, CapStrokeText,
					fmt.Sprintf("command #%d StrokeText requires %q or lowering via %q", i, CapStrokeText, CapDrawText))
			}
			if !finiteFloats(c.X, c.Y, c.FontSize) || !strokeFinite(c.Stroke) {
				addIssue(i, cmd, IssueNonFinite, CapStrokeText,
					fmt.Sprintf("StrokeText position/style contains NaN/Inf at command #%d", i))
			}
		case SetBlendModeCommand:
			required[CapBlendModes] = struct{}{}
			if !caps.SupportsBlendMode(c.Mode) {
				rep.OK = false
				rep.Missing = append(rep.Missing, Capability(fmt.Sprintf("blend_mode:%d", int(c.Mode))))
				rep.Issues = append(rep.Issues, PreflightIssue{
					Index:      i,
					Command:    cmd.Type().String(),
					Code:       IssueUnsupportedBlendMode,
					Capability: CapBlendModes,
					BlendMode:  c.Mode,
					Message: fmt.Sprintf("command #%d SetBlendMode(%d) is not in declared BlendModes",
						i, int(c.Mode)),
				})
			}
		case SetLineWidthCommand:
			if math.IsNaN(c.Width) || math.IsInf(c.Width, 0) {
				addIssue(i, cmd, IssueNonFinite, "",
					fmt.Sprintf("line width is NaN/Inf at command #%d", i))
			}
		case SetMiterLimitCommand:
			if math.IsNaN(c.Limit) || math.IsInf(c.Limit, 0) {
				addIssue(i, cmd, IssueNonFinite, "",
					fmt.Sprintf("miter limit is NaN/Inf at command #%d", i))
			}
		case SetDashCommand:
			vals := append([]float64{c.Offset}, c.Pattern...)
			if !finiteFloats(vals...) {
				addIssue(i, cmd, IssueNonFinite, "",
					fmt.Sprintf("dash pattern/offset contains NaN/Inf at command #%d", i))
			}
		}
	}

	reqCaps := make([]Capability, 0, len(required))
	for c := range required {
		reqCaps = append(reqCaps, c)
	}
	rep.Required = sortedUniqueCaps(reqCaps)

	// Compute Missing from Required against the profile. Blend-mode dynamic
	// codes added above are preserved and sorted together with feature caps.
	missingSet := map[Capability]struct{}{}
	for _, c := range rep.Required {
		if !caps.Has(c) && caps.FallbackFor(c) == "" {
			missingSet[c] = struct{}{}
		}
	}
	for _, m := range rep.Missing { // dynamic blend_mode:* codes
		missingSet[m] = struct{}{}
	}
	missing := make([]Capability, 0, len(missingSet))
	for m := range missingSet {
		missing = append(missing, m)
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
	rep.Missing = missing

	return rep
}

func gradientBrushCap(resources *ResourcePool, ref BrushRef) (Capability, bool) {
	if resources == nil || !ref.IsValid() {
		return "", false
	}
	switch resources.GetBrush(ref).(type) {
	case *LinearGradientBrush:
		return CapGradientLinear, true
	case *RadialGradientBrush:
		return CapGradientRadial, true
	case *SweepGradientBrush:
		return CapGradientSweep, true
	default:
		return "", false
	}
}

func finiteFloats(vals ...float64) bool {
	for _, v := range vals {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

func finiteMatrix(m Matrix) bool {
	return finiteFloats(m.A, m.B, m.C, m.D, m.E, m.F)
}

func rectFinite(r Rect) bool {
	return finiteFloats(r.MinX, r.MinY, r.MaxX, r.MaxY)
}

func strokeFinite(s Stroke) bool {
	if !finiteFloats(s.Width, s.MiterLimit, s.DashOffset) {
		return false
	}
	return finiteFloats(s.DashPattern...)
}

// pathFinite inspects path verbs/coordinates for non-finite values.
func pathFinite(p interface {
	Coords() []float64
}) bool {
	if p == nil {
		return true
	}
	return finiteFloats(p.Coords()...)
}

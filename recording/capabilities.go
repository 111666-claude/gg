package recording

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// Capability identifies one command family that a backend can execute.
//
// Capabilities form a bitmask. A backend declares the capabilities it can
// execute natively through [CapabilityBackend]. Backends that do not implement
// [CapabilityBackend] are treated as supporting everything, which preserves the
// original behavior of the recording package.
//
// State and transform commands (Save, Restore, SetTransform) are mandatory for
// every backend and therefore have no capability bit. Style commands have
// individual bits even though playback folds the recorded style into each
// drawing command; a backend that does not consume style commands can skip them
// or declare a no-op fallback for them.
type Capability uint32

// CapNone means "no capability". It is the zero value and is also used as the
// target of a degradation that intentionally drops a command.
const CapNone Capability = 0

const (
	// CapFillPath is the ability to fill an arbitrary path.
	CapFillPath Capability = 1 << iota
	// CapStrokePath is the ability to stroke an arbitrary path.
	CapStrokePath
	// CapFillRect is the ability to fill an axis-aligned rectangle.
	CapFillRect
	// CapStrokeRect is the ability to stroke an axis-aligned rectangle.
	CapStrokeRect
	// CapDrawImage is the ability to draw an image.
	CapDrawImage
	// CapDrawText is the ability to draw filled text.
	CapDrawText
	// CapStrokeText is the ability to render stroked text. The Backend
	// interface has no stroke-text method, so a backend that declares it
	// accepts DrawText for a StrokeText command.
	CapStrokeText

	// CapClipPath is the ability to clip to an arbitrary path.
	CapClipPath
	// CapClipRoundRect is the ability to clip to a rounded rectangle. It maps
	// to the same Backend method as CapClipPath.
	CapClipRoundRect
	// CapClearClip is the ability to remove the current clip.
	CapClearClip

	// CapStyleFill is the ability to consume SetFillStyle commands.
	CapStyleFill
	// CapStyleStroke is the ability to consume SetStrokeStyle commands.
	CapStyleStroke
	// CapStyleLineWidth is the ability to consume SetLineWidth commands.
	CapStyleLineWidth
	// CapStyleLineCap is the ability to consume SetLineCap commands.
	CapStyleLineCap
	// CapStyleLineJoin is the ability to consume SetLineJoin commands.
	CapStyleLineJoin
	// CapStyleMiterLimit is the ability to consume SetMiterLimit commands.
	CapStyleMiterLimit
	// CapStyleDash is the ability to consume SetDash commands.
	CapStyleDash
	// CapStyleFillRule is the ability to consume SetFillRule commands.
	CapStyleFillRule
	// CapStyleAntiAlias is the ability to consume SetAntiAlias commands.
	CapStyleAntiAlias
)

const (
	// CapDrawing groups all drawing commands.
	CapDrawing = CapFillPath | CapStrokePath | CapFillRect | CapStrokeRect |
		CapDrawImage | CapDrawText | CapStrokeText
	// CapClips groups all clipping commands.
	CapClips = CapClipPath | CapClipRoundRect | CapClearClip
	// CapStyles groups all style commands.
	CapStyles = CapStyleFill | CapStyleStroke | CapStyleLineWidth |
		CapStyleLineCap | CapStyleLineJoin | CapStyleMiterLimit |
		CapStyleDash | CapStyleFillRule | CapStyleAntiAlias
	// CapAll is every capability defined by this package.
	CapAll = CapDrawing | CapClips | CapStyles
)

// capabilityNames lists capabilities in declaration order. The order is used
// for deterministic reports and error messages.
var capabilityNames = [...]struct {
	cap  Capability
	name string
}{
	{CapFillPath, "FillPath"},
	{CapStrokePath, "StrokePath"},
	{CapFillRect, "FillRect"},
	{CapStrokeRect, "StrokeRect"},
	{CapDrawImage, "DrawImage"},
	{CapDrawText, "DrawText"},
	{CapStrokeText, "StrokeText"},
	{CapClipPath, "ClipPath"},
	{CapClipRoundRect, "ClipRoundRect"},
	{CapClearClip, "ClearClip"},
	{CapStyleFill, "SetFillStyle"},
	{CapStyleStroke, "SetStrokeStyle"},
	{CapStyleLineWidth, "SetLineWidth"},
	{CapStyleLineCap, "SetLineCap"},
	{CapStyleLineJoin, "SetLineJoin"},
	{CapStyleMiterLimit, "SetMiterLimit"},
	{CapStyleDash, "SetDash"},
	{CapStyleFillRule, "SetFillRule"},
	{CapStyleAntiAlias, "SetAntiAlias"},
}

// orderedCapabilities returns every single-bit capability in declaration order.
func orderedCapabilities() []Capability {
	caps := make([]Capability, len(capabilityNames))
	for i := range capabilityNames {
		caps[i] = capabilityNames[i].cap
	}
	return caps
}

// Contains reports whether c includes every bit in other.
func (c Capability) Contains(other Capability) bool {
	return other&^c == 0
}

// IsSingle reports whether c is exactly one known capability bit.
func (c Capability) IsSingle() bool {
	return c != CapNone && c&(c-1) == 0 && c&^CapAll == 0
}

// IsStyle reports whether c is a single style capability bit.
func (c Capability) IsStyle() bool {
	return c.IsSingle() && c&^CapStyles == 0
}

// String returns a human-readable list of the capability bits in c.
func (c Capability) String() string {
	if c == CapNone {
		return "None"
	}
	parts := make([]string, 0, len(capabilityNames))
	remaining := c
	for _, entry := range capabilityNames {
		if remaining&entry.cap != 0 {
			parts = append(parts, entry.name)
			remaining &^= entry.cap
		}
	}
	if remaining != 0 {
		parts = append(parts, fmt.Sprintf("Unknown(0x%x)", uint32(remaining)))
	}
	return strings.Join(parts, "|")
}

// CapabilitySet is a backend's capability declaration.
type CapabilitySet struct {
	// Native lists the commands the backend executes itself.
	//
	// Two capabilities map to the same Backend method and differ in the path
	// complexity they describe:
	//
	//	CapClipPath       SetClip accepts an arbitrary path
	//	CapClipRoundRect  SetClip accepts a rounded-rectangle path
	//	CapDrawText       DrawText renders filled text
	//	CapStrokeText     DrawText renders the text it is given for a stroke request
	//
	// A backend that accepts only rounded-rectangle clips declares
	// CapClipRoundRect and not CapClipPath.
	Native Capability

	// Fallbacks overrides this package's built-in equivalent commands.
	//
	// Built-in fallbacks are exact rewrites, not approximations:
	//
	//	FillRect      -> FillPath
	//	StrokeRect    -> StrokePath
	//	ClipRoundRect -> ClipPath
	//	style commands -> dropped (their effect travels with the drawing command)
	//
	// A backend declaration for the same source capability replaces the
	// built-in entry. Target CapNone drops the command and is only valid for
	// style commands. Chains are resolved transitively; a cycle, a target that
	// is not an equivalent command, or a fallback declared for a natively
	// supported capability is reported as [ErrCapabilityConflict].
	Fallbacks map[Capability]Capability
}

// CapabilityBackend is implemented by backends that declare what they support.
//
// A Backend that does not implement this interface is treated as supporting
// every capability (the pre-capability behavior). Implement it to let
// [Recording.Preflight] report gaps and to enable [PolicySkip] and
// [PolicyDegrade].
type CapabilityBackend interface {
	Backend

	// BackendCapabilities reports the commands this backend executes natively
	// and the equivalent commands it accepts as degradation targets.
	BackendCapabilities() CapabilitySet
}

// builtinFallbacks are the equivalent-command substitutions available to every
// backend that declares capabilities.
var builtinFallbacks = map[Capability]Capability{
	CapFillRect:        CapFillPath,
	CapStrokeRect:      CapStrokePath,
	CapClipRoundRect:   CapClipPath,
	CapStyleFill:       CapNone,
	CapStyleStroke:     CapNone,
	CapStyleLineWidth:  CapNone,
	CapStyleLineCap:    CapNone,
	CapStyleLineJoin:   CapNone,
	CapStyleMiterLimit: CapNone,
	CapStyleDash:       CapNone,
	CapStyleFillRule:   CapNone,
	CapStyleAntiAlias:  CapNone,
}

// fallbackFor returns the effective fallback for capability. Backend declarations
// override built-in substitutions.
func (s CapabilitySet) fallbackFor(capability Capability) (Capability, bool) {
	if target, ok := s.Fallbacks[capability]; ok {
		return target, true
	}
	target, ok := builtinFallbacks[capability]
	return target, ok
}

// equivalent reports whether replacing a command for `from` with one for
// `target` preserves the output. Style commands are equivalent only to CapNone,
// because their effect is already folded into the drawing command.
func equivalent(from, target Capability) bool {
	if target == CapNone {
		return from.IsStyle()
	}
	switch from {
	case CapFillRect:
		return target == CapFillPath
	case CapStrokeRect:
		return target == CapStrokePath
	case CapClipRoundRect:
		return target == CapClipPath
	}
	return false
}

// resolveDegradation follows the fallback chain for capability until it
// reaches a capability the backend natively supports, or CapNone.
//
// It returns ok=false when no equivalent command is available. Cycles,
// non-equivalent targets, and fallbacks declared for native capabilities are
// reported as errors.
func resolveDegradation(capability, native Capability, set CapabilitySet) (target Capability, ok bool, err error) {
	seen := make(map[Capability]bool, 4)
	cur := capability
	for hop := 0; hop <= len(capabilityNames); hop++ {
		next, has := set.fallbackFor(cur)
		if !has {
			return CapNone, false, nil
		}
		if next == cur || seen[next] {
			return CapNone, false, fmt.Errorf("%w: fallback chain for %s revisits %s", ErrFallbackRecursion, capability, next)
		}
		if next == CapNone {
			if !capability.IsStyle() {
				return CapNone, false, fmt.Errorf("%w: %s cannot degrade to no-op", ErrCapabilityConflict, capability)
			}
			return CapNone, true, nil
		}
		if !equivalent(capability, next) {
			return CapNone, false, fmt.Errorf("%w: fallback for %s resolves to %s, which is not an equivalent command",
				ErrCapabilityConflict, capability, next)
		}
		if native.Contains(next) {
			return next, true, nil
		}
		seen[next] = true
		cur = next
	}
	return CapNone, false, fmt.Errorf("%w: fallback chain for %s is too long", ErrFallbackRecursion, capability)
}

// validateCapabilitySet reports contradictory declarations before any command
// is replayed. Fallback cycles are reported first, as [ErrFallbackRecursion],
// because they make resolution impossible regardless of the target.
func validateCapabilitySet(set CapabilitySet) error {
	if unknown := set.Native &^ CapAll; unknown != 0 {
		return fmt.Errorf("%w: unsupported native capability bits 0x%x", ErrCapabilityConflict, uint32(unknown))
	}
	for from, target := range set.Fallbacks {
		if !from.IsSingle() {
			return fmt.Errorf("%w: fallback source %s is not a single capability", ErrCapabilityConflict, from)
		}
		if from&^CapAll != 0 {
			return fmt.Errorf("%w: fallback source 0x%x is unknown", ErrCapabilityConflict, uint32(from))
		}
		if target&^CapAll != 0 {
			return fmt.Errorf("%w: fallback target 0x%x for %s is unknown", ErrCapabilityConflict, uint32(target), from)
		}
	}
	if err := detectFallbackCycle(set); err != nil {
		return err
	}
	for from, target := range set.Fallbacks {
		if set.Native.Contains(from) {
			return fmt.Errorf("%w: %s is declared native but also has a fallback to %s",
				ErrCapabilityConflict, from, target)
		}
		if !equivalent(from, target) {
			return fmt.Errorf("%w: %s cannot degrade to %s", ErrCapabilityConflict, from, target)
		}
	}
	return nil
}

// detectFallbackCycle walks the declared fallback graph and reports the first
// cycle. Only declarations are considered; built-in substitutions are acyclic.
func detectFallbackCycle(set CapabilitySet) error {
	const (
		unvisited = iota
		inProgress
		done
	)
	state := make(map[Capability]int, len(set.Fallbacks))
	var walk func(capability Capability) error
	walk = func(capability Capability) error {
		switch state[capability] {
		case unvisited:
		case done:
			return nil
		case inProgress:
			return fmt.Errorf("%w: fallback chain revisits %s", ErrFallbackRecursion, capability)
		}
		state[capability] = inProgress
		if next, ok := set.Fallbacks[capability]; ok && next != CapNone {
			if err := walk(next); err != nil {
				return err
			}
		}
		state[capability] = done
		return nil
	}
	for from := range set.Fallbacks {
		if err := walk(from); err != nil {
			return err
		}
	}
	return nil
}

// commandCapabilityTable maps a concrete command type to the capability needed
// to execute it. Commands absent from the table require no capability.
var commandCapabilityTable = map[reflect.Type]Capability{
	reflect.TypeOf(ClearClipCommand{}):      CapClearClip,
	reflect.TypeOf(SetClipCommand{}):        CapClipPath,
	reflect.TypeOf(ClipRoundRectCommand{}):  CapClipRoundRect,
	reflect.TypeOf(FillPathCommand{}):       CapFillPath,
	reflect.TypeOf(StrokePathCommand{}):     CapStrokePath,
	reflect.TypeOf(FillRectCommand{}):       CapFillRect,
	reflect.TypeOf(StrokeRectCommand{}):     CapStrokeRect,
	reflect.TypeOf(DrawImageCommand{}):      CapDrawImage,
	reflect.TypeOf(DrawTextCommand{}):       CapDrawText,
	reflect.TypeOf(StrokeTextCommand{}):     CapStrokeText,
	reflect.TypeOf(SetFillStyleCommand{}):   CapStyleFill,
	reflect.TypeOf(SetStrokeStyleCommand{}): CapStyleStroke,
	reflect.TypeOf(SetLineWidthCommand{}):   CapStyleLineWidth,
	reflect.TypeOf(SetLineCapCommand{}):     CapStyleLineCap,
	reflect.TypeOf(SetLineJoinCommand{}):    CapStyleLineJoin,
	reflect.TypeOf(SetMiterLimitCommand{}):  CapStyleMiterLimit,
	reflect.TypeOf(SetDashCommand{}):        CapStyleDash,
	reflect.TypeOf(SetFillRuleCommand{}):    CapStyleFillRule,
	reflect.TypeOf(SetAntiAliasCommand{}):   CapStyleAntiAlias,
}

// commandCapability maps a command to the capability required to execute it.
// State and transform commands require no capability. ok is false for command
// types this package does not know how to play back.
func commandCapability(cmd Command) (capability Capability, ok bool) {
	switch cmd.(type) {
	case SaveCommand, RestoreCommand, SetTransformCommand:
		return CapNone, true
	}
	if capability, known := commandCapabilityTable[reflect.TypeOf(cmd)]; known {
		return capability, true
	}
	return CapNone, false
}

// CapabilityIssue describes one capability the backend is missing, the command
// indexes that need it, and whether an equivalent command is available.
type CapabilityIssue struct {
	// Capability is the missing capability.
	Capability Capability
	// Commands lists the zero-based indexes of commands that need it.
	Commands []int
	// Fallback is the equivalent capability used by [PolicyDegrade], or
	// CapNone when the command is dropped or cannot be degraded.
	Fallback Capability
	// Degradable reports whether [PolicyDegrade] can play every command that
	// needs this capability.
	Degradable bool
}

// CapabilityReport is the result of checking a recording against a backend
// before any output is produced.
type CapabilityReport struct {
	// Declared reports whether the backend implements [CapabilityBackend].
	// When false, Native is CapAll and Missing is empty.
	Declared bool
	// Required is the union of capabilities needed by the recording.
	Required Capability
	// Native is the capability set the backend declared (CapAll when the
	// backend does not declare capabilities).
	Native Capability
	// Missing lists undeclared capabilities in declaration order.
	Missing []Capability
	// Issues carries one entry per missing capability, in the same order.
	Issues []CapabilityIssue
}

// IsComplete reports whether the backend can execute every command natively.
// A nil report is complete.
func (r *CapabilityReport) IsComplete() bool {
	return r == nil || len(r.Missing) == 0
}

// String returns a human-readable summary of the report.
func (r *CapabilityReport) String() string {
	if r == nil {
		return "capability report: <nil>"
	}
	if !r.Declared {
		return "capability report: backend declares no capabilities (assumed complete)"
	}
	if len(r.Missing) == 0 {
		return "capability report: complete"
	}
	var b strings.Builder
	b.WriteString("capability report: missing ")
	for i, issue := range r.Issues {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(issue.Capability.String())
		b.WriteString(" (commands ")
		for j, idx := range issue.Commands {
			if j > 0 {
				b.WriteString(",")
			}
			b.WriteString(strconv.Itoa(idx))
		}
		b.WriteString(")")
		switch {
		case !issue.Degradable:
		case issue.Fallback == CapNone:
			b.WriteString(" -> drop")
		default:
			b.WriteString(" -> ")
			b.WriteString(issue.Fallback.String())
		}
	}
	return b.String()
}

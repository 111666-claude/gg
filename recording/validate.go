package recording

import (
	"fmt"
	"math"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/text"
)

// ResourceKind identifies the resource pool a validation error refers to.
type ResourceKind uint8

const (
	// ResourceCommand is used for problems that are not tied to a resource
	// pool (command order, malformed numeric parameters, unknown tags).
	ResourceCommand ResourceKind = iota
	// ResourcePath refers to the path pool (ResourcePool.GetPath).
	ResourcePath
	// ResourceBrush refers to the brush pool (ResourcePool.GetBrush).
	ResourceBrush
	// ResourceImage refers to the image pool (ResourcePool.GetImage).
	ResourceImage
	// ResourceFont refers to the font pool (ResourcePool.GetFont) and to
	// font faces embedded directly in text commands.
	ResourceFont
)

// String returns the lowercase resource category name used in error messages.
func (k ResourceKind) String() string {
	switch k {
	case ResourcePath:
		return "path"
	case ResourceBrush:
		return "brush"
	case ResourceImage:
		return "image"
	case ResourceFont:
		return "font"
	default:
		return "command"
	}
}

// ValidationError describes a single defect found in a Recording.
//
// CommandIndex is the zero-based position of the offending command, or -1 when
// the problem is not attached to a specific command (e.g. canvas dimensions or
// a missing resource pool). Kind identifies the affected resource category and
// ResourceIndex its index within that pool (-1 when not applicable). Reason is
// a human-readable explanation.
type ValidationError struct {
	// CommandIndex is the zero-based command index, or -1 for recording-level
	// problems.
	CommandIndex int
	// Kind is the resource category of the failing reference.
	Kind ResourceKind
	// ResourceIndex is the index within the resource pool, or -1 when the
	// problem is not a pool reference.
	ResourceIndex int
	// Reason is a human-readable description of the defect.
	Reason string

	// tag is the CommandType of the offending command, used to render the
	// command name. Zero value renders as "command"; recording-level errors
	// leave it unset.
	tag CommandType
}

// Error implements the error interface.
func (e ValidationError) Error() string {
	name := e.tag.String()
	if e.CommandIndex < 0 {
		return "recording: " + e.Reason
	}
	if e.Kind == ResourceCommand {
		return fmt.Sprintf("recording: command[%d] %s: %s",
			e.CommandIndex, name, e.Reason)
	}
	return fmt.Sprintf("recording: command[%d] %s: %s ref %d: %s",
		e.CommandIndex, name, e.Kind, e.ResourceIndex, e.Reason)
}

// CommandType returns the type tag of the offending command. Recording-level
// problems (CommandIndex == -1) return an out-of-range tag whose String()
// reports "Unknown".
func (e ValidationError) CommandType() CommandType { return e.tag }

// ValidationErrors is the set of defects returned in collect-all mode.
type ValidationErrors []ValidationError

// Error implements the error interface, joining every defect on its own line.
func (errs ValidationErrors) Error() string {
	if len(errs) == 0 {
		return "recording: validation failed"
	}
	if len(errs) == 1 {
		return errs[0].Error()
	}
	msg := fmt.Sprintf("recording: %d validation errors:\n", len(errs))
	for i := range errs {
		msg += "\t- " + errs[i].Error() + "\n"
	}
	return msg
}

// ValidateOption configures ValidateRecording.
type ValidateOption func(*validateConfig)

type validateConfig struct {
	failFast bool
	maxErrs  int
}

// FailFast configures ValidateRecording to stop at the first defect instead of
// collecting every problem. The returned error is always a ValidationError in
// that mode.
func FailFast() ValidateOption {
	return func(c *validateConfig) { c.failFast = true }
}

// MaxValidationErrors caps the number of defects returned in collect-all mode.
// A non-positive value means no cap. The cap keeps validation of adversarial
// recordings (millions of bad commands) allocation-bounded. When the cap is
// reached, a trailing ValidationError records that further defects were
// discarded.
func MaxValidationErrors(n int) ValidateOption {
	return func(c *validateConfig) { c.maxErrs = n }
}

// defaultMaxValidationErrors bounds error accumulation for pathological inputs.
const defaultMaxValidationErrors = 1024

// ValidateRecording inspects a Recording for structural defects that can turn
// into nil dereferences or arithmetic faults inside a playback Backend.
//
// It is a pure read-only check: the recording, its command slice and its
// resource pools are never modified. It verifies:
//
//   - command order, including balanced Save/Restore state nesting
//   - ClipRoundRect parameters (finite, positive size, in-range radius)
//   - Path/Brush/Image/Font references (known tag, in-range index, non-nil
//     pooled resource)
//   - finiteness (no NaN/Inf) of every numeric parameter, including matrices,
//     rectangles, stroke metrics, dash patterns and brush geometry
//   - validity of enum tags (FillRule, LineCap, LineJoin, InterpolationMode)
//
// By default every defect is collected and returned as ValidationErrors.
// FailFast changes this to stop at the first defect. A valid recording,
// including an empty one, returns nil.
func ValidateRecording(r *Recording, opts ...ValidateOption) error {
	cfg := validateConfig{maxErrs: defaultMaxValidationErrors}
	for _, opt := range opts {
		opt(&cfg)
	}

	v := &validator{cfg: cfg}
	v.check(r)
	if len(v.errs) == 0 {
		return nil
	}
	if cfg.failFast {
		return v.errs[0]
	}
	return v.errs
}

// Validate is a convenience wrapper around [ValidateRecording].
func (r *Recording) Validate(opts ...ValidateOption) error {
	return ValidateRecording(r, opts...)
}

type validator struct {
	cfg  validateConfig
	errs ValidationErrors
	// saveStack holds command indices of unmatched Save commands so that an
	// unclosed save can be reported at its own command index.
	saveStack []int
}

func (v *validator) check(r *Recording) {
	if r == nil {
		v.report(-1, CommandType(255), ValidationError{
			Kind:   ResourceCommand,
			Reason: "nil recording",
		})
		return
	}

	if r.width <= 0 || r.height <= 0 {
		v.report(-1, CommandType(255), ValidationError{
			Kind: ResourceCommand,
			Reason: fmt.Sprintf("canvas dimensions must be positive, got %dx%d",
				r.width, r.height),
		})
	}

	if r.resources == nil {
		v.report(-1, CommandType(255), ValidationError{
			Kind:   ResourceCommand,
			Reason: "missing resource pool",
		})
		// Every reference check below would be meaningless; still walk the
		// commands to catch ordering and numeric defects.
	}

	pool := r.resources
	saveDepth := 0
	for i, cmd := range r.commands {
		// Saturation guard: once collect-all mode has hit its cap, the only
		// remaining work would be discarded. Skip straight to the end so the
		// cost of a pathological recording stays O(commands) comparisons with
		// no per-command allocation.
		if !v.cfg.failFast && v.cfg.maxErrs > 0 && len(v.errs) > v.cfg.maxErrs {
			break
		}
		if cmd == nil {
			v.report(i, CommandType(255), ValidationError{
				Kind:   ResourceCommand,
				Reason: "nil command",
			})
			continue
		}

		tag := cmd.Type()
		if int(tag) >= len(commandTypeNames) {
			v.report(i, tag, ValidationError{
				Kind:   ResourceCommand,
				Reason: fmt.Sprintf("unknown command tag %d", uint8(tag)),
			})
			continue
		}

		switch c := cmd.(type) {
		case SaveCommand:
			saveDepth++
			v.saveStack = append(v.saveStack, i)

		case RestoreCommand:
			saveDepth--
			if saveDepth < 0 {
				v.report(i, tag, ValidationError{
					Kind:   ResourceCommand,
					Reason: "Restore without a matching Save (state stack underflow)",
				})
				saveDepth = 0
			} else {
				v.saveStack = v.saveStack[:len(v.saveStack)-1]
			}

		case SetTransformCommand:
			v.checkFiniteMatrix(i, tag, c.Matrix, "transform matrix")

		case SetClipCommand:
			v.checkFillRule(i, tag, c.Rule)
			v.checkPathRef(i, tag, pool, c.Path)

		case ClipRoundRectCommand:
			v.checkClipRoundRect(i, tag, c)

		case FillPathCommand:
			v.checkFillRule(i, tag, c.Rule)
			v.checkPathRef(i, tag, pool, c.Path)
			v.checkBrushRef(i, tag, pool, c.Brush)

		case StrokePathCommand:
			v.checkPathRef(i, tag, pool, c.Path)
			v.checkBrushRef(i, tag, pool, c.Brush)
			v.checkStroke(i, tag, c.Stroke)

		case FillRectCommand:
			v.checkRect(i, tag, c.Rect, "rect")
			v.checkBrushRef(i, tag, pool, c.Brush)

		case StrokeRectCommand:
			v.checkRect(i, tag, c.Rect, "rect")
			v.checkBrushRef(i, tag, pool, c.Brush)
			v.checkStroke(i, tag, c.Stroke)

		case DrawImageCommand:
			v.checkImageRef(i, tag, pool, c.Image)
			v.checkRect(i, tag, c.SrcRect, "source rect")
			v.checkRect(i, tag, c.DstRect, "destination rect")
			v.checkImageOptions(i, tag, c.Options)

		case DrawTextCommand:
			v.checkBrushRef(i, tag, pool, c.Brush)
			v.checkTextCommand(i, tag, c.Text, c.X, c.Y, c.FontSize, c.Face)

		case StrokeTextCommand:
			v.checkBrushRef(i, tag, pool, c.Brush)
			v.checkTextCommand(i, tag, c.Text, c.X, c.Y, c.FontSize, c.Face)
			v.checkStroke(i, tag, c.Stroke)

		case SetFillStyleCommand:
			v.checkBrushRef(i, tag, pool, c.Brush)

		case SetStrokeStyleCommand:
			v.checkBrushRef(i, tag, pool, c.Brush)

		case SetLineWidthCommand:
			if !isFinite(c.Width) {
				v.finiteErr(i, tag, "line width", c.Width)
			}

		case SetLineCapCommand:
			if c.Cap > LineCapSquare {
				v.report(i, tag, ValidationError{
					Kind:   ResourceCommand,
					Reason: fmt.Sprintf("invalid line cap tag %d", uint8(c.Cap)),
				})
			}

		case SetLineJoinCommand:
			if c.Join > LineJoinBevel {
				v.report(i, tag, ValidationError{
					Kind:   ResourceCommand,
					Reason: fmt.Sprintf("invalid line join tag %d", uint8(c.Join)),
				})
			}

		case SetMiterLimitCommand:
			if !isFinite(c.Limit) {
				v.finiteErr(i, tag, "miter limit", c.Limit)
			}

		case SetDashCommand:
			if !isFinite(c.Offset) {
				v.finiteErr(i, tag, "dash offset", c.Offset)
			}
			for j, d := range c.Pattern {
				if !isFinite(d) {
					v.finiteErr(i, tag, fmt.Sprintf("dash pattern[%d]", j), d)
				}
			}

		case SetFillRuleCommand:
			v.checkFillRule(i, tag, c.Rule)

		case SetAntiAliasCommand, ClearClipCommand:
			// Tag-only commands; nothing beyond the type check above.
		}

		if v.cfg.failFast && len(v.errs) > 0 {
			return
		}
	}

	if saveDepth > 0 {
		// Report every unmatched Save, deepest first, each at its own index.
		for j := len(v.saveStack) - 1; j >= 0; j-- {
			v.report(v.saveStack[j], CmdSave, ValidationError{
				Kind:   ResourceCommand,
				Reason: "Save without a matching Restore (unclosed state)",
			})
		}
	}
}

// report appends a defect. The caller fills Kind/Reason; index and tag are
// supplied here so every error carries its command location. Command-level
// problems default ResourceIndex to -1; resource problems pass the pool index
// explicitly through badRef or the ValidationError literal.
func (v *validator) report(i int, tag CommandType, e ValidationError) {
	e.CommandIndex = i
	e.tag = tag
	if e.Kind == ResourceCommand {
		e.ResourceIndex = -1
	}

	if v.cfg.failFast {
		if len(v.errs) == 0 {
			v.errs = append(v.errs, e)
		}
		return
	}
	// Once the cap (plus the single truncation notice) is reached, stop
	// constructing errors entirely. Without this guard an adversarial
	// recording with millions of bad commands would spend one allocation per
	// discarded defect; the validator must stay allocation-bounded.
	if v.cfg.maxErrs > 0 && len(v.errs) > v.cfg.maxErrs {
		return
	}
	if v.cfg.maxErrs > 0 && len(v.errs) == v.cfg.maxErrs {
		v.errs = append(v.errs, ValidationError{
			CommandIndex: -1,
			tag:          CommandType(255),
			Kind:         ResourceCommand,
			Reason: fmt.Sprintf("error limit %d reached; further defects discarded",
				v.cfg.maxErrs),
		})
		return
	}
	v.errs = append(v.errs, e)
}

func (v *validator) checkClipRoundRect(i int, tag CommandType, c ClipRoundRectCommand) {
	if !isFinite(c.X) || !isFinite(c.Y) {
		v.report(i, tag, ValidationError{
			Kind:   ResourceCommand,
			Reason: fmt.Sprintf("clip origin must be finite, got (%v, %v)", c.X, c.Y),
		})
	}
	if !isFinite(c.W) || !isFinite(c.H) || c.W <= 0 || c.H <= 0 {
		v.report(i, tag, ValidationError{
			Kind:   ResourceCommand,
			Reason: fmt.Sprintf("clip size must be finite and positive, got %vx%v", c.W, c.H),
		})
	}
	if !isFinite(c.Radius) || c.Radius < 0 {
		v.report(i, tag, ValidationError{
			Kind:   ResourceCommand,
			Reason: fmt.Sprintf("clip radius must be finite and non-negative, got %v", c.Radius),
		})
	} else if c.W > 0 && c.H > 0 {
		if maxR := math.Min(c.W, c.H) / 2; c.Radius > maxR {
			v.report(i, tag, ValidationError{
				Kind: ResourceCommand,
				Reason: fmt.Sprintf("clip radius %v exceeds half the smaller rect dimension %v",
					c.Radius, maxR),
			})
		}
	}
}

func (v *validator) checkFiniteMatrix(i int, tag CommandType, m Matrix, label string) {
	if !isFinite(m.A) || !isFinite(m.B) || !isFinite(m.C) ||
		!isFinite(m.D) || !isFinite(m.E) || !isFinite(m.F) {
		v.report(i, tag, ValidationError{
			Kind:   ResourceCommand,
			Reason: fmt.Sprintf("%s contains NaN or Inf: %+v", label, m),
		})
	}
}

func (v *validator) checkRect(i int, tag CommandType, r Rect, label string) {
	if !isFinite(r.MinX) || !isFinite(r.MinY) || !isFinite(r.MaxX) || !isFinite(r.MaxY) {
		v.report(i, tag, ValidationError{
			Kind:   ResourceCommand,
			Reason: fmt.Sprintf("%s contains NaN or Inf: %+v", label, r),
		})
	}
}

func (v *validator) checkFillRule(i int, tag CommandType, rule FillRule) {
	if rule > FillRuleEvenOdd {
		v.report(i, tag, ValidationError{
			Kind:   ResourceCommand,
			Reason: fmt.Sprintf("invalid fill rule tag %d", uint8(rule)),
		})
	}
}

func (v *validator) checkImageOptions(i int, tag CommandType, opts ImageOptions) {
	if opts.Interpolation > InterpolationBilinear {
		v.report(i, tag, ValidationError{
			Kind:   ResourceCommand,
			Reason: fmt.Sprintf("invalid interpolation mode tag %d", uint8(opts.Interpolation)),
		})
	}
	if !isFinite(opts.Alpha) {
		v.finiteErr(i, tag, "image alpha", opts.Alpha)
	}
}

func (v *validator) checkStroke(i int, tag CommandType, s Stroke) {
	if !isFinite(s.Width) {
		v.finiteErr(i, tag, "stroke width", s.Width)
	}
	if !isFinite(s.MiterLimit) {
		v.finiteErr(i, tag, "stroke miter limit", s.MiterLimit)
	}
	if !isFinite(s.DashOffset) {
		v.finiteErr(i, tag, "stroke dash offset", s.DashOffset)
	}
	if s.Cap > LineCapSquare {
		v.report(i, tag, ValidationError{
			Kind:   ResourceCommand,
			Reason: fmt.Sprintf("invalid stroke line cap tag %d", uint8(s.Cap)),
		})
	}
	if s.Join > LineJoinBevel {
		v.report(i, tag, ValidationError{
			Kind:   ResourceCommand,
			Reason: fmt.Sprintf("invalid stroke line join tag %d", uint8(s.Join)),
		})
	}
	for j, d := range s.DashPattern {
		if !isFinite(d) {
			v.finiteErr(i, tag, fmt.Sprintf("stroke dash pattern[%d]", j), d)
		}
	}
}

func (v *validator) checkTextCommand(i int, tag CommandType, s string, x, y, fontSize float64, face text.Face) {
	if !isFinite(x) || !isFinite(y) {
		v.report(i, tag, ValidationError{
			Kind:   ResourceCommand,
			Reason: fmt.Sprintf("text position must be finite, got (%v, %v)", x, y),
		})
	}
	if !isFinite(fontSize) {
		v.finiteErr(i, tag, "font size", fontSize)
	}
	// An empty string never reaches a backend's text path; a nil face only
	// matters when glyphs would actually be measured or drawn.
	if s != "" && face == nil {
		v.report(i, tag, ValidationError{
			Kind:          ResourceFont,
			ResourceIndex: -1,
			Reason:        "text command with non-empty text has a nil font face",
		})
	}
}

func (v *validator) checkPathRef(i int, tag CommandType, pool *ResourcePool, ref PathRef) {
	if uint32(ref) == InvalidRef {
		v.badRef(i, tag, ResourcePath, int(ref), "explicit invalid reference")
		return
	}
	if pool == nil {
		v.badRef(i, tag, ResourcePath, int(ref), "resource pool is missing")
		return
	}
	// Compare in uint32 space: converting a >2^31 reference to int would be
	// negative on 32-bit platforms and slip past a length comparison, then
	// panic on a negative slice index during playback.
	if uint64(ref) >= uint64(len(pool.paths)) {
		v.badRef(i, tag, ResourcePath, int(ref),
			fmt.Sprintf("index out of range (pool has %d paths)", len(pool.paths)))
		return
	}
	if pool.paths[ref] == nil {
		v.badRef(i, tag, ResourcePath, int(ref),
			"pooled path is nil and would dereference in a backend")
	}
}

func (v *validator) checkBrushRef(i int, tag CommandType, pool *ResourcePool, ref BrushRef) {
	if uint32(ref) == InvalidRef {
		v.badRef(i, tag, ResourceBrush, int(ref), "explicit invalid reference")
		return
	}
	if pool == nil {
		v.badRef(i, tag, ResourceBrush, int(ref), "resource pool is missing")
		return
	}
	if uint64(ref) >= uint64(len(pool.brushes)) {
		v.badRef(i, tag, ResourceBrush, int(ref),
			fmt.Sprintf("index out of range (pool has %d brushes)", len(pool.brushes)))
		return
	}
	brush := pool.brushes[ref]
	if brush == nil {
		v.badRef(i, tag, ResourceBrush, int(ref), "pooled brush is nil")
		return
	}
	v.checkBrush(i, tag, int(ref), pool, brush)
}

func (v *validator) checkImageRef(i int, tag CommandType, pool *ResourcePool, ref ImageRef) {
	if uint32(ref) == InvalidRef {
		v.badRef(i, tag, ResourceImage, int(ref), "explicit invalid reference")
		return
	}
	if pool == nil {
		v.badRef(i, tag, ResourceImage, int(ref), "resource pool is missing")
		return
	}
	if uint64(ref) >= uint64(len(pool.images)) {
		v.badRef(i, tag, ResourceImage, int(ref),
			fmt.Sprintf("index out of range (pool has %d images)", len(pool.images)))
		return
	}
	if pool.images[ref] == nil {
		v.badRef(i, tag, ResourceImage, int(ref), "pooled image is nil")
	}
}

func (v *validator) badRef(i int, tag CommandType, kind ResourceKind, idx int, reason string) {
	v.report(i, tag, ValidationError{
		Kind:          kind,
		ResourceIndex: idx,
		Reason:        reason,
	})
}

func (v *validator) finiteErr(i int, tag CommandType, field string, value float64) {
	v.report(i, tag, ValidationError{
		Kind:   ResourceCommand,
		Reason: fmt.Sprintf("%s must be finite, got %v", field, value),
	})
}

// checkBrush validates the geometry and color stops stored inside a pooled
// brush. A brush that survives pooling can still carry NaN/Inf coordinates.
func (v *validator) checkBrush(i int, tag CommandType, idx int, pool *ResourcePool, brush Brush) {
	switch b := brush.(type) {
	case SolidBrush:
		v.checkRGBA(i, tag, ResourceBrush, idx, b.Color, "solid brush color")

	case *LinearGradientBrush:
		v.checkPoint(i, tag, ResourceBrush, idx, b.Start, "gradient start")
		v.checkPoint(i, tag, ResourceBrush, idx, b.End, "gradient end")
		v.checkStops(i, tag, ResourceBrush, idx, b.Stops)
		v.checkExtend(i, tag, idx, b.Extend)

	case *RadialGradientBrush:
		v.checkPoint(i, tag, ResourceBrush, idx, b.Center, "gradient center")
		v.checkPoint(i, tag, ResourceBrush, idx, b.Focus, "gradient focus")
		if !isFinite(b.StartRadius) || !isFinite(b.EndRadius) {
			v.report(i, tag, ValidationError{
				Kind:          ResourceBrush,
				ResourceIndex: idx,
				Reason: fmt.Sprintf("gradient radii must be finite, got %v..%v",
					b.StartRadius, b.EndRadius),
			})
		}
		v.checkStops(i, tag, ResourceBrush, idx, b.Stops)
		v.checkExtend(i, tag, idx, b.Extend)

	case *SweepGradientBrush:
		v.checkPoint(i, tag, ResourceBrush, idx, b.Center, "gradient center")
		if !isFinite(b.StartAngle) || !isFinite(b.EndAngle) {
			v.report(i, tag, ValidationError{
				Kind:          ResourceBrush,
				ResourceIndex: idx,
				Reason: fmt.Sprintf("sweep angles must be finite, got %v..%v",
					b.StartAngle, b.EndAngle),
			})
		}
		v.checkStops(i, tag, ResourceBrush, idx, b.Stops)
		v.checkExtend(i, tag, idx, b.Extend)

	case *PatternBrush:
		// Pattern brushes reference the image pool directly; reuse the same
		// range/nil checks so the error category stays consistent.
		v.checkImageRef(i, tag, pool, b.Image)
		m := b.Transform
		if !isFinite(m.A) || !isFinite(m.B) || !isFinite(m.C) ||
			!isFinite(m.D) || !isFinite(m.E) || !isFinite(m.F) {
			v.report(i, tag, ValidationError{
				Kind:          ResourceBrush,
				ResourceIndex: idx,
				Reason:        fmt.Sprintf("pattern transform contains NaN or Inf: %+v", m),
			})
		}

	default:
		v.report(i, tag, ValidationError{
			Kind:          ResourceBrush,
			ResourceIndex: idx,
			Reason:        fmt.Sprintf("unknown brush implementation %T", brush),
		})
	}
}

func (v *validator) checkPoint(i int, tag CommandType, kind ResourceKind, idx int, p gg.Point, label string) {
	if !isFinite(p.X) || !isFinite(p.Y) {
		v.report(i, tag, ValidationError{
			Kind:          kind,
			ResourceIndex: idx,
			Reason:        fmt.Sprintf("%s must be finite, got (%v, %v)", label, p.X, p.Y),
		})
	}
}

func (v *validator) checkRGBA(i int, tag CommandType, kind ResourceKind, idx int, c gg.RGBA, label string) {
	if !isFinite(c.R) || !isFinite(c.G) || !isFinite(c.B) || !isFinite(c.A) {
		v.report(i, tag, ValidationError{
			Kind:          kind,
			ResourceIndex: idx,
			Reason:        fmt.Sprintf("%s contains NaN or Inf: %+v", label, c),
		})
	}
}

func (v *validator) checkStops(i int, tag CommandType, kind ResourceKind, idx int, stops []GradientStop) {
	for j, stop := range stops {
		if !isFinite(stop.Offset) {
			v.report(i, tag, ValidationError{
				Kind:          kind,
				ResourceIndex: idx,
				Reason:        fmt.Sprintf("gradient stop[%d] offset must be finite, got %v", j, stop.Offset),
			})
		}
		v.checkRGBA(i, tag, kind, idx, stop.Color,
			fmt.Sprintf("gradient stop[%d] color", j))
	}
}

func (v *validator) checkExtend(i int, tag CommandType, idx int, mode ExtendMode) {
	if mode < ExtendPad || mode > ExtendReflect {
		v.report(i, tag, ValidationError{
			Kind:          ResourceBrush,
			ResourceIndex: idx,
			Reason:        fmt.Sprintf("invalid gradient extend mode %d", int(mode)),
		})
	}
}

// isFinite reports whether f is neither NaN nor an infinity.
func isFinite(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}

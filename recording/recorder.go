package recording

import (
	"image"
	"math"
	"strings"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/text"
)

// Recorder captures drawing operations as commands.
// It mirrors the gg.Context drawing API but generates commands
// instead of rasterizing pixels. Use FinishRecording to obtain
// an immutable Recording that can be replayed to different backends.
//
// Example:
//
//	rec := recording.NewRecorder(800, 600)
//	rec.SetRGB(1, 0, 0)
//	rec.DrawCircle(100, 100, 50)
//	rec.Fill()
//	recording := rec.FinishRecording()
//
// The Recorder is not safe for concurrent use. Use one Recorder per
// goroutine; independent Recorders share no mutable state.
type Recorder struct {
	width, height int
	commands      []Command
	resources     *ResourcePool

	// Current path being built
	currentPath *gg.Path

	// state is the single live graphics-state snapshot. Every style,
	// transform and font access goes through it; Save/Restore only push
	// and pop independent copies on stateStack.
	state graphicsState

	// stateStack holds cloned snapshots for Save/Restore.
	stateStack []graphicsState
}

// NewRecorder creates a new Recorder for the given dimensions.
// The Recorder starts with default state: black fill/stroke, 1px line width,
// butt caps, miter joins, non-zero fill rule, and identity transform.
func NewRecorder(width, height int) *Recorder {
	return &Recorder{
		width:       width,
		height:      height,
		commands:    make([]Command, 0, 256),
		resources:   NewResourcePool(),
		currentPath: gg.NewPath(),
		state:       defaultGraphicsState(),
		stateStack:  make([]graphicsState, 0, 8),
	}
}

// Reset returns the Recorder to the same state as a freshly allocated
// NewRecorder(width, height): all commands, resources, path contents, styles
// and saved snapshots are cleared, so a Recorder can be reused for a new
// recording without leaking state from the previous one. Recordings already
// returned by FinishRecording keep their own command slice and resource pool
// and are unaffected.
func (r *Recorder) Reset() {
	r.commands = make([]Command, 0, cap(r.commands))
	r.resources = NewResourcePool()
	r.currentPath = gg.NewPath()
	r.state = defaultGraphicsState()
	r.stateStack = make([]graphicsState, 0, cap(r.stateStack))
}

// FinishRecording returns an immutable Recording containing all recorded commands.
// After calling FinishRecording, the Recorder should not be used again.
func (r *Recorder) FinishRecording() *Recording {
	return &Recording{
		width:     r.width,
		height:    r.height,
		commands:  r.commands,
		resources: r.resources,
	}
}

// Recording is an immutable container for recorded drawing commands.
// It can be replayed to any Backend implementation.
type Recording struct {
	width, height int
	commands      []Command
	resources     *ResourcePool
}

// Width returns the width of the recording canvas.
func (r *Recording) Width() int {
	return r.width
}

// Height returns the height of the recording canvas.
func (r *Recording) Height() int {
	return r.height
}

// Commands returns the recorded commands.
func (r *Recording) Commands() []Command {
	return r.commands
}

// Resources returns the resource pool.
func (r *Recording) Resources() *ResourcePool {
	return r.resources
}

// Playback replays the recording to the given backend.
func (r *Recording) Playback(backend Backend) error {
	// Initialize backend
	if err := backend.Begin(r.width, r.height); err != nil {
		return err
	}

	transform := Identity()
	transformStack := make([]Matrix, 0, 8)

	// Replay each command
	for _, cmd := range r.commands {
		switch c := cmd.(type) {
		case SaveCommand:
			transformStack = append(transformStack, transform)
			backend.Save()
		case RestoreCommand:
			backend.Restore()
			if len(transformStack) > 0 {
				transform = transformStack[len(transformStack)-1]
				transformStack = transformStack[:len(transformStack)-1]
			}
		case SetTransformCommand:
			transform = c.Matrix
			backend.SetTransform(c.Matrix)
		case SetClipCommand:
			path := r.resources.GetPath(c.Path)
			setPlaybackClip(backend, path, c.Rule, transform)
		case ClearClipCommand:
			backend.ClearClip()
		case ClipRoundRectCommand:
			rrPath := buildClipRoundRectPath(c, transform)
			setPlaybackClip(backend, rrPath, FillRuleNonZero, transform)
		case FillPathCommand:
			path := r.resources.GetPath(c.Path)
			brush := r.resources.GetBrush(c.Brush)
			backend.FillPath(path, brush, c.Rule)
		case StrokePathCommand:
			path := r.resources.GetPath(c.Path)
			brush := r.resources.GetBrush(c.Brush)
			backend.StrokePath(path, brush, c.Stroke)
		case FillRectCommand:
			brush := r.resources.GetBrush(c.Brush)
			backend.FillRect(c.Rect, brush)
		case StrokeRectCommand:
			brush := r.resources.GetBrush(c.Brush)
			path := gg.BuildPath().Rect(c.Rect.MinX, c.Rect.MinY, c.Rect.Width(), c.Rect.Height()).Build()
			// StrokeRectCommand stores world-space bounds. Isolate the identity
			// transform so backends do not apply the recording transform twice,
			// then restore their complete graphics state for later commands.
			backend.Save()
			backend.SetTransform(Identity())
			backend.StrokePath(path, brush, c.Stroke)
			backend.Restore()
		case DrawImageCommand:
			img := r.resources.GetImage(c.Image)
			backend.DrawImage(img, c.SrcRect, c.DstRect, c.Options)
		case DrawTextCommand:
			brush := r.resources.GetBrush(c.Brush)
			backend.DrawText(c.Text, c.X, c.Y, c.Face, brush)
		case StrokeTextCommand:
			brush := r.resources.GetBrush(c.Brush)
			backend.DrawText(c.Text, c.X, c.Y, c.Face, brush)
		// Style commands are handled by the backend's internal state
		// during the actual drawing operations
		case SetFillStyleCommand, SetStrokeStyleCommand,
			SetLineWidthCommand, SetLineCapCommand, SetLineJoinCommand,
			SetMiterLimitCommand, SetDashCommand, SetFillRuleCommand:
			// These are state commands that were recorded but the actual
			// style is captured in the drawing commands themselves
		}
	}

	return backend.End()
}

// setPlaybackClip applies a world-space clip without disturbing the backend's
// current transform. Save/Restore cannot be used here because Restore would
// also discard the newly installed clip.
func setPlaybackClip(backend Backend, path *gg.Path, rule FillRule, transform Matrix) {
	backend.SetTransform(Identity())
	backend.SetClip(path, rule)
	backend.SetTransform(transform)
}

// buildClipRoundRectPath builds a world-space rounded-rectangle clip path.
// For uniform axis-aligned transforms, uses the fast two-corner + scaled radius
// path. For rotation/shear/non-uniform scale, builds the rounded rect in user
// space and transforms the full path — matching Context.ClipRoundRect (#503).
func buildClipRoundRectPath(c ClipRoundRectCommand, t Matrix) *gg.Path {
	if isUniformAxisAligned(t) {
		x1, y1 := t.TransformPoint(c.X, c.Y)
		x2, y2 := t.TransformPoint(c.X+c.W, c.Y+c.H)
		x := math.Min(x1, x2)
		y := math.Min(y1, y2)
		w := math.Abs(x2 - x1)
		h := math.Abs(y2 - y1)
		radius := 0.0
		if c.Radius > 0 {
			radius = c.Radius * t.ScaleFactor()
			radius = math.Min(radius, math.Min(w, h)/2)
		}
		return gg.BuildPath().RoundRect(x, y, w, h, radius).Build()
	}
	px, py := c.X, c.Y
	pw, ph := math.Abs(c.W), math.Abs(c.H)
	if c.W < 0 {
		px += c.W
	}
	if c.H < 0 {
		py += c.H
	}
	userPath := gg.NewPath()
	userPath.RoundedRectangle(px, py, pw, ph, c.Radius)
	return userPath.Transform(gg.Matrix{
		A: t.A, B: t.B, C: t.C,
		D: t.D, E: t.E, F: t.F,
	})
}

// isUniformAxisAligned reports whether the transform keeps rounded rectangles
// axis-aligned with circular (not elliptical) corners.
func isUniformAxisAligned(t Matrix) bool {
	switch {
	case t.B == 0 && t.D == 0:
		return math.Abs(t.A) == math.Abs(t.E)
	case t.A == 0 && t.E == 0:
		return math.Abs(t.B) == math.Abs(t.D)
	default:
		return false
	}
}

// --------------------------------------------------------------------------
// Dimensions
// --------------------------------------------------------------------------

// Width returns the width of the recording canvas.
func (r *Recorder) Width() int {
	return r.width
}

// Height returns the height of the recording canvas.
func (r *Recorder) Height() int {
	return r.height
}

// --------------------------------------------------------------------------
// State Management
// --------------------------------------------------------------------------

// Save saves the current graphics state to the stack.
// The state includes transform, fill style, stroke style, line properties,
// anti-aliasing and font settings.
//
// The snapshot is deep-copied, so later mutations (including replacing the
// dash pattern) cannot affect a saved frame. If the stack has reached
// maxStateDepth, Save is a no-op: no frame is pushed and no SaveCommand is
// emitted, preventing unbounded memory growth from runaway nesting.
func (r *Recorder) Save() {
	if len(r.stateStack) >= maxStateDepth {
		return
	}
	r.stateStack = append(r.stateStack, r.state.clone())
	r.commands = append(r.commands, SaveCommand{})
}

// Restore restores the previously saved graphics state.
// If the state stack is empty, this is a no-op (and emits no command).
// The restored snapshot is itself a deep copy, so a subsequent SetDash on the
// live state can never mutate frames still held by the stack.
func (r *Recorder) Restore() {
	s, ok := r.popSnapshot()
	if !ok {
		return
	}
	r.state = s
	r.commands = append(r.commands, RestoreCommand{})
}

// popSnapshot removes and returns the top state frame without emitting a
// command. It is the single stack-pop primitive: Restore and DrawEllipticalArc
// both unwind frames through it so stack semantics live in exactly one place.
// The returned snapshot already owns an independent dash slice.
func (r *Recorder) popSnapshot() (graphicsState, bool) {
	if len(r.stateStack) == 0 {
		return graphicsState{}, false
	}
	last := len(r.stateStack) - 1
	s := r.stateStack[last]
	r.stateStack[last] = graphicsState{} // drop references before shortening
	r.stateStack = r.stateStack[:last]
	return s, true
}

// Push is an alias for Save, matching gg.Context API.
func (r *Recorder) Push() {
	r.Save()
}

// Pop is an alias for Restore, matching gg.Context API.
func (r *Recorder) Pop() {
	r.Restore()
}

// --------------------------------------------------------------------------
// Transform
// --------------------------------------------------------------------------

// Identity resets the transformation matrix to identity.
func (r *Recorder) Identity() {
	r.state.transform = Identity()
	r.commands = append(r.commands, SetTransformCommand{Matrix: r.state.transform})
}

// Translate applies a translation to the transformation matrix.
func (r *Recorder) Translate(x, y float64) {
	r.state.transform = r.state.transform.Multiply(Translate(x, y))
	r.commands = append(r.commands, SetTransformCommand{Matrix: r.state.transform})
}

// Scale applies a scaling transformation.
func (r *Recorder) Scale(sx, sy float64) {
	r.state.transform = r.state.transform.Multiply(Scale(sx, sy))
	r.commands = append(r.commands, SetTransformCommand{Matrix: r.state.transform})
}

// Rotate applies a rotation (angle in radians).
func (r *Recorder) Rotate(angle float64) {
	r.state.transform = r.state.transform.Multiply(Rotate(angle))
	r.commands = append(r.commands, SetTransformCommand{Matrix: r.state.transform})
}

// RotateAbout rotates around a specific point.
func (r *Recorder) RotateAbout(angle, x, y float64) {
	r.Translate(x, y)
	r.Rotate(angle)
	r.Translate(-x, -y)
}

// ScaleAbout scales around a specific point.
func (r *Recorder) ScaleAbout(sx, sy, x, y float64) {
	r.Translate(x, y)
	r.Scale(sx, sy)
	r.Translate(-x, -y)
}

// ShearAbout shears around a specific point.
func (r *Recorder) ShearAbout(sx, sy, x, y float64) {
	r.Translate(x, y)
	r.Shear(sx, sy)
	r.Translate(-x, -y)
}

// Shear applies a shear transformation.
func (r *Recorder) Shear(x, y float64) {
	r.state.transform = r.state.transform.Multiply(Shear(x, y))
	r.commands = append(r.commands, SetTransformCommand{Matrix: r.state.transform})
}

// Transform multiplies the current transformation matrix by the given matrix.
func (r *Recorder) Transform(m Matrix) {
	r.state.transform = r.state.transform.Multiply(m)
	r.commands = append(r.commands, SetTransformCommand{Matrix: r.state.transform})
}

// SetTransform replaces the current transformation matrix.
func (r *Recorder) SetTransform(m Matrix) {
	r.state.transform = m
	r.commands = append(r.commands, SetTransformCommand{Matrix: r.state.transform})
}

// GetTransform returns a copy of the current transformation matrix.
func (r *Recorder) GetTransform() Matrix {
	return r.state.transform
}

// TransformPoint transforms a point by the current matrix.
func (r *Recorder) TransformPoint(x, y float64) (float64, float64) {
	return r.state.transform.TransformPoint(x, y)
}

// InvertY inverts the Y axis (useful for coordinate system changes).
func (r *Recorder) InvertY() {
	r.Translate(0, float64(r.height))
	r.Scale(1, -1)
}

// --------------------------------------------------------------------------
// Color/Style
// --------------------------------------------------------------------------

// SetColor sets both fill and stroke color from a color.Color.
func (r *Recorder) SetColor(c gg.RGBA) {
	brush := NewSolidBrush(c)
	r.state.fillBrush = brush
	r.state.strokeBrush = brush
	brushRef := r.resources.AddBrush(brush)
	r.commands = append(r.commands,
		SetFillStyleCommand{Brush: brushRef},
		SetStrokeStyleCommand{Brush: brushRef})
}

// SetRGB sets both fill and stroke color using RGB values (0-1).
func (r *Recorder) SetRGB(red, green, blue float64) {
	r.SetColor(gg.RGB(red, green, blue))
}

// SetRGBA sets both fill and stroke color using RGBA values (0-1).
func (r *Recorder) SetRGBA(red, green, blue, alpha float64) {
	r.SetColor(gg.RGBA2(red, green, blue, alpha))
}

// SetHexColor sets both fill and stroke color using a hex string.
func (r *Recorder) SetHexColor(hex string) {
	r.SetColor(gg.Hex(hex))
}

// SetFillStyle sets the fill brush.
func (r *Recorder) SetFillStyle(brush Brush) {
	r.state.fillBrush = brush
	brushRef := r.resources.AddBrush(brush)
	r.commands = append(r.commands, SetFillStyleCommand{Brush: brushRef})
}

// SetStrokeStyle sets the stroke brush.
func (r *Recorder) SetStrokeStyle(brush Brush) {
	r.state.strokeBrush = brush
	brushRef := r.resources.AddBrush(brush)
	r.commands = append(r.commands, SetStrokeStyleCommand{Brush: brushRef})
}

// SetFillBrush sets the fill brush from a gg.Brush.
func (r *Recorder) SetFillBrush(brush gg.Brush) {
	r.SetFillStyle(BrushFromGG(brush))
}

// SetStrokeBrush sets the stroke brush from a gg.Brush.
func (r *Recorder) SetStrokeBrush(brush gg.Brush) {
	r.SetStrokeStyle(BrushFromGG(brush))
}

// SetFillRGB sets the fill color using RGB values (0-1).
func (r *Recorder) SetFillRGB(red, green, blue float64) {
	r.SetFillStyle(NewSolidBrush(gg.RGB(red, green, blue)))
}

// SetFillRGBA sets the fill color using RGBA values (0-1).
func (r *Recorder) SetFillRGBA(red, green, blue, alpha float64) {
	r.SetFillStyle(NewSolidBrush(gg.RGBA2(red, green, blue, alpha)))
}

// SetStrokeRGB sets the stroke color using RGB values (0-1).
func (r *Recorder) SetStrokeRGB(red, green, blue float64) {
	r.SetStrokeStyle(NewSolidBrush(gg.RGB(red, green, blue)))
}

// SetStrokeRGBA sets the stroke color using RGBA values (0-1).
func (r *Recorder) SetStrokeRGBA(red, green, blue, alpha float64) {
	r.SetStrokeStyle(NewSolidBrush(gg.RGBA2(red, green, blue, alpha)))
}

// --------------------------------------------------------------------------
// Line Properties
// --------------------------------------------------------------------------

// SetLineWidth sets the line width for stroking.
func (r *Recorder) SetLineWidth(width float64) {
	r.state.lineWidth = width
	r.commands = append(r.commands, SetLineWidthCommand{Width: width})
}

// SetLineCap sets the line cap style.
func (r *Recorder) SetLineCap(lc LineCap) {
	r.state.lineCap = lc
	r.commands = append(r.commands, SetLineCapCommand{Cap: lc})
}

// SetLineCapGG sets the line cap style from gg.LineCap.
func (r *Recorder) SetLineCapGG(lc gg.LineCap) {
	// #nosec G115 -- LineCap enum values are within uint8 range
	r.SetLineCap(LineCap(lc))
}

// SetLineJoin sets the line join style.
func (r *Recorder) SetLineJoin(join LineJoin) {
	r.state.lineJoin = join
	r.commands = append(r.commands, SetLineJoinCommand{Join: join})
}

// SetLineJoinGG sets the line join style from gg.LineJoin.
func (r *Recorder) SetLineJoinGG(join gg.LineJoin) {
	// #nosec G115 -- LineJoin enum values are within uint8 range
	r.SetLineJoin(LineJoin(join))
}

// SetMiterLimit sets the miter limit for line joins.
func (r *Recorder) SetMiterLimit(limit float64) {
	r.state.miterLimit = limit
	r.commands = append(r.commands, SetMiterLimitCommand{Limit: limit})
}

// SetDash sets the dash pattern for stroking.
// Pass alternating dash and gap lengths.
// Passing no arguments clears the dash pattern (returns to solid lines).
func (r *Recorder) SetDash(lengths ...float64) {
	if len(lengths) == 0 {
		r.ClearDash()
		return
	}

	r.state.dashPattern = make([]float64, len(lengths))
	copy(r.state.dashPattern, lengths)
	r.commands = append(r.commands, SetDashCommand{Pattern: r.state.dashPattern, Offset: r.state.dashOffset})
}

// SetDashOffset sets the starting offset into the dash pattern.
func (r *Recorder) SetDashOffset(offset float64) {
	r.state.dashOffset = offset
	if r.state.dashPattern != nil {
		r.commands = append(r.commands, SetDashCommand{Pattern: r.state.dashPattern, Offset: r.state.dashOffset})
	}
}

// ClearDash removes the dash pattern, returning to solid lines.
func (r *Recorder) ClearDash() {
	r.state.dashPattern = nil
	r.state.dashOffset = 0
	r.commands = append(r.commands, SetDashCommand{Pattern: nil, Offset: 0})
}

// SetFillRule sets the fill rule.
func (r *Recorder) SetFillRule(rule FillRule) {
	r.state.fillRule = rule
	r.commands = append(r.commands, SetFillRuleCommand{Rule: rule})
}

// SetFillRuleGG sets the fill rule from gg.FillRule.
func (r *Recorder) SetFillRuleGG(rule gg.FillRule) {
	// #nosec G115 -- FillRule enum values are within uint8 range
	r.SetFillRule(FillRule(rule))
}

// SetAntiAlias enables or disables anti-aliasing for geometry rendering.
// When disabled, shapes are rendered with binary coverage (no gray edge pixels).
func (r *Recorder) SetAntiAlias(enabled bool) {
	r.state.antiAlias = enabled
	r.commands = append(r.commands, SetAntiAliasCommand{Enabled: enabled})
}

// AntiAlias returns whether anti-aliasing is enabled.
func (r *Recorder) AntiAlias() bool {
	return r.state.antiAlias
}

// --------------------------------------------------------------------------
// Path Building
// --------------------------------------------------------------------------

// MoveTo starts a new subpath at the given point.
func (r *Recorder) MoveTo(x, y float64) {
	px, py := r.state.transform.TransformPoint(x, y)
	r.currentPath.MoveTo(px, py)
}

// LineTo adds a line to the current path.
func (r *Recorder) LineTo(x, y float64) {
	px, py := r.state.transform.TransformPoint(x, y)
	r.currentPath.LineTo(px, py)
}

// QuadraticTo adds a quadratic Bezier curve to the current path.
func (r *Recorder) QuadraticTo(cx, cy, x, y float64) {
	cpx, cpy := r.state.transform.TransformPoint(cx, cy)
	px, py := r.state.transform.TransformPoint(x, y)
	r.currentPath.QuadraticTo(cpx, cpy, px, py)
}

// CubicTo adds a cubic Bezier curve to the current path.
func (r *Recorder) CubicTo(c1x, c1y, c2x, c2y, x, y float64) {
	cp1x, cp1y := r.state.transform.TransformPoint(c1x, c1y)
	cp2x, cp2y := r.state.transform.TransformPoint(c2x, c2y)
	px, py := r.state.transform.TransformPoint(x, y)
	r.currentPath.CubicTo(cp1x, cp1y, cp2x, cp2y, px, py)
}

// ClosePath closes the current subpath.
func (r *Recorder) ClosePath() {
	r.currentPath.Close()
}

// ClearPath clears the current path.
func (r *Recorder) ClearPath() {
	r.currentPath.Clear()
}

// NewSubPath starts a new subpath without closing the previous one.
// This is a no-op as MoveTo already creates a new subpath.
func (r *Recorder) NewSubPath() {
	// No-op, provided for API compatibility
}

// --------------------------------------------------------------------------
// Drawing
// --------------------------------------------------------------------------

// Fill fills the current path and clears it.
func (r *Recorder) Fill() {
	if r.currentPath.NumVerbs() == 0 {
		return
	}

	pathRef := r.resources.AddPath(r.currentPath)
	brushRef := r.resources.AddBrush(r.state.fillBrush)

	r.commands = append(r.commands, FillPathCommand{
		Path:  pathRef,
		Brush: brushRef,
		Rule:  r.state.fillRule,
	})

	r.currentPath = gg.NewPath()
}

// FillPreserve fills the current path without clearing it.
func (r *Recorder) FillPreserve() {
	if r.currentPath.NumVerbs() == 0 {
		return
	}

	pathRef := r.resources.AddPath(r.currentPath)
	brushRef := r.resources.AddBrush(r.state.fillBrush)

	r.commands = append(r.commands, FillPathCommand{
		Path:  pathRef,
		Brush: brushRef,
		Rule:  r.state.fillRule,
	})
}

// Stroke strokes the current path and clears it.
func (r *Recorder) Stroke() {
	if r.currentPath.NumVerbs() == 0 {
		return
	}

	pathRef := r.resources.AddPath(r.currentPath)
	brushRef := r.resources.AddBrush(r.state.strokeBrush)

	r.commands = append(r.commands, StrokePathCommand{
		Path:   pathRef,
		Brush:  brushRef,
		Stroke: r.state.currentStroke(),
	})

	r.currentPath = gg.NewPath()
}

// StrokePreserve strokes the current path without clearing it.
func (r *Recorder) StrokePreserve() {
	if r.currentPath.NumVerbs() == 0 {
		return
	}

	pathRef := r.resources.AddPath(r.currentPath)
	brushRef := r.resources.AddBrush(r.state.strokeBrush)

	r.commands = append(r.commands, StrokePathCommand{
		Path:   pathRef,
		Brush:  brushRef,
		Stroke: r.state.currentStroke(),
	})
}

// FillStroke fills and then strokes the current path, then clears it.
func (r *Recorder) FillStroke() {
	r.FillPreserve()
	r.Stroke()
}

// --------------------------------------------------------------------------
// Shapes
// --------------------------------------------------------------------------

// DrawPoint draws a single point at the given coordinates.
func (r *Recorder) DrawPoint(x, y, radius float64) {
	r.DrawCircle(x, y, radius)
}

// DrawLine draws a line between two points.
func (r *Recorder) DrawLine(x1, y1, x2, y2 float64) {
	r.MoveTo(x1, y1)
	r.LineTo(x2, y2)
}

// DrawRectangle draws a rectangle.
func (r *Recorder) DrawRectangle(x, y, w, h float64) {
	r.MoveTo(x, y)
	r.LineTo(x+w, y)
	r.LineTo(x+w, y+h)
	r.LineTo(x, y+h)
	r.ClosePath()
}

// DrawRoundedRectangle draws a rectangle with rounded corners.
func (r *Recorder) DrawRoundedRectangle(x, y, w, h, radius float64) {
	// Clamp radius to half of the smaller dimension
	maxR := math.Min(w, h) / 2
	if radius > maxR {
		radius = maxR
	}

	r.MoveTo(x+radius, y)
	r.LineTo(x+w-radius, y)
	r.drawArcPath(x+w-radius, y+radius, radius, -math.Pi/2, 0)
	r.LineTo(x+w, y+h-radius)
	r.drawArcPath(x+w-radius, y+h-radius, radius, 0, math.Pi/2)
	r.LineTo(x+radius, y+h)
	r.drawArcPath(x+radius, y+h-radius, radius, math.Pi/2, math.Pi)
	r.LineTo(x, y+radius)
	r.drawArcPath(x+radius, y+radius, radius, math.Pi, 3*math.Pi/2)
	r.ClosePath()
}

// DrawCircle draws a circle.
func (r *Recorder) DrawCircle(x, y, radius float64) {
	const k = 0.5522847498307936 // 4/3 * (sqrt(2) - 1)
	offset := radius * k

	r.MoveTo(x+radius, y)
	r.CubicTo(x+radius, y+offset, x+offset, y+radius, x, y+radius)
	r.CubicTo(x-offset, y+radius, x-radius, y+offset, x-radius, y)
	r.CubicTo(x-radius, y-offset, x-offset, y-radius, x, y-radius)
	r.CubicTo(x+offset, y-radius, x+radius, y-offset, x+radius, y)
	r.ClosePath()
}

// DrawEllipse draws an ellipse.
func (r *Recorder) DrawEllipse(x, y, rx, ry float64) {
	const k = 0.5522847498307936
	ox := rx * k
	oy := ry * k

	r.MoveTo(x+rx, y)
	r.CubicTo(x+rx, y+oy, x+ox, y+ry, x, y+ry)
	r.CubicTo(x-ox, y+ry, x-rx, y+oy, x-rx, y)
	r.CubicTo(x-rx, y-oy, x-ox, y-ry, x, y-ry)
	r.CubicTo(x+ox, y-ry, x+rx, y-oy, x+rx, y)
	r.ClosePath()
}

// DrawArc draws a circular arc.
func (r *Recorder) DrawArc(x, y, radius, angle1, angle2 float64) {
	r.drawArcPath(x, y, radius, angle1, angle2)
}

// drawArcPath adds arc segments to the current path.
func (r *Recorder) drawArcPath(cx, cy, radius, angle1, angle2 float64) {
	const twoPi = 2 * math.Pi
	for angle2 < angle1 {
		angle2 += twoPi
	}

	const maxAngle = math.Pi / 2
	numSegments := int(math.Ceil((angle2 - angle1) / maxAngle))
	angleStep := (angle2 - angle1) / float64(numSegments)

	for i := 0; i < numSegments; i++ {
		a1 := angle1 + float64(i)*angleStep
		a2 := a1 + angleStep
		r.arcSegment(cx, cy, radius, a1, a2)
	}
}

// arcSegment adds a single arc segment using cubic Bezier curves.
func (r *Recorder) arcSegment(cx, cy, radius, a1, a2 float64) {
	alpha := math.Sin(a2-a1) * (math.Sqrt(4+3*math.Tan((a2-a1)/2)*math.Tan((a2-a1)/2)) - 1) / 3

	cos1, sin1 := math.Cos(a1), math.Sin(a1)
	cos2, sin2 := math.Cos(a2), math.Sin(a2)

	x1 := cx + radius*cos1
	y1 := cy + radius*sin1
	x2 := cx + radius*cos2
	y2 := cy + radius*sin2

	c1x := x1 - alpha*radius*sin1
	c1y := y1 + alpha*radius*cos1
	c2x := x2 + alpha*radius*sin2
	c2y := y2 - alpha*radius*cos2

	if r.currentPath.NumVerbs() == 0 {
		r.MoveTo(x1, y1)
	}
	r.CubicTo(c1x, c1y, c2x, c2y, x2, y2)
}

// DrawEllipticalArc draws an elliptical arc.
func (r *Recorder) DrawEllipticalArc(x, y, rx, ry, angle1, angle2 float64) {
	r.Save()
	r.Translate(x, y)
	r.Scale(rx, ry)
	r.DrawArc(0, 0, 1, angle1, angle2)
	// Unwind the frame through the shared stack primitive so the full
	// snapshot (styles, font, anti-alias, transform) is restored, while
	// keeping the path contents that were built under the local transform.
	// A RestoreCommand is always emitted, matching the pre-refactor stream
	// even when Save was a no-op against a full or empty stack.
	if s, ok := r.popSnapshot(); ok {
		r.state = s
	}
	r.commands = append(r.commands, RestoreCommand{})
}

// --------------------------------------------------------------------------
// Rectangles (Optimized)
// --------------------------------------------------------------------------

// FillRectangle fills a rectangle without adding it to the path.
// This is an optimized operation for the common case of axis-aligned rectangles.
func (r *Recorder) FillRectangle(x, y, w, h float64) {
	// Transform corners
	x1, y1 := r.state.transform.TransformPoint(x, y)
	x2, y2 := r.state.transform.TransformPoint(x+w, y+h)

	rect := NewRectFromPoints(x1, y1, x2, y2)
	brushRef := r.resources.AddBrush(r.state.fillBrush)

	r.commands = append(r.commands, FillRectCommand{
		Rect:  rect,
		Brush: brushRef,
	})
}

// StrokeRectangle strokes a rectangle without adding it to the path.
func (r *Recorder) StrokeRectangle(x, y, w, h float64) {
	stroke := r.state.currentStroke()
	// Recorder paths contain world-space coordinates, while gg applies the
	// current transform's scale to stroke metrics at draw time. Playback uses
	// identity for those world-space paths, so capture the same metric scaling
	// in the command. Dash lengths follow gg's current convention and scale
	// only when the transform enlarges the path.
	transformScale := r.state.transform.ScaleFactor()
	if transformScale <= 0 {
		transformScale = 1
	}
	stroke.Width *= transformScale
	if transformScale > 1 && len(stroke.DashPattern) > 0 {
		stroke.DashPattern = append([]float64(nil), stroke.DashPattern...)
		for i := range stroke.DashPattern {
			stroke.DashPattern[i] *= transformScale
		}
		stroke.DashOffset *= transformScale
	}
	brushRef := r.resources.AddBrush(r.state.strokeBrush)

	// StrokeRectCommand stores only normalized world-space bounds, so it is
	// exact only when translation is the complete transform and the input
	// dimensions preserve the path's top-left start and clockwise traversal.
	// Other transforms need all four transformed corners, especially for
	// dashed strokes where traversal direction is observable.
	translationOnly := r.state.transform.A == 1 && r.state.transform.B == 0 &&
		r.state.transform.D == 0 && r.state.transform.E == 1
	if w < 0 || h < 0 || !translationOnly {
		path := gg.NewPath()
		x1, y1 := r.state.transform.TransformPoint(x, y)
		path.MoveTo(x1, y1)
		x2, y2 := r.state.transform.TransformPoint(x+w, y)
		path.LineTo(x2, y2)
		x3, y3 := r.state.transform.TransformPoint(x+w, y+h)
		path.LineTo(x3, y3)
		x4, y4 := r.state.transform.TransformPoint(x, y+h)
		path.LineTo(x4, y4)
		path.Close()
		pathRef := r.resources.AddPath(path)

		// Isolate identity around the world-space path so every backend applies
		// its coordinates and captured stroke metrics exactly once.
		r.commands = append(r.commands,
			SaveCommand{},
			SetTransformCommand{Matrix: Identity()},
			StrokePathCommand{Path: pathRef, Brush: brushRef, Stroke: stroke},
			RestoreCommand{},
		)
		return
	}

	// Keep the compact representation for the common axis-aligned,
	// traversal-preserving case.
	x1, y1 := r.state.transform.TransformPoint(x, y)
	x2, y2 := r.state.transform.TransformPoint(x+w, y+h)
	rect := NewRectFromPoints(x1, y1, x2, y2)

	r.commands = append(r.commands, StrokeRectCommand{
		Rect:   rect,
		Brush:  brushRef,
		Stroke: stroke,
	})
}

// --------------------------------------------------------------------------
// Clipping
// --------------------------------------------------------------------------

// Clip sets the current path as the clipping region and clears the path.
func (r *Recorder) Clip() {
	if r.currentPath.NumVerbs() == 0 {
		return
	}

	pathRef := r.resources.AddPath(r.currentPath)
	r.commands = append(r.commands, SetClipCommand{
		Path: pathRef,
		Rule: r.state.fillRule,
	})

	r.currentPath = gg.NewPath()
}

// ClipPreserve sets the current path as the clipping region but keeps the path.
func (r *Recorder) ClipPreserve() {
	if r.currentPath.NumVerbs() == 0 {
		return
	}

	pathRef := r.resources.AddPath(r.currentPath)
	r.commands = append(r.commands, SetClipCommand{
		Path: pathRef,
		Rule: r.state.fillRule,
	})
}

// ResetClip removes all clipping regions.
func (r *Recorder) ResetClip() {
	r.commands = append(r.commands, ClearClipCommand{})
}

// ClipRoundRect sets a rounded rectangle as the clipping region.
// The rectangle is defined in user-space coordinates with uniform corner radius.
func (r *Recorder) ClipRoundRect(x, y, w, h, radius float64) {
	r.commands = append(r.commands, ClipRoundRectCommand{
		X: x, Y: y, W: w, H: h, Radius: radius,
	})
}

// --------------------------------------------------------------------------
// Image
// --------------------------------------------------------------------------

// DrawImage draws an image at the specified position.
func (r *Recorder) DrawImage(img image.Image, x, y int) {
	if img == nil {
		return
	}

	bounds := img.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()

	imageRef := r.resources.AddImage(img)

	// Transform destination rectangle
	x1, y1 := r.state.transform.TransformPoint(float64(x), float64(y))
	x2, y2 := r.state.transform.TransformPoint(float64(x+w), float64(y+h))

	srcRect := NewRect(0, 0, float64(w), float64(h))
	dstRect := NewRectFromPoints(x1, y1, x2, y2)

	r.commands = append(r.commands, DrawImageCommand{
		Image:   imageRef,
		SrcRect: srcRect,
		DstRect: dstRect,
		Options: DefaultImageOptions(),
	})
}

// DrawImageAnchored draws an image with an anchor point.
func (r *Recorder) DrawImageAnchored(img image.Image, x, y int, ax, ay float64) {
	if img == nil {
		return
	}

	bounds := img.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()

	// Adjust position based on anchor
	drawX := float64(x) - float64(w)*ax
	drawY := float64(y) - float64(h)*ay

	imageRef := r.resources.AddImage(img)

	// Transform destination rectangle
	x1, y1 := r.state.transform.TransformPoint(drawX, drawY)
	x2, y2 := r.state.transform.TransformPoint(drawX+float64(w), drawY+float64(h))

	srcRect := NewRect(0, 0, float64(w), float64(h))
	dstRect := NewRectFromPoints(x1, y1, x2, y2)

	r.commands = append(r.commands, DrawImageCommand{
		Image:   imageRef,
		SrcRect: srcRect,
		DstRect: dstRect,
		Options: DefaultImageOptions(),
	})
}

// DrawImageScaled draws an image scaled to fit the specified rectangle.
func (r *Recorder) DrawImageScaled(img image.Image, x, y, w, h float64) {
	if img == nil {
		return
	}

	bounds := img.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()

	imageRef := r.resources.AddImage(img)

	// Transform destination rectangle
	x1, y1 := r.state.transform.TransformPoint(x, y)
	x2, y2 := r.state.transform.TransformPoint(x+w, y+h)

	srcRect := NewRect(0, 0, float64(srcW), float64(srcH))
	dstRect := NewRectFromPoints(x1, y1, x2, y2)

	r.commands = append(r.commands, DrawImageCommand{
		Image:   imageRef,
		SrcRect: srcRect,
		DstRect: dstRect,
		Options: DefaultImageOptions(),
	})
}

// --------------------------------------------------------------------------
// Text
// --------------------------------------------------------------------------

// SetFont sets the current font face for text drawing.
func (r *Recorder) SetFont(face text.Face) {
	r.state.fontFace = face
}

// SetFontSize sets the current font size in points.
func (r *Recorder) SetFontSize(size float64) {
	r.state.fontSize = size
}

// SetFontFamily sets the current font family name.
func (r *Recorder) SetFontFamily(family string) {
	r.state.fontFamily = family
}

// transformScale returns the uniform scale factor from the current CTM.
// Used to scale font size into world space (matching pre-transformed positions).
func (r *Recorder) transformScale() float64 {
	return math.Sqrt(r.state.transform.A*r.state.transform.A + r.state.transform.B*r.state.transform.B)
}

// DrawString draws text at position (x, y) where y is the baseline.
// Position is transformed to world space. Font size is scaled by the CTM's
// scale factor so that playback in identity space renders at the correct size
// (matching how positions are pre-transformed).
func (r *Recorder) DrawString(s string, x, y float64) {
	px, py := r.state.transform.TransformPoint(x, y)
	brushRef := r.resources.AddBrush(r.state.fillBrush)

	r.commands = append(r.commands, DrawTextCommand{
		Text:       s,
		X:          px,
		Y:          py,
		FontSize:   r.state.fontSize * r.transformScale(),
		FontFamily: r.state.fontFamily,
		Face:       r.state.fontFace,
		Brush:      brushRef,
	})
}

// DrawStringAnchored draws text with an anchor point.
// The anchor offset is computed at record time using text.Measure + face metrics,
// matching Context.DrawStringAnchored exactly. The final baseline position is
// stored in the command (Skia SkTextBlob pattern — pre-positioned glyphs).
func (r *Recorder) DrawStringAnchored(s string, x, y, ax, ay float64) {
	if r.state.fontFace != nil && (ax != 0 || ay != 0) {
		w, _ := text.Measure(s, r.state.fontFace)
		metrics := r.state.fontFace.Metrics()
		h := metrics.Ascent + metrics.Descent
		x -= w * ax
		y = y + metrics.Ascent - ay*h
	}
	r.DrawString(s, x, y)
}

// StrokeString strokes text outlines at position (x, y) where y is the baseline.
// The stroke width, cap, join, and dash are captured from the current recorder state.
func (r *Recorder) StrokeString(s string, x, y float64) {
	px, py := r.state.transform.TransformPoint(x, y)

	brushRef := r.resources.AddBrush(r.state.strokeBrush)

	r.commands = append(r.commands, StrokeTextCommand{
		Text:       s,
		X:          px,
		Y:          py,
		FontSize:   r.state.fontSize * r.transformScale(),
		FontFamily: r.state.fontFamily,
		Face:       r.state.fontFace,
		Brush:      brushRef,
		Stroke:     r.state.currentStroke(),
	})
}

// StrokeStringAnchored strokes text outlines with an anchor point.
// Anchor offset computed at record time, same as DrawStringAnchored.
func (r *Recorder) StrokeStringAnchored(s string, x, y, ax, ay float64) {
	if r.state.fontFace != nil && (ax != 0 || ay != 0) {
		w, _ := text.Measure(s, r.state.fontFace)
		metrics := r.state.fontFace.Metrics()
		h := metrics.Ascent + metrics.Descent
		x -= w * ax
		y = y + metrics.Ascent - ay*h
	}
	r.StrokeString(s, x, y)
}

// MeasureString returns approximate dimensions of text.
// Note: Actual measurement depends on the backend and font.
// Returns (0, 0) if no font is set.
func (r *Recorder) MeasureString(s string) (w, h float64) {
	if r.state.fontFace == nil {
		// Approximate measurement based on font size
		// Average character width is roughly 0.6 * font size
		w = float64(len(s)) * r.state.fontSize * 0.6
		h = r.state.fontSize * 1.2
		return
	}
	return text.Measure(s, r.state.fontFace)
}

// WordWrap wraps text to fit within the given width using word boundaries.
// Uses the current font face for text measurement.
// If no font face is set, returns the input string as a single-element slice.
func (r *Recorder) WordWrap(s string, w float64) []string {
	if r.state.fontFace == nil {
		return []string{s}
	}
	results := text.WrapText(s, r.state.fontFace, w, text.WrapWord)
	lines := make([]string, len(results))
	for i, res := range results {
		lines[i] = res.Text
	}
	return lines
}

// MeasureMultilineString measures text that may contain newlines.
// Returns (width, height) where width is the maximum line width.
// If no font face is set, returns (0, 0).
func (r *Recorder) MeasureMultilineString(s string, lineSpacing float64) (width, height float64) {
	if r.state.fontFace == nil {
		return 0, 0
	}
	lines := recorderSplitLines(s)
	fh := r.state.fontFace.Metrics().LineHeight()
	for _, line := range lines {
		lw, _ := text.Measure(line, r.state.fontFace)
		if lw > width {
			width = lw
		}
	}
	n := float64(len(lines))
	height = n*fh*lineSpacing - (lineSpacing-1)*fh
	return
}

// DrawStringWrapped wraps text to the given width and draws it with alignment.
// Each wrapped line is recorded as a separate DrawText command.
func (r *Recorder) DrawStringWrapped(s string, x, y, ax, ay, width, lineSpacing float64, align text.Alignment) {
	lines := r.WordWrap(s, width)
	if len(lines) == 0 {
		return
	}

	var fh float64
	if r.state.fontFace != nil {
		fh = r.state.fontFace.Metrics().LineHeight()
	} else {
		fh = r.state.fontSize * 1.2
	}

	// Total height (same formula as MeasureMultilineString)
	n := float64(len(lines))
	h := n*fh*lineSpacing - (lineSpacing-1)*fh

	// Adjust starting position by anchor
	x -= ax * width
	y -= ay * h

	// Adjust x base for alignment
	switch align {
	case text.AlignCenter:
		x += width / 2
	case text.AlignRight:
		x += width
	}

	for _, line := range lines {
		drawX := x
		switch align {
		case text.AlignCenter:
			lw, _ := r.MeasureString(line)
			drawX = x - lw/2
		case text.AlignRight:
			lw, _ := r.MeasureString(line)
			drawX = x - lw
		}
		r.DrawString(line, drawX, y)
		y += fh * lineSpacing
	}
}

// recorderSplitLines splits text by line breaks, normalizing \r\n and \r to \n.
func recorderSplitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Split(s, "\n")
}

// --------------------------------------------------------------------------
// Utility Methods
// --------------------------------------------------------------------------

// Clear resets the entire canvas to transparent (zero alpha),
// matching [gg.Context.Clear] semantics. To fill with a specific
// background color, use [Recorder.ClearWithColor].
func (r *Recorder) Clear() {
	r.ClearWithColor(gg.Transparent)
}

// ClearWithColor fills the entire canvas with the specified color.
// This is the recommended way to set a background color before drawing.
func (r *Recorder) ClearWithColor(c gg.RGBA) {
	oldBrush := r.state.fillBrush
	r.state.fillBrush = NewSolidBrush(c)
	r.FillRectangle(0, 0, float64(r.width), float64(r.height))
	r.state.fillBrush = oldBrush
}

// GetCurrentPoint returns the current point of the path.
// Returns (0, 0, false) if there is no current point.
func (r *Recorder) GetCurrentPoint() (x, y float64, ok bool) {
	if r.currentPath == nil || !r.currentPath.HasCurrentPoint() {
		return 0, 0, false
	}
	pt := r.currentPath.CurrentPoint()
	return pt.X, pt.Y, true
}

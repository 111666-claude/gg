package recording

import (
	"github.com/gogpu/gg"
	"github.com/gogpu/gg/text"
)

// maxStateDepth bounds the Save/Restore snapshot stack. A pathological caller
// that issues more Saves than this cannot grow the recorder's memory without
// limit: excess Saves become no-ops instead of panicking. Balanced Save/Restore
// pairs below the limit are recorded and replayed exactly as before.
const maxStateDepth = 1 << 14

// graphicsState is the single, copyable snapshot of every Recorder style,
// transform and font field that Save/Restore must preserve. Keeping the state
// in one struct means adding a new style field is a local change here rather
// than a coordinated edit across Save, Restore and every call site.
//
// The only mutable field is dashPattern; clone takes a defensive copy of it so
// that later SetDash/ClearDash calls can never mutate a frame already on the
// stack (and vice versa). Brush values are shared by reference, matching the
// pre-refactor behavior: brushes are treated as immutable definitions and the
// emitted command stream references them through the ResourcePool.
type graphicsState struct {
	fillBrush   Brush
	strokeBrush Brush

	lineWidth  float64
	lineCap    LineCap
	lineJoin   LineJoin
	miterLimit float64

	dashPattern []float64
	dashOffset  float64

	fillRule  FillRule
	antiAlias bool

	transform Matrix

	fontFace   text.Face
	fontFamily string
	fontSize   float64
}

// defaultGraphicsState returns the initial Recorder state: black fill/stroke,
// 1px line width, butt caps, miter joins, non-zero fill rule, anti-aliasing on
// and the identity transform.
func defaultGraphicsState() graphicsState {
	defaultBrush := NewSolidBrush(gg.Black)
	return graphicsState{
		fillBrush:   defaultBrush,
		strokeBrush: defaultBrush,
		lineWidth:   1.0,
		lineCap:     LineCapButt,
		lineJoin:    LineJoinMiter,
		miterLimit:  4.0,
		fillRule:    FillRuleNonZero,
		antiAlias:   true,
		transform:   Identity(),
	}
}

// clone returns an independent snapshot. The dash pattern is deep-copied; all
// other fields are value types or intentionally shared immutable references
// (brushes, font faces). A nil dash pattern stays nil, preserving the
// solid-line distinction that ClearDash relies on.
func (s graphicsState) clone() graphicsState {
	cp := s
	if s.dashPattern != nil {
		cp.dashPattern = make([]float64, len(s.dashPattern))
		copy(cp.dashPattern, s.dashPattern)
	}
	return cp
}

// currentStroke builds the Stroke value captured by stroke-drawing commands
// from the live snapshot, so every call site observes the same fields.
func (s graphicsState) currentStroke() Stroke {
	return Stroke{
		Width:       s.lineWidth,
		Cap:         s.lineCap,
		Join:        s.lineJoin,
		MiterLimit:  s.miterLimit,
		DashPattern: s.dashPattern,
		DashOffset:  s.dashOffset,
	}
}

package scene

import (
	"math"

	"github.com/gogpu/gg"
)

// Filter applies visual effects to rendered layers.
// Filters are applied during layer pop when LayerFiltered is used.
//
// The filter interface supports both in-place and copy operations:
// - If src == dst, the filter operates in-place when possible
// - Some filters (like blur) may require temporary buffers internally
//
// Bounds handling:
// - Input bounds specify the region to process
// - ExpandBounds returns how much the output grows (e.g., blur radius)
//
// Coordinate convention: when driven by a FilterChain, src and dst always
// share the same local coordinate frame. The chain owns the frame's origin
// (which may be negative in global coordinates) and translates every bounds
// rect before invoking Apply, so a filter only ever sees non-negative pixel
// coordinates relative to the Pixmap it receives.
type Filter interface {
	// Apply processes src pixmap and writes result to dst.
	// bounds specifies the affected region in pixel coordinates.
	// The filter may read pixels outside bounds but only writes within bounds.
	Apply(src, dst *gg.Pixmap, bounds Rect)

	// ExpandBounds returns the expanded bounds after filter application.
	// This is used for buffer allocation:
	// - Blur expands by radius in all directions
	// - Shadow expands by offset + blur radius
	// - Color matrix does not expand
	ExpandBounds(input Rect) Rect
}

// FilterType identifies the type of filter for serialization and debugging.
type FilterType uint8

// Filter type constants.
const (
	// FilterNone represents no filter (identity).
	FilterNone FilterType = iota

	// FilterBlur represents Gaussian blur filter.
	FilterBlur

	// FilterDropShadow represents drop shadow filter.
	FilterDropShadow

	// FilterColorMatrix represents color matrix transformation.
	FilterColorMatrix
)

// String returns a human-readable name for the filter type.
func (ft FilterType) String() string {
	switch ft {
	case FilterNone:
		return "None"
	case FilterBlur:
		return "Blur"
	case FilterDropShadow:
		return "DropShadow"
	case FilterColorMatrix:
		return "ColorMatrix"
	default:
		return unknownStr
	}
}

// ExpandsOutput returns true if this filter type typically expands output bounds.
func (ft FilterType) ExpandsOutput() bool {
	return ft == FilterBlur || ft == FilterDropShadow
}

// FilterChain represents multiple filters applied in sequence.
// Filters are applied in order from first to last.
//
// Buffer management: multi-filter runs borrow temporary Pixmaps from a pool
// (see filter_pool.go). All temporary buffers share one coordinate frame —
// the union of every per-step bounds — wrapped in an internal filterContext
// carrying origin, extent and stride. Filters therefore always read and write
// in local coordinates; ExpandBounds is used purely for geometry, never to
// rebase pixel offsets. Borrowed buffers are returned to the pool even when a
// filter panics, and the chain retains no reference between runs.
type FilterChain struct {
	filters []Filter

	// bufPool supplies temporary pixmaps. When nil, the process-wide
	// defaultFilterPixmapPool is used. Tests may inject a counting pool.
	bufPool filterPixmapPool
}

// NewFilterChain creates a new filter chain from the given filters.
func NewFilterChain(filters ...Filter) *FilterChain {
	chain := &FilterChain{
		filters: make([]Filter, 0, len(filters)),
	}
	for _, f := range filters {
		if f != nil {
			chain.filters = append(chain.filters, f)
		}
	}
	return chain
}

// Add appends a filter to the chain.
func (fc *FilterChain) Add(f Filter) {
	if f != nil {
		fc.filters = append(fc.filters, f)
	}
}

// setPool installs the temporary-buffer pool used by multi-filter runs.
// Intended for tests and dedicated render pipelines; pass nil to restore the
// default pool.
func (fc *FilterChain) setPool(p filterPixmapPool) {
	fc.bufPool = p
}

// Apply processes src through all filters in sequence.
//
// Coordinate model: bounds is expressed in the global frame of src and dst,
// which are assumed to share an origin of (0, 0). The chain computes the
// geometric bounds after every filter via ExpandBounds, derives one common
// origin from their union, and runs every filter against pooled temporary
// pixmaps in that common local frame. The final result is blitted back into
// dst at the correct global offset, clipped to dst's extent.
//
// Nil src/dst and empty bounds are no-ops; no temporary buffer is allocated.
// If a filter panics, all borrowed buffers are still returned to the pool and
// the panic is re-raised to the caller.
func (fc *FilterChain) Apply(src, dst *gg.Pixmap, bounds Rect) {
	if src == nil || dst == nil {
		return
	}
	if bounds.IsEmpty() {
		return
	}

	if len(fc.filters) == 0 {
		// No filters - copy src to dst if different.
		if src != dst {
			copyPixmap(src, dst, bounds)
		}
		return
	}

	if len(fc.filters) == 1 {
		// Single filter - direct application, preserving the legacy path so
		// in-place Apply (src == dst) keeps working without copies.
		fc.filters[0].Apply(src, dst, bounds)
		return
	}

	fc.applyMulti(src, dst, bounds)
}

// maxInlineFilterSteps is the largest chain whose per-step bookkeeping fits
// in stack-allocated fixed arrays. Longer chains fall back to pooled slices,
// so the common case (a handful of filters) performs no slice allocations.
const maxInlineFilterSteps = 16

// applyMulti runs a chain of two or more filters through pooled temporary
// contexts that share one coordinate frame.
//
// # Coordinate model
//
// Every per-step bounds rect is expressed in the caller's global frame, where
// src and dst live at origin (0,0). The temporary frame gets a single origin:
//
//	origin = floor(union of all step bounds) when that minimum is negative,
//	         otherwise (0,0)
//
// When every step bounds is non-negative the origin is (0,0), so the temporary
// frame is the global frame and the whole run reproduces the pre-refactor
// topology exactly: filters ping-pong between two pooled buffers and the final
// filter writes straight into dst, keeping output pixel-identical to the
// legacy implementation (edge clamping, clipping and all).
//
// When a step bounds reaches into negative global coordinates, the origin
// shifts to the floored union minimum so every filter still works in
// non-negative local coordinates. The legacy code indexed those negative
// coordinates directly and silently dropped the pixels; here the first filter
// reads through a pooled source window covering the full frame (transparent
// where src doesn't reach), and the finished frame is blitted back into dst at
// its global offset, clipped to dst's extent.
//
// ExpandBounds is used purely for geometry — deciding the frame origin, extent
// and each filter's local bounds — never to rebase pixel offsets by hand.
func (fc *FilterChain) applyMulti(src, dst *gg.Pixmap, bounds Rect) {
	n := len(fc.filters)

	// Step 1: geometry only. ExpandBounds describes how each filter grows the
	// affected region; it never influences pixel offsets.
	var stepBounds [maxInlineFilterSteps + 1]Rect
	var stepBoundsHeap []Rect
	steps := stepBounds[:0]
	if n+1 > len(stepBounds) {
		stepBoundsHeap = getStepBoundsSlice(n + 1)
		defer putStepBoundsSlice(stepBoundsHeap)
		steps = stepBoundsHeap[:0]
	}
	steps = append(steps, bounds)
	for _, f := range fc.filters {
		steps = append(steps, f.ExpandBounds(steps[len(steps)-1]))
	}

	// Step 2: the union of every step bounds determines the frame extent.
	// The frame origin is the floored union minimum. When it is non-negative
	// every filter can work directly in the caller's global frame (origin 0);
	// when it is negative the frame shifts to that origin so no filter ever
	// receives a negative local coordinate.
	union := bounds
	for _, b := range steps[1:] {
		union = union.Union(b)
	}
	rawOriginX := int(mathFloor32(union.MinX))
	rawOriginY := int(mathFloor32(union.MinY))
	shifted := rawOriginX < 0 || rawOriginY < 0
	originX, originY := 0, 0
	if shifted {
		originX, originY = rawOriginX, rawOriginY
	}

	// Buffer dimensions follow the pre-refactor convention: integer truncation
	// of the union's width/height plus one pixel of border, preserving the
	// edge-clamping behavior of existing filters.
	w := int(union.Width()) + 1
	h := int(union.Height()) + 1
	const maxDim = 1 << 30
	if w <= 0 || h <= 0 || w > maxDim || h > maxDim {
		return
	}

	pool := fc.pool()

	if !shifted {
		fc.runGlobalFrame(pool, src, dst, steps, w, h)
		return
	}
	fc.runLocalFrame(pool, src, dst, steps, w, h, originX, originY)
}

// runGlobalFrame is the zero-origin topology: temporary buffers share the
// caller's global frame, so filters receive the step bounds unchanged. The
// first n-1 filters ping-pong between two pooled buffers and the last filter
// writes directly into dst, exactly as the pre-refactor chain did. In-place
// Apply (src == dst) routes the final write through the idle second buffer to
// avoid read/write aliasing.
func (fc *FilterChain) runGlobalFrame(
	pool filterPixmapPool, src, dst *gg.Pixmap, steps []Rect, w, h int,
) {
	n := len(fc.filters)
	tempA := pool.Get(w, h)
	tempB := pool.Get(w, h)
	defer func() {
		pool.Put(tempA)
		pool.Put(tempB)
	}()

	fc.filters[0].Apply(src, tempA, steps[0])

	if n == 2 {
		fc.filters[1].Apply(tempA, dst, steps[1])
		return
	}

	current, next := tempA, tempB
	for i := 1; i < n-1; i++ {
		fc.filters[i].Apply(current, next, steps[i])
		current, next = next, current
	}

	finalFilter := fc.filters[n-1]
	if src != dst {
		finalFilter.Apply(current, dst, steps[n-1])
		return
	}

	// In-place: stage the final output in the idle ping-pong buffer, then blit
	// the expanded region back into the caller's pixmap.
	finalFilter.Apply(current, next, steps[n-1])
	nextCtx := filterContext{pixmap: next, origin: imagePoint{}, extent: imagePoint{X: next.Width(), Y: next.Height()}, stride: next.Width() * 4}
	dstCtx := filterContext{pixmap: dst, origin: imagePoint{}, extent: imagePoint{X: dst.Width(), Y: dst.Height()}, stride: dst.Width() * 4}
	blitContexts(nextCtx, dstCtx, steps[n])
}

// runLocalFrame is the negative-coordinate topology. All pooled buffers share
// the frame origin (originX, originY); step bounds are translated so filters
// only see non-negative local coordinates. The caller's source is copied into
// a pooled source window covering the full frame — transparent where src does
// not reach — and the finished frame is blitted back into dst at its global
// offset, clipped to dst's extent.
func (fc *FilterChain) runLocalFrame(
	pool filterPixmapPool, src, dst *gg.Pixmap, steps []Rect,
	w, h, originX, originY int,
) {
	n := len(fc.filters)
	tempA := pool.Get(w, h)
	tempB := pool.Get(w, h)
	srcWindow := pool.Get(w, h)
	defer func() {
		pool.Put(tempA)
		pool.Put(tempB)
		pool.Put(srcWindow)
	}()

	origin := imagePoint{X: originX, Y: originY}
	ctxA := newFilterContext(tempA, origin)
	ctxB := newFilterContext(tempB, origin)
	winCtx := newFilterContext(srcWindow, origin)

	// The window starts cleared (pool contract); copy only overlapping src
	// pixels so out-of-reach local positions stay transparent.
	winCtx.fillWindow(srcWindow, src)
	fc.filters[0].Apply(srcWindow, tempA, ctxA.toLocal(steps[0]))

	current, next := ctxA, ctxB
	for i := 1; i < n; i++ {
		fc.filters[i].Apply(current.pixmap, next.pixmap, next.toLocal(steps[i]))
		current, next = next, current
	}

	dstCtx := filterContext{pixmap: dst, origin: imagePoint{}, extent: imagePoint{X: dst.Width(), Y: dst.Height()}, stride: dst.Width() * 4}
	finalBounds := steps[n]
	if src != dst {
		blitContexts(current, dstCtx, finalBounds)
		return
	}

	// In-place: stage through an independent same-size snapshot so the final
	// blit never reads and writes the caller's pixmap at once.
	snapshot := pool.Get(w, h)
	defer pool.Put(snapshot)
	snapCtx := newFilterContext(snapshot, origin)
	blitContexts(current, snapCtx, finalBounds)
	blitContexts(snapCtx, dstCtx, finalBounds)
}

// ExpandBounds returns the combined expansion of all filters.
func (fc *FilterChain) ExpandBounds(input Rect) Rect {
	result := input
	for _, f := range fc.filters {
		result = f.ExpandBounds(result)
	}
	return result
}

// Len returns the number of filters in the chain.
func (fc *FilterChain) Len() int {
	return len(fc.filters)
}

// IsEmpty returns true if the chain has no filters.
func (fc *FilterChain) IsEmpty() bool {
	return len(fc.filters) == 0
}

// copyPixmap copies pixels from src to dst within bounds, clipping the region
// to the extent of both pixmaps. Negative bounds are handled safely.
func copyPixmap(src, dst *gg.Pixmap, bounds Rect) {
	blitContexts(
		filterContext{pixmap: src, origin: imagePoint{}, extent: imagePoint{X: src.Width(), Y: src.Height()}, stride: src.Width() * 4},
		filterContext{pixmap: dst, origin: imagePoint{}, extent: imagePoint{X: dst.Width(), Y: dst.Height()}, stride: dst.Width() * 4},
		bounds,
	)
}

// blitContexts copies the pixels of srcCtx overlapping globalRect into dstCtx
// at the matching global position. The two contexts may use different origins
// and extents; each global pixel is mapped to its own local coordinate in each
// context. The copied region is intersected with both extents so no Pixmap is
// ever indexed out of bounds, and a zero-area intersection is a no-op.
func blitContexts(srcCtx, dstCtx filterContext, globalRect Rect) {
	// Global integer window: floor the minima, ceil the maxima for full
	// pixel coverage.
	gMinX := int(mathFloor32(globalRect.MinX))
	gMinY := int(mathFloor32(globalRect.MinY))
	gMaxX := int(mathCeil32(globalRect.MaxX))
	gMaxY := int(mathCeil32(globalRect.MaxY))

	// Intersect with the source's global coverage.
	sMinX := clampToRange(gMinX-srcCtx.origin.X, 0, srcCtx.extent.X)
	sMinY := clampToRange(gMinY-srcCtx.origin.Y, 0, srcCtx.extent.Y)
	sMaxX := clampToRange(gMaxX-srcCtx.origin.X, 0, srcCtx.extent.X)
	sMaxY := clampToRange(gMaxY-srcCtx.origin.Y, 0, srcCtx.extent.Y)
	if sMinX >= sMaxX || sMinY >= sMaxY {
		return
	}

	// Map the source window's global corners into destination-local coords.
	// Both iterations advance in lockstep, so differing origins are respected
	// without assuming the local rectangles coincide.
	dStartX := (sMinX + srcCtx.origin.X) - dstCtx.origin.X
	dStartY := (sMinY + srcCtx.origin.Y) - dstCtx.origin.Y

	// Clip the run length against the destination extent.
	w := sMaxX - sMinX
	h := sMaxY - sMinY
	if dStartX < 0 {
		sMinX -= dStartX
		w += dStartX
		dStartX = 0
	}
	if dStartY < 0 {
		sMinY -= dStartY
		h += dStartY
		dStartY = 0
	}
	if dStartX+w > dstCtx.extent.X {
		w = dstCtx.extent.X - dStartX
	}
	if dStartY+h > dstCtx.extent.Y {
		h = dstCtx.extent.Y - dStartY
	}
	if w <= 0 || h <= 0 {
		return
	}

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dstCtx.pixmap.SetPixel(dStartX+x, dStartY+y,
				srcCtx.pixmap.GetPixel(sMinX+x, sMinY+y))
		}
	}
}

// mathFloor32 / mathCeil32 wrap the standard math package for float32 values.
func mathFloor32(v float32) float32 {
	return float32(math.Floor(float64(v)))
}

func mathCeil32(v float32) float32 {
	return float32(math.Ceil(float64(v)))
}

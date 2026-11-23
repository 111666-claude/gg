package scene

import (
	"sync"

	"github.com/gogpu/gg"
)

// maxChainTempBytes bounds the memory retained by the FilterChain scratch
// pixmap pool. Pixmaps above this size are freed instead of pooled, so one
// oversized frame cannot pin large buffers for the process lifetime.
const maxChainTempBytes = 16 * 1024 * 1024 // 16 MiB of RGBA8 (4 Mi-pixel)

// chainPixmapPool reuses destination-sized scratch pixmaps across chain
// applications. It stores *gg.Pixmap directly, so after warmup the hot path
// allocates nothing. A size mismatch frees the pooled pixmap and allocates a
// fresh one.
var chainPixmapPool sync.Pool

// acquireChainPixmap returns a cleared pixmap of exactly w*h, from the pool
// when one of matching size is available. Scratch pixmaps must always start
// transparent: a fresh allocation is zeroed by Go, and pooled ones are
// cleared explicitly so no residue from a previous frame survives.
func acquireChainPixmap(w, h int) *gg.Pixmap {
	if v := chainPixmapPool.Get(); v != nil {
		pm := v.(*gg.Pixmap)
		if pm.Width() == w && pm.Height() == h {
			pm.Clear(gg.Transparent)
			return pm
		}
	}
	// gg.NewPixmap is backed by a zeroed byte slice.
	return gg.NewPixmap(w, h)
}

// releaseChainPixmap returns a scratch pixmap to the pool unless it exceeds
// the memory cap, in which case it is left for garbage collection.
func releaseChainPixmap(pm *gg.Pixmap) {
	if pm == nil {
		return
	}
	if int64(pm.Width())*int64(pm.Height())*4 > maxChainTempBytes {
		return
	}
	chainPixmapPool.Put(pm)
}

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
type FilterChain struct {
	filters []Filter
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

// Apply processes src through all filters in sequence.
// For chains with more than one filter, temporary buffers are used.
//
// Every stage receives the bounds produced by the previous stage's
// ExpandBounds, so filters that grow their output (e.g. Gaussian blur) keep
// their full halo through the chain. Temporary pixmaps share the destination's
// coordinate system and dimensions, so non-zero bounds origins work the same
// as origin-zero ones; the scratch pixmaps are drawn from a size-capped pool.
func (fc *FilterChain) Apply(src, dst *gg.Pixmap, bounds Rect) {
	if src == nil || dst == nil {
		return
	}
	if len(fc.filters) == 0 {
		// No filters - copy src to dst if different
		if src != dst {
			copyPixmap(src, dst, bounds)
		}
		return
	}

	if len(fc.filters) == 1 {
		// Single filter - direct application
		fc.filters[0].Apply(src, dst, bounds)
		return
	}

	// Multiple filters - alternate between two destination-sized scratch
	// pixmaps. Same dimensions and coordinate system as dst means every
	// stage (including expanded bounds and non-zero origins) is handled by
	// the filter's own edge clamping.
	temp1 := acquireChainPixmap(dst.Width(), dst.Height())
	temp2 := acquireChainPixmap(dst.Width(), dst.Height())
	defer func() {
		releaseChainPixmap(temp1)
		releaseChainPixmap(temp2)
	}()

	currentBounds := bounds
	current := src

	for i, f := range fc.filters {
		// Each stage processes the bounds accumulated by every ExpandBounds
		// before it, so the first blur's halo is visible to the second.
		stageBounds := currentBounds
		currentBounds = f.ExpandBounds(currentBounds)

		if i == len(fc.filters)-1 {
			f.Apply(current, dst, stageBounds)
			continue
		}

		next := temp1
		if i%2 == 1 {
			next = temp2
		}
		f.Apply(current, next, stageBounds)
		current = next
	}
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

// copyPixmap copies pixels from src to dst within bounds.
func copyPixmap(src, dst *gg.Pixmap, bounds Rect) {
	minX := int(bounds.MinX)
	maxX := int(bounds.MaxX)
	minY := int(bounds.MinY)
	maxY := int(bounds.MaxY)

	// Clamp to pixmap dimensions
	if minX < 0 {
		minX = 0
	}
	if minY < 0 {
		minY = 0
	}
	if maxX > src.Width() {
		maxX = src.Width()
	}
	if maxY > src.Height() {
		maxY = src.Height()
	}
	if maxX > dst.Width() {
		maxX = dst.Width()
	}
	if maxY > dst.Height() {
		maxY = dst.Height()
	}

	for y := minY; y < maxY; y++ {
		for x := minX; x < maxX; x++ {
			dst.SetPixel(x, y, src.GetPixel(x, y))
		}
	}
}

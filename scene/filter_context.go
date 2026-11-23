package scene

import (
	"github.com/gogpu/gg"
)

// filterContext binds a Pixmap to the global coordinate frame used by a
// FilterChain run. Every temporary buffer in a chain shares one common
// origin, so every Filter reads and writes in the same local coordinate
// system: local (x, y) maps to global (origin.X+x, origin.Y+y).
//
// The fields mirror the classic pixel-buffer descriptor (origin/extent/stride):
//   - origin is the global coordinate of pixel (0, 0) and may be negative;
//   - extent is the pixel size of the backing Pixmap;
//   - stride is the number of bytes between two rows (extent.W*4 for RGBA).
//
// A filterContext never owns the Pixmap: the FilterChain borrows buffers from
// the pixmap pool and returns them when the run completes, so no reference to
// a previous frame's pixels is retained.
type filterContext struct {
	pixmap *gg.Pixmap
	origin imagePoint // global coordinate of local (0, 0)
	extent imagePoint // pixel width/height of pixmap
	stride int        // bytes per row
}

// imagePoint is an integer point in global pixel coordinates.
type imagePoint struct {
	X, Y int
}

// newFilterContext wraps pixmap and records the global coordinate of its
// top-left pixel. The pixmap must be non-nil with positive dimensions.
func newFilterContext(pixmap *gg.Pixmap, origin imagePoint) filterContext {
	return filterContext{
		pixmap: pixmap,
		origin: origin,
		extent: imagePoint{X: pixmap.Width(), Y: pixmap.Height()},
		stride: pixmap.Width() * 4,
	}
}

// toLocal converts a global-space Rect to local pixel coordinates.
// Conversion truncates toward zero, matching the integer-coordinate
// convention used by the existing Filter implementations.
func (c filterContext) toLocal(global Rect) Rect {
	return Rect{
		MinX: global.MinX - float32(c.origin.X),
		MinY: global.MinY - float32(c.origin.Y),
		MaxX: global.MaxX - float32(c.origin.X),
		MaxY: global.MaxY - float32(c.origin.Y),
	}
}

// fillWindow populates window — a pooled pixmap covering this context's full
// extent — with the caller's source pixels where the source overlaps and
// leaves the rest transparent. The window shares c's origin, so every local
// coordinate indexes the same global pixel as the pooled temporary contexts.
// Pixels outside src's extent remain zero, reproducing Pixmap.GetPixel's
// transparent out-of-bounds semantics. The caller borrows window from the
// filter pool, so this path allocates nothing on a warm pool.
func (c filterContext) fillWindow(window, src *gg.Pixmap) *gg.Pixmap {
	// Overlap between src's global extent [0,srcW)x[0,srcH) and the context's
	// global extent, expressed in local window coordinates.
	minX := clampToRange(-c.origin.X, 0, c.extent.X)
	minY := clampToRange(-c.origin.Y, 0, c.extent.Y)
	maxX := clampToRange(src.Width()-c.origin.X, 0, c.extent.X)
	maxY := clampToRange(src.Height()-c.origin.Y, 0, c.extent.Y)
	for y := minY; y < maxY; y++ {
		for x := minX; x < maxX; x++ {
			window.SetPixel(x, y, src.GetPixel(x+c.origin.X, y+c.origin.Y))
		}
	}
	return window
}

// clampToRange clamps v into [lo, hi].
func clampToRange(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

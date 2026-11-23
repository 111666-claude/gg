package scene

import (
	"sync"

	"github.com/gogpu/gg"
)

// maxPooledPixmapPixels bounds the pixel count of pixmaps kept in the filter
// buffer pool. Larger buffers are freed instead of retained so a single
// oversized filter run cannot pin a huge allocation for the process lifetime.
const maxPooledPixmapPixels = 4096 * 4096

// filterPixmapPool lends temporary *gg.Pixmap buffers to a FilterChain run.
// Borrowed buffers are returned through Put, which also severs the chain's
// reference: a FilterChain never holds pixels from a previous frame.
type filterPixmapPool interface {
	// Get returns a transparent pixmap of at least w*h pixels.
	// w and h must be positive.
	Get(w, h int) *gg.Pixmap
	// Put returns a pixmap previously obtained from Get. Nil is ignored.
	Put(pm *gg.Pixmap)
}

// syncPixmapPool is the default filterPixmapPool, backed by a sync.Pool.
// A returned pixmap is reused only when it matches the requested dimensions
// exactly; otherwise the stale buffer is dropped and a fresh one allocated.
// Every borrowed buffer is cleared to transparent so filters cannot observe
// pixels left behind by an earlier frame.
type syncPixmapPool struct {
	pool sync.Pool // *gg.Pixmap
}

// defaultFilterPixmapPool is shared by all FilterChain values that do not
// configure a dedicated pool.
var defaultFilterPixmapPool filterPixmapPool = &syncPixmapPool{}

func (p *syncPixmapPool) Get(w, h int) *gg.Pixmap {
	if v := p.pool.Get(); v != nil {
		pm := v.(*gg.Pixmap)
		if pm.Width() == w && pm.Height() == h {
			pm.Clear(gg.Transparent)
			return pm
		}
		// Wrong dimensions: let the stale buffer be collected.
	}
	return gg.NewPixmap(w, h)
}

func (p *syncPixmapPool) Put(pm *gg.Pixmap) {
	if pm == nil {
		return
	}
	// Retain only reasonably sized buffers to bound pool memory.
	if pm.Width()*pm.Height() > maxPooledPixmapPixels {
		return
	}
	p.pool.Put(pm)
}

// pool returns the configured pool for this chain, falling back to the
// process-wide default pool. The fallback keeps the zero-value FilterChain
// usable and preserves the NewFilterChain constructor signature.
func (fc *FilterChain) pool() filterPixmapPool {
	if fc.bufPool != nil {
		return fc.bufPool
	}
	return defaultFilterPixmapPool
}

// stepBoundsPool reuses the per-step bounds slices of long filter chains.
// Chains up to maxInlineFilterSteps use stack arrays and never touch this pool.
var stepBoundsPool = sync.Pool{
	New: func() any {
		s := make([]Rect, 0, 64)
		return &s
	},
}

func getStepBoundsSlice(capHint int) []Rect {
	s := stepBoundsPool.Get().(*[]Rect)
	if cap(*s) < capHint {
		*s = make([]Rect, 0, capHint)
	}
	return (*s)[:0]
}

func putStepBoundsSlice(s []Rect) {
	if cap(s) < 64 {
		return
	}
	ss := s[:0]
	stepBoundsPool.Put(&ss)
}

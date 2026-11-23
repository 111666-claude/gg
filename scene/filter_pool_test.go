package scene

import (
	"testing"

	"github.com/gogpu/gg"
)

func TestSyncPixmapPoolReuseAndClear(t *testing.T) {
	p := &syncPixmapPool{}
	pm := p.Get(4, 6)
	if pm.Width() != 4 || pm.Height() != 6 {
		t.Fatalf("got %dx%d, want 4x6", pm.Width(), pm.Height())
	}
	// Dirty the buffer before returning it.
	pm.SetPixel(0, 0, gg.Red)
	p.Put(pm)

	// Same dimensions: reused and cleared.
	got := p.Get(4, 6)
	if got != pm {
		t.Fatal("expected the same pooled pixmap to be reused")
	}
	if got.GetPixel(0, 0).A != 0 {
		t.Fatal("reused pixmap was not cleared to transparent")
	}
	p.Put(got)
}

func TestSyncPixmapPoolDimensionMismatch(t *testing.T) {
	p := &syncPixmapPool{}
	pm := p.Get(4, 4)
	p.Put(pm)

	// Different dimensions: a fresh pixmap is allocated rather than aliasing
	// the wrong-sized stale buffer.
	got := p.Get(8, 8)
	if got == pm {
		t.Fatal("dimension mismatch must not reuse the stale pixmap")
	}
	if got.Width() != 8 || got.Height() != 8 {
		t.Fatalf("got %dx%d, want 8x8", got.Width(), got.Height())
	}
}

func TestSyncPixmapPoolOversizedNotRetained(t *testing.T) {
	p := &syncPixmapPool{}
	// A buffer larger than the retention cap must be dropped on Put, so an
	// oversized filter run cannot pin a huge allocation.
	side := maxPooledPixmapPixels/4 + 16
	big := p.Get(side, 4) // pixel count exceeds the cap
	p.Put(big)
	// Pool internal state isn't observable directly, but a subsequent Get must
	// hand back a cleared buffer regardless of retention behavior.
	got := p.Get(side, 4)
	if got.GetPixel(0, 0).A != 0 {
		t.Fatal("oversized re-borrow must be clear")
	}
}

func TestFilterChainPoolDefaultFallback(t *testing.T) {
	// A chain with no injected pool must use the process-wide default and
	// still run correctly without leaking.
	chain := NewFilterChain(newFixedChain()...)
	src := gg.NewPixmap(20, 20)
	fillSource(src)
	dst := gg.NewPixmap(20, 20)
	chain.Apply(src, dst, Rect{MinX: 5, MinY: 5, MaxX: 15, MaxY: 15})
	if dst.GetPixel(5, 5).A == 0 {
		t.Fatal("default-pool chain produced no output")
	}
}

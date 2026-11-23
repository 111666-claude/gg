package scene

import (
	"runtime"
	"sync"
	"testing"

	"github.com/gogpu/gg"
)

// growFilter is an expanding test filter. It enlarges the output bounds by
// pad pixels on every side, fills newly covered pixels with a unique color
// derived from their global position, and tints existing pixels so each
// filter's contribution is distinguishable in the final output.
type growFilter struct {
	name string
	pad  float32
	tint float64 // added (clamped) to the red channel of carried pixels
}

func (f *growFilter) ExpandBounds(input Rect) Rect {
	return Rect{
		MinX: input.MinX - f.pad,
		MinY: input.MinY - f.pad,
		MaxX: input.MaxX + f.pad,
		MaxY: input.MaxY + f.pad,
	}
}

func (f *growFilter) Apply(src, dst *gg.Pixmap, bounds Rect) {
	if src == nil || dst == nil {
		return
	}
	// bounds is the output region in the shared local frame.
	minX := clampToRange(int(bounds.MinX), 0, dst.Width())
	minY := clampToRange(int(bounds.MinY), 0, dst.Height())
	maxX := clampToRange(int(bounds.MaxX), 0, dst.Width())
	maxY := clampToRange(int(bounds.MaxY), 0, dst.Height())
	for y := minY; y < maxY; y++ {
		for x := minX; x < maxX; x++ {
			c := src.GetPixel(x, y) // transparent outside src extent
			if c.A == 0 {
				// Newly exposed border: deterministic, position-dependent.
				dst.SetPixel(x, y, gg.RGBA2(0.1, 0.9, float64((x+y)%7)/7.0, 1))
				continue
			}
			r := c.R + f.tint
			if r > 1 {
				r = 1
			}
			dst.SetPixel(x, y, gg.RGBA2(r, c.G, c.B, c.A))
		}
	}
}

// passthroughFilter is a non-expanding filter: ExpandBounds is the identity.
// It shifts the green channel slightly so its presence is visible in pixels
// while leaving geometry untouched.
type passthroughFilter struct {
	shift float64
}

func (f *passthroughFilter) ExpandBounds(input Rect) Rect {
	return input
}

func (f *passthroughFilter) Apply(src, dst *gg.Pixmap, bounds Rect) {
	if src == nil || dst == nil {
		return
	}
	minX := clampToRange(int(bounds.MinX), 0, dst.Width())
	minY := clampToRange(int(bounds.MinY), 0, dst.Height())
	maxX := clampToRange(int(bounds.MaxX), 0, dst.Width())
	maxY := clampToRange(int(bounds.MaxY), 0, dst.Height())
	for y := minY; y < maxY; y++ {
		for x := minX; x < maxX; x++ {
			c := src.GetPixel(x, y)
			g := c.G + f.shift
			if g > 1 {
				g = 1
			}
			dst.SetPixel(x, y, gg.RGBA2(c.R, g, c.B, c.A))
		}
	}
}

// recordingFilter wraps a Filter and records the bounds handed to every Apply
// call, allowing tests to verify per-step local bounds propagation.
type recordingFilter struct {
	inner      Filter
	applyRects []Rect
}

func (r *recordingFilter) Apply(src, dst *gg.Pixmap, bounds Rect) {
	r.applyRects = append(r.applyRects, bounds)
	r.inner.Apply(src, dst, bounds)
}

func (r *recordingFilter) ExpandBounds(input Rect) Rect {
	return r.inner.ExpandBounds(input)
}

// countingPool is an injectable filterPixmapPool that records borrow/return
// activity and retains returned buffers, so tests can assert reuse and
// reference lifetimes deterministically (sync.Pool may drop entries at GC).
type countingPool struct {
	mu        sync.Mutex
	gets      int
	puts      int
	active    int
	peak      int
	retained  []*gg.Pixmap
	maxPixels int // >0: reject pooling of larger buffers
}

func (p *countingPool) Get(w, h int) *gg.Pixmap {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.gets++
	p.active++
	if p.active > p.peak {
		p.peak = p.active
	}
	for i, pm := range p.retained {
		if pm != nil && pm.Width() == w && pm.Height() == h {
			p.retained = append(p.retained[:i], p.retained[i+1:]...)
			// Match the production pool contract: borrowed buffers are
			// always handed out cleared.
			pm.Clear(gg.Transparent)
			return pm
		}
	}
	return gg.NewPixmap(w, h)
}

func (p *countingPool) Put(pm *gg.Pixmap) {
	if pm == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.puts++
	p.active--
	if p.maxPixels > 0 && pm.Width()*pm.Height() > p.maxPixels {
		return
	}
	p.retained = append(p.retained, pm)
}

// legacyChainApply reproduces the pre-refactor FilterChain.Apply algorithm
// byte-for-byte (max-bounds temp pixmaps sized w+1/h+1, direct absolute
// indexing). It serves as the golden reference for pixel compatibility on
// chains whose bounds stay non-negative — the only geometry the old code
// could represent correctly.
func legacyChainApply(filters []Filter, src, dst *gg.Pixmap, bounds Rect) {
	maxBounds := bounds
	for _, f := range filters {
		maxBounds = f.ExpandBounds(maxBounds)
	}
	w := int(maxBounds.Width()) + 1
	h := int(maxBounds.Height()) + 1
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	temp1 := gg.NewPixmap(w, h)
	temp2 := gg.NewPixmap(w, h)

	filters[0].Apply(src, temp1, bounds)
	currentBounds := filters[0].ExpandBounds(bounds)

	current := temp1
	next := temp2
	for i := 1; i < len(filters)-1; i++ {
		filters[i].Apply(current, next, currentBounds)
		currentBounds = filters[i].ExpandBounds(currentBounds)
		current, next = next, current
	}
	filters[len(filters)-1].Apply(current, dst, currentBounds)
}

func newFixedChain() []Filter {
	// Two expanding filters with different radii plus one non-expanding.
	return []Filter{
		&growFilter{name: "grow2", pad: 2, tint: 0.2},
		&passthroughFilter{shift: 0.1},
		&growFilter{name: "grow3", pad: 3, tint: 0.3},
	}
}

func fillSource(src *gg.Pixmap) {
	for y := 0; y < src.Height(); y++ {
		for x := 0; x < src.Width(); x++ {
			src.SetPixel(x, y, gg.RGBA2(float64(x)/float64(src.Width()),
				float64(y)/float64(src.Height()), 0.4, 1))
		}
	}
}

func pixmapsEqual(a, b *gg.Pixmap) (bool, string) {
	if a.Width() != b.Width() || a.Height() != b.Height() {
		return false, "dimension mismatch"
	}
	for y := 0; y < a.Height(); y++ {
		for x := 0; x < a.Width(); x++ {
			ca, cb := a.GetPixel(x, y), b.GetPixel(x, y)
			if !colorApproxEqual(ca, cb, 0.02) {
				return false, "first diff at"
			}
		}
	}
	return true, ""
}

// TestFilterChainFixedChainGolden is the headline compatibility test: the
// refactored chain (pooled contexts, shared origin) must produce the same
// pixels as the pre-refactor implementation for a fixed chain of two
// expanding filters and one non-expanding filter.
//
// Inputs are offset so every per-step bounds stays non-negative: both the
// legacy and refactored implementations then share an identical (0,0) origin
// coordinate frame and identical buffer dimensions, which makes a strict
// per-pixel comparison meaningful. Negative-coordinate geometry — where the
// legacy code silently dropped pixels — is covered separately by
// TestFilterChainNegativeOrigin.
func TestFilterChainFixedChainGolden(t *testing.T) {
	filters := newFixedChain()
	// Combined expansion is 5px; start at 5 so the union origin is exactly
	// (0,0) and no step bounds go negative.
	boundsList := []Rect{
		{MinX: 5, MinY: 5, MaxX: 16, MaxY: 12},
		{MinX: 7, MinY: 8, MaxX: 14, MaxY: 10},
		{MinX: 5, MinY: 5, MaxX: 9, MaxY: 9},
	}

	for bi, bounds := range boundsList {
		// Both implementations get identical, independent src/dst buffers so
		// the comparison isolates the chain's buffer/bounds handling.
		newSource := func() *gg.Pixmap {
			s := gg.NewPixmap(28, 24)
			fillSource(s)
			return s
		}

		// Golden: legacy algorithm.
		goldenSrc, goldenDst := newSource(), gg.NewPixmap(28, 24)
		legacyChainApply(filters, goldenSrc, goldenDst, bounds)

		// Refactored.
		pool := &countingPool{}
		chain := NewFilterChain(filters...)
		chain.setPool(pool)
		newSrc, newDst := newSource(), gg.NewPixmap(28, 24)
		chain.Apply(newSrc, newDst, bounds)

		if eq, where := pixmapsEqual(goldenDst, newDst); !eq {
			t.Fatalf("bounds[%d] %+v: pixel mismatch (%s)", bi, bounds, where)
		}

		// Every borrowed buffer must have been returned; none stays active.
		if pool.active != 0 {
			t.Errorf("bounds[%d]: pool active=%d, want 0 (leak)", bi, pool.active)
		}
		// Origin is (0,0) for these cases, so only the two ping-pong buffers
		// are borrowed per run.
		if pool.gets != 2 || pool.puts != 2 {
			t.Errorf("bounds[%d]: gets=%d puts=%d, want 2/2", bi, pool.gets, pool.puts)
		}

		// A re-borrowed buffer of the same size must arrive cleared: filters
		// can never observe pixels left behind by an earlier run.
		if len(pool.retained) == 0 {
			t.Fatalf("bounds[%d]: pool retained no buffers", bi)
		}
		retained := pool.retained[0]
		pm := pool.Get(retained.Width(), retained.Height())
		for i, b := range pm.Data() {
			if b != 0 {
				t.Fatalf("bounds[%d]: borrowed pixmap contains stale frame data at byte %d", bi, i)
			}
		}
		pool.Put(pm)
	}

	// Also verify the chain's reported geometry matches sequential expansion.
	bounds := boundsList[0]
	want := bounds
	for _, f := range filters {
		want = f.ExpandBounds(want)
	}
	if got := NewFilterChain(filters...).ExpandBounds(bounds); got != want {
		t.Errorf("chain ExpandBounds = %+v, want %+v", got, want)
	}
}

// TestFilterChainStepBounds verifies each filter sees the correct translated
// local bounds, and that the chain's reported per-step geometry matches
// sequential ExpandBounds calls.
func TestFilterChainStepBounds(t *testing.T) {
	base := newFixedChain()
	recorded := make([]*recordingFilter, len(base))
	filters := make([]Filter, len(base))
	for i, f := range base {
		recorded[i] = &recordingFilter{inner: f}
		filters[i] = recorded[i]
	}
	chain := NewFilterChain(filters...)

	// Start at 5: the total 5-pixel expansion lands exactly on zero, so the
	// union minimum is non-negative and the chain runs in the global frame.
	bounds := Rect{MinX: 5, MinY: 5, MaxX: 20, MaxY: 17}
	// Global bounds entering each filter: filter i receives stepBounds[i]
	// (the chain's input for step i), translated into the shared local frame.
	inputGlobal := []Rect{
		bounds,
		base[0].ExpandBounds(bounds),
		base[1].ExpandBounds(base[0].ExpandBounds(bounds)),
	}
	wantFinal := base[2].ExpandBounds(base[1].ExpandBounds(base[0].ExpandBounds(bounds)))
	if got := chain.ExpandBounds(bounds); got != wantFinal {
		t.Fatalf("chain ExpandBounds = %+v, want %+v", got, wantFinal)
	}

	// Frame origin mirrors the implementation: a non-negative union minimum
	// keeps the origin at (0,0) and filters receive their step bounds
	// unchanged, exactly as the pre-refactor chain passed them.
	originX := max(0, int(mathFloor32(wantFinal.MinX)))
	originY := max(0, int(mathFloor32(wantFinal.MinY)))
	if originX != 0 || originY != 0 {
		t.Fatalf("test setup: expected clamped origin (0,0), got (%d,%d)", originX, originY)
	}

	src := gg.NewPixmap(40, 40)
	fillSource(src)
	dst := gg.NewPixmap(40, 40)
	chain.Apply(src, dst, bounds)

	for i, rf := range recorded {
		if len(rf.applyRects) != 1 {
			t.Fatalf("filter %d applied %d times, want 1", i, len(rf.applyRects))
		}
		// In the global frame (clamped origin 0) filters receive their step
		// bounds unchanged, exactly as the pre-refactor chain passed them.
		got := rf.applyRects[0]
		if got != inputGlobal[i] {
			t.Errorf("filter %d local bounds = %+v, want %+v", i, got, inputGlobal[i])
		}
	}
}

// TestFilterChainLocalFrameBounds exercises a chain whose union minimum is
// strictly negative: every filter must receive non-negative local bounds
// translated by the shared frame origin, and the recorded values must match
// the per-step global bounds minus that origin.
func TestFilterChainLocalFrameBounds(t *testing.T) {
	base := newFixedChain()
	recorded := make([]*recordingFilter, len(base))
	filters := make([]Filter, len(base))
	for i, f := range base {
		recorded[i] = &recordingFilter{inner: f}
		filters[i] = recorded[i]
	}
	chain := NewFilterChain(filters...)

	bounds := Rect{MinX: 1, MinY: 1, MaxX: 10, MaxY: 8}
	steps := []Rect{bounds}
	for _, f := range base {
		steps = append(steps, f.ExpandBounds(steps[len(steps)-1]))
	}
	originX := int(mathFloor32(steps[len(steps)-1].MinX)) // -5
	originY := int(mathFloor32(steps[len(steps)-1].MinY))
	if originX >= 0 || originY >= 0 {
		t.Fatalf("test setup: expected negative origin, got (%d,%d)", originX, originY)
	}

	src := gg.NewPixmap(20, 20)
	fillSource(src)
	dst := gg.NewPixmap(20, 20)
	chain.Apply(src, dst, bounds)

	for i, rf := range recorded {
		if len(rf.applyRects) != 1 {
			t.Fatalf("filter %d applied %d times, want 1", i, len(rf.applyRects))
		}
		got := rf.applyRects[0]
		want := Rect{
			MinX: steps[i].MinX - float32(originX),
			MinY: steps[i].MinY - float32(originY),
			MaxX: steps[i].MaxX - float32(originX),
			MaxY: steps[i].MaxY - float32(originY),
		}
		if got != want {
			t.Errorf("filter %d local bounds = %+v, want %+v", i, got, want)
		}
		if got.MinX < 0 || got.MinY < 0 {
			t.Errorf("filter %d saw negative local coords: %+v", i, got)
		}
	}
}

// TestFilterChainNegativeOrigin checks a chain whose expanded bounds extend
// into negative global coordinates. The old implementation silently dropped
// those pixels; the refactor must place them correctly relative to the origin.
func TestFilterChainNegativeOrigin(t *testing.T) {
	filters := newFixedChain()
	bounds := Rect{MinX: 1, MinY: 1, MaxX: 10, MaxY: 8}

	chain := NewFilterChain(filters...)
	pool := &countingPool{}
	chain.setPool(pool)

	src := gg.NewPixmap(20, 20)
	fillSource(src)
	dst := gg.NewPixmap(20, 20)
	chain.Apply(src, dst, bounds)

	finalBounds := chain.ExpandBounds(bounds)
	if finalBounds.MinX >= 0 || finalBounds.MinY >= 0 {
		t.Fatalf("test setup: expected negative expanded bounds, got %+v", finalBounds)
	}

	// Border pixels introduced by the expanding filters must land in dst at
	// their global positions (e.g. global (0,0) corresponds to the first
	// reachable pixel even though the union origin is (-5,-5)).
	border := dst.GetPixel(0, 0)
	if border.A == 0 {
		t.Error("negative-origin border pixel (0,0) was not written; origin handling broken")
	}
	// Pixels outside the expanded region must remain untouched.
	if c := dst.GetPixel(19, 19); c.A != 0 {
		t.Errorf("pixel outside final bounds modified: %+v", c)
	}
	if pool.active != 0 {
		t.Errorf("pool active=%d, want 0 (negative-origin run leaked a buffer)", pool.active)
	}
	// Non-zero origin requires the extra pooled source-window buffer.
	if pool.gets != 3 || pool.puts != 3 {
		t.Errorf("negative-origin gets=%d puts=%d, want 3/3", pool.gets, pool.puts)
	}
}

// TestFilterChainClipAfterExpand covers a destination smaller than the
// expanded output: the blit must clip without any out-of-bounds write.
func TestFilterChainClipAfterExpand(t *testing.T) {
	filters := newFixedChain()
	chain := NewFilterChain(filters...)
	chain.setPool(&countingPool{})

	bounds := Rect{MinX: 0, MinY: 0, MaxX: 12, MaxY: 12}
	src := gg.NewPixmap(12, 12)
	fillSource(src)
	dst := gg.NewPixmap(6, 6) // deliberately too small
	chain.Apply(src, dst, bounds)

	// All six rows/cols should have received some output; nothing panicked.
	written := 0
	for y := 0; y < 6; y++ {
		for x := 0; x < 6; x++ {
			if dst.GetPixel(x, y).A != 0 {
				written++
			}
		}
	}
	if written == 0 {
		t.Error("clipped destination received no pixels")
	}
}

// TestFilterChainZeroSize covers empty and degenerate bounds: no filter runs,
// no buffer is borrowed.
func TestFilterChainZeroSize(t *testing.T) {
	pool := &countingPool{}
	chain := NewFilterChain(newFixedChain()...)
	chain.setPool(pool)
	src := gg.NewPixmap(8, 8)
	dst := gg.NewPixmap(8, 8)

	for _, bounds := range []Rect{
		{MinX: 3, MinY: 3, MaxX: 3, MaxY: 3}, // inverted/empty
		{MinX: 5, MinY: 5, MaxX: 2, MaxY: 2}, // inverted
		EmptyRect(),
	} {
		chain.Apply(src, dst, bounds)
	}
	if pool.gets != 0 {
		t.Errorf("empty bounds borrowed %d buffers, want 0", pool.gets)
	}
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			if dst.GetPixel(x, y).A != 0 {
				t.Fatalf("empty bounds modified dst at (%d,%d)", x, y)
			}
		}
	}
}

// TestFilterChainSmallDestinationAndNilArgs checks undersized destinations
// on single-filter paths and defensive nil handling.
func TestFilterChainSmallDestinationAndNilArgs(t *testing.T) {
	chain := NewFilterChain(&growFilter{pad: 4, tint: 0.1})
	chain.Apply(nil, gg.NewPixmap(4, 4), Rect{MinX: 0, MinY: 0, MaxX: 4, MaxY: 4})
	chain.Apply(gg.NewPixmap(4, 4), nil, Rect{MinX: 0, MinY: 0, MaxX: 4, MaxY: 4})

	src := gg.NewPixmap(10, 10)
	fillSource(src)
	dst := gg.NewPixmap(3, 3)
	chain.Apply(src, dst, Rect{MinX: 0, MinY: 0, MaxX: 10, MaxY: 10}) // must not panic
}

// panicFilter panics inside Apply to exercise buffer-return safety.
type panicFilter struct{}

func (panicFilter) Apply(src, dst *gg.Pixmap, bounds Rect) { panic("boom") }
func (panicFilter) ExpandBounds(input Rect) Rect           { return input }

// TestFilterChainPanicReturnsBuffers ensures a panicking filter does not leak
// pooled buffers and that the pool remains usable afterwards.
func TestFilterChainPanicReturnsBuffers(t *testing.T) {
	pool := &countingPool{}
	chain := NewFilterChain(&passthroughFilter{}, panicFilter{})
	chain.setPool(pool)

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic to propagate")
			}
		}()
		chain.Apply(gg.NewPixmap(6, 6), gg.NewPixmap(6, 6),
			Rect{MinX: 0, MinY: 0, MaxX: 6, MaxY: 6})
	}()

	if pool.active != 0 {
		t.Errorf("after panic pool active=%d, want 0 (leaked buffers)", pool.active)
	}

	// The pool must still serve a subsequent healthy run.
	healthy := NewFilterChain(newFixedChain()...)
	healthy.setPool(pool)
	src := gg.NewPixmap(10, 10)
	fillSource(src)
	dst := gg.NewPixmap(10, 10)
	healthy.Apply(src, dst, Rect{MinX: 0, MinY: 0, MaxX: 10, MaxY: 10})
	if pool.active != 0 {
		t.Errorf("pool unusable after panic: active=%d", pool.active)
	}
}

// expandingPanicFilter expands bounds (forcing a negative-origin local frame)
// and then panics, exercising buffer-return on the shifted topology.
type expandingPanicFilter struct{}

func (expandingPanicFilter) Apply(src, dst *gg.Pixmap, bounds Rect) { panic("shifted boom") }
func (expandingPanicFilter) ExpandBounds(input Rect) Rect {
	return Rect{MinX: input.MinX - 4, MinY: input.MinY - 4, MaxX: input.MaxX + 4, MaxY: input.MaxY + 4}
}

func TestFilterChainPanicInLocalFrame(t *testing.T) {
	pool := &countingPool{}
	// Expanding first filter drives the union minimum negative; the panic in
	// the second filter happens after all three local-frame buffers are
	// borrowed, so all three must come back.
	chain := NewFilterChain(&growFilter{pad: 2, tint: 0.1}, expandingPanicFilter{})
	chain.setPool(pool)

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic to propagate")
			}
		}()
		chain.Apply(gg.NewPixmap(10, 10), gg.NewPixmap(10, 10),
			Rect{MinX: 1, MinY: 1, MaxX: 8, MaxY: 8})
	}()

	if pool.active != 0 {
		t.Errorf("local-frame panic leaked buffers: active=%d", pool.active)
	}
	if pool.gets != pool.puts || pool.gets == 0 {
		t.Errorf("gets=%d puts=%d, want equal and non-zero", pool.gets, pool.puts)
	}
}

// TestFilterChainPoolReuse verifies that repeated runs reuse the same pooled
// pixmaps, that borrowed buffers are cleared (no previous-frame pixels), and
// that the chain holds no buffer references between runs.
func TestFilterChainPoolReuse(t *testing.T) {
	pool := &countingPool{}
	chain := NewFilterChain(newFixedChain()...)
	chain.setPool(pool)

	// Bounds offset so the total 5-pixel expansion lands exactly at zero: the
	// frame origin is (0,0) and each run borrows exactly two ping-pong buffers.
	bounds := Rect{MinX: 5, MinY: 5, MaxX: 15, MaxY: 15}
	for run := 0; run < 3; run++ {
		src := gg.NewPixmap(24, 24)
		fillSource(src)
		dst := gg.NewPixmap(24, 24)
		chain.Apply(src, dst, bounds)
	}

	// First run allocates two buffers; later runs must reuse both.
	if pool.gets != 6 || pool.puts != 6 {
		t.Errorf("gets=%d puts=%d, want 6/6", pool.gets, pool.puts)
	}
	if len(pool.retained) != 2 {
		t.Errorf("retained buffers=%d, want 2", len(pool.retained))
	}
	// The chain itself must not hold buffer references between runs: all
	// buffers are parked in the pool, and re-borrowing hands back cleared
	// memory rather than previous-frame pixels.
	for _, parked := range pool.retained {
		reb := pool.Get(parked.Width(), parked.Height())
		for i, b := range reb.Data() {
			if b != 0 {
				t.Fatalf("re-borrowed pixmap contains stale frame data at byte %d", i)
			}
		}
		pool.Put(reb)
	}
}

// TestFilterChainPoolReuseDefaultPool exercises the production sync.Pool path
// (no injected pool): after warmup a reused chain runs Apply with zero heap
// allocations at both zero and negative frame origins, and stays race free
// under concurrent use.
func TestFilterChainPoolReuseDefaultPool(t *testing.T) {
	// Allocation accounting uses an injected pool so it is deterministic
	// regardless of when the runtime decides to drain sync.Pool. The pool
	// hands out pre-warmed buffers and the chain must not allocate pixmaps.
	assertZeroAllocs := func(chain *FilterChain, src, dst *gg.Pixmap, bounds Rect) {
		run := func() { chain.Apply(src, dst, bounds) }
		// Internal warmup beyond AllocsPerRun's own single trial.
		for i := 0; i < 3; i++ {
			run()
		}
		if a := testing.AllocsPerRun(10, run); a != 0 {
			t.Errorf("Apply allocs/run = %v, want 0 (fully pooled)", a)
		}
	}

	// Zero frame origin (input offset by the total 5-pixel expansion).
	zeroPool := &countingPool{}
	zeroChain := NewFilterChain(newFixedChain()...)
	zeroChain.setPool(zeroPool)
	zeroSrc := gg.NewPixmap(26, 26)
	fillSource(zeroSrc)
	zeroDst := gg.NewPixmap(26, 26)
	assertZeroAllocs(zeroChain, zeroSrc, zeroDst, Rect{MinX: 5, MinY: 5, MaxX: 17, MaxY: 17})
	if zeroPool.active != 0 {
		t.Errorf("zero-origin run leaked buffers: active=%d", zeroPool.active)
	}

	// Negative frame origin: the source window is pooled as well.
	negPool := &countingPool{}
	negChain := NewFilterChain(newFixedChain()...)
	negChain.setPool(negPool)
	negSrc := gg.NewPixmap(26, 26)
	fillSource(negSrc)
	negDst := gg.NewPixmap(26, 26)
	assertZeroAllocs(negChain, negSrc, negDst, Rect{MinX: 1, MinY: 1, MaxX: 12, MaxY: 12})
	if negPool.active != 0 {
		t.Errorf("negative-origin run leaked buffers: active=%d", negPool.active)
	}

	// Concurrent stress on the real global sync.Pool for the race detector.
	// Each goroutine owns its chain and pixmaps; only the global pool is shared.
	var wg sync.WaitGroup
	for i := 0; i < runtime.GOMAXPROCS(0)*2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			chain := NewFilterChain(newFixedChain()...)
			src := gg.NewPixmap(26, 26)
			fillSource(src)
			dst := gg.NewPixmap(26, 26)
			for j := 0; j < 5; j++ {
				chain.Apply(src, dst, Rect{MinX: 5, MinY: 5, MaxX: 17, MaxY: 17})
			}
		}()
	}
	wg.Wait()
}

// TestFilterChainInPlace verifies the in-place Apply contract (src == dst)
// for single filters and multi-filter chains.
func TestFilterChainInPlace(t *testing.T) {
	bounds := Rect{MinX: 0, MinY: 0, MaxX: 10, MaxY: 10}

	// Single filter in place: legacy direct path.
	one := NewFilterChain(&passthroughFilter{shift: 0.2})
	pm := gg.NewPixmap(10, 10)
	fillSource(pm)
	one.Apply(pm, pm, bounds)
	if pm.GetPixel(4, 4).A == 0 {
		t.Fatal("in-place single filter lost pixel data")
	}

	// Multi filter in place: pooled ping-pong, final blit back into pm.
	multi := NewFilterChain(newFixedChain()...)
	pool := &countingPool{}
	multi.setPool(pool)
	pm2 := gg.NewPixmap(16, 16)
	fillSource(pm2)
	multi.Apply(pm2, pm2, bounds)
	if pool.active != 0 {
		t.Errorf("in-place run leaked buffers: active=%d", pool.active)
	}
	if pm2.GetPixel(0, 0).A == 0 {
		t.Error("in-place multi filter produced no output")
	}
}

// TestFilterChainSingleAndEmptyCompatibility locks in the no-filter and
// single-filter behaviors against direct expectations.
func TestFilterChainSingleAndEmptyCompatibility(t *testing.T) {
	bounds := Rect{MinX: 1, MinY: 1, MaxX: 6, MaxY: 6}
	src := gg.NewPixmap(8, 8)
	fillSource(src)

	// Empty chain: copy semantics.
	dst := gg.NewPixmap(8, 8)
	NewFilterChain().Apply(src, dst, bounds)
	for y := 1; y < 6; y++ {
		for x := 1; x < 6; x++ {
			if !colorApproxEqual(src.GetPixel(x, y), dst.GetPixel(x, y), 0.001) {
				t.Fatalf("empty chain copy mismatch at (%d,%d)", x, y)
			}
		}
	}
	if dst.GetPixel(0, 0).A != 0 {
		t.Error("empty chain wrote outside bounds")
	}

	// Single filter: receives the original bounds object unchanged (no
	// origin translation on the direct path).
	rf := &recordingFilter{inner: &passthroughFilter{}}
	chain := NewFilterChain(rf)
	chain.Apply(src, gg.NewPixmap(8, 8), bounds)
	if len(rf.applyRects) != 1 || rf.applyRects[0] != bounds {
		t.Errorf("single filter bounds = %+v, want %+v", rf.applyRects, bounds)
	}
}

// TestFilterChainLongChainHeapFallback forces a chain longer than
// maxInlineFilterSteps so the per-step bounds use the pooled heap slice. It
// must produce output and return every borrowed buffer without leaking.
func TestFilterChainLongChainHeapFallback(t *testing.T) {
	n := maxInlineFilterSteps + 5
	filters := make([]Filter, 0, n)
	for i := 0; i < n; i++ {
		// Mostly non-expanding so the frame stays small; one expansion keeps
		// the multi-filter path meaningful.
		if i == 0 {
			filters = append(filters, &growFilter{pad: 1, tint: 0.05})
		} else {
			filters = append(filters, &passthroughFilter{shift: 0.01})
		}
	}
	pool := &countingPool{}
	chain := NewFilterChain(filters...)
	chain.setPool(pool)

	src := gg.NewPixmap(20, 20)
	fillSource(src)
	dst := gg.NewPixmap(20, 20)
	chain.Apply(src, dst, Rect{MinX: 5, MinY: 5, MaxX: 15, MaxY: 15})

	if pool.active != 0 {
		t.Errorf("long chain leaked buffers: active=%d", pool.active)
	}
	if dst.GetPixel(5, 5).A == 0 {
		t.Error("long chain produced no output")
	}
}

// TestFilterChainTwoFilterGolden covers the n==2 special case against the
// legacy reference, which writes the final filter straight into dst.
func TestFilterChainTwoFilterGolden(t *testing.T) {
	filters := []Filter{
		&growFilter{pad: 2, tint: 0.2},
		&passthroughFilter{shift: 0.1},
	}
	bounds := Rect{MinX: 5, MinY: 5, MaxX: 14, MaxY: 11}
	newSource := func() *gg.Pixmap {
		s := gg.NewPixmap(24, 20)
		fillSource(s)
		return s
	}
	gSrc, gDst := newSource(), gg.NewPixmap(24, 20)
	legacyChainApply(filters, gSrc, gDst, bounds)

	pool := &countingPool{}
	chain := NewFilterChain(filters...)
	chain.setPool(pool)
	nSrc, nDst := newSource(), gg.NewPixmap(24, 20)
	chain.Apply(nSrc, nDst, bounds)

	if eq, where := pixmapsEqual(gDst, nDst); !eq {
		t.Fatalf("two-filter pixel mismatch (%s)", where)
	}
	if pool.gets != 2 || pool.puts != 2 {
		t.Errorf("two-filter gets=%d puts=%d, want 2/2", pool.gets, pool.puts)
	}
}

package recording

import (
	"image"
	"image/color"
	"os"
	"sync"
	"testing"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/text"
)

// --------------------------------------------------------------------------
// Path dedup
// --------------------------------------------------------------------------

func TestResourcePool_DedupPath_Repeated(t *testing.T) {
	pool := NewResourcePool()
	pool.EnableDedup(true)

	makePath := func() *gg.Path {
		p := gg.NewPath()
		p.Rectangle(10, 10, 50, 50)
		p.Circle(30, 30, 10)
		return p
	}

	ref1 := pool.AddPath(makePath())
	for i := 0; i < 100; i++ {
		ref := pool.AddPath(makePath())
		if ref != ref1 {
			t.Fatalf("identical path got ref %d, want %d", ref, ref1)
		}
	}
	if pool.PathCount() != 1 {
		t.Errorf("PathCount() = %d, want 1", pool.PathCount())
	}
}

func TestResourcePool_DedupPath_NearIdentical(t *testing.T) {
	pool := NewResourcePool()
	pool.EnableDedup(true)

	base := gg.NewPath()
	base.MoveTo(0, 0)
	base.LineTo(100, 100)
	refBase := pool.AddPath(base)

	// One coordinate differs by a tiny epsilon: must NOT dedup.
	epsilon := gg.NewPath()
	epsilon.MoveTo(0, 0)
	epsilon.LineTo(100, 100+1e-9)
	refEps := pool.AddPath(epsilon)

	if refEps == refBase {
		t.Error("near-identical path (1e-9 difference) incorrectly deduplicated")
	}
	if pool.PathCount() != 2 {
		t.Errorf("PathCount() = %d, want 2", pool.PathCount())
	}

	// Extra verb, same coords: must NOT dedup.
	extra := gg.NewPath()
	extra.MoveTo(0, 0)
	extra.LineTo(100, 100)
	extra.Close()
	if ref := pool.AddPath(extra); ref == refBase {
		t.Error("path with extra Close verb incorrectly deduplicated")
	}
}

func TestResourcePool_DedupPath_NilAndEmpty(t *testing.T) {
	pool := NewResourcePool()
	pool.EnableDedup(true)

	nilRef1 := pool.AddPath(nil)
	nilRef2 := pool.AddPath(nil)
	if nilRef1 != nilRef2 {
		t.Errorf("nil paths got refs %d and %d, want equal", nilRef1, nilRef2)
	}

	emptyRef1 := pool.AddPath(gg.NewPath())
	emptyRef2 := pool.AddPath(gg.NewPath())
	if emptyRef1 != emptyRef2 {
		t.Errorf("empty paths got refs %d and %d, want equal", emptyRef1, emptyRef2)
	}
	if emptyRef1 == nilRef1 {
		t.Error("empty path deduplicated onto nil path")
	}
	if got := pool.GetPath(nilRef1); got != nil {
		t.Errorf("GetPath(nil ref) = %v, want nil", got)
	}
	if got := pool.GetPath(emptyRef1); got == nil {
		t.Error("GetPath(empty ref) = nil, want empty path")
	}
	if pool.PathCount() != 2 {
		t.Errorf("PathCount() = %d, want 2 (nil + empty)", pool.PathCount())
	}
}

func TestResourcePool_DedupPath_HashCollisionConfirmed(t *testing.T) {
	pool := NewResourcePool()
	pool.EnableDedup(true)

	pathA := gg.NewPath()
	pathA.MoveTo(0, 0)
	pathA.LineTo(10, 10)
	refA := pool.AddPath(pathA)

	pathB := gg.NewPath()
	pathB.MoveTo(5, 5)
	pathB.LineTo(20, 20)

	// Force a hash collision: register refA under pathB's fingerprint.
	// The full content confirmation must reject the match.
	fpB := pathFingerprint(pathB)
	pool.pathIndex[fpB] = append(pool.pathIndex[fpB], uint32(refA))

	refB := pool.AddPath(pathB)
	if refB == refA {
		t.Fatal("hash collision produced false dedup: different paths share a ref")
	}
	if pool.PathCount() != 2 {
		t.Errorf("PathCount() = %d, want 2", pool.PathCount())
	}
	if !pathsEqual(pool.GetPath(refB), pathB) {
		t.Error("GetPath(refB) content does not match pathB")
	}
	if !pathsEqual(pool.GetPath(refA), pathA) {
		t.Error("GetPath(refA) content does not match pathA")
	}
}

func TestResourcePool_DedupPath_Immutability(t *testing.T) {
	pool := NewResourcePool()
	pool.EnableDedup(true)

	original := gg.NewPath()
	original.MoveTo(0, 0)
	original.LineTo(10, 10)
	ref := pool.AddPath(original)

	// Mutate the original after Add (LineTo and Append): pooled copy must
	// not change.
	original.LineTo(20, 20)
	other := gg.NewPath()
	other.Circle(50, 50, 5)
	original.Append(other)

	pooled := pool.GetPath(ref)
	if len(pooled.Verbs()) != 2 {
		t.Errorf("pooled path has %d verbs after mutating original, want 2", len(pooled.Verbs()))
	}

	// A path equal to the mutated original must NOT match the pooled entry.
	if got := pool.AddPath(original); got == ref {
		t.Error("mutated original deduplicated onto its pre-mutation entry")
	}
}

// --------------------------------------------------------------------------
// Brush dedup
// --------------------------------------------------------------------------

func TestResourcePool_DedupBrush_Repeated(t *testing.T) {
	pool := NewResourcePool()
	pool.EnableDedup(true)

	red1 := pool.AddBrush(NewSolidBrush(gg.Red))
	red2 := pool.AddBrush(NewSolidBrush(gg.RGBA{R: 1, G: 0, B: 0, A: 1}))
	if red1 != red2 {
		t.Errorf("identical solid brushes got refs %d and %d", red1, red2)
	}

	// Value and pointer forms of the same content must dedup together.
	solid := SolidBrush{Color: gg.Red}
	red3 := pool.AddBrush(&solid)
	if red3 != red1 {
		t.Errorf("pointer-form solid brush got ref %d, want %d", red3, red1)
	}

	// Different alpha: must not dedup.
	semi := pool.AddBrush(NewSolidBrush(gg.RGBA2(1, 0, 0, 0.5)))
	if semi == red1 {
		t.Error("semi-transparent brush deduplicated onto opaque brush")
	}

	// Gradients built separately with identical stops must dedup.
	grad1 := NewLinearGradientBrush(0, 0, 100, 0).
		AddColorStop(0, gg.Red).
		AddColorStop(1, gg.Blue)
	grad2 := NewLinearGradientBrush(0, 0, 100, 0).
		AddColorStop(0, gg.RGBA{R: 1, G: 0, B: 0, A: 1}).
		AddColorStop(1, gg.RGBA{B: 1, A: 1})
	g1 := pool.AddBrush(grad1)
	g2 := pool.AddBrush(grad2)
	if g1 != g2 {
		t.Errorf("identical gradients got refs %d and %d", g1, g2)
	}

	// Same gradient with one different stop offset: must not dedup.
	grad3 := NewLinearGradientBrush(0, 0, 100, 0).
		AddColorStop(0.01, gg.Red).
		AddColorStop(1, gg.Blue)
	if g3 := pool.AddBrush(grad3); g3 == g1 {
		t.Error("gradient with different stop offset incorrectly deduplicated")
	}

	// Radial and sweep gradients with matching content.
	r1 := pool.AddBrush(NewRadialGradientBrush(50, 50, 0, 50).AddColorStop(0, gg.White).AddColorStop(1, gg.Black))
	r2 := pool.AddBrush(NewRadialGradientBrush(50, 50, 0, 50).AddColorStop(0, gg.White).AddColorStop(1, gg.Black))
	if r1 != r2 {
		t.Errorf("identical radial gradients got refs %d and %d", r1, r2)
	}
	s1 := pool.AddBrush(NewSweepGradientBrush(50, 50, 0).AddColorStop(0, gg.Red).AddColorStop(1, gg.Red))
	s2 := pool.AddBrush(NewSweepGradientBrush(50, 50, 0).AddColorStop(0, gg.Red).AddColorStop(1, gg.Red))
	if s1 != s2 {
		t.Errorf("identical sweep gradients got refs %d and %d", s1, s2)
	}

	// Nil brushes dedup to a single entry.
	n1 := pool.AddBrush(nil)
	n2 := pool.AddBrush(nil)
	var typedNil *LinearGradientBrush
	n3 := pool.AddBrush(typedNil)
	if n1 != n2 || n1 != n3 {
		t.Errorf("nil brushes got refs %d, %d, %d, want all equal", n1, n2, n3)
	}
}

// --------------------------------------------------------------------------
// Image dedup
// --------------------------------------------------------------------------

// makeRGBA returns a w×h RGBA image filled with the given premultiplied color.
func makeRGBA(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func TestResourcePool_DedupImage_SemiTransparent(t *testing.T) {
	pool := NewResourcePool()
	pool.EnableDedup(true)

	// Two separately allocated images with identical semi-transparent pixels.
	semi := color.RGBA{R: 200, G: 100, B: 50, A: 128}
	img1 := makeRGBA(16, 16, semi)
	img2 := makeRGBA(16, 16, semi)
	if img1 == img2 {
		t.Fatal("test setup: images must be distinct allocations")
	}

	ref1 := pool.AddImage(img1)
	ref2 := pool.AddImage(img2)
	if ref1 != ref2 {
		t.Errorf("pixel-identical images got refs %d and %d (pointer compare would explain this)", ref1, ref2)
	}
	if pool.ImageCount() != 1 {
		t.Errorf("ImageCount() = %d, want 1", pool.ImageCount())
	}

	// Same size, one pixel differs in alpha: must not dedup.
	img3 := makeRGBA(16, 16, semi)
	img3.SetRGBA(7, 7, color.RGBA{R: 200, G: 100, B: 50, A: 129})
	if ref3 := pool.AddImage(img3); ref3 == ref1 {
		t.Error("image with one different alpha pixel incorrectly deduplicated")
	}

	// Different size: must not dedup.
	img4 := makeRGBA(8, 16, semi)
	if ref4 := pool.AddImage(img4); ref4 == ref1 {
		t.Error("image with different size incorrectly deduplicated")
	}
}

func TestResourcePool_DedupImage_ColorModel(t *testing.T) {
	pool := NewResourcePool()
	pool.EnableDedup(true)

	// Opaque gray pixels: RGBA and Gray render identically, but the color
	// models differ, so they must not dedup.
	rgbaImg := makeRGBA(8, 8, color.RGBA{R: 128, G: 128, B: 128, A: 255})
	grayImg := image.NewGray(image.Rect(0, 0, 8, 8))
	for i := range grayImg.Pix {
		grayImg.Pix[i] = 128
	}

	rgbaRef := pool.AddImage(rgbaImg)
	grayRef := pool.AddImage(grayImg)
	if rgbaRef == grayRef {
		t.Error("RGBA and Gray images with identical visible pixels incorrectly deduplicated")
	}

	// NRGBA whose visible colors match the RGBA image: still a different
	// color model, must not dedup.
	nrgbaImg := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			nrgbaImg.SetNRGBA(x, y, color.NRGBA{R: 128, G: 128, B: 128, A: 255})
		}
	}
	if nrgbaRef := pool.AddImage(nrgbaImg); nrgbaRef == rgbaRef {
		t.Error("NRGBA and RGBA images incorrectly deduplicated")
	}

	// A second Gray image with identical pixels dedups onto the first.
	grayImg2 := image.NewGray(image.Rect(0, 0, 8, 8))
	copy(grayImg2.Pix, grayImg.Pix)
	if ref := pool.AddImage(grayImg2); ref != grayRef {
		t.Errorf("identical Gray images got refs %d and %d", ref, grayRef)
	}
}

func TestResourcePool_DedupImage_Nil(t *testing.T) {
	pool := NewResourcePool()
	pool.EnableDedup(true)

	ref1 := pool.AddImage(nil)
	ref2 := pool.AddImage(nil)
	if ref1 != ref2 {
		t.Errorf("nil images got refs %d and %d, want equal", ref1, ref2)
	}
	if pool.ImageCount() != 1 {
		t.Errorf("ImageCount() = %d, want 1", pool.ImageCount())
	}
}

// --------------------------------------------------------------------------
// Font dedup
// --------------------------------------------------------------------------

func loadTestFace(t *testing.T, size float64) (text.Face, *text.FontSource) {
	t.Helper()
	data, err := os.ReadFile("../text/testdata/goregular.ttf")
	if err != nil {
		t.Skipf("test font unavailable: %v", err)
	}
	src, err := text.NewFontSource(data)
	if err != nil {
		t.Fatalf("NewFontSource: %v", err)
	}
	return src.Face(size), src
}

func TestResourcePool_DedupFont(t *testing.T) {
	pool := NewResourcePool()
	pool.EnableDedup(true)

	face12, src := loadTestFace(t, 12)
	defer src.Close()
	face12again := src.Face(12)
	face16 := src.Face(16)

	ref1 := pool.AddFont(face12)
	ref2 := pool.AddFont(face12again)
	if ref1 != ref2 {
		t.Errorf("same-source same-size faces got refs %d and %d", ref1, ref2)
	}
	if ref3 := pool.AddFont(face16); ref3 == ref1 {
		t.Error("different-size faces incorrectly deduplicated")
	}

	// Nil faces dedup to a single entry.
	n1 := pool.AddFont(nil)
	n2 := pool.AddFont(nil)
	if n1 != n2 {
		t.Errorf("nil faces got refs %d and %d, want equal", n1, n2)
	}

	// A face from a different FontSource (same font bytes) must NOT dedup:
	// source identity is by pointer.
	face12other, src2 := loadTestFace(t, 12)
	defer src2.Close()
	if ref4 := pool.AddFont(face12other); ref4 == ref1 {
		t.Error("faces from different FontSources incorrectly deduplicated")
	}
}

// --------------------------------------------------------------------------
// Toggle, stats, overflow
// --------------------------------------------------------------------------

func TestResourcePool_DedupToggle(t *testing.T) {
	pool := NewResourcePool()

	// Disabled (default): every Add appends, refs follow insertion order.
	mkPath := func() *gg.Path {
		p := gg.NewPath()
		p.Rectangle(0, 0, 10, 10)
		return p
	}
	ref0 := pool.AddPath(mkPath())
	ref1 := pool.AddPath(mkPath())
	if ref0 != 0 || ref1 != 1 {
		t.Errorf("dedup-off refs = %d, %d, want 0, 1 (insertion order)", ref0, ref1)
	}
	if pool.PathCount() != 2 {
		t.Errorf("dedup-off PathCount() = %d, want 2", pool.PathCount())
	}

	// Enable: existing entries become matchable, duplicates collapse.
	pool.EnableDedup(true)
	if !pool.DedupEnabled() {
		t.Error("DedupEnabled() = false after EnableDedup(true)")
	}
	ref2 := pool.AddPath(mkPath())
	if ref2 != ref0 {
		t.Errorf("after enabling, duplicate got ref %d, want first match %d", ref2, ref0)
	}
	if pool.PathCount() != 2 {
		t.Errorf("PathCount() = %d, want 2", pool.PathCount())
	}

	// Disable again: appends resume, old refs stay valid.
	pool.EnableDedup(false)
	if pool.DedupEnabled() {
		t.Error("DedupEnabled() = true after EnableDedup(false)")
	}
	ref3 := pool.AddPath(mkPath())
	if ref3 != 2 {
		t.Errorf("after disabling, new add got ref %d, want 2 (append)", ref3)
	}
	if !pathsEqual(pool.GetPath(ref0), mkPath()) {
		t.Error("old reference invalid after toggling dedup")
	}
}

func TestResourcePool_DedupStats(t *testing.T) {
	pool := NewResourcePool()
	pool.EnableDedup(true)

	brush := NewSolidBrush(gg.Red)
	for i := 0; i < 10; i++ {
		pool.AddBrush(brush)
	}
	for i := 0; i < 5; i++ {
		p := gg.NewPath()
		p.Circle(10, 10, 5)
		pool.AddPath(p)
	}

	stats := pool.DedupStats()
	if !stats.Enabled {
		t.Error("stats.Enabled = false, want true")
	}
	if stats.Hits != 13 {
		t.Errorf("stats.Hits = %d, want 13 (9 brush + 4 path)", stats.Hits)
	}
	if stats.Misses != 2 {
		t.Errorf("stats.Misses = %d, want 2", stats.Misses)
	}
	if stats.SavedBytes == 0 {
		t.Error("stats.SavedBytes = 0, want > 0")
	}
	if stats.ExtraBytes == 0 {
		t.Error("stats.ExtraBytes = 0, want > 0")
	}

	// Clear resets the counters but keeps the setting.
	pool.Clear()
	stats = pool.DedupStats()
	if stats.Hits != 0 || stats.Misses != 0 || stats.SavedBytes != 0 || stats.ExtraBytes != 0 {
		t.Errorf("stats after Clear = %+v, want zeroed", stats)
	}
	if !stats.Enabled {
		t.Error("stats.Enabled = false after Clear, want true (setting kept)")
	}
}

func TestResourcePool_Overflow(t *testing.T) {
	defer func(prev uint64) { maxPoolResources = prev }(maxPoolResources)
	maxPoolResources = 2

	for _, dedup := range []bool{false, true} {
		pool := NewResourcePool()
		pool.EnableDedup(dedup)

		p := gg.NewPath()
		p.Rectangle(0, 0, 1, 1)
		if ref := pool.AddPath(p); !ref.IsValid() {
			t.Fatalf("dedup=%v: first AddPath invalid", dedup)
		}
		if ref := pool.AddPath(p); !ref.IsValid() {
			t.Fatalf("dedup=%v: second AddPath invalid", dedup)
		}
		// Third unique path overflows (dedup on: the duplicate above reused
		// a slot, so add a distinct path to force the overflow).
		p2 := gg.NewPath()
		p2.Rectangle(0, 0, 2, 2)
		ref := pool.AddPath(p2)
		if dedup {
			// dedup: second add was a hit, so this is only the 2nd entry.
			if !ref.IsValid() {
				t.Fatal("dedup: unexpected overflow on second unique path")
			}
			p3 := gg.NewPath()
			p3.Rectangle(0, 0, 3, 3)
			if ref = pool.AddPath(p3); ref.IsValid() {
				t.Errorf("dedup: overflow ref = %d, want InvalidRef", ref)
			}
		} else if ref.IsValid() {
			t.Errorf("dedup off: overflow ref = %d, want InvalidRef", ref)
		}
		if pool.PathCount() != 2 {
			t.Errorf("dedup=%v: PathCount() = %d, want 2 (overflow stored nothing)", dedup, pool.PathCount())
		}

		// Brushes/images/fonts follow the same guard.
		for i := 0; i < 4; i++ {
			pool.AddBrush(NewSolidBrush(gg.RGBA{R: float64(i) / 4, A: 1}))
		}
		if pool.BrushCount() != 2 {
			t.Errorf("dedup=%v: BrushCount() = %d, want 2", dedup, pool.BrushCount())
		}
		for i := 0; i < 4; i++ {
			pool.AddImage(makeRGBA(1, 1, color.RGBA{R: uint8(i), A: 255}))
		}
		if pool.ImageCount() != 2 {
			t.Errorf("dedup=%v: ImageCount() = %d, want 2", dedup, pool.ImageCount())
		}
		for i := 0; i < 4; i++ {
			pool.AddFont(nil)
		}
		// With dedup on, nil faces collapse to a single entry; with dedup
		// off, the first two append and the rest hit the overflow guard.
		wantFonts := 2
		if dedup {
			wantFonts = 1
		}
		if pool.FontCount() != wantFonts {
			t.Errorf("dedup=%v: FontCount() = %d, want %d", dedup, pool.FontCount(), wantFonts)
		}
	}
}

// --------------------------------------------------------------------------
// Clone / Clear with dedup
// --------------------------------------------------------------------------

func TestResourcePool_DedupClone(t *testing.T) {
	pool := NewResourcePool()
	pool.EnableDedup(true)

	mkPath := func() *gg.Path {
		p := gg.NewPath()
		p.MoveTo(0, 0)
		p.LineTo(10, 10)
		return p
	}
	pathRef := pool.AddPath(mkPath())
	pool.AddPath(mkPath()) // dedup hit
	brushRef := pool.AddBrush(NewSolidBrush(gg.Red))
	pool.AddBrush(NewSolidBrush(gg.Red))
	imgRef := pool.AddImage(makeRGBA(4, 4, color.RGBA{G: 255, A: 255}))

	clone := pool.Clone()

	// Refs stay valid and counts match.
	if clone.PathCount() != 1 || clone.BrushCount() != 1 || clone.ImageCount() != 1 {
		t.Fatalf("clone counts = %d/%d/%d, want 1/1/1",
			clone.PathCount(), clone.BrushCount(), clone.ImageCount())
	}
	if !pathsEqual(clone.GetPath(pathRef), mkPath()) {
		t.Error("cloned path content mismatch")
	}
	if clone.GetBrush(brushRef) == nil || clone.GetImage(imgRef) == nil {
		t.Error("clone missing brush or image")
	}

	// The clone must not share the mutable *gg.Path with the original.
	if clone.GetPath(pathRef) == pool.GetPath(pathRef) {
		t.Error("clone shares *gg.Path with original")
	}

	// Dedup still works on the clone: same content returns the existing ref.
	if ref := clone.AddPath(mkPath()); ref != pathRef {
		t.Errorf("clone dedup returned ref %d, want %d", ref, pathRef)
	}

	// And the two pools' indexes are independent.
	other := gg.NewPath()
	other.Circle(1, 1, 1)
	clone.AddPath(other)
	if pool.PathCount() != 1 {
		t.Errorf("original PathCount() = %d after clone add, want 1", pool.PathCount())
	}
}

func TestResourcePool_DedupClear(t *testing.T) {
	pool := NewResourcePool()
	pool.EnableDedup(true)

	p := gg.NewPath()
	p.Rectangle(0, 0, 5, 5)
	pool.AddPath(p)
	pool.AddPath(p)
	pool.Clear()

	if pool.PathCount() != 0 {
		t.Errorf("PathCount() after Clear = %d, want 0", pool.PathCount())
	}
	// Re-adding after Clear starts from ref 0 again.
	if ref := pool.AddPath(p); ref != 0 {
		t.Errorf("ref after Clear = %d, want 0", ref)
	}
	if ref := pool.AddPath(p); ref != 0 {
		t.Errorf("dedup after Clear returned ref %d, want 0", ref)
	}
}

// --------------------------------------------------------------------------
// Concurrency
// --------------------------------------------------------------------------

func TestResourcePool_DedupConcurrent(t *testing.T) {
	pool := NewResourcePool()
	pool.EnableDedup(true)

	const goroutines = 8
	const addsPerGoroutine = 200
	const uniquePaths = 4

	var wg sync.WaitGroup
	refs := make([][]PathRef, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			refs[g] = make([]PathRef, 0, addsPerGoroutine)
			for i := 0; i < addsPerGoroutine; i++ {
				p := gg.NewPath()
				// Only `uniquePaths` distinct shapes across all goroutines.
				p.Circle(float64(i%uniquePaths)*10, 0, 5)
				ref := pool.AddPath(p)
				pool.AddBrush(NewSolidBrush(gg.RGBA{R: float64(i%uniquePaths) / 4, A: 1}))
				refs[g] = append(refs[g], ref)
				// Concurrent reads must see complete entries.
				got := pool.GetPath(ref)
				if got == nil {
					t.Errorf("GetPath(%d) returned nil during concurrent add", ref)
					return
				}
				if len(got.Verbs()) == 0 {
					t.Errorf("GetPath(%d) returned half-built path", ref)
					return
				}
			}
		}(g)
	}
	wg.Wait()

	if pool.PathCount() != uniquePaths {
		t.Errorf("PathCount() = %d, want %d unique", pool.PathCount(), uniquePaths)
	}
	if pool.BrushCount() != uniquePaths {
		t.Errorf("BrushCount() = %d, want %d unique", pool.BrushCount(), uniquePaths)
	}
	// Every returned ref resolves to a complete, correctly shaped path.
	for g := 0; g < goroutines; g++ {
		for _, ref := range refs[g] {
			if !ref.IsValid() {
				t.Fatalf("goroutine %d got invalid ref", g)
			}
			if got := pool.GetPath(ref); got == nil || len(got.Verbs()) == 0 {
				t.Fatalf("goroutine %d: ref %d resolves to incomplete path", g, ref)
			}
		}
	}
	stats := pool.DedupStats()
	// Stats are shared across resource kinds: paths and brushes both count.
	total := uint64(goroutines * addsPerGoroutine * 2)
	if stats.Hits+stats.Misses != total {
		t.Errorf("stats: Hits(%d)+Misses(%d) = %d, want %d",
			stats.Hits, stats.Misses, stats.Hits+stats.Misses, total)
	}
	if stats.Misses != uniquePaths*2 {
		t.Errorf("stats.Misses = %d, want %d (paths + brushes)", stats.Misses, uniquePaths*2)
	}
}

// --------------------------------------------------------------------------
// Memory growth
// --------------------------------------------------------------------------

func TestResourcePool_DedupMemoryGrowth(t *testing.T) {
	const repeats = 500

	withDedup := NewResourcePool()
	withDedup.EnableDedup(true)
	withoutDedup := NewResourcePool()

	for i := 0; i < repeats; i++ {
		p := gg.NewPath()
		p.RoundedRectangle(0, 0, 100, 40, 8)
		b := NewSolidBrush(gg.RGBA{R: 0.2, G: 0.4, B: 0.8, A: 1})
		withDedup.AddPath(p)
		withDedup.AddBrush(b)
		withoutDedup.AddPath(p)
		withoutDedup.AddBrush(b)
	}

	if withDedup.PathCount() != 1 || withDedup.BrushCount() != 1 {
		t.Errorf("dedup pool counts = %d paths, %d brushes; want 1, 1",
			withDedup.PathCount(), withDedup.BrushCount())
	}
	if withoutDedup.PathCount() != repeats || withoutDedup.BrushCount() != repeats {
		t.Errorf("plain pool counts = %d paths, %d brushes; want %d, %d",
			withoutDedup.PathCount(), withoutDedup.BrushCount(), repeats, repeats)
	}

	stats := withDedup.DedupStats()
	if stats.SavedBytes == 0 {
		t.Error("SavedBytes = 0, want > 0 for 499 deduplicated paths")
	}
	// The savings must outweigh the dedup table overhead by a wide margin
	// in this repeated-resource scenario.
	if stats.SavedBytes < stats.ExtraBytes*10 {
		t.Errorf("SavedBytes(%d) < 10x ExtraBytes(%d)", stats.SavedBytes, stats.ExtraBytes)
	}
}

// --------------------------------------------------------------------------
// Benchmarks
// --------------------------------------------------------------------------

// BenchmarkResourcePool_AddPath_Dedup measures repeated adds of an identical
// path with dedup on (hit path) versus the plain append path.
func BenchmarkResourcePool_AddPath_Dedup(b *testing.B) {
	pool := NewResourcePool()
	pool.EnableDedup(true)
	path := gg.NewPath()
	path.Rectangle(0, 0, 100, 100)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pool.AddPath(path)
	}
}

// BenchmarkResourcePool_AddPath_DedupUnique measures the miss path: every
// add is a distinct path, so dedup pays the fingerprint cost without hits.
func BenchmarkResourcePool_AddPath_DedupUnique(b *testing.B) {
	pool := NewResourcePool()
	pool.EnableDedup(true)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		path := gg.NewPath()
		path.Circle(float64(i), 0, 10)
		pool.AddPath(path)
	}
}

// BenchmarkResourcePool_AddBrush_Dedup measures repeated adds of an
// identical brush with dedup on.
func BenchmarkResourcePool_AddBrush_Dedup(b *testing.B) {
	pool := NewResourcePool()
	pool.EnableDedup(true)
	brush := NewSolidBrush(gg.Red)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pool.AddBrush(brush)
	}
}

// BenchmarkResourcePool_DedupScene simulates a UI scene: 1000 draw calls
// cycling through 10 unique path/brush pairs, with dedup on and off.
func BenchmarkResourcePool_DedupScene(b *testing.B) {
	paths := make([]*gg.Path, 10)
	brushes := make([]Brush, 10)
	for i := range paths {
		p := gg.NewPath()
		p.RoundedRectangle(0, 0, float64(50+i*10), 30, 6)
		paths[i] = p
		brushes[i] = NewSolidBrush(gg.RGBA{R: float64(i) / 10, G: 0.5, B: 1, A: 1})
	}

	b.Run("DedupOn", func(b *testing.B) {
		pool := NewResourcePool()
		pool.EnableDedup(true)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			k := i % 10
			pool.AddPath(paths[k])
			pool.AddBrush(brushes[k])
		}
	})
	b.Run("DedupOff", func(b *testing.B) {
		pool := NewResourcePool()
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			k := i % 10
			pool.AddPath(paths[k])
			pool.AddBrush(brushes[k])
		}
	})
}

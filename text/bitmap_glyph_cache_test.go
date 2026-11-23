package text

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"sync"
	"sync/atomic"
	"testing"

	xdraw "golang.org/x/image/draw"

	"github.com/gogpu/gg/text/emoji"
)

// --- Test helpers -----------------------------------------------------------

// testBitmap returns a small bitmap glyph descriptor for tests.
func testBitmap(glyphID uint16) *emoji.BitmapGlyph {
	return &emoji.BitmapGlyph{
		GlyphID: glyphID,
		Width:   16,
		Height:  16,
		OriginX: 1,
		OriginY: 14,
		PPEM:    32,
	}
}

// encodeTestPNG returns PNG-encoded RGBA image data.
func encodeTestPNG(t testing.TB, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

// mockColorFont implements ColorFont with call counting and fault injection.
type mockColorFont struct {
	// bitmaps maps glyphID to the bitmap to return. Missing glyphs fail.
	bitmaps map[uint16]*emoji.BitmapGlyph
	// corrupt, when non-nil, forces Decode to fail for these glyph IDs.
	corrupt map[uint16]bool
	// loads counts BitmapGlyph calls (cache misses in the draw path).
	loads atomic.Int64
}

func (m *mockColorFont) HasColorTables() bool { return true }

func (m *mockColorFont) GlyphType(glyphID uint16) GlyphType {
	if _, ok := m.bitmaps[glyphID]; ok {
		return GlyphTypeBitmap
	}
	return GlyphTypeOutline
}

func (m *mockColorFont) BitmapGlyph(glyphID uint16, ppem uint16) (*emoji.BitmapGlyph, error) {
	m.loads.Add(1)
	b, ok := m.bitmaps[glyphID]
	if !ok {
		return nil, emoji.ErrGlyphNotInBitmap
	}
	if m.corrupt[glyphID] {
		// Fault injection: valid PNG header is replaced with garbage,
		// so BitmapGlyph.Decode fails.
		return &emoji.BitmapGlyph{
			GlyphID: glyphID,
			Data:    []byte("not a png"),
			Format:  emoji.FormatPNG,
			Width:   b.Width,
			Height:  b.Height,
			PPEM:    b.PPEM,
		}, nil
	}
	return b, nil
}

func (m *mockColorFont) COLRGlyph(uint16, int) (*emoji.COLRGlyph, error) {
	return nil, emoji.ErrGlyphNotInCOLR
}

// newMockColorFont builds a mock font whose glyphs are valid PNG bitmaps.
func newMockColorFont(t testing.TB, glyphIDs ...uint16) *mockColorFont {
	t.Helper()
	m := &mockColorFont{
		bitmaps: make(map[uint16]*emoji.BitmapGlyph),
		corrupt: make(map[uint16]bool),
	}
	for _, id := range glyphIDs {
		img := image.NewRGBA(image.Rect(0, 0, 16, 16))
		// Fill with a distinct opaque color per glyph.
		c := color.RGBA{R: uint8(id), G: 200, B: 100, A: 255}
		for i := range img.Pix {
			if i%4 == 0 {
				img.Pix[i] = c.R
				img.Pix[i+1] = c.G
				img.Pix[i+2] = c.B
				img.Pix[i+3] = c.A
			}
		}
		m.bitmaps[id] = &emoji.BitmapGlyph{
			GlyphID: id,
			Data:    encodeTestPNG(t, img),
			Format:  emoji.FormatPNG,
			Width:   16,
			Height:  16,
			OriginX: 0,
			OriginY: 16,
			PPEM:    32,
		}
	}
	return m
}

// --- Edge-case contract -----------------------------------------------------

func TestBitmapGlyphCache_ZeroCapacity(t *testing.T) {
	for _, size := range []int{0, -5} {
		cache := NewBitmapGlyphCache(size)
		img := image.NewRGBA(image.Rect(0, 0, 16, 16))

		cache.Put(1, 100, 32, testBitmap(100), img)
		if cache.Size() != 0 {
			t.Errorf("size=%d: Size() = %d, want 0 (disabled cache)", size, cache.Size())
		}
		if got := cache.Get(1, 100, 32); got != nil {
			t.Errorf("size=%d: Get() = %v, want nil (disabled cache)", size, got)
		}

		// Clear and Stats must be safe on a disabled cache.
		cache.Clear()
		stats := cache.Stats()
		if stats.MaxEntries != 0 || stats.MaxBytes != 0 {
			t.Errorf("size=%d: disabled cache reports limits %+v", size, stats)
		}
	}
}

func TestBitmapGlyphCache_ZeroByteLimit(t *testing.T) {
	cache := NewBitmapGlyphCacheWithLimits(100, 0)
	cache.Put(1, 100, 32, testBitmap(100), image.NewRGBA(image.Rect(0, 0, 16, 16)))
	if cache.Size() != 0 {
		t.Errorf("Size() = %d, want 0 (zero byte budget disables cache)", cache.Size())
	}
}

func TestBitmapGlyphCache_NilInputs(t *testing.T) {
	cache := NewBitmapGlyphCache(100)
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))

	// nil image: no-op.
	cache.Put(1, 100, 32, testBitmap(100), nil)
	// nil bitmap: no-op.
	cache.Put(1, 101, 32, nil, img)
	// empty image: no-op.
	cache.Put(1, 102, 32, testBitmap(102), image.NewRGBA(image.Rectangle{}))

	if cache.Size() != 0 {
		t.Errorf("Size() = %d, want 0 after nil/empty Puts", cache.Size())
	}
}

func TestBitmapGlyphCache_DuplicatePut(t *testing.T) {
	cache := NewBitmapGlyphCache(100)

	img1 := image.NewRGBA(image.Rect(0, 0, 16, 16))
	img2 := image.NewRGBA(image.Rect(0, 0, 16, 16))
	img2.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})

	cache.Put(1, 100, 32, testBitmap(100), img1)
	cache.Put(1, 100, 32, testBitmap(100), img2)

	if cache.Size() != 1 {
		t.Errorf("Size() = %d, want 1 after duplicate Put", cache.Size())
	}

	got := cache.Get(1, 100, 32)
	if got == nil {
		t.Fatal("Get() returned nil after duplicate Put")
	}
	r, _, _, _ := got.Img.At(0, 0).RGBA()
	if r == 0 {
		t.Error("duplicate Put did not replace the cached image")
	}

	// Byte accounting must reflect a single entry, not two.
	wantBytes := int64(16 * 16 * 4)
	if cache.Bytes() != wantBytes {
		t.Errorf("Bytes() = %d, want %d after duplicate Put", cache.Bytes(), wantBytes)
	}
}

func TestBitmapGlyphCache_LRUOrder(t *testing.T) {
	// 2 entries per shard.
	cache := NewBitmapGlyphCache(2 * bitmapCacheShardCount)
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))

	const fontID = uintptr(7)
	g1 := uint16(1000)
	g2 := sameShardGlyphID(cache, fontID, g1)
	g3 := sameShardGlyphID(cache, fontID, g2) // > g2 > g1, same shard

	cache.Put(fontID, g1, 32, testBitmap(g1), img)
	cache.Put(fontID, g2, 32, testBitmap(g2), img)

	// Refresh g1 so g2 becomes the LRU entry.
	if cache.Get(fontID, g1, 32) == nil {
		t.Fatal("Get() returned nil for g1")
	}

	// Inserting g3 must evict g2 (LRU), not g1.
	cache.Put(fontID, g3, 32, testBitmap(g3), img)

	if cache.Get(fontID, g1, 32) == nil {
		t.Error("g1 was evicted despite being recently used")
	}
	if cache.Get(fontID, g2, 32) != nil {
		t.Error("g2 (LRU) was not evicted")
	}
	if cache.Get(fontID, g3, 32) == nil {
		t.Error("g3 (newest) missing")
	}
}

func TestBitmapGlyphCache_ByteLimit(t *testing.T) {
	// Budget for exactly two 16x16 RGBA images per shard.
	const imgBytes = 16 * 16 * 4
	cache := NewBitmapGlyphCacheWithLimits(1000, 2*imgBytes*bitmapCacheShardCount)
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))

	const fontID = uintptr(9)
	g1 := uint16(2000)
	g2 := sameShardGlyphID(cache, fontID, g1)
	g3 := sameShardGlyphID(cache, fontID, g2) // > g2 > g1, same shard

	cache.Put(fontID, g1, 32, testBitmap(g1), img)
	cache.Put(fontID, g2, 32, testBitmap(g2), img)
	if got := cache.Bytes(); got != 2*imgBytes {
		t.Fatalf("Bytes() = %d, want %d", got, 2*imgBytes)
	}

	// Third image exceeds the byte budget: LRU (g1) is evicted.
	cache.Put(fontID, g3, 32, testBitmap(g3), img)
	if cache.Get(fontID, g1, 32) != nil {
		t.Error("byte-limit eviction did not remove LRU entry")
	}
	if got := cache.Bytes(); got != 2*imgBytes {
		t.Errorf("Bytes() = %d, want %d after eviction", got, 2*imgBytes)
	}
	if cache.Stats().Evictions != 1 {
		t.Errorf("Evictions = %d, want 1", cache.Stats().Evictions)
	}
}

func TestBitmapGlyphCache_OversizedEntry(t *testing.T) {
	// Entry larger than the per-shard byte budget is never cached.
	cache := NewBitmapGlyphCacheWithLimits(100, 16*16*4*bitmapCacheShardCount)
	big := image.NewRGBA(image.Rect(0, 0, 64, 64))

	cache.Put(1, 100, 32, testBitmap(100), big)
	if cache.Size() != 0 {
		t.Errorf("Size() = %d, want 0 for oversized entry", cache.Size())
	}
	if cache.Get(1, 100, 32) != nil {
		t.Error("Get() returned oversized entry")
	}
}

func TestBitmapGlyphCache_Stats(t *testing.T) {
	cache := NewBitmapGlyphCache(100)
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))

	cache.Put(1, 100, 32, testBitmap(100), img)

	cache.Get(1, 100, 32) // hit
	cache.Get(1, 100, 32) // hit
	cache.Get(1, 999, 32) // miss

	stats := cache.Stats()
	if stats.Hits != 2 {
		t.Errorf("Hits = %d, want 2", stats.Hits)
	}
	if stats.Misses != 1 {
		t.Errorf("Misses = %d, want 1", stats.Misses)
	}
	if stats.Entries != 1 {
		t.Errorf("Entries = %d, want 1", stats.Entries)
	}
	wantRate := 2.0 / 3.0
	if stats.HitRate < wantRate-0.001 || stats.HitRate > wantRate+0.001 {
		t.Errorf("HitRate = %f, want ~%f", stats.HitRate, wantRate)
	}
	if stats.MaxEntries <= 0 || stats.MaxBytes <= 0 {
		t.Errorf("limits not reported: %+v", stats)
	}

	cache.ResetStats()
	if s := cache.Stats(); s.Hits != 0 || s.Misses != 0 || s.Evictions != 0 {
		t.Errorf("ResetStats did not zero counters: %+v", s)
	}
}

func TestBitmapGlyphCache_ReturnedCopyIsolation(t *testing.T) {
	cache := NewBitmapGlyphCache(100)
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	cache.Put(1, 100, 32, testBitmap(100), img)

	got := cache.Get(1, 100, 32)
	if got == nil {
		t.Fatal("Get() returned nil")
	}

	// Mutating the returned struct must not corrupt cache internals.
	got.Width = 9999
	got.OriginX = -123
	got.PPEM = 1

	again := cache.Get(1, 100, 32)
	if again.Width != 16 || again.OriginX != 1 || again.PPEM != 32 {
		t.Errorf("cache entry mutated via returned copy: %+v", again)
	}
}

// --- Concurrency (run with -race) -------------------------------------------

func TestBitmapGlyphCache_ConcurrentSameKey(t *testing.T) {
	cache := NewBitmapGlyphCache(64)
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	bitmap := testBitmap(100)

	const goroutines = 16
	const iterations = 500

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				cache.Put(1, 100, 32, bitmap, img)
				got := cache.Get(1, 100, 32)
				if got != nil && got.Width != 16 {
					t.Errorf("corrupt entry: width %d", got.Width)
				}
			}
		}()
	}
	wg.Wait()

	if cache.Size() != 1 {
		t.Errorf("Size() = %d, want 1", cache.Size())
	}
	stats := cache.Stats()
	if stats.Hits+stats.Misses != goroutines*iterations {
		t.Errorf("Hits+Misses = %d, want %d", stats.Hits+stats.Misses, goroutines*iterations)
	}
	if stats.Hits == 0 {
		t.Error("expected some cache hits under concurrent same-key access")
	}
}

func TestBitmapGlyphCache_ConcurrentMultiFont(t *testing.T) {
	cache := NewBitmapGlyphCache(256)
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))

	var wg sync.WaitGroup
	// Multiple "fonts" (fontIDs) and glyphs, plus concurrent Clear.
	for font := uintptr(1); font <= 4; font++ {
		for glyph := uint16(0); glyph < 8; glyph++ {
			wg.Add(1)
			go func(fontID uintptr, glyphID uint16) {
				defer wg.Done()
				bitmap := testBitmap(glyphID)
				for i := 0; i < 200; i++ {
					cache.Put(fontID, glyphID, 32, bitmap, img)
					cache.Get(fontID, glyphID, 32)
				}
			}(font, glyph)
		}
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			cache.Clear()
		}
	}()
	wg.Wait()

	// Cache must remain consistent: size never exceeds the total budget.
	if cache.Size() > cache.Stats().MaxEntries {
		t.Errorf("Size() = %d exceeds MaxEntries = %d", cache.Size(), cache.Stats().MaxEntries)
	}
	if cache.Bytes() > cache.Stats().MaxBytes {
		t.Errorf("Bytes() = %d exceeds MaxBytes = %d", cache.Bytes(), cache.Stats().MaxBytes)
	}
}

// --- Draw path: fixed font, fault injection, reload after eviction ----------

func TestDrawBitmapGlyph_CacheHitAndFaultInjection(t *testing.T) {
	cache := NewBitmapGlyphCache(100)
	font := newMockColorFont(t, 10, 11)
	font.corrupt[11] = true // glyph 11 always fails to decode

	dst := image.NewRGBA(image.Rect(0, 0, 200, 50))
	const fontID = uintptr(42)

	// First draw of glyph 10: miss + load.
	drawBitmapGlyphWith(cache, dst, font, fontID, 10, 32, 10, 40)
	if font.loads.Load() != 1 {
		t.Fatalf("loads = %d, want 1", font.loads.Load())
	}

	// Second draw: cache hit, no reload.
	drawBitmapGlyphWith(cache, dst, font, fontID, 10, 32, 30, 40)
	if font.loads.Load() != 1 {
		t.Fatalf("loads = %d after cached draw, want 1", font.loads.Load())
	}

	// Corrupt glyph: decode fails, glyph skipped, nothing cached.
	drawBitmapGlyphWith(cache, dst, font, fontID, 11, 32, 50, 40)
	drawBitmapGlyphWith(cache, dst, font, fontID, 11, 32, 70, 40)
	if font.loads.Load() != 3 {
		t.Fatalf("loads = %d, want 3 (failures are retried, not cached)", font.loads.Load())
	}
	if cache.Get(fontID, 11, 32) != nil {
		t.Error("decode failure was cached")
	}

	// The valid glyph rendered pixels.
	if !imageHasPixel(dst, 10, 40) {
		t.Error("valid bitmap glyph did not render")
	}
}

func TestDrawBitmapGlyph_ReloadAfterEviction(t *testing.T) {
	// 1 entry per shard: same-shard glyphs evict each other.
	cache := NewBitmapGlyphCache(bitmapCacheShardCount)
	font := newMockColorFont(t, 20)
	dst := image.NewRGBA(image.Rect(0, 0, 200, 50))
	const fontID = uintptr(77)

	g1 := uint16(20)
	g2 := sameShardGlyphID(cache, fontID, g1)
	font.bitmaps[g2] = font.bitmaps[g1]

	drawBitmapGlyphWith(cache, dst, font, fontID, g1, 32, 10, 40)
	drawBitmapGlyphWith(cache, dst, font, fontID, g2, 32, 30, 40) // evicts g1
	if cache.Get(fontID, g1, 32) != nil {
		t.Fatal("g1 should have been evicted")
	}

	before := font.loads.Load()
	drawBitmapGlyphWith(cache, dst, font, fontID, g1, 32, 50, 40) // reload
	if font.loads.Load() != before+1 {
		t.Errorf("loads after eviction = %d, want %d (reload)", font.loads.Load(), before+1)
	}
	if cache.Stats().Evictions == 0 {
		t.Error("expected observable eviction")
	}
}

func TestDrawBitmapGlyph_ConcurrentFixedFont(t *testing.T) {
	// Simulates a high-frequency chat window: many goroutines render the
	// same emoji set from one font through one shared cache.
	cache := NewBitmapGlyphCache(64)
	font := newMockColorFont(t, 1, 2, 3, 4)
	const fontID = uintptr(1234)

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			dst := image.NewRGBA(image.Rect(0, 0, 400, 50))
			for i := 0; i < 100; i++ {
				glyphID := uint16(1 + (i+seed)%4)
				drawBitmapGlyphWith(cache, dst, font, fontID, glyphID, 32, float64((i%20)*20), 40)
			}
		}(g)
	}
	wg.Wait()

	// Each of the 4 glyphs is loaded exactly once despite concurrency.
	if loads := font.loads.Load(); loads != 4 {
		t.Errorf("loads = %d, want 4 (one per glyph)", loads)
	}
	stats := cache.Stats()
	if stats.Hits == 0 {
		t.Error("expected cache hits in concurrent rendering")
	}
	// 8 goroutines * 100 draws = 800 lookups; 4 misses, 796 hits.
	if stats.Hits+stats.Misses != 800 {
		t.Errorf("Hits+Misses = %d, want 800", stats.Hits+stats.Misses)
	}
	if stats.HitRate < 0.99 {
		t.Errorf("HitRate = %f, want >= 0.99", stats.HitRate)
	}
}

// TestDrawWithEmoji_NonColorFallback verifies that fonts without color
// tables still take the standard Draw path, unchanged.
func TestDrawWithEmoji_NonColorFallback(t *testing.T) {
	fontPath := testFontPath(t)
	source, err := NewFontSourceFromFile(fontPath)
	if err != nil {
		t.Fatalf("Failed to load font: %v", err)
	}
	defer func() { _ = source.Close() }()

	face := source.Face(16.0)
	const text = "Hello, emoji-free world!"

	dstEmoji := image.NewRGBA(image.Rect(0, 0, 400, 50))
	dstPlain := image.NewRGBA(image.Rect(0, 0, 400, 50))

	DrawWithEmoji(dstEmoji, text, face, 10, 30, color.Black)
	Draw(dstPlain, text, face, 10, 30, color.Black)

	if !imagesEqual(dstEmoji, dstPlain) {
		t.Error("DrawWithEmoji output differs from Draw for non-color font")
	}
}

// TestDrawWithEmoji_ConcurrentFixedFont renders one fixed font from many
// goroutines (chat-window workload) under the race detector.
func TestDrawWithEmoji_ConcurrentFixedFont(t *testing.T) {
	fontPath := testFontPath(t)
	source, err := NewFontSourceFromFile(fontPath)
	if err != nil {
		t.Fatalf("Failed to load font: %v", err)
	}
	defer func() { _ = source.Close() }()

	face := source.Face(16.0)

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dst := image.NewRGBA(image.Rect(0, 0, 400, 50))
			for i := 0; i < 50; i++ {
				DrawWithEmoji(dst, "chat message text", face, 10, 30, color.Black)
			}
		}()
	}
	wg.Wait()
}

// drawBitmapGlyphWith is drawBitmapGlyph with an explicit cache, so tests
// do not depend on (or pollute) the shared process-wide cache.
func drawBitmapGlyphWith(cache *BitmapGlyphCache, dst *image.RGBA, colorFont ColorFont, fontID uintptr, glyphID uint16, ppem uint16, x, y float64) {
	bitmap := cache.getOrLoad(colorFont, fontID, glyphID, ppem)
	if bitmap == nil {
		return
	}
	scale := float64(ppem) / float64(bitmap.PPEM)
	if scale == 0 {
		scale = 1.0
	}
	scaledW := int(float64(bitmap.Width) * scale)
	scaledH := int(float64(bitmap.Height) * scale)
	if scaledW <= 0 || scaledH <= 0 {
		return
	}
	destX := int(x + float64(bitmap.OriginX)*scale)
	destY := int(y - float64(bitmap.OriginY)*scale)
	destRect := image.Rect(destX, destY, destX+scaledW, destY+scaledH)
	// Mirror the production draw path exactly.
	xdraw.CatmullRom.Scale(dst, destRect, bitmap.Img, bitmap.Img.Bounds(), xdraw.Over, nil)
}

// imageHasPixel reports whether any pixel in the column band near x has
// non-zero alpha (a glyph was drawn).
func imageHasPixel(img *image.RGBA, x, y int) bool {
	b := img.Bounds()
	for py := max(b.Min.Y, y-40); py < min(b.Max.Y, y+10); py++ {
		for px := max(b.Min.X, x-5); px < min(b.Max.X, x+20); px++ {
			if img.RGBAAt(px, py).A != 0 {
				return true
			}
		}
	}
	return false
}

// imagesEqual reports whether two RGBA images have identical pixels.
func imagesEqual(a, b *image.RGBA) bool {
	if !a.Bounds().Eq(b.Bounds()) {
		return false
	}
	return bytes.Equal(a.Pix, b.Pix)
}

// --- Benchmarks ---------------------------------------------------------------

func BenchmarkBitmapGlyphCacheGetHit(b *testing.B) {
	cache := NewBitmapGlyphCache(1024)
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	cache.Put(1, 100, 32, testBitmap(100), img)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if cache.Get(1, 100, 32) == nil {
			b.Fatal("unexpected miss")
		}
	}
}

func BenchmarkBitmapGlyphCacheGetMiss(b *testing.B) {
	cache := NewBitmapGlyphCache(1024)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.Get(1, 100, 32)
	}
}

func BenchmarkBitmapGlyphCachePut(b *testing.B) {
	cache := NewBitmapGlyphCache(1024)
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	bitmap := testBitmap(100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.Put(1, uint16(i%512), 32, bitmap, img)
	}
}

func BenchmarkBitmapGlyphCacheConcurrent(b *testing.B) {
	cache := NewBitmapGlyphCache(1024)
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	bitmap := testBitmap(100)
	// Warm the cache: 90% hits, 10% misses on a working set of 64 glyphs.
	for g := uint16(0); g < 64; g++ {
		cache.Put(1, g, 32, bitmap, img)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			glyphID := uint16(i % 64)
			if i%10 == 0 {
				cache.Put(1, glyphID, 32, bitmap, img)
			} else {
				cache.Get(1, glyphID, 32)
			}
			i++
		}
	})
}

func BenchmarkDrawBitmapGlyphCached(b *testing.B) {
	font := newMockColorFont(b, 1, 2, 3, 4)
	cache := NewBitmapGlyphCache(64)
	dst := image.NewRGBA(image.Rect(0, 0, 400, 50))
	const fontID = uintptr(5)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		drawBitmapGlyphWith(cache, dst, font, fontID, uint16(1+i%4), 32, float64((i%20)*20), 40)
	}
	b.StopTimer()
	b.Logf("font loads: %d, hit rate: %.4f", font.loads.Load(), cache.Stats().HitRate)
}

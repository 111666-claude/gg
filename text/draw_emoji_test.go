package text

import (
	"image"
	"testing"

	"github.com/gogpu/gg/text/emoji"
)

func TestBitmapGlyphCache_NewAndSize(t *testing.T) {
	cache := NewBitmapGlyphCache(100)
	if cache == nil {
		t.Fatal("NewBitmapGlyphCache returned nil")
	}

	if cache.Size() != 0 {
		t.Errorf("Size() = %d, want 0", cache.Size())
	}
}

func TestBitmapGlyphCache_PutAndGet(t *testing.T) {
	cache := NewBitmapGlyphCache(100)

	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	bitmap := &emoji.BitmapGlyph{
		GlyphID: 100,
		Width:   16,
		Height:  16,
		OriginX: 0,
		OriginY: 16,
		PPEM:    32,
	}

	fontID := uintptr(0x12345678)

	// Put entry.
	cache.Put(fontID, 100, 32, bitmap, img)

	if cache.Size() != 1 {
		t.Errorf("Size() = %d, want 1", cache.Size())
	}

	// Get entry.
	cached := cache.Get(fontID, 100, 32)
	if cached == nil {
		t.Fatal("Get() returned nil for existing entry")
	}

	if cached.Width != 16 || cached.Height != 16 {
		t.Errorf("cached size = %dx%d, want 16x16", cached.Width, cached.Height)
	}

	if cached.OriginX != 0 || cached.OriginY != 16 {
		t.Errorf("cached origin = (%f, %f), want (0, 16)", cached.OriginX, cached.OriginY)
	}
}

func TestBitmapGlyphCache_GetMissing(t *testing.T) {
	cache := NewBitmapGlyphCache(100)

	cached := cache.Get(0x12345678, 100, 32)
	if cached != nil {
		t.Error("Get() returned non-nil for missing entry")
	}
}

func TestBitmapGlyphCache_Clear(t *testing.T) {
	cache := NewBitmapGlyphCache(100)

	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	bitmap := &emoji.BitmapGlyph{
		GlyphID: 100,
		Width:   16,
		Height:  16,
	}

	cache.Put(0x12345678, 100, 32, bitmap, img)
	cache.Put(0x12345678, 101, 32, bitmap, img)
	cache.Put(0x12345678, 102, 32, bitmap, img)

	if cache.Size() != 3 {
		t.Errorf("Size() = %d, want 3", cache.Size())
	}

	cache.Clear()

	if cache.Size() != 0 {
		t.Errorf("Size() after Clear() = %d, want 0", cache.Size())
	}
}

func TestBitmapGlyphCache_Eviction(t *testing.T) {
	// LRU eviction: when a shard is full, only its least-recently-used
	// entry is evicted — the cache is never cleared wholesale.
	cache := NewBitmapGlyphCache(bitmapCacheShardCount) // 1 entry per shard

	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	bitmap := &emoji.BitmapGlyph{
		GlyphID: 100,
		Width:   16,
		Height:  16,
	}

	const fontID = uintptr(0x12345678)

	// Two keys in the same shard, one in a different shard.
	g1 := uint16(100)
	g2 := sameShardGlyphID(cache, fontID, g1)
	other := differentShardGlyphID(cache, fontID, g1)

	cache.Put(fontID, g1, 32, bitmap, img)
	cache.Put(fontID, other, 32, bitmap, img)

	if cache.Size() != 2 {
		t.Errorf("Size() = %d, want 2", cache.Size())
	}

	// Adding a same-shard key evicts only the LRU entry in that shard.
	cache.Put(fontID, g2, 32, bitmap, img)

	if cache.Size() != 2 {
		t.Errorf("Size() after eviction = %d, want 2", cache.Size())
	}

	// The evicted LRU entry is gone; the new and unrelated entries remain.
	if cache.Get(fontID, g1, 32) != nil {
		t.Error("Get() returned evicted LRU entry")
	}
	if cache.Get(fontID, g2, 32) == nil {
		t.Error("Get() returned nil for newly added entry")
	}
	if cache.Get(fontID, other, 32) == nil {
		t.Error("Get() returned nil for entry in unrelated shard")
	}

	stats := cache.Stats()
	if stats.Evictions != 1 {
		t.Errorf("Evictions = %d, want 1", stats.Evictions)
	}
}

// sameShardGlyphID returns a glyph ID (different from g) whose key hashes
// to the same shard as (fontID, g, 32) in cache.
func sameShardGlyphID(cache *BitmapGlyphCache, fontID uintptr, g uint16) uint16 {
	want := cache.shardFor(bitmapCacheKey{fontID: fontID, glyphID: g, ppem: 32})
	for candidate := g + 1; ; candidate++ {
		if cache.shardFor(bitmapCacheKey{fontID: fontID, glyphID: candidate, ppem: 32}) == want {
			return candidate
		}
	}
}

// differentShardGlyphID returns a glyph ID whose key hashes to a different
// shard than (fontID, g, 32) in cache.
func differentShardGlyphID(cache *BitmapGlyphCache, fontID uintptr, g uint16) uint16 {
	avoid := cache.shardFor(bitmapCacheKey{fontID: fontID, glyphID: g, ppem: 32})
	for candidate := g + 1; ; candidate++ {
		if cache.shardFor(bitmapCacheKey{fontID: fontID, glyphID: candidate, ppem: 32}) != avoid {
			return candidate
		}
	}
}

func TestColorFontInfo_HasAnyColorTable(t *testing.T) {
	tests := []struct {
		name string
		info ColorFontInfo
		want bool
	}{
		{
			name: "no tables",
			info: ColorFontInfo{},
			want: false,
		},
		{
			name: "has CBDT",
			info: ColorFontInfo{HasCBDT: true},
			want: true,
		},
		{
			name: "has sbix",
			info: ColorFontInfo{HasSbix: true},
			want: true,
		},
		{
			name: "has COLR",
			info: ColorFontInfo{HasCOLR: true},
			want: true,
		},
		{
			name: "has SVG",
			info: ColorFontInfo{HasSVG: true},
			want: true,
		},
		{
			name: "has multiple",
			info: ColorFontInfo{HasCBDT: true, HasCOLR: true},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.info.HasAnyColorTable(); got != tt.want {
				t.Errorf("HasAnyColorTable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestColorFontInfo_PreferredColorFormat(t *testing.T) {
	tests := []struct {
		name string
		info ColorFontInfo
		want string
	}{
		{
			name: "no tables",
			info: ColorFontInfo{},
			want: "",
		},
		{
			name: "CBDT only",
			info: ColorFontInfo{HasCBDT: true},
			want: "CBDT",
		},
		{
			name: "sbix only",
			info: ColorFontInfo{HasSbix: true},
			want: "sbix",
		},
		{
			name: "COLR only",
			info: ColorFontInfo{HasCOLR: true},
			want: "COLR",
		},
		{
			name: "SVG only",
			info: ColorFontInfo{HasSVG: true},
			want: "SVG",
		},
		{
			name: "CBDT preferred over COLR",
			info: ColorFontInfo{HasCBDT: true, HasCOLR: true},
			want: "CBDT",
		},
		{
			name: "sbix preferred over COLR",
			info: ColorFontInfo{HasSbix: true, HasCOLR: true},
			want: "sbix",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.info.PreferredColorFormat(); got != tt.want {
				t.Errorf("PreferredColorFormat() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDetectGlyphType_NoColorFont(t *testing.T) {
	// Test with a font that doesn't implement ColorFont.
	// Since we don't have a real font here, we test the fallback behavior.
	// A nil font should return GlyphTypeOutline.

	// We can't easily test with a real font without loading one,
	// but we can verify the function signature works.
	_ = DetectGlyphType
}

func TestCachedBitmap_Fields(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	cached := &CachedBitmap{
		Img:     img,
		OriginX: 1.5,
		OriginY: 28.5,
		Width:   32,
		Height:  32,
	}

	if cached.Width != 32 {
		t.Errorf("Width = %d, want 32", cached.Width)
	}
	if cached.Height != 32 {
		t.Errorf("Height = %d, want 32", cached.Height)
	}
	if cached.OriginX != 1.5 {
		t.Errorf("OriginX = %f, want 1.5", cached.OriginX)
	}
	if cached.OriginY != 28.5 {
		t.Errorf("OriginY = %f, want 28.5", cached.OriginY)
	}
}

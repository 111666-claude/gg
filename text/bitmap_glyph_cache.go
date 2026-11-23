package text

import (
	"container/list"
	"image"
	"sync"
	"sync/atomic"

	"github.com/gogpu/gg/text/emoji"
)

// Bitmap glyph cache configuration constants.
const (
	// bitmapCacheShardCount is the number of shards for reduced lock contention.
	// Must be a power of 2 for fast modulo via bitwise AND.
	bitmapCacheShardCount = 16

	// bitmapCacheShardMask selects a shard from a key hash (shardCount - 1).
	bitmapCacheShardMask = bitmapCacheShardCount - 1

	// DefaultBitmapGlyphCacheEntries is the default total entry budget
	// for the shared cache used by DrawWithEmoji.
	DefaultBitmapGlyphCacheEntries = 512

	// DefaultBitmapGlyphCacheBytes is the default total byte budget
	// for the shared cache used by DrawWithEmoji (64 MiB of RGBA pixels).
	DefaultBitmapGlyphCacheBytes = 64 << 20

	// bytesPerPixel estimates the in-memory cost of a decoded bitmap.
	// Decoded emoji are composited as RGBA, so 4 bytes per pixel is a
	// faithful upper bound for NRGBA/RGBA sources.
	bytesPerPixel = 4
)

// BitmapGlyphCache is a thread-safe, sharded LRU cache for decoded bitmap
// glyphs (emoji). It is safe for concurrent use by multiple goroutines.
//
// Design:
//   - 16 shards, each with its own mutex and LRU list, so a hot font does
//     not block lookups for unrelated fonts.
//   - Keys are (fontID, glyphID, ppem) triples.
//   - Eviction is least-recently-used within a shard, bounded by both an
//     entry limit and a byte limit (decoded pixel size). Evictions are
//     observable via Stats.
//   - A zero or negative capacity disables the cache: Get always misses
//     and Put is a no-op.
//
// Returned *CachedBitmap values are copies; the cache never mutates a
// struct after it has been handed out. The Img field is shared with the
// cache, so callers must treat it as read-only.
type BitmapGlyphCache struct {
	shards [bitmapCacheShardCount]bitmapGlyphShard

	maxEntriesPerShard int
	maxBytesPerShard   int64
	disabled           bool

	// flightMu guards flights, the single-flight load registry.
	flightMu sync.Mutex
	flights  map[bitmapCacheKey]*bitmapLoadFlight

	// Statistics (atomic for lock-free reads).
	hits      atomic.Uint64
	misses    atomic.Uint64
	evictions atomic.Uint64
}

// bitmapLoadFlight tracks an in-flight decode so concurrent misses on the
// same key decode exactly once (single-flight).
type bitmapLoadFlight struct {
	done  chan struct{}
	value *CachedBitmap // never handed out directly; followers get a copy
}

// bitmapGlyphShard is a single shard of the cache with its own lock.
type bitmapGlyphShard struct {
	mu      sync.Mutex
	entries map[bitmapCacheKey]*list.Element
	lru     *list.List // front = most recently used; values are bitmapCacheKey
	bytes   int64
}

// bitmapCacheEntry is the value stored in a shard's LRU element.
type bitmapCacheEntry struct {
	key   bitmapCacheKey
	value CachedBitmap
	bytes int64
}

// bitmapCacheKey uniquely identifies a cached bitmap.
type bitmapCacheKey struct {
	fontID  uintptr // Font identity (see ComputeFontID).
	glyphID uint16
	ppem    uint16
}

// hash mixes the key fields for shard selection.
func (k bitmapCacheKey) hash() uint64 {
	h := uint64(k.fontID)*0x9E3779B97F4A7C15 + uint64(k.glyphID)<<32 + uint64(k.ppem)
	h ^= h >> 29
	h *= 0xBF58476D1CE4E5B9
	h ^= h >> 32
	return h
}

// CachedBitmap holds a decoded bitmap glyph ready for compositing.
type CachedBitmap struct {
	Img     image.Image
	OriginX float32
	OriginY float32
	Width   int
	Height  int
	// PPEM is the pixels-per-em of the source strike the bitmap was
	// extracted from. The draw path scales by requestedPPEM / PPEM.
	PPEM uint16
}

// BitmapGlyphCacheStats reports cache usage and effectiveness.
type BitmapGlyphCacheStats struct {
	// Hits is the number of successful Get lookups.
	Hits uint64
	// Misses is the number of Get lookups that found nothing.
	Misses uint64
	// Evictions is the number of entries removed by LRU/byte pressure.
	Evictions uint64
	// Entries is the current number of cached entries.
	Entries int
	// Bytes is the current estimated size of cached decoded images.
	Bytes int64
	// MaxEntries is the total entry budget (0 means disabled).
	MaxEntries int
	// MaxBytes is the total byte budget (0 means disabled).
	MaxBytes int64
	// HitRate is Hits / (Hits + Misses), or 0 if no lookups occurred.
	HitRate float64
}

// NewBitmapGlyphCache creates a new bitmap glyph cache bounded by entry
// count, using the default byte budget (DefaultBitmapGlyphCacheBytes).
// maxSize <= 0 creates a disabled cache: Get always misses, Put is a no-op.
func NewBitmapGlyphCache(maxSize int) *BitmapGlyphCache {
	return NewBitmapGlyphCacheWithLimits(maxSize, DefaultBitmapGlyphCacheBytes)
}

// NewBitmapGlyphCacheWithLimits creates a new bitmap glyph cache bounded by
// both entry count and total decoded-pixel bytes. The budgets are divided
// evenly across shards; eviction happens per shard, so one busy font cannot
// evict entries of fonts hashed to other shards.
//
// maxEntries <= 0 or maxBytes <= 0 creates a disabled cache.
func NewBitmapGlyphCacheWithLimits(maxEntries int, maxBytes int64) *BitmapGlyphCache {
	c := &BitmapGlyphCache{}
	if maxEntries <= 0 || maxBytes <= 0 {
		c.disabled = true
		return c
	}
	c.maxEntriesPerShard = (maxEntries + bitmapCacheShardCount - 1) / bitmapCacheShardCount
	c.maxBytesPerShard = (maxBytes + bitmapCacheShardCount - 1) / bitmapCacheShardCount
	for i := range c.shards {
		c.shards[i].entries = make(map[bitmapCacheKey]*list.Element)
		c.shards[i].lru = list.New()
	}
	return c
}

// shardFor returns the shard responsible for a key.
func (c *BitmapGlyphCache) shardFor(key bitmapCacheKey) *bitmapGlyphShard {
	return &c.shards[key.hash()&bitmapCacheShardMask]
}

// Get retrieves a cached bitmap, or nil if not cached.
//
// The returned struct is a copy owned by the caller; mutating its fields
// does not affect the cache. The Img field is shared and must be treated
// as read-only. A hit moves the entry to the front of its shard's LRU.
func (c *BitmapGlyphCache) Get(fontID uintptr, glyphID, ppem uint16) *CachedBitmap {
	if c == nil || c.disabled {
		return nil
	}
	key := bitmapCacheKey{fontID: fontID, glyphID: glyphID, ppem: ppem}
	if value := c.lookup(key); value != nil {
		c.hits.Add(1)
		return value
	}
	c.misses.Add(1)
	return nil
}

// lookup returns a caller-owned copy of the cached value without
// touching statistics. The cache must be enabled.
func (c *BitmapGlyphCache) lookup(key bitmapCacheKey) *CachedBitmap {
	shard := c.shardFor(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()

	elem, ok := shard.entries[key]
	if !ok {
		return nil
	}
	shard.lru.MoveToFront(elem)
	value := elem.Value.(*bitmapCacheEntry).value // copy out under lock
	return &value
}

// Put stores a bitmap in the cache.
//
// Behavior contract:
//   - nil bitmap or nil img: no-op (decode failures are never cached).
//   - Duplicate key: the entry is replaced and becomes most-recently-used.
//   - Entry larger than the per-shard byte budget: no-op (never cacheable).
//   - Otherwise the LRU tail is evicted until both the entry and byte
//     budgets admit the new entry.
func (c *BitmapGlyphCache) Put(fontID uintptr, glyphID, ppem uint16, bitmap *emoji.BitmapGlyph, img image.Image) {
	if c == nil || c.disabled || bitmap == nil || img == nil {
		return
	}
	value := CachedBitmap{
		Img:     img,
		OriginX: bitmap.OriginX,
		OriginY: bitmap.OriginY,
		Width:   bitmap.Width,
		Height:  bitmap.Height,
		PPEM:    bitmap.PPEM,
	}
	c.putValue(bitmapCacheKey{fontID: fontID, glyphID: glyphID, ppem: ppem}, value)
}

// putValue inserts or replaces an entry. The cache must be enabled and
// value.Img must be non-nil.
func (c *BitmapGlyphCache) putValue(key bitmapCacheKey, value CachedBitmap) {
	bounds := value.Img.Bounds()
	if bounds.Empty() {
		return
	}
	entryBytes := int64(bounds.Dx()) * int64(bounds.Dy()) * bytesPerPixel
	if entryBytes > c.maxBytesPerShard {
		// Can never fit, even in an empty shard.
		return
	}

	shard := c.shardFor(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()

	// Duplicate Put: replace in place and refresh recency.
	if elem, ok := shard.entries[key]; ok {
		entry := elem.Value.(*bitmapCacheEntry)
		shard.bytes += entryBytes - entry.bytes
		entry.value = value
		entry.bytes = entryBytes
		shard.lru.MoveToFront(elem)
		return
	}

	// Evict LRU tail until the new entry fits both budgets.
	for shard.lru.Len() > 0 &&
		(len(shard.entries) >= c.maxEntriesPerShard || shard.bytes+entryBytes > c.maxBytesPerShard) {
		shard.evictOldest(c)
	}

	entry := &bitmapCacheEntry{key: key, value: value, bytes: entryBytes}
	elem := shard.lru.PushFront(entry)
	shard.entries[key] = elem
	shard.bytes += entryBytes
}

// evictOldest removes the least-recently-used entry from the shard.
// The shard lock must be held.
func (s *bitmapGlyphShard) evictOldest(c *BitmapGlyphCache) {
	elem := s.lru.Back()
	if elem == nil {
		return
	}
	entry := elem.Value.(*bitmapCacheEntry)
	s.lru.Remove(elem)
	delete(s.entries, entry.key)
	s.bytes -= entry.bytes
	c.evictions.Add(1)
}

// getOrLoad returns a cached bitmap, loading and caching it on a miss.
// It is the draw-path helper: a load or decode failure returns nil and is
// not cached, so a later call retries. Concurrent misses on the same key
// are single-flighted: exactly one goroutine decodes, the rest wait and
// share the result. The caller must not mutate the returned Img.
//
// Statistics: a call that triggers (or waits on a failed) font load counts
// as one miss; every other call counts as one hit.
func (c *BitmapGlyphCache) getOrLoad(colorFont ColorFont, fontID uintptr, glyphID, ppem uint16) *CachedBitmap {
	if c == nil || c.disabled {
		return loadBitmapGlyph(colorFont, glyphID, ppem)
	}
	key := bitmapCacheKey{fontID: fontID, glyphID: glyphID, ppem: ppem}
	if value := c.lookup(key); value != nil {
		c.hits.Add(1)
		return value
	}

	// Single-flight: join an in-flight load for this key if one exists.
	// The re-check of lookup under flightMu is load-bearing: the leader
	// stores the entry (putValue) before deregistering its flight under
	// flightMu, so observing "no flight" here implies a completed load is
	// already visible in the shard. Lock order is flightMu -> shard.mu;
	// no path takes them in the opposite order.
	c.flightMu.Lock()
	if flight, ok := c.flights[key]; ok {
		c.flightMu.Unlock()
		<-flight.done
		if flight.value == nil {
			c.misses.Add(1)
			return nil
		}
		c.hits.Add(1)
		value := *flight.value // caller-owned copy
		return &value
	}
	if value := c.lookup(key); value != nil {
		c.flightMu.Unlock()
		c.hits.Add(1)
		return value
	}
	flight := &bitmapLoadFlight{done: make(chan struct{})}
	if c.flights == nil {
		c.flights = make(map[bitmapCacheKey]*bitmapLoadFlight)
	}
	c.flights[key] = flight
	c.flightMu.Unlock()

	// Load outside any lock: decoding must not block other shards.
	value := loadBitmapGlyph(colorFont, glyphID, ppem)
	if value != nil {
		c.putValue(key, *value)
	}

	c.flightMu.Lock()
	delete(c.flights, key)
	c.flightMu.Unlock()
	flight.value = value
	close(flight.done)

	c.misses.Add(1)
	return value
}

// loadBitmapGlyph extracts and decodes a bitmap glyph. Failures return nil.
func loadBitmapGlyph(colorFont ColorFont, glyphID, ppem uint16) *CachedBitmap {
	bitmap, err := colorFont.BitmapGlyph(glyphID, ppem)
	if err != nil || bitmap == nil {
		return nil
	}
	img, err := bitmap.Decode()
	if err != nil || img == nil {
		return nil
	}
	return &CachedBitmap{
		Img:     img,
		OriginX: bitmap.OriginX,
		OriginY: bitmap.OriginY,
		Width:   bitmap.Width,
		Height:  bitmap.Height,
		PPEM:    bitmap.PPEM,
	}
}

// Clear removes all entries from the cache. Statistics are cumulative and
// are not reset; use ResetStats for that.
func (c *BitmapGlyphCache) Clear() {
	if c == nil || c.disabled {
		return
	}
	for i := range c.shards {
		shard := &c.shards[i]
		shard.mu.Lock()
		shard.entries = make(map[bitmapCacheKey]*list.Element)
		shard.lru.Init()
		shard.bytes = 0
		shard.mu.Unlock()
	}
}

// Size returns the number of cached entries.
func (c *BitmapGlyphCache) Size() int {
	if c == nil || c.disabled {
		return 0
	}
	total := 0
	for i := range c.shards {
		shard := &c.shards[i]
		shard.mu.Lock()
		total += len(shard.entries)
		shard.mu.Unlock()
	}
	return total
}

// Bytes returns the estimated total size of cached decoded images.
func (c *BitmapGlyphCache) Bytes() int64 {
	if c == nil || c.disabled {
		return 0
	}
	var total int64
	for i := range c.shards {
		shard := &c.shards[i]
		shard.mu.Lock()
		total += shard.bytes
		shard.mu.Unlock()
	}
	return total
}

// Stats returns a snapshot of cache statistics.
func (c *BitmapGlyphCache) Stats() BitmapGlyphCacheStats {
	if c == nil {
		return BitmapGlyphCacheStats{}
	}
	hits := c.hits.Load()
	misses := c.misses.Load()
	var hitRate float64
	if total := hits + misses; total > 0 {
		hitRate = float64(hits) / float64(total)
	}
	stats := BitmapGlyphCacheStats{
		Hits:      hits,
		Misses:    misses,
		Evictions: c.evictions.Load(),
		Entries:   c.Size(),
		Bytes:     c.Bytes(),
		HitRate:   hitRate,
	}
	if !c.disabled {
		stats.MaxEntries = c.maxEntriesPerShard * bitmapCacheShardCount
		stats.MaxBytes = c.maxBytesPerShard * bitmapCacheShardCount
	}
	return stats
}

// ResetStats resets the hit/miss/eviction counters to zero.
// Cached entries are not affected.
func (c *BitmapGlyphCache) ResetStats() {
	if c == nil {
		return
	}
	c.hits.Store(0)
	c.misses.Store(0)
	c.evictions.Store(0)
}

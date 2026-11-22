package recording

import (
	"image"
	"sync"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/text"
)

// Note: PathRef, BrushRef, ImageRef, and InvalidRef are defined in command.go

// FontRef is a reference to a pooled font face.
// The zero value is a valid reference to the first font (if any).
type FontRef uint32

// IsValid returns true if the reference is valid (not InvalidRef).
func (r FontRef) IsValid() bool {
	return uint32(r) != InvalidRef
}

// ResourcePool stores resources referenced by recording commands.
// Resources are stored in slices indexed by their reference types.
// Each Add operation clones mutable resources to ensure immutability.
//
// By default every Add appends a new entry and references follow insertion
// order. EnableDedup switches the pool to content-based deduplication:
// identical resources then share a single entry and reference. While dedup
// is enabled the pool is safe for concurrent use; otherwise ResourcePool is
// not safe for concurrent use and external synchronization must be provided.
type ResourcePool struct {
	paths   []*gg.Path
	brushes []Brush
	images  []image.Image
	fonts   []text.Face

	// Dedup state. The mutex is engaged only while dedup is enabled, so the
	// default pool keeps its original zero-synchronization cost.
	mu         sync.RWMutex
	dedup      bool
	pathIndex  map[uint64][]uint32
	brushIndex map[uint64][]uint32
	imageIndex map[uint64][]uint32
	fontIndex  map[uint64][]uint32
	stats      DedupStats
}

// NewResourcePool creates an empty resource pool with pre-allocated capacity.
// Deduplication is disabled; use EnableDedup to turn it on.
func NewResourcePool() *ResourcePool {
	return &ResourcePool{
		paths:   make([]*gg.Path, 0, 64),
		brushes: make([]Brush, 0, 32),
		images:  make([]image.Image, 0, 8),
		fonts:   make([]text.Face, 0, 4),
	}
}

// AddPath adds a path to the pool and returns its reference.
// The path is cloned to ensure immutability of the recording.
//
// With dedup enabled, a path whose content matches an existing entry returns
// that entry's reference and nothing is stored. If the pool is full
// (maxPoolResources entries), AddPath returns PathRef(InvalidRef).
func (p *ResourcePool) AddPath(path *gg.Path) PathRef {
	if !p.dedup {
		return p.appendPath(path)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	fp := pathFingerprint(path)
	for _, ref := range p.pathIndex[fp] {
		if pathsEqual(p.paths[ref], path) {
			p.stats.Hits++
			p.stats.SavedBytes += pathMemSize(path)
			return PathRef(ref)
		}
	}
	ref := p.appendPath(path)
	if ref.IsValid() {
		p.pathIndex[fp] = append(p.pathIndex[fp], uint32(ref))
		p.stats.Misses++
		p.stats.ExtraBytes += dedupEntryBytes
	}
	return ref
}

// appendPath stores the path (cloned unless nil) and returns its reference.
func (p *ResourcePool) appendPath(path *gg.Path) PathRef {
	if uint64(len(p.paths)) >= maxPoolResources {
		return PathRef(InvalidRef)
	}
	if path == nil {
		// Add nil as-is to preserve the reference
		p.paths = append(p.paths, nil)
	} else {
		// Clone path to ensure immutability
		p.paths = append(p.paths, path.Clone())
	}
	// #nosec G115 -- bounded by maxPoolResources, so the index fits uint32
	return PathRef(uint32(len(p.paths) - 1))
}

// GetPath returns the path for the given reference.
// Returns nil if the reference is invalid.
func (p *ResourcePool) GetPath(ref PathRef) *gg.Path {
	p.rlock()
	defer p.runlock()
	if int(ref) >= len(p.paths) {
		return nil
	}
	return p.paths[ref]
}

// PathCount returns the number of paths in the pool.
func (p *ResourcePool) PathCount() int {
	p.rlock()
	defer p.runlock()
	return len(p.paths)
}

// AddBrush adds a brush to the pool and returns its reference.
// Brushes are stored directly as they are typically immutable value types.
//
// With dedup enabled, a brush whose content matches an existing entry
// returns that entry's reference. If the pool is full, AddBrush returns
// BrushRef(InvalidRef).
func (p *ResourcePool) AddBrush(brush Brush) BrushRef {
	if !p.dedup {
		return p.appendBrush(brush)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	fp := brushFingerprint(brush)
	for _, ref := range p.brushIndex[fp] {
		if brushEqual(p.brushes[ref], brush) {
			p.stats.Hits++
			p.stats.SavedBytes += brushMemSize(brush)
			return BrushRef(ref)
		}
	}
	ref := p.appendBrush(brush)
	if ref.IsValid() {
		p.brushIndex[fp] = append(p.brushIndex[fp], uint32(ref))
		p.stats.Misses++
		p.stats.ExtraBytes += dedupEntryBytes
	}
	return ref
}

func (p *ResourcePool) appendBrush(brush Brush) BrushRef {
	if uint64(len(p.brushes)) >= maxPoolResources {
		return BrushRef(InvalidRef)
	}
	p.brushes = append(p.brushes, brush)
	// #nosec G115 -- bounded by maxPoolResources, so the index fits uint32
	return BrushRef(uint32(len(p.brushes) - 1))
}

// GetBrush returns the brush for the given reference.
// Returns nil if the reference is invalid.
func (p *ResourcePool) GetBrush(ref BrushRef) Brush {
	p.rlock()
	defer p.runlock()
	if int(ref) >= len(p.brushes) {
		return nil
	}
	return p.brushes[ref]
}

// BrushCount returns the number of brushes in the pool.
func (p *ResourcePool) BrushCount() int {
	p.rlock()
	defer p.runlock()
	return len(p.brushes)
}

// AddImage adds an image to the pool and returns its reference.
// Images are stored directly as Go's image.Image is already immutable.
//
// With dedup enabled, images are compared by bounds, color model, and pixel
// content — never by pointer — so two distinct image values with identical
// pixels share one entry. If the pool is full, AddImage returns
// ImageRef(InvalidRef).
func (p *ResourcePool) AddImage(img image.Image) ImageRef {
	if !p.dedup {
		return p.appendImage(img)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	fp := imageFingerprint(img)
	for _, ref := range p.imageIndex[fp] {
		if imagesEqual(p.images[ref], img) {
			p.stats.Hits++
			p.stats.SavedBytes += refSlotBytes
			return ImageRef(ref)
		}
	}
	ref := p.appendImage(img)
	if ref.IsValid() {
		p.imageIndex[fp] = append(p.imageIndex[fp], uint32(ref))
		p.stats.Misses++
		p.stats.ExtraBytes += dedupEntryBytes
	}
	return ref
}

func (p *ResourcePool) appendImage(img image.Image) ImageRef {
	if uint64(len(p.images)) >= maxPoolResources {
		return ImageRef(InvalidRef)
	}
	p.images = append(p.images, img)
	// #nosec G115 -- bounded by maxPoolResources, so the index fits uint32
	return ImageRef(uint32(len(p.images) - 1))
}

// GetImage returns the image for the given reference.
// Returns nil if the reference is invalid.
func (p *ResourcePool) GetImage(ref ImageRef) image.Image {
	p.rlock()
	defer p.runlock()
	if int(ref) >= len(p.images) {
		return nil
	}
	return p.images[ref]
}

// ImageCount returns the number of images in the pool.
func (p *ResourcePool) ImageCount() int {
	p.rlock()
	defer p.runlock()
	return len(p.images)
}

// AddFont adds a font face to the pool and returns its reference.
// Font faces are stored directly as they are already immutable and safe to share.
//
// With dedup enabled, faces sharing the same FontSource and configuration
// (size, direction, language, features, variations) share one entry. If the
// pool is full, AddFont returns FontRef(InvalidRef).
func (p *ResourcePool) AddFont(face text.Face) FontRef {
	if !p.dedup {
		return p.appendFont(face)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	fp := faceFingerprint(face)
	for _, ref := range p.fontIndex[fp] {
		if facesEqual(p.fonts[ref], face) {
			p.stats.Hits++
			p.stats.SavedBytes += refSlotBytes
			return FontRef(ref)
		}
	}
	ref := p.appendFont(face)
	if ref.IsValid() {
		p.fontIndex[fp] = append(p.fontIndex[fp], uint32(ref))
		p.stats.Misses++
		p.stats.ExtraBytes += dedupEntryBytes
	}
	return ref
}

func (p *ResourcePool) appendFont(face text.Face) FontRef {
	if uint64(len(p.fonts)) >= maxPoolResources {
		return FontRef(InvalidRef)
	}
	p.fonts = append(p.fonts, face)
	// #nosec G115 -- bounded by maxPoolResources, so the index fits uint32
	return FontRef(uint32(len(p.fonts) - 1))
}

// GetFont returns the font face for the given reference.
// Returns nil if the reference is invalid.
func (p *ResourcePool) GetFont(ref FontRef) text.Face {
	p.rlock()
	defer p.runlock()
	if int(ref) >= len(p.fonts) {
		return nil
	}
	return p.fonts[ref]
}

// FontCount returns the number of font faces in the pool.
func (p *ResourcePool) FontCount() int {
	p.rlock()
	defer p.runlock()
	return len(p.fonts)
}

// Clear removes all resources from the pool.
// This does not release the underlying memory; use NewResourcePool for that.
// Dedup indexes and statistics are reset; the dedup on/off setting is kept.
func (p *ResourcePool) Clear() {
	p.lock()
	defer p.unlock()
	p.paths = p.paths[:0]
	p.brushes = p.brushes[:0]
	p.images = p.images[:0]
	p.fonts = p.fonts[:0]
	if p.dedup {
		p.pathIndex = make(map[uint64][]uint32)
		p.brushIndex = make(map[uint64][]uint32)
		p.imageIndex = make(map[uint64][]uint32)
		p.fontIndex = make(map[uint64][]uint32)
		p.stats = DedupStats{Enabled: true}
	}
}

// Clone creates a deep copy of the resource pool.
// Paths are cloned; other resources are copied by reference (they are immutable).
// The clone never shares a mutable *gg.Path with the original, and it
// preserves the dedup setting with a freshly built index.
func (p *ResourcePool) Clone() *ResourcePool {
	p.rlock()
	defer p.runlock()
	clone := &ResourcePool{
		paths:   make([]*gg.Path, len(p.paths)),
		brushes: make([]Brush, len(p.brushes)),
		images:  make([]image.Image, len(p.images)),
		fonts:   make([]text.Face, len(p.fonts)),
		dedup:   p.dedup,
		stats:   p.stats,
	}

	// Clone paths (mutable)
	for i, path := range p.paths {
		if path != nil {
			clone.paths[i] = path.Clone()
		}
	}

	// Copy brushes directly (immutable structs)
	copy(clone.brushes, p.brushes)

	// Copy images and fonts by reference (already immutable)
	copy(clone.images, p.images)
	copy(clone.fonts, p.fonts)

	// Rebuild the dedup index against the cloned resources so the clone
	// never shares index state with the original.
	if p.dedup {
		clone.rebuildIndexLocked()
	}

	return clone
}

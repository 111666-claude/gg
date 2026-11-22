package recording

import (
	"image"
	"math"
	"reflect"
	"slices"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/text"
)

// This file implements optional content-based deduplication for ResourcePool.
//
// When deduplication is enabled (EnableDedup), every Add call computes a
// stable fingerprint (FNV-1a over the full resource content) and, on a
// fingerprint hit, verifies the complete content before returning the
// existing reference. Hash collisions therefore never produce false sharing:
// a collision simply falls through to storing a new resource.
//
// Deterministic edge-case handling:
//   - nil resources (nil path, nil brush, nil image, nil face) share a single
//     reserved fingerprint and deduplicate against each other.
//   - Empty resources (empty path, zero-stop gradient, 0x0 image) are
//     fingerprinted like any other content and deduplicate normally.
//   - When a resource slice reaches maxPoolResources entries, Add returns a
//     reference equal to InvalidRef and stores nothing.

// maxPoolResources bounds each resource slice so every index stays
// representable as a valid uint32 reference (InvalidRef is the sentinel and
// can never be a valid index). It is a variable so tests can exercise the
// overflow path without allocating 4 billion entries.
var maxPoolResources = uint64(InvalidRef)

// DedupStats reports resource deduplication activity for a ResourcePool.
type DedupStats struct {
	// Enabled reports whether deduplication is currently enabled.
	Enabled bool
	// Hits is the number of Add calls that reused an existing resource.
	Hits uint64
	// Misses is the number of Add calls that stored a new unique resource
	// while deduplication was enabled.
	Misses uint64
	// SavedBytes estimates the memory not allocated thanks to reuse
	// (cloned paths, brush storage, reference slots).
	SavedBytes uint64
	// ExtraBytes estimates the memory used by the deduplication hash tables.
	ExtraBytes uint64
}

// Memory accounting constants (approximate, amortized).
const (
	pathHeaderBytes  = 144 // gg.Path struct with embedded buffers
	brushHeaderBytes = 48  // brush struct without stops
	stopBytes        = 16  // one GradientStop
	refSlotBytes     = 16  // one interface/pointer slot in a pool slice
	dedupEntryBytes  = 56  // map bucket share + ref slice element, amortized
)

// --------------------------------------------------------------------------
// FNV-1a helpers (stdlib-equivalent, allocation-free)
// --------------------------------------------------------------------------

const (
	fnvOffsetBasis64 = 14695981039346656037
	fnvPrime64       = 1099511628211
	// nilFingerprint is the reserved fingerprint for nil resources.
	nilFingerprint = 0
)

func fnvAddByte(h uint64, b byte) uint64 {
	return (h ^ uint64(b)) * fnvPrime64
}

func fnvAddUint64(h, v uint64) uint64 {
	for i := 0; i < 8; i++ {
		h = fnvAddByte(h, byte(v>>(8*i)))
	}
	return h
}

func fnvAddInt(h uint64, v int) uint64 {
	return fnvAddUint64(h, uint64(int64(v)))
}

func fnvAddFloat64(h uint64, f float64) uint64 {
	return fnvAddUint64(h, math.Float64bits(f))
}

func fnvAddFloat32(h uint64, f float32) uint64 {
	return fnvAddUint64(h, uint64(math.Float32bits(f)))
}

func fnvAddString(h uint64, s string) uint64 {
	for i := 0; i < len(s); i++ {
		h = fnvAddByte(h, s[i])
	}
	return h
}

// --------------------------------------------------------------------------
// Path fingerprint and equality
// --------------------------------------------------------------------------

// pathFingerprint hashes the complete verb and coordinate streams.
// Coordinates are hashed by their exact bit patterns, so the fingerprint is
// stable for identical content regardless of the *gg.Path pointer.
func pathFingerprint(p *gg.Path) uint64 {
	if p == nil {
		return nilFingerprint
	}
	h := uint64(fnvOffsetBasis64)
	verbs := p.Verbs()
	coords := p.Coords()
	h = fnvAddInt(h, len(verbs))
	for _, v := range verbs {
		h = fnvAddByte(h, byte(v))
	}
	h = fnvAddInt(h, len(coords))
	for _, c := range coords {
		h = fnvAddFloat64(h, c)
	}
	return h
}

// pathsEqual reports whether two paths have identical verb and coordinate
// streams. Coordinates compare by bit pattern, consistent with the hash.
func pathsEqual(a, b *gg.Path) bool {
	if a == nil || b == nil {
		return a == b
	}
	av, bv := a.Verbs(), b.Verbs()
	if len(av) != len(bv) {
		return false
	}
	for i := range av {
		if av[i] != bv[i] {
			return false
		}
	}
	ac, bc := a.Coords(), b.Coords()
	if len(ac) != len(bc) {
		return false
	}
	for i := range ac {
		if math.Float64bits(ac[i]) != math.Float64bits(bc[i]) {
			return false
		}
	}
	return true
}

func pathMemSize(p *gg.Path) uint64 {
	if p == nil {
		return refSlotBytes
	}
	return pathHeaderBytes + uint64(len(p.Verbs())) + uint64(len(p.Coords()))*8
}

// --------------------------------------------------------------------------
// Brush fingerprint and equality
// --------------------------------------------------------------------------

// Brush type tags for fingerprinting.
const (
	brushTagSolid = iota + 1
	brushTagLinear
	brushTagRadial
	brushTagSweep
	brushTagPattern
	brushTagUnknown
)

// brushFingerprint hashes the full brush definition. Pointer and value forms
// of the same brush type fingerprint identically; nil (including typed-nil
// pointers) maps to the reserved nil fingerprint.
func brushFingerprint(b Brush) uint64 {
	switch br := normalizeBrush(b).(type) {
	case nil:
		return nilFingerprint
	case SolidBrush:
		h := fnvAddByte(fnvOffsetBasis64, brushTagSolid)
		return rgbaFingerprint(h, br.Color)
	case LinearGradientBrush:
		h := fnvAddByte(fnvOffsetBasis64, brushTagLinear)
		h = pointFingerprint(h, br.Start)
		h = pointFingerprint(h, br.End)
		h = fnvAddInt(h, int(br.Extend))
		return stopsFingerprint(h, br.Stops)
	case RadialGradientBrush:
		h := fnvAddByte(fnvOffsetBasis64, brushTagRadial)
		h = pointFingerprint(h, br.Center)
		h = pointFingerprint(h, br.Focus)
		h = fnvAddFloat64(h, br.StartRadius)
		h = fnvAddFloat64(h, br.EndRadius)
		h = fnvAddInt(h, int(br.Extend))
		return stopsFingerprint(h, br.Stops)
	case SweepGradientBrush:
		h := fnvAddByte(fnvOffsetBasis64, brushTagSweep)
		h = pointFingerprint(h, br.Center)
		h = fnvAddFloat64(h, br.StartAngle)
		h = fnvAddFloat64(h, br.EndAngle)
		h = fnvAddInt(h, int(br.Extend))
		return stopsFingerprint(h, br.Stops)
	case PatternBrush:
		h := fnvAddByte(fnvOffsetBasis64, brushTagPattern)
		h = fnvAddUint64(h, uint64(br.Image))
		h = fnvAddInt(h, int(br.Repeat))
		return matrixFingerprint(h, br.Transform)
	default:
		// The interface is sealed, but stay defensive: distinct dynamic
		// types fingerprint by their type name.
		return fnvAddString(fnvAddByte(fnvOffsetBasis64, brushTagUnknown),
			reflect.TypeOf(b).String())
	}
}

// normalizeBrush converts pointer forms of the built-in brush types to their
// value forms and maps typed-nil pointers to nil.
func normalizeBrush(b Brush) Brush {
	switch br := b.(type) {
	case *SolidBrush:
		if br == nil {
			return nil
		}
		return *br
	case *LinearGradientBrush:
		if br == nil {
			return nil
		}
		return *br
	case *RadialGradientBrush:
		if br == nil {
			return nil
		}
		return *br
	case *SweepGradientBrush:
		if br == nil {
			return nil
		}
		return *br
	case *PatternBrush:
		if br == nil {
			return nil
		}
		return *br
	default:
		return b
	}
}

func rgbaFingerprint(h uint64, c gg.RGBA) uint64 {
	h = fnvAddFloat64(h, c.R)
	h = fnvAddFloat64(h, c.G)
	h = fnvAddFloat64(h, c.B)
	return fnvAddFloat64(h, c.A)
}

func pointFingerprint(h uint64, p gg.Point) uint64 {
	h = fnvAddFloat64(h, p.X)
	return fnvAddFloat64(h, p.Y)
}

func matrixFingerprint(h uint64, m gg.Matrix) uint64 {
	h = fnvAddFloat64(h, m.A)
	h = fnvAddFloat64(h, m.B)
	h = fnvAddFloat64(h, m.C)
	h = fnvAddFloat64(h, m.D)
	h = fnvAddFloat64(h, m.E)
	return fnvAddFloat64(h, m.F)
}

func stopsFingerprint(h uint64, stops []GradientStop) uint64 {
	h = fnvAddInt(h, len(stops))
	for _, s := range stops {
		h = fnvAddFloat64(h, s.Offset)
		h = rgbaFingerprint(h, s.Color)
	}
	return h
}

// brushEqual reports whether two brushes define identical content.
// Pointer and value forms compare equal; nil stops and empty stops are equal
// (consistent with the fingerprint, which hashes only the stop count).
func brushEqual(a, b Brush) bool {
	an, bn := normalizeBrush(a), normalizeBrush(b)
	if an == nil || bn == nil {
		return an == nil && bn == nil
	}
	switch av := an.(type) {
	case SolidBrush:
		bv, ok := bn.(SolidBrush)
		return ok && av == bv
	case LinearGradientBrush:
		bv, ok := bn.(LinearGradientBrush)
		return ok && av.Start == bv.Start && av.End == bv.End &&
			av.Extend == bv.Extend && slices.Equal(av.Stops, bv.Stops)
	case RadialGradientBrush:
		bv, ok := bn.(RadialGradientBrush)
		return ok && av.Center == bv.Center && av.Focus == bv.Focus &&
			av.StartRadius == bv.StartRadius && av.EndRadius == bv.EndRadius &&
			av.Extend == bv.Extend && slices.Equal(av.Stops, bv.Stops)
	case SweepGradientBrush:
		bv, ok := bn.(SweepGradientBrush)
		return ok && av.Center == bv.Center && av.StartAngle == bv.StartAngle &&
			av.EndAngle == bv.EndAngle && av.Extend == bv.Extend &&
			slices.Equal(av.Stops, bv.Stops)
	case PatternBrush:
		bv, ok := bn.(PatternBrush)
		return ok && av.Image == bv.Image && av.Repeat == bv.Repeat &&
			av.Transform == bv.Transform
	default:
		return false
	}
}

func brushMemSize(b Brush) uint64 {
	switch br := normalizeBrush(b).(type) {
	case nil:
		return refSlotBytes
	case SolidBrush:
		return brushHeaderBytes
	case LinearGradientBrush:
		return brushHeaderBytes + uint64(len(br.Stops))*stopBytes
	case RadialGradientBrush:
		return brushHeaderBytes + uint64(len(br.Stops))*stopBytes
	case SweepGradientBrush:
		return brushHeaderBytes + uint64(len(br.Stops))*stopBytes
	case PatternBrush:
		return brushHeaderBytes
	default:
		return brushHeaderBytes
	}
}

// --------------------------------------------------------------------------
// Image fingerprint and equality
// --------------------------------------------------------------------------

// Image type tags. The tag participates in the fingerprint so that two
// images with identical visible pixels but different color models
// (e.g. *image.RGBA vs *image.NRGBA, or *image.Gray vs *image.RGBA)
// never deduplicate together.
const (
	imgTagUnknown = iota + 1
	imgTagRGBA
	imgTagNRGBA
	imgTagAlpha
	imgTagGray
	imgTagPaletted
)

func imageTypeTag(img image.Image) int {
	switch img.(type) {
	case *image.RGBA:
		return imgTagRGBA
	case *image.NRGBA:
		return imgTagNRGBA
	case *image.Alpha:
		return imgTagAlpha
	case *image.Gray:
		return imgTagGray
	case *image.Paletted:
		return imgTagPaletted
	default:
		return imgTagUnknown
	}
}

// imageFingerprint hashes the image by bounds, concrete color model, and
// full pixel content — never by pointer. Two distinct *image.RGBA values
// with the same pixels fingerprint identically.
func imageFingerprint(img image.Image) uint64 {
	if img == nil {
		return nilFingerprint
	}
	h := uint64(fnvOffsetBasis64)
	h = fnvAddInt(h, imageTypeTag(img))
	b := img.Bounds()
	h = fnvAddInt(h, b.Min.X)
	h = fnvAddInt(h, b.Min.Y)
	h = fnvAddInt(h, b.Max.X)
	h = fnvAddInt(h, b.Max.Y)
	switch im := img.(type) {
	case *image.RGBA:
		return hashVisiblePixels(h, im.Pix, im.Stride, im.Rect, 4)
	case *image.NRGBA:
		return hashVisiblePixels(h, im.Pix, im.Stride, im.Rect, 4)
	case *image.Alpha:
		return hashVisiblePixels(h, im.Pix, im.Stride, im.Rect, 1)
	case *image.Gray:
		return hashVisiblePixels(h, im.Pix, im.Stride, im.Rect, 1)
	case *image.Paletted:
		h = fnvAddInt(h, len(im.Palette))
		for _, c := range im.Palette {
			r, g, bl, a := c.RGBA()
			h = fnvAddUint64(h, uint64(r))
			h = fnvAddUint64(h, uint64(g))
			h = fnvAddUint64(h, uint64(bl))
			h = fnvAddUint64(h, uint64(a))
		}
		return hashVisiblePixels(h, im.Pix, im.Stride, im.Rect, 1)
	default:
		// Generic path: hash every pixel through the color model.
		return hashPixelsGeneric(h, img)
	}
}

// hashVisiblePixels hashes only the bytes visible within rect, row by row,
// so subimages with different strides but identical content match.
// Pix[0] corresponds to rect.Min, so the row offset is (y-rect.Min.Y)*stride.
func hashVisiblePixels(h uint64, pix []byte, stride int, rect image.Rectangle, bpp int) uint64 {
	rowLen := rect.Dx() * bpp
	for y := 0; y < rect.Dy(); y++ {
		row := pix[y*stride : y*stride+rowLen]
		for _, v := range row {
			h = fnvAddByte(h, v)
		}
	}
	return h
}

func hashPixelsGeneric(h uint64, img image.Image) uint64 {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			h = fnvAddUint64(h, uint64(r))
			h = fnvAddUint64(h, uint64(g))
			h = fnvAddUint64(h, uint64(bl))
			h = fnvAddUint64(h, uint64(a))
		}
	}
	return h
}

// imagesEqual performs the full content confirmation behind imageFingerprint.
func imagesEqual(a, b image.Image) bool {
	if a == nil || b == nil {
		return a == b
	}
	if imageTypeTag(a) != imageTypeTag(b) {
		return false
	}
	if a.Bounds() != b.Bounds() {
		return false
	}
	switch am := a.(type) {
	case *image.RGBA:
		return visiblePixelsEqual(am, b.(*image.RGBA), 4)
	case *image.NRGBA:
		return visiblePixelsEqual(am, b.(*image.NRGBA), 4)
	case *image.Alpha:
		return visiblePixelsEqual(am, b.(*image.Alpha), 1)
	case *image.Gray:
		return visiblePixelsEqual(am, b.(*image.Gray), 1)
	case *image.Paletted:
		bm := b.(*image.Paletted)
		if len(am.Palette) != len(bm.Palette) {
			return false
		}
		for i, c := range am.Palette {
			r1, g1, b1, a1 := c.RGBA()
			r2, g2, b2, a2 := bm.Palette[i].RGBA()
			if r1 != r2 || g1 != g2 || b1 != b2 || a1 != a2 {
				return false
			}
		}
		return visiblePixelsEqual(am, bm, 1)
	default:
		bounds := a.Bounds()
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				r1, g1, b1, a1 := a.At(x, y).RGBA()
				r2, g2, b2, a2 := b.At(x, y).RGBA()
				if r1 != r2 || g1 != g2 || b1 != b2 || a1 != a2 {
					return false
				}
			}
		}
		return true
	}
}

// visiblePixelsEqual compares the visible pixels of two images that share
// the same concrete type and bounds, tolerating different strides.
func visiblePixelsEqual(a, b image.Image, bpp int) bool {
	switch am := a.(type) {
	case *image.RGBA:
		bm := b.(*image.RGBA)
		return rowsEqual(am.Pix, am.Stride, bm.Pix, bm.Stride, am.Rect, 4)
	case *image.NRGBA:
		bm := b.(*image.NRGBA)
		return rowsEqual(am.Pix, am.Stride, bm.Pix, bm.Stride, am.Rect, 4)
	case *image.Alpha:
		bm := b.(*image.Alpha)
		return rowsEqual(am.Pix, am.Stride, bm.Pix, bm.Stride, am.Rect, 1)
	case *image.Gray:
		bm := b.(*image.Gray)
		return rowsEqual(am.Pix, am.Stride, bm.Pix, bm.Stride, am.Rect, 1)
	case *image.Paletted:
		bm := b.(*image.Paletted)
		return rowsEqual(am.Pix, am.Stride, bm.Pix, bm.Stride, am.Rect, 1)
	}
	return false
}

// rowsEqual compares the visible pixel rows of two images with equal bounds,
// tolerating different strides and subimage offsets.
func rowsEqual(aPix []byte, aStride int, bPix []byte, bStride int, rect image.Rectangle, bpp int) bool {
	rowLen := rect.Dx() * bpp
	for y := 0; y < rect.Dy(); y++ {
		aRow := aPix[y*aStride : y*aStride+rowLen]
		bRow := bPix[y*bStride : y*bStride+rowLen]
		if !slices.Equal(aRow, bRow) {
			return false
		}
	}
	return true
}

// --------------------------------------------------------------------------
// Font fingerprint and equality
// --------------------------------------------------------------------------

// faceFingerprint hashes the face configuration (size, direction, language,
// features, variations). The font source identity is deliberately not hashed;
// it is verified by pointer in facesEqual, so faces from different
// FontSources share a bucket and are disambiguated on confirmation.
func faceFingerprint(face text.Face) uint64 {
	if isNilFace(face) {
		return nilFingerprint
	}
	h := uint64(fnvOffsetBasis64)
	h = fnvAddFloat64(h, face.Size())
	h = fnvAddInt(h, int(face.Direction()))
	h = fnvAddString(h, face.Language())
	features := face.Features()
	h = fnvAddInt(h, len(features))
	for _, f := range features {
		for _, b := range f.Tag {
			h = fnvAddByte(h, b)
		}
		h = fnvAddUint64(h, uint64(f.Value))
	}
	variations := face.Variations()
	h = fnvAddInt(h, len(variations))
	for _, v := range variations {
		for _, b := range v.Tag {
			h = fnvAddByte(h, b)
		}
		h = fnvAddFloat32(h, v.Value)
	}
	return h
}

// facesEqual performs the full content confirmation behind faceFingerprint.
// Two faces are equal when they share the same FontSource (the heavyweight
// font data identity) and have identical configuration.
func facesEqual(a, b text.Face) bool {
	an, bn := isNilFace(a), isNilFace(b)
	if an || bn {
		return an == bn
	}
	if a.Source() != b.Source() {
		return false
	}
	if a.Size() != b.Size() || a.Direction() != b.Direction() || a.Language() != b.Language() {
		return false
	}
	if !slices.Equal(a.Features(), b.Features()) {
		return false
	}
	return slices.Equal(a.Variations(), b.Variations())
}

// isNilFace reports whether the face is nil, including a typed-nil pointer
// stored in the interface.
func isNilFace(face text.Face) bool {
	if face == nil {
		return true
	}
	v := reflect.ValueOf(face)
	return v.Kind() == reflect.Ptr && v.IsNil()
}

// --------------------------------------------------------------------------
// ResourcePool dedup API
// --------------------------------------------------------------------------

// EnableDedup turns content-based deduplication on or off.
//
// When enabled, Add calls fingerprint the resource content and return the
// existing reference on a confirmed content match instead of appending a
// copy. When disabled (the default), every Add appends and references follow
// insertion order — the historical behavior.
//
// Enabling builds the lookup index from the resources already in the pool,
// so resources added before enabling can still match later Adds. Disabling
// keeps every previously returned reference valid; new Adds append again.
//
// While deduplication is enabled the pool is safe for concurrent use
// (Adds, Gets, and stats are mutex-protected). EnableDedup itself is a
// setup-time operation and must not race with other pool calls.
func (p *ResourcePool) EnableDedup(enabled bool) {
	if enabled == p.dedup {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dedup = enabled
	if enabled {
		p.rebuildIndexLocked()
		p.stats = DedupStats{Enabled: true}
	} else {
		p.pathIndex, p.brushIndex, p.imageIndex, p.fontIndex = nil, nil, nil, nil
		p.stats.Enabled = false
	}
}

// DedupEnabled reports whether content deduplication is enabled.
func (p *ResourcePool) DedupEnabled() bool {
	return p.dedup
}

// DedupStats returns a snapshot of the deduplication statistics.
func (p *ResourcePool) DedupStats() DedupStats {
	if p.dedup {
		p.mu.RLock()
		defer p.mu.RUnlock()
	}
	return p.stats
}

// rebuildIndexLocked recomputes all dedup indexes from the stored resources.
// p.mu must be held (or the pool must be single-threaded) by the caller.
func (p *ResourcePool) rebuildIndexLocked() {
	p.pathIndex = make(map[uint64][]uint32, len(p.paths))
	p.brushIndex = make(map[uint64][]uint32, len(p.brushes))
	p.imageIndex = make(map[uint64][]uint32, len(p.images))
	p.fontIndex = make(map[uint64][]uint32, len(p.fonts))
	entries := 0
	for i, path := range p.paths {
		fp := pathFingerprint(path)
		p.pathIndex[fp] = append(p.pathIndex[fp], uint32(i)) // #nosec G115 -- bounded by maxPoolResources
		entries++
	}
	for i, brush := range p.brushes {
		fp := brushFingerprint(brush)
		p.brushIndex[fp] = append(p.brushIndex[fp], uint32(i)) // #nosec G115 -- bounded by maxPoolResources
		entries++
	}
	for i, img := range p.images {
		fp := imageFingerprint(img)
		p.imageIndex[fp] = append(p.imageIndex[fp], uint32(i)) // #nosec G115 -- bounded by maxPoolResources
		entries++
	}
	for i, face := range p.fonts {
		fp := faceFingerprint(face)
		p.fontIndex[fp] = append(p.fontIndex[fp], uint32(i)) // #nosec G115 -- bounded by maxPoolResources
		entries++
	}
	p.stats.ExtraBytes = uint64(entries) * dedupEntryBytes
}

// --------------------------------------------------------------------------
// Locking helpers — engaged only while dedup is enabled, so the default
// (non-dedup) pool keeps its original zero-synchronization cost.
// --------------------------------------------------------------------------

func (p *ResourcePool) lock() {
	if p.dedup {
		p.mu.Lock()
	}
}

func (p *ResourcePool) unlock() {
	if p.dedup {
		p.mu.Unlock()
	}
}

func (p *ResourcePool) rlock() {
	if p.dedup {
		p.mu.RLock()
	}
}

func (p *ResourcePool) runlock() {
	if p.dedup {
		p.mu.RUnlock()
	}
}

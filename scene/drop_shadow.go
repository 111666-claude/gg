package scene

import (
	"math"
	"sync"

	"github.com/gogpu/gg"
)

// Working-buffer safety limits. They only reject pathological inputs (absurd
// bounds or radii) so Apply degrades to a source-over fallback instead of
// attempting a multi-hundred-megabyte allocation or panicking.
const (
	// maxShadowDim caps a single shadow working-buffer dimension.
	maxShadowDim = 1 << 15 // 32768
	// maxShadowPixels caps the total number of shadow working pixels.
	// A float32 RGBA working buffer at this size occupies ~256 MiB.
	maxShadowPixels = 1 << 24 // ~16.7M
	// maxKernelHalf caps the Gaussian kernel half-size. Larger radii fall
	// back to source-over rather than spending tens of thousands of taps
	// per pixel.
	maxKernelHalf = 8192
	// fnvOffset32/fnvPrime32 are the 32-bit FNV-1a constants used by Hash.
	fnvOffset32 = uint64(2166136261)
	fnvPrime32  = uint64(16777619)
)

// DropShadowFilter renders a configurable drop shadow derived from the source
// alpha channel and composites it *under* the original content.
//
// The pipeline is:
//  1. Extract the source alpha, resampled at (x - OffsetX, y - OffsetY) with
//     bilinear interpolation so fractional offsets stay smooth.
//  2. Blur the shifted alpha with a separable Gaussian. Sampling past the
//     source footprint contributes zero, so a shadow never grows outside the
//     shifted source shape.
//  3. Scale it by Color.A * Intensity and colorize with the (straight-alpha)
//     Color.
//  4. Composite shadow-over-backdrop, then source-over-shadow. The source is
//     always on top: a shadow can never cover source pixels.
//
// All math runs in premultiplied-alpha space on the raw Pixmap bytes; pixels
// outside the shadow/source support are left byte-for-byte untouched, so the
// shadow never pollutes transparent pixels of the original layer.
//
// DropShadowFilter is safe for repeated Apply calls and supports src == dst
// (in-place): shadow alpha is fully built before any destination byte is
// written.
type DropShadowFilter struct {
	// OffsetX is the horizontal shadow displacement in pixels. May be negative.
	OffsetX float64

	// OffsetY is the vertical shadow displacement in pixels. May be negative.
	OffsetY float64

	// BlurRadius is the Gaussian sigma (standard deviation) in pixels.
	// Zero produces a hard-edged shadow. Negative or non-finite values are
	// treated as zero.
	BlurRadius float64

	// Intensity multiplies the shadow alpha. Zero (or negative, or
	// non-finite) disables the shadow entirely, leaving a plain
	// source-over-backdrop composite.
	Intensity float64

	// Color is the straight-alpha shadow color. Its alpha modulates the
	// blurred source alpha together with Intensity.
	Color gg.RGBA
}

// Compile-time guarantee that DropShadowFilter satisfies the Filter interface.
var _ Filter = (*DropShadowFilter)(nil)

// NewDropShadowFilter creates a drop shadow with full intensity (1.0).
// Common usage:
//
//	NewDropShadowFilter(3, 3, 5, gg.RGBA2(0, 0, 0, 0.5))
func NewDropShadowFilter(offsetX, offsetY, blurRadius float64, color gg.RGBA) *DropShadowFilter {
	return &DropShadowFilter{
		OffsetX:    offsetX,
		OffsetY:    offsetY,
		BlurRadius: blurRadius,
		Intensity:  1.0,
		Color:      color,
	}
}

// NewDropShadowFilterIntensity creates a drop shadow with an explicit
// intensity multiplier on top of the shadow color alpha.
func NewDropShadowFilterIntensity(offsetX, offsetY, blurRadius, intensity float64, color gg.RGBA) *DropShadowFilter {
	return &DropShadowFilter{
		OffsetX:    offsetX,
		OffsetY:    offsetY,
		BlurRadius: blurRadius,
		Intensity:  intensity,
		Color:      color,
	}
}

// NewSimpleDropShadow creates a black 50%-alpha, full-intensity shadow.
func NewSimpleDropShadow(offsetX, offsetY, blurRadius float64) *DropShadowFilter {
	return &DropShadowFilter{
		OffsetX:    offsetX,
		OffsetY:    offsetY,
		BlurRadius: blurRadius,
		Intensity:  1.0,
		Color:      gg.RGBA2(0, 0, 0, 0.5),
	}
}

// Type returns the filter type tag for serialization and debugging.
func (f *DropShadowFilter) Type() FilterType {
	return FilterDropShadow
}

// sanitized returns a copy with every parameter replaced by a safe finite
// value: non-finite offsets/radius become 0, non-finite intensity becomes 0
// (shadow off), and non-finite color components become 0 with all components
// clamped to [0, 1].
func (f *DropShadowFilter) sanitized() DropShadowFilter {
	s := DropShadowFilter{
		OffsetX:    f.OffsetX,
		OffsetY:    f.OffsetY,
		BlurRadius: f.BlurRadius,
		Intensity:  f.Intensity,
		Color:      f.Color,
	}
	if !isFinite64(s.OffsetX) {
		s.OffsetX = 0
	}
	if !isFinite64(s.OffsetY) {
		s.OffsetY = 0
	}
	if !isFinite64(s.BlurRadius) || s.BlurRadius < 0 {
		s.BlurRadius = 0
	}
	if !isFinite64(s.Intensity) || s.Intensity <= 0 {
		s.Intensity = 0
	}
	s.Color.R = sanitizeColorComponent(s.Color.R)
	s.Color.G = sanitizeColorComponent(s.Color.G)
	s.Color.B = sanitizeColorComponent(s.Color.B)
	s.Color.A = sanitizeColorComponent(s.Color.A)
	return s
}

// shadowVisible reports whether the configuration can produce any shadow
// contribution at all.
func (f DropShadowFilter) shadowVisible() bool {
	return f.Intensity > 0 && f.Color.A > 0
}

// ExpandBounds returns the bounds required to hold both the original content
// and the full (offset + 3-sigma blur) shadow. Fractional offsets are covered
// pixel-tight via ceiling. Empty or non-finite input bounds pass through
// unchanged.
func (f *DropShadowFilter) ExpandBounds(input Rect) Rect {
	if input.IsEmpty() || !isFiniteRect(input) {
		return input
	}
	s := f.sanitized()
	// Shadow support for pixel p is source at p-offset blurred by 3-sigma.
	// Grow each side by the blur reach plus the offset magnitude on the side
	// toward which the shadow is displaced. The whole expression is ceiled in
	// float64 so integer offsets stay exact and fractional ones pixel-tight.
	blurReach := 3 * s.BlurRadius
	left := float32(math.Ceil(blurReach - s.OffsetX))
	right := float32(math.Ceil(blurReach + s.OffsetX))
	top := float32(math.Ceil(blurReach - s.OffsetY))
	bottom := float32(math.Ceil(blurReach + s.OffsetY))

	return Rect{
		MinX: input.MinX - left,
		MinY: input.MinY - top,
		MaxX: input.MaxX + right,
		MaxY: input.MaxY + bottom,
	}
}

// Apply renders the shadow and composites the source over it.
//
// The filter reads src anywhere within the expanded bounds but only writes dst
// pixels inside ExpandBounds(bounds) intersected with the dst dimensions;
// every other dst byte is preserved. Degenerate inputs (nil pixmaps, empty or
// non-finite bounds, zero intensity, transparent shadow, oversized working
// regions) fall back to a source-over copy or a no-op — Apply never panics.
func (f *DropShadowFilter) Apply(src, dst *gg.Pixmap, bounds Rect) {
	if src == nil || dst == nil {
		return
	}
	if bounds.IsEmpty() || !isFiniteRect(bounds) {
		// No trustworthy region: a filter must not write outside its
		// bounds, so this is a deliberate no-op rather than a guess.
		return
	}

	s := f.sanitized()

	// Write region: expanded bounds clipped to the destination.
	expanded := f.ExpandBounds(bounds)
	if !isFiniteRect(expanded) {
		return
	}
	// Guard the float->int conversion: values beyond int32 range are produced
	// by extreme (but finite) offsets/radii and would silently wrap on 32-bit
	// platforms, defeating the allocation guards below.
	const int32Range = 1 << 30
	if v := abs64(float64(expanded.MinX)); v > int32Range ||
		abs64(float64(expanded.MinY)) > int32Range ||
		abs64(float64(expanded.MaxX)) > int32Range ||
		abs64(float64(expanded.MaxY)) > int32Range {
		compositeSourceOnly(src, dst, 0, 0, minInt(src.Width(), dst.Width()), minInt(src.Height(), dst.Height()))
		return
	}
	x0 := maxInt(int(math.Floor(float64(expanded.MinX))), 0)
	y0 := maxInt(int(math.Floor(float64(expanded.MinY))), 0)
	x1 := minInt(int(math.Ceil(float64(expanded.MaxX))), dst.Width())
	y1 := minInt(int(math.Ceil(float64(expanded.MaxY))), dst.Height())
	if x0 >= x1 || y0 >= y1 {
		return
	}
	width, height := x1-x0, y1-y0

	// Reject absurd working sets before allocating.
	if width > maxShadowDim || height > maxShadowDim ||
		int64(width)*int64(height) > maxShadowPixels {
		compositeSourceOnly(src, dst, 0, 0, minInt(src.Width(), dst.Width()), minInt(src.Height(), dst.Height()))
		return
	}

	var shadow []float32
	if s.shadowVisible() {
		kernel := shadowGaussianKernel(s.BlurRadius)
		if len(kernel) > 2*maxKernelHalf+1 {
			// Radius too large to process in bounded time: fall back.
			compositeSourceOnly(src, dst, 0, 0, minInt(src.Width(), dst.Width()), minInt(src.Height(), dst.Height()))
			return
		}

		alpha := getShadowFloats(width * height)
		work := getShadowFloats(width * height)
		defer putShadowFloats(alpha)
		defer putShadowFloats(work)

		extractShiftedAlpha(src, alpha, x0, y0, width, height, s.OffsetX, s.OffsetY)
		if s.BlurRadius > 0 {
			blurChannel(alpha, work, width, height, kernel)
		}

		// Color alpha and intensity fold into a single coverage multiplier.
		cov := s.Color.A * s.Intensity
		shadow = alpha
		if cov != 1.0 {
			for i := range shadow {
				shadow[i] = float32(math.Min(1, float64(shadow[i])*cov))
			}
		}
	}

	compositeDropShadow(src, dst, shadow, x0, y0, width, height, s.Color)
	dst.NotifyPixelsChanged()
}

// compositeSourceOnly blends src over dst in premultiplied space within the
// given pixel rectangle (clamped to both pixmaps). Used by fallback paths.
func compositeSourceOnly(src, dst *gg.Pixmap, x0, y0, x1, y1 int) {
	x1 = minInt(x1, minInt(src.Width(), dst.Width()))
	y1 = minInt(y1, minInt(src.Height(), dst.Height()))
	if x0 >= x1 || y0 >= y1 {
		return
	}
	compositeDropShadow(src, dst, nil, x0, y0, x1-x0, y1-y0, gg.RGBA{})
	dst.NotifyPixelsChanged()
}

// extractShiftedAlpha samples the source alpha channel into out, mapping each
// destination pixel center back through the offset. Bilinear interpolation
// gives smooth fractional offsets; samples outside the source pixmap are zero
// so the shadow footprint stays exactly the shifted source footprint.
func extractShiftedAlpha(src *gg.Pixmap, out []float32, x0, y0, width, height int, offsetX, offsetY float64) {
	srcData := src.Data()
	srcW, srcH := src.Width(), src.Height()

	for y := 0; y < height; y++ {
		// Source coordinate of this destination pixel center.
		fy := float64(y0+y) + 0.5 - offsetY - 0.5
		iy := math.Floor(fy)
		ty := fy - iy
		j0 := int(iy)
		j1 := j0 + 1

		for x := 0; x < width; x++ {
			fx := float64(x0+x) + 0.5 - offsetX - 0.5
			ix := math.Floor(fx)
			tx := fx - ix
			i0 := int(ix)
			i1 := i0 + 1

			// Alpha bytes at the four neighbors (0 when out of range).
			a00 := alphaByteOrZero(srcData, srcW, srcH, i0, j0)
			a10 := alphaByteOrZero(srcData, srcW, srcH, i1, j0)
			a01 := alphaByteOrZero(srcData, srcW, srcH, i0, j1)
			a11 := alphaByteOrZero(srcData, srcW, srcH, i1, j1)

			top := float64(a00)*(1-tx) + float64(a10)*tx
			bot := float64(a01)*(1-tx) + float64(a11)*tx
			out[y*width+x] = float32((top*(1-ty) + bot*ty) / 255.0)
		}
	}
}

// alphaByteOrZero returns the premultiplied alpha byte at (x, y) or 0 when
// the coordinate is outside the pixmap (zero-extension, not edge clamp).
func alphaByteOrZero(data []byte, width, height, x, y int) byte {
	if x < 0 || y < 0 || x >= width || y >= height {
		return 0
	}
	return data[(y*width+x)*4+3]
}

// blurChannel applies the separable 1D kernel in both directions. Samples
// outside the working rectangle clamp to its nearest edge instead of going to
// zero: the rectangle is ExpandBounds clipped to the destination, so shadow
// energy past it is either outside the destination (unwritable) or already
// covered by ExpandBounds. Clamping keeps shadow coverage intact right up to
// the destination edge instead of fading it out. src and tmp must each hold
// width*height float32 values; the result is written back into src.
func blurChannel(src, tmp []float32, width, height int, kernel []float32) {
	half := len(kernel) / 2

	// Horizontal pass: src -> tmp.
	for y := 0; y < height; y++ {
		row := y * width
		for x := 0; x < width; x++ {
			var sum float32
			for k := 0; k < len(kernel); k++ {
				kx := x + k - half
				if kx < 0 {
					kx = 0
				} else if kx >= width {
					kx = width - 1
				}
				sum += src[row+kx] * kernel[k]
			}
			tmp[row+x] = sum
		}
	}

	// Vertical pass: tmp -> src.
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			var sum float32
			for k := 0; k < len(kernel); k++ {
				ky := y + k - half
				if ky < 0 {
					ky = 0
				} else if ky >= height {
					ky = height - 1
				}
				sum += tmp[ky*width+x] * kernel[k]
			}
			src[y*width+x] = sum
		}
	}
}

// compositeDropShadow performs the two-level premultiplied source-over blend.
// shadow is the per-pixel shadow coverage in [0, 1]; nil means "no shadow",
// in which case only src is composited over the existing dst. Pixels outside
// the rectangle are never touched.
//
// Blending order (premultiplied):
//
//	mid = shadowPremul + dst * (1 - shadowAlpha)
//	out = srcPremul   + mid * (1 - srcAlpha)
func compositeDropShadow(src, dst *gg.Pixmap, shadow []float32, x0, y0, width, height int, color gg.RGBA) {
	srcData := src.Data()
	dstData := dst.Data()
	srcW, srcH := src.Width(), src.Height()
	dstW := dst.Width()

	// Straight shadow color, applied as premul*coverage per pixel.
	cR, cG, cB := float32(color.R), float32(color.G), float32(color.B)

	for y := 0; y < height; y++ {
		dy := y0 + y
		if dy < 0 || dy >= dst.Height() {
			continue
		}
		for x := 0; x < width; x++ {
			dx := x0 + x
			if dx < 0 || dx >= dstW {
				continue
			}
			dIdx := (dy*dstW + dx) * 4

			// Existing backdrop (premultiplied bytes).
			bR := float32(dstData[dIdx+0])
			bG := float32(dstData[dIdx+1])
			bB := float32(dstData[dIdx+2])
			bA := float32(dstData[dIdx+3])

			// Level 1: shadow over backdrop.
			if shadow != nil {
				sa := shadow[y*width+x]
				if sa < 0 {
					sa = 0
				} else if sa > 1 {
					sa = 1
				}
				if sa > 0 {
					inv := 1 - sa
					sR := cR * 255 * sa
					sG := cG * 255 * sa
					sB := cB * 255 * sa
					sA := 255 * sa
					bR = sR + bR*inv
					bG = sG + bG*inv
					bB = sB + bB*inv
					bA = sA + bA*inv
				}
			}

			// Level 2: source over the shadow/backdrop mix.
			var sR, sG, sB, sA float32
			if dx >= 0 && dx < srcW && dy >= 0 && dy < srcH {
				sIdx := (dy*srcW + dx) * 4
				sR = float32(srcData[sIdx+0])
				sG = float32(srcData[sIdx+1])
				sB = float32(srcData[sIdx+2])
				sA = float32(srcData[sIdx+3])
			}
			invSrc := 1 - sA/255
			dstData[dIdx+0] = clampU8(sR + bR*invSrc)
			dstData[dIdx+1] = clampU8(sG + bG*invSrc)
			dstData[dIdx+2] = clampU8(sB + bB*invSrc)
			dstData[dIdx+3] = clampU8(sA + bA*invSrc)
		}
	}
}

// Equals reports whether two filters have identical configuration. Useful for
// cache-key decisions: a changed parameter must invalidate cached output.
func (f *DropShadowFilter) Equals(other *DropShadowFilter) bool {
	if f == nil || other == nil {
		return f == other
	}
	return f.OffsetX == other.OffsetX &&
		f.OffsetY == other.OffsetY &&
		f.BlurRadius == other.BlurRadius &&
		f.Intensity == other.Intensity &&
		f.Color == other.Color
}

// Hash derives a 64-bit FNV-1a hash of the filter configuration for use in
// layer cache keys. Distinct parameters always produce distinct hashes
// (bit-exact float components); callers typically fold this into the layer's
// content hash so parameter changes force re-rendering.
func (f *DropShadowFilter) Hash() uint64 {
	h := fnvOffset32
	h = fnvMix64(h, uint64(FilterDropShadow))
	h = fnvMix64(h, math.Float64bits(f.OffsetX))
	h = fnvMix64(h, math.Float64bits(f.OffsetY))
	h = fnvMix64(h, math.Float64bits(f.BlurRadius))
	h = fnvMix64(h, math.Float64bits(f.Intensity))
	h = fnvMix64(h, math.Float64bits(f.Color.R))
	h = fnvMix64(h, math.Float64bits(f.Color.G))
	h = fnvMix64(h, math.Float64bits(f.Color.B))
	h = fnvMix64(h, math.Float64bits(f.Color.A))
	return h
}

func fnvMix64(h, v uint64) uint64 {
	h ^= v
	h *= fnvPrime32
	return h
}

// --- small shared helpers ---

func isFinite64(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func abs64(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func isFiniteRect(r Rect) bool {
	vals := []float32{r.MinX, r.MinY, r.MaxX, r.MaxY}
	for _, v := range vals {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return false
		}
	}
	return true
}

func sanitizeColorComponent(v float64) float64 {
	if !isFinite64(v) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func clampU8(v float32) byte {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return byte(v + 0.5)
}

// --- Gaussian kernel cache (1D, normalized, edge-clamp sampling) ---

var shadowKernelMu sync.RWMutex
var shadowKernelCache = map[uint64][]float32{}

// shadowGaussianKernel returns a normalized 1D Gaussian kernel of size
// 2*ceil(3*radius)+1 for the radius, cached across calls. Radius <= 0 yields
// the identity kernel [1].
func shadowGaussianKernel(radius float64) []float32 {
	if radius <= 0 {
		return []float32{1}
	}
	key := uint64(math.Round(radius * 64))

	shadowKernelMu.RLock()
	if k, ok := shadowKernelCache[key]; ok {
		shadowKernelMu.RUnlock()
		return k
	}
	shadowKernelMu.RUnlock()

	half := int(math.Ceil(radius * 3))
	size := half*2 + 1
	kernel := make([]float32, size)
	twoSigmaSq := 2 * radius * radius
	sum := 0.0
	for i := 0; i < size; i++ {
		x := float64(i - half)
		w := math.Exp(-(x * x) / twoSigmaSq)
		kernel[i] = float32(w)
		sum += w
	}
	if sum > 0 {
		inv := float32(1 / sum)
		for i := range kernel {
			kernel[i] *= inv
		}
	}

	shadowKernelMu.Lock()
	shadowKernelCache[key] = kernel
	shadowKernelMu.Unlock()
	return kernel
}

// --- float32 working-buffer pool ---

const shadowPoolCap = 1 << 24 // channels; matches maxShadowPixels

type shadowBuffer struct{ data []float32 }

var shadowBufferPool sync.Pool

func getShadowFloats(n int) []float32 {
	if n <= 0 {
		return nil
	}
	if buf, ok := shadowBufferPool.Get().(*shadowBuffer); ok {
		if cap(buf.data) >= n {
			return buf.data[:n]
		}
	}
	return make([]float32, n)
}

func putShadowFloats(buf []float32) {
	if cap(buf) == 0 || cap(buf) > shadowPoolCap {
		return // let the GC reclaim oversized buffers
	}
	shadowBufferPool.Put(&shadowBuffer{data: buf[:cap(buf)]})
}

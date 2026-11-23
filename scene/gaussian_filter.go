package scene

import (
	"math"
	"sync"

	"github.com/gogpu/gg"
)

// Gaussian blur configuration limits.
const (
	// MaxGaussianRadius bounds the kernel reach (half-width, in pixels) of
	// any single axis. Radius or sigma inputs whose 3-sigma support would
	// exceed this reach are clamped so that the working set of a blur —
	// kernel taps and temporary buffer — stays deterministic and bounded
	// even for pathological input.
	MaxGaussianRadius = 256.0

	// maxGaussianTaps is the largest kernel permitted on one axis, matching
	// the MaxGaussianRadius reach for odd kernels.
	maxGaussianTaps = int(MaxGaussianRadius)*2 + 1

	// maxGaussianTempBytes bounds the memory retained by the temporary-buffer
	// pool. Buffers larger than this are released to the allocator instead of
	// being pooled, so a single huge blur cannot pin an oversized buffer for
	// the lifetime of the process.
	maxGaussianTempBytes = 16 * 1024 * 1024 // 16 MiB of float32 RGBA (4 Mi-pixel)
)

// GaussianBlurFilter is a composable Filter implementing a bounded, separable
// Gaussian blur on premultiplied-alpha RGBA pixmaps.
//
// The two axes are independent: each carries its own sigma and kernel, so
// directional glow (e.g. a horizontal-only bloom) is a first-class case rather
// than a square blur. Convolution runs as two 1D passes (horizontal then
// vertical), giving O(w*h*(kx+ky)) work instead of O(w*h*kx*ky).
//
// Edge semantics: pixels outside the source pixmap are treated as transparent
// black, never as edge-extended copies. This preserves the total premultiplied
// energy of the input (alpha only spreads, it is not synthesized) and ensures
// content at the border — or outside the filter region — is not brightened.
//
// The filter is safe for reuse: kernels are built lazily and cached on the
// filter, temporary buffers come from a shared, size-capped pool, and Apply
// never writes to src (it works through a scratch buffer even when src == dst).
type GaussianBlurFilter struct {
	sigmaX float64
	sigmaY float64
	tapsX  int // 1 means identity; otherwise the total kernel tap count
	tapsY  int

	kernOnce sync.Once
	mu       sync.RWMutex
	kernelX  []float32
	kernelY  []float32
}

// Compile-time check: GaussianBlurFilter is a Filter.
var _ Filter = (*GaussianBlurFilter)(nil)

// NewGaussianBlur creates a Gaussian blur from per-axis standard deviations
// (sigmas), in pixels. This is the conventional "blur radius" used by SVG,
// CSS and Skia filter primitives: the bounded kernel reaches ceil(3*sigma)
// pixels (99.7% of the Gaussian mass) on that axis. A radius <= 0 produces an
// identity (no-op) filter on that axis.
//
// For an exact pixel-reach kernel independent of the 3-sigma convention, use
// NewGaussianBlurTaps.
func NewGaussianBlur(radiusX, radiusY float64) *GaussianBlurFilter {
	sx := clampRadius(radiusX)
	sy := clampRadius(radiusY)
	return &GaussianBlurFilter{
		sigmaX: sx,
		sigmaY: sy,
		tapsX:  gaussianTapsForSigma(sx),
		tapsY:  gaussianTapsForSigma(sy),
	}
}

// NewGaussianBlurSigma is an explicit alias for NewGaussianBlur: the inputs
// are standard deviations in pixels, truncated at 3 sigma.
func NewGaussianBlurSigma(sigmaX, sigmaY float64) *GaussianBlurFilter {
	return NewGaussianBlur(sigmaX, sigmaY)
}

// NewGaussianBlurTaps creates a Gaussian blur with explicit kernel tap counts.
// This is the advanced entry point: it makes even-sized kernels reachable
// (the radius/sigma constructors always yield odd kernels centered on a pixel).
//
// taps must be >= 1. With odd taps the kernel is symmetric about a pixel
// center; with even taps the support shifts half a pixel right (offsets
// -(taps/2-1) … taps/2), matching the standard even-kernel convention.
// Taps are capped so the reach never exceeds MaxGaussianRadius.
func NewGaussianBlurTaps(sigmaX, sigmaY float64, tapsX, tapsY int) *GaussianBlurFilter {
	sigmaX = clampRadius(sigmaX)
	sigmaY = clampRadius(sigmaY)
	return &GaussianBlurFilter{
		sigmaX: sigmaX,
		sigmaY: sigmaY,
		tapsX:  clampTaps(sigmaX, tapsX),
		tapsY:  clampTaps(sigmaY, tapsY),
	}
}

// FilterType returns FilterBlur for serialization and debugging.
func (f *GaussianBlurFilter) FilterType() FilterType { return FilterBlur }

// RadiusX returns the practical horizontal reach in pixels (0 = identity).
func (f *GaussianBlurFilter) RadiusX() float64 {
	kx, _ := f.kernels()
	return float64(reach(kx))
}

// RadiusY returns the practical vertical reach in pixels (0 = identity).
func (f *GaussianBlurFilter) RadiusY() float64 {
	_, ky := f.kernels()
	return float64(reach(ky))
}

// SigmaX returns the horizontal standard deviation.
func (f *GaussianBlurFilter) SigmaX() float64 { return f.sigmaX }

// SigmaY returns the vertical standard deviation.
func (f *GaussianBlurFilter) SigmaY() float64 { return f.sigmaY }

// ExpandBounds grows the input rectangle by the kernel reach on each axis.
// An identity axis contributes no expansion. Empty input stays empty so that
// empty layers do not gain phantom bounds.
func (f *GaussianBlurFilter) ExpandBounds(input Rect) Rect {
	if input.IsEmpty() {
		return input
	}
	ex := float32(f.RadiusX())
	ey := float32(f.RadiusY())
	return Rect{
		MinX: input.MinX - ex,
		MinY: input.MinY - ey,
		MaxX: input.MaxX + ex,
		MaxY: input.MaxY + ey,
	}
}

// Apply blurs src into dst over the output region given by bounds.
//
// src and dst share one pixel coordinate system. Reads past the edges of src
// sample transparent black; writes are clamped to dst's dimensions, and
// nothing outside bounds is written. src is never modified, including when
// src == dst (the separable passes run through a pooled scratch buffer).
func (f *GaussianBlurFilter) Apply(src, dst *gg.Pixmap, bounds Rect) {
	if src == nil || dst == nil || bounds.IsEmpty() {
		return
	}

	kx, ky := f.kernels()

	// Fast path: both axes are identity. Copy intersecting pixels only.
	if len(kx) == 1 && len(ky) == 1 {
		copyPixmap(src, dst, bounds)
		return
	}

	// Pixel-aligned output region, intersected with the destination.
	minX, minY, maxX, maxY := clampRectToPixmap(bounds, dst)
	if minX >= maxX || minY >= maxY {
		return
	}
	width, height := maxX-minX, maxY-minY

	temp := acquireBlurTemp(width, height)
	defer releaseBlurTemp(temp)

	// Pass 1: horizontal, src -> temp (temp is region-local; rows/cols map
	// back to global coordinates via minX/minY).
	blurPassHorizontal(src, temp.data, kx, minX, minY, width, height)
	// Pass 2: vertical, temp -> dst.
	blurPassVertical(temp.data, dst, ky, minX, minY, width, height)
}

// kernels lazily builds and caches the two 1D kernels.
func (f *GaussianBlurFilter) kernels() (kx, ky []float32) {
	f.kernOnce.Do(func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.kernelX = buildGaussianKernel1D(f.sigmaX, f.tapsX)
		f.kernelY = buildGaussianKernel1D(f.sigmaY, f.tapsY)
	})
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.kernelX, f.kernelY
}

// ---------------------------------------------------------------------------
// Kernel construction
// ---------------------------------------------------------------------------

// buildGaussianKernel1D builds a normalized, bounded 1D Gaussian kernel.
//
// taps <= 1 (or sigma <= 0) yields the identity kernel [1]. Otherwise the
// kernel is the truncated Gaussian exp(-x²/(2σ²)) over integer offsets,
// renormalized so the weights sum to exactly 1. Even tap counts shift the
// support half a pixel right: offsets run from -(taps/2-1) through taps/2.
func buildGaussianKernel1D(sigma float64, taps int) []float32 {
	if sigma <= 0 || taps <= 1 {
		return []float32{1}
	}

	offsets := kernelOffsets(taps)
	kernel := make([]float32, len(offsets))
	twoSigmaSq := 2 * sigma * sigma

	var sum float64
	for i, off := range offsets {
		w := math.Exp(-(float64(off) * float64(off)) / twoSigmaSq)
		kernel[i] = float32(w)
		sum += w
	}
	if sum <= 0 {
		// Degenerate sigma: fall back to a centered identity weight.
		for i := range kernel {
			kernel[i] = 0
		}
		kernel[len(kernel)/2] = 1
		return kernel
	}

	inv := 1 / sum
	for i := range kernel {
		kernel[i] *= float32(inv)
	}
	return kernel
}

// kernelOffsets returns the integer sample offsets for a kernel of taps.
// Odd taps: -(taps/2) … taps/2. Even taps: -(taps/2-1) … taps/2.
func kernelOffsets(taps int) []int {
	offsets := make([]int, taps)
	half := taps / 2
	if taps%2 == 0 {
		for i := 0; i < taps; i++ {
			offsets[i] = i - (half - 1)
		}
		return offsets
	}
	for i := 0; i < taps; i++ {
		offsets[i] = i - half
	}
	return offsets
}

// kernelRange returns the inclusive min/max integer sample offsets of a
// kernel with the given tap count without allocating. Inverse of the
// convention in kernelOffsets: offset of weight k is minOff + k.
func kernelRange(taps int) (minOff, maxOff int) {
	half := taps / 2
	if taps%2 == 0 {
		return -(half - 1), half
	}
	return -half, half
}

// reach returns the maximum absolute offset of a kernel: its pixel reach.
func reach(kernel []float32) int {
	if len(kernel) <= 1 {
		return 0
	}
	return len(kernel) / 2
}

// gaussianTapsForSigma returns the odd tap count whose half-width covers
// 3 sigma, capped at the MaxGaussianRadius reach. Identity sigmas produce a
// single tap.
func gaussianTapsForSigma(sigma float64) int {
	if sigma <= 0 {
		return 1
	}
	half := int(math.Ceil(sigma * 3))
	taps := half*2 + 1
	if taps > maxGaussianTaps {
		return maxGaussianTaps
	}
	return taps
}

// clampRadius clamps a radius/sigma into the supported range and drops NaN.
func clampRadius(r float64) float64 {
	if math.IsNaN(r) || r <= 0 {
		return 0
	}
	if r > MaxGaussianRadius {
		return MaxGaussianRadius
	}
	return r
}

// clampTaps normalizes an explicit tap count and enforces the reach cap.
func clampTaps(sigma float64, taps int) int {
	if sigma <= 0 {
		return 1
	}
	if taps < 1 {
		return gaussianTapsForSigma(sigma)
	}
	if taps > maxGaussianTaps {
		// Largest permitted kernel with the same parity.
		taps = maxGaussianTaps - (1 - taps&1)
	}
	return taps
}

// ---------------------------------------------------------------------------
// Separable passes
// ---------------------------------------------------------------------------

// blurPassHorizontal convolves each row of src with kernel, writing RGBA
// float32 premultiplied values into the region-local temp buffer. Source
// samples outside the pixmap are transparent black (zero padding).
func blurPassHorizontal(src *gg.Pixmap, temp []float32, kernel []float32,
	minX, minY, width, height int,
) {
	srcW, srcH := src.Width(), src.Height()
	srcData := src.Data()
	offMin, _ := kernelRange(len(kernel))

	for y := 0; y < height; y++ {
		srcY := minY + y
		rowInBounds := srcY >= 0 && srcY < srcH
		for x := 0; x < width; x++ {
			srcX := minX + x
			var r, g, b, a float32

			if rowInBounds {
				rowBase := srcY * srcW * 4
				for k := 0; k < len(kernel); k++ {
					sx := srcX + offMin + k
					if sx < 0 || sx >= srcW {
						continue // transparent black neighbor
					}
					idx := rowBase + sx*4
					w := kernel[k]
					r += float32(srcData[idx]) * w
					g += float32(srcData[idx+1]) * w
					b += float32(srcData[idx+2]) * w
					a += float32(srcData[idx+3]) * w
				}
			}

			out := (y*width + x) * 4
			temp[out] = r
			temp[out+1] = g
			temp[out+2] = b
			temp[out+3] = a
		}
	}
}

// blurPassVertical convolves each column of the region-local temp buffer with
// kernel and writes premultiplied uint8 RGBA into dst. Rows above/below the
// region are transparent black (zero padding), matching the horizontal pass.
func blurPassVertical(temp []float32, dst *gg.Pixmap, kernel []float32,
	minX, minY, width, height int,
) {
	dstW, dstH := dst.Width(), dst.Height()
	dstData := dst.Data()
	offMin, _ := kernelRange(len(kernel))

	for y := 0; y < height; y++ {
		dstY := minY + y
		if dstY < 0 || dstY >= dstH {
			continue
		}
		for x := 0; x < width; x++ {
			dstX := minX + x
			if dstX < 0 || dstX >= dstW {
				continue
			}

			var r, g, b, a float32
			for k := 0; k < len(kernel); k++ {
				sy := y + offMin + k
				if sy < 0 || sy >= height {
					continue // zero-padded edge
				}
				idx := (sy*width + x) * 4
				w := kernel[k]
				r += temp[idx] * w
				g += temp[idx+1] * w
				b += temp[idx+2] * w
				a += temp[idx+3] * w
			}

			out := (dstY*dstW + dstX) * 4
			dstData[out] = clampByte255(r)
			dstData[out+1] = clampByte255(g)
			dstData[out+2] = clampByte255(b)
			dstData[out+3] = clampByte255(a)
		}
	}
}

// clampByte255 rounds a premultiplied channel value into [0, 255].
func clampByte255(v float32) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v + 0.5)
}

// clampRectToPixmap converts bounds into an integer pixel rectangle clipped
// to the pixmap's dimensions.
func clampRectToPixmap(bounds Rect, pm *gg.Pixmap) (minX, minY, maxX, maxY int) {
	minX = int(math.Floor(float64(bounds.MinX)))
	minY = int(math.Floor(float64(bounds.MinY)))
	maxX = int(math.Ceil(float64(bounds.MaxX)))
	maxY = int(math.Ceil(float64(bounds.MaxY)))

	if minX < 0 {
		minX = 0
	}
	if minY < 0 {
		minY = 0
	}
	if maxX > pm.Width() {
		maxX = pm.Width()
	}
	if maxY > pm.Height() {
		maxY = pm.Height()
	}
	return minX, minY, maxX, maxY
}

// ---------------------------------------------------------------------------
// Scratch-buffer pool (size-capped)
// ---------------------------------------------------------------------------

// blurTempBuffer wraps a float32 RGBA scratch buffer for pool storage.
type blurTempBuffer struct {
	data []float32
}

// blurTempPool holds scratch buffers between Apply calls. Buffers exceeding
// maxGaussianTempBytes are not returned to the pool (see releaseBlurTemp).
// The pool stores *blurTempBuffer pointers so the wrapper itself is reused
// across Get/Put cycles: after warmup the hot path allocates nothing.
var blurTempPool sync.Pool

// acquireBlurTemp returns a wrapper holding a zeroed float32 RGBA buffer of
// width*height*4 elements. The same wrapper is handed back through
// releaseBlurTemp.
func acquireBlurTemp(width, height int) *blurTempBuffer {
	size := width * height * 4
	if size <= 0 {
		return nil
	}
	if v := blurTempPool.Get(); v != nil {
		wrapper := v.(*blurTempBuffer)
		if cap(wrapper.data) < size {
			// Retain the wrapper, replace only the backing array.
			wrapper.data = make([]float32, size)
		} else {
			wrapper.data = wrapper.data[:size]
			clear(wrapper.data)
		}
		return wrapper
	}
	return &blurTempBuffer{data: make([]float32, size)}
}

// releaseBlurTemp returns a scratch wrapper to the pool, unless its buffer
// exceeds the memory cap — oversized buffers and their wrappers are then
// released to the allocator.
func releaseBlurTemp(wrapper *blurTempBuffer) {
	if wrapper == nil || cap(wrapper.data) == 0 ||
		cap(wrapper.data) > maxGaussianTempBytes {
		return
	}
	wrapper.data = wrapper.data[:cap(wrapper.data)]
	blurTempPool.Put(wrapper)
}

// Copyright 2026 The gogpu Authors
// SPDX-License-Identifier: MIT

package render

import (
	"errors"
	"image"
	"image/draw"
	"math"
)

// Filter selects the texture sampling filter.
type Filter uint8

const (
	// FilterNearest selects the closest texel (nearest-neighbor sampling).
	FilterNearest Filter = iota
	// FilterBilinear interpolates the four nearest texels (2x2 bilinear).
	FilterBilinear
)

// AffineTransform is a 2D affine matrix in row-major order:
//
//	| A B C |
//	| D E F |
//
// applying x' = A*x + B*y + C, y' = D*x + E*y + F.
//
// It maps the destination rectangle's local coordinate system into target
// (canvas) pixel coordinates. The zero value is NOT a valid transform; use
// IdentityTransform.
type AffineTransform struct {
	A, B, C float64
	D, E, F float64
}

// IdentityTransform returns the identity affine transform.
func IdentityTransform() AffineTransform {
	return AffineTransform{A: 1, E: 1}
}

// IsFinite reports whether every component is a finite number.
// NaN or infinite matrices are rejected before rendering (safe degradation).
func (m AffineTransform) IsFinite() bool {
	values := [6]float64{m.A, m.B, m.C, m.D, m.E, m.F}
	for _, v := range values {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

// Determinant returns the determinant of the 2x2 linear part. A zero (or
// denormal) determinant means the matrix collapses area and is not invertible.
func (m AffineTransform) Determinant() float64 {
	return m.A*m.E - m.B*m.D
}

// Invert returns the inverse matrix. The boolean is false when the matrix is
// not invertible (degenerate linear part).
func (m AffineTransform) Invert() (AffineTransform, bool) {
	det := m.Determinant()
	if det == 0 || math.IsNaN(det) || math.IsInf(det, 0) {
		return AffineTransform{}, false
	}
	invDet := 1.0 / det
	return AffineTransform{
		A: m.E * invDet,
		B: -m.B * invDet,
		C: (m.B*m.F - m.C*m.E) * invDet,
		D: -m.D * invDet,
		E: m.A * invDet,
		F: (m.C*m.D - m.A*m.F) * invDet,
	}, true
}

// TransformPoint applies the matrix to (x, y).
func (m AffineTransform) TransformPoint(x, y float64) (float64, float64) {
	return m.A*x + m.B*y + m.C, m.D*x + m.E*y + m.F
}

// ImageDrawOp describes one texture draw: a source rectangle of an image is
// mapped onto a destination rectangle, and the destination rectangle is placed
// into the target through Transform.
//
// The full forward mapping from source-image coordinates to target pixels is:
//
//	target = Transform * Translate(dst.Min) * Scale(dst.Size/src.Size) * src
//
// Negative destination width/height (or a flipped Transform) produce mirrored
// output. SrcRect is expressed in source-image pixel coordinates.
type ImageDrawOp struct {
	// Image is the source image. Stored as an immutable interface value:
	// the renderer never mutates it, so a recorded command cannot be altered
	// by replaying it.
	Image image.Image

	// SrcRect is the sampled rectangle in image coordinates. An empty
	// rectangle selects the full image bounds.
	SrcRect image.Rectangle

	// DstMin is the destination rectangle's top-left corner in the local
	// coordinate system that Transform maps into target coordinates.
	DstMin image.Point

	// DstWidth and DstHeight are the destination rectangle dimensions.
	// Negative values flip the image on that axis. A zero value is illegal
	// (rejected at validation).
	DstWidth  float64
	DstHeight float64

	// Filter selects nearest or bilinear sampling.
	Filter Filter

	// Alpha is an extra opacity multiplier in [0, 1]. Values outside the
	// range are clamped. 1.0 means no modulation.
	Alpha float64

	// Transform places the destination rectangle in the target. Use
	// IdentityTransform for an untransformed blit.
	Transform AffineTransform
}

// Maximum texture dimension accepted by the software renderer. Images larger
// than this are rejected with an error rather than risking huge intermediate
// allocations or integer overflow in index arithmetic. 32767 matches the
// common GPU texture cap and keeps w*h*4 well inside int range.
const maxSoftwareTextureSize = 32767

// minDrawableDstPixels is the smallest destination extent that is rendered.
// Smaller rectangles degenerate to zero covered pixels and are skipped safely.
const minDrawableDstPixels = 1e-6

// validateImageDrawOp validates a texture draw and normalizes derived values.
// It returns the effective source rectangle, or an error with context when the
// operation cannot be rendered. Errors are preferred over silent corruption for
// caller-visible misuses; the Render loop itself degrades by skipping.
func validateImageDrawOp(op *ImageDrawOp) (image.Rectangle, error) {
	if op == nil {
		return image.Rectangle{}, errors.New("render: nil image draw op")
	}
	if op.Image == nil {
		return image.Rectangle{}, errors.New("render: draw image with nil image resource")
	}
	bounds := op.Image.Bounds()
	if bounds.Empty() {
		return image.Rectangle{}, errors.New("render: draw image with empty bounds")
	}
	if bounds.Dx() > maxSoftwareTextureSize || bounds.Dy() > maxSoftwareTextureSize {
		return image.Rectangle{}, errors.New("render: image exceeds maximum texture size")
	}

	src := op.SrcRect
	if src.Empty() {
		src = bounds
	}
	// Intersect with the image bounds so an out-of-range source rect cannot
	// drive out-of-bounds texel access.
	src = src.Intersect(bounds)
	if src.Empty() {
		return image.Rectangle{}, errors.New("render: draw image with empty or out-of-range source rectangle")
	}

	if op.DstWidth == 0 || op.DstHeight == 0 {
		return image.Rectangle{}, errors.New("render: draw image with zero-sized destination")
	}
	if math.IsNaN(op.DstWidth) || math.IsNaN(op.DstHeight) ||
		math.IsInf(op.DstWidth, 0) || math.IsInf(op.DstHeight, 0) {
		return image.Rectangle{}, errors.New("render: draw image with non-finite destination")
	}
	if math.Abs(op.DstWidth) < minDrawableDstPixels ||
		math.Abs(op.DstHeight) < minDrawableDstPixels {
		return image.Rectangle{}, errors.New("render: draw image destination too small")
	}
	if !op.Transform.IsFinite() {
		return image.Rectangle{}, errors.New("render: draw image with non-finite transform")
	}
	if det := op.Transform.Determinant(); det == 0 || math.IsNaN(det) {
		return image.Rectangle{}, errors.New("render: draw image transform is not invertible")
	}
	if op.Alpha != 0 && (math.IsNaN(op.Alpha) || math.IsInf(op.Alpha, 0)) {
		return image.Rectangle{}, errors.New("render: draw image with non-finite alpha")
	}
	return src, nil
}

// premulTexel is one premultiplied-alpha RGBA texel in [0,255].
type premulTexel struct {
	r, g, b, a float64
}

// textureSampler is a standalone CPU texture sampler. It is deliberately
// independent of the renderer/scene so it can be compared directly against an
// independent reference sampler in tests.
//
// All interpolation happens in premultiplied-alpha space, which is the correct
// space for filtering: transparent texels contribute (0,0,0,0) instead of
// their unassociated RGB, preventing dark halos along semi-transparent edges
// (the same convention used by Skia's SkBitmapProcShader, Cairo and tiny-skia).
type textureSampler struct {
	// rgba is the source converted to a tightly packed, zero-origin
	// premultiplied RGBA buffer. Conversion happens once per draw so arbitrary
	// image.Image implementations (NRGBA, Gray, Paletted, ...) share one hot
	// loop.
	rgba          *image.RGBA
	width, height int
	stride        int
	filter        Filter
	alpha         float64
}

// newTextureSampler prepares a sampler for the given source image. A tightly
// packed, origin-based *image.RGBA is reused directly (its documented storage
// is premultiplied and the referenced image is never modified); any other
// layout or color model is converted once into a normalized packed copy.
func newTextureSampler(img image.Image, filter Filter, alpha float64) *textureSampler {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	var packed *image.RGBA
	// Reuse only a tightly packed, origin-based RGBA buffer. A non-zero Min or
	// padded stride would break the sampler's relative offset arithmetic, so
	// any other layout is copied into a normalized buffer.
	if rgba, ok := img.(*image.RGBA); ok &&
		rgba.Stride == w*4 &&
		rgba.Rect.Min.X == 0 && rgba.Rect.Min.Y == 0 {
		packed = rgba
	} else {
		packed = image.NewRGBA(image.Rect(0, 0, w, h))
		// draw.Src with an *image.RGBA destination premultiplies any
		// non-premultiplied source through the stdlib color model.
		draw.Draw(packed, packed.Bounds(), img, bounds.Min, draw.Src)
	}

	if alpha < 0 {
		alpha = 0
	} else if alpha > 1 {
		alpha = 1
	}
	if math.IsNaN(alpha) {
		alpha = 1
	}

	return &textureSampler{
		rgba:   packed,
		width:  w,
		height: h,
		stride: packed.Stride,
		filter: filter,
		alpha:  alpha,
	}
}

// offset returns the byte offset of texel (x, y), which must be in bounds.
// The backing buffer is always normalized to a zero-origin packed RGBA.
func (s *textureSampler) offset(x, y int) int {
	return y*s.stride + x*4
}

// fetch reads texel (x, y) as premultiplied RGBA. The caller guarantees the
// coordinates are within [0, width) x [0, height); the bounds test is repeated
// defensively so a logic mistake degrades to transparent instead of panicking.
func (s *textureSampler) fetch(x, y int) premulTexel {
	if x < 0 || y < 0 || x >= s.width || y >= s.height {
		return premulTexel{}
	}
	i := s.offset(x, y)
	pix := s.rgba.Pix
	if i+3 >= len(pix) {
		return premulTexel{}
	}
	a := float64(pix[i+3])
	if a == 0 {
		// Premultiplied transparent texel: RGB must contribute nothing.
		// Some producers leave stray RGB behind; treat it as black.
		return premulTexel{}
	}
	return premulTexel{
		r: float64(pix[i]),
		g: float64(pix[i+1]),
		b: float64(pix[i+2]),
		a: a,
	}
}

// clampToEdge clamps a texel coordinate to the valid range.
func (s *textureSampler) clampToEdge(v, max int) int {
	if v < 0 {
		return 0
	}
	if v > max {
		return max
	}
	return v
}

// sample returns the filtered premultiplied color at image-space coordinate
// (u, v). Coordinates use the texel-center convention: texel (i, j) is centered
// at (i+0.5, j+0.5). Out-of-range positions clamp to the edge texel, matching
// GPU clamp-to-edge addressing.
func (s *textureSampler) sample(u, v float64) premulTexel {
	// Shift to texel-grid coordinates where integer part = top-left texel.
	fu := u - 0.5
	fv := v - 0.5

	ix := floorInt(fu)
	iy := floorInt(fv)

	if s.filter == FilterNearest {
		// Nearest: round to the closest texel center.
		nx := s.clampToEdge(int(math.Round(fu)), s.width-1)
		ny := s.clampToEdge(int(math.Round(fv)), s.height-1)
		return s.applyAlpha(s.fetch(nx, ny))
	}

	fx := fu - math.Floor(fu)
	fy := fv - math.Floor(fv)

	// Fast exact-integer path: one tap when aligned on a texel corner.
	if fx == 0 && fy == 0 &&
		ix >= 0 && iy >= 0 && ix < s.width && iy < s.height {
		return s.applyAlpha(s.fetch(ix, iy))
	}

	x0 := s.clampToEdge(ix, s.width-1)
	x1 := s.clampToEdge(ix+1, s.width-1)
	y0 := s.clampToEdge(iy, s.height-1)
	y1 := s.clampToEdge(iy+1, s.height-1)

	p00 := s.fetch(x0, y0)
	p10 := s.fetch(x1, y0)
	p01 := s.fetch(x0, y1)
	p11 := s.fetch(x1, y1)

	w00 := (1 - fx) * (1 - fy)
	w10 := fx * (1 - fy)
	w01 := (1 - fx) * fy
	w11 := fx * fy

	t := premulTexel{
		r: p00.r*w00 + p10.r*w10 + p01.r*w01 + p11.r*w11,
		g: p00.g*w00 + p10.g*w10 + p01.g*w01 + p11.g*w11,
		b: p00.b*w00 + p10.b*w10 + p01.b*w01 + p11.b*w11,
		a: p00.a*w00 + p10.a*w10 + p01.a*w01 + p11.a*w11,
	}
	return s.applyAlpha(t)
}

// applyAlpha multiplies the sampled premultiplied color by the draw opacity.
// Because the color is premultiplied, all four channels scale together, which
// keeps transparency physically correct.
func (s *textureSampler) applyAlpha(t premulTexel) premulTexel {
	if s.alpha >= 1 {
		return t
	}
	if s.alpha <= 0 {
		return premulTexel{}
	}
	return premulTexel{
		r: t.r * s.alpha,
		g: t.g * s.alpha,
		b: t.b * s.alpha,
		a: t.a * s.alpha,
	}
}

// floorInt is math.Floor returning an int, correct for negative coordinates.
func floorInt(v float64) int {
	i := int(v)
	if v < float64(i) {
		return i - 1
	}
	return i
}

// drawTexture renders a validated image draw op into the target pixel buffer
// (premultiplied RGBA8, the image.RGBA / PixmapTarget layout).
//
// The routine walks only target pixels covered by the transformed destination
// quad, inverse-maps each pixel center to source-image coordinates, samples the
// texture and composites source-over. Bounds, stride and empty regions are all
// checked before indexing, so out-of-target or degenerate draws never write
// outside the buffer.
func drawTexture(pixels []byte, targetWidth, targetHeight, stride int, op *ImageDrawOp, src image.Rectangle) {
	sampler := newTextureSampler(op.Image, op.Filter, op.Alpha)

	// Local-space destination corners (pre-transform).
	dstW := op.DstWidth
	dstH := op.DstHeight
	dstX := float64(op.DstMin.X)
	dstY := float64(op.DstMin.Y)
	corners := [4][2]float64{
		{dstX, dstY},
		{dstX + dstW, dstY},
		{dstX + dstW, dstY + dstH},
		{dstX, dstY + dstH},
	}

	// Bounding box of the transformed quad in target pixel coordinates.
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, c := range corners {
		tx, ty := op.Transform.TransformPoint(c[0], c[1])
		minX = math.Min(minX, tx)
		minY = math.Min(minY, ty)
		maxX = math.Max(maxX, tx)
		maxY = math.Max(maxY, ty)
	}

	// Iterate the integer pixel range covering the quad, clipped to the target.
	// +1 on the max side to include pixels whose center sits inside the quad.
	x0 := maxInt(floorInt(minX), 0)
	y0 := maxInt(floorInt(minY), 0)
	x1 := minInt(floorInt(maxX)+1, targetWidth)
	y1 := minInt(floorInt(maxY)+1, targetHeight)
	if x1 <= x0 || y1 <= y0 {
		return
	}

	// Inverse of the full forward map: target pixel -> local dst -> source.
	inv, ok := op.Transform.Invert()
	if !ok {
		return // validated earlier; defensive
	}

	srcW := float64(src.Dx())
	srcH := float64(src.Dy())
	scaleX := dstW / srcW
	scaleY := dstH / srcH
	// The sampler addresses texels relative to the image bounds origin, so
	// convert the absolute source rectangle to relative coordinates. This
	// keeps non-zero-Min images (image.RGBA SubImages) addressing correct.
	relMinX := float64(src.Min.X - op.Image.Bounds().Min.X)
	relMinY := float64(src.Min.Y - op.Image.Bounds().Min.Y)
	relMaxX := relMinX + srcW
	relMaxY := relMinY + srcH

	for py := y0; py < y1; py++ {
		rowBase := py * stride
		if rowBase < 0 || rowBase >= len(pixels) {
			continue
		}
		for px := x0; px < x1; px++ {
			off := rowBase + px*4
			if off+3 >= len(pixels) || off < 0 {
				continue
			}

			// Target pixel center -> local destination coordinates.
			lx, ly := inv.TransformPoint(float64(px)+0.5, float64(py)+0.5)

			// Reject points outside the destination rectangle (the iteration
			// range is its bounding box; rotated quads leave corner gaps).
			if lx < math.Min(dstX, dstX+dstW) || lx > math.Max(dstX, dstX+dstW) ||
				ly < math.Min(dstY, dstY+dstH) || ly > math.Max(dstY, dstY+dstH) {
				continue
			}

			// Local destination -> sampler coordinates (relative to bounds).
			u := relMinX + (lx-dstX)/scaleX
			v := relMinY + (ly-dstY)/scaleY

			// Clamp-to-edge: keep sampling inside the source rectangle.
			u = clampFloat(u, relMinX, relMaxX)
			v = clampFloat(v, relMinY, relMaxY)

			texel := sampler.sample(u, v)
			if texel.a <= 0 {
				continue // Fully transparent: leave destination untouched.
			}
			compositePremul(pixels, off, texel)
		}
	}
}

// compositePremul blends one premultiplied source texel onto a premultiplied
// destination pixel using source-over: dst' = src + dst * (1 - srcA).
func compositePremul(pixels []byte, off int, t premulTexel) {
	sr := clampByte(t.r)
	sg := clampByte(t.g)
	sb := clampByte(t.b)
	sa := clampByte(t.a)
	if sa == 0 {
		return
	}
	if sa == 255 {
		pixels[off] = sr
		pixels[off+1] = sg
		pixels[off+2] = sb
		pixels[off+3] = 255
		return
	}
	inv := uint16(255 - sa)
	pixels[off] = sr + byte((uint16(pixels[off])*inv+127)/255)
	pixels[off+1] = sg + byte((uint16(pixels[off+1])*inv+127)/255)
	pixels[off+2] = sb + byte((uint16(pixels[off+2])*inv+127)/255)
	pixels[off+3] = sa + byte((uint16(pixels[off+3])*inv+127)/255)
}

// clampByte rounds and clamps a [0,255]-range float to a byte.
func clampByte(v float64) byte {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return byte(v + 0.5)
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
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

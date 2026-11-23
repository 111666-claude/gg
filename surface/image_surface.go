// Copyright 2026 The gogpu Authors
// SPDX-License-Identifier: MIT

package surface

import (
	"image"
	"image/color"
	"image/draw"
	"math"

	"github.com/gogpu/gg/internal/raster"
)

// ImageSurface is a CPU-based surface that renders to an *image.RGBA.
//
// It uses raster.AnalyticFiller for high-quality anti-aliased rendering.
// This is the default surface implementation for software rendering.
//
// Example:
//
//	s := surface.NewImageSurface(800, 600)
//	defer s.Close()
//
//	s.Clear(color.White)
//	path := surface.NewPath()
//	path.Circle(400, 300, 100)
//	s.Fill(path, surface.FillStyle{Color: color.RGBA{255, 0, 0, 255}})
//
//	img := s.Snapshot()
type ImageSurface struct {
	width  int
	height int
	img    *image.RGBA

	// filler provides analytic anti-aliasing
	filler *raster.AnalyticFiller

	// edgeBuilder converts paths to edges
	edgeBuilder *raster.EdgeBuilder

	// closed tracks if Close has been called
	closed bool
}

// NewImageSurface creates a new CPU-based surface with the given dimensions.
func NewImageSurface(width, height int) *ImageSurface {
	if width <= 0 {
		width = 1
	}
	if height <= 0 {
		height = 1
	}

	return &ImageSurface{
		width:       width,
		height:      height,
		img:         image.NewRGBA(image.Rect(0, 0, width, height)),
		filler:      raster.NewAnalyticFiller(width, height),
		edgeBuilder: raster.NewEdgeBuilder(2), // 4x AA quality
	}
}

// NewImageSurfaceFromImage creates a surface backed by an existing image.
// The surface will render into the provided image directly.
func NewImageSurfaceFromImage(img *image.RGBA) *ImageSurface {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	return &ImageSurface{
		width:       width,
		height:      height,
		img:         img,
		filler:      raster.NewAnalyticFiller(width, height),
		edgeBuilder: raster.NewEdgeBuilder(2),
	}
}

// Width returns the surface width.
func (s *ImageSurface) Width() int {
	return s.width
}

// Height returns the surface height.
func (s *ImageSurface) Height() int {
	return s.height
}

// Clear fills the entire surface with the given color.
func (s *ImageSurface) Clear(c color.Color) {
	if s.closed {
		return
	}

	r, g, b, a := c.RGBA()
	//nolint:gosec // G115: safe - r>>8 is always in [0, 255]
	rgba := color.RGBA{
		R: uint8(r >> 8),
		G: uint8(g >> 8),
		B: uint8(b >> 8),
		A: uint8(a >> 8),
	}

	draw.Draw(s.img, s.img.Bounds(), &image.Uniform{rgba}, image.Point{}, draw.Src)
}

// Fill fills the given path using the specified style.
func (s *ImageSurface) Fill(path *Path, style FillStyle) {
	if s.closed || path == nil || path.IsEmpty() {
		return
	}

	// Get fill color
	fillColor := s.resolveColor(style.Color, style.Pattern)

	// Convert fill rule
	var fillRule raster.FillRule
	switch style.Rule {
	case FillRuleEvenOdd:
		fillRule = raster.FillRuleEvenOdd
	default:
		fillRule = raster.FillRuleNonZero
	}

	// Build edges from path
	s.edgeBuilder.Reset()
	s.edgeBuilder.SetFlattenCurves(false)
	s.edgeBuilder.BuildFromPath(path, raster.IdentityTransform{})

	if s.edgeBuilder.IsEmpty() {
		return
	}

	// Reset filler and render
	s.filler.Reset()
	s.filler.Fill(s.edgeBuilder, fillRule, func(y int, runs *raster.AlphaRuns) {
		if y < 0 || y >= s.height {
			return
		}
		s.blendRow(y, runs, fillColor)
	})
}

// Stroke strokes the given path using the specified style.
func (s *ImageSurface) Stroke(path *Path, style StrokeStyle) {
	if s.closed || path == nil || path.IsEmpty() {
		return
	}

	// Get stroke color
	strokeColor := s.resolveColor(style.Color, style.Pattern)

	// Create stroke path by expanding the original path
	strokePath := s.expandStroke(path, style)
	if strokePath == nil || strokePath.IsEmpty() {
		return
	}

	// Build edges from expanded stroke path
	s.edgeBuilder.Reset()
	s.edgeBuilder.SetFlattenCurves(false)
	s.edgeBuilder.BuildFromPath(strokePath, raster.IdentityTransform{})

	if s.edgeBuilder.IsEmpty() {
		return
	}

	// Render as filled shape (stroke expansion creates filled outline)
	s.filler.Reset()
	s.filler.Fill(s.edgeBuilder, raster.FillRuleNonZero, func(y int, runs *raster.AlphaRuns) {
		if y < 0 || y >= s.height {
			return
		}
		s.blendRow(y, runs, strokeColor)
	})
}

// DrawImage draws an image onto the surface.
//
// The source rectangle (opts.SrcRect, defaulting to the image's full bounds)
// is mapped onto the destination rectangle (opts.DstRect, defaulting to a
// rectangle of the source's size anchored at at). When the rectangles differ
// in size the image is scaled with opts.Filter.
//
// A negative width or height in DstRect mirrors the image along that axis,
// so flipped thumbnails and previews can be produced without a transform.
//
// Sampling is performed in premultiplied alpha space, then converted back to
// straight alpha and composited with the existing source-over rules. This
// avoids dark fringes around transparent edges. opts.Alpha is applied to the
// sampled alpha before compositing.
//
// The destination is clipped to the surface bounds and the source rectangle
// is clipped to the image bounds, so partially visible or oversized draws are
// safe and never allocate based on the destination size. Calls that replicate
// the previous 1:1 nearest-neighbor behavior produce bit-identical results.
func (s *ImageSurface) DrawImage(img image.Image, at Point, opts *DrawImageOptions) {
	if s.closed || img == nil {
		return
	}

	imgBounds := img.Bounds()
	if imgBounds.Empty() {
		return
	}

	srcRect, dstRect, dstGiven, filter := resolveDrawImageOptions(opts, imgBounds)
	alpha, ok := resolveDrawImageAlpha(opts)
	if !ok {
		return
	}

	// Signed source span. A negative span mirrors source sampling.
	srcSpanW := float64(srcRect.Max.X) - float64(srcRect.Min.X)
	srcSpanH := float64(srcRect.Max.Y) - float64(srcRect.Min.Y)
	if srcSpanW == 0 || srcSpanH == 0 {
		return
	}

	// Resolve the destination origin and signed span.
	var (
		dstX, dstY         int
		dstSpanW, dstSpanH float64
	)
	if dstGiven {
		dstX, dstY = dstRect.Min.X, dstRect.Min.Y
		dstSpanW = float64(dstRect.Max.X) - float64(dstRect.Min.X)
		dstSpanH = float64(dstRect.Max.Y) - float64(dstRect.Min.Y)
	} else {
		// Preserve the historical truncation of the anchor position.
		if !isFiniteCoord(at.X) || !isFiniteCoord(at.Y) {
			return
		}
		dstX, dstY = int(at.X), int(at.Y)
		dstSpanW, dstSpanH = srcSpanW, srcSpanH
	}

	if dstSpanW == 0 || dstSpanH == 0 || !isFiniteCoord(dstSpanW) || !isFiniteCoord(dstSpanH) {
		return
	}

	// Fast path: unmirrored 1:1 blit. All filters are mathematically the
	// identity at exact pixel centers, so taking the historical loop here
	// guarantees that equal-size calls remain pixel-identical after the
	// scaling work was introduced (including its exact alpha handling).
	if dstSpanW == srcSpanW && dstSpanH == srcSpanH &&
		dstSpanW > 0 && dstSpanH > 0 {
		s.drawImageBlit(img, srcRect, dstX, dstY, alpha)
		return
	}

	s.drawImageScaled(img, srcRect, dstX, dstY, dstSpanW, dstSpanH, filter, alpha)
}

// resolveDrawImageOptions returns the effective source and destination
// rectangles plus the filter. dstGiven reports whether the caller supplied a
// destination rectangle.
func resolveDrawImageOptions(opts *DrawImageOptions, imgBounds image.Rectangle) (
	srcRect image.Rectangle,
	dstRect image.Rectangle,
	dstGiven bool,
	filter Filter,
) {
	srcRect = imgBounds
	if opts == nil {
		return srcRect, dstRect, false, FilterNearest
	}
	if opts.SrcRect != nil {
		srcRect = *opts.SrcRect
	}
	if opts.DstRect != nil {
		dstRect = *opts.DstRect
		dstGiven = true
	}
	filter = opts.Filter
	switch filter {
	case FilterNearest, FilterBilinear, FilterBicubic:
	default:
		filter = FilterNearest
	}
	return srcRect, dstRect, dstGiven, filter
}

// resolveDrawImageAlpha normalizes the requested opacity.
//
// The zero value keeps the historical meaning of fully transparent: the
// pre-scaling implementation multiplied every source pixel by Alpha, so a zero
// value produced no visible change. Callers that want a visible draw must pass
// Alpha > 0 (DefaultDrawImageOptions returns 1.0).
//
// NaN and +Inf were effectively opaque before (the historical "< 1.0" check was
// false) and remain so. Negative values and -Inf draw nothing, which is the
// deterministic reading of the old "multiply by a negative alpha" path.
func resolveDrawImageAlpha(opts *DrawImageOptions) (float64, bool) {
	if opts == nil {
		return 1, true
	}
	alpha := opts.Alpha
	switch {
	case math.IsNaN(alpha), math.IsInf(alpha, 1):
		return 1, true
	case math.IsInf(alpha, -1), alpha <= 0:
		return 0, false
	case alpha > 1:
		return 1, true
	}
	return alpha, true
}

// drawImageBlit is the historical 1:1 nearest-neighbor path. It produces the
// same pixels as earlier releases, including their alpha handling, but clips
// the iteration to the surface so an oversized source/destination rectangle
// cannot drive unbounded work.
func (s *ImageSurface) drawImageBlit(img image.Image, srcBounds image.Rectangle, dstX, dstY int, alpha float64) {
	w := int64(srcBounds.Max.X) - int64(srcBounds.Min.X)
	h := int64(srcBounds.Max.Y) - int64(srcBounds.Min.Y)
	if w <= 0 || h <= 0 {
		return
	}

	dx0, dy0 := int64(dstX), int64(dstY)
	cx0 := maxInt64(dx0, 0)
	cx1 := minInt64(dx0+w, int64(s.width))
	cy0 := maxInt64(dy0, 0)
	cy1 := minInt64(dy0+h, int64(s.height))
	if cx0 >= cx1 || cy0 >= cy1 {
		return
	}

	imgBounds := img.Bounds()
	srcMinX := int64(srcBounds.Min.X)
	srcMinY := int64(srcBounds.Min.Y)

	for dy := cy0; dy < cy1; dy++ {
		sy64 := srcMinY + (dy - dy0)
		if sy64 < int64(imgBounds.Min.Y) || sy64 >= int64(imgBounds.Max.Y) {
			// Outside the image bounds; standard image.Image.At returns a
			// transparent color there, which is a no-op for source-over.
			continue
		}
		sy := int(sy64)
		for dx := cx0; dx < cx1; dx++ {
			sx64 := srcMinX + (dx - dx0)
			if sx64 < int64(imgBounds.Min.X) || sx64 >= int64(imgBounds.Max.X) {
				continue
			}
			srcColor := img.At(int(sx64), sy)
			if alpha < 1.0 {
				srcColor = s.applyAlpha(srcColor, alpha)
			}
			s.blendPixel(int(dx), int(dy), srcColor)
		}
	}
}

// drawImageScaled maps src onto the destination rectangle, sampling with the
// requested filter. The source and destination spans are signed: a negative
// span mirrors sampling/drawing along that axis.
func (s *ImageSurface) drawImageScaled(
	img image.Image,
	src image.Rectangle,
	dstX, dstY int,
	dstSpanW, dstSpanH float64,
	filter Filter,
	alpha float64,
) {
	imgBounds := img.Bounds()

	// Valid source pixel range: requested rectangle ∩ image bounds. Sampling
	// is clamped to this range so a sub-rectangle never bleeds in unrelated
	// neighbors and out-of-image coordinates are never read.
	loX := maxInt(minInt(src.Min.X, src.Max.X), imgBounds.Min.X)
	hiX := minInt(maxInt(src.Min.X, src.Max.X), imgBounds.Max.X)
	loY := maxInt(minInt(src.Min.Y, src.Max.Y), imgBounds.Min.Y)
	hiY := minInt(maxInt(src.Min.Y, src.Max.Y), imgBounds.Max.Y)
	if loX >= hiX || loY >= hiY {
		return
	}

	x0, x1, ok := visibleRange(dstX, dstSpanW, s.width)
	if !ok {
		return
	}
	y0, y1, ok := visibleRange(dstY, dstSpanH, s.height)
	if !ok {
		return
	}

	srcSpanW := float64(src.Max.X) - float64(src.Min.X)
	srcSpanH := float64(src.Max.Y) - float64(src.Min.Y)
	srcX0 := float64(src.Min.X)
	srcY0 := float64(src.Min.Y)

	// Cache the x mapping for the clipped column range.
	cols := x1 - x0
	srcXs := make([]float64, cols)
	validX := make([]bool, cols)
	for i := range cols {
		dx := x0 + i
		t := (float64(dx) + 0.5 - float64(dstX)) / dstSpanW
		if t < 0 || t >= 1 {
			continue
		}
		sx := srcX0 + t*srcSpanW
		// The epsilon admits the final texel so nearest sampling can honor
		// exact source edges despite floating-point rounding.
		if sx < float64(loX) || sx >= float64(hiX)+sourceEdgeEpsilon {
			continue
		}
		srcXs[i] = sx
		validX[i] = true
	}

	for dy := y0; dy < y1; dy++ {
		t := (float64(dy) + 0.5 - float64(dstY)) / dstSpanH
		if t < 0 || t >= 1 {
			continue
		}
		sy := srcY0 + t*srcSpanH
		if sy < float64(loY) || sy >= float64(hiY)+sourceEdgeEpsilon || math.IsNaN(sy) {
			continue
		}

		for i := range cols {
			if !validX[i] {
				continue
			}
			pr, pg, pb, pa := samplePremul(img, srcXs[i], sy, filter, loX, hiX, loY, hiY)
			c := unpremultiply(pr, pg, pb, pa, alpha)
			if c.A == 0 {
				continue
			}
			s.blendPixelAlpha(x0+i, dy, c, 255)
		}
	}
}

// samplePremul returns the filtered color in premultiplied 8-bit space.
func samplePremul(img image.Image, sx, sy float64, filter Filter, loX, hiX, loY, hiY int) (r, g, b, a float64) {
	switch filter {
	case FilterBilinear:
		return sampleBilinearPremul(img, sx, sy, loX, hiX, loY, hiY)
	case FilterBicubic:
		return sampleBicubicPremul(img, sx, sy, loX, hiX, loY, hiY)
	default:
		return sampleNearestPremul(img, sx, sy, loX, hiX, loY, hiY)
	}
}

// sampleNearestPremul selects the pixel containing (sx, sy).
func sampleNearestPremul(img image.Image, sx, sy float64, loX, hiX, loY, hiY int) (r, g, b, a float64) {
	x := clampInt(int(math.Floor(sx)), loX, hiX-1)
	y := clampInt(int(math.Floor(sy)), loY, hiY-1)
	return premulAt(img, x, y)
}

// sampleBilinearPremul performs bilinear filtering in premultiplied space.
func sampleBilinearPremul(img image.Image, sx, sy float64, loX, hiX, loY, hiY int) (r, g, b, a float64) {
	fx := sx - 0.5
	fy := sy - 0.5
	x0f := math.Floor(fx)
	y0f := math.Floor(fy)
	tx := fx - x0f
	ty := fy - y0f
	x0 := clampInt(int(x0f), loX, hiX-1)
	x1 := clampInt(int(x0f)+1, loX, hiX-1)
	y0 := clampInt(int(y0f), loY, hiY-1)
	y1 := clampInt(int(y0f)+1, loY, hiY-1)

	r00, g00, b00, a00 := premulAt(img, x0, y0)
	r10, g10, b10, a10 := premulAt(img, x1, y0)
	r01, g01, b01, a01 := premulAt(img, x0, y1)
	r11, g11, b11, a11 := premulAt(img, x1, y1)

	r = lerp2D(r00, r10, r01, r11, tx, ty)
	g = lerp2D(g00, g10, g01, g11, tx, ty)
	b = lerp2D(b00, b10, b01, b11, tx, ty)
	a = lerp2D(a00, a10, a01, a11, tx, ty)
	return r, g, b, a
}

// sampleBicubicPremul performs Catmull-Rom bicubic filtering in premultiplied space.
func sampleBicubicPremul(img image.Image, sx, sy float64, loX, hiX, loY, hiY int) (r, g, b, a float64) {
	fx := sx - 0.5
	fy := sy - 0.5
	xi := int(math.Floor(fx))
	yi := int(math.Floor(fy))
	tx := fx - float64(xi)
	ty := fy - float64(yi)

	wx := [4]float64{
		cubicWeight(tx + 1),
		cubicWeight(tx),
		cubicWeight(tx - 1),
		cubicWeight(tx - 2),
	}
	wy := [4]float64{
		cubicWeight(ty + 1),
		cubicWeight(ty),
		cubicWeight(ty - 1),
		cubicWeight(ty - 2),
	}

	for j := -1; j <= 2; j++ {
		py := clampInt(yi+j, loY, hiY-1)
		for i := -1; i <= 2; i++ {
			px := clampInt(xi+i, loX, hiX-1)
			cr, cg, cb, ca := premulAt(img, px, py)
			w := wx[i+1] * wy[j+1]
			r += cr * w
			g += cg * w
			b += cb * w
			a += ca * w
		}
	}
	return r, g, b, a
}

// premulAt returns the premultiplied 8-bit color of a pixel.
//
// Per the image package contract, *image.RGBA bytes are already
// premultiplied and *image.NRGBA bytes are straight, so the latter is
// premultiplied here. Other types go through color.Color.RGBA, which also
// yields premultiplied channels.
func premulAt(img image.Image, x, y int) (r, g, b, a float64) {
	switch im := img.(type) {
	case *image.RGBA:
		i := im.PixOffset(x, y)
		return float64(im.Pix[i+0]), float64(im.Pix[i+1]), float64(im.Pix[i+2]), float64(im.Pix[i+3])
	case *image.NRGBA:
		i := im.PixOffset(x, y)
		return straightToPremul(im.Pix[i+0], im.Pix[i+1], im.Pix[i+2], im.Pix[i+3])
	case *image.Gray:
		i := im.PixOffset(x, y)
		v := float64(im.Pix[i])
		return v, v, v, 255
	default:
		cr, cg, cb, ca := img.At(x, y).RGBA()
		// RGBA() returns 16-bit premultiplied channels.
		//nolint:gosec // G115: shifting a uint32 right by 8 fits uint8
		return float64(uint8(cr >> 8)), float64(uint8(cg >> 8)), float64(uint8(cb >> 8)), float64(uint8(ca >> 8))
	}
}

// straightToPremul converts straight 8-bit channels to premultiplied floats.
func straightToPremul(r, g, b, a uint8) (pr, pg, pb, pa float64) {
	if a == 0 {
		return 0, 0, 0, 0
	}
	af := float64(a)
	return float64(r) * af / 255.0, float64(g) * af / 255.0, float64(b) * af / 255.0, af
}

// unpremultiply converts a filtered premultiplied color back to straight
// 8-bit RGBA and applies opacity to the alpha channel.
//
// Catmull-Rom filtering can overshoot at sharp edges, producing premultiplied
// channels above the filtered alpha. They are clamped to [0, alpha] first so
// the recovered straight color is valid (no channel exceeds 255, no halo).
func unpremultiply(pr, pg, pb, pa, opacity float64) color.RGBA {
	pa = clampChannel(pa)
	if pa <= 0 {
		return color.RGBA{}
	}
	r := clampChannel(pr)
	g := clampChannel(pg)
	b := clampChannel(pb)
	// A premultiplied channel can never exceed its alpha.
	r = math.Min(r, pa)
	g = math.Min(g, pa)
	b = math.Min(b, pa)
	inv := 255.0 / pa
	r = clampChannel(r * inv)
	g = clampChannel(g * inv)
	b = clampChannel(b * inv)
	a := pa
	if opacity < 1 {
		// Truncate like the historical applyAlpha/rescale path.
		a = float64(uint8(clampChannel(a * opacity)))
	}
	//nolint:gosec // G115: values are clamped to [0, 255]
	return color.RGBA{
		R: uint8(math.Round(r)),
		G: uint8(math.Round(g)),
		B: uint8(math.Round(b)),
		A: uint8(math.Round(a)),
	}
}

// lerp2D performs bilinear interpolation on a 2x2 grid.
func lerp2D(v00, v10, v01, v11, tx, ty float64) float64 {
	v0 := v00 + (v10-v00)*tx
	v1 := v01 + (v11-v01)*tx
	return v0 + (v1-v0)*ty
}

// cubicWeight computes the Catmull-Rom cubic weight for distance t.
func cubicWeight(t float64) float64 {
	absT := math.Abs(t)
	if absT < 1 {
		return 1.5*absT*absT*absT - 2.5*absT*absT + 1.0
	}
	if absT < 2 {
		return -0.5*absT*absT*absT + 2.5*absT*absT - 4.0*absT + 2.0
	}
	return 0
}

// visibleRange returns the surface pixel indices that may intersect the
// signed destination span [min, min+span). The result is clipped to
// [0, limit), so oversized destinations never drive unbounded work.
func visibleRange(origin int, span float64, limit int) (int, int, bool) {
	if span == 0 || math.IsNaN(span) || math.IsInf(span, 0) {
		return 0, 0, false
	}
	a := float64(origin)
	b := a + span
	lo := math.Min(a, b)
	hi := math.Max(a, b)
	if hi <= 0 || lo >= float64(limit) {
		return 0, 0, false
	}
	lo = math.Max(lo, 0)
	hi = math.Min(hi, float64(limit))
	if lo >= hi {
		return 0, 0, false
	}
	// Clamp in float space before converting so out-of-range spans cannot
	// overflow int on 32-bit platforms.
	x0 := int(math.Floor(lo))
	x1 := int(math.Ceil(hi))
	if x0 < 0 {
		x0 = 0
	}
	if x1 > limit {
		x1 = limit
	}
	if x0 >= x1 {
		return 0, 0, false
	}
	return x0, x1, true
}

// isFiniteCoord reports whether v can be safely converted to int.
func isFiniteCoord(v float64) bool {
	const maxCoord = float64(1 << 62)
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= -maxCoord && v <= maxCoord
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// sourceEdgeEpsilon tolerates floating-point round-off when comparing a
// mapped source coordinate with the exclusive upper edge of the source rect.
const sourceEdgeEpsilon = 1e-9

// clampChannel clamps a channel value to the 8-bit range, treating NaN and
// negative values as zero.
func clampChannel(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 255 {
		return 255
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

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// Flush ensures all pending operations are complete.
// For ImageSurface, this is a no-op.
func (s *ImageSurface) Flush() error {
	return nil
}

// Snapshot returns a copy of the current surface contents.
func (s *ImageSurface) Snapshot() *image.RGBA {
	if s.closed {
		return nil
	}

	result := image.NewRGBA(image.Rect(0, 0, s.width, s.height))
	copy(result.Pix, s.img.Pix)
	return result
}

// Close releases resources associated with the surface.
func (s *ImageSurface) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	s.img = nil
	s.filler = nil
	s.edgeBuilder = nil
	return nil
}

// Image returns the underlying image.RGBA.
// This is a direct reference, not a copy.
func (s *ImageSurface) Image() *image.RGBA {
	return s.img
}

// Capabilities returns the surface capabilities.
func (s *ImageSurface) Capabilities() Capabilities {
	return Capabilities{
		SupportsSubSurface: false,
		SupportsResize:     false,
		SupportsClipping:   false,
		SupportsBlendModes: false,
		SupportsAntialias:  true,
		MaxWidth:           0, // Unlimited
		MaxHeight:          0,
	}
}

// resolveColor extracts color from Color or Pattern.
// Note: Pattern support is planned for future; currently uses color only.
func (s *ImageSurface) resolveColor(c color.Color, _ Pattern) color.RGBA {
	if c != nil {
		r, g, b, a := c.RGBA()
		//nolint:gosec // G115: safe - r>>8 is always in [0, 255]
		return color.RGBA{
			R: uint8(r >> 8),
			G: uint8(g >> 8),
			B: uint8(b >> 8),
			A: uint8(a >> 8),
		}
	}
	// Pattern would be sampled per-pixel, but for now use black
	return color.RGBA{0, 0, 0, 255}
}

// blendRow blends alpha runs onto a scanline.
func (s *ImageSurface) blendRow(y int, runs *raster.AlphaRuns, c color.RGBA) {
	for x, alpha := range runs.Iter() {
		if x < 0 || x >= s.width {
			continue
		}
		s.blendPixelAlpha(x, y, c, alpha)
	}
}

// blendPixelAlpha blends a color with coverage alpha onto the image.
func (s *ImageSurface) blendPixelAlpha(x, y int, src color.RGBA, alpha uint8) {
	if alpha == 0 {
		return
	}

	idx := s.img.PixOffset(x, y)

	if alpha == 255 && src.A == 255 {
		// Fully opaque - direct write
		s.img.Pix[idx+0] = src.R
		s.img.Pix[idx+1] = src.G
		s.img.Pix[idx+2] = src.B
		s.img.Pix[idx+3] = src.A
		return
	}

	// Source-over compositing with coverage
	// srcA = src.A * alpha / 255
	srcA := uint32(src.A) * uint32(alpha) / 255
	invSrcA := 255 - srcA

	dstR := uint32(s.img.Pix[idx+0])
	dstG := uint32(s.img.Pix[idx+1])
	dstB := uint32(s.img.Pix[idx+2])
	dstA := uint32(s.img.Pix[idx+3])

	outA := srcA + dstA*invSrcA/255
	if outA == 0 {
		return
	}

	outR := (uint32(src.R)*srcA + dstR*dstA*invSrcA/255) / outA
	outG := (uint32(src.G)*srcA + dstG*dstA*invSrcA/255) / outA
	outB := (uint32(src.B)*srcA + dstB*dstA*invSrcA/255) / outA

	//nolint:gosec // G115: safe - values are clamped to [0, 255]
	s.img.Pix[idx+0] = uint8(outR)
	//nolint:gosec // G115: safe
	s.img.Pix[idx+1] = uint8(outG)
	//nolint:gosec // G115: safe
	s.img.Pix[idx+2] = uint8(outB)
	//nolint:gosec // G115: safe
	s.img.Pix[idx+3] = uint8(outA)
}

// blendPixel blends a color onto the image at (x, y).
func (s *ImageSurface) blendPixel(x, y int, src color.Color) {
	r, g, b, a := src.RGBA()
	//nolint:gosec // G115: safe - r>>8 is always in [0, 255]
	srcR := uint8(r >> 8)
	//nolint:gosec // G115: safe
	srcG := uint8(g >> 8)
	//nolint:gosec // G115: safe
	srcB := uint8(b >> 8)
	//nolint:gosec // G115: safe
	srcA := uint8(a >> 8)
	s.blendPixelAlpha(x, y, color.RGBA{srcR, srcG, srcB, srcA}, 255)
}

// applyAlpha multiplies a color's alpha by the given factor.
func (s *ImageSurface) applyAlpha(c color.Color, alpha float64) color.Color {
	r, g, b, a := c.RGBA()
	newA := uint16(float64(a) * alpha)
	//nolint:gosec // G115: safe - r,g,b are uint32 from RGBA() which fits uint16
	return color.RGBA64{
		R: uint16(r),
		G: uint16(g),
		B: uint16(b),
		A: newA,
	}
}

// expandStroke creates a filled path representing the stroke outline.
// This is a simplified implementation using parallel offset curves.
func (s *ImageSurface) expandStroke(path *Path, style StrokeStyle) *Path {
	if style.Width <= 0 {
		return nil
	}

	halfWidth := style.Width / 2.0
	result := NewPath()

	// Simple stroke expansion: create parallel paths
	// This is a basic implementation; production code would handle
	// caps, joins, and miters properly.

	var segments []strokeSegment
	var curX, curY, startX, startY float32

	pointIdx := 0
	for _, verb := range path.verbs {
		switch verb {
		case raster.MoveTo:
			startX = path.points[pointIdx]
			startY = path.points[pointIdx+1]
			curX, curY = startX, startY
			pointIdx += 2

		case raster.LineTo:
			x, y := path.points[pointIdx], path.points[pointIdx+1]
			segments = append(segments, strokeSegment{
				x0: curX, y0: curY,
				x1: x, y1: y,
			})
			curX, curY = x, y
			pointIdx += 2

		case raster.QuadTo:
			// Flatten quad to lines for stroke
			cx, cy := path.points[pointIdx], path.points[pointIdx+1]
			x, y := path.points[pointIdx+2], path.points[pointIdx+3]
			s.flattenQuadForStroke(curX, curY, cx, cy, x, y, &segments)
			curX, curY = x, y
			pointIdx += 4

		case raster.CubicTo:
			// Flatten cubic to lines for stroke
			c1x, c1y := path.points[pointIdx], path.points[pointIdx+1]
			c2x, c2y := path.points[pointIdx+2], path.points[pointIdx+3]
			x, y := path.points[pointIdx+4], path.points[pointIdx+5]
			s.flattenCubicForStroke(curX, curY, c1x, c1y, c2x, c2y, x, y, &segments)
			curX, curY = x, y
			pointIdx += 6

		case raster.Close:
			if curX != startX || curY != startY {
				segments = append(segments, strokeSegment{
					x0: curX, y0: curY,
					x1: startX, y1: startY,
				})
			}
			curX, curY = startX, startY
		}
	}

	if len(segments) == 0 {
		return nil
	}

	// Create expanded outline
	s.buildStrokeOutline(result, segments, halfWidth, style)
	return result
}

// strokeSegment represents a line segment for stroke expansion.
type strokeSegment struct {
	x0, y0, x1, y1 float32
}

// flattenQuadForStroke flattens a quadratic curve to line segments.
func (s *ImageSurface) flattenQuadForStroke(x0, y0, cx, cy, x1, y1 float32, segments *[]strokeSegment) {
	const tolerance = 0.5
	s.flattenQuadRecursive(x0, y0, cx, cy, x1, y1, tolerance, 0, segments)
}

func (s *ImageSurface) flattenQuadRecursive(x0, y0, cx, cy, x1, y1, tol float32, depth int, segments *[]strokeSegment) {
	if depth > 10 {
		*segments = append(*segments, strokeSegment{x0, y0, x1, y1})
		return
	}

	dx := x1 - x0
	dy := y1 - y0
	dcx := cx - x0
	dcy := cy - y0
	cross := dcx*dy - dcy*dx
	lenSq := dx*dx + dy*dy

	if lenSq < 1e-6 || cross*cross/lenSq < tol*tol {
		*segments = append(*segments, strokeSegment{x0, y0, x1, y1})
		return
	}

	q0x := (x0 + cx) * 0.5
	q0y := (y0 + cy) * 0.5
	q1x := (cx + x1) * 0.5
	q1y := (cy + y1) * 0.5
	mx := (q0x + q1x) * 0.5
	my := (q0y + q1y) * 0.5

	s.flattenQuadRecursive(x0, y0, q0x, q0y, mx, my, tol, depth+1, segments)
	s.flattenQuadRecursive(mx, my, q1x, q1y, x1, y1, tol, depth+1, segments)
}

// flattenCubicForStroke flattens a cubic curve to line segments.
func (s *ImageSurface) flattenCubicForStroke(x0, y0, c1x, c1y, c2x, c2y, x1, y1 float32, segments *[]strokeSegment) {
	const tolerance = 0.5
	s.flattenCubicRecursive(x0, y0, c1x, c1y, c2x, c2y, x1, y1, tolerance, 0, segments)
}

func (s *ImageSurface) flattenCubicRecursive(x0, y0, c1x, c1y, c2x, c2y, x1, y1, tol float32, depth int, segments *[]strokeSegment) {
	if depth > 10 {
		*segments = append(*segments, strokeSegment{x0, y0, x1, y1})
		return
	}

	dx := x1 - x0
	dy := y1 - y0
	lenSq := dx*dx + dy*dy

	if lenSq < 1e-6 {
		*segments = append(*segments, strokeSegment{x0, y0, x1, y1})
		return
	}

	dc1x := c1x - x0
	dc1y := c1y - y0
	cross1 := dc1x*dy - dc1y*dx

	dc2x := c2x - x0
	dc2y := c2y - y0
	cross2 := dc2x*dy - dc2y*dx

	maxCross := cross1
	if maxCross < 0 {
		maxCross = -maxCross
	}
	if cross2 > maxCross {
		maxCross = cross2
	}
	if -cross2 > maxCross {
		maxCross = -cross2
	}

	if maxCross*maxCross/lenSq < tol*tol {
		*segments = append(*segments, strokeSegment{x0, y0, x1, y1})
		return
	}

	m01x := (x0 + c1x) * 0.5
	m01y := (y0 + c1y) * 0.5
	m12x := (c1x + c2x) * 0.5
	m12y := (c1y + c2y) * 0.5
	m23x := (c2x + x1) * 0.5
	m23y := (c2y + y1) * 0.5
	m012x := (m01x + m12x) * 0.5
	m012y := (m01y + m12y) * 0.5
	m123x := (m12x + m23x) * 0.5
	m123y := (m12y + m23y) * 0.5
	mx := (m012x + m123x) * 0.5
	my := (m012y + m123y) * 0.5

	s.flattenCubicRecursive(x0, y0, m01x, m01y, m012x, m012y, mx, my, tol, depth+1, segments)
	s.flattenCubicRecursive(mx, my, m123x, m123y, m23x, m23y, x1, y1, tol, depth+1, segments)
}

// buildStrokeOutline creates the outline path from line segments.
// Note: style parameter reserved for future cap/join handling.
func (s *ImageSurface) buildStrokeOutline(result *Path, segments []strokeSegment, halfWidth float64, _ StrokeStyle) {
	if len(segments) == 0 {
		return
	}

	hw := float32(halfWidth)

	// Forward pass: create one side of the stroke
	type offsetPoint struct{ x, y float32 }
	forward := make([]offsetPoint, 0, len(segments)+1)
	backward := make([]offsetPoint, 0, len(segments)+1)

	for i, seg := range segments {
		dx := seg.x1 - seg.x0
		dy := seg.y1 - seg.y0
		length := float32(1e-10)
		if dx != 0 || dy != 0 {
			length = float32(sqrtf32(dx*dx + dy*dy))
		}

		// Normal vector
		nx := -dy / length * hw
		ny := dx / length * hw

		if i == 0 {
			// Start cap (butt for now)
			forward = append(forward, offsetPoint{seg.x0 + nx, seg.y0 + ny})
			backward = append(backward, offsetPoint{seg.x0 - nx, seg.y0 - ny})
		}

		// End of segment
		forward = append(forward, offsetPoint{seg.x1 + nx, seg.y1 + ny})
		backward = append(backward, offsetPoint{seg.x1 - nx, seg.y1 - ny})
	}

	// Build the outline path
	if len(forward) > 0 {
		result.MoveTo(float64(forward[0].x), float64(forward[0].y))
		for i := 1; i < len(forward); i++ {
			result.LineTo(float64(forward[i].x), float64(forward[i].y))
		}
	}

	// Connect to backward side
	for i := len(backward) - 1; i >= 0; i-- {
		result.LineTo(float64(backward[i].x), float64(backward[i].y))
	}

	result.Close()
}

// sqrtf32 computes square root for float32.
func sqrtf32(x float32) float32 {
	if x <= 0 {
		return 0
	}
	// Newton-Raphson iteration for sqrt
	r := x
	for i := 0; i < 3; i++ {
		r = 0.5 * (r + x/r)
	}
	return r
}

// Verify ImageSurface implements Surface interface.
var (
	_ Surface        = (*ImageSurface)(nil)
	_ CapableSurface = (*ImageSurface)(nil)
)

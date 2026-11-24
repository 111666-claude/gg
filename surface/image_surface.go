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

// DrawImage draws an image at the specified position.
//
// When opts is nil the image is blitted 1:1 at `at` with nearest-neighbor
// sampling (the historical default). opts.SrcRect selects a sub-rectangle of
// the source; opts.DstRect maps that source onto an arbitrary destination
// rectangle (scaling and flips). opts.Filter selects nearest or bilinear
// sampling and opts.Alpha applies an opacity multiplier.
//
// Sampling and compositing run in premultiplied-alpha space so transparent
// source pixels never darken semi-transparent edges. All geometry is clipped
// to the surface bounds before indexing; out-of-range, empty or oversized
// inputs degrade to a no-op rather than panicking.
func (s *ImageSurface) DrawImage(img image.Image, at Point, opts *DrawImageOptions) {
	if s.closed || img == nil {
		return
	}

	bounds := img.Bounds()
	if bounds.Empty() {
		return
	}

	srcBounds := bounds
	alpha := 1.0
	filter := FilterNearest
	var dstBounds *image.Rectangle
	if opts != nil {
		if opts.SrcRect != nil {
			srcBounds = *opts.SrcRect
		}
		alpha = opts.Alpha
		filter = opts.Filter
		dstBounds = opts.DstRect
	}

	srcBounds = srcBounds.Intersect(bounds)
	if srcBounds.Empty() {
		return
	}
	if alpha <= 0 {
		return
	}
	if alpha > 1 {
		alpha = 1
	}

	// Default destination: source-sized rectangle at `at`.
	dstX, dstY := int(at.X), int(at.Y)
	dstW, dstH := srcBounds.Dx(), srcBounds.Dy()
	if dstBounds != nil && !dstBounds.Empty() {
		dstX = dstBounds.Min.X
		dstY = dstBounds.Min.Y
		dstW = dstBounds.Dx()
		dstH = dstBounds.Dy()
	}
	if dstW == 0 || dstH == 0 {
		return
	}

	source := s.ensureRGBA(img)
	blitImageScaled(s.img, source, srcBounds, image.Rect(dstX, dstY, dstX+dstW, dstY+dstH), filter, alpha)
}

// ensureRGBA returns a tightly packed, premultiplied RGBA view of img. An
// *image.RGBA source is reused directly (its documented storage is
// premultiplied); other color models are converted once so the blit hot loop
// has a single representation.
func (s *ImageSurface) ensureRGBA(img image.Image) *image.RGBA {
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba
	}
	bounds := img.Bounds()
	packed := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(packed, packed.Bounds(), img, bounds.Min, draw.Src)
	return packed
}

// blitImageScaled maps srcBounds of source onto dstBounds of the destination
// image, inverse-mapping each destination pixel center and compositing the
// sampled premultiplied color source-over. Negative dimensions are normalized
// via the rectangle conventions; the iteration range is clipped to dst so no
// write leaves the buffer.
func blitImageScaled(dst, src *image.RGBA, srcBounds, dstBounds image.Rectangle, filter Filter, alpha float64) {
	srcW := srcBounds.Dx()
	srcH := srcBounds.Dy()
	dstW := dstBounds.Dx()
	dstH := dstBounds.Dy()
	if srcW <= 0 || srcH <= 0 || dstW <= 0 || dstH <= 0 {
		return
	}

	scaleX := float64(dstW) / float64(srcW)
	scaleY := float64(dstH) / float64(srcH)

	// Clip the destination iteration to the target buffer.
	x0 := maxIntSurface(dstBounds.Min.X, dst.Rect.Min.X)
	y0 := maxIntSurface(dstBounds.Min.Y, dst.Rect.Min.Y)
	x1 := minIntSurface(dstBounds.Max.X, dst.Rect.Max.X)
	y1 := minIntSurface(dstBounds.Max.Y, dst.Rect.Max.Y)

	maxSrcX := srcBounds.Max.X - 1
	maxSrcY := srcBounds.Max.Y - 1

	for dy := y0; dy < y1; dy++ {
		for dx := x0; dx < x1; dx++ {
			fx := (float64(dx-dstBounds.Min.X)+0.5)/scaleX - 0.5
			fy := (float64(dy-dstBounds.Min.Y)+0.5)/scaleY - 0.5
			u := fx + float64(srcBounds.Min.X)
			v := fy + float64(srcBounds.Min.Y)

			var sr, sg, sb, sa float64
			if filter == FilterBilinear {
				sr, sg, sb, sa = sampleBilinearSurface(src, u, v,
					float64(srcBounds.Min.X), float64(srcBounds.Min.Y),
					float64(maxSrcX), float64(maxSrcY))
			} else {
				// Nearest: round the texel-center coordinate.
				nx := clampIntSurface(int(math.Round(u)), srcBounds.Min.X, maxSrcX)
				ny := clampIntSurface(int(math.Round(v)), srcBounds.Min.Y, maxSrcY)
				sr, sg, sb, sa = fetchPremulSurface(src, nx, ny)
			}

			if alpha < 1 {
				sr *= alpha
				sg *= alpha
				sb *= alpha
				sa *= alpha
			}
			if sa <= 0 {
				continue
			}
			compositePremulSurface(dst, dx, dy, clampByteSurface(sr), clampByteSurface(sg), clampByteSurface(sb), clampByteSurface(sa))
		}
	}
}

// fetchPremulSurface reads one premultiplied texel as [0,255] floats.
func fetchPremulSurface(src *image.RGBA, x, y int) (r, g, b, a float64) {
	i := src.PixOffset(x, y)
	if i < 0 || i+3 >= len(src.Pix) {
		return 0, 0, 0, 0
	}
	a = float64(src.Pix[i+3])
	if a == 0 {
		return 0, 0, 0, 0
	}
	return float64(src.Pix[i]), float64(src.Pix[i+1]), float64(src.Pix[i+2]), a
}

// sampleBilinearSurface performs 2x2 bilinear filtering in premultiplied space
// with clamp-to-edge addressing.
func sampleBilinearSurface(src *image.RGBA, u, v, minX, minY, maxX, maxY float64) (r, g, b, a float64) {
	ix := floorIntSurface(u - 0.5)
	iy := floorIntSurface(v - 0.5)
	xf := u - 0.5 - math.Floor(u-0.5)
	yf := v - 0.5 - math.Floor(v-0.5)

	x0 := clampIntSurface(ix, int(minX), int(maxX))
	x1 := clampIntSurface(ix+1, int(minX), int(maxX))
	y0 := clampIntSurface(iy, int(minY), int(maxY))
	y1 := clampIntSurface(iy+1, int(minY), int(maxY))

	r00, g00, b00, a00 := fetchPremulSurface(src, x0, y0)
	r10, g10, b10, a10 := fetchPremulSurface(src, x1, y0)
	r01, g01, b01, a01 := fetchPremulSurface(src, x0, y1)
	r11, g11, b11, a11 := fetchPremulSurface(src, x1, y1)

	w00 := (1 - xf) * (1 - yf)
	w10 := xf * (1 - yf)
	w01 := (1 - xf) * yf
	w11 := xf * yf

	r = r00*w00 + r10*w10 + r01*w01 + r11*w11
	g = g00*w00 + g10*w10 + g01*w01 + g11*w11
	b = b00*w00 + b10*w10 + b01*w01 + b11*w11
	a = a00*w00 + a10*w10 + a01*w01 + a11*w11
	return
}

// compositePremulSurface blends a premultiplied source onto a premultiplied
// destination pixel with source-over.
func compositePremulSurface(dst *image.RGBA, x, y int, sr, sg, sb, sa byte) {
	i := dst.PixOffset(x, y)
	if i < 0 || i+3 >= len(dst.Pix) {
		return
	}
	if sa == 0 {
		return
	}
	if sa == 255 {
		dst.Pix[i] = sr
		dst.Pix[i+1] = sg
		dst.Pix[i+2] = sb
		dst.Pix[i+3] = 255
		return
	}
	inv := uint16(255 - sa)
	dst.Pix[i] = sr + byte((uint16(dst.Pix[i])*inv+127)/255)
	dst.Pix[i+1] = sg + byte((uint16(dst.Pix[i+1])*inv+127)/255)
	dst.Pix[i+2] = sb + byte((uint16(dst.Pix[i+2])*inv+127)/255)
	dst.Pix[i+3] = sa + byte((uint16(dst.Pix[i+3])*inv+127)/255)
}

func clampByteSurface(v float64) byte {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return byte(v + 0.5)
}

func clampIntSurface(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func floorIntSurface(v float64) int {
	i := int(v)
	if v < float64(i) {
		return i - 1
	}
	return i
}

func minIntSurface(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxIntSurface(a, b int) int {
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
var _ Surface = (*ImageSurface)(nil)
var _ CapableSurface = (*ImageSurface)(nil)

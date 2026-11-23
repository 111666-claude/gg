// Copyright 2026 The gogpu Authors
// SPDX-License-Identifier: MIT

package surface

import (
	"errors"
	"image"
	"image/color"
	"image/draw"
	"sync"

	"github.com/gogpu/gg/internal/raster"
)

// Shared pixel-storage errors.
var (
	// ErrSurfaceClosed is returned when an operation targets a closed
	// surface, or a surface whose root has been closed.
	ErrSurfaceClosed = errors.New("surface: surface is closed")

	// ErrEmptySubSurface is returned when CreateSubSurface is given a
	// zero-area region.
	ErrEmptySubSurface = errors.New("surface: sub-surface bounds must be non-empty")

	// ErrInvalidSubSurfaceBounds is returned when CreateSubSurface is given
	// negative dimensions or a region outside the parent bounds.
	ErrInvalidSubSurfaceBounds = errors.New("surface: invalid sub-surface bounds")
)

// sharedPixels is the state shared by a root ImageSurface and all of its
// sub-surfaces. It owns the pixel storage (through the root image) and the
// mutex that serializes every drawing operation on the whole surface tree.
//
// The mutex is shared on purpose: parent and child surfaces alias the same
// pixels, so concurrent access through any two surfaces of the same tree
// would otherwise race. A single lock makes the tree safe to share across
// goroutines without forcing callers to coordinate which surface they hold.
type sharedPixels struct {
	mu sync.Mutex

	// rootImg is the full-size image that owns the backing array.
	// Sub-surfaces reference slices of rootImg.Pix via SubImage.
	rootImg *image.RGBA

	// rootClosed is set when the root surface is closed. A closed root
	// invalidates every sub-surface even though the sub-surfaces themselves
	// were never closed.
	rootClosed bool
}

// ImageSurface is a CPU-based surface that renders to an *image.RGBA.
//
// It uses raster.AnalyticFiller for high-quality anti-aliased rendering.
// This is the default surface implementation for software rendering.
//
// An ImageSurface created with NewImageSurface is a root surface and owns
// its pixel storage. Surfaces created with CreateSubSurface share the root
// storage: they describe a rectangular region through the root stride and an
// origin, so drawing to a sub-surface is immediately visible on the parent
// and on overlapping sibling sub-surfaces.
//
// All surfaces of one tree share a mutex; drawing operations are safe for
// concurrent use across goroutines.
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

	// origin is the top-left corner of this surface in root pixel
	// coordinates. It is (0, 0) for a root surface.
	origin image.Point

	// bounds is the absolute rectangle (root pixel coordinates) covered by
	// this surface. Local coordinates map to root coordinates by adding
	// origin.
	bounds image.Rectangle

	// isRoot reports whether this surface owns the shared storage.
	isRoot bool

	// parent is the surface this one was created from, or nil for a root.
	parent *ImageSurface

	// shared is the storage and lock shared by the whole surface tree.
	shared *sharedPixels

	// filler provides analytic anti-aliasing
	filler *raster.AnalyticFiller

	// edgeBuilder converts paths to edges
	edgeBuilder *raster.EdgeBuilder

	// clipMask is the per-pixel coverage mask for the current clip, in local
	// coordinates. nil means clipping is disabled.
	clipMask []uint8

	// clipStack stores saved clip masks for PushClip/PopClip.
	clipStack [][]uint8

	// closed tracks if Close has been called on this specific surface.
	closed bool
}

// NewImageSurface creates a new CPU-based surface with the given dimensions.
// Non-positive dimensions are clamped to 1, matching the historical behavior.
func NewImageSurface(width, height int) *ImageSurface {
	if width <= 0 {
		width = 1
	}
	if height <= 0 {
		height = 1
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	return newRootImageSurface(img)
}

// NewImageSurfaceFromImage creates a surface backed by an existing image.
// The surface will render into the provided image directly.
func NewImageSurfaceFromImage(img *image.RGBA) *ImageSurface {
	return newRootImageSurface(img)
}

// newRootImageSurface builds the root surface for the given image.
func newRootImageSurface(img *image.RGBA) *ImageSurface {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	return &ImageSurface{
		width:       width,
		height:      height,
		img:         img,
		origin:      bounds.Min,
		bounds:      bounds,
		isRoot:      true,
		shared:      &sharedPixels{rootImg: img},
		filler:      raster.NewAnalyticFiller(width, height),
		edgeBuilder: raster.NewEdgeBuilder(2), // 4x AA quality
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

// Origin returns the top-left corner of this surface in the root image's
// pixel coordinate system. It is (0, 0) for a root surface.
func (s *ImageSurface) Origin() image.Point {
	return s.origin
}

// Bounds returns the absolute rectangle covered by this surface in root
// pixel coordinates. A root surface returns its full image bounds; a
// sub-surface returns the region it aliases.
func (s *ImageSurface) Bounds() image.Rectangle {
	return s.bounds
}

// Parent returns the surface this sub-surface was created from, or nil for a
// root surface.
func (s *ImageSurface) Parent() *ImageSurface {
	return s.parent
}

// IsSubSurface reports whether this surface aliases a region of a parent.
func (s *ImageSurface) IsSubSurface() bool {
	return !s.isRoot
}

// aliveLocked reports whether the surface may accept drawing operations.
// The caller must hold shared.mu.
func (s *ImageSurface) aliveLocked() bool {
	return !s.closed && !s.shared.rootClosed && s.img != nil
}

// begin locks the shared mutex and reports whether the surface is usable.
// On a false return the mutex is not held; on a true return the caller must
// unlock shared.mu (typically via defer).
func (s *ImageSurface) begin() bool {
	s.shared.mu.Lock()
	if !s.aliveLocked() {
		s.shared.mu.Unlock()
		return false
	}
	return true
}

// Clear fills the entire surface with the given color.
// Clear ignores the active clip; it always repaints the full local bounds.
func (s *ImageSurface) Clear(c color.Color) {
	if !s.begin() {
		return
	}
	defer s.shared.mu.Unlock()

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
	if path == nil || path.IsEmpty() {
		return
	}
	if !s.begin() {
		return
	}
	defer s.shared.mu.Unlock()

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
	if path == nil || path.IsEmpty() {
		return
	}
	if !s.begin() {
		return
	}
	defer s.shared.mu.Unlock()

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
// If opts is nil, default options are used. When opts.DstRect is set, the
// source is scaled into that rectangle (local coordinates); otherwise it is
// blitted 1:1 at at.
func (s *ImageSurface) DrawImage(img image.Image, at Point, opts *DrawImageOptions) {
	if img == nil || !s.begin() {
		return
	}
	defer s.shared.mu.Unlock()

	// Preserve the historical convention: a nil opts means fully opaque,
	// otherwise opts.Alpha is taken verbatim (0 = fully transparent).
	alpha := 1.0
	if opts != nil {
		alpha = opts.Alpha
	}
	if opts != nil && opts.DstRect != nil {
		s.drawImageScaled(img, opts, alpha)
		return
	}
	s.drawImageBlit(img, at, opts, alpha)
}

// drawImageBlit performs the 1:1 nearest-neighbor blit.
func (s *ImageSurface) drawImageBlit(img image.Image, at Point, opts *DrawImageOptions, alpha float64) {
	srcBounds := img.Bounds()
	if opts != nil && opts.SrcRect != nil {
		srcBounds = *opts.SrcRect
	}
	if srcBounds.Empty() {
		return
	}

	dstX := int(at.X)
	dstY := int(at.Y)

	// Simple nearest-neighbor blit for now
	for sy := srcBounds.Min.Y; sy < srcBounds.Max.Y; sy++ {
		dy := dstY + (sy - srcBounds.Min.Y)
		if dy < 0 || dy >= s.height {
			continue
		}

		for sx := srcBounds.Min.X; sx < srcBounds.Max.X; sx++ {
			dx := dstX + (sx - srcBounds.Min.X)
			if dx < 0 || dx >= s.width {
				continue
			}

			srcColor := img.At(sx, sy)
			if alpha < 1.0 {
				srcColor = s.applyAlpha(srcColor, alpha)
			}
			if !s.applyClip(dx, dy, &srcColor) {
				continue
			}
			s.blendPixel(dx, dy, srcColor)
		}
	}
}

// drawImageScaled draws the source scaled into opts.DstRect (local coords).
func (s *ImageSurface) drawImageScaled(img image.Image, opts *DrawImageOptions, alpha float64) {
	srcBounds := img.Bounds()
	if opts.SrcRect != nil {
		srcBounds = *opts.SrcRect
	}
	if srcBounds.Empty() {
		return
	}

	dstFull := opts.DstRect.Canon()
	dst := dstFull.Intersect(image.Rect(0, 0, s.width, s.height))
	if dst.Empty() {
		return
	}

	dw := dstFull.Dx()
	dh := dstFull.Dy()
	if dw <= 0 || dh <= 0 {
		return
	}

	bilinear := opts.Filter == FilterBilinear
	sxScale := float64(srcBounds.Dx()) / float64(dw)
	syScale := float64(srcBounds.Dy()) / float64(dh)

	for dy := dst.Min.Y; dy < dst.Max.Y; dy++ {
		fy := (float64(dy-dstFull.Min.Y)+0.5)*syScale - 0.5
		for dx := dst.Min.X; dx < dst.Max.X; dx++ {
			fx := (float64(dx-dstFull.Min.X)+0.5)*sxScale - 0.5
			srcColor := sampleSource(img, srcBounds, fx, fy, bilinear)
			if alpha < 1.0 {
				srcColor = s.applyAlpha(srcColor, alpha)
			}
			if !s.applyClip(dx, dy, &srcColor) {
				continue
			}
			s.blendPixel(dx, dy, srcColor)
		}
	}
}

// Flush ensures all pending operations are complete.
// For ImageSurface, this is a no-op.
func (s *ImageSurface) Flush() error {
	return nil
}

// Snapshot returns an independent copy of the current local contents.
// The returned image never shares pixels with the surface, so it stays
// valid after the surface is closed or drawn to again.
func (s *ImageSurface) Snapshot() *image.RGBA {
	if !s.begin() {
		return nil
	}
	defer s.shared.mu.Unlock()

	return s.copySnapshotLocked()
}

// SnapshotWith returns the current local contents as an RGBA image.
//
// When copyPixels is true (equivalent to Snapshot) the returned image is an
// independent copy. When false, a read-only view backed by the shared pixel
// storage is returned without copying: it reflects later draws and must not
// be modified by the caller, and becomes invalid once the root surface is
// closed.
func (s *ImageSurface) SnapshotWith(copyPixels bool) *image.RGBA {
	if !s.begin() {
		return nil
	}
	defer s.shared.mu.Unlock()

	if copyPixels {
		return s.copySnapshotLocked()
	}
	view, _ := s.img.SubImage(s.img.Bounds()).(*image.RGBA)
	return view
}

// SnapshotView returns a zero-copy read-only view of the local contents,
// backed by the shared pixel storage. See SnapshotWith for the semantics.
func (s *ImageSurface) SnapshotView() *image.RGBA {
	return s.SnapshotWith(false)
}

// copySnapshotLocked copies the local region into a tightly packed RGBA
// image. Rows are copied individually because sub-surface images keep the
// root stride. The caller must hold shared.mu.
func (s *ImageSurface) copySnapshotLocked() *image.RGBA {
	result := image.NewRGBA(image.Rect(0, 0, s.width, s.height))
	rowBytes := s.width * 4
	for y := 0; y < s.height; y++ {
		srcOff := s.localPixOffset(0, y)
		dstOff := result.PixOffset(0, y)
		copy(result.Pix[dstOff:dstOff+rowBytes], s.img.Pix[srcOff:srcOff+rowBytes])
	}
	return result
}

// Close releases resources associated with this surface.
//
// Closing a sub-surface only releases that sub-surface's own rasterizers;
// the shared pixel storage and every other surface of the tree keep working.
// Closing the root surface invalidates the whole tree. Close is idempotent.
func (s *ImageSurface) Close() error {
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true
	if s.isRoot {
		s.shared.rootClosed = true
	}

	s.img = nil
	s.filler = nil
	s.edgeBuilder = nil
	s.clipMask = nil
	s.clipStack = nil
	return nil
}

// Image returns the underlying image.RGBA.
// This is a direct reference, not a copy. For a sub-surface it is a
// SubImage view sharing the root stride and pixel array.
// Returns nil after Close.
func (s *ImageSurface) Image() *image.RGBA {
	return s.img
}

// Capabilities returns the surface capabilities.
func (s *ImageSurface) Capabilities() Capabilities {
	return Capabilities{
		SupportsSubSurface: true,
		SupportsResize:     false,
		SupportsClipping:   true,
		SupportsBlendModes: false,
		SupportsAntialias:  true,
		MaxWidth:           0, // Unlimited
		MaxHeight:          0,
	}
}

// applyClip multiplies the color's alpha by the clip coverage at (x, y).
// It returns false when the clip rejects the pixel entirely.
// A nil clip mask leaves the color untouched.
func (s *ImageSurface) applyClip(x, y int, c *color.Color) bool {
	if s.clipMask == nil {
		return true
	}
	coverage := s.clipMask[y*s.width+x]
	if coverage == 0 {
		return false
	}
	if coverage != 255 {
		*c = s.applyAlpha(*c, float64(coverage)/255.0)
	}
	return true
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

// blendRow blends alpha runs onto a scanline, honoring the clip mask.
func (s *ImageSurface) blendRow(y int, runs *raster.AlphaRuns, c color.RGBA) {
	for x, alpha := range runs.Iter() {
		if x < 0 || x >= s.width {
			continue
		}
		if s.clipMask != nil {
			alpha = multiplyCoverage(alpha, s.clipMask[y*s.width+x])
			if alpha == 0 {
				continue
			}
		}
		s.blendPixelAlpha(x, y, c, alpha)
	}
}

// multiplyCoverage combines rasterizer coverage with clip coverage.
func multiplyCoverage(a, b uint8) uint8 {
	return uint8(uint32(a) * uint32(b) / 255) //nolint:gosec // G115: result fits uint8
}

// localPixOffset returns the Pix index for a local (x, y) coordinate.
//
// A sub-surface image keeps the root Rect.Min (its origin), so the stdlib
// PixOffset would subtract that origin and produce a negative index for
// local coordinates. The pixel array of an image.RGBA is laid out so that
// the local top-left corner sits at offset zero, hence a stride-based
// offset works for both root surfaces and sub-surfaces.
func (s *ImageSurface) localPixOffset(x, y int) int {
	return y*s.img.Stride + x*4
}

// blendPixelAlpha blends a color with coverage alpha onto the image.
func (s *ImageSurface) blendPixelAlpha(x, y int, src color.RGBA, alpha uint8) {
	if alpha == 0 {
		return
	}

	idx := s.localPixOffset(x, y)

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

// sampleSource samples img at fractional coordinates relative to srcBounds.
// Bilinear sampling interpolates the four nearest pixels; nearest sampling
// rounds toward the top-left.
func sampleSource(img image.Image, srcBounds image.Rectangle, fx, fy float64, bilinear bool) color.Color {
	if !bilinear {
		x := srcBounds.Min.X + intFloor(fx)
		y := srcBounds.Min.Y + intFloor(fy)
		return img.At(clampInt(x, srcBounds.Min.X, srcBounds.Max.X-1),
			clampInt(y, srcBounds.Min.Y, srcBounds.Max.Y-1))
	}

	x0 := clampInt(srcBounds.Min.X+intFloor(fx), srcBounds.Min.X, srcBounds.Max.X-1)
	y0 := clampInt(srcBounds.Min.Y+intFloor(fy), srcBounds.Min.Y, srcBounds.Max.Y-1)
	x1 := clampInt(x0+1, srcBounds.Min.X, srcBounds.Max.X-1)
	y1 := clampInt(y0+1, srcBounds.Min.Y, srcBounds.Max.Y-1)

	tx := clampF64(fx-float64(intFloor(fx)), 0, 1)
	ty := clampF64(fy-float64(intFloor(fy)), 0, 1)

	return lerpColor(
		lerpColor(img.At(x0, y0), img.At(x1, y0), tx),
		lerpColor(img.At(x0, y1), img.At(x1, y1), tx),
		ty,
	)
}

// lerpColor linearly interpolates two colors in straight RGBA space.
func lerpColor(c0, c1 color.Color, t float64) color.Color {
	r0, g0, b0, a0 := c0.RGBA()
	r1, g1, b1, a1 := c1.RGBA()
	return color.RGBA64{
		R: uint16(float64(r0)*(1-t) + float64(r1)*t), //nolint:gosec // G115: interpolated 16-bit value
		G: uint16(float64(g0)*(1-t) + float64(g1)*t), //nolint:gosec // G115
		B: uint16(float64(b0)*(1-t) + float64(b1)*t), //nolint:gosec // G115
		A: uint16(float64(a0)*(1-t) + float64(a1)*t), //nolint:gosec // G115
	}
}

func intFloor(x float64) int {
	i := int(x)
	if x < float64(i) {
		return i - 1
	}
	return i
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

func clampF64(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Verify ImageSurface implements the surface interfaces.
var (
	_ Surface          = (*ImageSurface)(nil)
	_ SubSurface       = (*ImageSurface)(nil)
	_ ClippableSurface = (*ImageSurface)(nil)
	_ CapableSurface   = (*ImageSurface)(nil)
)

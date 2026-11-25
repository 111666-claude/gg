// Copyright 2026 The gogpu Authors
// SPDX-License-Identifier: MIT

package render

import (
	"errors"
	"fmt"
	"image/color"
	"math"

	"github.com/gogpu/gg/internal/raster"
)

// SoftwareRenderer is a CPU-based renderer using the core/ package algorithms.
//
// This renderer provides anti-aliased 2D rendering without any GPU dependencies.
// It uses AnalyticFiller for high-quality coverage-based anti-aliasing.
//
// Performance characteristics:
//   - Single-threaded (future: parallel scanline processing)
//   - O(n) where n is the number of pixels covered
//   - Memory: O(width) for scanline buffers
//
// Example:
//
//	renderer := render.NewSoftwareRenderer()
//	target := render.NewPixmapTarget(800, 600)
//	scene := render.NewScene()
//
//	scene.SetFillColor(color.RGBA{255, 0, 0, 255})
//	scene.Circle(400, 300, 100)
//	scene.Fill()
//
//	renderer.Render(target, scene)
//	img := target.Image()
type SoftwareRenderer struct {
	// edgeBuilder is reused for path-to-edge conversion.
	edgeBuilder *raster.EdgeBuilder

	// filler is reused for analytic anti-aliasing.
	filler *raster.AnalyticFiller

	// lastWidth and lastHeight track the filler dimensions.
	lastWidth, lastHeight int
}

// NewSoftwareRenderer creates a new CPU-based software renderer.
func NewSoftwareRenderer() *SoftwareRenderer {
	return &SoftwareRenderer{
		edgeBuilder: raster.NewEdgeBuilder(2), // 4x AA quality
	}
}

// Render draws the scene to the target.
//
// This method processes each drawing command in the scene and renders
// it to the target using CPU-based rasterization.
//
// Blend modes are fully supported on this renderer (see BlendMode). An
// unknown/unsupported blend mode fails with ErrUnsupportedBlendMode before any
// command writes to the target. Per-image validation failures skip just that
// image and are joined into the returned error while the rest of the scene is
// rendered, so one bad draw cannot silently corrupt the destination.
//
// Returns an error if the target is GPU-only (no Pixels() support), its pixel
// buffer is too small to index safely, or a command is deterministically
// invalid.
func (r *SoftwareRenderer) Render(target RenderTarget, scene *Scene) error {
	if target == nil {
		return errors.New("render: nil target")
	}

	pixels := target.Pixels()
	if pixels == nil {
		return errors.New("render: target does not support CPU rendering")
	}

	if scene == nil || scene.IsEmpty() {
		return nil
	}

	width := target.Width()
	height := target.Height()
	stride := target.Stride()

	if width < 0 || height < 0 {
		return fmt.Errorf("render: invalid target dimensions %dx%d", width, height)
	}
	if width == 0 || height == 0 {
		return nil
	}
	// A buffer must back every byte indexed by y*stride + x*4. Validate before
	// rasterizing so a short/lying target cannot trigger an index panic.
	needed := stride*(height-1) + width*4
	if stride < width*4 || needed > len(pixels) {
		return fmt.Errorf("render: target pixel buffer too small: need %d bytes, have %d",
			needed, len(pixels))
	}

	// Preflight blend modes atomically: reject an unknown mode before touching
	// the destination so failure never leaves a partially blended target.
	for _, cmd := range scene.drawCommands() {
		if _, ok := premulBlendFuncFor(cmd.blend); !ok {
			return fmt.Errorf("%w: %d", ErrUnsupportedBlendMode, uint8(cmd.blend))
		}
	}

	// Ensure filler is sized correctly
	r.ensureFiller(width, height)

	var drawErrs []error

	// Process each command
	for _, cmd := range scene.drawCommands() {
		switch cmd.op {
		case opClear:
			r.renderClear(pixels, width, height, stride, cmd.color)

		case opFill:
			if cmd.path != nil && !cmd.path.IsEmpty() {
				r.renderFill(pixels, width, height, stride, cmd.path, cmd.color,
					cmd.fillRule, cmd.blend, cmd.opacity, cmd.clip, cmd.clipActive)
			}

		case opStroke:
			if cmd.path != nil && !cmd.path.IsEmpty() {
				r.renderStroke(pixels, width, height, stride, cmd.path, cmd.color,
					cmd.width, cmd.blend, cmd.opacity, cmd.clip, cmd.clipActive)
			}

		case opImage:
			if err := r.renderImage(pixels, width, height, stride, cmd.image,
				cmd.clip, cmd.clipActive); err != nil {
				// Safe degradation: skip the offending image, keep rendering.
				drawErrs = append(drawErrs, err)
			}
		}
	}

	return errors.Join(drawErrs...)
}

// Flush ensures all rendering is complete.
// For the software renderer, this is a no-op as operations are synchronous.
func (r *SoftwareRenderer) Flush() error {
	return nil
}

// Capabilities returns the renderer's capabilities.
//
// The software renderer implements every BlendMode in this package on the CPU
// with premultiplied-alpha math, so SupportsBlendModes is true. Gradients and
// textures are reported conservatively: solid/scene image draws work, but the
// retained Scene has no gradient command path yet.
func (r *SoftwareRenderer) Capabilities() RendererCapabilities {
	return RendererCapabilities{
		IsGPU:                false,
		SupportsAntialiasing: true,
		SupportsBlendModes:   true,
		SupportsGradients:    false, // TODO: implement
		SupportsTextures:     true,  // Scene.DrawImage CPU blits are supported.
		MaxTextureSize:       maxSoftwareImageSize,
	}
}

// SupportsBlendMode reports whether m is implemented by this renderer.
func (r *SoftwareRenderer) SupportsBlendMode(m BlendMode) bool {
	return m.IsValid()
}

// SupportedBlendModes returns a fresh slice of every implemented blend mode.
func (r *SoftwareRenderer) SupportedBlendModes() []BlendMode {
	out := make([]BlendMode, int(blendModeCount))
	for i := BlendMode(0); i < blendModeCount; i++ {
		out[i] = i
	}
	return out
}

// maxSoftwareImageSize bounds accepted source image dimensions. It matches the
// common GPU texture cap and keeps w*h*4 arithmetic comfortably in int range.
const maxSoftwareImageSize = 32767

// ensureFiller ensures the filler is sized for the target dimensions.
func (r *SoftwareRenderer) ensureFiller(width, height int) {
	if r.filler == nil || r.lastWidth != width || r.lastHeight != height {
		r.filler = raster.NewAnalyticFiller(width, height)
		r.lastWidth = width
		r.lastHeight = height
	}
}

// renderClear fills the entire target with a solid color.
func (r *SoftwareRenderer) renderClear(pixels []byte, width, height, stride int, c color.Color) {
	cr, cg, cb, ca := c.RGBA()
	// Convert from 16-bit to 8-bit (mask ensures value fits in uint8)
	//nolint:gosec // G115: mask ensures no overflow
	pr := uint8((cr >> 8) & 0xFF)
	//nolint:gosec // G115: mask ensures no overflow
	pg := uint8((cg >> 8) & 0xFF)
	//nolint:gosec // G115: mask ensures no overflow
	pb := uint8((cb >> 8) & 0xFF)
	//nolint:gosec // G115: mask ensures no overflow
	pa := uint8((ca >> 8) & 0xFF)

	for y := 0; y < height; y++ {
		rowOffset := y * stride
		for x := 0; x < width; x++ {
			offset := rowOffset + x*4
			pixels[offset] = pr
			pixels[offset+1] = pg
			pixels[offset+2] = pb
			pixels[offset+3] = pa
		}
	}
}

// colorPremulBytes converts c to 8-bit premultiplied RGBA bytes. color.Color.
// RGBA already returns alpha-premultiplied 16-bit channels, so the high bytes
// are the exact premultiplied source values image.RGBA stores.
func colorPremulBytes(c color.Color) (pr, pg, pb, pa uint8) {
	if c == nil {
		return 0, 0, 0, 0
	}
	cr, cg, cb, ca := c.RGBA()
	//nolint:gosec // G115: >>8 yields a value in [0,255].
	return uint8(cr >> 8), uint8(cg >> 8), uint8(cb >> 8), uint8(ca >> 8)
}

// clipContainsPixel applies pixel-center clip semantics.
func clipContainsPixel(x, y int, clip clipRect, active bool) bool {
	if !active {
		return true
	}
	fx := float64(x) + 0.5
	fy := float64(y) + 0.5
	return fx >= clip.minX && fx < clip.maxX && fy >= clip.minY && fy < clip.maxY
}

// composeRunPixel composites one covered sample into the premultiplied RGBA
// target buffer. coverage is the raster coverage in [0,255]; opacity is the
// command alpha multiplier in [0,1]. All math is premultiplied alpha.
func composeRunPixel(pixels []byte, offset int,
	pr, pg, pb, pa uint8, coverage uint16, opacity float64, mode BlendMode,
) {
	if coverage == 0 {
		return
	}

	// Opacity scales alpha together with the already-premultiplied channels.
	if opacity < 1 {
		if opacity < 0 {
			opacity = 0
		} else if opacity > 1 {
			opacity = 1
		}
		ob := uint16(math.Round(opacity * 255))
		pa = byte(mul255(uint16(pa), ob))
		pr = byte(mul255(uint16(pr), ob))
		pg = byte(mul255(uint16(pg), ob))
		pb = byte(mul255(uint16(pb), ob))
	}

	// Coverage acts as an additional source alpha: scale alpha and the
	// premultiplied channels identically.
	sa8 := byte(mul255(uint16(pa), coverage))
	if sa8 == 0 {
		return
	}
	sr8 := byte(mul255(uint16(pr), coverage))
	sg8 := byte(mul255(uint16(pg), coverage))
	sb8 := byte(mul255(uint16(pb), coverage))

	dr := pixels[offset]
	dg := pixels[offset+1]
	db := pixels[offset+2]
	da := pixels[offset+3]

	fn, ok := premulBlendFuncFor(mode)
	if !ok {
		// Render preflights modes; this is a defensive explicit fallback.
		fn = blendSourceOver
	}

	or, og, ob2, oa := fn(sr8, sg8, sb8, sa8, dr, dg, db, da)
	pixels[offset] = or
	pixels[offset+1] = og
	pixels[offset+2] = ob2
	pixels[offset+3] = oa
}

// renderFill fills a path with a solid color.
func (r *SoftwareRenderer) renderFill(pixels []byte, width, height, stride int,
	path *pathBuilder, c color.Color, fillRule raster.FillRule,
	mode BlendMode, opacity float64, clip clipRect, clipActive bool,
) {
	pr, pg, pb, pa := colorPremulBytes(c)

	// Build edges from path
	r.edgeBuilder.Reset()
	r.edgeBuilder.SetFlattenCurves(false)

	// Bound edge coordinates to the target plus a small AA margin. Without
	// this, pathologically large finite coordinates overflow the fixed-point
	// edge math and can mis-rasterize or spin the filler.
	margin := float32(2)
	r.edgeBuilder.SetClipRect(&raster.Rect{
		MinX: -margin,
		MinY: -margin,
		MaxX: float32(width) + margin,
		MaxY: float32(height) + margin,
	})

	r.edgeBuilder.BuildFromPath(path, raster.IdentityTransform{})

	if r.edgeBuilder.IsEmpty() {
		return
	}

	// Reset filler
	r.filler.Reset()

	// Fill using analytic anti-aliasing
	r.filler.Fill(r.edgeBuilder, fillRule, func(y int, runs *raster.AlphaRuns) {
		if y < 0 || y >= height {
			return
		}

		rowOffset := y * stride

		// Blend each run to the pixel buffer
		for run := range runs.IterRuns() {
			if run.Alpha == 0 {
				continue
			}

			for i := 0; i < run.Count; i++ {
				x := run.X + i
				if x < 0 || x >= width {
					continue
				}
				if !clipContainsPixel(x, y, clip, clipActive) {
					continue
				}

				offset := rowOffset + x*4
				composeRunPixel(pixels, offset, pr, pg, pb, pa,
					uint16(run.Alpha), opacity, mode)
			}
		}
	})
}

// renderImage blits one validated image operation with nearest-neighbor mapping,
// honoring source crop, non-zero Bounds, opacity, blend and clip.
func (r *SoftwareRenderer) renderImage(pixels []byte, width, height, stride int,
	op *ImageDrawOp, clip clipRect, clipActive bool,
) error {
	if op == nil || op.Image == nil {
		return errors.New("render: nil image draw operation")
	}
	bounds := op.Image.Bounds()
	if bounds.Empty() {
		return errors.New("render: image has empty bounds")
	}
	if bounds.Dx() > maxSoftwareImageSize || bounds.Dy() > maxSoftwareImageSize {
		return fmt.Errorf("render: image %dx%d exceeds max %d",
			bounds.Dx(), bounds.Dy(), maxSoftwareImageSize)
	}

	src := op.Src
	if src.Empty() {
		src = bounds
	}
	src = src.Intersect(bounds)
	if src.Empty() {
		return errors.New("render: source rectangle outside image bounds")
	}

	dw := op.DstW
	dh := op.DstH
	if dw == 0 || dh == 0 {
		return errors.New("render: zero-sized image destination")
	}
	if math.IsNaN(op.DstX) || math.IsNaN(op.DstY) ||
		math.IsNaN(dw) || math.IsNaN(dh) ||
		math.IsInf(op.DstX, 0) || math.IsInf(op.DstY, 0) ||
		math.IsInf(dw, 0) || math.IsInf(dh, 0) {
		return errors.New("render: non-finite image destination")
	}
	if !op.Blend.IsValid() {
		return fmt.Errorf("%w: image %d", ErrUnsupportedBlendMode, uint8(op.Blend))
	}
	opacity := clampOpacity(op.Opacity)

	// Destination integer footprint. Negative extents are not needed by the
	// fixtures; clamp to a normal positive rect but keep coordinates exact for
	// the common case.
	x0 := int(math.Floor(op.DstX))
	y0 := int(math.Floor(op.DstY))
	x1 := int(math.Ceil(op.DstX + dw))
	y1 := int(math.Ceil(op.DstY + dh))
	if dw < 0 {
		x0, x1 = int(math.Floor(op.DstX+dw)), int(math.Ceil(op.DstX))
	}
	if dh < 0 {
		y0, y1 = int(math.Floor(op.DstY+dh)), int(math.Ceil(op.DstY))
	}

	srcW := src.Dx()
	srcH := src.Dy()

	for dy := y0; dy < y1; dy++ {
		if dy < 0 || dy >= height {
			continue
		}
		for dx := x0; dx < x1; dx++ {
			if dx < 0 || dx >= width {
				continue
			}
			if !clipContainsPixel(dx, dy, clip, clipActive) {
				continue
			}

			// Map destination pixel center back to source. The source rect is
			// in absolute image coordinates; subtracting bounds.Min happens
			// through At() which also uses absolute coordinates, so non-zero
			// Bounds are handled without an extra origin shift.
			fx := (float64(dx)+0.5-op.DstX)/dw*float64(srcW) - 0.5
			fy := (float64(dy)+0.5-op.DstY)/dh*float64(srcH) - 0.5
			sx := src.Min.X + int(math.Round(fx))
			sy := src.Min.Y + int(math.Round(fy))
			if sx < src.Min.X {
				sx = src.Min.X
			}
			if sy < src.Min.Y {
				sy = src.Min.Y
			}
			if sx >= src.Max.X {
				sx = src.Max.X - 1
			}
			if sy >= src.Max.Y {
				sy = src.Max.Y - 1
			}

			// color.Color.RGBA returns premultiplied 16-bit channels.
			cr, cg, cb, ca := op.Image.At(sx, sy).RGBA()
			//nolint:gosec // >>8 bounds the value to [0,255].
			pr, pg, pb, pa := uint8(cr>>8), uint8(cg>>8), uint8(cb>>8), uint8(ca>>8)

			offset := dy*stride + dx*4
			// Full coverage; opacity handles source alpha modulation.
			composeRunPixel(pixels, offset, pr, pg, pb, pa, 255, opacity, op.Blend)
		}
	}
	return nil
}

// mul255 multiplies two 8-bit values and rounds the result back to 8-bit.
// It is used for coverage and premultiplied source-over arithmetic.
func mul255(a, b uint16) uint16 {
	return (a*b + 127) / 255
}

// renderStroke strokes a path with a solid color.
//
// Note: This is a simplified implementation that converts stroke to fill
// using path expansion. Full stroke support with line caps/joins is
// planned for a future release.
func (r *SoftwareRenderer) renderStroke(pixels []byte, width, height, stride int,
	path *pathBuilder, c color.Color, strokeWidth float64,
	mode BlendMode, opacity float64, clip clipRect, clipActive bool,
) {
	// For now, convert stroke to a filled path by expanding
	// This is a simplified approach - full stroke support requires
	// proper path offsetting with caps and joins

	if strokeWidth <= 0 {
		return
	}

	// Create expanded path
	expandedPath := r.expandStroke(path, strokeWidth)
	if expandedPath == nil || expandedPath.IsEmpty() {
		return
	}

	// Render as filled path
	r.renderFill(pixels, width, height, stride, expandedPath, c,
		raster.FillRuleNonZero, mode, opacity, clip, clipActive)
}

// expandStroke creates a filled path from a stroked path.
// This is a simplified implementation that creates rectangles for each line segment.
func (r *SoftwareRenderer) expandStroke(path *pathBuilder, width float64) *pathBuilder {
	if len(path.verbs) == 0 || len(path.points) < 2 {
		return nil
	}

	halfWidth := width / 2.0
	expanded := &pathBuilder{
		verbs:  make([]raster.PathVerb, 0, len(path.verbs)*8),
		points: make([]float32, 0, len(path.points)*8),
	}

	var curX, curY float32
	var startX, startY float32
	pointIdx := 0

	for _, verb := range path.verbs {
		switch verb {
		case raster.MoveTo:
			curX = path.points[pointIdx]
			curY = path.points[pointIdx+1]
			startX = curX
			startY = curY
			pointIdx += 2

		case raster.LineTo:
			x := path.points[pointIdx]
			y := path.points[pointIdx+1]
			r.addLineStroke(expanded, curX, curY, x, y, float32(halfWidth))
			curX = x
			curY = y
			pointIdx += 2

		case raster.QuadTo:
			// Simplify: treat as line to endpoint
			x := path.points[pointIdx+2]
			y := path.points[pointIdx+3]
			r.addLineStroke(expanded, curX, curY, x, y, float32(halfWidth))
			curX = x
			curY = y
			pointIdx += 4

		case raster.CubicTo:
			// Simplify: treat as line to endpoint
			x := path.points[pointIdx+4]
			y := path.points[pointIdx+5]
			r.addLineStroke(expanded, curX, curY, x, y, float32(halfWidth))
			curX = x
			curY = y
			pointIdx += 6

		case raster.Close:
			if curX != startX || curY != startY {
				r.addLineStroke(expanded, curX, curY, startX, startY, float32(halfWidth))
			}
			curX = startX
			curY = startY
		}
	}

	return expanded
}

// addLineStroke adds a stroked line segment as a rectangle.
func (r *SoftwareRenderer) addLineStroke(path *pathBuilder, x0, y0, x1, y1, halfWidth float32) {
	// Calculate direction vector
	dx := x1 - x0
	dy := y1 - y0

	// Length of segment
	length := sqrt32(dx*dx + dy*dy)
	if length < 1e-6 {
		return // Degenerate segment
	}

	// Perpendicular vector (normalized and scaled by half width)
	px := -dy / length * halfWidth
	py := dx / length * halfWidth

	// Four corners of the stroke rectangle
	// CCW winding for correct fill
	path.verbs = append(path.verbs,
		raster.MoveTo,
		raster.LineTo,
		raster.LineTo,
		raster.LineTo,
		raster.Close,
	)
	path.points = append(path.points,
		x0+px, y0+py, // Top-left
		x1+px, y1+py, // Top-right
		x1-px, y1-py, // Bottom-right
		x0-px, y0-py, // Bottom-left
	)
}

// sqrt32 is a helper for float32 square root.
func sqrt32(x float32) float32 {
	// Use Newton-Raphson iteration
	if x <= 0 {
		return 0
	}
	z := x / 2
	for i := 0; i < 10; i++ {
		z = (z + x/z) / 2
	}
	return z
}

// Ensure SoftwareRenderer implements Renderer and CapableRenderer.
var (
	_ Renderer        = (*SoftwareRenderer)(nil)
	_ CapableRenderer = (*SoftwareRenderer)(nil)
)

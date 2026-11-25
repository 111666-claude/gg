package gg

import (
	intImage "github.com/gogpu/gg/internal/image"
)

// Layer represents a drawing layer with blend mode and opacity.
// Layers allow isolating drawing operations and compositing them with
// different blend modes and opacity values, similar to layers in Photoshop
// or SVG group opacity.
type Layer struct {
	pixmap    *Pixmap
	blendMode BlendMode
	opacity   float64
	mask      *Mask // optional alpha mask, applied on PopLayer (nil = no mask)
}

// layerStack manages the layer hierarchy for the context.
type layerStack struct {
	layers []*Layer
	pool   *intImage.Pool
}

// newLayerStack creates a new layer stack with a pool for memory reuse.
func newLayerStack() *layerStack {
	return &layerStack{
		layers: make([]*Layer, 0, 4),
		pool:   intImage.NewPool(8),
	}
}

// PushLayer creates a new layer and makes it the active drawing target.
// All subsequent drawing operations will render to this layer until PopLayer is called.
//
// The layer will be composited onto the parent layer/canvas when PopLayer is called,
// using the specified blend mode and opacity.
//
// Parameters:
//   - blendMode: How to composite this layer onto the parent (e.g., BlendMultiply, BlendScreen)
//   - opacity: Layer opacity in range [0.0, 1.0] where 0 is fully transparent and 1 is fully opaque
//
// Example:
//
//	dc.PushLayer(gg.BlendMultiply, 0.5)
//	dc.SetRGB(1, 0, 0)
//	dc.DrawCircle(100, 100, 50)
//	dc.Fill()
//	dc.PopLayer() // Composite circle onto canvas with multiply blend at 50% opacity
func (c *Context) PushLayer(blendMode BlendMode, opacity float64) {
	// Clamp opacity to valid range
	if opacity < 0 {
		opacity = 0
	}
	if opacity > 1 {
		opacity = 1
	}

	// Initialize layer stack if needed
	if c.layerStack == nil {
		c.layerStack = newLayerStack()
	}

	// Save base pixmap on first push
	if len(c.layerStack.layers) == 0 && c.basePixmap == nil {
		c.basePixmap = c.pixmap
	}

	// Create the layer at the physical resolution used by the renderer. The
	// context dimensions are logical when deviceScale != 1, but drawing paths
	// are transformed into device coordinates before rasterization.
	layerPixmap := NewPixmap(c.PixelWidth(), c.PixelHeight())
	layerPixmap.Clear(Transparent)

	// Create layer
	layer := &Layer{
		pixmap:    layerPixmap,
		blendMode: blendMode,
		opacity:   opacity,
	}

	// Save current pixmap and switch to layer pixmap
	c.layerStack.layers = append(c.layerStack.layers, layer)
	c.pixmap = layerPixmap
}

// PopLayer composites the current layer onto the parent layer/canvas.
// Uses the blend mode and opacity specified in the corresponding PushLayer call.
//
// The layer is composited using the specified blend mode and opacity.
// After compositing, the layer's memory is returned to the pool for reuse.
//
// If there are no layers to pop, this function does nothing.
//
// Example:
//
//	dc.PushLayer(gg.BlendScreen, 1.0)
//	// ... draw operations ...
//	dc.PopLayer() // Composite layer onto parent
func (c *Context) PopLayer() {
	if c.layerStack == nil || len(c.layerStack.layers) == 0 {
		return
	}

	// Pop the current layer
	layers := c.layerStack.layers
	layer := layers[len(layers)-1]
	c.layerStack.layers = layers[:len(layers)-1]

	// Get parent pixmap (either previous layer or base)
	var parentPixmap *Pixmap
	if len(c.layerStack.layers) > 0 {
		parentPixmap = c.layerStack.layers[len(c.layerStack.layers)-1].pixmap
	} else {
		// Restore base pixmap
		parentPixmap = c.basePixmap
		c.basePixmap = nil
	}

	// Apply mask to layer content before compositing (PushMaskLayer).
	if layer.mask != nil {
		c.applyMaskToPixmap(layer.pixmap, layer.mask)
	}

	// Composite layer onto parent
	c.compositeLayer(layer, parentPixmap)

	// Restore parent pixmap as current drawing target
	c.pixmap = parentPixmap
}

// PushMaskLayer creates an isolated layer with an associated alpha mask.
// All subsequent drawing operations render to this layer normally (without masking).
// When PopLayer is called, the ENTIRE layer is masked by the mask and then
// composited onto the parent using source-over blending with full opacity.
//
// This produces different results from SetMask: PushMaskLayer masks the
// composited group, while SetMask masks each shape individually.
//
// Matches Vello push_mask_layer() semantics (research §4).
//
// Example:
//
//	mask := gg.NewMaskFromAlpha(maskImage)
//	dc.PushMaskLayer(mask)
//	dc.DrawCircle(100, 100, 50)
//	dc.Fill()
//	dc.DrawRect(80, 80, 40, 40)
//	dc.Fill()
//	dc.PopLayer() // entire layer content masked, then composited
func (c *Context) PushMaskLayer(mask *Mask) {
	// Clamp: nil mask means no masking (equivalent to PushLayer).
	if mask == nil {
		c.PushLayer(BlendNormal, 1.0)
		return
	}

	// Initialize layer stack if needed.
	if c.layerStack == nil {
		c.layerStack = newLayerStack()
	}

	// Save base pixmap on first push.
	if len(c.layerStack.layers) == 0 && c.basePixmap == nil {
		c.basePixmap = c.pixmap
	}

	// Create the layer at the physical resolution used by the renderer. The
	// context dimensions are logical when deviceScale != 1, but drawing paths
	// are transformed into device coordinates before rasterization.
	layerPixmap := NewPixmap(c.PixelWidth(), c.PixelHeight())
	layerPixmap.Clear(Transparent)

	// Create layer with mask.
	layer := &Layer{
		pixmap:    layerPixmap,
		blendMode: BlendNormal,
		opacity:   1.0,
		mask:      mask,
	}

	// Switch to layer pixmap.
	c.layerStack.layers = append(c.layerStack.layers, layer)
	c.pixmap = layerPixmap
}

// applyMaskToPixmap applies a DestinationIn mask to a pixmap's pixel data.
// For each pixel: all channels are scaled by mask.At(x,y) / 255.
func (c *Context) applyMaskToPixmap(pm *Pixmap, mask *Mask) {
	applyMaskToPixmapData(pm, mask)
}

// SetBlendMode sets the blend mode for subsequent fill and stroke operations.
// The blend mode controls how source pixels are composited onto the destination.
//
// All 29 blend modes from the W3C Compositing and Blending specification are
// supported: 14 Porter-Duff operators, 11 separable modes (Multiply, Screen,
// Overlay, etc.), and 4 non-separable HSL modes (Hue, Saturation, Color, Luminosity).
//
// The default mode is BlendNormal (source-over alpha compositing).
// For SourceOver (the default), rendering uses the existing optimized float64
// inline path with zero additional overhead.
//
// Example:
//
//	dc.SetBlendMode(gg.BlendMultiply)
//	dc.SetRGB(1, 0, 0)
//	dc.DrawRectangle(0, 0, 100, 100)
//	dc.Fill()
//	dc.SetBlendMode(gg.BlendNormal) // restore default
func (c *Context) SetBlendMode(mode BlendMode) {
	c.paint.blendMode = mapPublicBlendMode(mode)
}

// GetBlendMode returns the current blend mode for fill and stroke operations.
func (c *Context) GetBlendMode() BlendMode {
	return BlendMode(c.paint.blendMode)
}

// compositeLayer composites a layer onto a parent pixmap using the layer's
// blend mode and opacity.
//
// Compositing runs in premultiplied alpha space (the storage format of both
// pixmaps). All BlendMode values are supported through the shared blend table,
// not only the four legacy modes understood by internal/image.DrawImage.
func (c *Context) compositeLayer(layer *Layer, parent *Pixmap) {
	if layer == nil || layer.pixmap == nil || parent == nil {
		return
	}
	compositePixmapLayer(parent, layer.pixmap,
		mapPublicBlendMode(layer.blendMode), layer.opacity)
}

// compositePixmapLayer blends src onto dst in place using an internal paint
// blend mode. opacity in [0,1] scales the (already premultiplied) source
// channels. Only the overlap of the two pixmaps is touched, so differently
// sized layers can never write out of bounds.
func compositePixmapLayer(dst, src *Pixmap, mode paintBlendMode, opacity float64) {
	if dst == nil || src == nil {
		return
	}
	switch {
	case opacity != opacity: // NaN: corrupt value must not erase content.
		opacity = 1
	case opacity < 0:
		opacity = 0
	case opacity > 1:
		opacity = 1
	}
	if opacity == 0 {
		return
	}

	fn := getBlendFunc(mode)
	alphaScale := floatToByte(opacity)

	w := dst.Width()
	if sw := src.Width(); sw < w {
		w = sw
	}
	h := dst.Height()
	if sh := src.Height(); sh < h {
		h = sh
	}
	if w <= 0 || h <= 0 {
		return
	}

	dstData := dst.Data()
	srcData := src.Data()
	dstStride := dst.Width() * 4
	srcStride := src.Width() * 4

	// Source-over at full opacity is the common fast path.
	if mode == blendModeSourceOver && opacity == 1 {
		for y := 0; y < h; y++ {
			dstRow := y * dstStride
			srcRow := y * srcStride
			for x := 0; x < w; x++ {
				di := dstRow + x*4
				si := srcRow + x*4
				sa := srcData[si+3]
				if sa == 0 {
					continue
				}
				if sa == 255 {
					dstData[di] = srcData[si]
					dstData[di+1] = srcData[si+1]
					dstData[di+2] = srcData[si+2]
					dstData[di+3] = 255
					continue
				}
				inv := 255 - sa
				dstData[di] = addDiv255PD(srcData[si], mulDiv255PD(dstData[di], inv))
				dstData[di+1] = addDiv255PD(srcData[si+1], mulDiv255PD(dstData[di+1], inv))
				dstData[di+2] = addDiv255PD(srcData[si+2], mulDiv255PD(dstData[di+2], inv))
				dstData[di+3] = addDiv255PD(sa, mulDiv255PD(dstData[di+3], inv))
			}
		}
		return
	}

	for y := 0; y < h; y++ {
		dstRow := y * dstStride
		srcRow := y * srcStride
		for x := 0; x < w; x++ {
			di := dstRow + x*4
			si := srcRow + x*4
			sr := mulDiv255PD(srcData[si], alphaScale)
			sg := mulDiv255PD(srcData[si+1], alphaScale)
			sb := mulDiv255PD(srcData[si+2], alphaScale)
			sa := mulDiv255PD(srcData[si+3], alphaScale)

			or, og, ob, oa := fn(sr, sg, sb, sa,
				dstData[di], dstData[di+1], dstData[di+2], dstData[di+3])
			dstData[di] = or
			dstData[di+1] = og
			dstData[di+2] = ob
			dstData[di+3] = oa
		}
	}
}

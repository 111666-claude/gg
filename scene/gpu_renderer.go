package scene

import (
	"image"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/text"
)

// GPUSceneRenderer provides GPU-accelerated scene rendering by decoding
// scene commands into gg.Context draw calls. The gg.Context handles
// GPU/CPU dispatch automatically via its accelerator system.
//
// This follows the Vello pattern: scene encoding is stateless, and
// rendering decodes the encoding atomically into draw calls. The GPU
// accelerator receives shapes via FillShape/FillPath and flushes them
// in a single render pass.
//
// For scenes with simple fill/stroke operations (the common case in UI
// rendering), this provides a direct GPU path that avoids the tile-based
// CPU decomposition in scene.Renderer.
//
// Usage:
//
//	gpuR := scene.NewGPUSceneRenderer(dc)
//	err := gpuR.RenderScene(myScene)
type GPUSceneRenderer struct {
	dc *gg.Context
}

// NewGPUSceneRenderer creates a GPU scene renderer that renders through
// the given gg.Context. The context's GPU accelerator (if registered)
// will handle shape rendering; CPU fallback is automatic.
func NewGPUSceneRenderer(dc *gg.Context) *GPUSceneRenderer {
	return &GPUSceneRenderer{dc: dc}
}

// RenderScene decodes scene commands and renders them through the gg.Context.
// The decoder walks the binary encoding tag-by-tag, building paths and
// dispatching fill/stroke calls that route through the GPU accelerator.
//
// Transform handling: scene transforms are applied via dc.SetTransform()
// (direct matrix replacement) instead of dc.Push()/Pop(). This avoids
// corrupting the clip stack. Push/Pop is reserved for clip and layer
// boundaries only, ensuring that TagTransform inside a clip region does
// not accidentally pop the clip's saved state.
//
// Reusable sub-scene instances (TagInstance) are traversed recursively with
// their transform/opacity/blend/clip applied as a group; the child encoding
// is never copied into the host.
//
// Returns nil if the scene is empty.
func (r *GPUSceneRenderer) RenderScene(scene *Scene) error {
	if scene == nil {
		return nil
	}
	if err := scene.ValidateInstances(); err != nil {
		return err
	}

	enc := scene.Encoding()
	if enc == nil || len(enc.Tags()) == 0 {
		return nil
	}

	dc := r.dc

	// Save the initial matrix so we can compose scene transforms correctly
	// and restore the original state after rendering.
	// Scene transforms use dc.SetTransform() (direct replacement) instead of
	// dc.Push()/Pop() to avoid interfering with clip Push/Pop nesting.
	baseMatrix := dc.GetTransform()

	frame := &gpuFrame{
		r:          r,
		dc:         dc,
		rootScene:  scene,
		ownerStack: []*Scene{scene},
		baseMatrix: baseMatrix,
		path:       gg.NewPath(),
	}
	if err := frame.renderEncoding(enc, nil, baseMatrix, 0); err != nil {
		return err
	}

	// Restore the original matrix. Unlike the previous Push/Pop approach,
	// SetTransform is a direct replacement so we restore explicitly.
	dc.SetTransform(baseMatrix)

	return nil
}

// gpuFrame carries per-RenderScene state across recursive instance playback.
type gpuFrame struct {
	r          *GPUSceneRenderer
	dc         *gg.Context
	rootScene  *Scene
	ownerStack []*Scene
	baseMatrix gg.Matrix
	path       *gg.Path
}

// renderEncoding decodes enc through the context. When in is non-nil, enc is
// an instanced child encoding and baseMatrix is the host's matrix at the
// instance site; the instance group is isolated via PushLayer when it needs
// opacity or non-default blending.
//
//nolint:gocyclo,cyclop,funlen,gocognit // tag dispatch across all scene command types
func (f *gpuFrame) renderEncoding(enc *Encoding, in *Instance, baseMatrix gg.Matrix, depth int) error {
	dec := NewDecoder(enc)
	if dec == nil {
		return nil
	}

	dc := f.dc
	path := f.path

	// Instance group isolation: render the child into a layer when its
	// opacity or blend mode differs from the default source-over/opaque.
	layerPushed := false
	// currentMatrix is the full context transform for the next draw. For an
	// instanced child, baseMatrix already includes the instance placement
	// (playInstance pre-composes host*instance), so it is used directly as
	// the seed — child encodings commonly omit TagTransform entirely.
	currentMatrix := baseMatrix
	if in != nil {
		if in.Opacity < 1 || (in.Blend != BlendNormal && in.Blend != BlendSourceOver) {
			dc.PushLayer(gg.BlendMode(in.Blend), float64(in.Opacity))
			layerPushed = true
		}
	}
	dc.SetTransform(currentMatrix)

	for dec.Next() {
		switch dec.Tag() {
		case TagTransform:
			a := dec.Transform()
			sceneMatrix := gg.Matrix{
				A: float64(a.A), B: float64(a.B), C: float64(a.C),
				D: float64(a.D), E: float64(a.E), F: float64(a.F),
			}
			if in != nil {
				// baseMatrix already contains host*instance; only compose
				// the child-local transform on top.
				currentMatrix = baseMatrix.Multiply(sceneMatrix)
			} else {
				currentMatrix = baseMatrix.Multiply(sceneMatrix)
			}
			dc.SetTransform(currentMatrix)

		case TagBeginPath:
			path.Clear()

		case TagMoveTo:
			x, y := dec.MoveTo()
			path.MoveTo(float64(x), float64(y))

		case TagLineTo:
			x, y := dec.LineTo()
			path.LineTo(float64(x), float64(y))

		case TagQuadTo:
			cx, cy, x, y := dec.QuadTo()
			path.QuadraticTo(float64(cx), float64(cy), float64(x), float64(y))

		case TagCubicTo:
			c1x, c1y, c2x, c2y, x, y := dec.CubicTo()
			path.CubicTo(float64(c1x), float64(c1y), float64(c2x), float64(c2y), float64(x), float64(y))

		case TagClosePath:
			path.Close()

		case TagEndPath:
			// Path building complete; fill/stroke tag follows.

		case TagFill:
			brush, style := dec.Fill()
			applySceneBrush(dc, brush)
			if style == FillEvenOdd {
				dc.SetFillRule(gg.FillRuleEvenOdd)
			} else {
				dc.SetFillRule(gg.FillRuleNonZero)
			}
			_ = dc.FillPath(path)
			path.Clear()

		case TagFillRoundRect:
			brush, style, rect, rx, ry := dec.FillRoundRect()
			applySceneBrush(dc, brush)
			if style == FillEvenOdd {
				dc.SetFillRule(gg.FillRuleEvenOdd)
			} else {
				dc.SetFillRule(gg.FillRuleNonZero)
			}
			radius := float64(rx)
			if ry > rx {
				radius = float64(ry)
			}
			dc.DrawRoundedRectangle(
				float64(rect.MinX), float64(rect.MinY),
				float64(rect.MaxX-rect.MinX), float64(rect.MaxY-rect.MinY),
				radius,
			)
			_ = dc.Fill()

		case TagStroke:
			brush, style := dec.Stroke()
			applySceneBrush(dc, brush)
			if style != nil {
				if style.Width > 0 {
					dc.SetLineWidth(float64(style.Width))
				}
				dc.SetLineCap(gg.LineCap(style.Cap))
				dc.SetLineJoin(gg.LineJoin(style.Join))
				if style.MiterLimit > 0 {
					dc.SetMiterLimit(float64(style.MiterLimit))
				}
			}
			_ = dc.StrokePath(path)
			path.Clear()

		case TagPushLayer:
			blend, alpha := dec.PushLayer()
			dc.PushLayer(gg.BlendMode(blend), float64(alpha))

		case TagPopLayer:
			dc.PopLayer()

		case TagBeginClip:
			// Push state before clip so EndClip can restore the previous clip
			// level AND the transform that was active at clip time.
			dc.Push()
			// Detect rectangular clips → hardware scissor (ClipRect).
			shape := gg.DetectShape(path)
			if shape.Kind == gg.ShapeRect {
				x := shape.CenterX - shape.Width/2
				y := shape.CenterY - shape.Height/2
				dc.ClipRect(x, y, shape.Width, shape.Height)
			} else {
				dc.DrawPath(path)
				dc.Clip()
				dc.ClearPath()
			}
			path.Clear()

		case TagEndClip:
			dc.Pop()

		case TagSetAntiAlias:
			aa := dec.AntiAlias()
			dc.SetAntiAlias(aa)

		case TagText:
			run, glyphs, str, brush := dec.Text()
			fontScene := f.rootScene
			if in != nil && in.Child != nil {
				fontScene = in.Child
			}
			f.resolveText(fontScene, run, glyphs, str, brush)

		case TagImage:
			imageIndex, imgTransform := dec.Image()
			imageScene := f.rootScene
			if in != nil && in.Child != nil {
				imageScene = in.Child
			}
			f.resolveImage(imageScene, imageIndex, imgTransform)

		case TagInstance:
			id, instTransform, opacity, blend := dec.Instance()
			if err := f.playInstance(id, instTransform, opacity, blend, baseMatrix, depth); err != nil {
				return err
			}

		default:
			// Unknown tags are skipped by the decoder advancing tagIdx.
		}
	}

	if layerPushed {
		dc.PopLayer()
	}
	return nil
}

// playInstance resolves one nested instance against the nearest enclosing
// scene and recursively renders it. The encoded placement is carried on a
// local copy so the shared registered Instance is never mutated.
func (f *gpuFrame) playInstance(id uint32, transform Affine, opacity float32, blend BlendMode, hostMatrix gg.Matrix, depth int) error {
	if depth >= MaxInstanceDepth {
		return ErrInstanceDepthExceeded
	}
	owner := f.ownerStack[len(f.ownerStack)-1]
	registered, ok := owner.InstanceByID(id)
	if !ok || registered == nil || registered.Child == nil {
		return ErrInstanceNotFound
	}
	if registered.Child.version != registered.childVersion {
		return ErrInstanceMoved
	}
	if isSingular(transform) {
		return ErrSingularMatrix
	}
	in := *registered
	in.Transform = transform
	in.Opacity = opacity
	in.Blend = blend

	m := transform
	instanceMatrix := gg.Matrix{
		A: float64(m.A), B: float64(m.B), C: float64(m.C),
		D: float64(m.D), E: float64(m.E), F: float64(m.F),
	}
	childBase := hostMatrix.Multiply(instanceMatrix)

	childEnc := in.Child.Encoding()
	if childEnc == nil || childEnc.IsEmpty() {
		return nil
	}
	f.ownerStack = append(f.ownerStack, in.Child)
	err := f.renderEncoding(childEnc, &in, childBase, depth+1)
	f.ownerStack = f.ownerStack[:len(f.ownerStack)-1]
	return err
}

// resolveText resolves a TagText glyph run. Prefers DrawShapedGlyphs (no re-shaping)
// with DrawString as fallback. Produces hinted, atlas-batched, DPI-aware text.
func (f *gpuFrame) resolveText(sourceScene *Scene, run GlyphRunData, glyphs []GlyphEntry, str string, brush Brush) {
	if run.GlyphCount == 0 {
		return
	}

	source := sourceScene.fontRegistry[run.FontSourceID]
	if source == nil {
		return
	}

	dc := f.dc
	applySceneBrush(dc, brush)

	prevFace := dc.Font()
	face := source.Face(float64(run.FontSize))
	dc.SetFont(face)

	if len(glyphs) > 0 {
		isCJK := run.Flags&TextFlagCJK != 0
		shaped := make([]text.ShapedGlyph, len(glyphs))
		for i, g := range glyphs {
			shaped[i] = text.ShapedGlyph{GID: g.GlyphID, X: float64(g.X), Y: float64(g.Y), IsCJK: isCJK}
		}
		dc.DrawShapedGlyphs(shaped, face, float64(run.OriginX), float64(run.OriginY))
	} else if str != "" {
		dc.DrawString(str, float64(run.OriginX), float64(run.OriginY))
	}

	dc.SetFont(prevFace)
}

// resolveImage renders a scene image through dc.DrawImageEx, honoring
// the full affine transform (scale + translation). The transform encodes
// scaleX in A, scaleY in E, translateX in C, translateY in F.
//
// When the image belongs to an instanced child scene, its child-local index
// is resolved through the child's registry and mapped onto the host image
// slice by pointer, so no image pixels are copied at attachment time.
func (f *gpuFrame) resolveImage(sourceScene *Scene, imageIndex uint32, transform Affine) {
	var img *Image
	if int(imageIndex) < len(sourceScene.imageRegistry) {
		img = sourceScene.imageRegistry[imageIndex]
	}
	if sourceScene != f.rootScene && img != nil {
		hostIdx, ok := f.lookupHostImage(img)
		if !ok || int(hostIdx) >= len(f.rootScene.imageRegistry) {
			return
		}
		img = f.rootScene.imageRegistry[hostIdx]
	}
	if img == nil || img.Width <= 0 || img.Height <= 0 || len(img.Data) < img.Width*img.Height*4 {
		return
	}

	rgba := &image.RGBA{
		Pix:    img.Data,
		Stride: img.Width * 4,
		Rect:   image.Rect(0, 0, img.Width, img.Height),
	}
	buf := gg.ImageBufFromImage(rgba)

	dstW := float64(transform.A) * float64(img.Width)
	dstH := float64(transform.E) * float64(img.Height)
	if dstW < 0 {
		dstW = -dstW
	}
	if dstH < 0 {
		dstH = -dstH
	}

	f.dc.DrawImageEx(buf, gg.DrawImageOptions{
		X:         float64(transform.C),
		Y:         float64(transform.F),
		DstWidth:  dstW,
		DstHeight: dstH,
	})
}

// lookupHostImage maps a child-scene image pointer to its index in the host
// scene's merged registry.
func (f *gpuFrame) lookupHostImage(img *Image) (uint32, bool) {
	for i, registered := range f.rootScene.imageRegistry {
		if registered == img {
			return uint32(i), true //nolint:gosec // bounded by slice length
		}
	}
	return 0, false
}

// applySceneBrush sets the gg.Context color from a scene.Brush.
func applySceneBrush(dc *gg.Context, brush Brush) {
	if brush.Kind == BrushSolid {
		dc.SetRGBA(brush.Color.R, brush.Color.G, brush.Color.B, brush.Color.A)
	}
}

// CanUseGPU returns true if a GPU accelerator is registered and can
// render directly. This is used by scene.Renderer to auto-select
// the GPU path when available.
func CanUseGPU() bool {
	return gg.AcceleratorCanRenderDirect()
}

package raster

import (
	"image"
	"math"
	"reflect"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/recording"
	"golang.org/x/image/draw"
)

// DrawImage composites the src sub-rectangle of img into dst.
//
// It honors, in order:
//
//   - src in the image's own Bounds coordinate space (non-zero Bounds.Min and
//     negative/out-of-bounds coordinates are handled without reading outside
//     the pixel buffer);
//   - dst signed extents (a reversed rectangle mirrors the frame);
//   - opts.Transform when present (rotation/scale affine recorded with the
//     command), otherwise the backend's current transform.
//
// Fully-outside, empty and non-finite rectangles are deterministic no-ops.
// A nil image (including a typed nil) is a no-op. The caller's pixels are
// never mutated and the gg context matrix/clip state is restored.
func (b *Backend) DrawImage(img image.Image, src, dst recording.Rect, opts recording.ImageOptions) {
	if b == nil || b.ctx == nil {
		return
	}
	if drawImageIsNil(img) {
		return
	}

	bounds := img.Bounds()
	if bounds.Empty() {
		return
	}
	if !finiteRect(src) || !finiteRect(dst) {
		return
	}

	bw := float64(bounds.Dx())
	bh := float64(bounds.Dy())
	minX := float64(bounds.Min.X)
	minY := float64(bounds.Min.Y)

	// The zero SrcRect means "entire image Bounds".
	if src == (recording.Rect{}) {
		src = recording.Rect{MinX: minX, MinY: minY, MaxX: minX + bw, MaxY: minY + bh}
	}

	srcW := src.MaxX - src.MinX
	srcH := src.MaxY - src.MinY
	if srcW <= 0 || srcH <= 0 {
		// Explicit empty/reversed source addresses no pixels.
		return
	}

	dstW := dst.MaxX - dst.MinX
	dstH := dst.MaxY - dst.MinY
	if dstW == 0 || dstH == 0 {
		return
	}

	// Intersect the source with the true image bounds in absolute coordinates.
	clipMinX := math.Max(src.MinX, minX)
	clipMinY := math.Max(src.MinY, minY)
	clipMaxX := math.Min(src.MaxX, minX+bw)
	clipMaxY := math.Min(src.MaxY, minY+bh)
	if clipMaxX <= clipMinX || clipMaxY <= clipMinY {
		return
	}

	// Integer crop in zero-based buffer-local coordinates, rounded outward so
	// fractional edges still sample boundary texels.
	crop := image.Rect(
		int(math.Floor(clipMinX-minX)),
		int(math.Floor(clipMinY-minY)),
		int(math.Ceil(clipMaxX-minX)),
		int(math.Ceil(clipMaxY-minY)),
	).Intersect(image.Rect(0, 0, int(bw), int(bh)))
	if crop.Empty() {
		return
	}

	buf := gg.ImageBufFromImage(normalizeZeroOrigin(img))
	if buf == nil {
		return
	}

	interpolation := gg.InterpBilinear
	if opts.Interpolation == recording.InterpolationNearest {
		interpolation = gg.InterpNearest
	}
	// Alpha is passed through unchanged: DrawImageEx applies the same
	// zero-means-opaque default as the software reference path.
	alpha := opts.Alpha

	drawOpts := gg.DrawImageOptions{
		X:             0,
		Y:             0,
		DstWidth:      float64(crop.Dx()),
		DstHeight:     float64(crop.Dy()),
		SrcRect:       &crop,
		Interpolation: interpolation,
		Opacity:       alpha,
		BlendMode:     gg.BlendNormal,
	}

	b.ctx.Push()
	defer b.ctx.Pop()

	if opts.HasTransform && matrixFinite(opts.Transform) {
		// Explicit affine maps absolute image pixels -> world coordinates.
		// Reset to the persistent device scale so a stale CTM from a previous
		// fill cannot compose twice, then install:
		//   G = F * T(Bounds.Min) * T(crop.Min)
		// which maps draw-rect-local q in [0,crop.Dx] to world.
		f := recordingMatrixToGG(opts.Transform)
		b.applyDeviceScale()
		b.ctx.Transform(f)
		b.ctx.Translate(minX, minY)
		b.ctx.Translate(float64(crop.Min.X), float64(crop.Min.Y))
		b.ctx.DrawImageEx(buf, drawOpts)
		return
	}

	// Rect-only mapping: derive the axis-aligned source->user transform and
	// let DrawImageEx compose it with the live CTM (which carries any
	// SetTransform rotation/scale and the device scale).
	scaleX := dstW / srcW
	scaleY := dstH / srcH
	// Destination sub-rectangle corresponding to the (possibly partial) crop.
	x := dst.MinX + (float64(crop.Min.X)+minX-src.MinX)*scaleX
	y := dst.MinY + (float64(crop.Min.Y)+minY-src.MinY)*scaleY
	drawOpts.X = x
	drawOpts.Y = y
	drawOpts.DstWidth = float64(crop.Dx()) * scaleX
	drawOpts.DstHeight = float64(crop.Dy()) * scaleY
	b.ctx.DrawImageEx(buf, drawOpts)
}

// isFinite reports whether f is neither NaN nor ±Inf.
func isFinite(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}

// normalizeZeroOrigin returns an image whose Bounds.Min is (0,0) and whose
// pixels are safe to index from that origin. Images already zero-origin are
// returned unchanged. This corrects image.SubImage views (which share the
// parent's Pix allocation and offset) and non-zero Bounds images before
// conversion to an ImageBuf.
func normalizeZeroOrigin(img image.Image) image.Image {
	b := img.Bounds()
	if b.Min == (image.Point{}) {
		return img
	}
	dst := image.NewRGBA(image.Rectangle{Max: b.Size()})
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
	return dst
}

// drawImageIsNil reports whether img is nil, including a typed-nil pointer.
func drawImageIsNil(img image.Image) bool {
	if img == nil {
		return true
	}
	v := reflect.ValueOf(img)
	switch v.Kind() { //nolint:exhaustive // only nilable kinds matter
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return v.IsNil()
	default:
		return false
	}
}

func finiteRect(r recording.Rect) bool {
	return isFinite(r.MinX) && isFinite(r.MinY) &&
		isFinite(r.MaxX) && isFinite(r.MaxY)
}

func matrixFinite(m recording.Matrix) bool {
	return isFinite(m.A) && isFinite(m.B) && isFinite(m.C) &&
		isFinite(m.D) && isFinite(m.E) && isFinite(m.F)
}

func recordingMatrixToGG(m recording.Matrix) gg.Matrix {
	return gg.Matrix{A: m.A, B: m.B, C: m.C, D: m.D, E: m.E, F: m.F}
}

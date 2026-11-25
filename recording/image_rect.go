package recording

import (
	"image"
	"reflect"
)

// boundsRect converts an image.Bounds rectangle into a recording Rect in the
// image's own coordinate space, preserving a potentially non-zero Min.
func boundsRect(b image.Rectangle) Rect {
	return Rect{
		MinX: float64(b.Min.X),
		MinY: float64(b.Min.Y),
		MaxX: float64(b.Max.X),
		MaxY: float64(b.Max.Y),
	}
}

// isNilImageLike reports whether img is nil, including a typed-nil pointer
// stored in an image.Image interface.
func isNilImageLike(img image.Image) bool {
	if img == nil {
		return true
	}
	v := reflect.ValueOf(img)
	switch v.Kind() { //nolint:exhaustive // only nilable kinds can be nil
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return v.IsNil()
	default:
		return false
	}
}

// recordImageCommand pools img and appends a DrawImageCommand.
//
// placement maps absolute image-pixel coordinates (image.Image.Bounds space)
// to user-space destination coordinates. The active recorder transform is then
// composed in, producing Options.Transform (absolute image pixels -> world)
// and the world-space axis-aligned destination bounding box.
func (r *Recorder) recordImageCommand(img image.Image, src Rect, placement Matrix, userOpts ...ImageOptions) {
	imageRef := r.resources.AddImage(img)

	forward := r.transform.Multiply(placement)

	// World-space axis-aligned bounding box of the mapped source quad. All
	// four corners matter under rotation: transforming only the diagonal can
	// under-grow the box.
	cx := [4]float64{src.MinX, src.MaxX, src.MaxX, src.MinX}
	cy := [4]float64{src.MinY, src.MinY, src.MaxY, src.MaxY}
	x0, y0 := forward.TransformPoint(cx[0], cy[0])
	minX, maxX := x0, x0
	minY, maxY := y0, y0
	for i := 1; i < 4; i++ {
		x, y := forward.TransformPoint(cx[i], cy[i])
		if x < minX {
			minX = x
		}
		if x > maxX {
			maxX = x
		}
		if y < minY {
			minY = y
		}
		if y > maxY {
			maxY = y
		}
	}

	opts := DefaultImageOptions()
	if len(userOpts) > 0 {
		opts = userOpts[0]
	}
	opts.Transform = forward
	opts.HasTransform = true

	r.commands = append(r.commands, DrawImageCommand{
		Image:   imageRef,
		SrcRect: src,
		DstRect: Rect{MinX: minX, MinY: minY, MaxX: maxX, MaxY: maxY},
		Options: opts,
	})
}

// DrawImageRect draws the src sub-rectangle of img into dst.
//
// src is expressed in the image's own coordinate space (the same space as
// img.Bounds, so it may carry the Bounds.Min origin and negative coordinates).
// dst is in current user coordinates and may use a reversed Min/Max to mirror
// the frame. The active recorder transform (translation, rotation, scale) is
// recorded with the command.
//
// For compatibility with callers that represent rectangles either as
// image.Rectangle (integer source pixels) or recording.Rect (float geometry),
// both src and dst accept either type (or *image.Rectangle). An optional
// trailing ImageOptions is accepted as well. Any unsupported rectangle type is
// ignored and no command is recorded.
func (r *Recorder) DrawImageRect(img image.Image, src, dst any, opts ...ImageOptions) {
	if isNilImageLike(img) {
		return
	}
	sr, okSrc := rectFromAny(src)
	dr, okDst := rectFromAny(dst)
	if !okSrc || !okDst {
		return
	}

	sw := sr.MaxX - sr.MinX
	sh := sr.MaxY - sr.MinY
	dw := dr.MaxX - dr.MinX
	dh := dr.MaxY - dr.MinY

	var placement Matrix
	if sw != 0 && sh != 0 {
		sx := dw / sw
		sy := dh / sh
		placement = Matrix{
			A: sx,
			E: sy,
			C: dr.MinX - sr.MinX*sx,
			F: dr.MinY - sr.MinY*sy,
		}
	} else {
		// Degenerate source: keep the matrix finite. Backends draw nothing.
		placement = Matrix{C: dr.MinX, F: dr.MinY}
	}

	r.recordImageCommand(img, sr, placement, opts...)
}

// rectFromAny converts an image.Rectangle, *image.Rectangle or recording.Rect
// into a Rect.
func rectFromAny(v any) (Rect, bool) {
	switch t := v.(type) {
	case Rect:
		return t, true
	case *Rect:
		if t == nil {
			return Rect{}, false
		}
		return *t, true
	case image.Rectangle:
		return boundsRect(t), true
	case *image.Rectangle:
		if t == nil {
			return Rect{}, false
		}
		return boundsRect(*t), true
	default:
		return Rect{}, false
	}
}

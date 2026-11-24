package recording

import (
	"fmt"
	"image"
	"math"
)

// MaxImageDimension is the largest image width/height accepted by the
// recording preflight. Backends with smaller caps apply their own limits; this
// guards against integer overflow and pathological allocations before replay.
const MaxImageDimension = 32767

// Validate performs a preflight check of every recorded command without
// replaying it. It verifies that all resource references resolve and that
// DrawImage commands carry legal rectangles and finite geometry.
//
// The first problem encountered is returned with command-index context so
// callers can reject a bad recording before it reaches a backend. A nil or
// empty recording validates successfully.
func (r *Recording) Validate() error {
	if r == nil {
		return nil
	}
	for i, cmd := range r.commands {
		switch c := cmd.(type) {
		case DrawImageCommand:
			if err := r.validateDrawImage(i, c); err != nil {
				return err
			}
		case FillPathCommand:
			if !c.Path.IsValid() || r.resources.GetPath(c.Path) == nil {
				return fmt.Errorf("recording: command %d FillPath references missing path %d", i, c.Path)
			}
			if !c.Brush.IsValid() || r.resources.GetBrush(c.Brush) == nil {
				return fmt.Errorf("recording: command %d FillPath references missing brush %d", i, c.Brush)
			}
		case StrokePathCommand:
			if !c.Path.IsValid() || r.resources.GetPath(c.Path) == nil {
				return fmt.Errorf("recording: command %d StrokePath references missing path %d", i, c.Path)
			}
			if !c.Brush.IsValid() || r.resources.GetBrush(c.Brush) == nil {
				return fmt.Errorf("recording: command %d StrokePath references missing brush %d", i, c.Brush)
			}
		case FillRectCommand:
			if !c.Brush.IsValid() || r.resources.GetBrush(c.Brush) == nil {
				return fmt.Errorf("recording: command %d FillRect references missing brush %d", i, c.Brush)
			}
		case StrokeRectCommand:
			if !c.Brush.IsValid() || r.resources.GetBrush(c.Brush) == nil {
				return fmt.Errorf("recording: command %d StrokeRect references missing brush %d", i, c.Brush)
			}
		}
	}
	return nil
}

// imageRectFromRecording converts a recording Rect to an image.Rectangle,
// normalizing reversed edges so the stdlib intersection logic works.
func imageRectFromRecording(rect Rect) image.Rectangle {
	x0 := int(rect.MinX)
	y0 := int(rect.MinY)
	x1 := int(rect.MaxX)
	y1 := int(rect.MaxY)
	return image.Rect(x0, y0, x1, y1)
}

// validateDrawImage checks a single DrawImageCommand for replay safety.
func (r *Recording) validateDrawImage(index int, c DrawImageCommand) error {
	// Missing resource: an unresolvable image reference must not reach a
	// backend as a nil image, where behavior differs per implementation.
	if !c.Image.IsValid() || r.resources.GetImage(c.Image) == nil {
		return fmt.Errorf("recording: command %d DrawImage references missing image %d", index, c.Image)
	}
	img := r.resources.GetImage(c.Image)
	bounds := img.Bounds()
	if bounds.Empty() {
		return fmt.Errorf("recording: command %d DrawImage source image has empty bounds", index)
	}
	if bounds.Dx() > MaxImageDimension || bounds.Dy() > MaxImageDimension {
		return fmt.Errorf("recording: command %d DrawImage image exceeds %dpx limit", index, MaxImageDimension)
	}

	// A non-empty SrcRect must be finite and intersect the image bounds.
	if !c.SrcRect.IsEmpty() {
		if !rectIsFinite(c.SrcRect) {
			return fmt.Errorf("recording: command %d DrawImage has non-finite source rect", index)
		}
		src := imageRectFromRecording(c.SrcRect).Intersect(bounds)
		if src.Empty() {
			return fmt.Errorf("recording: command %d DrawImage source rect lies outside image bounds", index)
		}
	}

	if !rectIsFinite(c.DstRect) {
		return fmt.Errorf("recording: command %d DrawImage has non-finite destination rect", index)
	}
	if c.DstRect.IsEmpty() {
		return fmt.Errorf("recording: command %d DrawImage has empty destination rect", index)
	}

	if math.IsNaN(c.Options.Alpha) || math.IsInf(c.Options.Alpha, 0) {
		return fmt.Errorf("recording: command %d DrawImage has non-finite alpha", index)
	}
	return nil
}

// rectIsFinite reports whether all four rectangle components are finite.
func rectIsFinite(rect Rect) bool {
	for _, v := range [4]float64{rect.MinX, rect.MinY, rect.MaxX, rect.MaxY} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

// Copyright 2026 The gogpu Authors
// SPDX-License-Identifier: MIT

package surface

import (
	"image"

	"github.com/gogpu/gg/internal/raster"
)

// CreateSubSurface creates a new surface backed by a rectangular region of
// this surface.
//
// The returned sub-surface shares the root pixel storage: it keeps the root
// image's stride and stores an origin rather than copying pixels. Drawing to
// the sub-surface (Fill, Stroke, DrawImage, Clear) lands directly in the
// shared pixels and is immediately visible through the parent and through
// any overlapping sub-surfaces. All coordinates on the sub-surface are
// relative to its local bounds, which start at (0, 0).
//
// Sub-surfaces can be nested: the bounds are relative to the parent
// sub-surface and must lie within it.
//
// Bounds are validated against the parent's local bounds. A zero-area region
// returns ErrEmptySubSurface; negative dimensions or an out-of-range region
// return ErrInvalidSubSurfaceBounds. A closed surface (or a surface whose
// root is closed) returns ErrSurfaceClosed.
func (s *ImageSurface) CreateSubSurface(bounds image.Rectangle) (Surface, error) {
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()

	if !s.aliveLocked() {
		return nil, ErrSurfaceClosed
	}

	local := bounds
	// Reject reversed (negative-size) rectangles on the raw arguments:
	// image.Rect canonicalizes by swapping corners, so checking Dx() alone
	// would silently turn Rect(10,0,5,10) into a valid 5x10 region.
	if local.Min.X > local.Max.X || local.Min.Y > local.Max.Y {
		return nil, ErrInvalidSubSurfaceBounds
	}
	if local.Min.X < 0 || local.Min.Y < 0 ||
		local.Max.X > s.width || local.Max.Y > s.height {
		return nil, ErrInvalidSubSurfaceBounds
	}
	if local.Empty() {
		return nil, ErrEmptySubSurface
	}

	return s.newChildLocked(local), nil
}

// newChildLocked constructs a sub-surface for local bounds relative to this
// surface. The caller must hold shared.mu.
func (s *ImageSurface) newChildLocked(local image.Rectangle) *ImageSurface {
	// Local coordinates -> root pixel coordinates.
	rootRect := image.Rect(
		s.origin.X+local.Min.X,
		s.origin.Y+local.Min.Y,
		s.origin.X+local.Max.X,
		s.origin.Y+local.Max.Y,
	)

	subImg, _ := s.shared.rootImg.SubImage(rootRect).(*image.RGBA)
	width := local.Dx()
	height := local.Dy()

	return &ImageSurface{
		width:       width,
		height:      height,
		img:         subImg,
		origin:      rootRect.Min,
		bounds:      rootRect,
		isRoot:      false,
		parent:      s,
		shared:      s.shared,
		filler:      raster.NewAnalyticFiller(width, height),
		edgeBuilder: raster.NewEdgeBuilder(2),
	}
}

// SetClip replaces the active clip with the region described by path.
//
// The path is expressed in local coordinates and is rasterized into a
// per-pixel coverage mask; subsequent Fill, Stroke and DrawImage operations
// are restricted to the clipped region with correct anti-aliased edges.
// A nil or empty path disables clipping.
func (s *ImageSurface) SetClip(path *Path) {
	if !s.begin() {
		return
	}
	defer s.shared.mu.Unlock()

	if path == nil || path.IsEmpty() {
		s.clipMask = nil
		return
	}
	s.clipMask = s.rasterizeClipMaskLocked(path)
}

// ClearClip removes the clipping region.
func (s *ImageSurface) ClearClip() {
	if !s.begin() {
		return
	}
	defer s.shared.mu.Unlock()

	s.clipMask = nil
}

// PushClip saves the current clip state so it can be restored with PopClip.
func (s *ImageSurface) PushClip() {
	if !s.begin() {
		return
	}
	defer s.shared.mu.Unlock()

	saved := s.clipMask
	if saved != nil {
		saved = append([]uint8(nil), saved...)
	}
	s.clipStack = append(s.clipStack, saved)
}

// PopClip restores the clip state saved by the matching PushClip.
// Popping an empty stack disables clipping.
func (s *ImageSurface) PopClip() {
	if !s.begin() {
		return
	}
	defer s.shared.mu.Unlock()

	if len(s.clipStack) == 0 {
		s.clipMask = nil
		return
	}
	last := len(s.clipStack) - 1
	s.clipMask = s.clipStack[last]
	s.clipStack = s.clipStack[:last]
}

// rasterizeClipMaskLocked renders path into a coverage mask of the surface's
// local size. The caller must hold shared.mu.
func (s *ImageSurface) rasterizeClipMaskLocked(path *Path) []uint8 {
	mask := make([]uint8, s.width*s.height)

	s.edgeBuilder.Reset()
	s.edgeBuilder.SetFlattenCurves(false)
	s.edgeBuilder.BuildFromPath(path, raster.IdentityTransform{})
	if s.edgeBuilder.IsEmpty() {
		return mask
	}

	s.filler.Reset()
	s.filler.Fill(s.edgeBuilder, raster.FillRuleNonZero,
		func(y int, runs *raster.AlphaRuns) {
			if y < 0 || y >= s.height {
				return
			}
			for x, alpha := range runs.Iter() {
				if x < 0 || x >= s.width || alpha == 0 {
					continue
				}
				mask[y*s.width+x] = alpha
			}
		})
	return mask
}

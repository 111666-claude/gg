// Copyright 2026 The gogpu Authors
// SPDX-License-Identifier: MIT

package surface

import (
	"math"

	"github.com/gogpu/gg/internal/raster"
)

// clipCoordLimit bounds path coordinates before clip rasterization.
//
// The analytic rasterizer works in 16.16 fixed-point, so coordinates must
// stay well below 1<<15 pixels to avoid int32 overflow. Any finite
// coordinate beyond this limit is clamped; because the limit is far outside
// every reachable surface, clamping does not change coverage on the surface.
const clipCoordLimit float32 = 32000

// clipState is one level of the clip stack.
type clipState struct {
	// mask holds per-pixel clip coverage (width*height, row-major).
	// nil means "no clip" — every pixel is fully visible.
	mask []uint8

	// empty is true when mask is non-nil but entirely zero,
	// i.e. nothing can be drawn. Lets drawing operations early-out.
	empty bool
}

// SetClip intersects the current clip region with the given path using the
// non-zero winding rule. Successive SetClip calls intersect, so the visible
// region can only shrink until ClearClip or PopClip.
//
// The path is evaluated in the current (identity) coordinate system.
// A nil or empty path clips everything (subsequent drawing is a no-op),
// matching cairo semantics. Paths containing non-finite coordinates (NaN,
// ±Inf) are treated as empty. Finite coordinates far outside the surface
// are clamped to a safe rasterization range without changing on-surface
// coverage.
func (s *ImageSurface) SetClip(path *Path) {
	s.SetClipWithRule(path, FillRuleNonZero)
}

// SetClipWithRule is SetClip with an explicit fill rule. Use FillRuleEvenOdd
// to clip to shapes with holes (e.g. rings).
func (s *ImageSurface) SetClipWithRule(path *Path, rule FillRule) {
	if s.closed {
		return
	}

	var rasterRule raster.FillRule
	if rule == FillRuleEvenOdd {
		rasterRule = raster.FillRuleEvenOdd
	} else {
		rasterRule = raster.FillRuleNonZero
	}

	sanitized, valid := sanitizeClipPath(path)
	if !valid {
		// nil/empty path or non-finite coordinates: defined result is
		// an empty clip — nothing will be drawn.
		s.setClipEmpty()
		return
	}

	s.ensureClipRasterizer()

	// Rasterize the path coverage into the reusable scratch buffer.
	scratch := s.clipScratch
	for i := range scratch {
		scratch[i] = 0
	}

	s.clipEdgeBuilder.Reset()
	s.clipEdgeBuilder.SetFlattenCurves(false)
	s.clipEdgeBuilder.BuildFromPath(sanitized, raster.IdentityTransform{})

	if !s.clipEdgeBuilder.IsEmpty() {
		s.clipFiller.Reset()
		s.clipFiller.Fill(s.clipEdgeBuilder, rasterRule, func(y int, runs *raster.AlphaRuns) {
			if y < 0 || y >= s.height {
				return
			}
			runs.CopyTo(scratch[y*s.width : (y+1)*s.width])
		})
	}

	s.intersectClip(scratch)
}

// PushClip saves the current clip state. The saved state is restored by the
// matching PopClip. This is the clip equivalent of Save/Restore.
//
// Each pushed level with an active clip holds its own width*height mask
// copy, so stack depth directly bounds clip memory: depth * width * height
// bytes plus one shared scratch buffer.
func (s *ImageSurface) PushClip() {
	if s.closed {
		return
	}

	state := clipState{empty: s.clip.empty}
	if s.clip.mask != nil {
		state.mask = make([]uint8, len(s.clip.mask))
		copy(state.mask, s.clip.mask)
	}
	s.clipStack = append(s.clipStack, state)
}

// PopClip restores the clip state saved by the most recent PushClip.
// Popping with an empty stack is a safe no-op.
func (s *ImageSurface) PopClip() {
	if s.closed || len(s.clipStack) == 0 {
		return
	}

	top := s.clipStack[len(s.clipStack)-1]
	s.clipStack = s.clipStack[:len(s.clipStack)-1]

	if top.mask == nil {
		s.clip = clipState{}
		return
	}

	// Reuse the restored buffer as the active mask.
	s.clip = top
}

// ClearClip removes the clipping region from the current state, restoring
// full-surface drawing. States saved with PushClip are unaffected.
func (s *ImageSurface) ClearClip() {
	if s.closed {
		return
	}
	s.clip = clipState{}
}

// HasClip reports whether a clipping region is currently active.
func (s *ImageSurface) HasClip() bool {
	return s.clip.mask != nil
}

// setClipEmpty installs an all-zero clip mask.
func (s *ImageSurface) setClipEmpty() {
	s.ensureClipMaskBuffers()
	for i := range s.clipMaskBuf {
		s.clipMaskBuf[i] = 0
	}
	s.clip = clipState{mask: s.clipMaskBuf, empty: true}
}

// intersectClip combines the current clip with new coverage (scratch).
// Intersection is coverage multiplication: out = cur * new / 255.
func (s *ImageSurface) intersectClip(scratch []uint8) {
	s.ensureClipMaskBuffers()

	if s.clip.mask == nil {
		// No active clip: the new coverage becomes the clip.
		copy(s.clipMaskBuf, scratch)
	} else {
		// Intersect: out = cur * new / 255. Works both in place (active
		// mask is the shared buffer) and when the active mask is a
		// restored stack buffer (read from it, write the shared buffer).
		cur := s.clip.mask
		for i := range s.clipMaskBuf {
			s.clipMaskBuf[i] = uint8(uint16(cur[i]) * uint16(scratch[i]) / 255) //nolint:gosec // product/255 fits uint8
		}
	}

	// Track the all-zero case so drawing can early-out.
	empty := true
	for _, v := range s.clipMaskBuf {
		if v != 0 {
			empty = false
			break
		}
	}
	s.clip = clipState{mask: s.clipMaskBuf, empty: empty}
}

// ensureClipRasterizer lazily allocates the filler/edge-builder used for
// clip rasterization so they don't clobber the drawing pipeline's state.
//
// The edge builder gets a clip rect around the surface (expanded by a
// margin): out-of-range edges are clipped with winding-preserving sentinel
// verticals before fixed-point conversion (RAST-010), which keeps huge
// coordinates from overflowing the rasterizer without changing on-surface
// coverage.
func (s *ImageSurface) ensureClipRasterizer() {
	if s.clipFiller == nil {
		s.clipFiller = raster.NewAnalyticFiller(s.width, s.height)
		s.clipEdgeBuilder = raster.NewEdgeBuilder(2)
		const margin = 1024
		r := raster.Rect{
			MinX: -margin,
			MinY: -margin,
			MaxX: float32(s.width) + margin,
			MaxY: float32(s.height) + margin,
		}
		s.clipEdgeBuilder.SetClipRect(&r)
	}
	s.ensureClipMaskBuffers()
}

// ensureClipMaskBuffers lazily allocates the mask and scratch buffers.
// Both are exactly width*height bytes, allocated once and reused.
func (s *ImageSurface) ensureClipMaskBuffers() {
	n := s.width * s.height
	if len(s.clipMaskBuf) != n {
		s.clipMaskBuf = make([]uint8, n)
	}
	if len(s.clipScratch) != n {
		s.clipScratch = make([]uint8, n)
	}
}

// sanitizeClipPath validates a clip path and clamps out-of-range coordinates.
//
// Returns (nil, false) when the path must produce an empty clip: nil path,
// empty path, or any non-finite coordinate. Returns (path, true) unchanged
// when all coordinates are already in the safe rasterization range, or a
// clamped clone otherwise (the caller's path is never mutated).
func sanitizeClipPath(path *Path) (*Path, bool) {
	if path == nil || path.IsEmpty() {
		return nil, false
	}

	needsClamp := false
	for _, v := range path.points {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return nil, false
		}
		if v > clipCoordLimit || v < -clipCoordLimit {
			needsClamp = true
		}
	}

	if !needsClamp {
		return path, true
	}

	clamped := path.Clone()
	for i, v := range clamped.points {
		if v > clipCoordLimit {
			clamped.points[i] = clipCoordLimit
		} else if v < -clipCoordLimit {
			clamped.points[i] = -clipCoordLimit
		}
	}
	return clamped, true
}

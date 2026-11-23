// Copyright 2026 The gogpu Authors
// SPDX-License-Identifier: MIT

// Package surface provides a unified surface abstraction for 2D rendering.
//
// Surface is the core rendering target abstraction that decouples drawing
// operations from their implementation. This allows the same drawing code
// to work with:
//
//   - CPU-based software rendering (ImageSurface)
//   - GPU-accelerated rendering (GPUSurface)
//   - Third-party backends via registry
//
// # Architecture
//
// The surface package follows the Cairo/Skia pattern where surfaces are
// rendering targets independent of the drawing context. This separation
// enables:
//
//   - Backend switching without code changes
//   - Testing with mock surfaces
//   - Third-party backend integration (RFC #46)
//
// # Surface Types
//
//   - ImageSurface: CPU-based rendering to *image.RGBA using core.AnalyticFiller
//   - GPUSurface: GPU-accelerated rendering wrapper (requires external device)
//
// # Registry (RFC #46)
//
// Third-party backends can register surfaces via the registry:
//
//	surface.Register("vulkan", func(w, h int, opts Options) (Surface, error) {
//	    return NewVulkanSurface(w, h)
//	})
//
//	// Later:
//	s, err := surface.NewSurfaceByName("vulkan", 800, 600, nil)
//
// # Usage
//
// Basic usage with ImageSurface:
//
//	// Create a CPU-based surface
//	s := surface.NewImageSurface(800, 600)
//	defer s.Close()
//
//	// Clear with white background
//	s.Clear(color.White)
//
//	// Create a path
//	path := surface.NewPath()
//	path.MoveTo(100, 100)
//	path.LineTo(200, 100)
//	path.LineTo(150, 200)
//	path.Close()
//
//	// Fill with red
//	s.Fill(path, surface.FillStyle{
//	    Color: color.RGBA{255, 0, 0, 255},
//	    Rule:  surface.FillRuleNonZero,
//	})
//
//	// Get the result
//	img := s.Snapshot()
//
// # Sub-surfaces
//
// ImageSurface implements SubSurface: CreateSubSurface returns a surface
// backed by a rectangular region of the parent, sharing the root pixel
// storage (stride and origin) rather than copying. Coordinates on the
// sub-surface are local to its bounds, and every draw lands directly in the
// shared pixels, so it is visible through the parent and overlapping
// siblings. Sub-surfaces can be nested, and SnapshotWith selects between an
// independent copy and a zero-copy read-only view:
//
//	sub, err := s.CreateSubSurface(image.Rect(10, 10, 50, 50))
//	if err != nil { /* invalid or closed */ }
//	sub.Fill(path, surface.FillStyle{Color: color.RGBA{0, 0, 255, 255}})
//	view := sub.(*surface.ImageSurface).SnapshotView() // shared pixels
//
// ImageSurface also implements ClippableSurface for path-based clipping.
//
// # Integration with gg.Context
//
// The surface package is designed to integrate with gg.Context (see INT-001).
// Context will use Surface as its rendering target, allowing backend switching.
//
// # References
//
//   - Cairo: https://cairographics.org/manual/cairo-Image-Surfaces.html
//   - Skia: https://skia.org/docs/user/api/skcanvas_overview/
//   - RFC #46: Third-party backend support
package surface

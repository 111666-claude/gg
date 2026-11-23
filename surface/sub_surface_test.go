// Copyright 2026 The gogpu Authors
// SPDX-License-Identifier: MIT

package surface

import (
	"errors"
	"image"
	"image/color"
	"sync"
	"testing"
)

// mustSub creates a sub-surface and returns the concrete type so the test
// can call sub-surface-only methods directly.
func mustSub(t *testing.T, s *ImageSurface, bounds image.Rectangle) *ImageSurface {
	t.Helper()
	sub, err := s.CreateSubSurface(bounds)
	if err != nil {
		t.Fatalf("CreateSubSurface(%v): %v", bounds, err)
	}
	return sub.(*ImageSurface)
}

// TestSubSurfaceCreationAndBounds checks dimensions, origin, bounds, parent
// linkage and the SubSurface/ClippableSurface capability reports.
func TestSubSurfaceCreationAndBounds(t *testing.T) {
	parent := NewImageSurface(40, 30)
	defer func() { _ = parent.Close() }()

	sub := mustSub(t, parent, image.Rect(10, 5, 30, 20))

	if sub.Width() != 20 || sub.Height() != 15 {
		t.Errorf("sub size = %dx%d, want 20x15", sub.Width(), sub.Height())
	}
	if got := sub.Origin(); got != (image.Point{X: 10, Y: 5}) {
		t.Errorf("Origin = %v, want (10,5)", got)
	}
	if got := sub.Bounds(); got != image.Rect(10, 5, 30, 20) {
		t.Errorf("Bounds = %v, want (10,5)-(30,20)", got)
	}
	if sub.Parent() != parent {
		t.Error("Parent() should return the creating surface")
	}
	if !sub.IsSubSurface() || parent.IsSubSurface() {
		t.Error("IsSubSurface mismatch")
	}
	if parent.Bounds() != image.Rect(0, 0, 40, 30) {
		t.Errorf("parent Bounds = %v, want full rect", parent.Bounds())
	}

	caps := parent.Capabilities()
	if !caps.SupportsSubSurface || !caps.SupportsClipping {
		t.Errorf("capabilities = %+v, want sub-surface and clipping", caps)
	}

	if _, ok := any(parent).(SubSurface); !ok {
		t.Error("ImageSurface must implement SubSurface")
	}
	if _, ok := any(parent).(ClippableSurface); !ok {
		t.Error("ImageSurface must implement ClippableSurface")
	}
}

// TestSubSurfaceSharedStride verifies the child aliases the parent's pixel
// array and keeps its stride.
func TestSubSurfaceSharedStride(t *testing.T) {
	parent := NewImageSurface(16, 16)
	defer func() { _ = parent.Close() }()

	sub := mustSub(t, parent, image.Rect(4, 4, 12, 12))

	if sub.Image().Stride != parent.Image().Stride {
		t.Errorf("child stride = %d, want parent stride %d",
			sub.Image().Stride, parent.Image().Stride)
	}

	// Write through the child's image. SetRGBA uses the image's absolute
	// bounds, whose Min is the child origin in root coordinates.
	rootMin := sub.Image().Bounds().Min
	sub.Image().SetRGBA(rootMin.X, rootMin.Y, color.RGBA{7, 8, 9, 255})
	if got := parent.Image().RGBAAt(4, 4); got != (color.RGBA{7, 8, 9, 255}) {
		t.Errorf("parent pixel at child origin = %v, want (7,8,9,255)", got)
	}

	// The surface's own drawing API uses local (0,0) coordinates.
	sub.Clear(color.RGBA{1, 2, 3, 255})
	if got := parent.Image().RGBAAt(4, 4); got != (color.RGBA{1, 2, 3, 255}) {
		t.Errorf("parent pixel after child Clear = %v, want (1,2,3,255)", got)
	}
}

// TestSubSurfaceFillVisibleOnParent draws on a child using local
// coordinates and checks the shared pixels through the parent.
func TestSubSurfaceFillVisibleOnParent(t *testing.T) {
	parent := NewImageSurface(40, 40)
	defer func() { _ = parent.Close() }()

	parent.Clear(color.RGBA{255, 255, 255, 255})

	sub := mustSub(t, parent, image.Rect(10, 10, 30, 30))

	// Local rect (5,5)-(15,15) maps to parent rect (15,15)-(25,25).
	path := NewPath()
	path.Rectangle(5, 5, 10, 10)
	sub.Fill(path, FillStyle{Color: color.RGBA{255, 0, 0, 255}})

	snap := parent.Snapshot()
	if got := snap.RGBAAt(20, 20); got.R != 255 || got.G != 0 || got.B != 0 {
		t.Errorf("parent center pixel = %v, want red", got)
	}
	// Just outside the mapped region stays white.
	if got := snap.RGBAAt(14, 20); got.R != 255 || got.G != 255 || got.B != 255 {
		t.Errorf("pixel outside child draw = %v, want white", got)
	}

	// The child's own snapshot uses local coordinates.
	childSnap := sub.Snapshot()
	if got := childSnap.RGBAAt(10, 10); got.R != 255 || got.G != 0 {
		t.Errorf("child snapshot center = %v, want red", got)
	}
}

// TestSubSurfaceStrokeAndDrawImage exercises Stroke and DrawImage through
// a sub-surface and confirms they land in shared pixels.
func TestSubSurfaceStrokeAndDrawImage(t *testing.T) {
	parent := NewImageSurface(40, 40)
	defer func() { _ = parent.Close() }()
	parent.Clear(color.White)

	sub := mustSub(t, parent, image.Rect(8, 8, 32, 32))

	line := NewPath()
	line.MoveTo(2, 12)
	line.LineTo(22, 12)
	sub.Stroke(line, StrokeStyle{
		Color: color.RGBA{0, 128, 0, 255},
		Width: 3,
	})

	parentSnap := parent.Snapshot()
	// Local (12,12) maps to parent (20,20).
	if got := parentSnap.RGBAAt(20, 20); got.G < 100 {
		t.Errorf("parent stroke pixel green = %d, want high", got.G)
	}

	src := newTestImageRGBA(4, 4, color.RGBA{0, 0, 255, 255})
	sub.DrawImage(src, Pt(2, 2), nil)
	// Local (3,3) maps to parent (11,11).
	if got := parent.Snapshot().RGBAAt(11, 11); got.B != 255 || got.R != 0 {
		t.Errorf("parent DrawImage pixel = %v, want blue", got)
	}
}

// TestSubSurfaceOverlappingSiblings draws into two overlapping child
// regions and verifies shared pixels, draw ordering and transparency.
func TestSubSurfaceOverlappingSiblings(t *testing.T) {
	parent := NewImageSurface(30, 20)
	defer func() { _ = parent.Close() }()
	parent.Clear(color.Transparent)

	// Two children overlapping on columns 12..19.
	left := mustSub(t, parent, image.Rect(0, 0, 20, 20))
	right := mustSub(t, parent, image.Rect(12, 0, 30, 20))

	red := NewPath()
	red.Rectangle(0, 0, 20, 20)
	left.Fill(red, FillStyle{Color: color.RGBA{255, 0, 0, 255}})

	// Semi-transparent green over the right region.
	green := NewPath()
	green.Rectangle(0, 0, 18, 20)
	right.Fill(green, FillStyle{Color: color.RGBA{0, 255, 0, 128}})

	snap := parent.Snapshot()

	// Left-only area: pure red.
	if got := snap.RGBAAt(5, 10); got.R != 255 || got.G != 0 {
		t.Errorf("left-only pixel = %v, want red", got)
	}
	// Right-only area: blended green over transparent.
	if got := snap.RGBAAt(25, 10); got.G == 0 {
		t.Errorf("right-only pixel = %v, want green", got)
	}
	// Overlap area: both colors composited in the shared pixel.
	overlap := snap.RGBAAt(15, 10)
	if overlap.R == 0 || overlap.G == 0 {
		t.Errorf("overlap pixel = %v, want red+green blend", overlap)
	}
	if overlap.A != 255 {
		t.Errorf("overlap alpha = %d, want 255", overlap.A)
	}

	// The left child sees the right child's draw through the shared storage.
	leftSnap := left.Snapshot()
	if got := leftSnap.RGBAAt(15, 10); got.G == 0 {
		t.Errorf("left child did not observe sibling draw: %v", got)
	}
}

// TestSubSurfaceTransparentCompositing checks source-over blending against
// an existing transparent and opaque background.
func TestSubSurfaceTransparentCompositing(t *testing.T) {
	parent := NewImageSurface(10, 10)
	defer func() { _ = parent.Close() }()

	sub := mustSub(t, parent, image.Rect(0, 0, 10, 10))
	sub.DrawImage(newTestImageRGBA(10, 10, color.RGBA{255, 0, 0, 128}), Pt(0, 0), nil)

	got := parent.Snapshot().RGBAAt(5, 5)
	if got.A != 128 {
		t.Errorf("alpha over transparent = %d, want 128", got.A)
	}
	if got.R != 255 {
		t.Errorf("red over transparent = %d, want 255", got.R)
	}

	// Opaque white underneath, then 50% black on top.
	parent.Clear(color.RGBA{255, 255, 255, 255})
	sub.DrawImage(newTestImageRGBA(10, 10, color.RGBA{0, 0, 0, 128}), Pt(0, 0), nil)
	got = parent.Snapshot().RGBAAt(5, 5)
	if got.A != 255 {
		t.Errorf("alpha over opaque alpha = %d, want 255", got.A)
	}
	// Black at 50% over white lands near 127.
	if got.R > 140 {
		t.Errorf("black 50%% over white = %d, want ~127", got.R)
	}
}

// TestSubSurfaceNested creates a grandchild and verifies coordinate mapping
// through two levels of origin offsets.
func TestSubSurfaceNested(t *testing.T) {
	parent := NewImageSurface(50, 50)
	defer func() { _ = parent.Close() }()
	parent.Clear(color.White)

	child := mustSub(t, parent, image.Rect(10, 10, 40, 40))
	grand := mustSub(t, child, image.Rect(5, 5, 20, 20))

	if got := grand.Origin(); got != (image.Point{X: 15, Y: 15}) {
		t.Errorf("grandchild origin = %v, want (15,15)", got)
	}
	if got := grand.Bounds(); got != image.Rect(15, 15, 30, 30) {
		t.Errorf("grandchild bounds = %v, want (15,15)-(30,30)", got)
	}
	if grand.Parent() != child {
		t.Error("grandchild parent linkage wrong")
	}

	path := NewPath()
	path.Rectangle(0, 0, 5, 5)
	grand.Fill(path, FillStyle{Color: color.RGBA{0, 0, 255, 255}})

	// Grandchild local (2,2) -> parent (17,17).
	if got := parent.Snapshot().RGBAAt(17, 17); got.B != 255 || got.R != 0 {
		t.Errorf("nested fill pixel = %v, want blue", got)
	}
}

// TestSubSurfaceClearIsLocal verifies Clear only repaints the child region.
func TestSubSurfaceClearIsLocal(t *testing.T) {
	parent := NewImageSurface(20, 20)
	defer func() { _ = parent.Close() }()
	parent.Clear(color.RGBA{10, 10, 10, 255})

	sub := mustSub(t, parent, image.Rect(5, 5, 15, 15))
	sub.Clear(color.RGBA{200, 200, 200, 255})

	snap := parent.Snapshot()
	if got := snap.RGBAAt(10, 10); got.R != 200 {
		t.Errorf("inside region = %v, want gray 200", got)
	}
	if got := snap.RGBAAt(2, 2); got.R != 10 {
		t.Errorf("outside region = %v, want gray 10", got)
	}
	// Edge-adjacent pixel outside the region is untouched.
	if got := snap.RGBAAt(15, 10); got.R != 10 {
		t.Errorf("boundary neighbor = %v, want gray 10", got)
	}
}

// TestSubSurfaceClip restricts Fill and DrawImage to a clip path, including
// push/pop and clear.
func TestSubSurfaceClip(t *testing.T) {
	parent := NewImageSurface(30, 30)
	defer func() { _ = parent.Close() }()
	parent.Clear(color.White)

	sub := mustSub(t, parent, image.Rect(5, 5, 25, 25))

	clip := NewPath()
	clip.Rectangle(4, 4, 12, 12)
	sub.SetClip(clip)

	rect := NewPath()
	rect.Rectangle(0, 0, 20, 20)
	sub.Fill(rect, FillStyle{Color: color.RGBA{255, 0, 0, 255}})

	snap := parent.Snapshot()
	// Local (10,10) inside clip -> parent (15,15) red.
	if got := snap.RGBAAt(15, 15); got.R != 255 || got.G != 0 {
		t.Errorf("clipped-inside pixel = %v, want red", got)
	}
	// Local (2,2) outside clip -> parent (7,7) stays white.
	if got := snap.RGBAAt(7, 7); got.R != 255 || got.G != 255 {
		t.Errorf("clipped-outside pixel = %v, want white", got)
	}

	// DrawImage honors the clip too.
	sub.DrawImage(newTestImageRGBA(20, 20, color.RGBA{0, 0, 255, 255}), Pt(0, 0), nil)
	// Local (2,2) is outside the clip: it must stay the white background.
	whiteBG := color.RGBA{255, 255, 255, 255}
	if got := parent.Snapshot().RGBAAt(7, 7); got != whiteBG {
		t.Errorf("DrawImage escaped clip: %v, want %v", got, whiteBG)
	}
	if got := parent.Snapshot().RGBAAt(15, 15); got.B != 255 {
		t.Errorf("DrawImage inside clip missing: %v", got)
	}

	// Push the current clip, clear it, draw everywhere.
	sub.PushClip()
	sub.ClearClip()
	sub.Fill(rect, FillStyle{Color: color.RGBA{0, 255, 0, 255}})
	if got := parent.Snapshot().RGBAAt(7, 7); got.G != 255 {
		t.Errorf("after ClearClip pixel = %v, want green", got)
	}

	// Pop restores the original rect clip.
	sub.PopClip()
	parent.Clear(color.White)
	sub.Fill(rect, FillStyle{Color: color.RGBA{255, 0, 0, 255}})
	if got := parent.Snapshot().RGBAAt(7, 7); got.R != 255 || got.G != 255 {
		t.Errorf("restored clip not enforced: %v", got)
	}
}

// TestSubSurfaceScaledDrawImage checks DstRect scaling with both filters,
// clipped to the child and landing in shared pixels.
func TestSubSurfaceScaledDrawImage(t *testing.T) {
	parent := NewImageSurface(30, 30)
	defer func() { _ = parent.Close() }()
	parent.Clear(color.Transparent)

	sub := mustSub(t, parent, image.Rect(5, 5, 25, 25))

	src := image.NewRGBA(image.Rect(0, 0, 2, 2))
	src.SetRGBA(0, 0, color.RGBA{255, 0, 0, 255})
	src.SetRGBA(1, 0, color.RGBA{0, 255, 0, 255})
	src.SetRGBA(0, 1, color.RGBA{0, 0, 255, 255})
	src.SetRGBA(1, 1, color.RGBA{255, 255, 0, 255})

	dst := image.Rect(0, 0, 10, 10)
	sub.DrawImage(src, Pt(0, 0), &DrawImageOptions{
		DstRect: &dst,
		Alpha:   1.0,
		Filter:  FilterNearest,
	})

	snap := parent.Snapshot()
	// Nearest upscale: local (2,2) region samples source (0,0) red.
	if got := snap.RGBAAt(7, 7); got.R != 255 || got.G != 0 || got.B != 0 {
		t.Errorf("nearest upscale pixel = %v, want red", got)
	}
	// Outside the child, still transparent.
	if got := snap.RGBAAt(2, 2); got.A != 0 {
		t.Errorf("pixel outside child changed: %v", got)
	}

	// Bilinear run must not panic and stays within the destination.
	parent.Clear(color.Transparent)
	sub.DrawImage(src, Pt(0, 0), &DrawImageOptions{
		DstRect: &dst,
		Alpha:   1.0,
		Filter:  FilterBilinear,
	})
	if got := parent.Snapshot().RGBAAt(7, 7); got.A == 0 {
		t.Error("bilinear upscale produced no pixels")
	}

	// A DstRect extending past the child must be clipped, not panic.
	oversized := image.Rect(-5, -5, 30, 30)
	sub.DrawImage(src, Pt(0, 0), &DrawImageOptions{
		DstRect: &oversized,
		Alpha:   1.0,
		Filter:  FilterNearest,
	})
}

// TestSubSurfaceSnapshotCopyAndView distinguishes the copying Snapshot from
// the zero-copy read-only view.
func TestSubSurfaceSnapshotCopyAndView(t *testing.T) {
	parent := NewImageSurface(12, 12)
	defer func() { _ = parent.Close() }()
	parent.Clear(color.RGBA{1, 2, 3, 255})

	sub := mustSub(t, parent, image.Rect(2, 2, 10, 10))

	cp := sub.SnapshotWith(true)
	if cp == nil || cp.Bounds() != image.Rect(0, 0, 8, 8) {
		t.Fatalf("copy snapshot bounds = %v, want 8x8", cp.Bounds())
	}
	if got := cp.RGBAAt(0, 0); got != (color.RGBA{1, 2, 3, 255}) {
		t.Errorf("copy pixel = %v, want (1,2,3,255)", got)
	}
	// Tight packing: the copied image uses its own width as stride.
	if cp.Stride != 8*4 {
		t.Errorf("copy stride = %d, want %d", cp.Stride, 8*4)
	}

	view := sub.SnapshotView()
	if view.Stride != parent.Image().Stride {
		t.Errorf("view stride = %d, want root stride %d", view.Stride, parent.Image().Stride)
	}
	// The view keeps absolute bounds, so its local (0,0) is read at the
	// child origin.
	viewMin := view.Bounds().Min

	// Mutate shared pixels through the parent; the view follows, the copy
	// does not.
	parent.Image().SetRGBA(2, 2, color.RGBA{9, 9, 9, 255})
	if got := view.RGBAAt(viewMin.X, viewMin.Y); got != (color.RGBA{9, 9, 9, 255}) {
		t.Errorf("view did not track shared write: %v", got)
	}
	if got := cp.RGBAAt(0, 0); got != (color.RGBA{1, 2, 3, 255}) {
		t.Errorf("copy was affected by later write: %v", got)
	}

	// Default Snapshot remains a copy.
	defaultSnap := sub.Snapshot()
	parent.Image().SetRGBA(2, 2, color.RGBA{4, 4, 4, 255})
	if got := defaultSnap.RGBAAt(0, 0); got != (color.RGBA{9, 9, 9, 255}) {
		t.Errorf("Snapshot should be an independent copy: %v", got)
	}
}

// TestSubSurfaceCloseDoesNotReleaseParent covers child close, repeated
// close and continued parent/sibling operation.
func TestSubSurfaceCloseDoesNotReleaseParent(t *testing.T) {
	parent := NewImageSurface(20, 20)
	defer func() { _ = parent.Close() }()
	parent.Clear(color.White)

	sub := mustSub(t, parent, image.Rect(0, 0, 10, 10))
	sibling := mustSub(t, parent, image.Rect(10, 0, 20, 10))

	if err := sub.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := sub.Close(); err != nil {
		t.Errorf("repeated Close: %v", err)
	}

	// The parent storage is untouched and still writable.
	path := NewPath()
	path.Rectangle(0, 0, 5, 5)
	parent.Fill(path, FillStyle{Color: color.RGBA{255, 0, 0, 255}})
	if got := parent.Snapshot().RGBAAt(2, 2); got.R != 255 {
		t.Errorf("parent draw after child close failed: %v", got)
	}

	// The sibling still works against the shared storage.
	sp := NewPath()
	sp.Rectangle(0, 0, 5, 5)
	sibling.Fill(sp, FillStyle{Color: color.RGBA{0, 0, 255, 255}})
	if got := parent.Snapshot().RGBAAt(12, 2); got.B != 255 {
		t.Errorf("sibling draw after child close failed: %v", got)
	}

	// Operations on the closed child are safe no-ops.
	sub.Fill(path, FillStyle{Color: color.Black})
	sub.Clear(color.Black)
	if sub.Snapshot() != nil {
		t.Error("Snapshot on closed child should be nil")
	}
	if sub.Image() != nil {
		t.Error("Image on closed child should be nil")
	}

	// Creating a child of a closed surface fails.
	if _, err := sub.CreateSubSurface(image.Rect(0, 0, 2, 2)); !errors.Is(err, ErrSurfaceClosed) {
		t.Errorf("CreateSubSurface on closed child err = %v, want ErrSurfaceClosed", err)
	}
}

// TestSubSurfaceParentClose invalidates every descendant.
func TestSubSurfaceParentClose(t *testing.T) {
	parent := NewImageSurface(20, 20)

	child := mustSub(t, parent, image.Rect(0, 0, 10, 10))
	grand := mustSub(t, child, image.Rect(0, 0, 5, 5))

	if err := parent.Close(); err != nil {
		t.Errorf("parent Close: %v", err)
	}

	for name, sfc := range map[string]Surface{"child": child, "grand": grand} {
		sfc.Clear(color.Black)
		sfc.Fill(NewPath(), FillStyle{Color: color.Black})
		if sfc.Snapshot() != nil {
			t.Errorf("%s Snapshot after root close should be nil", name)
		}
		if err := sfc.Close(); err != nil {
			t.Errorf("%s Close after root close: %v", name, err)
		}
	}

	// Re-creating a region on a closed root fails.
	if _, err := parent.CreateSubSurface(image.Rect(0, 0, 2, 2)); !errors.Is(err, ErrSurfaceClosed) {
		t.Errorf("err = %v, want ErrSurfaceClosed", err)
	}
}

// TestSubSurfaceInvalidBounds exercises every rejection path.
func TestSubSurfaceInvalidBounds(t *testing.T) {
	parent := NewImageSurface(20, 20)
	defer func() { _ = parent.Close() }()

	cases := []struct {
		name   string
		bounds image.Rectangle
		want   error
	}{
		{"empty", image.Rect(5, 5, 5, 10), ErrEmptySubSurface},
		// image.Rect swaps reversed corners, so negative-size inputs must be
		// built with raw struct literals to reach the validation.
		{"negative width", image.Rectangle{Min: image.Pt(10, 0), Max: image.Pt(5, 10)}, ErrInvalidSubSurfaceBounds},
		{"negative height", image.Rectangle{Min: image.Pt(0, 10), Max: image.Pt(10, 5)}, ErrInvalidSubSurfaceBounds},
		{"negative origin", image.Rect(-1, 0, 10, 10), ErrInvalidSubSurfaceBounds},
		{"x overflow", image.Rect(15, 0, 25, 10), ErrInvalidSubSurfaceBounds},
		{"y overflow", image.Rect(0, 15, 10, 25), ErrInvalidSubSurfaceBounds},
		{"zero size", image.Rect(0, 0, 0, 0), ErrEmptySubSurface},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub, err := parent.CreateSubSurface(tc.bounds)
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			if sub != nil {
				t.Error("invalid bounds must not return a surface")
			}
		})
	}

	// Full-bounds region is legal.
	if _, err := parent.CreateSubSurface(image.Rect(0, 0, 20, 20)); err != nil {
		t.Errorf("full-bounds sub-surface: %v", err)
	}
}

// TestSubSurfaceOutOfBoundsDrawIsSafe draws outside local bounds via paths,
// images and offsets; nothing may write past the region.
func TestSubSurfaceOutOfBoundsDrawIsSafe(t *testing.T) {
	parent := NewImageSurface(30, 30)
	defer func() { _ = parent.Close() }()
	parent.Clear(color.White)

	sub := mustSub(t, parent, image.Rect(10, 10, 20, 20))

	// Path extending well beyond the local 10x10 region.
	overflow := NewPath()
	overflow.Rectangle(-20, -20, 60, 60)
	sub.Fill(overflow, FillStyle{Color: color.RGBA{255, 0, 0, 255}})

	snap := parent.Snapshot()
	// Pixels outside the child's root bounds must stay white.
	for _, p := range []image.Point{{5, 5}, {25, 25}, {9, 15}, {20, 9}} {
		if got := snap.RGBAAt(p.X, p.Y); got.R != 255 || got.G != 255 || got.B != 255 {
			t.Errorf("pixel %v = %v, want untouched white", p, got)
		}
	}
	if got := snap.RGBAAt(15, 15); got.R != 255 || got.G != 0 {
		t.Errorf("inside pixel = %v, want red", got)
	}

	// DrawImage placed partially off-region.
	src := newTestImageRGBA(8, 8, color.RGBA{0, 0, 255, 255})
	sub.DrawImage(src, Pt(-4, -4), nil)
	sub.DrawImage(src, Pt(8, 8), nil)
	white := color.RGBA{255, 255, 255, 255}
	if got := parent.Snapshot().RGBAAt(9, 12); got != white {
		t.Errorf("DrawImage wrote one pixel left of the region: %v", got)
	}
	if got := parent.Snapshot().RGBAAt(12, 9); got != white {
		t.Errorf("DrawImage wrote one pixel above the region: %v", got)
	}
	// But the blitted part inside the region must exist.
	if got := parent.Snapshot().RGBAAt(10, 10); got.B != 255 || got.R != 0 {
		t.Errorf("in-region blit missing: %v", got)
	}
}

// TestSubSurfaceConcurrentAccess hammers parent, child and sibling from
// several goroutines; under -race this proves the shared lock prevents
// races and every write stays inside the surface.
func TestSubSurfaceConcurrentAccess(t *testing.T) {
	parent := NewImageSurface(40, 40)
	defer func() { _ = parent.Close() }()

	a := mustSub(t, parent, image.Rect(0, 0, 25, 40))
	b := mustSub(t, parent, image.Rect(20, 0, 40, 40))

	surfaces := []Surface{parent, a, b}
	var wg sync.WaitGroup
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			rect := NewPath()
			rect.Rectangle(0, 0, 20, 20)
			style := FillStyle{Color: color.RGBA{uint8(id * 30), 100, 200, 200}}
			for i := 0; i < 200; i++ {
				sfc := surfaces[i%len(surfaces)]
				sfc.Fill(rect, style)
				sfc.Snapshot()
				if i%7 == 0 {
					sfc.Clear(color.RGBA{255, 255, 255, 255})
				}
				if i%11 == 0 {
					_ = sfc.Flush()
				}
			}
		}(g)
	}
	wg.Wait()

	// Final state must be fully readable and in-range.
	snap := parent.Snapshot()
	if snap.Bounds() != image.Rect(0, 0, 40, 40) {
		t.Fatalf("final snapshot bounds = %v", snap.Bounds())
	}
	_ = a.Close()
	_ = b.Close()
}

// TestSubSurfaceSnapshotAfterClose covers snapshots on closed surfaces,
// including a view requested after the root was closed.
func TestSubSurfaceSnapshotAfterClose(t *testing.T) {
	parent := NewImageSurface(10, 10)
	sub := mustSub(t, parent, image.Rect(0, 0, 5, 5))

	_ = sub.Close()
	if sub.SnapshotWith(true) != nil || sub.SnapshotWith(false) != nil {
		t.Error("snapshots on a closed sub-surface must be nil")
	}

	other := mustSub(t, parent, image.Rect(0, 0, 5, 5))
	_ = parent.Close()
	if other.SnapshotView() != nil {
		t.Error("view after root close must be nil")
	}
}

// TestSubSurfaceCapabilitiesOnChild confirms a child reports the same
// capabilities as the root.
func TestSubSurfaceCapabilitiesOnChild(t *testing.T) {
	parent := NewImageSurface(10, 10)
	defer func() { _ = parent.Close() }()

	sub := mustSub(t, parent, image.Rect(0, 0, 5, 5))
	caps := sub.Capabilities()
	if !caps.SupportsSubSurface || !caps.SupportsClipping || !caps.SupportsAntialias {
		t.Errorf("child caps = %+v", caps)
	}
}

// TestSubSurfaceOnNonZeroMinRoot covers a root surface built from an image
// whose bounds do not start at (0,0): the stride-based indexing must still
// map local coordinates correctly through root and child.
func TestSubSurfaceOnNonZeroMinRoot(t *testing.T) {
	img := image.NewRGBA(image.Rect(100, 100, 110, 110))
	root := NewImageSurfaceFromImage(img)
	defer func() { _ = root.Close() }()

	if root.Origin() != (image.Point{X: 100, Y: 100}) {
		t.Errorf("root origin = %v, want (100,100)", root.Origin())
	}

	root.Clear(color.RGBA{255, 255, 255, 255})
	sub := mustSub(t, root, image.Rect(2, 2, 8, 8))

	if got := sub.Origin(); got != (image.Point{X: 102, Y: 102}) {
		t.Errorf("child origin = %v, want (102,102)", got)
	}

	path := NewPath()
	path.Rectangle(0, 0, 6, 6)
	sub.Fill(path, FillStyle{Color: color.RGBA{255, 0, 0, 255}})

	// Child local (3,3) -> root absolute (105,105).
	if got := img.RGBAAt(105, 105); got.R != 255 || got.G != 0 {
		t.Errorf("child draw through non-zero-min root = %v, want red", got)
	}
	if got := img.RGBAAt(101, 101); got != (color.RGBA{255, 255, 255, 255}) {
		t.Errorf("pixel outside child changed: %v", got)
	}
}

// TestSubSurfaceNestedClip verifies a clip set on a grandchild restricts
// draws to the nested region and never escapes to parent-only pixels.
func TestSubSurfaceNestedClip(t *testing.T) {
	parent := NewImageSurface(40, 40)
	defer func() { _ = parent.Close() }()
	parent.Clear(color.White)

	child := mustSub(t, parent, image.Rect(10, 10, 30, 30))
	grand := mustSub(t, child, image.Rect(5, 5, 15, 15))

	clip := NewPath()
	clip.Rectangle(2, 2, 6, 6)
	grand.SetClip(clip)

	rect := NewPath()
	rect.Rectangle(0, 0, 10, 10)
	grand.Fill(rect, FillStyle{Color: color.RGBA{0, 0, 255, 255}})

	snap := parent.Snapshot()
	// Grandchild local (4,4) -> child (9,9) -> parent (19,19): inside clip.
	if got := snap.RGBAAt(19, 19); got.B != 255 || got.R != 0 {
		t.Errorf("nested clipped-inside pixel = %v, want blue", got)
	}
	// Grandchild local (1,1) -> parent (16,16): outside clip, stays white.
	if got := snap.RGBAAt(16, 16); got != (color.RGBA{255, 255, 255, 255}) {
		t.Errorf("nested clipped-outside pixel = %v, want white", got)
	}
	// Parent-only pixel is never touched.
	if got := snap.RGBAAt(5, 5); got != (color.RGBA{255, 255, 255, 255}) {
		t.Errorf("parent-only pixel changed: %v", got)
	}
}

// TestSubSurfaceParentWriteVisibleToChild confirms writes made through the
// parent after the child was created appear in the child's view.
func TestSubSurfaceParentWriteVisibleToChild(t *testing.T) {
	parent := NewImageSurface(20, 20)
	defer func() { _ = parent.Close() }()
	parent.Clear(color.Transparent)

	sub := mustSub(t, parent, image.Rect(5, 5, 15, 15))

	path := NewPath()
	path.Rectangle(6, 6, 8, 8)
	parent.Fill(path, FillStyle{Color: color.RGBA{10, 20, 30, 255}})

	// Parent (7,7) lies inside the child region -> child local (2,2).
	if got := sub.Snapshot().RGBAAt(2, 2); got != (color.RGBA{10, 20, 30, 255}) {
		t.Errorf("child did not observe parent write: %v", got)
	}
}

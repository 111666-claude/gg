package scene

import (
	"testing"

	"github.com/gogpu/gg"
)

func TestFilterContextDescriptor(t *testing.T) {
	pm := gg.NewPixmap(7, 4)
	ctx := newFilterContext(pm, imagePoint{X: -3, Y: -2})

	if ctx.origin != (imagePoint{X: -3, Y: -2}) {
		t.Errorf("origin = %+v, want (-3,-2)", ctx.origin)
	}
	if ctx.extent != (imagePoint{X: 7, Y: 4}) {
		t.Errorf("extent = %+v, want (7,4)", ctx.extent)
	}
	// RGBA: 4 bytes per pixel, so stride must track the row width.
	if ctx.stride != 7*4 {
		t.Errorf("stride = %d, want %d", ctx.stride, 7*4)
	}
	if ctx.pixmap != pm {
		t.Error("context must keep the borrowed pixmap reference")
	}
}

func TestFilterContextToLocal(t *testing.T) {
	ctx := newFilterContext(gg.NewPixmap(20, 20), imagePoint{X: -5, Y: -4})
	got := ctx.toLocal(Rect{MinX: -5, MinY: -4, MaxX: 10, MaxY: 8})
	want := Rect{MinX: 0, MinY: 0, MaxX: 15, MaxY: 12}
	if got != want {
		t.Errorf("toLocal = %+v, want %+v", got, want)
	}

	// A positive-origin frame translates the other direction.
	ctx2 := newFilterContext(gg.NewPixmap(20, 20), imagePoint{X: 3, Y: 6})
	got2 := ctx2.toLocal(Rect{MinX: 3, MinY: 6, MaxX: 9, MaxY: 10})
	want2 := Rect{MinX: 0, MinY: 0, MaxX: 6, MaxY: 4}
	if got2 != want2 {
		t.Errorf("positive-origin toLocal = %+v, want %+v", got2, want2)
	}
}

func TestFilterContextFillWindow(t *testing.T) {
	// Frame origin (-3,-2), extent 10x8. Source covers global [0,6)x[0,5).
	src := gg.NewPixmap(6, 5)
	fillSource(src)
	window := gg.NewPixmap(10, 8) // starts zeroed, like a freshly pooled buffer
	ctx := newFilterContext(window, imagePoint{X: -3, Y: -2})
	returned := ctx.fillWindow(window, src)
	if returned != window {
		t.Fatal("fillWindow must return the same pooled pixmap")
	}

	// A global source pixel (0,0) maps to local (3,2) in the window.
	want := src.GetPixel(0, 0)
	if got := window.GetPixel(3, 2); !colorApproxEqual(got, want, 0.001) {
		t.Errorf("mapped pixel local(3,2) = %+v, want %+v", got, want)
	}
	// Local (0,0) is global (-3,-2), outside src: stays transparent.
	if got := window.GetPixel(0, 0); got.A != 0 {
		t.Errorf("out-of-reach local(0,0) = %+v, want transparent", got)
	}
	// Bottom-right corner beyond src is also untouched.
	if got := window.GetPixel(9, 7); got.A != 0 {
		t.Errorf("out-of-reach local(9,7) = %+v, want transparent", got)
	}
}

func TestClampToRange(t *testing.T) {
	cases := []struct {
		v, lo, hi, want int
	}{
		{5, 0, 10, 5},
		{-3, 0, 10, 0},
		{11, 0, 10, 10},
		{0, 0, 0, 0},
	}
	for _, c := range cases {
		if got := clampToRange(c.v, c.lo, c.hi); got != c.want {
			t.Errorf("clampToRange(%d,%d,%d) = %d, want %d", c.v, c.lo, c.hi, got, c.want)
		}
	}
}

package scene

import (
	"math"
	"math/rand"
	"sync"
	"testing"

	"github.com/gogpu/gg"
)

// --- construction & metadata ---

func TestDropShadowConstructors(t *testing.T) {
	col := gg.RGBA2(0.1, 0.2, 0.3, 0.4)

	f := NewDropShadowFilter(2, 3, 4, col)
	if f.OffsetX != 2 || f.OffsetY != 3 || f.BlurRadius != 4 || f.Intensity != 1 || f.Color != col {
		t.Fatalf("NewDropShadowFilter = %+v", f)
	}
	if f.Type() != FilterDropShadow {
		t.Fatalf("Type() = %v, want FilterDropShadow", f.Type())
	}

	fi := NewDropShadowFilterIntensity(2, 3, 4, 2.5, col)
	if fi.Intensity != 2.5 {
		t.Fatalf("Intensity = %v, want 2.5", fi.Intensity)
	}

	simple := NewSimpleDropShadow(1, 1, 2)
	if simple.Color != gg.RGBA2(0, 0, 0, 0.5) || simple.Intensity != 1 {
		t.Fatalf("NewSimpleDropShadow = %+v", simple)
	}
}

func TestDropShadowEqualsAndHash(t *testing.T) {
	a := NewDropShadowFilter(2, 3, 4, gg.RGBA2(0, 0, 0, 0.5))
	b := NewDropShadowFilter(2, 3, 4, gg.RGBA2(0, 0, 0, 0.5))
	if !a.Equals(b) || a.Hash() != b.Hash() {
		t.Fatal("identical filters should be equal with same hash")
	}

	variants := []*DropShadowFilter{
		NewDropShadowFilter(2.1, 3, 4, a.Color),
		NewDropShadowFilter(2, 3.1, 4, a.Color),
		NewDropShadowFilter(2, 3, 4.1, a.Color),
		NewDropShadowFilterIntensity(2, 3, 4, 0.9, a.Color),
		NewDropShadowFilter(2, 3, 4, gg.RGBA2(0, 0, 0, 0.6)),
	}
	seen := map[uint64]bool{a.Hash(): true}
	for i, v := range variants {
		if a.Equals(v) {
			t.Errorf("variant %d must not equal base", i)
		}
		if seen[v.Hash()] {
			t.Errorf("variant %d produced a colliding hash", i)
		}
		seen[v.Hash()] = true
	}

	var nilFilter *DropShadowFilter
	if nilFilter.Equals(a) || a.Equals(nilFilter) {
		t.Error("nil comparisons must be false")
	}
	if !nilFilter.Equals(nil) {
		t.Error("two nil filters should be equal")
	}
}

// --- ExpandBounds ---

func TestDropShadowExpandBounds(t *testing.T) {
	base := Rect{MinX: 10, MinY: 20, MaxX: 110, MaxY: 120}

	tests := []struct {
		name               string
		offsetX, offsetY   float64
		blur               float64
		wantMinX, wantMinY float32
		wantMaxX, wantMaxY float32
	}{
		// blurReach=15. left=ceil(15-10)=5, right=ceil(15+10)=25
		{"positive offset, blur 5", 10, 10, 5, 5, 15, 135, 145},
		// left=ceil(15+10)=25, right=ceil(15-10)=5
		{"negative offset, blur 5", -10, -10, 5, -15, -5, 115, 125},
		// no blur: left=ceil(-5)=-5, right=5
		{"zero blur positive offset", 5, 5, 0, 15, 25, 115, 125},
		{"zero blur negative offset", -5, -5, 0, 5, 15, 105, 115},
		// left=ceil(6+7)=13, top=ceil(6-9)=-3, right=ceil(6-7)=-1,
		// bottom=ceil(6+9)=15
		{"mixed signs", -7, 9, 2, -3, 23, 109, 135},
		// no blur, half-pixel offsets: ceil(-2.5)=-2, ceil(2.5)=3
		{"fractional offset expands pixel-tight", 2.5, -2.5, 0, 12, 17, 113, 118},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewDropShadowFilter(tt.offsetX, tt.offsetY, tt.blur, gg.Black)
			got := f.ExpandBounds(base)
			if got.MinX != tt.wantMinX || got.MinY != tt.wantMinY ||
				got.MaxX != tt.wantMaxX || got.MaxY != tt.wantMaxY {
				t.Errorf("ExpandBounds = %+v, want min(%v,%v) max(%v,%v)",
					got, tt.wantMinX, tt.wantMinY, tt.wantMaxX, tt.wantMaxY)
			}
		})
	}
}

func TestDropShadowExpandBoundsDegenerate(t *testing.T) {
	f := NewDropShadowFilter(5, 5, 5, gg.Black)

	empty := EmptyRect()
	if got := f.ExpandBounds(empty); !got.IsEmpty() {
		t.Errorf("empty input should pass through, got %+v", got)
	}

	nan := Rect{MinX: 0, MinY: 0, MaxX: float32(math.NaN()), MaxY: 10}
	if got := f.ExpandBounds(nan); isFiniteRect(got) {
		t.Errorf("non-finite input should pass through, got %+v", got)
	}

	// Input (0,0)-(50,50): every side must reach at least 20px out. Note the
	// exact per-side value is 20 (15 blur + 5 offset) on the max sides and 10
	// (15 blur - 5 offset) on the min sides, so assert containment rather
	// than equality.
	input := Rect{MinX: 0, MinY: 0, MaxX: 50, MaxY: 50}
	exp := f.ExpandBounds(input)
	if exp.MinX > -10 || exp.MinY > -10 || exp.MaxX < 70 || exp.MaxY < 70 {
		t.Errorf("expansion %+v does not contain the offset+blur shadow", exp)
	}
}

// --- pixel correctness ---

// solidSquarePixmap returns a pixmap with an opaque square.
func solidSquarePixmap(w, h, x0, y0, x1, y1 int, c gg.RGBA) *gg.Pixmap {
	pm := gg.NewPixmap(w, h)
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			pm.SetPixel(x, y, c)
		}
	}
	return pm
}

func TestDropShadowHardShadowNoBlur(t *testing.T) {
	// White 5x5 square at (5,5)-(10,10); black full-alpha hard shadow at +(4,4).
	src := solidSquarePixmap(24, 24, 5, 5, 10, 10, gg.White)
	dst := gg.NewPixmap(24, 24)
	f := NewDropShadowFilter(4, 4, 0, gg.RGBA2(0, 0, 0, 1))

	f.Apply(src, dst, Rect{MinX: 0, MinY: 0, MaxX: 24, MaxY: 24})

	// Inside source: still white, shadow must not cover it.
	if c := dst.GetPixel(7, 7); c.A < 0.99 || c.R < 0.99 {
		t.Errorf("source pixel covered by shadow: %+v", c)
	}
	// Shadow-only area (shifted square minus overlap): black and opaque.
	if c := dst.GetPixel(12, 12); !(c.A > 0.99 && c.R < 0.01) {
		t.Errorf("expected hard black shadow at (12,12), got %+v", c)
	}
	if c := dst.GetPixel(9, 12); !(c.A > 0.99 && c.R < 0.01) {
		t.Errorf("expected hard black shadow at (9,12), got %+v", c)
	}
	// Outside both: untouched transparent — no pollution of transparent pixels.
	if c := dst.GetPixel(0, 0); c.A != 0 {
		t.Errorf("transparent backdrop polluted at (0,0): %+v", c)
	}
	if c := dst.GetPixel(20, 20); c.A != 0 {
		t.Errorf("transparent backdrop polluted at (20,20): %+v", c)
	}
	// Pixel between source edge and shadow: transparent gap.
	if c := dst.GetPixel(10, 7); c.A != 0 {
		t.Errorf("gap pixel should be transparent, got %+v", c)
	}
}

func TestDropShadowNegativeOffset(t *testing.T) {
	src := solidSquarePixmap(20, 20, 8, 8, 13, 13, gg.White)
	dst := gg.NewPixmap(20, 20)
	f := NewDropShadowFilter(-5, -5, 0, gg.RGBA2(0, 0, 0, 1))

	f.Apply(src, dst, Rect{MinX: 0, MinY: 0, MaxX: 20, MaxY: 20})

	// Shadow shifts up-left: (8,8)-(13,13) -> (3,3)-(8,8). Point (4,4)
	// is shadow-only.
	if c := dst.GetPixel(4, 4); !(c.A > 0.99 && c.R < 0.01) {
		t.Errorf("expected shadow at (4,4), got %+v", c)
	}
	// Source remains on top at overlap.
	if c := dst.GetPixel(10, 10); c.A < 0.99 || c.R < 0.99 {
		t.Errorf("source should stay on top: %+v", c)
	}
	// Lower-right region untouched.
	if c := dst.GetPixel(18, 18); c.A != 0 {
		t.Errorf("unexpected pixel at (18,18): %+v", c)
	}

	// ExpandBounds must reach into negative coordinates.
	exp := f.ExpandBounds(Rect{MinX: 8, MinY: 8, MaxX: 13, MaxY: 13})
	// no blur: left=ceil(5)=5, right=ceil(-5)=-5
	if exp.MinX != 3 || exp.MinY != 3 || exp.MaxX != 8 || exp.MaxY != 8 {
		t.Errorf("negative-offset ExpandBounds = %+v", exp)
	}
}

func TestDropShadowPremultipliedSemiTransparentSource(t *testing.T) {
	// 50% red source. The zero-offset black shadow fills the same footprint,
	// so every source pixel sits over full-coverage shadow.
	src := solidSquarePixmap(16, 16, 2, 2, 14, 14, gg.RGBA2(1, 0, 0, 0.5))
	dst := gg.NewPixmap(16, 16)
	f := NewDropShadowFilter(0, 0, 0, gg.RGBA2(0, 0, 0, 1))

	f.Apply(src, dst, Rect{MinX: 0, MinY: 0, MaxX: 16, MaxY: 16})

	// At (8,8): black shadow coverage 0.5 (derived from source alpha) under a
	// 50% red source.
	// premul shadow: (0,0,0,127.5); blend under source premul (127.5,0,0,127.5):
	//   out A = 127.5 + 127.5*(1-0.5) = 191 (~0.75)
	//   out R = 127.5 + 0 = 127.5 -> straight R = 127.5/191 ~= 0.667.
	c := dst.GetPixel(8, 8)
	if math.Abs(c.A-0.75) > 0.02 || math.Abs(c.R-2.0/3.0) > 0.02 || c.G > 0.02 || c.B > 0.02 {
		t.Errorf("premultiplied composite wrong: %+v", c)
	}
	idx := (8*16 + 8) * 4
	if math.Abs(float64(dst.Data()[idx+0])-127.5) > 2 || math.Abs(float64(dst.Data()[idx+3])-191) > 2 {
		t.Errorf("premul bytes R=%d A=%d, want ~127/191", dst.Data()[idx+0], dst.Data()[idx+3])
	}

	// A separate opaque source with an offset shadow gives an unambiguous
	// full-coverage shadow-only pixel: solid black, alpha 1.
	src2 := solidSquarePixmap(16, 16, 4, 4, 9, 9, gg.White)
	dst2 := gg.NewPixmap(16, 16)
	NewDropShadowFilter(4, 4, 0, gg.RGBA2(0, 0, 0, 1)).
		Apply(src2, dst2, Rect{MinX: 0, MinY: 0, MaxX: 16, MaxY: 16})
	if c := dst2.GetPixel(10, 10); c.A < 0.99 || c.R > 0.01 {
		t.Errorf("shadow-only pixel should be solid black: %+v", c)
	}

	// Fully transparent source pixels never gain premultiplied color.
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			if src.Data()[(y*16+x)*4+3] == 0 && dst.Data()[(y*16+x)*4+3] != 0 {
				t.Fatalf("outside-shadow pixel (%d,%d) unexpectedly written", x, y)
			}
		}
	}
}

func TestDropShadowColoredAndIntensity(t *testing.T) {
	// Blue, 100% color alpha, intensity 0.5 -> shadow coverage 0.5.
	src := solidSquarePixmap(16, 16, 4, 4, 8, 8, gg.White)
	dst := gg.NewPixmap(16, 16)
	f := NewDropShadowFilterIntensity(4, 4, 0, 0.5, gg.RGBA2(0, 0, 1, 1))
	f.Apply(src, dst, Rect{MinX: 0, MinY: 0, MaxX: 16, MaxY: 16})

	c := dst.GetPixel(10, 10) // shadow-only
	if math.Abs(c.A-0.5) > 0.02 || c.B < 0.98 || c.R > 0.02 {
		t.Errorf("intensity-scaled blue shadow wrong: %+v", c)
	}
}

func TestDropShadowZeroIntensityAndTransparentColor(t *testing.T) {
	src := solidSquarePixmap(12, 12, 3, 3, 8, 8, gg.RGBA2(0, 1, 0, 0.4))

	for name, f := range map[string]*DropShadowFilter{
		"zero intensity":     NewDropShadowFilterIntensity(3, 3, 2, 0, gg.Black),
		"transparent col":    NewDropShadowFilter(3, 3, 2, gg.RGBA2(0, 0, 0, 0)),
		"negative intensity": NewDropShadowFilterIntensity(3, 3, 2, -1, gg.Black),
	} {
		t.Run(name, func(t *testing.T) {
			dst := gg.NewPixmap(12, 12)
			f.Apply(src, dst, Rect{MinX: 0, MinY: 0, MaxX: 12, MaxY: 12})

			// Source pixels copied faithfully...
			if c := dst.GetPixel(5, 5); !colorApproxEqual(c, gg.RGBA2(0, 1, 0, 0.4), 0.02) {
				t.Errorf("source altered without shadow: %+v", c)
			}
			// ...and nothing appears where the source is transparent.
			if c := dst.GetPixel(0, 0); c.A != 0 {
				t.Errorf("shadow leaked with %s: %+v", name, c)
			}
		})
	}
}

func TestDropShadowBlurredShadowSpreads(t *testing.T) {
	src := solidSquarePixmap(30, 30, 10, 10, 15, 15, gg.White)
	dst := gg.NewPixmap(30, 30)
	f := NewDropShadowFilter(0, 0, 3, gg.RGBA2(0, 0, 0, 1))
	f.Apply(src, dst, Rect{MinX: 0, MinY: 0, MaxX: 30, MaxY: 30})

	// Directly beside the square the blur must leave a partial shadow.
	if c := dst.GetPixel(16, 12); c.A <= 0.05 || c.A >= 0.95 {
		t.Errorf("blurred edge should have partial alpha, got %+v", c)
	}
	// Three-sigma away (~9px) shadow should have faded to ~0.
	if c := dst.GetPixel(24, 12); c.A > 0.02 {
		t.Errorf("shadow should have faded past 3 sigma, got %+v", c)
	}
	// Source stays fully white on top of its own blurred shadow.
	if c := dst.GetPixel(12, 12); c.A < 0.99 || c.R < 0.99 {
		t.Errorf("source altered by blurred shadow: %+v", c)
	}
}

func TestDropShadowFractionalOffset(t *testing.T) {
	// Room on every side so no destination edge interferes.
	src := solidSquarePixmap(20, 20, 8, 8, 12, 12, gg.White)
	dst := gg.NewPixmap(20, 20)
	f := NewDropShadowFilter(0.5, 0.5, 0, gg.RGBA2(0, 0, 0, 1))
	f.Apply(src, dst, Rect{MinX: 0, MinY: 0, MaxX: 20, MaxY: 20})

	// Shifted square spans (8.5,8.5)-(12.5,12.5). Pixel (12,10) straddles the
	// right edge with two of four bilinear neighbors inside -> ~50% coverage.
	c := dst.GetPixel(12, 10)
	if c.A > 0.6 || c.A < 0.4 {
		t.Errorf("fractional offset should give ~50%% coverage at edge, got %+v", c)
	}
	// Interior of the shifted square stays fully covered.
	if c := dst.GetPixel(10, 10); c.A < 0.99 {
		t.Errorf("shadow interior should be fully covered: %+v", c)
	}
}

// --- white background, semi-transparent circle (the headline scenario) ---

func TestDropShadowWhiteBackgroundCircle(t *testing.T) {
	const size = 40
	// Source layer: a semi-transparent red circle on a TRANSPARENT layer, so
	// the shadow is derived from the circle's alpha, not a full-rect card.
	pm := gg.NewPixmap(size, size)
	dc := gg.NewContextForPixmap(pm)
	dc.SetRGBA(1, 0, 0, 0.5)
	dc.DrawCircle(16, 16, 8)
	dc.Fill()

	// Composite onto an opaque white destination, mirroring a white UI surface.
	dst := gg.NewPixmap(size, size)
	dst.FillRect(dst.Bounds(), 255, 255, 255, 255)

	f := NewDropShadowFilterIntensity(4, 4, 2, 0.8, gg.RGBA2(0, 0, 0, 1))
	f.Apply(pm, dst, Rect{MinX: 0, MinY: 0, MaxX: size, MaxY: size})

	// Background corner stays white: the circle shadow never reaches it.
	if c := dst.GetPixel(1, 1); c.A < 0.99 || c.R < 0.99 {
		t.Errorf("white backdrop corrupted: %+v", c)
	}
	// A point above the circle (opposite the +offset) is untouched white too.
	if c := dst.GetPixel(16, 4); c.R < 0.99 || c.G < 0.99 || c.B < 0.99 {
		t.Errorf("backdrop above the circle polluted: %+v", c)
	}
	// Circle center keeps its red tint over the shadow (red dominates blue).
	if c := dst.GetPixel(16, 16); !(c.R > c.B && c.A > 0.9) {
		t.Errorf("circle content lost: %+v", c)
	}
	// A strong shadow-only pixel down-right of the circle is clearly darker
	// than the white backdrop (R ~0.66 vs 1.0).
	shadowPx := dst.GetPixel(25, 20)
	if shadowPx.R >= 0.85 || shadowPx.A < 0.99 {
		t.Errorf("shadow not visible on white bg: %+v", shadowPx)
	}
	// Bounds cover circle + offset + 3-sigma blur: blur reach is 6px; the
	// +4 offset extends the max sides to 10, min sides stay at 6-4=2.
	exp := f.ExpandBounds(Rect{MinX: 8, MinY: 8, MaxX: 24, MaxY: 24})
	if exp.MinX != 6 || exp.MinY != 6 || exp.MaxX != 34 || exp.MaxY != 34 {
		t.Errorf("circle ExpandBounds = %+v", exp)
	}
}

// --- in-place, undersized target, repeated apply ---

func TestDropShadowInPlace(t *testing.T) {
	pm := solidSquarePixmap(20, 20, 6, 6, 11, 11, gg.White)
	genBefore := pm.GenerationID()

	f := NewDropShadowFilter(3, 3, 1, gg.RGBA2(0, 0, 0, 1))
	f.Apply(pm, pm, Rect{MinX: 0, MinY: 0, MaxX: 20, MaxY: 20}) // must not panic

	if pm.GenerationID() == genBefore {
		t.Error("in-place Apply should bump the generation id")
	}
	// Source still white.
	if c := pm.GetPixel(8, 8); c.A < 0.99 || c.R < 0.99 {
		t.Errorf("in-place source corrupted: %+v", c)
	}
	// Shadow rendered close to the source edge (blur keeps coverage high).
	if c := pm.GetPixel(12, 12); c.A <= 0.5 {
		t.Errorf("in-place shadow missing: %+v", c)
	}
}

func TestDropShadowUndersizedDestination(t *testing.T) {
	src := solidSquarePixmap(30, 30, 5, 5, 20, 20, gg.White)
	dst := gg.NewPixmap(10, 10) // smaller than source/expanded bounds
	f := NewDropShadowFilter(6, 6, 2, gg.RGBA2(0, 0, 0, 1))

	// Must stay inside dst and never panic.
	f.Apply(src, dst, Rect{MinX: 0, MinY: 0, MaxX: 30, MaxY: 30})
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			if c := dst.GetPixel(x, y); c.A < 0 || c.A > 1.0001 {
				t.Fatalf("out-of-range write produced invalid pixel (%d,%d)=%+v", x, y, c)
			}
		}
	}
}

func TestDropShadowRepeatedApplyIsStable(t *testing.T) {
	src := solidSquarePixmap(20, 20, 5, 5, 10, 10, gg.White)
	f := NewDropShadowFilter(3, 3, 1, gg.RGBA2(0, 0, 0, 0.6))

	dst1 := gg.NewPixmap(20, 20)
	f.Apply(src, dst1, Rect{MinX: 0, MinY: 0, MaxX: 20, MaxY: 20})

	dst2 := gg.NewPixmap(20, 20)
	f.Apply(src, dst2, Rect{MinX: 0, MinY: 0, MaxX: 20, MaxY: 20})
	f.Apply(src, dst2, Rect{MinX: 0, MinY: 0, MaxX: 20, MaxY: 20})

	// First application is deterministic...
	dst1b := gg.NewPixmap(20, 20)
	f.Apply(src, dst1b, Rect{MinX: 0, MinY: 0, MaxX: 20, MaxY: 20})
	for i := range dst1.Data() {
		if diff := int(dst1.Data()[i]) - int(dst1b.Data()[i]); diff > 1 || diff < -1 {
			t.Fatalf("Apply not deterministic at byte %d: %d vs %d", i, dst1.Data()[i], dst1b.Data()[i])
		}
	}
	// ...re-applying over the same output never pushes any channel past 255
	// and never erases the source.
	for i := 0; i < len(dst2.Data()); i += 4 {
		if dst2.Data()[i+3] == 0 && (dst2.Data()[i] != 0 || dst2.Data()[i+1] != 0 || dst2.Data()[i+2] != 0) {
			t.Fatalf("transparent pixel gained color at byte %d", i)
		}
	}
	if c := dst2.GetPixel(7, 7); c.A < 0.99 || c.R < 0.99 {
		t.Errorf("source degraded after repeated Apply: %+v", c)
	}
}

// --- degenerate inputs: never panic ---

func TestDropShadowDegenerateInputs(t *testing.T) {
	good := gg.NewPixmap(8, 8)
	good.SetPixel(4, 4, gg.White)

	cases := []struct {
		name string
		src  *gg.Pixmap
		dst  *gg.Pixmap
	}{
		{"both nil", nil, nil},
		{"nil src", nil, gg.NewPixmap(8, 8)},
		{"nil dst", good, nil},
	}
	f := NewSimpleDropShadow(2, 2, 1)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f.Apply(tc.src, tc.dst, Rect{MinX: 0, MinY: 0, MaxX: 8, MaxY: 8})
		})
	}

	// Empty bounds: no-op, dst untouched.
	dst := gg.NewPixmap(8, 8)
	f.Apply(good, dst, EmptyRect())
	if c := dst.GetPixel(4, 4); c.A != 0 {
		t.Errorf("empty bounds must be a no-op, got %+v", c)
	}

	// Non-finite bounds: fallback without panic.
	f.Apply(good, dst, Rect{MinX: 0, MinY: 0, MaxX: float32(math.Inf(1)), MaxY: 8})
	f.Apply(good, dst, Rect{MinX: float32(math.NaN()), MinY: 0, MaxX: 8, MaxY: 8})

	// Huge but finite bounds: fallback path instead of giant allocation.
	big := Rect{MinX: 0, MinY: 0, MaxX: 1 << 20, MaxY: 1 << 20}
	f.Apply(good, gg.NewPixmap(8, 8), big)

	// Non-finite parameters: sanitized to safe values.
	weird := NewDropShadowFilter(math.NaN(), math.Inf(-1), math.NaN(),
		gg.RGBA2(math.NaN(), 0, math.Inf(1), math.NaN()))
	dst2 := gg.NewPixmap(8, 8)
	weird.Apply(good, dst2, Rect{MinX: 0, MinY: 0, MaxX: 8, MaxY: 8})

	// Huge blur radius: kernel guard fallback, no panic.
	hugeBlur := NewDropShadowFilter(0, 0, 1e6, gg.Black)
	hugeBlur.Apply(good, dst2, Rect{MinX: 0, MinY: 0, MaxX: 8, MaxY: 8})

	// Oversized working region falls back to a source-over copy: the single
	// source pixel must still reach the destination, with no panic.
	oversized := NewDropShadowFilter(0, 0, 0, gg.Black)
	dst3 := gg.NewPixmap(8, 8)
	oversized.Apply(good, dst3, Rect{MinX: 0, MinY: 0, MaxX: 1 << 16, MaxY: 1 << 16})
	if c := dst3.GetPixel(4, 4); c.A < 0.99 {
		t.Errorf("oversized-bounds fallback should still composite source: %+v", c)
	}

	// Extreme but finite offsets expand the rect past int32 range. The
	// float->int conversion must be guarded (no wrap, no giant allocation,
	// no panic); it degrades to a source-over fallback.
	extreme := NewDropShadowFilter(1e9, -1e9, 1e9, gg.Black)
	dstExtreme := gg.NewPixmap(8, 8)
	extreme.Apply(good, dstExtreme, Rect{MinX: 0, MinY: 0, MaxX: 8, MaxY: 8})
	if c := dstExtreme.GetPixel(4, 4); c.A < 0.99 {
		t.Errorf("extreme-offset fallback should keep source: %+v", c)
	}
	// ExpandBounds itself remains finite and deterministic.
	if eb := extreme.ExpandBounds(Rect{MinX: 0, MinY: 0, MaxX: 8, MaxY: 8}); !isFiniteRect(eb) {
		t.Errorf("extreme ExpandBounds must stay finite: %+v", eb)
	}

	// Out-of-range color components are clamped to [0,1]; shadow stays valid.
	clamped := NewDropShadowFilter(0, 0, 0, gg.RGBA2(2, -1, 0.5, 2))
	dst4 := gg.NewPixmap(8, 8)
	clamped.Apply(good, dst4, Rect{MinX: 0, MinY: 0, MaxX: 8, MaxY: 8})
	if c := dst4.GetPixel(3, 3); c.A < 0 || c.A > 1.0001 || c.R > 1.0001 {
		t.Errorf("clamped color produced invalid pixel: %+v", c)
	}
}

func TestDropShadowBufferPoolReuse(t *testing.T) {
	// Exercise the pool put/get round trip across several sizes, including an
	// oversized buffer that must be dropped rather than retained.
	src := solidSquarePixmap(12, 12, 4, 4, 8, 8, gg.White)
	f := NewDropShadowFilter(1, 1, 1, gg.Black)
	for i := 0; i < 4; i++ {
		dst := gg.NewPixmap(12, 12)
		f.Apply(src, dst, Rect{MinX: 0, MinY: 0, MaxX: 12, MaxY: 12})
		if c := dst.GetPixel(6, 6); c.A < 0.99 {
			t.Fatalf("pooled-buffer run %d lost source: %+v", i, c)
		}
	}
	// Oversized allocation path (above shadowPoolCap) must not panic on put.
	big := make([]float32, shadowPoolCap+1)
	putShadowFloats(big)
}

// --- filter chain composition ---

// recordFilter is a pass-through filter that records its position in the
// observed call sequence for chain tests.
type recordFilter struct {
	order *[]byte
	tag   byte
}

func (r *recordFilter) Apply(src, dst *gg.Pixmap, bounds Rect) {
	copyPixmap(src, dst, bounds)
	*r.order = append(*r.order, r.tag)
}

func (r *recordFilter) ExpandBounds(input Rect) Rect { return input }

func TestDropShadowInFilterChain(t *testing.T) {
	// Keep a margin around the source so the fully expanded (negative-origin)
	// shadow still lands inside the 0-origin working pixmaps.
	const margin = 12
	src := gg.NewPixmap(44, 44)
	for y := margin + 6; y < margin+11; y++ {
		for x := margin + 6; x < margin+11; x++ {
			src.SetPixel(x, y, gg.White)
		}
	}
	inBounds := Rect{MinX: margin, MinY: margin, MaxX: margin + 20, MaxY: margin + 20}

	// Two drop shadows with opposing offsets, plus a pass-through filter.
	order := make([]byte, 0, 3)
	s1 := NewDropShadowFilter(-4, -4, 1, gg.RGBA2(1, 0, 0, 1)) // red shadow up-left
	s2 := NewDropShadowFilter(4, 4, 1, gg.RGBA2(0, 0, 1, 1))   // blue shadow down-right
	mid := &recordFilter{order: &order, tag: 2}
	chain := NewFilterChain(s1, mid, s2)

	if chain.Len() != 3 {
		t.Fatalf("chain Len = %d, want 3", chain.Len())
	}

	// Chain expansion: each shadow contributes blur reach 3 plus its offset.
	// The pass-through filter adds nothing, so per side the bounds accumulate
	// both filters: blur 3+3=6; opposing offsets (-4 then +4) net to 0 on min
	// sides and 8 on max sides. Assert containment of both offset directions
	// and both blurs.
	exp := chain.ExpandBounds(inBounds)
	if exp.MinX > margin-6 || exp.MinY > margin-6 ||
		exp.MaxX < margin+20+6 || exp.MaxY < margin+20+6 {
		t.Errorf("chain ExpandBounds too small: %+v", exp)
	}
	if exp.MinX < 0 || exp.MinY < 0 {
		t.Errorf("margin should keep expanded bounds non-negative, got %+v", exp)
	}

	// The FilterChain allocates temps from the combined bounds; give the
	// target the expanded size with origin 0.
	w := int(exp.MaxX) + 2
	h := int(exp.MaxY) + 2
	dst := gg.NewPixmap(w, h)
	chain.Apply(src, dst, inBounds)

	// Source survives the full chain.
	if c := dst.GetPixel(margin+8, margin+8); c.A < 0.99 || c.R < 0.99 {
		t.Errorf("source lost through chain: %+v", c)
	}
	// The middle pass-through filter ran exactly once, between the two
	// shadow filters (the chain is first-to-last ordered).
	if len(order) != 1 || order[0] != 2 {
		t.Errorf("middle filter order = %v, want [2] once", order)
	}
	// Blue (last) shadow visible down-right of the source.
	if c := dst.GetPixel(margin+14, margin+14); c.B <= c.R {
		t.Errorf("final blue shadow missing: %+v", c)
	}
}

func TestDropShadowChainEmptyAndSingle(t *testing.T) {
	src := solidSquarePixmap(10, 10, 2, 2, 6, 6, gg.White)

	// Empty chain: identity copy.
	empty := NewFilterChain()
	dst := gg.NewPixmap(10, 10)
	empty.Apply(src, dst, Rect{MinX: 0, MinY: 0, MaxX: 10, MaxY: 10})
	if c := dst.GetPixel(4, 4); c.A < 0.99 {
		t.Errorf("empty chain altered source: %+v", c)
	}

	// Single shadow filter through the chain is equivalent to direct apply.
	f := NewDropShadowFilter(2, 2, 0, gg.RGBA2(0, 0, 0, 1))
	chain := NewFilterChain(f)
	direct := gg.NewPixmap(14, 14)
	viaChain := gg.NewPixmap(14, 14)
	b := Rect{MinX: 0, MinY: 0, MaxX: 14, MaxY: 14}
	f.Apply(src, direct, b)
	chain.Apply(src, viaChain, b)
	for i := range direct.Data() {
		if diff := int(direct.Data()[i]) - int(viaChain.Data()[i]); diff > 1 || diff < -1 {
			t.Fatalf("single-filter chain differs at byte %d: %d vs %d", i, direct.Data()[i], viaChain.Data()[i])
		}
	}
}

// --- cache invalidation ---

func TestDropShadowCacheInvalidation(t *testing.T) {
	cache := NewLayerCache(4)
	src := solidSquarePixmap(16, 16, 4, 4, 9, 9, gg.White)
	bounds := Rect{MinX: 0, MinY: 0, MaxX: 16, MaxY: 16}

	render := func(f *DropShadowFilter) (*gg.Pixmap, uint64) {
		dst := gg.NewPixmap(16, 16)
		f.Apply(src, dst, bounds)
		h := f.Hash()
		cache.Put(h, dst, 1)
		return dst, h
	}

	f1 := NewDropShadowFilter(3, 3, 2, gg.RGBA2(0, 0, 0, 0.7))
	dst1, h1 := render(f1)
	if cached, ok := cache.Get(h1); !ok || cached != dst1 {
		t.Fatal("first render should be cached under its hash")
	}

	// Same configuration: same hash -> cache hit.
	f1Again := NewDropShadowFilter(3, 3, 2, gg.RGBA2(0, 0, 0, 0.7))
	if f1Again.Hash() != h1 {
		t.Fatal("identical configuration must hash identically")
	}

	// Changed offset / intensity / color: new hash, old entry invalidated.
	f2 := NewDropShadowFilterIntensity(5, 3, 2, 1.2, gg.RGBA2(0, 0, 0, 0.7))
	_, h2 := render(f2)
	if h2 == h1 {
		t.Fatal("parameter change must change the cache key")
	}
	cache.Invalidate(h1)
	if cache.Contains(h1) {
		t.Error("Invalidate must remove the stale entry")
	}
	if !cache.Contains(h2) {
		t.Error("new entry should remain cached")
	}

	// Generation id changes on Apply, which is the Pixmap-level cache signal.
	plain := gg.NewPixmap(16, 16)
	before := plain.GenerationID()
	f1.Apply(src, plain, bounds)
	if plain.GenerationID() == before {
		t.Error("Apply must bump Pixmap generation id for GPU cache invalidation")
	}
}

// --- transparent background end-to-end ---

func TestDropShadowTransparentBackdropUntouched(t *testing.T) {
	src := solidSquarePixmap(18, 18, 6, 6, 10, 10, gg.RGBA2(1, 0, 1, 0.3))
	dst := gg.NewPixmap(18, 18)
	f := NewDropShadowFilter(2, 2, 1, gg.RGBA2(0, 0, 0, 0.9))
	f.Apply(src, dst, Rect{MinX: 0, MinY: 0, MaxX: 18, MaxY: 18})

	for y := 0; y < 18; y++ {
		for x := 0; x < 18; x++ {
			i := (y*18 + x) * 4
			a := dst.Data()[i+3]
			if a == 0 && (dst.Data()[i] != 0 || dst.Data()[i+1] != 0 || dst.Data()[i+2] != 0) {
				t.Fatalf("fully transparent pixel (%d,%d) carries color rgb=%d,%d,%d",
					x, y, dst.Data()[i], dst.Data()[i+1], dst.Data()[i+2])
			}
		}
	}
}

// TestDropShadowRandomizedStress hammers Apply with adversarial parameters and
// destination sizes, including non-finite values, extreme magnitudes and
// in-place re-application. It must never panic, allocate pathologically, or
// emit color under a zero-alpha pixel.
func TestDropShadowRandomizedStress(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	src := gg.NewPixmap(24, 24)
	for y := 6; y < 18; y++ {
		for x := 6; x < 18; x++ {
			src.SetPixel(x, y, gg.RGBA2(rng.Float64(), rng.Float64(), rng.Float64(), rng.Float64()))
		}
	}
	values := []float64{
		0, -0.0, 1, -1, 5, -5, 0.5, -0.5,
		1e9, -1e9, math.NaN(), math.Inf(1), math.Inf(-1),
	}
	pick := func() float64 { return values[rng.Intn(len(values))] }

	for i := 0; i < 200; i++ {
		f := NewDropShadowFilter(pick(), pick(), pick(),
			gg.RGBA2(pick(), pick(), pick(), pick()))
		f.Intensity = pick()

		var dst *gg.Pixmap
		switch rng.Intn(3) {
		case 0:
			dst = gg.NewPixmap(24, 24)
		case 1:
			dst = gg.NewPixmap(rng.Intn(20)+1, rng.Intn(20)+1)
		default:
			dst = gg.NewPixmap(30, 30)
		}
		b := Rect{MinX: 0, MinY: 0,
			MaxX: float32(rng.Intn(30) + 1), MaxY: float32(rng.Intn(30) + 1)}

		f.Apply(src, dst, b)
		f.Apply(dst, dst, b) // in-place re-application

		for idx := 0; idx < len(dst.Data()); idx += 4 {
			if dst.Data()[idx+3] == 0 &&
				(dst.Data()[idx]|dst.Data()[idx+1]|dst.Data()[idx+2]) != 0 {
				t.Fatalf("iter %d: transparent pixel carries color", i)
			}
		}
	}
}

// TestDropShadowKernelConcurrent exercises the shared kernel cache from many
// goroutines to prove the RWMutex guards are race-free and kernels normalize.
func TestDropShadowKernelConcurrent(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k := shadowGaussianKernel(float64(i%8) + 0.5)
			var sum float32
			for _, v := range k {
				sum += v
			}
			if sum < 0.99 || sum > 1.01 {
				t.Errorf("kernel not normalized: sum=%v", sum)
			}
		}(i)
	}
	wg.Wait()
}

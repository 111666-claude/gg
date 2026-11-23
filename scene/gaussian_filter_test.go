package scene

import (
	"testing"

	"github.com/gogpu/gg"
)

// ---------------------------------------------------------------------------
// Kernel construction
// ---------------------------------------------------------------------------

func TestGaussianKernelIdentity(t *testing.T) {
	f := NewGaussianBlur(0, 0)
	kx, ky := f.kernels()

	if len(kx) != 1 || kx[0] != 1 {
		t.Errorf("zero-radius X kernel = %v, want [1]", kx)
	}
	if len(ky) != 1 || ky[0] != 1 {
		t.Errorf("zero-radius Y kernel = %v, want [1]", ky)
	}
	if f.RadiusX() != 0 || f.RadiusY() != 0 {
		t.Errorf("zero-radius reach = (%v,%v), want (0,0)", f.RadiusX(), f.RadiusY())
	}

	// Negative and NaN inputs are deterministic identities too.
	for _, r := range []float64{-1, -100} {
		g := NewGaussianBlur(r, r)
		kx, ky := g.kernels()
		if len(kx) != 1 || len(ky) != 1 {
			t.Errorf("radius %v: want identity kernels, got %v / %v", r, kx, ky)
		}
	}
}

func TestGaussianKernelOddNormalizedAndSymmetric(t *testing.T) {
	// radius is sigma; reach is ceil(3*sigma): sigma 5 -> 15, sigma 3 -> 9.
	f := NewGaussianBlur(5, 3)
	kx, ky := f.kernels()

	if len(kx) != 31 || f.RadiusX() != 15 {
		t.Errorf("X: taps=%d reach=%v, want 31 taps reach 15", len(kx), f.RadiusX())
	}
	if len(ky) != 19 || f.RadiusY() != 9 {
		t.Errorf("Y: taps=%d reach=%v, want 19 taps reach 9", len(ky), f.RadiusY())
	}

	for name, k := range map[string][]float32{"x": kx, "y": ky} {
		var sum float32
		for _, w := range k {
			if w < 0 || w > 1 {
				t.Fatalf("%s kernel weight out of [0,1]: %v", name, w)
			}
			sum += w
		}
		if absf(float64(sum-1)) > 1e-6 {
			t.Errorf("%s kernel sum = %v, want 1", name, sum)
		}
		// Symmetry about the center.
		for i := 0; i < len(k)/2; i++ {
			if absf(float64(k[i]-k[len(k)-1-i])) > 1e-7 {
				t.Errorf("%s kernel not symmetric: %v vs %v", name, k[i], k[len(k)-1-i])
			}
		}
		// Center is the peak.
		mid := len(k) / 2
		if k[mid] <= k[mid-1] {
			t.Errorf("%s kernel center not the peak", name)
		}
	}
}

func TestGaussianKernelEvenTaps(t *testing.T) {
	f := NewGaussianBlurTaps(1.5, 1.0, 4, 2)
	kx, ky := f.kernels()

	if len(kx) != 4 || len(ky) != 2 {
		t.Fatalf("taps = (%d,%d), want (4,2)", len(kx), len(ky))
	}
	if f.RadiusX() != 2 || f.RadiusY() != 1 {
		t.Errorf("reach = (%v,%v), want (2,1)", f.RadiusX(), f.RadiusY())
	}

	// Even kernel offsets: -(taps/2-1) … taps/2.
	if offs := kernelOffsets(4); len(offs) != 4 || offs[0] != -1 || offs[3] != 2 {
		t.Errorf("4-tap offsets = %v, want [-1 0 1 2]", offs)
	}
	if offs := kernelOffsets(2); len(offs) != 2 || offs[0] != 0 || offs[1] != 1 {
		t.Errorf("2-tap offsets = %v, want [0 1]", offs)
	}

	// Normalized and symmetric by |offset|: offsets are [-1 0 1 2],
	// so w(-1)==w(1); the half-pixel shift makes w(0) and w(2) distinct.
	var sumX float32
	for _, w := range kx {
		sumX += w
	}
	if absf(float64(sumX)-1) > 1e-6 {
		t.Errorf("even kernel sum = %v, want 1", sumX)
	}
	if absf(float64(kx[0]-kx[2])) > 1e-7 { // offsets -1 and 1
		t.Errorf("even kernel |-1| pair unequal: %v vs %v", kx[0], kx[2])
	}
	if !(kx[1] > kx[0] && kx[0] == kx[2] && kx[2] > kx[3]) { // weights decay with distance from the half-pixel center
		t.Errorf("even kernel not monotonic about its half-pixel center: %v", kx)
	}
}

func TestGaussianKernelCappedAtMaxRadius(t *testing.T) {
	f := NewGaussianBlur(1_000_000, 500)
	if f.RadiusX() != MaxGaussianRadius {
		t.Errorf("clamped X reach = %v, want %v", f.RadiusX(), MaxGaussianRadius)
	}
	if f.RadiusY() != MaxGaussianRadius {
		t.Errorf("clamped Y reach = %v, want %v", f.RadiusY(), MaxGaussianRadius)
	}

	// Explicit taps are capped too, parity preserved.
	even := NewGaussianBlurTaps(10, 10, 10_000, 10_001)
	kx, ky := even.kernels()
	if reach(kx) != int(MaxGaussianRadius) || len(kx)%2 != 0 {
		t.Errorf("capped even taps: reach=%d len=%d", reach(kx), len(kx))
	}
	if reach(ky) != int(MaxGaussianRadius) || len(ky)%2 != 1 {
		t.Errorf("capped odd taps: reach=%d len=%d", reach(ky), len(ky))
	}
}

func TestGaussianKernelCachingStable(t *testing.T) {
	f := NewGaussianBlur(4, 4)
	a1, b1 := f.kernels()
	a2, b2 := f.kernels()
	// Same backing slices on repeated access.
	if &a1[0] != &a2[0] || &b1[0] != &b2[0] {
		t.Error("kernels should be cached and reused across calls")
	}
}

// ---------------------------------------------------------------------------
// ExpandBounds
// ---------------------------------------------------------------------------

func TestGaussianExpandBounds(t *testing.T) {
	// radius is sigma: reach = ceil(3*sigma): 5 -> 15, 2 -> 6.
	f := NewGaussianBlur(5, 2)
	got := f.ExpandBounds(Rect{MinX: 10, MinY: 20, MaxX: 30, MaxY: 40})
	want := Rect{MinX: -5, MinY: 14, MaxX: 45, MaxY: 46}
	if got != want {
		t.Errorf("ExpandBounds = %+v, want %+v", got, want)
	}

	// Empty input stays empty — no phantom bounds for empty layers.
	empty := f.ExpandBounds(EmptyRect())
	if !empty.IsEmpty() {
		t.Errorf("ExpandBounds(empty) = %+v, want empty", empty)
	}

	// Identity filter does not expand.
	id := NewGaussianBlur(0, 0)
	r := Rect{MinX: 1, MinY: 2, MaxX: 3, MaxY: 4}
	if id.ExpandBounds(r) != r {
		t.Error("identity filter must not expand bounds")
	}
}

// ---------------------------------------------------------------------------
// Apply: edge cases
// ---------------------------------------------------------------------------

func TestGaussianApplyZeroRadiusIsIdentity(t *testing.T) {
	src := gg.NewPixmap(8, 8)
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			src.SetPixelPremul(x, y, uint8(x*30), uint8(y*30), 0, 200)
		}
	}
	dst := gg.NewPixmap(8, 8)
	f := NewGaussianBlur(0, 0)
	f.Apply(src, dst, Rect{MinX: 0, MinY: 0, MaxX: 8, MaxY: 8})

	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			i := (y*8 + x) * 4
			s, d := src.Data(), dst.Data()
			if d[i] != s[i] || d[i+1] != s[i+1] || d[i+2] != s[i+2] || d[i+3] != s[i+3] {
				t.Fatalf("identity mismatch at (%d,%d): src=%v dst=%v", x, y, s[i:i+4], d[i:i+4])
			}
		}
	}
}

func TestGaussianApplyNilAndEmpty(t *testing.T) {
	f := NewGaussianBlur(5, 5)
	bounds := Rect{MinX: 0, MinY: 0, MaxX: 8, MaxY: 8}

	// Must not panic on nil inputs.
	f.Apply(nil, nil, bounds)
	f.Apply(gg.NewPixmap(4, 4), nil, bounds)
	f.Apply(nil, gg.NewPixmap(4, 4), bounds)

	// Empty bounds leaves the destination untouched.
	dst := gg.NewPixmap(4, 4)
	dst.SetPixelPremul(2, 2, 10, 20, 30, 40)
	snapshot := append([]byte(nil), dst.Data()...)
	f.Apply(gg.NewPixmap(4, 4), dst, EmptyRect())
	if string(dst.Data()) != string(snapshot) {
		t.Error("Apply with empty bounds must not touch dst")
	}

	// Fully transparent source yields a fully transparent destination.
	src := gg.NewPixmap(6, 6)
	out := gg.NewPixmap(6, 6)
	f.Apply(src, out, Rect{MinX: 0, MinY: 0, MaxX: 6, MaxY: 6})
	for i := range out.Data() {
		if out.Data()[i] != 0 {
			t.Fatalf("empty layer produced non-zero output at byte %d", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Numerical correctness against a brute-force separable reference
// ---------------------------------------------------------------------------

// referenceBlur computes the expected premultiplied RGBA output with zero
// padding outside the source and the same integer-offset kernel convention.
func referenceBlur(t *testing.T, src *gg.Pixmap, sigmaX, sigmaY float64, tapsX, tapsY int) []byte {
	t.Helper()
	w, h := src.Width(), src.Height()
	kx := buildGaussianKernel1D(sigmaX, tapsX)
	ky := buildGaussianKernel1D(sigmaY, tapsY)
	ox := kernelOffsets(len(kx))
	oy := kernelOffsets(len(ky))

	out := make([]byte, w*h*4)
	sample := func(x, y int) [4]float32 {
		if x < 0 || y < 0 || x >= w || y >= h {
			return [4]float32{}
		}
		i := (y*w + x) * 4
		d := src.Data()
		return [4]float32{
			float32(d[i]), float32(d[i+1]), float32(d[i+2]), float32(d[i+3]),
		}
	}

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var acc [4]float32
			for iy, offY := range oy {
				// Horizontal convolution at this (zero-padded) row position.
				var hAcc [4]float32
				for ix, offX := range ox {
					p := sample(x+offX, y+offY)
					for c := 0; c < 4; c++ {
						hAcc[c] += p[c] * kx[ix]
					}
				}
				for c := 0; c < 4; c++ {
					acc[c] += hAcc[c] * ky[iy]
				}
			}
			i := (y*w + x) * 4
			for c := 0; c < 4; c++ {
				out[i+c] = clampByte255(acc[c])
			}
		}
	}
	return out
}

func TestGaussianApplyMatchesReference(t *testing.T) {
	src := gg.NewPixmap(16, 12)
	// Scatter a few premultiplied pixels, including semi-transparent ones.
	src.SetPixelPremul(8, 6, 200, 100, 50, 255)
	src.SetPixelPremul(3, 3, 128, 128, 128, 128)
	src.SetPixelPremul(12, 9, 40, 90, 160, 200)

	f := NewGaussianBlurTaps(1.5, 2.0, 7, 9) // odd, asymmetric
	dst := gg.NewPixmap(16, 12)
	f.Apply(src, dst, Rect{MinX: 0, MinY: 0, MaxX: 16, MaxY: 12})

	want := referenceBlur(t, src, 1.5, 2.0, 7, 9)
	for i := range want {
		d := absf(float64(dst.Data()[i]) - float64(want[i]))
		if d > 1 {
			t.Fatalf("byte %d: got %d want %d (full mismatch)", i, dst.Data()[i], want[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Glow semantics: black background, semi-transparent point, decay, alpha,
// transparent edges, no brightening outside the filter region.
// ---------------------------------------------------------------------------

func TestGaussianGlowOnBlackBackground(t *testing.T) {
	const n = 21
	src := gg.NewPixmap(n, n)
	// Semi-transparent white point at the center: straight white, alpha 0.5.
	src.SetPixel(n/2, n/2, gg.RGBA2(1, 1, 1, 0.5))

	f := NewGaussianBlur(4, 4)
	dst := gg.NewPixmap(n, n)
	f.Apply(src, dst, Rect{MinX: 0, MinY: 0, MaxX: n, MaxY: n})

	center := n / 2

	// 1. Alpha conservation: zero-padding spreads but never creates alpha.
	// Truncation at 3 sigma loses a small, deterministic fraction of the
	// mass (here ~30 alpha units over the whole 21x21 buffer), so allow a
	// bounded loss rather than expecting bit-exact conservation.
	var sumAIn, sumAOut int
	for i := 3; i < n*n*4; i += 4 {
		sumAIn += int(src.Data()[i])
		sumAOut += int(dst.Data()[i])
	}
	loss := sumAIn - sumAOut
	if loss < 0 || loss > sumAIn/4 {
		t.Errorf("alpha conservation violated: in=%d out=%d loss=%d", sumAIn, sumAOut, loss)
	}

	// 2. Monotonic radial decay along the center row.
	for x := center; x < n-1; x++ {
		a1 := dst.Data()[(center*n+x)*4+3]
		a2 := dst.Data()[(center*n+x+1)*4+3]
		if a2 > a1 {
			t.Errorf("alpha increased with distance at x=%d: %d -> %d", x, a1, a2)
		}
	}

	// 3. Hue preservation in premultiplied space: the white point stays gray.
	c := dst.GetPixel(center, center)
	if absf(c.R-1) > 0.02 || absf(c.G-1) > 0.02 || absf(c.B-1) > 0.02 {
		t.Errorf("blurred straight color drifted from white: %+v", c)
	}
	if c.A <= 0 || c.A >= 0.5 {
		t.Errorf("center alpha = %v, want (0, 0.5) after spreading", c.A)
	}

	// 4. Nothing exceeds the source's maximum premultiplied value: the blur
	//    cannot brighten any pixel beyond the input anywhere in the buffer.
	srcMax := uint8(0)
	for i := 0; i < len(src.Data()); i++ {
		if src.Data()[i] > srcMax {
			srcMax = src.Data()[i]
		}
	}
	for i, v := range dst.Data() {
		if v > srcMax {
			t.Fatalf("output byte %d = %d exceeds source max %d", i, v, srcMax)
		}
	}

	// 5. Halo reaches well beyond the source pixel. The truncated tail at
	//    the exact reach can quantize to zero, so assert one tap inside.
	if dst.Data()[(center*n+(center+3))*4+3] == 0 {
		t.Error("expected nonzero glow inside the kernel reach")
	}
}

func TestGaussianTransparentEdgeNoBrightening(t *testing.T) {
	// Point in the corner: most of the kernel support falls outside the
	// pixmap and must sample transparent black, not edge-extended pixels.
	src := gg.NewPixmap(10, 10)
	src.SetPixelPremul(0, 0, 255, 255, 255, 255)

	f := NewGaussianBlur(3, 3)
	dst := gg.NewPixmap(10, 10)
	f.Apply(src, dst, Rect{MinX: 0, MinY: 0, MaxX: 10, MaxY: 10})

	// The corner must be dimmer than the source (weight lost to the outside).
	if dst.Data()[3] >= 255 {
		t.Errorf("corner brightened/held at %d; zero padding must dim it", dst.Data()[3])
	}
	// No output pixel may exceed the source value anywhere.
	for i := 0; i < len(dst.Data()); i += 4 {
		for c := 0; c < 4; c++ {
			if dst.Data()[i+c] > src.Data()[c] {
				t.Fatalf("edge pixel channel %d brightened to %d", c, dst.Data()[i+c])
			}
		}
	}
	// Decay along the top row.
	for x := 0; x < 8; x++ {
		if dst.Data()[(x+1)*4+3] > dst.Data()[x*4+3] {
			t.Errorf("top-row alpha increased with distance at x=%d", x)
		}
	}
}

func TestGaussianDoesNotWriteOutsideBounds(t *testing.T) {
	src := gg.NewPixmap(12, 12)
	src.SetPixelPremul(6, 6, 255, 0, 0, 255)

	dst := gg.NewPixmap(12, 12)
	// Seed the destination with a marker color.
	dst.Clear(gg.RGBA2(0.1, 0.2, 0.3, 0.4))
	snapshot := append([]byte(nil), dst.Data()...)

	f := NewGaussianBlur(2, 2)
	// Process only a 4x4 region; the surrounding marker pixels must survive.
	f.Apply(src, dst, Rect{MinX: 4, MinY: 4, MaxX: 8, MaxY: 8})

	for y := 0; y < 12; y++ {
		for x := 0; x < 12; x++ {
			i := (y*12 + x) * 4
			inside := x >= 4 && x < 8 && y >= 4 && y < 8
			if !inside && string(dst.Data()[i:i+4]) != string(snapshot[i:i+4]) {
				t.Fatalf("pixel (%d,%d) outside bounds was modified", x, y)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Independent axes
// ---------------------------------------------------------------------------

func TestGaussianSingleAxis(t *testing.T) {
	src := gg.NewPixmap(15, 15)
	src.SetPixelPremul(7, 7, 200, 100, 50, 255)

	// Horizontal-only blur: sigma 4 -> reach 12.
	f := NewGaussianBlur(4, 0)
	if f.RadiusX() != 12 || f.RadiusY() != 0 {
		t.Fatalf("reach = (%v,%v), want (12,0)", f.RadiusX(), f.RadiusY())
	}
	dst := gg.NewPixmap(15, 15)
	f.Apply(src, dst, Rect{MinX: 0, MinY: 0, MaxX: 15, MaxY: 15})

	// Vertical neighbors are untouched (Y kernel is identity, same column
	// samples only the point's own column -> zero above/below).
	if dst.Data()[((7-1)*15+7)*4+3] != 0 || dst.Data()[((7+1)*15+7)*4+3] != 0 {
		t.Error("vertical neighbors must stay transparent with RadiusY=0")
	}
	// Horizontal neighbors carry the glow.
	if dst.Data()[(7*15+(7+1))*4+3] == 0 || dst.Data()[(7*15+(7-1))*4+3] == 0 {
		t.Error("horizontal neighbors must carry the glow with RadiusX>0")
	}
	// The point's row is the only non-transparent row.
	for y := 0; y < 15; y++ {
		if y == 7 {
			continue
		}
		for x := 0; x < 15; x++ {
			if dst.Data()[(y*15+x)*4+3] != 0 {
				t.Fatalf("row %d should be fully transparent, pixel %d nonzero", y, x)
			}
		}
	}

	// Vertical-only blur mirrors the behavior.
	fv := NewGaussianBlur(0, 3)
	dstv := gg.NewPixmap(15, 15)
	fv.Apply(src, dstv, Rect{MinX: 0, MinY: 0, MaxX: 15, MaxY: 15})
	if dstv.Data()[(7*15+(7+1))*4+3] != 0 {
		t.Error("horizontal neighbor must stay transparent with RadiusX=0")
	}
	if dstv.Data()[((7+1)*15+7)*4+3] == 0 {
		t.Error("vertical neighbor must carry the glow with RadiusY>0")
	}
}

// TestGaussianExplicitReach verifies the taps constructor gives an exact
// pixel reach independent of the 3-sigma convention.
func TestGaussianExplicitReach(t *testing.T) {
	f := NewGaussianBlurTaps(1.0, 1.0, 11, 11) // reach 5
	if f.RadiusX() != 5 || f.RadiusY() != 5 {
		t.Errorf("explicit reach = (%v,%v), want (5,5)", f.RadiusX(), f.RadiusY())
	}
	got := f.ExpandBounds(Rect{MinX: 10, MinY: 10, MaxX: 20, MaxY: 20})
	want := Rect{MinX: 5, MinY: 5, MaxX: 25, MaxY: 25}
	if got != want {
		t.Errorf("ExpandBounds = %+v, want %+v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Reuse: source immutability, in-place application, huge radius
// ---------------------------------------------------------------------------

func TestGaussianRepeatedApplyPreservesSource(t *testing.T) {
	src := gg.NewPixmap(12, 12)
	src.SetPixelPremul(6, 6, 210, 110, 30, 180)
	src.SetPixelPremul(2, 9, 10, 200, 90, 120)
	snapshot := append([]byte(nil), src.Data()...)

	f := NewGaussianBlur(3, 5)
	dst1 := gg.NewPixmap(12, 12)
	dst2 := gg.NewPixmap(12, 12)
	bounds := Rect{MinX: 0, MinY: 0, MaxX: 12, MaxY: 12}

	f.Apply(src, dst1, bounds)
	f.Apply(src, dst2, bounds)

	if string(src.Data()) != string(snapshot) {
		t.Error("Apply modified the source pixmap")
	}
	if string(dst1.Data()) != string(dst2.Data()) {
		t.Error("repeated Apply on the same input gave different results")
	}
}

func TestGaussianInPlaceApplication(t *testing.T) {
	pm := gg.NewPixmap(12, 12)
	pm.SetPixelPremul(6, 6, 200, 200, 200, 255)

	reference := gg.NewPixmap(12, 12)
	f := NewGaussianBlur(3, 3)
	bounds := Rect{MinX: 0, MinY: 0, MaxX: 12, MaxY: 12}

	// Out-of-place reference.
	src := gg.NewPixmap(12, 12)
	copy(src.Data(), pm.Data())
	f.Apply(src, reference, bounds)

	// In-place: must use scratch internally and match the reference.
	f.Apply(pm, pm, bounds)
	if string(pm.Data()) != string(reference.Data()) {
		t.Error("in-place Apply differs from out-of-place Apply")
	}
	if string(src.Data()) == string(pm.Data()) {
		t.Error("blur had no effect in-place")
	}
}

func TestGaussianHugeRadiusBoundedAndDeterministic(t *testing.T) {
	src := gg.NewPixmap(16, 16)
	src.SetPixelPremul(8, 8, 255, 255, 255, 255)

	f := NewGaussianBlur(1e6, 1e6)
	if f.RadiusX() != MaxGaussianRadius || f.RadiusY() != MaxGaussianRadius {
		t.Fatalf("huge radius not capped: %v", f.RadiusX())
	}

	dst1 := gg.NewPixmap(16, 16)
	dst2 := gg.NewPixmap(16, 16)
	bounds := Rect{MinX: 0, MinY: 0, MaxX: 16, MaxY: 16}
	f.Apply(src, dst1, bounds)
	f.Apply(src, dst2, bounds)

	if string(dst1.Data()) != string(dst2.Data()) {
		t.Error("huge-radius blur is non-deterministic")
	}
	// Even with a huge kernel the tiny image only retains a fraction of the
	// weight; the output must still be bounded and the source untouched.
	if string(src.Data()[(8*16+8)*4:][:4]) != string([]byte{255, 255, 255, 255}) {
		t.Error("source modified by huge-radius blur")
	}
}

// ---------------------------------------------------------------------------
// FilterChain composition and nested filters
// ---------------------------------------------------------------------------

func TestGaussianFilterChainNested(t *testing.T) {
	src := gg.NewPixmap(20, 20)
	src.SetPixelPremul(10, 10, 255, 255, 255, 255)

	inner := NewGaussianBlur(2, 2) // sigma 2 -> reach 6 each
	outer := NewGaussianBlur(2, 2)
	chain := NewFilterChain(inner, outer)

	// Bounds compose: 6 + 6 = 12 pixels of reach on every side.
	got := chain.ExpandBounds(Rect{MinX: 10, MinY: 10, MaxX: 11, MaxY: 11})
	want := Rect{MinX: -2, MinY: -2, MaxX: 23, MaxY: 23}
	if got != want {
		t.Errorf("chain ExpandBounds = %+v, want %+v", got, want)
	}

	dst := gg.NewPixmap(20, 20)
	// Full-canvas bounds: every stage reads the whole pixmap so the nested
	// halo has room to spread.
	chain.Apply(src, dst, Rect{MinX: 0, MinY: 0, MaxX: 20, MaxY: 20})

	// Pixels beyond a single blur's reach (distance 3) still carry glow,
	// proving the second stage received the first stage's expanded bounds.
	if dst.Data()[(10*20+(10+3))*4+3] == 0 {
		t.Error("nested blur lost its outer halo (bounds not expanded per stage)")
	}

	// Equivalent to applying the filters one after another.
	manual := gg.NewPixmap(20, 20)
	tmp := gg.NewPixmap(20, 20)
	b := Rect{MinX: 0, MinY: 0, MaxX: 20, MaxY: 20}
	inner.Apply(src, tmp, b)
	outer.Apply(tmp, manual, b)
	if string(dst.Data()) != string(manual.Data()) {
		t.Error("chain output differs from sequential application")
	}
}

func TestGaussianFilterChainThreeStages(t *testing.T) {
	// Three stages exercise both scratch buffers and the alternation logic.
	src := gg.NewPixmap(24, 24)
	src.SetPixelPremul(12, 12, 255, 255, 255, 255)

	chain := NewFilterChain(
		NewGaussianBlur(1, 1),
		NewGaussianBlur(1, 1),
		NewGaussianBlur(1, 1),
	)
	dst := gg.NewPixmap(24, 24)
	full := Rect{MinX: 0, MinY: 0, MaxX: 24, MaxY: 24}
	chain.Apply(src, dst, full)

	// Manual replication with the exact per-stage expanded-bounds protocol.
	t1 := gg.NewPixmap(24, 24)
	t2 := gg.NewPixmap(24, 24)
	out := gg.NewPixmap(24, 24)
	b0 := full
	b1 := chain.filters[0].ExpandBounds(b0)
	b2 := chain.filters[1].ExpandBounds(b1)
	chain.filters[0].Apply(src, t1, b0)
	chain.filters[1].Apply(t1, t2, b1)
	chain.filters[2].Apply(t2, out, b2)

	if string(dst.Data()) != string(out.Data()) {
		t.Error("three-stage chain output differs from manual per-stage application")
	}
	// A single sigma-1 blur is zero beyond distance 3; composing three blurs
	// must carry nonzero glow at distance 4 (each stage hands its halo on).
	single := gg.NewPixmap(24, 24)
	src2 := gg.NewPixmap(24, 24)
	src2.SetPixelPremul(12, 12, 255, 255, 255, 255)
	NewGaussianBlur(1, 1).Apply(src2, single, full)
	if single.Data()[(12*24+(12+4))*4+3] != 0 {
		t.Error("sanity: single blur should be zero past its 3px reach")
	}
	if dst.Data()[(12*24+(12+4))*4+3] == 0 {
		t.Error("three-stage chain lost its compounded halo")
	}
}

func TestGaussianFilterChainNonZeroOrigin(t *testing.T) {
	// Regression: the old chain allocated origin-sized scratch buffers and
	// silently dropped writes for non-zero bounds origins.
	src := gg.NewPixmap(24, 24)
	src.SetPixelPremul(12, 12, 255, 0, 0, 255)

	chain := NewFilterChain(NewGaussianBlur(2, 2), NewGaussianBlur(1, 1))
	dst := gg.NewPixmap(24, 24)
	region := Rect{MinX: 6, MinY: 6, MaxX: 18, MaxY: 18}
	chain.Apply(src, dst, region)

	// A pixel inside the region but offset from the origin must be blurred.
	center := dst.Data()[(12*24+12)*4+3]
	if center == 0 || center == 255 {
		t.Errorf("non-zero-origin chain produced center alpha %d, want partial glow", center)
	}
	// Outside the processed region nothing was written.
	if dst.Data()[3] != 0 {
		t.Error("chain wrote outside the requested bounds")
	}
}

// ---------------------------------------------------------------------------
// CPU renderer, layer cache and dirty-bounds integration
// ---------------------------------------------------------------------------

func TestRendererApplyLayerFilterDirtyAndCache(t *testing.T) {
	r := NewRenderer(64, 64, WithCache(NewLayerCache(1)))
	defer r.Close()

	// Start from a clean dirty state.
	r.dirty.GetAndClear()
	r.tileGrid.ClearDirty()

	layer := gg.NewPixmap(64, 64)
	layer.SetPixelPremul(32, 32, 255, 255, 255, 255)
	output := gg.NewPixmap(64, 64)

	filter := NewGaussianBlur(6, 6)
	const layerHash, resultHash uint64 = 111, 222
	layerBounds := Rect{MinX: 0, MinY: 0, MaxX: 64, MaxY: 64}
	expanded := r.ApplyLayerFilter(layer, output, layerHash, resultHash,
		layerBounds, filter)

	// Expanded bounds drive both processing and damage tracking.
	wantBounds := filter.ExpandBounds(layerBounds)
	if expanded != wantBounds {
		t.Errorf("expanded = %+v, want %+v", expanded, wantBounds)
	}
	if r.DirtyTileCount() == 0 {
		t.Error("expanded filter region was not marked dirty")
	}
	if !r.cache.Contains(resultHash) {
		t.Error("filtered result was not stored in the layer cache")
	}

	// Halo pixel (outside the original 1x1 content) is present.
	if output.Data()[(32*64+(32+5))*4+3] == 0 {
		t.Error("filtered output is missing its glow")
	}
}

func TestRendererApplyLayerFilterUsesCachedSource(t *testing.T) {
	r := NewRenderer(32, 32, WithCache(NewLayerCache(1)))
	defer r.Close()

	cached := gg.NewPixmap(32, 32)
	cached.SetPixelPremul(16, 16, 180, 90, 30, 200)
	const layerHash uint64 = 4242
	r.cache.Put(layerHash, cached, 1)

	// layer is nil: the cache entry must serve as the source.
	output := gg.NewPixmap(32, 32)
	filter := NewGaussianBlur(2, 2)
	r.ApplyLayerFilter(nil, output, layerHash, 0,
		Rect{MinX: 0, MinY: 0, MaxX: 32, MaxY: 32}, filter)

	nonZero := false
	for i := 3; i < len(output.Data()); i += 4 {
		if output.Data()[i] != 0 {
			nonZero = true
			break
		}
	}
	if !nonZero {
		t.Error("cached layer source was not used by ApplyLayerFilter")
	}
	if r.cache.Contains(layerHash) == false {
		t.Error("source cache entry should remain available")
	}
}

func TestGaussianFilterType(t *testing.T) {
	f := NewGaussianBlur(1, 2)
	if f.FilterType() != FilterBlur {
		t.Errorf("FilterType = %v, want FilterBlur", f.FilterType())
	}
	var _ Filter = NewGaussianBlurSigma(1, 1)
	var _ Filter = NewGaussianBlurTaps(1, 1, 3, 3)
}

func TestGaussianScratchPoolReuseAndCap(t *testing.T) {
	// Same-sized repeats must be served from the pool with no stale data:
	// a second, different source must not inherit pixels from the first run.
	f := NewGaussianBlur(2, 2)
	bounds := Rect{MinX: 0, MinY: 0, MaxX: 16, MaxY: 16}

	first := gg.NewPixmap(16, 16)
	first.SetPixelPremul(2, 2, 255, 255, 255, 255)
	out1 := gg.NewPixmap(16, 16)
	f.Apply(first, out1, bounds)

	second := gg.NewPixmap(16, 16)
	second.SetPixelPremul(13, 13, 255, 0, 0, 255)
	out2 := gg.NewPixmap(16, 16)
	f.Apply(second, out2, bounds)

	// The region around the first source's point must be empty on run two.
	if out2.Data()[(2*16+2)*4+3] != 0 {
		t.Error("pooled scratch buffer carried stale pixels between Apply calls")
	}
	if out2.Data()[(2*16+3)*4+3] != 0 {
		t.Error("pooled scratch buffer carried stale halo between Apply calls")
	}

	// Buffers over the cap are released rather than pooled: exercising a
	// large Apply must not leave an oversized entry in the pool.
	big := gg.NewPixmap(72, 72) // 72*72*4*4 bytes float32 = 82,944 — under cap
	f.Apply(big, gg.NewPixmap(72, 72), Rect{MinX: 0, MinY: 0, MaxX: 72, MaxY: 72})
	if v := blurTempPool.Get(); v != nil {
		buf := v.(*blurTempBuffer)
		if cap(buf.data) > maxGaussianTempBytes {
			t.Error("oversized scratch buffer was retained in the pool")
		}
		blurTempPool.Put(buf) // restore pool state for other tests
	}
}

func BenchmarkGaussianBlur256(b *testing.B) {
	src := gg.NewPixmap(256, 256)
	src.SetPixelPremul(128, 128, 255, 200, 100, 255)
	dst := gg.NewPixmap(256, 256)
	f := NewGaussianBlur(5, 5)
	bounds := Rect{MinX: 0, MinY: 0, MaxX: 256, MaxY: 256}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		f.Apply(src, dst, bounds)
	}
}

func BenchmarkGaussianBlurSingleAxis(b *testing.B) {
	src := gg.NewPixmap(256, 256)
	src.SetPixelPremul(128, 128, 255, 200, 100, 255)
	dst := gg.NewPixmap(256, 256)
	f := NewGaussianBlur(8, 0)
	bounds := Rect{MinX: 0, MinY: 0, MaxX: 256, MaxY: 256}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		f.Apply(src, dst, bounds)
	}
}

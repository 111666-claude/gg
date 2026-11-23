package scene

import (
	"errors"
	"testing"

	"github.com/gogpu/gg"
)

// buildMiniMap builds the shared immutable sub-scene used across the tests:
// a blue rounded square with a red circle on top. It also returns an image
// registered inside the sub-scene for resource-remapping tests.
func buildMiniMap() (*Scene, *Image) {
	child := NewScene()
	child.Fill(FillNonZero, IdentityAffine(),
		SolidBrush(gg.RGBA{R: 0.1, G: 0.3, B: 0.9, A: 1}),
		NewRectShape(0, 0, 40, 40))
	child.Fill(FillNonZero, IdentityAffine(),
		SolidBrush(gg.RGBA{R: 0.9, G: 0.2, B: 0.2, A: 1}),
		NewCircleShape(20, 20, 12))

	img := NewImage(4, 4)
	img.Data = make([]byte, 4*4*4)
	for i := range img.Data {
		img.Data[i] = 255
	}
	child.DrawImage(img, TranslateAffine(2, 2))
	return child, img
}

// renderSceneCPU renders s through the CPU tile renderer onto a fresh pixmap.
func renderSceneCPU(t *testing.T, w, h int, s *Scene) *gg.Pixmap {
	t.Helper()
	gg.CloseAccelerator()
	target := gg.NewPixmap(w, h)
	r := NewRenderer(w, h)
	t.Cleanup(r.Close)
	if err := r.Render(target, s); err != nil {
		t.Fatalf("Render: %v", err)
	}
	return target
}

// TestInstance_RejectedInputs covers the explicit handling of empty
// instances, cycles, negative opacity and singular matrices.
func TestInstance_RejectedInputs(t *testing.T) {
	child, _ := buildMiniMap()
	host := NewScene()

	t.Run("nil child", func(t *testing.T) {
		if _, err := NewInstance(nil, IdentityAffine()); !errors.Is(err, ErrNilInstance) {
			t.Fatalf("NewInstance(nil) err = %v, want ErrNilInstance", err)
		}
		if err := host.AddInstance(nil); !errors.Is(err, ErrNilInstance) {
			t.Fatalf("AddInstance(nil) err = %v, want ErrNilInstance", err)
		}
	})

	t.Run("empty child", func(t *testing.T) {
		empty := NewScene()
		in, err := NewInstance(empty, IdentityAffine())
		if err != nil {
			t.Fatalf("NewInstance on empty scene: %v", err)
		}
		if err := host.AddInstance(in); !errors.Is(err, ErrEmptyInstance) {
			t.Fatalf("AddInstance(empty) err = %v, want ErrEmptyInstance", err)
		}
	})

	t.Run("negative opacity", func(t *testing.T) {
		in, err := NewInstance(child, IdentityAffine())
		if err != nil {
			t.Fatalf("NewInstance: %v", err)
		}
		in.Opacity = -0.5
		if err := host.AddInstance(in); !errors.Is(err, ErrNegativeOpacity) {
			t.Fatalf("AddInstance(negative opacity) err = %v, want ErrNegativeOpacity", err)
		}
	})

	t.Run("singular matrix", func(t *testing.T) {
		singular := NewAffine(1, 2, 5, 2, 4, 7) // det = 1*4 - 2*2 = 0
		if _, err := NewInstance(child, singular); !errors.Is(err, ErrSingularMatrix) {
			t.Fatalf("NewInstance(singular) err = %v, want ErrSingularMatrix", err)
		}
		in := &Instance{Child: child, Transform: singular, Opacity: 1, Blend: BlendNormal}
		if err := host.AddInstance(in); !errors.Is(err, ErrSingularMatrix) {
			t.Fatalf("AddInstance(singular) err = %v, want ErrSingularMatrix", err)
		}
	})

	t.Run("direct self cycle", func(t *testing.T) {
		loop := NewScene()
		loop.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.Red), NewRectShape(0, 0, 10, 10))
		in, err := NewInstance(loop, TranslateAffine(1, 1))
		if err != nil {
			t.Fatalf("NewInstance: %v", err)
		}
		if err := loop.AddInstance(in); !errors.Is(err, ErrInstanceCycle) {
			t.Fatalf("self-reference err = %v, want ErrInstanceCycle", err)
		}
	})

	t.Run("transitive cycle", func(t *testing.T) {
		a := NewScene()
		b := NewScene()
		a.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.Red), NewRectShape(0, 0, 10, 10))
		b.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.Blue), NewRectShape(0, 0, 10, 10))
		inB, _ := NewInstance(b, IdentityAffine())
		if err := a.AddInstance(inB); err != nil {
			t.Fatalf("a.AddInstance(b): %v", err)
		}
		inA, _ := NewInstance(a, IdentityAffine())
		// b → a → b closes a cycle.
		if err := b.AddInstance(inA); !errors.Is(err, ErrInstanceCycle) {
			t.Fatalf("transitive cycle err = %v, want ErrInstanceCycle", err)
		}
	})

	t.Run("depth limit", func(t *testing.T) {
		// A legal chain has MaxInstanceDepth nested placements. Building one
		// more must be rejected.
		root := NewScene()
		root.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.Red), NewRectShape(0, 0, 5, 5))
		parent := root
		var deepest *Scene
		for i := 0; i < MaxInstanceDepth; i++ {
			next := NewScene()
			next.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.Green), NewRectShape(0, 0, 5, 5))
			in, err := NewInstance(next, TranslateAffine(1, 1))
			if err != nil {
				t.Fatalf("chain NewInstance at %d: %v", i, err)
			}
			if err := parent.AddInstance(in); err != nil {
				t.Fatalf("chain AddInstance at depth %d: %v", i+1, err)
			}
			parent = next
			deepest = next
		}
		over := NewScene()
		over.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.Blue), NewRectShape(0, 0, 5, 5))
		in, err := NewInstance(over, TranslateAffine(1, 1))
		if err != nil {
			t.Fatalf("over-deep NewInstance: %v", err)
		}
		if err := deepest.AddInstance(in); !errors.Is(err, ErrInstanceDepthExceeded) {
			t.Fatalf("depth overflow err = %v, want ErrInstanceDepthExceeded", err)
		}

		// The legal chain itself validates and renders.
		if err := root.ValidateInstances(); err != nil {
			t.Fatalf("legal-depth chain validation: %v", err)
		}
	})
}

// TestInstance_ThreeLayerTransforms places one sub-scene three times with
// different transforms and checks each landing zone renders the child's
// content at its transformed position — including negative scale.
func TestInstance_ThreeLayerTransforms(t *testing.T) {
	child, _ := buildMiniMap()
	child.Freeze()

	host := NewScene()
	placements := []struct {
		name string
		t    Affine
		// probe point in host space expected to land on the blue square fill
		probeX, probeY int
	}{
		{"translate", TranslateAffine(60, 10), 70, 20},
		{"scale 2 + translate", TranslateAffine(10, 80).Multiply(ScaleAffine(2, 2)), 15, 85},
		// Mirror maps child x∈[0,40] to host x∈[110,150] (x' = 150 - x).
		{"mirror + translate", TranslateAffine(150, 10).Multiply(ScaleAffine(-1, 1)), 145, 25},
	}
	for _, p := range placements {
		in, err := NewInstance(child, p.t)
		if err != nil {
			t.Fatalf("%s: NewInstance: %v", p.name, err)
		}
		if err := host.AddInstance(in); err != nil {
			t.Fatalf("%s: AddInstance: %v", p.name, err)
		}
	}

	// Command ordering: TagInstance entries appear in attachment order.
	enc := host.Encoding()
	var instanceTags []Tag
	for _, tag := range enc.Tags() {
		if tag == TagInstance {
			instanceTags = append(instanceTags, tag)
		}
	}
	if len(instanceTags) != 3 {
		t.Fatalf("TagInstance count = %d, want 3", len(instanceTags))
	}
	if len(host.Instances()) != 3 {
		t.Fatalf("host instances = %d, want 3", len(host.Instances()))
	}

	target := renderSceneCPU(t, 220, 180, host)
	for _, p := range placements {
		c := target.GetPixel(p.probeX, p.probeY)
		if c.B < 0.5 {
			t.Errorf("%s: probe (%d,%d) B=%.2f, want blue child content", p.name, p.probeX, p.probeY, c.B)
		}
	}

	// A point outside every transformed child must stay transparent.
	if c := target.GetPixel(200, 160); c.A != 0 {
		t.Errorf("background pixel alpha = %.2f, want 0", c.A)
	}
}

// TestInstance_OpacityAndBlend verifies group opacity and non-default blend
// modes composite onto content drawn below the instance in the same host.
func TestInstance_OpacityAndBlend(t *testing.T) {
	child, _ := buildMiniMap()

	host := NewScene()
	// White background under the first (half-opacity) instance.
	host.Fill(FillNonZero, IdentityAffine(),
		SolidBrush(gg.RGBA{R: 1, G: 1, B: 1, A: 1}),
		NewRectShape(0, 0, 40, 40))

	half, err := NewInstance(child, TranslateAffine(0, 0))
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	half.Opacity = 0.5
	if err := host.AddInstance(half); err != nil {
		t.Fatalf("AddInstance opacity: %v", err)
	}

	mult, err := NewInstance(child, TranslateAffine(50, 0))
	if err != nil {
		t.Fatalf("NewInstance multiply: %v", err)
	}
	mult.Opacity = 1
	mult.Blend = BlendMultiply
	if err := host.AddInstance(mult); err != nil {
		t.Fatalf("AddInstance multiply: %v", err)
	}

	target := renderSceneCPU(t, 120, 60, host)

	// Half-opacity blue over white produces a tinted blend: the premultiplied
	// blue (R≈23) is scaled by 0.5, then white shows through. The straight
	// result keeps B≈0.95 while R rises from ~0.1 to ~0.55 — visibly lighter
	// than the fully opaque child, which is the compositing contract.
	// Probe a blue-fill corner outside the white image (2..6) and red circle.
	blended := target.GetPixel(30, 8)
	if blended.A == 0 {
		t.Fatal("half-opacity instance produced no pixels")
	}
	if blended.R < 0.4 || blended.R > 0.7 {
		t.Errorf("half-opacity blue over white R=%.2f, want ~0.55 (white showing through)", blended.R)
	}
	if blended.B < 0.9 {
		t.Errorf("half-opacity blue B=%.2f, want blue channel preserved (~0.95)", blended.B)
	}
	if blended.G < 0.5 || blended.G > 0.75 {
		t.Errorf("half-opacity blue G=%.2f, want ~0.65", blended.G)
	}

	// Multiply instance onto an empty background still draws source content.
	mulPx := target.GetPixel(80, 8)
	if mulPx.B < 0.5 {
		t.Errorf("multiply instance B=%.2f, want child blue content", mulPx.B)
	}

	// Zero opacity renders nothing and is accepted.
	host2 := NewScene()
	zero, _ := NewInstance(child, TranslateAffine(0, 0))
	zero.Opacity = 0
	if err := host2.AddInstance(zero); err != nil {
		t.Fatalf("zero-opacity AddInstance: %v", err)
	}
	target2 := renderSceneCPU(t, 40, 40, host2)
	if c := target2.GetPixel(10, 10); c.A != 0 {
		t.Errorf("zero-opacity instance painted alpha=%.2f, want 0", c.A)
	}
}

// TestInstance_Clip restricts an instance to a host-space clip shape.
func TestInstance_Clip(t *testing.T) {
	child, _ := buildMiniMap()

	host := NewScene()
	in, err := NewInstance(child, TranslateAffine(0, 0))
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	// Clip to a 10x10 host-space rect at the top-left corner.
	in.Clip = NewRectShape(0, 0, 10, 10)
	if err := host.AddInstance(in); err != nil {
		t.Fatalf("AddInstance clip: %v", err)
	}

	target := renderSceneCPU(t, 40, 40, host)
	if c := target.GetPixel(5, 5); c.B < 0.5 {
		t.Errorf("inside clip B=%.2f, want child content", c.B)
	}
	if c := target.GetPixel(30, 30); c.A != 0 {
		t.Errorf("outside clip alpha=%.2f, want 0", c.A)
	}

	// Bounds must be intersected with the clip.
	b := in.Bounds()
	if b.MaxX > 10 || b.MaxY > 10 {
		t.Errorf("clipped instance bounds = %+v, want capped at 10x10", b)
	}
}

// TestInstance_NestedPlaysBack verifies two-level nesting and that
// transforms compose through the hierarchy.
func TestInstance_NestedPlaysBack(t *testing.T) {
	// Use a plain-fill leaf (no image) so every probe is unambiguous.
	leaf := NewScene()
	leaf.Fill(FillNonZero, IdentityAffine(),
		SolidBrush(gg.RGBA{R: 0.1, G: 0.3, B: 0.9, A: 1}),
		NewRectShape(0, 0, 40, 40))
	leaf.Freeze()

	mid := NewScene()
	midIn, err := NewInstance(leaf, TranslateAffine(10, 10))
	if err != nil {
		t.Fatalf("mid NewInstance: %v", err)
	}
	if err := mid.AddInstance(midIn); err != nil {
		t.Fatalf("mid AddInstance: %v", err)
	}
	mid.Freeze()

	root := NewScene()
	rootIn, err := NewInstance(mid, TranslateAffine(20, 30))
	if err != nil {
		t.Fatalf("root NewInstance: %v", err)
	}
	if err := root.AddInstance(rootIn); err != nil {
		t.Fatalf("root AddInstance: %v", err)
	}

	if err := root.ValidateInstances(); err != nil {
		t.Fatalf("ValidateInstances: %v", err)
	}

	// Composed translation: leaf rect [0,40] lands at host [30,70] x [40,80].
	target := renderSceneCPU(t, 120, 120, root)
	c := target.GetPixel(34, 44)
	if c.B < 0.5 {
		t.Errorf("nested instance B=%.2f at (34,44), want leaf blue content", c.B)
	}
	if c := target.GetPixel(5, 5); c.A != 0 {
		t.Errorf("untransformed corner alpha=%.2f, want 0", c.A)
	}
	// Leaf content must not appear at its raw local coordinates.
	if c := target.GetPixel(4, 4); c.A != 0 {
		t.Errorf("leaf content leaked to raw local coords, alpha=%.2f at (4,4)", c.A)
	}
	// The mid-only translation (10,10) must not be the final placement either.
	if c := target.GetPixel(14, 14); c.A != 0 {
		t.Errorf("leaf content rendered at mid-only offset, alpha=%.2f at (14,14)", c.A)
	}
}

// TestInstance_MovedChildInvalidation covers version/Bounds/cache
// invalidation when the referenced sub-scene changes after attachment.
func TestInstance_MovedChildInvalidation(t *testing.T) {
	child, _ := buildMiniMap()
	host := NewScene()
	in, err := NewInstance(child, TranslateAffine(0, 0))
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	if err := host.AddInstance(in); err != nil {
		t.Fatalf("AddInstance: %v", err)
	}
	if err := host.ValidateInstances(); err != nil {
		t.Fatalf("ValidateInstances before edit: %v", err)
	}
	tokenBefore := host.ContentVersion()

	// Mutate the child (it is not frozen): host render must now reject it.
	child.Fill(FillNonZero, TranslateAffine(100, 100),
		SolidBrush(gg.Green), NewRectShape(0, 0, 8, 8))
	if err := host.ValidateInstances(); !errors.Is(err, ErrInstanceMoved) {
		t.Fatalf("ValidateInstances after edit err = %v, want ErrInstanceMoved", err)
	}

	target := gg.NewPixmap(160, 160)
	r := NewRenderer(160, 160)
	defer r.Close()
	if err := r.Render(target, host); !errors.Is(err, ErrInstanceMoved) {
		t.Fatalf("Render after child edit err = %v, want ErrInstanceMoved", err)
	}
	if tokenBefore == host.ContentVersion() {
		t.Error("ContentVersion did not change after child mutation")
	}
}

// TestInstance_CacheKeyInvalidation demonstrates the cache-hit/invalidate
// contract: a key built from the encoding hash and ContentVersion stays
// valid while nothing changes, and breaks on any host or child edit.
func TestInstance_CacheKeyInvalidation(t *testing.T) {
	cache := NewLayerCache(4)
	child, _ := buildMiniMap()
	host := NewScene()
	in, err := NewInstance(child, ScaleAffine(2, 2))
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	if err := host.AddInstance(in); err != nil {
		t.Fatalf("AddInstance: %v", err)
	}

	cacheKey := func(s *Scene) uint64 {
		h := s.Encoding().Hash()
		return (h ^ s.ContentVersion()) * 1099511628211
	}
	key := cacheKey(host)
	cache.Put(key, gg.NewPixmap(8, 8), host.Version())
	if _, ok := cache.Get(key); !ok {
		t.Fatal("expected cache hit for unchanged instance scene")
	}

	// Editing the host (new placement) changes the key.
	in2, _ := NewInstance(child, TranslateAffine(80, 80))
	if err := host.AddInstance(in2); err != nil {
		t.Fatalf("second AddInstance: %v", err)
	}
	if cache.Contains(cacheKey(host)) {
		t.Error("stale cache entry survived host modification")
	}

	// Editing the child also invalidates via ContentVersion even though the
	// host encoding bytes are unchanged.
	child.Fill(FillNonZero, TranslateAffine(200, 200),
		SolidBrush(gg.Black), NewRectShape(0, 0, 4, 4))
	if cache.Contains(cacheKey(host)) {
		t.Error("stale cache entry survived child modification")
	}
}

// TestInstance_FreezePanics verifies frozen sub-scenes reject mutation and
// that hosts can still attach instances of them.
func TestInstance_FreezePanics(t *testing.T) {
	child, _ := buildMiniMap()
	child.Freeze()
	if !child.Frozen() {
		t.Fatal("Frozen() = false after Freeze")
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Error("Fill on frozen scene did not panic")
			}
		}()
		child.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.Red), NewRectShape(0, 0, 4, 4))
	}()
	func() {
		defer func() {
			if recover() == nil {
				t.Error("Reset on frozen scene did not panic")
			}
		}()
		child.Reset()
	}()

	host := NewScene()
	in, err := NewInstance(child, TranslateAffine(0, 0))
	if err != nil {
		t.Fatalf("NewInstance frozen child: %v", err)
	}
	if err := host.AddInstance(in); err != nil {
		t.Fatalf("AddInstance frozen child: %v", err)
	}
	child.Unfreeze()
	child.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.Red), NewRectShape(0, 0, 2, 2))
}

// TestInstance_ResourceRemapping verifies that an image registered only in
// the child scene is reachable from the host registry and renders through
// the instance without copying pixel data.
func TestInstance_ResourceRemapping(t *testing.T) {
	child, img := buildMiniMap()
	host := NewScene()
	in, err := NewInstance(child, TranslateAffine(10, 10))
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	if err := host.AddInstance(in); err != nil {
		t.Fatalf("AddInstance: %v", err)
	}

	// The host registry references the exact same *Image (no pixel copy).
	found := false
	for _, registered := range host.Images() {
		if registered == img {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("child image not merged into host registry by pointer")
	}

	target := renderSceneCPU(t, 80, 80, host)
	// The child image is drawn at child (2,2) + instance translation (10,10).
	c := target.GetPixel(13, 13)
	if c.A == 0 {
		t.Error("instanced image did not render: pixel transparent at (13,13)")
	}
}

// TestInstance_NoCommandOrPixelCopy ensures the host encoding stays tiny
// regardless of the child's command count and re-using a child twice shares
// the same resource pointers.
func TestInstance_NoCommandOrPixelCopy(t *testing.T) {
	child, img := buildMiniMap()
	childTags := len(child.Encoding().Tags())
	if childTags < 10 {
		t.Fatalf("test setup: child should contain many tags, got %d", childTags)
	}

	host := NewScene()
	for i := 0; i < 3; i++ {
		in, err := NewInstance(child, TranslateAffine(float32(i*50), 0))
		if err != nil {
			t.Fatalf("NewInstance %d: %v", i, err)
		}
		if err := host.AddInstance(in); err != nil {
			t.Fatalf("AddInstance %d: %v", i, err)
		}
	}

	// Three placements must add only three TagInstance commands, not three
	// copies of the child's command stream.
	count := 0
	for _, tag := range host.Encoding().Tags() {
		if tag == TagInstance {
			count++
		}
	}
	if count != 3 {
		t.Fatalf("host TagInstance count = %d, want 3", count)
	}
	if len(host.Encoding().Tags()) > 3+2 { // tags + possible clip markers
		t.Errorf("host encoding has %d tags, expected ~3 (no command copies)", len(host.Encoding().Tags()))
	}
	// Only one image pointer despite three placements.
	imageCount := 0
	for _, registered := range host.Images() {
		if registered == img {
			imageCount++
		}
	}
	if imageCount != 1 {
		t.Errorf("child image registered %d times in host, want exactly 1 shared pointer", imageCount)
	}
}

// TestInstance_AppendRemapsIDs verifies the legacy Scene.Append path adopts
// instance references and remaps their TagInstance ids, while the encoding
// merge stays byte-compatible for non-instance scenes.
func TestInstance_AppendRemapsIDs(t *testing.T) {
	leaf, _ := buildMiniMap()

	mid := NewScene()
	midIn, _ := NewInstance(leaf, TranslateAffine(5, 5))
	if err := mid.AddInstance(midIn); err != nil {
		t.Fatalf("mid AddInstance: %v", err)
	}

	host := NewScene()
	host.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.White), NewRectShape(0, 0, 200, 200))
	host.Append(mid)

	if err := host.ValidateInstances(); err != nil {
		t.Fatalf("ValidateInstances after Append: %v", err)
	}
	if len(host.Instances()) != 1 {
		t.Fatalf("host adopted %d instances, want 1", len(host.Instances()))
	}

	target := renderSceneCPU(t, 120, 120, host)
	c := target.GetPixel(10, 10)
	if c.B < 0.5 {
		t.Errorf("appended instance B=%.2f at (10,10), want leaf content", c.B)
	}

	// AppendWithTranslation path: ids remap and instance translation shifts.
	host2 := NewScene()
	host2.AppendWithTranslation(mid, 40, 40)
	if err := host2.ValidateInstances(); err != nil {
		t.Fatalf("ValidateInstances after AppendWithTranslation: %v", err)
	}
	target2 := renderSceneCPU(t, 120, 120, host2)
	c2 := target2.GetPixel(50, 50)
	if c2.B < 0.5 {
		t.Errorf("translated appended instance B=%.2f at (50,50), want leaf content", c2.B)
	}
}

// TestInstance_BoundsVersionBehavior checks host Bounds include transformed
// instance content and every placement bumps the host version.
func TestInstance_BoundsVersion(t *testing.T) {
	child, _ := buildMiniMap()
	host := NewScene()
	v0 := host.Version()

	in, _ := NewInstance(child, TranslateAffine(100, 200))
	if err := host.AddInstance(in); err != nil {
		t.Fatalf("AddInstance: %v", err)
	}
	if host.Version() == v0 {
		t.Error("AddInstance did not bump host version")
	}
	b := host.Bounds()
	if b.MinX < 100 || b.MinY < 200 || b.MaxX < 140 || b.MaxY < 240 {
		t.Errorf("host bounds %+v do not contain transformed child [100,200]-[140,240]", b)
	}
	if in.Bounds().IsEmpty() {
		t.Error("instance Bounds empty")
	}
}

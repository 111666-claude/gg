package scene

import (
	"testing"

	"github.com/gogpu/gg"
)

// TestInstance_RenderDirtyAndReplay verifies that incremental dirty
// rendering and repeated full renders of an instance scene produce the same
// pixels, and that rendering does not mutate host/child state.
func TestInstance_RenderDirtyAndReplay(t *testing.T) {
	gg.CloseAccelerator()
	child, _ := buildMiniMap()
	child.Freeze()

	host := NewScene()
	for i, tr := range []Affine{
		TranslateAffine(10, 10),
		TranslateAffine(80, 10),
		TranslateAffine(10, 80),
	} {
		in, err := NewInstance(child, tr)
		if err != nil {
			t.Fatalf("instance %d: %v", i, err)
		}
		if err := host.AddInstance(in); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}

	const w, h = 160, 140
	r := NewRenderer(w, h)
	defer r.Close()

	full := gg.NewPixmap(w, h)
	if err := r.Render(full, host); err != nil {
		t.Fatalf("full Render: %v", err)
	}

	// A second full render must be pixel-identical (deterministic replay).
	full2 := gg.NewPixmap(w, h)
	if err := r.Render(full2, host); err != nil {
		t.Fatalf("second Render: %v", err)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if full.GetPixel(x, y) != full2.GetPixel(x, y) {
				t.Fatalf("non-deterministic replay at (%d,%d): %v vs %v",
					x, y, full.GetPixel(x, y), full2.GetPixel(x, y))
			}
		}
	}

	// Dirty render of a fresh target matches the full render in a dirty tile.
	r.MarkAllDirty()
	dirty := gg.NewPixmap(w, h)
	if err := r.RenderDirty(dirty, host, nil); err != nil {
		t.Fatalf("RenderDirty: %v", err)
	}
	if c := dirty.GetPixel(15, 15); c.B < 0.5 {
		t.Errorf("dirty render missing instance content at (15,15): %v", c)
	}

	// Rendering must not mutate the registries or versions.
	if host.Version() == 0 {
		t.Error("host version unexpectedly zero")
	}
	vBefore := host.Version()
	_ = r.Render(gg.NewPixmap(w, h), host)
	if host.Version() != vBefore {
		t.Error("rendering mutated the host version")
	}
	if len(host.Images()) != len(child.Images()) {
		t.Errorf("host images = %d, want shared child count %d",
			len(host.Images()), len(child.Images()))
	}
}

// TestInstance_DiamondSharing verifies one sub-scene referenced by two hosts
// (a legal DAG) merges resources once per host and renders in both.
func TestInstance_DiamondSharing(t *testing.T) {
	gg.CloseAccelerator()
	shared, img := buildMiniMap()
	shared.Freeze()

	hostA := NewScene()
	inA, err := NewInstance(shared, TranslateAffine(0, 0))
	if err != nil {
		t.Fatalf("A NewInstance: %v", err)
	}
	if err := hostA.AddInstance(inA); err != nil {
		t.Fatalf("A AddInstance: %v", err)
	}

	hostB := NewScene()
	inB, err := NewInstance(shared, TranslateAffine(50, 50))
	if err != nil {
		t.Fatalf("B NewInstance: %v", err)
	}
	if err := hostB.AddInstance(inB); err != nil {
		t.Fatalf("B AddInstance: %v", err)
	}

	if err := hostA.ValidateInstances(); err != nil {
		t.Errorf("hostA validation: %v", err)
	}
	if err := hostB.ValidateInstances(); err != nil {
		t.Errorf("hostB validation: %v", err)
	}

	// Each host registry holds the shared image exactly once.
	for name, h := range map[string]*Scene{"A": hostA, "B": hostB} {
		count := 0
		for _, registered := range h.Images() {
			if registered == img {
				count++
			}
		}
		if count != 1 {
			t.Errorf("host%s registered shared image %d times, want 1", name, count)
		}
	}

	target := renderSceneCPU(t, 120, 120, hostA)
	if c := target.GetPixel(5, 5); c.B < 0.5 {
		t.Errorf("hostA content missing: %v", c)
	}
	targetB := renderSceneCPU(t, 120, 120, hostB)
	if c := targetB.GetPixel(55, 55); c.B < 0.5 {
		t.Errorf("hostB content missing: %v", c)
	}
	// The blue fill lives at child (0..40); after the (50,50) placement the
	// raw local corner must stay empty. Probe away from the child image at
	// (52..56).
	if c := targetB.GetPixel(45, 48); c.A != 0 {
		t.Errorf("hostB content at untransformed corner: alpha=%.2f", c.A)
	}
}

// TestInstance_EncodingStaysCompact asserts the TagInstance stream layout:
// one drawData id plus eight pathData floats per placement.
func TestInstance_EncodingStaysCompact(t *testing.T) {
	child, _ := buildMiniMap()
	host := NewScene()
	in, err := NewInstance(child, TranslateAffine(7, 9))
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	if err := host.AddInstance(in); err != nil {
		t.Fatalf("AddInstance: %v", err)
	}
	enc := host.Encoding()
	if len(enc.Tags()) != 1 || enc.Tags()[0] != TagInstance {
		t.Fatalf("tags = %v, want single TagInstance", enc.Tags())
	}
	dec := NewDecoder(enc)
	if !dec.Next() || dec.Tag() != TagInstance {
		t.Fatal("decoder did not yield TagInstance")
	}
	id, transform, opacity, blend := dec.Instance()
	if id != in.ID() {
		t.Errorf("decoded id = %d, want %d", id, in.ID())
	}
	if transform.C != 7 || transform.F != 9 {
		t.Errorf("decoded transform C/F = %v/%v, want 7/9", transform.C, transform.F)
	}
	if opacity != 1 || blend != BlendNormal {
		t.Errorf("decoded opacity/blend = %v/%v, want 1/Normal", opacity, blend)
	}
}

// TestInstance_DamageTracking verifies instance placements participate in
// per-command TaggedBounds so the damage pipeline detects their region.
func TestInstance_DamageTracking(t *testing.T) {
	gg.CloseAccelerator()
	child, _ := buildMiniMap()
	host := NewScene()
	in, err := NewInstance(child, TranslateAffine(100, 200))
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	if err := host.AddInstance(in); err != nil {
		t.Fatalf("AddInstance: %v", err)
	}

	tagged := host.TaggedBounds()
	if len(tagged) != 1 {
		t.Fatalf("TaggedBounds count = %d, want 1 for the instance", len(tagged))
	}
	r := tagged[0].Rect
	if r.Min.X < 100 || r.Min.Y < 200 || r.Max.X < 140 || r.Max.Y < 240 {
		t.Errorf("damage rect %v does not cover transformed child [100,200]-[140,240]", r)
	}

	const w, h = 300, 300
	renderer := NewRenderer(w, h)
	defer renderer.Close()
	tracker := NewDamageTracker()
	target := gg.NewPixmap(w, h)
	damage, err := renderer.RenderWithDamage(target, host, tracker)
	if err != nil {
		t.Fatalf("RenderWithDamage: %v", err)
	}
	if damage.Empty() {
		t.Error("first-frame damage rect empty, want full scene coverage")
	}
	if c := target.GetPixel(105, 205); c.B < 0.5 {
		t.Errorf("damage render missing instance content: %v", c)
	}
}

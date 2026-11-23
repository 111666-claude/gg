package scene_test

import (
	"errors"
	"testing"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/scene"
)

// TestGPUInstance_PlaysBack renders an instanced scene through the
// GPUSceneRenderer (with a registered tracker accelerator that falls back to
// CPU per shape) and verifies the child content lands at the transformed
// placement without being copied into the host encoding.
func TestGPUInstance_PlaysBack(t *testing.T) {
	tracker := &gpuTracker{}
	if err := gg.RegisterAccelerator(tracker); err != nil {
		t.Fatalf("RegisterAccelerator: %v", err)
	}
	defer gg.CloseAccelerator()

	child := scene.NewScene()
	child.Fill(scene.FillNonZero, scene.IdentityAffine(),
		scene.SolidBrush(gg.RGBA{R: 0.1, G: 0.3, B: 0.9, A: 1}),
		scene.NewRectShape(0, 0, 40, 40))

	host := scene.NewScene()
	in, err := scene.NewInstance(child, scene.TranslateAffine(60, 20))
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	if err := host.AddInstance(in); err != nil {
		t.Fatalf("AddInstance: %v", err)
	}

	target := gg.NewPixmap(160, 120)
	dc := gg.NewContextForPixmap(target)
	defer func() { _ = dc.Close() }()

	gpuR := scene.NewGPUSceneRenderer(dc)
	if err := gpuR.RenderScene(host); err != nil {
		t.Fatalf("RenderScene: %v", err)
	}
	if err := dc.FlushGPU(); err != nil {
		t.Fatalf("FlushGPU: %v", err)
	}

	c := target.GetPixel(70, 30)
	if c.B < 0.5 {
		t.Errorf("instanced fill B=%.2f at (70,30), want child blue", c.B)
	}
	if c := target.GetPixel(5, 5); c.A != 0 {
		t.Errorf("content outside placement alpha=%.2f, want 0", c.A)
	}
}

// TestGPUInstance_NestedAndMoved covers nested playback and the explicit
// ErrInstanceMoved propagation through the GPU renderer.
func TestGPUInstance_NestedAndMoved(t *testing.T) {
	gg.CloseAccelerator()

	leaf := scene.NewScene()
	leaf.Fill(scene.FillNonZero, scene.IdentityAffine(),
		scene.SolidBrush(gg.RGBA{R: 0.1, G: 0.3, B: 0.9, A: 1}),
		scene.NewRectShape(0, 0, 20, 20))

	mid := scene.NewScene()
	midIn, err := scene.NewInstance(leaf, scene.TranslateAffine(5, 5))
	if err != nil {
		t.Fatalf("mid NewInstance: %v", err)
	}
	if err := mid.AddInstance(midIn); err != nil {
		t.Fatalf("mid AddInstance: %v", err)
	}

	root := scene.NewScene()
	rootIn, err := scene.NewInstance(mid, scene.TranslateAffine(30, 40))
	if err != nil {
		t.Fatalf("root NewInstance: %v", err)
	}
	if err := root.AddInstance(rootIn); err != nil {
		t.Fatalf("root AddInstance: %v", err)
	}

	target := gg.NewPixmap(120, 120)
	dc := gg.NewContextForPixmap(target)
	defer func() { _ = dc.Close() }()
	gpuR := scene.NewGPUSceneRenderer(dc)
	if err := gpuR.RenderScene(root); err != nil {
		t.Fatalf("nested RenderScene: %v", err)
	}
	if c := target.GetPixel(40, 50); c.B < 0.5 {
		t.Errorf("nested instance B=%.2f at (40,50), want leaf blue", c.B)
	}

	// Mutating the leaf after attachment must surface as ErrInstanceMoved.
	leaf.Fill(scene.FillNonZero, scene.TranslateAffine(90, 90),
		scene.SolidBrush(gg.Green), scene.NewRectShape(0, 0, 4, 4))
	err = gpuR.RenderScene(root)
	if !errors.Is(err, scene.ErrInstanceMoved) {
		t.Fatalf("RenderScene after child edit err = %v, want ErrInstanceMoved", err)
	}
}

// TestGPUInstance_RejectedAtConstruction verifies the structural errors are
// returned before any GPU work begins.
func TestGPUInstance_RejectedAtConstruction(t *testing.T) {
	gg.CloseAccelerator()

	child := scene.NewScene()
	child.Fill(scene.FillNonZero, scene.IdentityAffine(),
		scene.SolidBrush(gg.Red), scene.NewRectShape(0, 0, 10, 10))
	host := scene.NewScene()

	if _, err := scene.NewInstance(child, scene.NewAffine(1, 2, 0, 2, 4, 0)); !errors.Is(err, scene.ErrSingularMatrix) {
		t.Fatalf("singular err = %v, want ErrSingularMatrix", err)
	}

	bad, err := scene.NewInstance(child, scene.IdentityAffine())
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	bad.Opacity = -1
	if err := host.AddInstance(bad); !errors.Is(err, scene.ErrNegativeOpacity) {
		t.Fatalf("negative opacity err = %v, want ErrNegativeOpacity", err)
	}
}

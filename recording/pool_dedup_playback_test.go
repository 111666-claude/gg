package recording_test

import (
	"image"
	"image/color"
	"testing"

	"github.com/gogpu/gg/recording"
	"github.com/gogpu/gg/recording/backends/raster"
)

// recordUIScene records a UI-like scene that reuses the same path, brush,
// and semi-transparent image hundreds of times.
func recordUIScene(dedup bool) *recording.Recording {
	rec := recording.NewRecorder(200, 200)
	rec.EnableResourceDedup(dedup)

	// One shared semi-transparent image, drawn many times.
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 255, G: 200, B: 0, A: 128})
		}
	}

	rec.SetRGB(0.2, 0.4, 0.8)
	for i := 0; i < 300; i++ {
		// Positions cycle through a 10x3 grid, so the same rounded-rect
		// geometry (and brush, and image) repeats many times.
		x := float64((i % 10) * 18)
		y := float64((i / 10 % 3) * 60)
		rec.DrawRoundedRectangle(x, y, 8, 8, 2)
		rec.Fill()
		if i%3 == 0 {
			rec.DrawImage(img, int(x), int(y))
		}
	}
	return rec.FinishRecording()
}

func playbackToImage(t *testing.T, rec *recording.Recording) image.Image {
	t.Helper()
	backend := raster.NewBackend()
	if err := rec.Playback(backend); err != nil {
		t.Fatalf("Playback: %v", err)
	}
	return backend.Image()
}

func TestRecording_DedupPlaybackPixelsIdentical(t *testing.T) {
	plain := recordUIScene(false)
	dedup := recordUIScene(true)

	// The dedup recording must reference far fewer resources.
	pr, dr := plain.Resources(), dedup.Resources()
	t.Logf("pool sizes: plain=%d paths/%d brushes/%d images, dedup=%d/%d/%d",
		pr.PathCount(), pr.BrushCount(), pr.ImageCount(),
		dr.PathCount(), dr.BrushCount(), dr.ImageCount())
	if dr.PathCount() >= pr.PathCount() {
		t.Errorf("dedup PathCount %d >= plain %d", dr.PathCount(), pr.PathCount())
	}
	if dr.ImageCount() != 1 {
		t.Errorf("dedup ImageCount = %d, want 1 (same image drawn 100x)", dr.ImageCount())
	}
	if pr.ImageCount() != 100 {
		t.Errorf("plain ImageCount = %d, want 100", pr.ImageCount())
	}

	// Playback of both recordings must produce identical pixels.
	imgPlain := playbackToImage(t, plain)
	imgDedup := playbackToImage(t, dedup)

	bounds := imgPlain.Bounds()
	if bounds != imgDedup.Bounds() {
		t.Fatalf("bounds differ: %v vs %v", bounds, imgDedup.Bounds())
	}
	diff := 0
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r1, g1, b1, a1 := imgPlain.At(x, y).RGBA()
			r2, g2, b2, a2 := imgDedup.At(x, y).RGBA()
			if r1 != r2 || g1 != g2 || b1 != b2 || a1 != a2 {
				if diff == 0 {
					t.Logf("first diff at (%d,%d): plain=%x%x%x%x dedup=%x%x%x%x",
						x, y, r1, g1, b1, a1, r2, g2, b2, a2)
				}
				diff++
			}
		}
	}
	if diff != 0 {
		t.Errorf("playback pixels differ at %d locations", diff)
	}
}

func TestRecording_DedupStatsViaRecorder(t *testing.T) {
	rec := recording.NewRecorder(64, 64)
	rec.EnableResourceDedup(true)
	rec.SetRGB(1, 0, 0)
	for i := 0; i < 50; i++ {
		rec.DrawCircle(32, 32, 10)
		rec.Fill()
	}
	stats := rec.ResourceDedupStats()
	if !stats.Enabled {
		t.Error("stats.Enabled = false, want true")
	}
	if stats.Hits == 0 {
		t.Error("stats.Hits = 0 after 50 identical fills, want > 0")
	}
	r := rec.FinishRecording()
	if r.Resources().PathCount() != 1 {
		t.Errorf("PathCount = %d, want 1", r.Resources().PathCount())
	}
}

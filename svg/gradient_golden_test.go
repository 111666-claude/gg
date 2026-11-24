package svg

// Golden image test for SVG gradients.
//
// Fixtures live in testdata/gradient/*.svg and their rendered PNGs in
// testdata/golden/gradient/*.png. Re-generate the PNGs after an intentional
// rendering change with:
//
//	UPDATE_GRADIENT_GOLDEN=1 go test ./svg -run TestGradientGoldenImages
//
// Correctness against an independently sampled reference lives in
// gradient_reference_test.go; this test guards against accidental output
// drift and provides inspectable golden artifacts.

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestGradientGoldenImages(t *testing.T) {
	fixtures, err := filepath.Glob(filepath.Join("testdata", "gradient", "*.svg"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fixtures) == 0 {
		t.Fatal("no gradient fixtures found")
	}
	update := os.Getenv("UPDATE_GRADIENT_GOLDEN") == "1"
	goldenDir := filepath.Join("testdata", "golden", "gradient")
	if update {
		if err := os.MkdirAll(goldenDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	for _, fx := range fixtures {
		name := filepath.Base(fx)
		pngName := name[:len(name)-len(".svg")] + ".png"
		goldenPath := filepath.Join(goldenDir, pngName)

		data, err := os.ReadFile(fx)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		doc, perr := Parse(data)
		if perr != nil {
			t.Fatalf("golden fixture %s must be valid: %v", name, perr)
		}
		w := int(doc.ViewBox.Width)
		h := int(doc.ViewBox.Height)
		got := doc.Render(w, h)

		if update {
			f, err := os.Create(goldenPath)
			if err != nil {
				t.Fatalf("create golden %s: %v", pngName, err)
			}
			if err := png.Encode(f, got); err != nil {
				t.Fatalf("encode %s: %v", pngName, err)
			}
			if cerr := f.Close(); cerr != nil {
				t.Fatal(cerr)
			}
			t.Logf("wrote %s", goldenPath)
			continue
		}

		goldenImg := loadGoldenPNG(t, goldenPath)
		goldenRGBA := imageToRGBA(goldenImg)
		result := compareRGBA(goldenRGBA, got)
		t.Logf("%s: maxDiff=%d diffPixels=%d", pngName, result.maxDiff, result.diffCount)
		if result.maxDiff > 4 {
			t.Errorf("%s golden drift: max channel diff %d > 4", pngName, result.maxDiff)
		}
	}
}

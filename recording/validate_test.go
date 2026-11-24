package recording

import (
	"image"
	"image/color"
	"math"
	"strings"
	"testing"
)

func newSolidTestImage(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	return img
}

func TestRecordingValidateAcceptsGoodRecording(t *testing.T) {
	rec := NewRecorder(100, 100)
	rec.DrawImage(newSolidTestImage(8, 8), 5, 5)
	if err := rec.FinishRecording().Validate(); err != nil {
		t.Fatalf("valid recording rejected: %v", err)
	}
}

func TestRecordingValidateMissingImage(t *testing.T) {
	rec := NewRecorder(100, 100)
	// Hand-craft a command whose image ref resolves to nothing, simulating a
	// corrupted or tampered recording.
	rec.commands = append(rec.commands, DrawImageCommand{
		Image:   ImageRef(99),
		SrcRect: NewRect(0, 0, 4, 4),
		DstRect: NewRect(0, 0, 4, 4),
		Options: DefaultImageOptions(),
	})
	err := rec.FinishRecording().Validate()
	if err == nil || !strings.Contains(err.Error(), "missing image") {
		t.Fatalf("expected missing-image error, got %v", err)
	}
}

func TestRecordingValidateEmptyImageBounds(t *testing.T) {
	rec := NewRecorder(100, 100)
	ref := rec.resources.AddImage(image.NewRGBA(image.Rect(0, 0, 0, 0)))
	rec.commands = append(rec.commands, DrawImageCommand{
		Image:   ref,
		DstRect: NewRect(0, 0, 4, 4),
		Options: DefaultImageOptions(),
	})
	if err := rec.FinishRecording().Validate(); err == nil {
		t.Fatal("expected empty-bounds error")
	}
}

func TestRecordingValidateBadRects(t *testing.T) {
	rec := NewRecorder(100, 100)
	ref := rec.resources.AddImage(newSolidTestImage(8, 8))

	cases := []struct {
		name string
		cmd  DrawImageCommand
		want string
	}{
		{
			name: "source outside image",
			cmd: DrawImageCommand{
				Image: ref, SrcRect: NewRect(100, 100, 10, 10),
				DstRect: NewRect(0, 0, 10, 10), Options: DefaultImageOptions(),
			},
			want: "outside image bounds",
		},
		{
			name: "empty destination",
			cmd: DrawImageCommand{
				Image: ref, SrcRect: NewRect(0, 0, 4, 4),
				DstRect: NewRect(0, 0, 0, 4), Options: DefaultImageOptions(),
			},
			want: "empty destination",
		},
		{
			name: "non-finite destination",
			cmd: DrawImageCommand{
				Image: ref, DstRect: Rect{MinX: 0, MinY: 0, MaxX: math.NaN(), MaxY: 4},
				Options: DefaultImageOptions(),
			},
			want: "non-finite destination",
		},
		{
			name: "non-finite alpha",
			cmd: DrawImageCommand{
				Image: ref, DstRect: NewRect(0, 0, 4, 4),
				Options: ImageOptions{Interpolation: InterpolationBilinear, Alpha: math.Inf(1)},
			},
			want: "non-finite alpha",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRecorder(100, 100)
			ref := r.resources.AddImage(newSolidTestImage(8, 8))
			tc.cmd.Image = ref
			r.commands = append(r.commands, tc.cmd)
			err := r.FinishRecording().Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestRecordingValidateOversizedImage(t *testing.T) {
	rec := NewRecorder(100, 100)
	big := image.NewRGBA(image.Rect(0, 0, MaxImageDimension+1, 4))
	ref := rec.resources.AddImage(big)
	rec.commands = append(rec.commands, DrawImageCommand{
		Image: ref, DstRect: NewRect(0, 0, 4, 4), Options: DefaultImageOptions(),
	})
	if err := rec.FinishRecording().Validate(); err == nil {
		t.Fatal("expected oversized-image error")
	}
}

func TestPlaybackRejectsInvalidRecording(t *testing.T) {
	rec := NewRecorder(100, 100)
	rec.commands = append(rec.commands, DrawImageCommand{
		Image:   ImageRef(7),
		DstRect: NewRect(0, 0, 4, 4),
		Options: DefaultImageOptions(),
	})
	backend := &imageTrackingBackend{}
	err := rec.FinishRecording().Playback(backend)
	if err == nil {
		t.Fatal("Playback must reject an invalid recording")
	}
	if backend.beginCalled {
		t.Error("backend.Begin must not be called when preflight fails")
	}
	if backend.imageDrawn {
		t.Error("backend.DrawImage must not be called when preflight fails")
	}
}

func TestPlaybackAcceptsValidImage(t *testing.T) {
	rec := NewRecorder(100, 100)
	rec.DrawImage(newSolidTestImage(8, 8), 10, 10)
	backend := &imageTrackingBackend{}
	if err := rec.FinishRecording().Playback(backend); err != nil {
		t.Fatalf("valid playback failed: %v", err)
	}
	if !backend.imageDrawn {
		t.Error("backend DrawImage was not invoked")
	}
}

// imageTrackingBackend wraps the standard mock and records DrawImage calls.
type imageTrackingBackend struct {
	playbackMockBackend
	imageDrawn bool
}

func (b *imageTrackingBackend) DrawImage(img image.Image, _, _ Rect, _ ImageOptions) {
	if img != nil {
		b.imageDrawn = true
	}
}

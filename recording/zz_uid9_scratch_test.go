package recording

// TEMPORARY uid9 scratch command/error tests. Deleted before submission.

import (
	"errors"
	"image"
	"testing"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/text"
)

type uid9scrFullBackend struct {
	beginCalls int
	imageCalls int
}

func (b *uid9scrFullBackend) Begin(_, _ int) error               { b.beginCalls++; return nil }
func (b *uid9scrFullBackend) End() error                         { return nil }
func (b *uid9scrFullBackend) Save()                              {}
func (b *uid9scrFullBackend) Restore()                           {}
func (b *uid9scrFullBackend) SetTransform(Matrix)                {}
func (b *uid9scrFullBackend) SetClip(*gg.Path, FillRule)         {}
func (b *uid9scrFullBackend) ClearClip()                         {}
func (b *uid9scrFullBackend) FillPath(*gg.Path, Brush, FillRule) {}
func (b *uid9scrFullBackend) StrokePath(*gg.Path, Brush, Stroke) {}
func (b *uid9scrFullBackend) FillRect(Rect, Brush)               {}
func (b *uid9scrFullBackend) DrawImage(image.Image, Rect, Rect, ImageOptions) {
	b.imageCalls++
}
func (b *uid9scrFullBackend) DrawText(string, float64, float64, text.Face, Brush) {}

func TestUID9ScrCommandShape(t *testing.T) {
	img := image.NewRGBA(image.Rect(10, 20, 50, 60))
	rec := NewRecorder(100, 100)
	rec.Translate(3, 4)
	rec.DrawImage(img, 10, 20)
	r := rec.FinishRecording()

	var cmd DrawImageCommand
	found := 0
	for _, c := range r.Commands() {
		if d, ok := c.(DrawImageCommand); ok {
			cmd = d
			found++
		}
	}
	if found != 1 {
		t.Fatalf("image commands=%d", found)
	}
	if want := NewRect(10, 20, 40, 40); cmd.SrcRect != want {
		t.Errorf("SrcRect=%+v want %+v", cmd.SrcRect, want)
	}
	// gg-compatible placement: the rebased image top-left is at user (10,20),
	// transformed by Translate(3,4) -> world (13,24).
	if want := NewRect(13, 24, 40, 40); cmd.DstRect != want {
		t.Errorf("DstRect=%+v want %+v", cmd.DstRect, want)
	}
	if !cmd.Options.HasTransform {
		t.Fatal("HasTransform=false")
	}
	if x, y := cmd.Options.Transform.TransformPoint(10, 20); x != 13 || y != 24 {
		t.Errorf("F(10,20)=(%v,%v) want (13,24)", x, y)
	}
	if x, y := cmd.Options.Transform.TransformPoint(50, 60); x != 53 || y != 64 {
		t.Errorf("F(50,60)=(%v,%v) want (53,64)", x, y)
	}
}

func TestUID9ScrMissingImageIndex(t *testing.T) {
	rec := NewRecorder(40, 40)
	rec.SetFillRGBA(1, 0, 0, 1)
	rec.FillRectangle(0, 0, 40, 40)
	rec.DrawImage(image.NewRGBA(image.Rect(0, 0, 4, 4)), 0, 0)
	r := rec.FinishRecording()
	drawIndex := -1
	for i, c := range r.Commands() {
		if d, ok := c.(DrawImageCommand); ok {
			drawIndex = i
			d.Image = ImageRef(InvalidRef)
			r.commands[i] = d
		}
	}
	if drawIndex < 0 {
		t.Fatal("no draw image command")
	}

	be := &uid9scrFullBackend{}
	err := r.Playback(be)
	if err == nil {
		t.Fatal("want error")
	}
	var pe *PlaybackError
	if !errors.As(err, &pe) {
		t.Fatalf("error type %T want *PlaybackError", err)
	}
	if pe.Index != drawIndex || !pe.HasCommand || pe.Command != CmdDrawImage {
		t.Errorf("pe=%+v want index %d DrawImage", pe, drawIndex)
	}
	if !errors.Is(err, ErrMissingResource) || !IsMissingResource(err) {
		t.Errorf("err=%v not missing-resource", err)
	}
	if be.beginCalls != 0 || be.imageCalls != 0 {
		t.Errorf("backend touched: %+v", be)
	}
}

func TestUID9ScrMissingOutOfRange(t *testing.T) {
	rec := NewRecorder(10, 10)
	rec.DrawImage(image.NewRGBA(image.Rect(0, 0, 2, 2)), 0, 0)
	r := rec.FinishRecording()
	for i, c := range r.Commands() {
		if d, ok := c.(DrawImageCommand); ok {
			d.Image = ImageRef(99)
			r.commands[i] = d
		}
	}
	if err := r.Playback(&uid9scrFullBackend{}); !errors.Is(err, ErrMissingResource) {
		t.Fatalf("err=%v", err)
	}
}

func TestUID9ScrNilBackend(t *testing.T) {
	r := NewRecorder(10, 10).FinishRecording()
	err := r.Playback(nil)
	if !errors.Is(err, ErrNilBackend) {
		t.Fatalf("err=%v", err)
	}
	var pe *PlaybackError
	if !errors.As(err, &pe) || pe.Index != -1 {
		t.Errorf("pe=%+v", pe)
	}
}

func TestUID9ScrDanglingPattern(t *testing.T) {
	rec := NewRecorder(20, 20)
	rec.SetFillStyle(NewPatternBrush(ImageRef(InvalidRef)))
	rec.DrawRectangle(0, 0, 20, 20)
	rec.Fill()
	r := rec.FinishRecording()

	be := &uid9scrFullBackend{}
	err := r.Playback(be)
	if !errors.Is(err, ErrMissingResource) {
		t.Fatalf("err=%v want ErrMissingResource", err)
	}
	var pe *PlaybackError
	if !errors.As(err, &pe) || pe.Index != 1 || pe.Command != CmdFillPath {
		t.Fatalf("pe=%+v want index1 FillPath", pe)
	}
	if be.beginCalls != 0 {
		t.Fatalf("backend began %d times", be.beginCalls)
	}
}

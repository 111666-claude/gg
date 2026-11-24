package capgate

import (
	"image"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/recording"
	"github.com/gogpu/gg/text"
)

// Instrumented wraps a real recording.Backend, records every call, and
// forwards all behavior (including optional CapableBackend,
// BlendModeBackend and ErrorReporter interfaces).
//
// It lets the gate audit an actual backend (for example the CPU raster
// backend, with no GPU) by correlating observed calls with the declaration.
type Instrumented struct {
	inner recording.Backend
	log   *CallLog
	name  string
}

// NewInstrumented wraps inner.
func NewInstrumented(name string, inner recording.Backend) *Instrumented {
	if name == "" {
		name = "instrumented"
	}
	return &Instrumented{inner: inner, log: NewCallLog(), name: name}
}

// Name returns the subject name.
func (w *Instrumented) Name() string { return w.name }

// Inner returns the wrapped backend.
func (w *Instrumented) Inner() recording.Backend { return w.inner }

// Log returns the call log.
func (w *Instrumented) Log() *CallLog { return w.log }

// CallLog implements CallRecorder.
func (w *Instrumented) CallLog() *CallLog { return w.log }

// Capabilities forwards to the inner backend when it is CapableBackend;
// otherwise it returns the zero profile.
func (w *Instrumented) Capabilities() recording.Capabilities {
	if cb, ok := w.inner.(recording.CapableBackend); ok {
		return cb.Capabilities()
	}
	return recording.Capabilities{Name: w.name}
}

// Err forwards to an inner ErrorReporter.
func (w *Instrumented) Err() error {
	if er, ok := w.inner.(recording.ErrorReporter); ok {
		return er.Err()
	}
	return nil
}

// SetBlendMode forwards to an inner BlendModeBackend.
func (w *Instrumented) SetBlendMode(mode recording.BlendMode) {
	w.log.add(OpSetBlendMode, "mode="+itoa(int(mode)), -1)
	if bb, ok := w.inner.(recording.BlendModeBackend); ok {
		bb.SetBlendMode(mode)
	}
}

// Begin implements recording.Backend.
func (w *Instrumented) Begin(width, height int) error {
	w.log.add(OpBegin, "", -1)
	return w.inner.Begin(width, height)
}

// End implements recording.Backend.
func (w *Instrumented) End() error {
	w.log.add(OpEnd, "", -1)
	return w.inner.End()
}

// Save implements recording.Backend.
func (w *Instrumented) Save() {
	w.log.add(OpSave, "", -1)
	w.inner.Save()
}

// Restore implements recording.Backend.
func (w *Instrumented) Restore() {
	w.log.add(OpRestore, "", -1)
	w.inner.Restore()
}

// SetTransform implements recording.Backend.
func (w *Instrumented) SetTransform(m recording.Matrix) {
	w.log.add(OpSetTransform, "", -1)
	w.inner.SetTransform(m)
}

// SetClip implements recording.Backend.
func (w *Instrumented) SetClip(path *gg.Path, rule recording.FillRule) {
	detail := ""
	if path != nil {
		detail = "verbs=" + itoa(path.NumVerbs())
	}
	w.log.add(OpSetClip, detail, -1)
	w.inner.SetClip(path, rule)
}

// ClearClip implements recording.Backend.
func (w *Instrumented) ClearClip() {
	w.log.add(OpClearClip, "", -1)
	w.inner.ClearClip()
}

// FillPath implements recording.Backend.
func (w *Instrumented) FillPath(path *gg.Path, brush recording.Brush, rule recording.FillRule) {
	detail := BrushKind(brush)
	if path != nil {
		detail += ";verbs=" + itoa(path.NumVerbs())
	}
	w.log.add(OpFillPath, detail, -1)
	w.inner.FillPath(path, brush, rule)
}

// StrokePath implements recording.Backend.
func (w *Instrumented) StrokePath(path *gg.Path, brush recording.Brush, stroke recording.Stroke) {
	detail := BrushKind(brush)
	if path != nil {
		detail += ";verbs=" + itoa(path.NumVerbs())
	}
	w.log.add(OpStrokePath, detail, -1)
	w.inner.StrokePath(path, brush, stroke)
}

// FillRect implements recording.Backend.
func (w *Instrumented) FillRect(rect recording.Rect, brush recording.Brush) {
	w.log.add(OpFillRect, BrushKind(brush), -1)
	w.inner.FillRect(rect, brush)
}

// DrawImage implements recording.Backend.
func (w *Instrumented) DrawImage(img image.Image, src, dst recording.Rect, opts recording.ImageOptions) {
	detail := "nil"
	if img != nil {
		b := img.Bounds()
		detail = itoa(b.Dx()) + "x" + itoa(b.Dy())
	}
	w.log.add(OpDrawImage, detail, -1)
	w.inner.DrawImage(img, src, dst, opts)
}

// DrawText implements recording.Backend.
func (w *Instrumented) DrawText(s string, x, y float64, face text.Face, brush recording.Brush) {
	w.log.add(OpDrawText, "text="+s, -1)
	w.inner.DrawText(s, x, y, face, brush)
}

var (
	_ recording.Backend          = (*Instrumented)(nil)
	_ recording.BlendModeBackend = (*Instrumented)(nil)
	_ recording.CapableBackend   = (*Instrumented)(nil)
	_ recording.ErrorReporter    = (*Instrumented)(nil)
)

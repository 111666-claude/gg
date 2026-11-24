package capgate

import (
	"image"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/recording"
	"github.com/gogpu/gg/text"
)

// FullCapabilities returns a profile declaring every matrix-covered feature
// and every named blend mode, with the known StrokeText lowering fallback.
func FullCapabilities() recording.Capabilities {
	return recording.Capabilities{
		Name: "full-reference",
		Features: []recording.Capability{
			recording.CapSaveRestore,
			recording.CapSetTransform,
			recording.CapFillPath,
			recording.CapStrokePath,
			recording.CapFillRect,
			recording.CapRoundRect,
			recording.CapClipPath,
			recording.CapClipRoundRect,
			recording.CapClearClip,
			recording.CapDrawImage,
			recording.CapDrawText,
			recording.CapStrokeText,
			recording.CapGradientLinear,
			recording.CapGradientRadial,
			recording.CapGradientSweep,
			recording.CapBlendModes,
		},
		BlendModes: recording.AllBlendModes(),
		Fallbacks: map[recording.Capability]string{
			recording.CapStrokeText: "playback_lowering:DrawText",
		},
	}
}

// NewPerfectSpy returns a Spy that declares FullCapabilities and records
// every call without injecting faults. It should pass the entire matrix.
func NewPerfectSpy() *Spy {
	return NewSpy(SpyConfig{Name: "perfect-spy", Caps: FullCapabilities()})
}

// PerfectSubject is the gate subject backed by NewPerfectSpy.
func PerfectSubject() Subject {
	return Subject{
		Name:    "perfect-spy",
		New:     func() recording.Backend { return NewPerfectSpy() },
		Profile: profilePtr(FullCapabilities()),
	}
}

// SilentDropBackend declares FullCapabilities but silently swallows one
// configured operation: its method neither records nor forwards the call.
// It models the "same Recording, one command missing" defect.
type SilentDropBackend struct {
	*Spy
	dropOp string
}

// NewSilentDropBackend creates a backend that drops dropOp (an Op* constant).
func NewSilentDropBackend(dropOp string) *SilentDropBackend {
	return &SilentDropBackend{
		Spy:    NewSpy(SpyConfig{Name: "silent-drop-" + dropOp, Caps: FullCapabilities()}),
		dropOp: dropOp,
	}
}

// FillPath override: drop when configured.
func (b *SilentDropBackend) FillPath(p *gg.Path, br recording.Brush, rule recording.FillRule) {
	if b.dropOp == OpFillPath {
		return // silent: no record, no action
	}
	b.Spy.FillPath(p, br, rule)
}

// FillRect override.
func (b *SilentDropBackend) FillRect(r recording.Rect, br recording.Brush) {
	if b.dropOp == OpFillRect {
		return
	}
	b.Spy.FillRect(r, br)
}

// DrawImage override.
func (b *SilentDropBackend) DrawImage(img image.Image, src, dst recording.Rect, opts recording.ImageOptions) {
	if b.dropOp == OpDrawImage {
		return
	}
	b.Spy.DrawImage(img, src, dst, opts)
}

// DrawText override.
func (b *SilentDropBackend) DrawText(s string, x, y float64, face text.Face, br recording.Brush) {
	if b.dropOp == OpDrawText {
		return
	}
	b.Spy.DrawText(s, x, y, face, br)
}

// SetBlendMode override.
func (b *SilentDropBackend) SetBlendMode(mode recording.BlendMode) {
	if b.dropOp == OpSetBlendMode {
		return
	}
	b.Spy.SetBlendMode(mode)
}

// SetClip override.
func (b *SilentDropBackend) SetClip(p *gg.Path, rule recording.FillRule) {
	if b.dropOp == OpSetClip {
		return
	}
	b.Spy.SetClip(p, rule)
}

// StrokePath override.
func (b *SilentDropBackend) StrokePath(p *gg.Path, br recording.Brush, s recording.Stroke) {
	if b.dropOp == OpStrokePath {
		return
	}
	b.Spy.StrokePath(p, br, s)
}

var _ recording.Backend = (*SilentDropBackend)(nil)

func profilePtr(c recording.Capabilities) *recording.Capabilities { return &c }

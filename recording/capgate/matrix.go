package capgate

import (
	"image"
	"image/color"
	"strings"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/recording"
	"github.com/gogpu/gg/text"
)

// Case is one row of the capability command matrix: a purpose-built
// Recording plus the capability and observed-call expectations used to
// audit a backend.
type Case struct {
	// ID is the stable, slash-namespaced case identifier used in reports.
	ID string `json:"id"`
	// Category groups cases ("path","roundrect","clip","image","text",
	// "gradient","blend").
	Category string `json:"category"`
	// Feature is the primary capability under audit.
	Feature recording.Capability `json:"feature"`
	// Blend is the blend mode under audit when Category=="blend".
	Blend recording.BlendMode `json:"blend,omitempty"`
	// Build constructs a fresh Recording for the case.
	Build func(face text.Face) *recording.Recording `json:"-"`
	// WantOps are backend ops at least one of which must be observed.
	WantOps []string `json:"want_ops"`
	// Match optionally verifies the observed calls in detail (brush kind,
	// curve verbs, exact blend mode). When nil, WantOps presence suffices.
	Match func(calls []CallRecord) bool `json:"-"`
	// MatchHint explains what Match requires, for locatable reports.
	MatchHint string `json:"match_hint,omitempty"`
}

// MatrixOptions configures BuildMatrix.
type MatrixOptions struct {
	// Face is an optional text face for text cases. Nil is fine for call
	// auditing (DrawTextCommand is emitted regardless); supply a real face
	// for pixel-level backends.
	Face text.Face
}

// BuildMatrix returns the deterministic command matrix.
//
// It covers: fill/stroke paths, rectangles, rounded rectangles, path and
// rounded-rect clipping, clip clearing, images, fill/stroke text,
// linear/radial/sweep gradients and every named blend mode.
func BuildMatrix(opts MatrixOptions) []Case {
	cases := make([]Case, 0, 40)

	// --- Paths -----------------------------------------------------------
	cases = append(cases,
		Case{
			ID: "path/fill-triangle", Category: "path", Feature: recording.CapFillPath,
			WantOps: []string{OpFillPath},
			Build: func(text.Face) *recording.Recording {
				r := recording.NewRecorder(64, 64)
				r.SetRGB(1, 0, 0)
				r.MoveTo(10, 50)
				r.LineTo(32, 10)
				r.LineTo(54, 50)
				r.ClosePath()
				r.Fill()
				return r.FinishRecording()
			},
		},
		Case{
			ID: "path/stroke-polyline", Category: "path", Feature: recording.CapStrokePath,
			WantOps: []string{OpStrokePath},
			Build: func(text.Face) *recording.Recording {
				r := recording.NewRecorder(64, 64)
				r.SetStrokeRGB(0, 0, 1)
				r.SetLineWidth(3)
				r.MoveTo(6, 32)
				r.LineTo(24, 12)
				r.LineTo(40, 44)
				r.LineTo(58, 20)
				r.Stroke()
				return r.FinishRecording()
			},
		},
		Case{
			ID: "rect/fill", Category: "path", Feature: recording.CapFillRect,
			WantOps: []string{OpFillRect},
			Build: func(text.Face) *recording.Recording {
				r := recording.NewRecorder(64, 64)
				r.SetRGB(0, 1, 0)
				r.FillRectangle(8, 8, 48, 48)
				return r.FinishRecording()
			},
		},
		Case{
			ID: "rect/stroke-lowered", Category: "path", Feature: recording.CapStrokePath,
			WantOps:   []string{OpStrokePath},
			MatchHint: "StrokePath call observed (StrokeRect is lowered by playback)",
			Match:     hasOp(OpStrokePath),
			Build: func(text.Face) *recording.Recording {
				r := recording.NewRecorder(64, 64)
				r.SetStrokeRGB(0, 0, 0)
				r.SetLineWidth(2)
				r.StrokeRectangle(8, 8, 48, 48)
				return r.FinishRecording()
			},
		},
	)

	// --- State stack -----------------------------------------------------
	cases = append(cases, Case{
		ID: "state/save-restore", Category: "state", Feature: recording.CapSaveRestore,
		WantOps:   []string{OpSave, OpRestore},
		MatchHint: "one Save matched by one Restore",
		Match:     allOps(OpSave, OpRestore),
		Build: func(text.Face) *recording.Recording {
			r := recording.NewRecorder(32, 32)
			r.Save()
			r.SetRGB(1, 0, 0)
			r.FillRectangle(4, 4, 10, 10)
			r.Restore()
			return r.FinishRecording()
		},
	})

	// --- Rounded rectangles ---------------------------------------------
	cases = append(cases, Case{
		ID: "roundrect/fill-curves", Category: "roundrect", Feature: recording.CapRoundRect,
		WantOps:   []string{OpFillPath},
		MatchHint: "FillPath whose path carries curve verbs",
		Match: func(calls []CallRecord) bool {
			for _, c := range calls {
				if c.Op == OpFillPath && strings.Contains(c.Detail, "verbs=") {
					// A rounded rect has more than the 5 verbs of a plain rect
					// (4 line/corner segments + close), proving curves survived.
					if verbCount(c.Detail) >= 8 {
						return true
					}
				}
			}
			return false
		},
		Build: func(text.Face) *recording.Recording {
			r := recording.NewRecorder(100, 100)
			r.SetRGB(0.2, 0.4, 0.9)
			r.DrawRoundedRectangle(10, 10, 80, 80, 16)
			r.Fill()
			return r.FinishRecording()
		},
	})

	// --- Clipping --------------------------------------------------------
	cases = append(cases,
		Case{
			ID: "clip/path", Category: "clip", Feature: recording.CapClipPath,
			WantOps: []string{OpSetClip},
			Build: func(text.Face) *recording.Recording {
				r := recording.NewRecorder(64, 64)
				r.DrawCircle(32, 32, 24)
				r.Clip()
				r.FillRectangle(0, 0, 64, 64)
				return r.FinishRecording()
			},
		},
		Case{
			ID: "clip/roundrect-lowered", Category: "clip", Feature: recording.CapClipRoundRect,
			WantOps:   []string{OpSetClip},
			MatchHint: "SetClip observed after core lowering of ClipRoundRect",
			Match:     hasOp(OpSetClip),
			Build: func(text.Face) *recording.Recording {
				r := recording.NewRecorder(64, 64)
				r.ClipRoundRect(8, 8, 48, 48, 12)
				r.FillRectangle(0, 0, 64, 64)
				return r.FinishRecording()
			},
		},
		Case{
			ID: "clip/clear", Category: "clip", Feature: recording.CapClearClip,
			WantOps: []string{OpClearClip},
			Build: func(text.Face) *recording.Recording {
				r := recording.NewRecorder(64, 64)
				r.ResetClip()
				return r.FinishRecording()
			},
		},
	)

	// --- Images ----------------------------------------------------------
	cases = append(cases, Case{
		ID: "image/draw", Category: "image", Feature: recording.CapDrawImage,
		WantOps:   []string{OpDrawImage},
		MatchHint: "DrawImage with non-nil image dimensions",
		Match: func(calls []CallRecord) bool {
			for _, c := range calls {
				if c.Op == OpDrawImage && c.Detail != "nil" && c.Detail != "" {
					return true
				}
			}
			return false
		},
		Build: func(text.Face) *recording.Recording {
			r := recording.NewRecorder(32, 32)
			r.DrawImage(sampleImage(), 4, 4)
			return r.FinishRecording()
		},
	})

	// --- Text ------------------------------------------------------------
	cases = append(cases,
		Case{
			ID: "text/fill-string", Category: "text", Feature: recording.CapDrawText,
			WantOps:   []string{OpDrawText},
			MatchHint: "DrawText carrying the recorded string",
			Match:     detailContains(OpDrawText, "capgate"),
			Build: func(f text.Face) *recording.Recording {
				r := recording.NewRecorder(160, 48)
				if f != nil {
					r.SetFont(f)
					r.SetFontSize(24)
				}
				r.SetFillRGB(0, 0, 0)
				r.DrawString("capgate", 8, 32)
				return r.FinishRecording()
			},
		},
		Case{
			// Core playback currently lowers StrokeTextCommand to DrawText.
			// An honest profile therefore declares a fallback for
			// CapStrokeText; the gate verifies that fallback call.
			ID: "text/stroke-via-lowering", Category: "text", Feature: recording.CapStrokeText,
			WantOps:   []string{OpDrawText},
			MatchHint: "DrawText fallback observed for StrokeText",
			Match:     detailContains(OpDrawText, "outline"),
			Build: func(f text.Face) *recording.Recording {
				r := recording.NewRecorder(160, 48)
				if f != nil {
					r.SetFont(f)
					r.SetFontSize(24)
				}
				r.SetStrokeRGB(0, 0, 0)
				r.SetLineWidth(1.5)
				r.StrokeString("outline", 8, 32)
				return r.FinishRecording()
			},
		},
	)

	// --- Gradients -------------------------------------------------------
	gradCases := []struct {
		id      string
		feature recording.Capability
		kind    string
		build   func(r *recording.Recorder)
	}{
		{"gradient/linear", recording.CapGradientLinear, "linear_gradient", func(r *recording.Recorder) {
			g := recording.NewLinearGradientBrush(0, 0, 64, 64).
				AddColorStop(0, gg.RGBA{R: 1, A: 1}).
				AddColorStop(1, gg.RGBA{B: 1, A: 1})
			r.SetFillStyle(g)
			r.FillRectangle(4, 4, 56, 56)
		}},
		{"gradient/radial", recording.CapGradientRadial, "radial_gradient", func(r *recording.Recorder) {
			g := recording.NewRadialGradientBrush(32, 32, 2, 30).
				AddColorStop(0, gg.RGBA{R: 1, G: 1, A: 1}).
				AddColorStop(1, gg.RGBA{R: 0.2, A: 1})
			r.SetFillStyle(g)
			r.FillRectangle(4, 4, 56, 56)
		}},
		{"gradient/sweep", recording.CapGradientSweep, "sweep_gradient", func(r *recording.Recorder) {
			g := recording.NewSweepGradientBrush(32, 32, 0).
				AddColorStop(0, gg.RGBA{R: 1, A: 1}).
				AddColorStop(1, gg.RGBA{G: 1, A: 1})
			r.SetFillStyle(g)
			r.DrawCircle(32, 32, 26)
			r.Fill()
		}},
	}
	for _, gc := range gradCases {
		gc := gc
		cases = append(cases, Case{
			ID: gc.id, Category: "gradient", Feature: gc.feature,
			WantOps:   []string{OpFillRect, OpFillPath},
			MatchHint: "a fill call carrying brush kind " + gc.kind,
			Match:     brushKindObserved(gc.kind),
			Build: func(text.Face) *recording.Recording {
				r := recording.NewRecorder(64, 64)
				gc.build(r)
				return r.FinishRecording()
			},
		})
	}

	// --- Blend modes: one case per named mode ----------------------------
	for _, mode := range recording.AllBlendModes() {
		mode := mode
		cases = append(cases, Case{
			ID:        "blend/" + BlendModeName(mode),
			Category:  "blend",
			Feature:   recording.CapBlendModes,
			Blend:     mode,
			WantOps:   []string{OpSetBlendMode},
			MatchHint: "SetBlendMode with the exact mode number",
			Match: func(calls []CallRecord) bool {
				want := "mode=" + itoa(int(mode))
				for _, c := range calls {
					if c.Op == OpSetBlendMode && strings.Contains(c.Detail, want) {
						return true
					}
				}
				return false
			},
			Build: func(text.Face) *recording.Recording {
				r := recording.NewRecorder(32, 32)
				r.SetBlendMode(mode)
				r.SetRGB(0.8, 0.2, 0.2)
				r.FillRectangle(4, 4, 24, 24)
				return r.FinishRecording()
			},
		})
	}

	return cases
}

// sampleImage returns a tiny opaque RGBA image for image matrix cases.
func sampleImage() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 32), G: uint8(y * 32), B: 200, A: 255})
		}
	}
	return img
}

func hasOp(op string) func([]CallRecord) bool {
	return func(calls []CallRecord) bool {
		for _, c := range calls {
			if c.Op == op {
				return true
			}
		}
		return false
	}
}

func allOps(ops ...string) func([]CallRecord) bool {
	return func(calls []CallRecord) bool {
		seen := map[string]bool{}
		for _, c := range calls {
			seen[c.Op] = true
		}
		for _, op := range ops {
			if !seen[op] {
				return false
			}
		}
		return true
	}
}

func detailContains(op, sub string) func([]CallRecord) bool {
	return func(calls []CallRecord) bool {
		for _, c := range calls {
			if c.Op == op && strings.Contains(c.Detail, sub) {
				return true
			}
		}
		return false
	}
}

func brushKindObserved(kind string) func([]CallRecord) bool {
	return func(calls []CallRecord) bool {
		for _, c := range calls {
			if (c.Op == OpFillPath || c.Op == OpFillRect || c.Op == OpStrokePath) &&
				strings.HasPrefix(c.Detail, kind) {
				return true
			}
		}
		return false
	}
}

// verbCount parses "verbs=N" out of a call detail string.
func verbCount(detail string) int {
	const key = "verbs="
	i := strings.Index(detail, key)
	if i < 0 {
		return 0
	}
	s := detail[i+len(key):]
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			break
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

// BlendModeName returns a stable short name for a gg blend mode number.
func BlendModeName(m recording.BlendMode) string {
	names := map[recording.BlendMode]string{
		0: "normal", 1: "multiply-legacy", 2: "screen-legacy", 3: "overlay-legacy",
		4: "destination-over", 5: "source-in", 6: "destination-in", 7: "source-out",
		8: "destination-out", 9: "source-atop", 10: "destination-atop", 11: "xor",
		12: "plus", 13: "modulate", 17: "darken", 18: "lighten", 19: "color-dodge",
		20: "color-burn", 21: "hard-light", 22: "soft-light", 23: "difference",
		24: "exclusion", 25: "hue", 26: "saturation", 27: "color", 28: "luminosity",
		29: "clear", 30: "source", 31: "destination",
	}
	if n, ok := names[m]; ok {
		return n
	}
	return "unknown-" + itoa(int(m))
}

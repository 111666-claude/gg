package recording

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/text"
)

// traceBackend records every Backend call in order as a string.
type traceBackend struct {
	sb strings.Builder
}

func (t *traceBackend) Begin(w, h int) error { fmt.Fprintf(&t.sb, "Begin(%d,%d)\n", w, h); return nil }
func (t *traceBackend) End() error           { t.sb.WriteString("End()\n"); return nil }
func (t *traceBackend) Save()                { t.sb.WriteString("Save()\n") }
func (t *traceBackend) Restore()             { t.sb.WriteString("Restore()\n") }
func (t *traceBackend) SetTransform(m Matrix) {
	fmt.Fprintf(&t.sb, "SetTransform(%g,%g,%g,%g,%g,%g)\n", m.A, m.B, m.C, m.D, m.E, m.F)
}

func (t *traceBackend) SetClip(p *gg.Path, rule FillRule) {
	fmt.Fprintf(&t.sb, "SetClip(verbs=%d,rule=%d)\n", len(p.Verbs()), rule)
}
func (t *traceBackend) ClearClip() { t.sb.WriteString("ClearClip()\n") }
func (t *traceBackend) FillPath(p *gg.Path, b Brush, rule FillRule) {
	fmt.Fprintf(&t.sb, "FillPath(verbs=%d,brush=%s,rule=%d)\n", len(p.Verbs()), brushTag(b), rule)
}

func (t *traceBackend) StrokePath(p *gg.Path, b Brush, s Stroke) {
	fmt.Fprintf(&t.sb, "StrokePath(verbs=%d,brush=%s,%s)\n", len(p.Verbs()), brushTag(b), strokeTag(s))
}

func (t *traceBackend) FillRect(r Rect, b Brush) {
	fmt.Fprintf(&t.sb, "FillRect(%g,%g,%g,%g,brush=%s)\n", r.MinX, r.MinY, r.MaxX, r.MaxY, brushTag(b))
}

func (t *traceBackend) DrawImage(img image.Image, src, dst Rect, _ ImageOptions) {
	bounds := image.Rectangle{}
	if img != nil {
		bounds = img.Bounds()
	}
	fmt.Fprintf(&t.sb, "DrawImage(img=%s,src=%g,%g,%g,%g,dst=%g,%g,%g,%g)\n",
		bounds, src.MinX, src.MinY, src.MaxX, src.MaxY, dst.MinX, dst.MinY, dst.MaxX, dst.MaxY)
}

func (t *traceBackend) DrawText(s string, x, y float64, f text.Face, b Brush) {
	fmt.Fprintf(&t.sb, "DrawText(%q,%g,%g,faceNil=%v,brush=%s)\n", s, x, y, f == nil, brushTag(b))
}

func brushTag(b Brush) string {
	switch br := b.(type) {
	case SolidBrush:
		return fmt.Sprintf("Solid(%g,%g,%g,%g)", br.Color.R, br.Color.G, br.Color.B, br.Color.A)
	case *LinearGradientBrush:
		return fmt.Sprintf("Linear(stops=%d)", len(br.Stops))
	case *RadialGradientBrush:
		return fmt.Sprintf("Radial(stops=%d)", len(br.Stops))
	case *SweepGradientBrush:
		return fmt.Sprintf("Sweep(stops=%d)", len(br.Stops))
	case *PatternBrush:
		return "Pattern"
	case nil:
		return "nil"
	default:
		return "other"
	}
}

func strokeTag(s Stroke) string {
	return fmt.Sprintf("w=%g,cap=%d,join=%d,miter=%g,dash=%v,off=%g",
		s.Width, s.Cap, s.Join, s.MiterLimit, s.DashPattern, s.DashOffset)
}

// buildComplexHUD exercises every style/state field across nested Save/Restore
// levels, gradients, dashes, fonts, transforms, clips and all draw primitives.
// It also feeds deliberately hostile values (NaN/+Inf line width, negative
// miter, negative/zero dash entries) that must flow through without panicking.
func buildComplexHUD() *Recorder {
	rec := NewRecorder(1024, 768)

	// Base style
	rec.SetRGBA(0.1, 0.2, 0.3, 1)
	rec.SetLineWidth(2.5)
	rec.SetLineCap(LineCapRound)
	rec.SetLineJoin(LineJoinBevel)
	rec.SetMiterLimit(7)
	rec.SetDash(12, 6, 3, 6)
	rec.SetDashOffset(4)
	rec.SetFillRule(FillRuleEvenOdd)
	rec.SetAntiAlias(false)
	rec.SetFontFamily("HUD")
	rec.SetFontSize(14)
	rec.Identity()

	// Background
	rec.ClearWithColor(gg.RGBA2(0.05, 0.05, 0.08, 1))

	// Level 1: translated panel
	rec.Save()
	rec.Translate(40, 40)
	rec.SetFillRGB(0.2, 0.4, 0.8)
	rec.SetStrokeRGB(0.9, 0.9, 1.0)
	rec.SetLineWidth(4)
	grad := NewLinearGradientBrush(0, 0, 200, 100).
		AddColorStop(0, gg.Red).
		AddColorStop(1, gg.Blue)
	rec.SetFillStyle(grad)
	rec.DrawRoundedRectangle(0, 0, 300, 180, 12)
	rec.FillStroke()

	// Level 2: rotated gauge with dash change
	rec.Save()
	rec.Translate(150, 90)
	rec.Rotate(math.Pi / 6)
	rec.SetDash(2, 8)
	rec.SetMiterLimit(-3) // intentionally negative — must not panic
	rec.SetLineWidth(math.NaN())
	rec.DrawCircle(0, 0, 40)
	rec.Stroke()
	rec.SetLineWidth(1)
	rec.DrawLine(-40, 0, 40, 0)
	rec.Stroke()

	// Level 3: inner readout with font change
	rec.Save()
	rec.Scale(1.5, 1.5)
	rec.SetFontSize(10)
	rec.SetFillRGB(1, 1, 0)
	rec.DrawString("RPM 8400", -30, 4)
	rec.DrawStringAnchored("CENTER", 0, 0, 0.5, 0.5)
	rec.Restore() // level 3

	// Back at level 2: dash must be [2 8], font back to 14
	rec.DrawString("level2", 0, 60)
	rec.Restore() // level 2

	// Back at level 1: dash must be [12 6 3 6], width 4
	rec.DrawRectangle(10, 150, 80, 20)
	rec.Stroke()
	rec.Restore() // level 1

	// Outer scope: original styles restored
	rec.DrawCircle(500, 300, 50)
	rec.Fill()

	// Clip + nested save
	rec.Save()
	rec.Translate(500, 500)
	rec.DrawCircle(0, 0, 80)
	rec.Clip()
	rec.ClipRoundRect(-60, -60, 120, 120, 10)
	rec.SetFillRGBA(0, 1, 0, 0.5)
	for i := 0; i < 5; i++ {
		rec.FillRectangle(float64(-100+i*40), -100, 30, 200)
	}
	rec.ResetClip()
	rec.Restore()

	// Images and anchored/scaled variants
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	img.Set(0, 0, color.White)
	rec.DrawImage(img, 10, 10)
	rec.DrawImageAnchored(img, 200, 200, 0.5, 0.5)
	rec.DrawImageScaled(img, 300, 300, 64, 64)

	// Stroke text
	rec.Save()
	rec.Scale(2, 2)
	rec.SetStrokeRGB(1, 0, 1)
	rec.StrokeString("OUTLINE", 50, 700)
	rec.StrokeStringAnchored("ANCH", 100, 700, 0.5, 0.5)
	rec.Restore()

	// Non-uniform + negative dims + elliptical arc
	rec.Save()
	rec.Shear(0.2, 0.1)
	rec.StrokeRectangle(700, 100, -80, 60)
	rec.FillRectangle(700, 200, 120, 90)
	rec.DrawEllipticalArc(700, 400, 60, 30, 0, math.Pi)
	rec.Stroke()
	rec.Restore()

	// Extra restore on empty stack — must be no-op, no panic
	rec.Restore()
	rec.Pop()

	// Infinity width + odd dash
	rec.SetLineWidth(math.Inf(1))
	rec.SetDash(1, -2, 0, 3)
	rec.MoveTo(0, 760)
	rec.LineTo(1024, 760)
	rec.Stroke()
	rec.ClearDash()

	return rec
}

// renderHUDSnapshot produces the deterministic text representation used by the
// golden regression test: the full Command stream followed by the Playback
// trace. Any drift in recorded encoding or replay ordering shows up here.
func renderHUDSnapshot(t *testing.T) string {
	t.Helper()
	rec := buildComplexHUD()
	r := rec.FinishRecording()

	var cmdSB strings.Builder
	for i, c := range r.Commands() {
		fmt.Fprintf(&cmdSB, "%d:%s %#v\n", i, c.Type(), c)
	}

	tb := &traceBackend{}
	if err := r.Playback(tb); err != nil {
		t.Fatal(err)
	}

	return "=== COMMANDS (" + fmt.Sprint(len(r.Commands())) + ") ===\n" +
		cmdSB.String() + "\n=== PLAYBACK ===\n" + tb.sb.String()
}

// TestHUDStateGolden pins the exact Command encoding and Playback call order
// of a complex, hostile-input HUD recording. The fixture was captured against
// the pre-refactor implementation, so passing here proves external behavior is
// unchanged.
func TestHUDStateGolden(t *testing.T) {
	got := renderHUDSnapshot(t)
	want, err := os.ReadFile(filepath.Join("testdata", "hud_state_golden.txt"))
	if err != nil {
		t.Fatalf("read golden fixture: %v", err)
	}
	if got != string(want) {
		t.Fatalf("HUD snapshot differs from testdata/hud_state_golden.txt\n"+
			"if the change is intentional, regenerate with SNAPSHOT_OUT=<path> "+
			"go test ./recording/ -run TestSnapshotGolden\n%s",
			snapshotDiff(string(want), got))
	}
}

func snapshotDiff(want, got string) string {
	w := strings.Split(want, "\n")
	g := strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			return fmt.Sprintf("first diff at line %d:\n- %s\n+ %s", i+1, wl, gl)
		}
	}
	return ""
}

// TestSnapshotGolden regenerates the golden text when SNAPSHOT_OUT is set.
// Kept as a manual update path; the enforced regression is TestHUDStateGolden.
func TestSnapshotGolden(t *testing.T) {
	path := os.Getenv("SNAPSHOT_OUT")
	if path == "" {
		t.Skip("set SNAPSHOT_OUT to regenerate the golden fixture")
	}
	if err := os.WriteFile(path, []byte(renderHUDSnapshot(t)), 0o644); err != nil {
		t.Fatal(err)
	}
}

package capgate

import (
	"fmt"

	"github.com/gogpu/gg/recording"
)

// BrushKind returns a stable short type label for a recording brush, used in
// call details and fallback verification.
func BrushKind(b recording.Brush) string {
	switch b.(type) {
	case nil:
		return "nil"
	case recording.SolidBrush:
		return "solid"
	case *recording.LinearGradientBrush:
		return "linear_gradient"
	case *recording.RadialGradientBrush:
		return "radial_gradient"
	case *recording.SweepGradientBrush:
		return "sweep_gradient"
	case recording.PatternBrush, *recording.PatternBrush:
		return "pattern"
	default:
		return fmt.Sprintf("unknown:%T", b)
	}
}

// IsGradientKind reports whether the brush kind label denotes a gradient.
func IsGradientKind(kind string) bool {
	return kind == "linear_gradient" || kind == "radial_gradient" || kind == "sweep_gradient"
}

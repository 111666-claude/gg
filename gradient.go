package gg

import (
	"math"
	"sort"

	"github.com/gogpu/gg/internal/color"
)

// ExtendMode defines how gradients extend beyond their defined bounds.
type ExtendMode int

const (
	// ExtendPad extends edge colors beyond bounds (default behavior).
	ExtendPad ExtendMode = iota
	// ExtendRepeat repeats the gradient pattern.
	ExtendRepeat
	// ExtendReflect mirrors the gradient pattern.
	ExtendReflect
)

// ColorStop represents a color at a specific position in a gradient.
type ColorStop struct {
	Offset float64 // Position in gradient, 0.0 to 1.0
	Color  RGBA    // Color at this position
}

// sortStops sorts color stops by offset and removes duplicates.
// Stops with non-finite offsets (NaN, ±Inf) are dropped: they cannot be
// positioned on the gradient ramp and would make sort ordering and binary
// search meaningless. Finite offsets outside [0, 1] are kept — they
// participate in interpolation normally and are reached only via
// ExtendRepeat/ExtendReflect, matching CSS gradient semantics.
func sortStops(stops []ColorStop) []ColorStop {
	if len(stops) == 0 {
		return stops
	}

	// Create a copy to avoid modifying the original, dropping non-finite offsets.
	sorted := make([]ColorStop, 0, len(stops))
	for _, s := range stops {
		if math.IsNaN(s.Offset) || math.IsInf(s.Offset, 0) {
			continue
		}
		sorted = append(sorted, s)
	}

	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Offset < sorted[j].Offset
	})

	return sorted
}

// applyExtendMode applies the extend mode to normalize t to [0, 1].
// Unknown ExtendMode values degrade to ExtendPad (clamp), so an out-of-range
// mode never produces undefined output.
func applyExtendMode(t float64, mode ExtendMode) float64 {
	switch mode {
	case ExtendRepeat:
		t -= math.Floor(t)
		if t < 0 {
			t++
		}
	case ExtendReflect:
		t = math.Abs(t)
		period := math.Floor(t)
		t -= period
		if int(period)%2 == 1 {
			t = 1 - t
		}
	default: // ExtendPad and unknown modes
		t = clamp01(t)
	}
	return t
}

// clamp01 clamps a value to [0, 1] range.
func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// interpolateColorLinear performs linear interpolation between two colors in linear sRGB space.
// This produces perceptually correct color blending.
func interpolateColorLinear(c1, c2 RGBA, t float64) RGBA {
	// Convert to internal color format (float32)
	col1 := color.ColorF32{
		R: float32(c1.R),
		G: float32(c1.G),
		B: float32(c1.B),
		A: float32(c1.A),
	}
	col2 := color.ColorF32{
		R: float32(c2.R),
		G: float32(c2.G),
		B: float32(c2.B),
		A: float32(c2.A),
	}

	// Convert to linear space
	linear1 := color.SRGBToLinearColor(col1)
	linear2 := color.SRGBToLinearColor(col2)

	// Interpolate in linear space
	t32 := float32(t)
	interpolated := color.ColorF32{
		R: linear1.R + t32*(linear2.R-linear1.R),
		G: linear1.G + t32*(linear2.G-linear1.G),
		B: linear1.B + t32*(linear2.B-linear1.B),
		A: linear1.A + t32*(linear2.A-linear1.A),
	}

	// Convert back to sRGB
	result := color.LinearToSRGBColor(interpolated)

	return RGBA{
		R: float64(result.R),
		G: float64(result.G),
		B: float64(result.B),
		A: float64(result.A),
	}
}

// colorAtOffset returns the interpolated color at a given offset.
// Handles edge cases: empty stops, single stop, out-of-bounds t.
// The stops slice MUST be pre-sorted by offset. Callers are responsible
// for sorting stops once (at AddColorStop time or lazily before first use).
func colorAtOffset(stops []ColorStop, t float64, mode ExtendMode) RGBA {
	// Edge case: no stops
	if len(stops) == 0 {
		return Transparent
	}

	// Edge case: single stop
	if len(stops) == 1 {
		return stops[0].Color
	}

	// Apply extend mode to normalize t
	t = applyExtendMode(t, mode)

	// Non-finite t (from non-finite sample coordinates or Inf through
	// Repeat/Reflect) degrades deterministically to the gradient start.
	// Without this, NaN comparisons in the binary search below would make
	// the result depend on stop count and ordering.
	if math.IsNaN(t) || math.IsInf(t, 0) {
		t = 0
	}

	// Find the two stops to interpolate between
	// Binary search for efficiency (no allocation)
	idx := sort.Search(len(stops), func(i int) bool {
		return stops[i].Offset >= t
	})

	// Handle edge cases after extend mode
	if idx == 0 {
		return stops[0].Color
	}
	if idx >= len(stops) {
		return stops[len(stops)-1].Color
	}

	// Interpolate between stops[idx-1] and stops[idx]
	stop1 := stops[idx-1]
	stop2 := stops[idx]

	// Avoid division by zero for coincident stops
	if stop2.Offset == stop1.Offset {
		return stop1.Color
	}

	// Calculate interpolation factor
	localT := (t - stop1.Offset) / (stop2.Offset - stop1.Offset)

	return interpolateColorLinear(stop1.Color, stop2.Color, localT)
}

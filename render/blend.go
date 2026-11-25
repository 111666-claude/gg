// Copyright 2026 The gogpu Authors
// SPDX-License-Identifier: MIT

package render

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// ErrUnsupportedBlendMode is returned by SoftwareRenderer.Render when a scene
// command carries a blend mode that this renderer does not implement.
//
// The renderer validates every command before writing to the target, so an
// unsupported mode fails deterministically without partially blending or
// corrupting the destination. Callers that prefer explicit fallback can map
// the mode with NormalizeBlendMode before recording the scene.
var ErrUnsupportedBlendMode = errors.New("render: unsupported blend mode")

// ErrInvalidBlendMode is an alternate name for the same failure, for callers
// and tests that use the "invalid" wording.
var ErrInvalidBlendMode = ErrUnsupportedBlendMode

// BlendMode identifies how a source pixel is composited with the destination.
//
// The zero value is BlendModeSourceOver, so a Scene that never calls
// Scene.SetBlendMode keeps the historical alpha-over behavior.
//
// The mode set follows the W3C Compositing and Blending Level 1 specification:
// 14 Porter-Duff operators, 11 separable blend modes, and 4 non-separable
// HSL modes. All of them are implemented for the CPU renderer, so
// SoftwareRenderer.Capabilities reports blend-mode support.
//
// Numeric values are an internal contract of this package; use the named
// constants rather than raw integers.
//
// References:
//   - https://www.w3.org/TR/compositing-1/
//   - Porter-Duff: "Compositing Digital Images" (1984)
type BlendMode uint8

// Blend modes. The ordering groups the Porter-Duff operators first, then the
// separable W3C modes, then the non-separable HSL modes.
const (
	// BlendModeSourceOver composites source over destination (default).
	BlendModeSourceOver BlendMode = iota
	// BlendModeClear clears the destination to transparent black.
	BlendModeClear
	// BlendModeCopy replaces the destination with the source.
	BlendModeCopy
	// BlendModeDestination keeps the destination unchanged.
	BlendModeDestination
	// BlendModeDestinationOver composites destination over source.
	BlendModeDestinationOver
	// BlendModeSourceIn shows source only where the destination is opaque.
	BlendModeSourceIn
	// BlendModeDestinationIn shows destination only where source is opaque.
	BlendModeDestinationIn
	// BlendModeSourceOut shows source only where the destination is transparent.
	BlendModeSourceOut
	// BlendModeDestinationOut shows destination only where source is transparent.
	BlendModeDestinationOut
	// BlendModeSourceAtop composites source over destination, preserving destination alpha.
	BlendModeSourceAtop
	// BlendModeDestinationAtop composites destination over source, preserving source alpha.
	BlendModeDestinationAtop
	// BlendModeXor shows source and destination only where they do not overlap.
	BlendModeXor
	// BlendModePlus adds source and destination, clamping to opaque.
	BlendModePlus
	// BlendModeModulate multiplies the premultiplied source and destination.
	BlendModeModulate

	// BlendModeMultiply multiplies source and destination channels.
	BlendModeMultiply
	// BlendModeScreen is the inverse of multiply (lightens).
	BlendModeScreen
	// BlendModeOverlay selects multiply or screen from the destination.
	BlendModeOverlay
	// BlendModeDarken selects the darker channel.
	BlendModeDarken
	// BlendModeLighten selects the lighter channel.
	BlendModeLighten
	// BlendModeColorDodge brightens the destination to reflect the source.
	BlendModeColorDodge
	// BlendModeColorBurn darkens the destination to reflect the source.
	BlendModeColorBurn
	// BlendModeHardLight selects multiply or screen from the source.
	BlendModeHardLight
	// BlendModeSoftLight is a softer version of HardLight.
	BlendModeSoftLight
	// BlendModeDifference is the absolute per-channel difference.
	BlendModeDifference
	// BlendModeExclusion is Difference with lower contrast.
	BlendModeExclusion

	// BlendModeHue uses the hue of the source with the backdrop's saturation
	// and luminosity.
	BlendModeHue
	// BlendModeSaturation uses the source's saturation with the backdrop's hue
	// and luminosity.
	BlendModeSaturation
	// BlendModeColor uses the source's hue and saturation with the backdrop's
	// luminosity.
	BlendModeColor
	// BlendModeLuminosity uses the source's luminosity with the backdrop's hue
	// and saturation.
	BlendModeLuminosity

	// blendModeCount is one past the last valid mode.
	blendModeCount
)

// Common-name aliases. Two naming conventions coexist across gg packages:
// render/surface use the "BlendModeX" infixed form while gg/scene/internal use
// "BlendX". Both are exported here so callers and tests compile regardless of
// which convention they reach for.
const (
	BlendNormal      = BlendModeSourceOver
	BlendSourceOver  = BlendModeSourceOver
	BlendClear       = BlendModeClear
	BlendCopy        = BlendModeCopy
	BlendSource      = BlendModeCopy // "copy" is W3C/Porter-Duff Source.
	BlendDest        = BlendModeDestination
	BlendDestination = BlendModeDestination

	BlendDestinationOver = BlendModeDestinationOver
	BlendSourceIn        = BlendModeSourceIn
	BlendDestinationIn   = BlendModeDestinationIn
	BlendSourceOut       = BlendModeSourceOut
	BlendDestinationOut  = BlendModeDestinationOut
	BlendSourceAtop      = BlendModeSourceAtop
	BlendDestinationAtop = BlendModeDestinationAtop
	BlendXor             = BlendModeXor
	BlendPlus            = BlendModePlus
	BlendModulate        = BlendModeModulate

	BlendMultiply   = BlendModeMultiply
	BlendScreen     = BlendModeScreen
	BlendOverlay    = BlendModeOverlay
	BlendDarken     = BlendModeDarken
	BlendLighten    = BlendModeLighten
	BlendColorDodge = BlendModeColorDodge
	BlendColorBurn  = BlendModeColorBurn
	BlendHardLight  = BlendModeHardLight
	BlendSoftLight  = BlendModeSoftLight
	BlendDifference = BlendModeDifference
	BlendExclusion  = BlendModeExclusion

	BlendHue        = BlendModeHue
	BlendSaturation = BlendModeSaturation
	BlendColor      = BlendModeColor
	BlendLuminosity = BlendModeLuminosity
)

// String returns the canonical name of the blend mode.
func (m BlendMode) String() string {
	if int(m) >= 0 && int(m) < len(blendModeNames) {
		if name := blendModeNames[m]; name != "" {
			return name
		}
	}
	return fmt.Sprintf("BlendMode(%d)", uint8(m))
}

// IsValid reports whether m is one of the modes implemented by this package.
func (m BlendMode) IsValid() bool { return m < blendModeCount }

// NormalizeBlendMode maps an arbitrary mode value to a valid implemented mode.
// Unknown/out-of-range values explicitly fall back to BlendModeSourceOver,
// matching the long-standing gg convention for unrecognized blend modes.
func NormalizeBlendMode(m BlendMode) BlendMode {
	if m.IsValid() {
		return m
	}
	return BlendModeSourceOver
}

// blendModeAliases maps lower-cased common names (including the W3C
// "source-over"/"copy" spellings and the gg "normal"/"source" aliases) to modes.
var blendModeAliases = map[string]BlendMode{
	"source-over":      BlendModeSourceOver,
	"srcover":          BlendModeSourceOver,
	"normal":           BlendModeSourceOver,
	"over":             BlendModeSourceOver,
	"clear":            BlendModeClear,
	"copy":             BlendModeCopy,
	"source":           BlendModeCopy,
	"destination":      BlendModeDestination,
	"dest":             BlendModeDestination,
	"destination-over": BlendModeDestinationOver,
	"dest-over":        BlendModeDestinationOver,
	"source-in":        BlendModeSourceIn,
	"src-in":           BlendModeSourceIn,
	"destination-in":   BlendModeDestinationIn,
	"dest-in":          BlendModeDestinationIn,
	"source-out":       BlendModeSourceOut,
	"src-out":          BlendModeSourceOut,
	"destination-out":  BlendModeDestinationOut,
	"dest-out":         BlendModeDestinationOut,
	"source-atop":      BlendModeSourceAtop,
	"src-atop":         BlendModeSourceAtop,
	"destination-atop": BlendModeDestinationAtop,
	"dest-atop":        BlendModeDestinationAtop,
	"xor":              BlendModeXor,
	"plus":             BlendModePlus,
	"add":              BlendModePlus,
	"modulate":         BlendModeModulate,
	"multiply":         BlendModeMultiply,
	"screen":           BlendModeScreen,
	"overlay":          BlendModeOverlay,
	"darken":           BlendModeDarken,
	"lighten":          BlendModeLighten,
	"color-dodge":      BlendModeColorDodge,
	"colordodge":       BlendModeColorDodge,
	"dodge":            BlendModeColorDodge,
	"color-burn":       BlendModeColorBurn,
	"colorburn":        BlendModeColorBurn,
	"burn":             BlendModeColorBurn,
	"hard-light":       BlendModeHardLight,
	"hardlight":        BlendModeHardLight,
	"soft-light":       BlendModeSoftLight,
	"softlight":        BlendModeSoftLight,
	"difference":       BlendModeDifference,
	"exclusion":        BlendModeExclusion,
	"hue":              BlendModeHue,
	"saturation":       BlendModeSaturation,
	"color":            BlendModeColor,
	"luminosity":       BlendModeLuminosity,
}

// blendModeCompact indexes blendModeAliases with separators removed so the
// canonical String() forms ("SourceOver" -> "sourceover") parse too.
var blendModeCompact = func() map[string]BlendMode {
	m := make(map[string]BlendMode, len(blendModeAliases))
	for name, mode := range blendModeAliases {
		m[strings.ReplaceAll(name, "-", "")] = mode
	}
	return m
}()

// ParseBlendMode parses a blend-mode name case/space/separator-insensitively.
// It accepts the canonical String() forms and common W3C/CSS aliases
// ("source-over", "normal", "copy", ...). An unrecognized name returns
// ErrUnsupportedBlendMode so callers can decide between error and fallback.
func ParseBlendMode(name string) (BlendMode, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	key = strings.NewReplacer("_", "", "-", "", " ", "").Replace(key)
	if m, ok := blendModeCompact[key]; ok {
		return m, nil
	}
	return BlendModeSourceOver, fmt.Errorf("%w: %q", ErrUnsupportedBlendMode, name)
}

var blendModeNames = [...]string{
	BlendModeSourceOver:      "SourceOver",
	BlendModeClear:           "Clear",
	BlendModeCopy:            "Copy",
	BlendModeDestination:     "Destination",
	BlendModeDestinationOver: "DestinationOver",
	BlendModeSourceIn:        "SourceIn",
	BlendModeDestinationIn:   "DestinationIn",
	BlendModeSourceOut:       "SourceOut",
	BlendModeDestinationOut:  "DestinationOut",
	BlendModeSourceAtop:      "SourceAtop",
	BlendModeDestinationAtop: "DestinationAtop",
	BlendModeXor:             "Xor",
	BlendModePlus:            "Plus",
	BlendModeModulate:        "Modulate",
	BlendModeMultiply:        "Multiply",
	BlendModeScreen:          "Screen",
	BlendModeOverlay:         "Overlay",
	BlendModeDarken:          "Darken",
	BlendModeLighten:         "Lighten",
	BlendModeColorDodge:      "ColorDodge",
	BlendModeColorBurn:       "ColorBurn",
	BlendModeHardLight:       "HardLight",
	BlendModeSoftLight:       "SoftLight",
	BlendModeDifference:      "Difference",
	BlendModeExclusion:       "Exclusion",
	BlendModeHue:             "Hue",
	BlendModeSaturation:      "Saturation",
	BlendModeColor:           "Color",
	BlendModeLuminosity:      "Luminosity",
}

// premulBlendFunc combines premultiplied source and destination bytes.
//
// All inputs and outputs are premultiplied alpha in [0,255], matching the
// format of PixmapTarget pixels. Coverage must be applied by the caller.
type premulBlendFunc func(sr, sg, sb, sa, dr, dg, db, da uint8) (r, g, b, a uint8)

// blendTable maps a BlendMode to its premultiplied pixel function.
var blendTable = [...]premulBlendFunc{
	BlendModeSourceOver:      blendSourceOver,
	BlendModeClear:           blendClear,
	BlendModeCopy:            blendCopy,
	BlendModeDestination:     blendDestination,
	BlendModeDestinationOver: blendDestinationOver,
	BlendModeSourceIn:        blendSourceIn,
	BlendModeDestinationIn:   blendDestinationIn,
	BlendModeSourceOut:       blendSourceOut,
	BlendModeDestinationOut:  blendDestinationOut,
	BlendModeSourceAtop:      blendSourceAtop,
	BlendModeDestinationAtop: blendDestinationAtop,
	BlendModeXor:             blendXor,
	BlendModePlus:            blendPlus,
	BlendModeModulate:        blendModulate,
	BlendModeMultiply:        blendMultiply,
	BlendModeScreen:          blendScreen,
	BlendModeOverlay:         blendOverlay,
	BlendModeDarken:          blendDarken,
	BlendModeLighten:         blendLighten,
	BlendModeColorDodge:      blendColorDodge,
	BlendModeColorBurn:       blendColorBurn,
	BlendModeHardLight:       blendHardLight,
	BlendModeSoftLight:       blendSoftLight,
	BlendModeDifference:      blendDifference,
	BlendModeExclusion:       blendExclusion,
	BlendModeHue:             blendHue,
	BlendModeSaturation:      blendSaturation,
	BlendModeColor:           blendColor,
	BlendModeLuminosity:      blendLuminosity,
}

// premulBlendFuncFor returns the blend function for m.
// The second result is false when m is not a supported mode; callers must
// either report ErrUnsupportedBlendMode or normalize explicitly.
func premulBlendFuncFor(m BlendMode) (premulBlendFunc, bool) {
	if !m.IsValid() {
		return nil, false
	}
	return blendTable[m], true
}

// --- Byte math (premultiplied) ---

// mulDiv255 multiplies two bytes and divides by 255, rounding to nearest.
func mulDiv255(a, b uint8) uint8 {
	return uint8((uint16(a)*uint16(b) + 127) / 255)
}

// mulDiv255Floor multiplies two bytes and divides by 255 with truncation,
// matching the common (a*b+255)>>8 pipeline where needed.
func mulDiv255Floor(a, b uint8) uint8 {
	return uint8((uint16(a)*uint16(b) + 255) >> 8) //nolint:gosec // bounded by >>8
}

// addClamp adds two bytes, clamping to 255.
func addClamp(a, b uint8) uint8 {
	sum := uint16(a) + uint16(b)
	if sum > 255 {
		return 255
	}
	return uint8(sum)
}

// lerpByte blends a and b by t/255, rounding to nearest.
func lerpByte(a, b, t uint8) uint8 {
	diff := int16(b) - int16(a)
	v := int16(a) + int16((int32(diff)*int32(t)+127)/255)
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// clampOpacity coerces an opacity value into [0,1]. Non-finite values and NaN
// resolve to 1 (fully opaque), the historical default, so a bad paint value
// can never zero out a target unexpectedly or leak NaN into indexing math.
func clampOpacity(o float64) float64 {
	if math.IsNaN(o) {
		return 1
	}
	if o < 0 {
		return 0
	}
	if o > 1 || math.IsInf(o, 1) {
		return 1
	}
	return o
}

// floatToByte converts a [0,1] float to a rounded byte, mapping non-finite
// values and NaN to a deterministic in-range byte.
func floatToByte(v float32) uint8 {
	if v != v { // NaN
		return 0
	}
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 255
	}
	return uint8(v*255 + 0.5)
}

// --- Porter-Duff operators ---

func blendClear(_, _, _, _, _, _, _, _ uint8) (r, g, b, a uint8) { return 0, 0, 0, 0 }

func blendCopy(sr, sg, sb, sa, _, _, _, _ uint8) (r, g, b, a uint8) { return sr, sg, sb, sa }

func blendDestination(_, _, _, _, dr, dg, db, da uint8) (r, g, b, a uint8) {
	return dr, dg, db, da
}

func blendSourceOver(sr, sg, sb, sa, dr, dg, db, da uint8) (r, g, b, a uint8) {
	inv := 255 - sa
	return addClamp(sr, mulDiv255(dr, inv)),
		addClamp(sg, mulDiv255(dg, inv)),
		addClamp(sb, mulDiv255(db, inv)),
		addClamp(sa, mulDiv255(da, inv))
}

func blendDestinationOver(sr, sg, sb, sa, dr, dg, db, da uint8) (r, g, b, a uint8) {
	inv := 255 - da
	return addClamp(mulDiv255(sr, inv), dr),
		addClamp(mulDiv255(sg, inv), dg),
		addClamp(mulDiv255(sb, inv), db),
		addClamp(mulDiv255(sa, inv), da)
}

func blendSourceIn(sr, sg, sb, sa, _, _, _, da uint8) (r, g, b, a uint8) {
	return mulDiv255(sr, da), mulDiv255(sg, da), mulDiv255(sb, da), mulDiv255(sa, da)
}

func blendDestinationIn(_, _, _, sa, dr, dg, db, da uint8) (r, g, b, a uint8) {
	return mulDiv255(dr, sa), mulDiv255(dg, sa), mulDiv255(db, sa), mulDiv255(da, sa)
}

func blendSourceOut(sr, sg, sb, sa, _, _, _, da uint8) (r, g, b, a uint8) {
	inv := 255 - da
	return mulDiv255(sr, inv), mulDiv255(sg, inv), mulDiv255(sb, inv), mulDiv255(sa, inv)
}

func blendDestinationOut(_, _, _, sa, dr, dg, db, da uint8) (r, g, b, a uint8) {
	inv := 255 - sa
	return mulDiv255(dr, inv), mulDiv255(dg, inv), mulDiv255(db, inv), mulDiv255(da, inv)
}

func blendSourceAtop(sr, sg, sb, sa, dr, dg, db, da uint8) (r, g, b, a uint8) {
	inv := 255 - sa
	return addClamp(mulDiv255(sr, da), mulDiv255(dr, inv)),
		addClamp(mulDiv255(sg, da), mulDiv255(dg, inv)),
		addClamp(mulDiv255(sb, da), mulDiv255(db, inv)),
		da
}

func blendDestinationAtop(sr, sg, sb, sa, dr, dg, db, da uint8) (r, g, b, a uint8) {
	inv := 255 - da
	return addClamp(mulDiv255(sr, inv), mulDiv255(dr, sa)),
		addClamp(mulDiv255(sg, inv), mulDiv255(dg, sa)),
		addClamp(mulDiv255(sb, inv), mulDiv255(db, sa)),
		sa
}

func blendXor(sr, sg, sb, sa, dr, dg, db, da uint8) (r, g, b, a uint8) {
	invD := 255 - da
	invS := 255 - sa
	return addClamp(mulDiv255(sr, invD), mulDiv255(dr, invS)),
		addClamp(mulDiv255(sg, invD), mulDiv255(dg, invS)),
		addClamp(mulDiv255(sb, invD), mulDiv255(db, invS)),
		addClamp(mulDiv255(sa, invD), mulDiv255(da, invS))
}

func blendPlus(sr, sg, sb, sa, dr, dg, db, da uint8) (r, g, b, a uint8) {
	return addClamp(sr, dr), addClamp(sg, dg), addClamp(sb, db), addClamp(sa, da)
}

// blendModulate multiplies the premultiplied channels, matching the
// Porter-Duff "source in destination" product used by gg's Modulate mode.
func blendModulate(sr, sg, sb, sa, dr, dg, db, da uint8) (r, g, b, a uint8) {
	return mulDiv255(sr, dr), mulDiv255(sg, dg), mulDiv255(sb, db), mulDiv255(sa, da)
}

// --- Separable blend modes ---

// sepChan computes one W3C blend channel on straight-alpha float values in
// [0,1] (source s, backdrop d) and returns the blended channel in [0,1].
type sepChan func(s, d float64) float64

// separableBlend applies the W3C separable formula using float64 throughout
// for the color math, which keeps semi-transparent unpremultiplication precise:
//
//	Co = αs(1-αb)Cs + αb(1-αs)Cb + αsαb B(Cb, Cs)
//	αo = αs + αb(1-αs)
//
// Inputs are premultiplied bytes from the target/source.
func separableBlend(sr, sg, sb, sa, dr, dg, db, da uint8, chanFn sepChan) (r, g, b, a uint8) {
	as := float64(sa) / 255
	ab := float64(da) / 255
	if as == 0 {
		return dr, dg, db, da
	}
	if ab == 0 {
		return sr, sg, sb, sa
	}

	// Unpremultiply without re-quantizing the straight channels to bytes.
	csR := float64(sr) / float64(sa)
	csG := float64(sg) / float64(sa)
	csB := float64(sb) / float64(sa)
	cbR := float64(dr) / float64(da)
	cbG := float64(dg) / float64(da)
	cbB := float64(db) / float64(da)

	br := chanFn(csR, cbR)
	bg := chanFn(csG, cbG)
	bb := chanFn(csB, cbB)

	coR := as*(1-ab)*csR + ab*(1-as)*cbR + as*ab*br
	coG := as*(1-ab)*csG + ab*(1-as)*cbG + as*ab*bg
	coB := as*(1-ab)*csB + ab*(1-as)*cbB + as*ab*bb
	ao := as + ab*(1-as)

	return f2b(coR), f2b(coG), f2b(coB), f2b(ao)
}

// f2b clamps/rounds a [0,1] float color to a byte deterministically.
func f2b(v float64) uint8 {
	if v != v || v <= 0 { // NaN guard
		return 0
	}
	if v >= 1 {
		return 255
	}
	return uint8(v*255 + 0.5)
}

func blendMultiply(sr, sg, sb, sa, dr, dg, db, da uint8) (r, g, b, a uint8) {
	return separableBlend(sr, sg, sb, sa, dr, dg, db, da, func(s, d float64) float64 {
		return s * d
	})
}

func blendScreen(sr, sg, sb, sa, dr, dg, db, da uint8) (r, g, b, a uint8) {
	return separableBlend(sr, sg, sb, sa, dr, dg, db, da, func(s, d float64) float64 {
		return s + d - s*d
	})
}

func blendOverlay(sr, sg, sb, sa, dr, dg, db, da uint8) (r, g, b, a uint8) {
	return separableBlend(sr, sg, sb, sa, dr, dg, db, da, overlayChannelF)
}

// overlayChannelF is the W3C overlay channel: the backdrop decides multiply
// vs screen.
func overlayChannelF(s, d float64) float64 {
	if d <= 0.5 {
		return 2 * s * d
	}
	return 1 - 2*(1-s)*(1-d)
}

func blendDarken(sr, sg, sb, sa, dr, dg, db, da uint8) (r, g, b, a uint8) {
	return separableBlend(sr, sg, sb, sa, dr, dg, db, da, math.Min)
}

func blendLighten(sr, sg, sb, sa, dr, dg, db, da uint8) (r, g, b, a uint8) {
	return separableBlend(sr, sg, sb, sa, dr, dg, db, da, math.Max)
}

func blendColorDodge(sr, sg, sb, sa, dr, dg, db, da uint8) (r, g, b, a uint8) {
	return separableBlend(sr, sg, sb, sa, dr, dg, db, da, func(s, d float64) float64 {
		if s >= 1 {
			return 1
		}
		return math.Min(1, d/(1-s))
	})
}

func blendColorBurn(sr, sg, sb, sa, dr, dg, db, da uint8) (r, g, b, a uint8) {
	return separableBlend(sr, sg, sb, sa, dr, dg, db, da, func(s, d float64) float64 {
		if s <= 0 {
			return 0
		}
		return 1 - math.Min(1, (1-d)/s)
	})
}

func blendHardLight(sr, sg, sb, sa, dr, dg, db, da uint8) (r, g, b, a uint8) {
	return separableBlend(sr, sg, sb, sa, dr, dg, db, da, func(s, d float64) float64 {
		// Hard-light is overlay with source and backdrop swapped.
		return overlayChannelF(d, s)
	})
}

func blendSoftLight(sr, sg, sb, sa, dr, dg, db, da uint8) (r, g, b, a uint8) {
	return separableBlend(sr, sg, sb, sa, dr, dg, db, da, softLightChannelF)
}

func softLightChannelF(s, d float64) float64 {
	if s <= 0.5 {
		return d - (1-2*s)*d*(1-d)
	}
	var curve float64
	if d <= 0.25 {
		curve = ((16*d-12)*d + 4) * d
	} else {
		curve = math.Sqrt(d)
	}
	return d + (2*s-1)*(curve-d)
}

func blendDifference(sr, sg, sb, sa, dr, dg, db, da uint8) (r, g, b, a uint8) {
	return separableBlend(sr, sg, sb, sa, dr, dg, db, da, func(s, d float64) float64 {
		return math.Abs(s - d)
	})
}

func blendExclusion(sr, sg, sb, sa, dr, dg, db, da uint8) (r, g, b, a uint8) {
	return separableBlend(sr, sg, sb, sa, dr, dg, db, da, func(s, d float64) float64 {
		return s + d - 2*s*d
	})
}

// --- Non-separable HSL blend modes ---

func blendHue(sr, sg, sb, sa, dr, dg, db, da uint8) (r, g, b, a uint8) {
	return nonSeparableBlend(sr, sg, sb, sa, dr, dg, db, da, hslHueBlend)
}

func blendSaturation(sr, sg, sb, sa, dr, dg, db, da uint8) (r, g, b, a uint8) {
	return nonSeparableBlend(sr, sg, sb, sa, dr, dg, db, da, hslSaturationBlend)
}

func blendColor(sr, sg, sb, sa, dr, dg, db, da uint8) (r, g, b, a uint8) {
	return nonSeparableBlend(sr, sg, sb, sa, dr, dg, db, da, hslColorBlend)
}

func blendLuminosity(sr, sg, sb, sa, dr, dg, db, da uint8) (r, g, b, a uint8) {
	return nonSeparableBlend(sr, sg, sb, sa, dr, dg, db, da, hslLuminosityBlend)
}

// nonSeparableBlend applies the W3C non-separable formula. The blend function
// operates on straight (unpremultiplied) float colors in [0,1].
func nonSeparableBlend(
	sr, sg, sb, sa, dr, dg, db, da uint8,
	fn func(sr, sg, sb, dr, dg, db float32) (float32, float32, float32),
) (r, g, b, a uint8) {
	if sa == 0 {
		return dr, dg, db, da
	}
	if da == 0 {
		return sr, sg, sb, sa
	}

	suR := float32(sr) / float32(sa)
	suG := float32(sg) / float32(sa)
	suB := float32(sb) / float32(sa)
	duR := float32(dr) / float32(da)
	duG := float32(dg) / float32(da)
	duB := float32(db) / float32(da)

	blR, blG, blB := fn(suR, suG, suB, duR, duG, duB)

	invS := 255 - sa
	invD := 255 - da
	outA := addClamp(sa, mulDiv255(da, invS))

	outR := addClamp(mulDiv255(dr, invS), mulDiv255(sr, invD))
	outG := addClamp(mulDiv255(dg, invS), mulDiv255(sg, invD))
	outB := addClamp(mulDiv255(db, invS), mulDiv255(sb, invD))

	saDa := float32(sa) / 255 * float32(da) / 255
	outR = addClamp(outR, floatToByte(blR*saDa))
	outG = addClamp(outG, floatToByte(blG*saDa))
	outB = addClamp(outB, floatToByte(blB*saDa))

	return outR, outG, outB, outA
}

func hslLuminosity(r, g, b float32) float32 { return 0.30*r + 0.59*g + 0.11*b }

func hslSaturation(r, g, b float32) float32 {
	mx := max3(r, g, b)
	mn := min3(r, g, b)
	return mx - mn
}

func max3(r, g, b float32) float32 {
	m := r
	if g > m {
		m = g
	}
	if b > m {
		m = b
	}
	return m
}

func min3(r, g, b float32) float32 {
	m := r
	if g < m {
		m = g
	}
	if b < m {
		m = b
	}
	return m
}

func hslClipColor(r, g, b float32) (float32, float32, float32) {
	l := hslLuminosity(r, g, b)
	mn := min3(r, g, b)
	mx := max3(r, g, b)
	if mn < 0 {
		d := l - mn
		if d != 0 {
			r = l + (r-l)*l/d
			g = l + (g-l)*l/d
			b = l + (b-l)*l/d
		}
	}
	if mx > 1 {
		d := mx - l
		if d != 0 {
			r = l + (r-l)*(1-l)/d
			g = l + (g-l)*(1-l)/d
			b = l + (b-l)*(1-l)/d
		}
	}
	return r, g, b
}

func hslSetLum(r, g, b, l float32) (float32, float32, float32) {
	d := l - hslLuminosity(r, g, b)
	return hslClipColor(r+d, g+d, b+d)
}

func hslSetSat(r, g, b, s float32) (float32, float32, float32) {
	minP, midP, maxP := &r, &g, &b
	if *minP > *midP {
		minP, midP = midP, minP
	}
	if *midP > *maxP {
		midP, maxP = maxP, midP
	}
	if *minP > *midP {
		minP, midP = midP, minP
	}
	if *maxP > *minP {
		*midP = ((*midP - *minP) * s) / (*maxP - *minP)
		*maxP = s
		*minP = 0
	}
	return r, g, b
}

func hslHueBlend(sr, sg, sb, dr, dg, db float32) (float32, float32, float32) {
	r, g, b := hslSetSat(sr, sg, sb, hslSaturation(dr, dg, db))
	return hslSetLum(r, g, b, hslLuminosity(dr, dg, db))
}

func hslSaturationBlend(sr, sg, sb, dr, dg, db float32) (float32, float32, float32) {
	r, g, b := hslSetSat(dr, dg, db, hslSaturation(sr, sg, sb))
	return hslSetLum(r, g, b, hslLuminosity(dr, dg, db))
}

func hslColorBlend(sr, sg, sb, dr, dg, db float32) (float32, float32, float32) {
	return hslSetLum(sr, sg, sb, hslLuminosity(dr, dg, db))
}

func hslLuminosityBlend(sr, sg, sb, dr, dg, db float32) (float32, float32, float32) {
	// Luminosity: backdrop hue/saturation with source luminosity.
	return hslSetLum(dr, dg, db, hslLuminosity(sr, sg, sb))
}

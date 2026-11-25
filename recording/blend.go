package recording

import "github.com/gogpu/gg"

// BlendMode identifies a compositing/blend operator for recorded draws.
//
// It is an alias of gg.BlendMode so numeric values and names match the public
// gg constants (W3C Compositing and Blending plus Porter-Duff operators).
// Recordings store the numeric mode; backends translate it to their native
// representation.
type BlendMode = gg.BlendMode

// Blend mode constants re-exported from gg so recording call sites can use a
// single package qualifier. Values are the public gg enum values.
const (
	BlendNormal          = gg.BlendNormal
	BlendSourceOver      = gg.BlendSourceOver
	BlendMultiply        = gg.BlendMultiply
	BlendScreen          = gg.BlendScreen
	BlendOverlay         = gg.BlendOverlay
	BlendDarken          = gg.BlendDarken
	BlendLighten         = gg.BlendLighten
	BlendColorDodge      = gg.BlendColorDodge
	BlendColorBurn       = gg.BlendColorBurn
	BlendHardLight       = gg.BlendHardLight
	BlendSoftLight       = gg.BlendSoftLight
	BlendDifference      = gg.BlendDifference
	BlendExclusion       = gg.BlendExclusion
	BlendHue             = gg.BlendHue
	BlendSaturation      = gg.BlendSaturation
	BlendColor           = gg.BlendColor
	BlendLuminosity      = gg.BlendLuminosity
	BlendDestinationOver = gg.BlendDestinationOver
	BlendSourceIn        = gg.BlendSourceIn
	BlendDestinationIn   = gg.BlendDestinationIn
	BlendSourceOut       = gg.BlendSourceOut
	BlendDestinationOut  = gg.BlendDestinationOut
	BlendSourceAtop      = gg.BlendSourceAtop
	BlendDestinationAtop = gg.BlendDestinationAtop
	BlendXor             = gg.BlendXor
	BlendPlus            = gg.BlendPlus
	BlendModulate        = gg.BlendModulate
	BlendClear           = gg.BlendClear
	BlendSource          = gg.BlendSource
	BlendCopy            = gg.BlendCopy
	BlendDestination     = gg.BlendDestination
)

// BlendModeBackend is an optional Backend capability implemented by backends
// that can apply non-default blend modes.
//
// Playback dispatches SetBlendModeCommand through this interface; a backend
// that does not implement it simply keeps source-over rather than failing the
// whole replay.
type BlendModeBackend interface {
	// SetBlendMode sets the blend/composite mode for subsequent drawing.
	// The default is gg.BlendNormal (source-over). Save/Restore must restore
	// the prior blend mode.
	SetBlendMode(mode BlendMode)
}

// SetBlendModeCommand changes the blend mode for subsequent commands.
// Blend mode is dispatch state: it applies until changed or restored by a
// surrounding Save/Restore.
type SetBlendModeCommand struct {
	// Mode is the blend mode to apply.
	Mode BlendMode
}

// Type implements Command.
func (SetBlendModeCommand) Type() CommandType { return CmdSetBlendMode }

// SetBlendMode records a blend-mode change. The default mode is
// gg.BlendNormal. Blend mode participates in Save/Restore for backends that
// preserve it across their own state stack.
func (r *Recorder) SetBlendMode(mode BlendMode) {
	r.commands = append(r.commands, SetBlendModeCommand{Mode: mode})
}

// AllBlendModes returns the stable list of named public gg blend modes.
// The slice is freshly allocated and ordered for reproducible reporting.
func AllBlendModes() []BlendMode {
	return []BlendMode{
		gg.BlendNormal,
		gg.BlendMultiply,
		gg.BlendScreen,
		gg.BlendOverlay,
		gg.BlendDarken,
		gg.BlendLighten,
		gg.BlendColorDodge,
		gg.BlendColorBurn,
		gg.BlendHardLight,
		gg.BlendSoftLight,
		gg.BlendDifference,
		gg.BlendExclusion,
		gg.BlendHue,
		gg.BlendSaturation,
		gg.BlendColor,
		gg.BlendLuminosity,
		gg.BlendDestinationOver,
		gg.BlendSourceIn,
		gg.BlendDestinationIn,
		gg.BlendSourceOut,
		gg.BlendDestinationOut,
		gg.BlendSourceAtop,
		gg.BlendDestinationAtop,
		gg.BlendXor,
		gg.BlendPlus,
		gg.BlendModulate,
		gg.BlendClear,
		gg.BlendCopy,
		gg.BlendSource,
		gg.BlendDestination,
	}
}

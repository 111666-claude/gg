package recording

import "github.com/gogpu/gg"

// BlendMode identifies a compositing/blend operator.
//
// It is an alias of gg.BlendMode so numeric values match the public gg
// constants (W3C Compositing and Blending Level 1 plus Porter-Duff
// operators). Recordings store the numeric mode and backends translate it
// to their native representation.
type BlendMode = gg.BlendMode

// BlendModeBackend is an optional Backend capability implemented by backends
// that can apply non-default blend modes.
//
// Playback dispatches SetBlendModeCommand to this method only when the
// backend implements the interface. Backends that do not implement it cannot
// claim the CapBlendModes capability, and the capability gate reports blend
// mode commands through their fallback/unsupported path instead.
type BlendModeBackend interface {
	// SetBlendMode sets the blend/composite mode for subsequent drawing.
	// The default mode is gg.BlendNormal (source-over). Save/Restore managed
	// by the backend must restore the previous blend mode.
	SetBlendMode(mode BlendMode)
}

// SetBlendModeCommand sets the blend/composite mode for subsequent commands.
//
// Unlike style commands that are captured per drawing command, the blend
// mode is dispatch state: it applies until changed or restored by a
// surrounding Save/Restore pair.
type SetBlendModeCommand struct {
	// Mode is the blend mode to apply.
	Mode BlendMode
}

// Type implements Command.
func (SetBlendModeCommand) Type() CommandType { return CmdSetBlendMode }

// SetBlendMode records a blend-mode change.
//
// The default mode is gg.BlendNormal. Blend mode participates in the
// backend's Save/Restore state stack: backends restore the prior mode on
// Restore.
func (r *Recorder) SetBlendMode(mode BlendMode) {
	r.commands = append(r.commands, SetBlendModeCommand{Mode: mode})
}

// AllBlendModes returns the complete, stable list of named public gg blend
// modes (W3C separable/non-separable operators plus Porter-Duff modes).
// The returned slice is a fresh copy and is ordered by the public numeric
// value so JSONL reports are reproducible.
func AllBlendModes() []BlendMode {
	return []BlendMode{
		gg.BlendNormal,          // 0
		gg.BlendMultiply,        // 1
		gg.BlendScreen,          // 2
		gg.BlendOverlay,         // 3
		gg.BlendDestinationOver, // 4
		gg.BlendSourceIn,        // 5
		gg.BlendDestinationIn,   // 6
		gg.BlendSourceOut,       // 7
		gg.BlendDestinationOut,  // 8
		gg.BlendSourceAtop,      // 9
		gg.BlendDestinationAtop, // 10
		gg.BlendXor,             // 11
		gg.BlendPlus,            // 12
		gg.BlendModulate,        // 13
		gg.BlendDarken,          // 17
		gg.BlendLighten,         // 18
		gg.BlendColorDodge,      // 19
		gg.BlendColorBurn,       // 20
		gg.BlendHardLight,       // 21
		gg.BlendSoftLight,       // 22
		gg.BlendDifference,      // 23
		gg.BlendExclusion,       // 24
		gg.BlendHue,             // 25
		gg.BlendSaturation,      // 26
		gg.BlendColor,           // 27
		gg.BlendLuminosity,      // 28
		gg.BlendClear,           // 29
		gg.BlendSource,          // 30
		gg.BlendDestination,     // 31
	}
}

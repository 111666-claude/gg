package recording

import (
	"errors"
	"fmt"
)

// Playback errors. All of them are wrapped in [PlaybackError] with the
// zero-based index of the command that triggered them, so callers can map a
// failure back to a recording position.
var (
	// ErrNilBackend is returned when playback is attempted with a nil backend.
	ErrNilBackend = errors.New("recording: nil backend")

	// ErrUnknownCommand is returned when a recording contains a command type
	// this package cannot play back. Unknown commands are rejected before
	// Begin, so no output is produced.
	ErrUnknownCommand = errors.New("recording: unknown command")

	// ErrCapabilityConflict is returned for contradictory capability
	// declarations, such as a fallback for a natively supported command.
	ErrCapabilityConflict = errors.New("recording: capability declaration conflict")

	// ErrFallbackRecursion is returned when a backend's fallback chain cycles.
	ErrFallbackRecursion = errors.New("recording: fallback recursion")

	// ErrUnsupportedCapability is returned when the backend cannot execute a
	// command and the selected [PlaybackPolicy] cannot recover.
	ErrUnsupportedCapability = errors.New("recording: backend lacks required capability")

	// ErrMissingResource is returned when a command references a path, brush
	// or image that is not in the recording's resource pool.
	ErrMissingResource = errors.New("recording: missing resource")

	// ErrInvalidPolicy is returned for an unknown [PlaybackPolicy] value.
	ErrInvalidPolicy = errors.New("recording: invalid playback policy")
)

// PlaybackError attaches a command position to a playback failure.
//
// Index is the zero-based index into [Recording.Commands]. It is -1 when the
// failure is not tied to a command (for example a nil backend or a Begin
// failure). Command is the command type at that index, when known.
type PlaybackError struct {
	// Index is the zero-based command index, or -1 when not applicable.
	Index int
	// Command is the command type at Index, if known.
	Command CommandType
	// HasCommand reports whether Command is meaningful.
	HasCommand bool
	// Err is the underlying error.
	Err error
}

// Error implements the error interface.
func (e *PlaybackError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.HasCommand {
		return fmt.Sprintf("recording: playback command %d (%s): %v", e.Index, e.Command, e.Err)
	}
	if e.Index >= 0 {
		return fmt.Sprintf("recording: playback command %d: %v", e.Index, e.Err)
	}
	return fmt.Sprintf("recording: playback: %v", e.Err)
}

// Unwrap returns the underlying error.
func (e *PlaybackError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// UnsupportedError reports the capability gaps found by playback or preflight.
// It is wrapped by ErrUnsupportedCapability.
type UnsupportedError struct {
	// Report is the preflight report describing every missing capability and
	// the commands that need it.
	Report *CapabilityReport
}

// Error implements the error interface.
func (e *UnsupportedError) Error() string {
	if e == nil || e.Report == nil {
		return ErrUnsupportedCapability.Error()
	}
	return ErrUnsupportedCapability.Error() + ": " + e.Report.String()
}

// Unwrap returns ErrUnsupportedCapability.
func (e *UnsupportedError) Unwrap() error {
	return ErrUnsupportedCapability
}

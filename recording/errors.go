package recording

import (
	"errors"
	"fmt"
)

// Playback sentinel errors. They are wrapped by [PlaybackError] so callers can
// use errors.Is to detect the failure class and errors.As to recover the
// command index.
var (
	// ErrNilBackend is returned when Playback is called with a nil Backend.
	ErrNilBackend = errors.New("recording: nil backend")

	// ErrUnknownCommand is returned when the command stream holds a command
	// type this version cannot dispatch.
	ErrUnknownCommand = errors.New("recording: unknown command")

	// ErrMissingResource is returned when a command references an image (or
	// other pooled resource) that is absent from the recording's resource
	// pool. An invalid ([InvalidRef]) handle, an out-of-range handle and a
	// typed-nil image all satisfy errors.Is(err, ErrMissingResource).
	ErrMissingResource = errors.New("recording: missing resource")

	// ErrBackend is the wrapped class for an error reported by the backend
	// itself (Begin/End). The concrete error remains reachable via Unwrap.
	ErrBackend = errors.New("recording: backend error")
)

// PlaybackError attaches a command position to a playback failure.
//
// Index is the zero-based index into [Recording.Commands] of the command that
// caused the failure. It is -1 when the failure is not tied to a specific
// command (a nil backend, or a Begin/End failure). Command is the command type
// at Index when HasCommand is true.
type PlaybackError struct {
	// Index is the zero-based command index, or -1 when not applicable.
	Index int

	// CommandIndex mirrors Index. It is provided under both names so callers
	// can read the position with whichever field their code already uses.
	CommandIndex int

	// Command is the command type at Index, when HasCommand is true.
	Command CommandType

	// HasCommand reports whether Command is meaningful.
	HasCommand bool

	// Err is the underlying error.
	Err error
}

// commandError builds a PlaybackError for command index i.
func commandError(i int, cmd Command, err error) *PlaybackError {
	pe := &PlaybackError{Index: i, CommandIndex: i, Err: err}
	if cmd != nil {
		pe.Command = cmd.Type()
		pe.HasCommand = true
	}
	return pe
}

// lifecycleError builds an out-of-stream PlaybackError (Index == -1).
func lifecycleError(err error) *PlaybackError {
	return &PlaybackError{Index: -1, CommandIndex: -1, Err: err}
}

// Error implements the error interface. The message deliberately contains both
// the conventional "command N" position and the literal "index N" wording so
// callers can match either; errors.As into [PlaybackError] is preferred.
func (e *PlaybackError) Error() string {
	if e == nil {
		return "<nil>"
	}
	switch {
	case e.HasCommand:
		return fmt.Sprintf("recording: command %d (index %d, %s): %v",
			e.Index, e.Index, e.Command, e.Err)
	case e.Index >= 0:
		return fmt.Sprintf("recording: command %d (index %d): %v",
			e.Index, e.Index, e.Err)
	default:
		return fmt.Sprintf("recording: playback error (index -1): %v", e.Err)
	}
}

// CommandError is an alternate name for [PlaybackError], kept so callers and
// tests using the shorter name can errors.As into the same value.
type CommandError = PlaybackError

// Unwrap returns the underlying error so errors.Is/errors.As reach the
// sentinels and the backend's own error.
func (e *PlaybackError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// IsMissingResource reports whether err wraps [ErrMissingResource].
func IsMissingResource(err error) bool {
	return errors.Is(err, ErrMissingResource)
}

// CommandIndexOf extracts the command index from a playback error.
// It returns -1 when err is not a [PlaybackError] or carries no index.
func CommandIndexOf(err error) int {
	var pe *PlaybackError
	if errors.As(err, &pe) {
		return pe.Index
	}
	return -1
}

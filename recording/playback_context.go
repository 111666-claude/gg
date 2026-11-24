package recording

import (
	"context"
	"fmt"

	"github.com/gogpu/gg"
)

// ErrorReporter is an optional Backend interface for backends whose drawing
// methods cannot return errors directly (the historical Backend methods are
// all void).
//
// A backend that fails while processing a command stores the failure and
// returns it from Err. PlaybackContext polls Err after every command and,
// on the first non-nil result, stops and returns a located PlaybackError.
// Implementations should clear no state; Err is expected to be sticky.
type ErrorReporter interface {
	// Err returns the first processing error, or nil.
	Err() error
}

// Playback phase identifiers used by PlaybackError.Phase.
const (
	PlaybackPhaseBegin   = "begin"
	PlaybackPhaseCommand = "command"
	PlaybackPhaseEnd     = "end"
	PlaybackPhaseCancel  = "canceled"
)

// PlaybackError locates a playback failure to a phase and, for command
// failures, the exact Recording command index and type.
type PlaybackError struct {
	// Phase is one of the PlaybackPhase* constants.
	Phase string
	// Index is the zero-based command index for PhaseCommand, else -1.
	Index int
	// Command is the command type for PhaseCommand.
	Command CommandType
	// Err is the wrapped error.
	Err error
}

// Error implements error.
func (e *PlaybackError) Error() string {
	switch e.Phase {
	case PlaybackPhaseCommand:
		return fmt.Sprintf("recording: playback failed at command #%d (%s): %v",
			e.Index, e.Command, e.Err)
	case PlaybackPhaseBegin, PlaybackPhaseEnd:
		return fmt.Sprintf("recording: playback %s failed: %v", e.Phase, e.Err)
	case PlaybackPhaseCancel:
		return fmt.Sprintf("recording: playback canceled before command #%d: %v", e.Index, e.Err)
	default:
		return fmt.Sprintf("recording: playback failed (%s): %v", e.Phase, e.Err)
	}
}

// Unwrap returns the underlying error for errors.Is/As.
func (e *PlaybackError) Unwrap() error { return e.Err }

// PlaybackContext replays the recording to backend, honoring ctx cancellation
// at command boundaries.
//
// (Playback is the context-free wrapper retained in recorder.go.)
//
// Semantics:
//
//   - If ctx is already canceled, no backend method is called and ctx.Err()
//     is returned (wrapped in a PlaybackError with PhaseCancel).
//   - Begin errors are returned as a PhaseBegin PlaybackError.
//   - After every command, if backend implements ErrorReporter and Err is
//     non-nil, playback stops and a PhaseCommand PlaybackError carrying the
//     command index/type is returned.
//   - If ctx is canceled mid-recording, dispatch stops at the next command
//     boundary; End is still called for cleanup and the cancellation is
//     returned as a PhaseCancel PlaybackError.
//   - End errors are returned as a PhaseEnd PlaybackError.
//
// Playback never mutates the Recording or its resource pool.
func (r *Recording) PlaybackContext(ctx context.Context, backend Backend) error {
	if r == nil {
		return &PlaybackError{Phase: PlaybackPhaseBegin, Index: -1, Err: fmt.Errorf("recording is nil")}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return &PlaybackError{Phase: PlaybackPhaseCancel, Index: 0, Err: err}
	}

	if err := backend.Begin(r.width, r.height); err != nil {
		return &PlaybackError{Phase: PlaybackPhaseBegin, Index: -1, Err: err}
	}

	transform := Identity()
	transformStack := make([]Matrix, 0, 8)

	pollErr := func(i int, cmd Command) error {
		if reporter, ok := backend.(ErrorReporter); ok {
			if err := reporter.Err(); err != nil {
				ct := CommandType(0)
				if cmd != nil {
					ct = cmd.Type()
				}
				return &PlaybackError{
					Phase:   PlaybackPhaseCommand,
					Index:   i,
					Command: ct,
					Err:     err,
				}
			}
		}
		return nil
	}

	// Replay each command.
	for i, cmd := range r.commands {
		if err := ctx.Err(); err != nil {
			// Best-effort finalization so backends can release resources.
			_ = backend.End()
			return &PlaybackError{Phase: PlaybackPhaseCancel, Index: i, Err: err}
		}

		switch c := cmd.(type) {
		case SaveCommand:
			transformStack = append(transformStack, transform)
			backend.Save()
		case RestoreCommand:
			backend.Restore()
			if len(transformStack) > 0 {
				transform = transformStack[len(transformStack)-1]
				transformStack = transformStack[:len(transformStack)-1]
			}
		case SetTransformCommand:
			transform = c.Matrix
			backend.SetTransform(c.Matrix)
		case SetClipCommand:
			path := r.resources.GetPath(c.Path)
			setPlaybackClip(backend, path, c.Rule, transform)
		case ClearClipCommand:
			backend.ClearClip()
		case ClipRoundRectCommand:
			rrPath := buildClipRoundRectPath(c, transform)
			setPlaybackClip(backend, rrPath, FillRuleNonZero, transform)
		case FillPathCommand:
			path := r.resources.GetPath(c.Path)
			brush := r.resources.GetBrush(c.Brush)
			backend.FillPath(path, brush, c.Rule)
		case StrokePathCommand:
			path := r.resources.GetPath(c.Path)
			brush := r.resources.GetBrush(c.Brush)
			backend.StrokePath(path, brush, c.Stroke)
		case FillRectCommand:
			brush := r.resources.GetBrush(c.Brush)
			backend.FillRect(c.Rect, brush)
		case StrokeRectCommand:
			brush := r.resources.GetBrush(c.Brush)
			path := gg.BuildPath().Rect(c.Rect.MinX, c.Rect.MinY, c.Rect.Width(), c.Rect.Height()).Build()
			// StrokeRectCommand stores world-space bounds. Isolate the identity
			// transform so backends do not apply the recording transform twice,
			// then restore their complete graphics state for later commands.
			backend.Save()
			backend.SetTransform(Identity())
			backend.StrokePath(path, brush, c.Stroke)
			backend.Restore()
		case DrawImageCommand:
			img := r.resources.GetImage(c.Image)
			backend.DrawImage(img, c.SrcRect, c.DstRect, c.Options)
		case DrawTextCommand:
			brush := r.resources.GetBrush(c.Brush)
			backend.DrawText(c.Text, c.X, c.Y, c.Face, brush)
		case StrokeTextCommand:
			brush := r.resources.GetBrush(c.Brush)
			// Core lowering: stroked text is currently delivered as DrawText.
			backend.DrawText(c.Text, c.X, c.Y, c.Face, brush)
		case SetBlendModeCommand:
			// Optional backend capability; backends without the interface
			// cannot declare CapBlendModes and the gate audits the gap.
			if bb, ok := backend.(BlendModeBackend); ok {
				bb.SetBlendMode(c.Mode)
			}
		// Style commands are handled by the backend's internal state
		// during the actual drawing operations.
		case SetFillStyleCommand, SetStrokeStyleCommand,
			SetLineWidthCommand, SetLineCapCommand, SetLineJoinCommand,
			SetMiterLimitCommand, SetDashCommand, SetFillRuleCommand,
			SetAntiAliasCommand:
			// State captured in the drawing commands themselves.
		}

		if err := pollErr(i, cmd); err != nil {
			_ = backend.End()
			return err
		}
	}

	if err := backend.End(); err != nil {
		return &PlaybackError{Phase: PlaybackPhaseEnd, Index: -1, Err: err}
	}
	if reporter, ok := backend.(ErrorReporter); ok {
		if err := reporter.Err(); err != nil {
			return &PlaybackError{Phase: PlaybackPhaseEnd, Index: len(r.commands) - 1, Err: err}
		}
	}
	return nil
}

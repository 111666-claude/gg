package recording

import (
	"fmt"
	"image"
	"reflect"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/text"
)

// PlaybackPolicy selects how playback handles commands the backend cannot
// execute.
type PlaybackPolicy uint8

const (
	// PolicyStrict fails before any output is produced when the backend
	// declares that it cannot execute a command. This is the default and
	// matches the behavior of backends that do not declare capabilities.
	PolicyStrict PlaybackPolicy = iota

	// PolicySkip skips unsupported commands, records each one in
	// [PlaybackResult.Skipped], and continues with the remaining commands.
	// The rest of the recording keeps its order and state stack, so later
	// supported commands still see the transform that is in effect.
	PolicySkip

	// PolicyDegrade rewrites unsupported commands into equivalent simpler
	// commands declared by the backend, for example FillRect into FillPath.
	// Playback fails when a command has no equivalent form.
	PolicyDegrade
)

// String returns the policy name.
func (p PlaybackPolicy) String() string {
	switch p {
	case PolicyStrict:
		return "strict"
	case PolicySkip:
		return "skip"
	case PolicyDegrade:
		return "degrade"
	default:
		return fmt.Sprintf("PlaybackPolicy(%d)", uint8(p))
	}
}

// PlaybackOptions configures [Recording.PlaybackWithOptions].
type PlaybackOptions struct {
	// Policy selects the recovery strategy for unsupported commands.
	// The zero value is [PolicyStrict].
	Policy PlaybackPolicy

	// OnSkip is called for each command skipped under [PolicySkip], in
	// playback order. It runs while playback is in progress and must not call
	// back into the recording or the backend.
	OnSkip func(SkippedCommand)
}

// SkippedCommand describes a command that [PolicySkip] did not play.
type SkippedCommand struct {
	// Index is the zero-based command index.
	Index int
	// Command is the command type.
	Command CommandType
	// Capability is the capability the backend did not declare.
	Capability Capability
}

// DegradedCommand describes a command that [PolicyDegrade] rewrote.
type DegradedCommand struct {
	// Index is the zero-based command index.
	Index int
	// Command is the original command type.
	Command CommandType
	// From is the capability the backend did not declare.
	From Capability
	// To is the equivalent capability used instead. CapNone means the command
	// was dropped because its effect is already carried by drawing commands.
	To Capability
}

// PlaybackResult reports what playback did with a recording.
type PlaybackResult struct {
	// Report is the capability check performed before playback started.
	Report *CapabilityReport
	// Skipped lists the commands not played, under [PolicySkip].
	Skipped []SkippedCommand
	// Degraded lists the commands rewritten to an equivalent form, under
	// [PolicyDegrade].
	Degraded []DegradedCommand
}

// Completeness reports whether every command was executed as recorded, with no
// skips and no degradation.
func (r *PlaybackResult) Completeness() bool {
	return r == nil || (len(r.Skipped) == 0 && len(r.Degraded) == 0)
}

// Playback replays the recording to the given backend using [PolicyStrict].
//
// It returns an error, including the command index, when the backend cannot
// execute a command, when the recording references a resource that is not in
// its pool, or when the recording contains an unknown command. All of these
// are detected before Begin, so a failed playback produces no output.
func (r *Recording) Playback(backend Backend) error {
	_, err := r.PlaybackWithOptions(backend, PlaybackOptions{})
	return err
}

// PlaybackWithOptions replays the recording to the given backend with an
// explicit recovery policy.
//
// The capability check runs before Begin. An error therefore means no output
// was produced, except for errors returned by Backend.Begin or Backend.End.
func (r *Recording) PlaybackWithOptions(backend Backend, opts PlaybackOptions) (*PlaybackResult, error) {
	plan, err := r.prepare(backend, opts)
	if err != nil {
		return nil, err
	}

	result := &PlaybackResult{Report: plan.report}

	if err := backend.Begin(r.width, r.height); err != nil {
		return result, &PlaybackError{Index: -1, Err: err}
	}

	p := &player{
		recording: r,
		backend:   backend,
		transform: Identity(),
		result:    result,
	}
	if err := p.run(plan, opts); err != nil {
		return result, err
	}

	if err := backend.End(); err != nil {
		return result, &PlaybackError{Index: -1, Err: fmt.Errorf("end: %w", err)}
	}
	return result, nil
}

// Preflight reports the capabilities the recording needs and the ones the
// backend does not declare. It does not touch the backend, so callers can
// inspect the gaps before starting playback.
//
// Preflight returns an error for a nil backend, an unknown command, or an
// invalid capability declaration (conflict or fallback recursion). Missing
// capabilities are reported through the returned [CapabilityReport], not as an
// error.
func (r *Recording) Preflight(backend Backend) (*CapabilityReport, error) {
	if err := checkBackend(backend); err != nil {
		return nil, err
	}
	perCommand, required, err := r.scanCommands()
	if err != nil {
		return nil, err
	}
	return r.buildReport(backend, perCommand, required)
}

// RequiredCapabilities returns the union of capabilities the recording needs.
// It returns an error for an unknown command.
func (r *Recording) RequiredCapabilities() (Capability, error) {
	_, required, err := r.scanCommands()
	return required, err
}

// Validate checks that the recording contains only known commands and that
// every referenced resource is present in the pool. It does not need a backend
// and does not take a policy into account.
func (r *Recording) Validate() error {
	for i, cmd := range r.commands {
		if _, ok := commandCapability(cmd); !ok {
			return &PlaybackError{
				Index: i,
				Err:   fmt.Errorf("%w: %T", ErrUnknownCommand, cmd),
			}
		}
		if err := r.checkResources(cmd); err != nil {
			return &PlaybackError{Index: i, Command: cmd.Type(), HasCommand: true, Err: err}
		}
	}
	return nil
}

// prepare validates the recording and backend and resolves, for every command,
// whether it is executed, skipped or degraded. It never calls the backend.
func (r *Recording) prepare(backend Backend, opts PlaybackOptions) (*playbackPlan, error) {
	if err := checkBackend(backend); err != nil {
		return nil, err
	}
	if opts.Policy > PolicyDegrade {
		return nil, &PlaybackError{Index: -1, Err: fmt.Errorf("%w: %s", ErrInvalidPolicy, opts.Policy)}
	}

	perCommand, required, err := r.scanCommands()
	if err != nil {
		return nil, err
	}

	report, err := r.buildReport(backend, perCommand, required)
	if err != nil {
		return nil, err
	}

	plan := &playbackPlan{
		report:  report,
		caps:    perCommand,
		actions: make([]commandAction, len(r.commands)),
		targets: make([]Capability, len(r.commands)),
	}

	native := report.Native
	for i, cmd := range r.commands {
		capability := perCommand[i]
		switch {
		case capability == CapNone || native.Contains(capability):
			// Executed as recorded.
		case opts.Policy == PolicySkip:
			plan.actions[i] = actionSkip
		case opts.Policy == PolicyDegrade && report.FallbackFor(capability) != nil:
			plan.actions[i] = actionDegrade
			plan.targets[i] = *report.FallbackFor(capability)
		default:
			return nil, &PlaybackError{
				Index:      i,
				Command:    cmd.Type(),
				HasCommand: true,
				Err:        &UnsupportedError{Report: report},
			}
		}
		if plan.actions[i] == actionExecute || plan.actions[i] == actionDegrade {
			if err := r.checkResources(cmd); err != nil {
				return nil, &PlaybackError{Index: i, Command: cmd.Type(), HasCommand: true, Err: err}
			}
		}
	}
	return plan, nil
}

// playbackPlan is the resolved decision for every command in a recording.
type playbackPlan struct {
	report  *CapabilityReport
	caps    []Capability
	actions []commandAction
	targets []Capability
}

type commandAction uint8

const (
	actionExecute commandAction = iota
	actionSkip
	actionDegrade
)

// player holds the transform state that mirrors the recording's state stack
// while commands are replayed.
type player struct {
	recording      *Recording
	backend        Backend
	transform      Matrix
	transformStack []Matrix
	result         *PlaybackResult
}

// run replays the planned commands in recording order. Only state commands are
// never skipped or degraded, so the backend's state stack stays balanced.
func (p *player) run(plan *playbackPlan, opts PlaybackOptions) error {
	for i, cmd := range p.recording.commands {
		switch plan.actions[i] {
		case actionSkip:
			skipped := SkippedCommand{Index: i, Command: cmd.Type(), Capability: plan.caps[i]}
			p.result.Skipped = append(p.result.Skipped, skipped)
			if opts.OnSkip != nil {
				opts.OnSkip(skipped)
			}
			continue
		case actionDegrade:
			p.result.Degraded = append(p.result.Degraded, DegradedCommand{
				Index:   i,
				Command: cmd.Type(),
				From:    plan.caps[i],
				To:      plan.targets[i],
			})
		}
		if err := p.apply(cmd, plan.actions[i] == actionDegrade); err != nil {
			return &PlaybackError{Index: i, Command: cmd.Type(), HasCommand: true, Err: err}
		}
	}
	return nil
}

// apply executes one command. degraded reports that the command should be
// replayed through its equivalent base command.
func (p *player) apply(cmd Command, degraded bool) error {
	switch c := cmd.(type) {
	case SaveCommand:
		p.save()

	case RestoreCommand:
		p.restore()

	case SetTransformCommand:
		p.transform = c.Matrix
		p.backend.SetTransform(c.Matrix)

	case SetClipCommand:
		return p.executeSetClip(c)

	case ClearClipCommand:
		p.backend.ClearClip()

	case ClipRoundRectCommand:
		// Native and degraded execution both go through SetClip: it is the
		// only clip operation the Backend interface exposes.
		rrPath := buildClipRoundRectPath(c, p.transform)
		setPlaybackClip(p.backend, rrPath, FillRuleNonZero, p.transform)

	case FillPathCommand:
		return p.executeFillPath(c)

	case StrokePathCommand:
		return p.executeStrokePath(c)

	case FillRectCommand:
		return p.executeFillRect(c, degraded)

	case StrokeRectCommand:
		return p.executeStrokeRect(c)

	case DrawImageCommand:
		img, err := p.recording.resolveImage(c.Image)
		if err != nil {
			return err
		}
		p.backend.DrawImage(img, c.SrcRect, c.DstRect, c.Options)

	case DrawTextCommand, StrokeTextCommand:
		return p.executeText(cmd)

	case SetFillStyleCommand, SetStrokeStyleCommand,
		SetLineWidthCommand, SetLineCapCommand, SetLineJoinCommand,
		SetMiterLimitCommand, SetDashCommand, SetFillRuleCommand,
		SetAntiAliasCommand:
		// Style commands are captured in the drawing commands themselves. A
		// backend that declares the style capability may consume them, but
		// playback has nothing to forward.
	}
	return nil
}

// save pushes both the backend state and the playback mirror of the transform.
func (p *player) save() {
	p.transformStack = append(p.transformStack, p.transform)
	p.backend.Save()
}

// restore pops the backend state and the playback mirror together, so a
// recording with an unmatched Restore keeps a sane mirror.
func (p *player) restore() {
	p.backend.Restore()
	if n := len(p.transformStack); n > 0 {
		p.transform = p.transformStack[n-1]
		p.transformStack = p.transformStack[:n-1]
	}
}

// executeSetClip resolves the clip path and installs it in world space.
func (p *player) executeSetClip(c SetClipCommand) error {
	path, err := p.recording.resolvePath(c.Path)
	if err != nil {
		return err
	}
	setPlaybackClip(p.backend, path, c.Rule, p.transform)
	return nil
}

// executeFillPath fills a resolved path with a resolved brush.
func (p *player) executeFillPath(c FillPathCommand) error {
	path, err := p.recording.resolvePath(c.Path)
	if err != nil {
		return err
	}
	brush, err := p.recording.resolveBrush(c.Brush)
	if err != nil {
		return err
	}
	p.backend.FillPath(path, brush, c.Rule)
	return nil
}

// executeStrokePath strokes a resolved path with a resolved brush.
func (p *player) executeStrokePath(c StrokePathCommand) error {
	path, err := p.recording.resolvePath(c.Path)
	if err != nil {
		return err
	}
	brush, err := p.recording.resolveBrush(c.Brush)
	if err != nil {
		return err
	}
	p.backend.StrokePath(path, brush, c.Stroke)
	return nil
}

// executeFillRect fills a rectangle, using the path form when the backend does
// not declare FillRect.
func (p *player) executeFillRect(c FillRectCommand, degraded bool) error {
	brush, err := p.recording.resolveBrush(c.Brush)
	if err != nil {
		return err
	}
	if degraded {
		// Equivalent form: fill the rectangle as a world-space path.
		p.backend.FillPath(worldRectPath(c.Rect), brush, FillRuleNonZero)
		return nil
	}
	p.backend.FillRect(c.Rect, brush)
	return nil
}

// executeStrokeRect strokes a rectangle. StrokeRectCommand stores world-space
// bounds, so the path form (native or degraded) isolates the identity
// transform; backends then do not apply the recording transform twice and
// restore their complete graphics state for later commands.
func (p *player) executeStrokeRect(c StrokeRectCommand) error {
	brush, err := p.recording.resolveBrush(c.Brush)
	if err != nil {
		return err
	}
	p.strokeWorldPath(worldRectPath(c.Rect), brush, c.Stroke)
	return nil
}

// executeText draws filled text, or draws stroked text as filled text because
// the Backend interface has no stroked-text operation. Backends that cannot do
// this exclude CapStrokeText from their declaration.
func (p *player) executeText(cmd Command) error {
	var (
		str    string
		x, y   float64
		face   text.Face
		brushR BrushRef
	)
	switch c := cmd.(type) {
	case DrawTextCommand:
		str, x, y, face, brushR = c.Text, c.X, c.Y, c.Face, c.Brush
	case StrokeTextCommand:
		str, x, y, face, brushR = c.Text, c.X, c.Y, c.Face, c.Brush
	}
	brush, err := p.recording.resolveBrush(brushR)
	if err != nil {
		return err
	}
	p.backend.DrawText(str, x, y, face, brush)
	return nil
}

// strokeWorldPath strokes a world-space path under an isolated identity
// transform, so every backend applies the coordinates and the stroke metrics
// exactly once, then restores its complete graphics state.
func (p *player) strokeWorldPath(path *gg.Path, brush Brush, stroke Stroke) {
	p.backend.Save()
	p.backend.SetTransform(Identity())
	p.backend.StrokePath(path, brush, stroke)
	p.backend.Restore()
}

// worldRectPath builds a world-space axis-aligned rectangle path.
func worldRectPath(rect Rect) *gg.Path {
	return gg.BuildPath().Rect(rect.MinX, rect.MinY, rect.Width(), rect.Height()).Build()
}

// setPlaybackClip applies a world-space clip without disturbing the backend's
// current transform. Save/Restore cannot be used here because Restore would
// also discard the newly installed clip.
func setPlaybackClip(backend Backend, path *gg.Path, rule FillRule, transform Matrix) {
	backend.SetTransform(Identity())
	backend.SetClip(path, rule)
	backend.SetTransform(transform)
}

// scanCommands maps every command to the capability it needs and builds the
// union of required capabilities. Unknown commands are reported with their
// index before any output is produced.
func (r *Recording) scanCommands() (perCommand []Capability, required Capability, err error) {
	perCommand = make([]Capability, len(r.commands))
	for i, cmd := range r.commands {
		capability, ok := commandCapability(cmd)
		if !ok {
			return nil, CapNone, &PlaybackError{
				Index: i,
				Err:   fmt.Errorf("%w: %T", ErrUnknownCommand, cmd),
			}
		}
		perCommand[i] = capability
		required |= capability
	}
	return perCommand, required, nil
}

// buildReport compares the required capabilities against the backend
// declaration. Every missing capability is resolved through the fallback chain
// so the report can state whether [PolicyDegrade] can recover.
func (r *Recording) buildReport(backend Backend, perCommand []Capability, required Capability) (*CapabilityReport, error) {
	set, declared := backendCapabilities(backend)
	report := &CapabilityReport{Declared: declared, Required: required}
	if !declared {
		report.Native = CapAll
		return report, nil
	}
	if err := validateCapabilitySet(set); err != nil {
		return nil, &PlaybackError{Index: -1, Err: err}
	}
	report.Native = set.Native

	indexes := make(map[Capability][]int, len(capabilityNames))
	for i, capability := range perCommand {
		if capability == CapNone || set.Native.Contains(capability) {
			continue
		}
		indexes[capability] = append(indexes[capability], i)
	}

	for _, capability := range orderedCapabilities() {
		commands := indexes[capability]
		if len(commands) == 0 {
			continue
		}
		target, ok, err := resolveDegradation(capability, set.Native, set)
		if err != nil {
			return nil, &PlaybackError{Index: commands[0], Command: r.commands[commands[0]].Type(), HasCommand: true, Err: err}
		}
		issue := CapabilityIssue{
			Capability: capability,
			Commands:   commands,
			Degradable: ok,
		}
		if ok {
			issue.Fallback = target
		}
		report.Missing = append(report.Missing, capability)
		report.Issues = append(report.Issues, issue)
	}
	return report, nil
}

// FallbackFor returns the equivalent capability for a missing capability
// reported by this report, or nil when the command cannot be degraded.
func (r *CapabilityReport) FallbackFor(capability Capability) *Capability {
	if r == nil {
		return nil
	}
	for i := range r.Issues {
		if r.Issues[i].Capability == capability && r.Issues[i].Degradable {
			target := r.Issues[i].Fallback
			return &target
		}
	}
	return nil
}

// checkResources verifies that every resource a command references is present.
func (r *Recording) checkResources(cmd Command) error {
	switch c := cmd.(type) {
	case SetClipCommand:
		_, err := r.resolvePath(c.Path)
		return err
	case FillPathCommand:
		if _, err := r.resolvePath(c.Path); err != nil {
			return err
		}
		_, err := r.resolveBrush(c.Brush)
		return err
	case StrokePathCommand:
		if _, err := r.resolvePath(c.Path); err != nil {
			return err
		}
		_, err := r.resolveBrush(c.Brush)
		return err
	case FillRectCommand:
		_, err := r.resolveBrush(c.Brush)
		return err
	case StrokeRectCommand:
		_, err := r.resolveBrush(c.Brush)
		return err
	case DrawImageCommand:
		_, err := r.resolveImage(c.Image)
		return err
	case DrawTextCommand:
		_, err := r.resolveBrush(c.Brush)
		return err
	case StrokeTextCommand:
		_, err := r.resolveBrush(c.Brush)
		return err
	}
	return nil
}

// resolvePath returns the path for ref or [ErrMissingResource].
func (r *Recording) resolvePath(ref PathRef) (*gg.Path, error) {
	if ref == PathRef(InvalidRef) || r.resources == nil {
		return nil, fmt.Errorf("%w: path %d", ErrMissingResource, ref)
	}
	path := r.resources.GetPath(ref)
	if path == nil {
		return nil, fmt.Errorf("%w: path %d is absent", ErrMissingResource, ref)
	}
	return path, nil
}

// resolveBrush returns the brush for ref or [ErrMissingResource].
func (r *Recording) resolveBrush(ref BrushRef) (Brush, error) {
	if ref == BrushRef(InvalidRef) || r.resources == nil {
		return nil, fmt.Errorf("%w: brush %d", ErrMissingResource, ref)
	}
	brush := r.resources.GetBrush(ref)
	if brush == nil {
		return nil, fmt.Errorf("%w: brush %d is absent", ErrMissingResource, ref)
	}
	return brush, nil
}

// resolveImage returns the image for ref or [ErrMissingResource].
func (r *Recording) resolveImage(ref ImageRef) (image.Image, error) {
	if ref == ImageRef(InvalidRef) || r.resources == nil {
		return nil, fmt.Errorf("%w: image %d", ErrMissingResource, ref)
	}
	img := r.resources.GetImage(ref)
	if img == nil {
		return nil, fmt.Errorf("%w: image %d is absent", ErrMissingResource, ref)
	}
	return img, nil
}

// backendCapabilities returns the capability set the backend declares.
// Backends that do not implement [CapabilityBackend] are treated as supporting
// everything.
func backendCapabilities(backend Backend) (CapabilitySet, bool) {
	if cb, ok := backend.(interface{ BackendCapabilities() CapabilitySet }); ok {
		return cb.BackendCapabilities(), true
	}
	return CapabilitySet{Native: CapAll}, false
}

// checkBackend rejects a nil backend before any method is called.
func checkBackend(backend Backend) error {
	if isNilBackend(backend) {
		return &PlaybackError{Index: -1, Err: ErrNilBackend}
	}
	return nil
}

// isNilBackend reports whether backend is nil, including a typed nil pointer
// stored in the interface.
func isNilBackend(backend Backend) bool {
	if backend == nil {
		return true
	}
	value := reflect.ValueOf(backend)
	switch value.Kind() { //nolint:exhaustive // only nilable kinds matter
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return value.IsNil()
	}
	return false
}

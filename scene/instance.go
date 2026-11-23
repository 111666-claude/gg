package scene

import (
	"errors"
	"math"
)

// MaxInstanceDepth bounds the nesting depth of scene instances during
// construction and playback. Depth 0 is the host scene; an instance directly
// attached to the host is depth 1.
const MaxInstanceDepth = 16

// Sentinel errors for reusable scene instances. Callers can use errors.Is to
// detect each condition explicitly instead of relying on silent clamping.
var (
	// ErrNilInstance is returned when AddInstance receives a nil instance or
	// AddScene receives a nil child scene.
	ErrNilInstance = errors.New("scene: instance is nil")

	// ErrEmptyInstance is returned when an instance references a child scene
	// with no recorded content.
	ErrEmptyInstance = errors.New("scene: instance references an empty scene")

	// ErrNegativeOpacity is returned when an instance opacity is below zero.
	// Opacity above 1.0 is clamped to 1.0; negative values are rejected.
	ErrNegativeOpacity = errors.New("scene: instance opacity must be >= 0")

	// ErrSingularMatrix is returned when an instance transform has a
	// non-invertible 2x2 part (det == 0), which would collapse all content
	// onto a line or point and cannot be rendered.
	ErrSingularMatrix = errors.New("scene: instance transform is singular")

	// ErrInstanceCycle is returned when adding an instance would make the
	// instance graph cyclic (a scene referencing itself directly or
	// transitively).
	ErrInstanceCycle = errors.New("scene: instance cycle detected")

	// ErrInstanceDepthExceeded is returned when the instance nesting depth
	// would exceed MaxInstanceDepth.
	ErrInstanceDepthExceeded = errors.New("scene: instance nesting depth exceeded")

	// ErrInstanceMoved is returned at render/validation time when a
	// referenced child scene was modified or reset after the instance was
	// attached. Referenced sub-scenes are immutable; freeze them with
	// Freeze to enforce this at the API level.
	ErrInstanceMoved = errors.New("scene: referenced sub-scene was modified after attachment")

	// ErrInstanceNotFound is returned at render time when an instance ID in
	// the encoding has no matching entry in the owning scene.
	ErrInstanceNotFound = errors.New("scene: instance id not found in scene")
)

// Instance is a reusable, lightweight reference to an immutable child Scene
// (a "sub-scene"). One sub-scene can be instanced many times — at different
// positions, scales, opacities and blend modes — without copying its command
// streams or image pixels.
//
// Instances are attached to a host Scene with Scene.AddInstance (or created
// via NewInstance + AddInstance). The host encoding records a single
// TagInstance command per placement; the child commands are traversed
// recursively at render time, with the instance transform/opacity/blend/clip
// applied as a compositing group.
//
// Image and font registries of referenced scenes are merged into the host by
// pointer (no pixel data is copied), and child resource indices are remapped
// to host indices during playback.
//
// The child scene must not be modified after the instance is attached.
// Call Freeze on shared sub-scenes to turn accidental mutation into an
// explicit panic; otherwise modification is detected at render time and
// reported as ErrInstanceMoved.
type Instance struct {
	// Child is the referenced immutable sub-scene. Never nil after
	// successful construction.
	Child *Scene

	// Transform maps child-scene coordinates into the host. It is combined
	// with the host's current transform at render time.
	Transform Affine

	// Opacity scales the alpha of the whole instanced group (0..1).
	Opacity float32

	// Blend controls how the instanced group composites onto the host
	// content below it.
	Blend BlendMode

	// Clip optionally restricts the instanced group to a shape expressed in
	// host coordinates (i.e. affected by the host's current transform, not
	// by Transform). Nil means no clip.
	Clip Shape

	// clipBounds is Clip.Bounds() transformed into absolute host space at
	// attachment time. It lets tile renderers reject non-overlapping tiles
	// without rebuilding the clip path.
	clipBounds Rect

	// id is the runtime identifier encoded into TagInstance and resolved
	// against the owning scene. Assigned by AddInstance.
	id uint32

	// childVersion snapshots Child.Version() at attachment time and is
	// rebased when the child legitimately grows nested instances.
	childVersion uint64

	// owner is the scene whose instances slice holds this instance.
	owner *Scene
}

// NewInstance creates an instance of child with identity transform, full
// opacity, normal blending and no clip. The returned instance is not yet
// attached to any scene; pass it to Scene.AddInstance.
//
// The transform is rejected as singular when its 2x2 determinant is zero.
// Negative opacity is rejected; opacity > 1 is clamped to 1.
func NewInstance(child *Scene, transform Affine) (*Instance, error) {
	if child == nil {
		return nil, ErrNilInstance
	}
	if isSingular(transform) {
		return nil, ErrSingularMatrix
	}
	return &Instance{
		Child:     child,
		Transform: transform,
		Opacity:   1,
		Blend:     BlendNormal,
	}, nil
}

// WithOpacity sets the group opacity (clamped to [0,1]) and returns the
// instance for chaining. Negative values are rejected at attachment time;
// this setter clamps them to zero to keep the builder style ergonomic.
func (in *Instance) WithOpacity(opacity float32) *Instance {
	in.Opacity = clampAlpha(opacity)
	return in
}

// WithBlend sets the compositing blend mode and returns the instance.
func (in *Instance) WithBlend(blend BlendMode) *Instance {
	in.Blend = blend
	return in
}

// WithClip sets a host-space clip shape (nil clears it) and returns the
// instance.
func (in *Instance) WithClip(clip Shape) *Instance {
	in.Clip = clip
	return in
}

// WithTransform replaces the instance transform and returns the instance.
// Singular transforms are rejected at attachment time.
func (in *Instance) WithTransform(t Affine) *Instance {
	in.Transform = t
	return in
}

// ID returns the runtime instance identifier assigned by the owning scene.
// Returns 0 before the instance has been attached.
func (in *Instance) ID() uint32 {
	return in.id
}

// ChildVersion returns the child scene version captured at attachment time.
func (in *Instance) ChildVersion() uint64 {
	return in.childVersion
}

// isSingular reports whether the affine's 2x2 part is non-invertible.
// NaN components are treated as singular.
func isSingular(a Affine) bool {
	det := a.A*a.E - a.B*a.D
	return math.IsNaN(float64(det)) || det == 0
}

// EncodeInstance writes a TagInstance command into the encoding.
// Layout: drawData <- instanceID; pathData <- 6 transform floats, opacity,
// blend mode (8 floats total). Instance bounds are tracked in
// instanceBounds only: Encoding.bounds covers non-instance content while
// Scene.bounds owns the full union including instances, which keeps the
// translation-append path from double-counting unshiftable instance bounds.
func (e *Encoding) EncodeInstance(id uint32, transform Affine, opacity float32, blend BlendMode, bounds Rect) {
	e.tags = append(e.tags, TagInstance)
	e.drawData = append(e.drawData, id)
	e.pathData = append(e.pathData,
		transform.A, transform.B, transform.C,
		transform.D, transform.E, transform.F,
		opacity, float32(blend),
	)
	e.instanceBounds = e.instanceBounds.Union(bounds)
}

// AddScene creates an instance of child with the given transform using
// default group state (opacity 1, BlendNormal, no clip) and attaches it to
// s. Equivalent to NewInstance followed by AddInstance.
func (s *Scene) AddScene(child *Scene, transform Affine) (*Instance, error) {
	in, err := NewInstance(child, transform)
	if err != nil {
		return nil, err
	}
	if err := s.AddInstance(in); err != nil {
		return nil, err
	}
	return in, nil
}

// AddInstance attaches a reusable sub-scene instance to this scene.
//
// The instance is validated (non-nil, non-empty child, non-negative opacity,
// non-singular transform), the sub-scene graph is checked for cycles and
// depth, and the child's image/font registries are merged into this scene by
// reference. A TagInstance command is recorded into the current layer so the
// instance participates in normal command ordering.
//
// The child sub-scene (and everything it references) becomes effectively
// immutable: modifying it later fails validation at render time. Call
// child.Freeze() to enforce immutability eagerly.
func (s *Scene) AddInstance(in *Instance) error {
	if in == nil {
		return ErrNilInstance
	}
	if in.Child == nil {
		return ErrNilInstance
	}
	if in.Child.IsEmpty() {
		return ErrEmptyInstance
	}
	if in.Opacity < 0 {
		return ErrNegativeOpacity
	}
	if isSingular(in.Transform) {
		return ErrSingularMatrix
	}

	// Walk the referenced sub-scene graph to reject cycles and excessive
	// depth before recording anything. The host itself seeds the ancestor
	// path so a direct self-reference counts as a cycle. DAGs are allowed:
	// the same sub-scene may be referenced by several hosts, so traversal
	// tracks the current ancestor path rather than a global "seen" set.
	ancestors := map[*Scene]struct{}{s: {}}
	// Host is depth 0; the new child starts at depth 1. A scene previously
	// attached as a child already carries its own attachDepth.
	hostDepth := s.attachDepth
	if err := s.addInstanceAt(in, hostDepth, ancestors); err != nil {
		return err
	}
	return nil
}

// addInstanceAt is the depth-aware AddInstance implementation. hostDepth is
// the depth of s in the attachment chain (top-level hosts pass 0).
func (s *Scene) addInstanceAt(in *Instance, hostDepth int, ancestors map[*Scene]struct{}) error {
	// The new child would sit at hostDepth+1, its descendants add
	// maxInstanceDepth(child) more levels. Checked before any state change
	// so a rejected attachment leaves both scenes untouched.
	if hostDepth+1+maxInstanceDepth(in.Child, map[*Scene]int{}) > MaxInstanceDepth {
		return ErrInstanceDepthExceeded
	}
	if err := s.validateInstanceGraph(in.Child, hostDepth+1, ancestors); err != nil {
		return err
	}

	// Clamp opacity defensively (builder setters already clamp, but an
	// Instance may have been constructed struct-literal style). Done before
	// the depth/cycle gates so a rejected attachment leaves no scene state
	// behind.
	in.Opacity = clampAlpha(in.Opacity)

	// Merge resource registries (pointer-level; no pixel/command copies).
	s.mergeChildResources(in.Child)

	in.childVersion = in.Child.version
	in.owner = s
	in.id = s.nextInstanceID
	s.nextInstanceID++
	// Record the child's attachment depth so a subsequent AddInstance on the
	// child can compute its own chain position without parent pointers
	// (scenes may be shared across a DAG).
	in.Child.attachDepth = hostDepth + 1
	in.Child.referrers = append(in.Child.referrers, in)
	s.instances = append(s.instances, in)
	enc := s.currentEncoding()

	// Optional clip, expressed in host coordinates under the current
	// transform. Mirrors PushLayer's clip encoding.
	if in.Clip != nil {
		path := s.shapeToPath(in.Clip)
		if path != nil && !path.IsEmpty() {
			s.emitTransformIfNeeded(enc, s.currentTransform)
			s.encodeScenePath(enc, path)
			s.pathPool.Put(path)
			enc.EncodeBeginClip()
			in.clipBounds = transformBounds(in.Clip.Bounds(), s.currentTransform)
		}
	}

	enc.EncodeInstance(in.id, in.Transform, in.Opacity, in.Blend, in.bounds())
	enc.RecordCommandBounds(in.bounds())
	s.layerStack.Top().UpdateBounds(in.bounds())
	s.bounds = s.bounds.Union(in.bounds())

	if in.Clip != nil && !in.clipBounds.IsEmpty() {
		enc.EncodeEndClip()
	}

	s.version++

	// Rebase snapshots up the referrer chain: when s itself is a referenced
	// child that just gained an instance, its version bumped and its hosts
	// must treat the structural growth as validated. Plain content edits
	// (Fill/Stroke/…) never rebase and still trip ErrInstanceMoved.
	s.rebaseReferrers()
	return nil
}

// rebaseReferrers refreshes childVersion on every instance pointing at s and
// climbs the referrer graph so a chain built bottom-up converges to
// consistent baselines.
func (s *Scene) rebaseReferrers() {
	visited := make(map[*Scene]struct{})
	var propagate func(sc *Scene)
	propagate = func(sc *Scene) {
		if _, ok := visited[sc]; ok {
			return
		}
		visited[sc] = struct{}{}
		for _, ref := range sc.referrers {
			if ref == nil || ref.Child == nil {
				continue
			}
			ref.childVersion = ref.Child.version
			if ref.owner != nil {
				propagate(ref.owner)
			}
		}
	}
	propagate(s)
}

// validateInstanceGraph recursively walks child's instance graph using an
// ancestor path. A scene already on the path is a back edge (cycle); a scene
// seen in a different branch is a legal DAG reference and is not re-walked.
// depth is the depth the child would occupy (the host is depth 0).
func (s *Scene) validateInstanceGraph(child *Scene, depth int, ancestors map[*Scene]struct{}) error {
	if _, onPath := ancestors[child]; onPath {
		return ErrInstanceCycle
	}
	ancestors[child] = struct{}{}
	defer delete(ancestors, child)
	for _, nested := range child.instances {
		if nested == nil || nested.Child == nil {
			continue
		}
		if depth+1 > MaxInstanceDepth {
			return ErrInstanceDepthExceeded
		}
		if err := s.validateInstanceGraph(nested.Child, depth+1, ancestors); err != nil {
			return err
		}
	}
	return nil
}

// maxInstanceDepth returns the longest instance-chain length starting at
// child (0 when child has no instances). Memoized for DAG sharing.
func maxInstanceDepth(child *Scene, memo map[*Scene]int) int {
	if d, ok := memo[child]; ok {
		return d
	}
	best := 0
	for _, nested := range child.instances {
		if nested == nil || nested.Child == nil {
			continue
		}
		if d := 1 + maxInstanceDepth(nested.Child, memo); d > best {
			best = d
		}
	}
	memo[child] = best
	return best
}

// mergeChildResources merges child's transitive image and font registries
// into this scene, deduping images by pointer and fonts by FontSourceID.
// Only registry slices/maps grow; image pixel buffers and FontSource data
// are shared, never copied.
func (s *Scene) mergeChildResources(child *Scene) {
	for _, img := range child.imageRegistry {
		s.registerImage(img)
	}
	for id, src := range child.fontRegistry {
		if _, ok := s.fontRegistry[id]; !ok {
			s.fontRegistry[id] = src
		}
	}
	for _, nested := range child.instances {
		if nested != nil && nested.Child != nil {
			s.mergeChildResources(nested.Child)
		}
	}
}

// InstanceByID resolves a TagInstance id to its Instance.
func (s *Scene) InstanceByID(id uint32) (*Instance, bool) {
	for _, in := range s.instances {
		if in != nil && in.id == id {
			return in, true
		}
	}
	return nil, false
}

// adoptInstances re-parents other's directly attached instances into s with
// fresh ids and returns the id shift to apply to other's TagInstance
// commands. Transitive child resources are merged as well. The instances
// keep their placement (transform/opacity/blend/clip); their child scenes
// become referenced by s instead of other.
//
// When dx/dy are non-zero (AppendWithTranslation path), the translation is
// composed after each instance transform so the instanced content follows
// the same offset semantics as copied path coordinates.
func (s *Scene) adoptInstances(other *Scene, dx, dy float32) uint32 {
	if len(other.instances) == 0 {
		return 0
	}
	offset := s.nextInstanceID
	for _, in := range other.instances {
		if in == nil || in.Child == nil {
			continue
		}
		s.mergeChildResources(in.Child)
		in.id = s.nextInstanceID
		s.nextInstanceID++
		if dx != 0 || dy != 0 {
			// Translation-only shift of the placement; mirrors how the
			// encoding merge rewrites the C/F components so the registered
			// Instance and its encoded TagInstance stay consistent.
			in.Transform.C += dx
			in.Transform.F += dy
		}
		s.instances = append(s.instances, in)
	}
	return offset
}

// Instances returns the instances directly attached to this scene.
func (s *Scene) Instances() []*Instance {
	return s.instances
}

// bounds returns the host-space bounding box of the instanced content,
// intersected with the host-space clip bounds when a clip is active.
func (in *Instance) bounds() Rect {
	b := transformBounds(in.Child.Bounds(), in.Transform)
	if in.Clip != nil && !in.clipBounds.IsEmpty() {
		b = intersectRect(b, in.clipBounds)
	}
	return b
}

// Bounds returns the host-space bounds of the instance.
func (in *Instance) Bounds() Rect {
	return in.bounds()
}

// intersectRect returns the intersection of two rectangles; an empty rect
// when they do not overlap.
func intersectRect(a, b Rect) Rect {
	r := Rect{
		MinX: max32(a.MinX, b.MinX),
		MinY: max32(a.MinY, b.MinY),
		MaxX: min32(a.MaxX, b.MaxX),
		MaxY: min32(a.MaxY, b.MaxY),
	}
	if r.IsEmpty() {
		return EmptyRect()
	}
	return r
}

// Freeze marks the scene and every transitively referenced sub-scene as
// immutable. Any subsequent mutating call panics with ErrInstanceMoved-style
// message instead of silently invalidating host caches. Reset also refuses
// to run on a frozen scene. Use Unfreeze only when the scene is no longer
// referenced by any host.
func (s *Scene) Freeze() {
	s.frozen = true
	for _, in := range s.instances {
		if in != nil && in.Child != nil {
			in.Child.Freeze()
		}
	}
}

// Unfreeze clears the frozen flag on this scene only.
func (s *Scene) Unfreeze() {
	s.frozen = false
}

// Frozen reports whether the scene is immutable.
func (s *Scene) Frozen() bool {
	return s.frozen
}

// checkMutable panics when a mutating operation is attempted on a frozen
// sub-scene. Called at the entry of every Scene mutator.
func (s *Scene) checkMutable() {
	if s.frozen {
		panic("scene: illegal mutation of frozen sub-scene referenced by an instance")
	}
}

// ValidateInstances walks the full instance graph reachable from this scene
// and returns the first problem found: moved (modified) children, missing
// registrations, cycles, or depth overflow. Renderers call it before
// playback so structural problems surface as explicit errors.
//
// The graph is allowed to be a DAG (one sub-scene referenced multiple
// times); only ancestor back edges are treated as cycles.
func (s *Scene) ValidateInstances() error {
	ancestors := map[*Scene]struct{}{s: {}}
	return s.validateInstancesLocked(0, ancestors)
}

func (s *Scene) validateInstancesLocked(depth int, ancestors map[*Scene]struct{}) error {
	for _, in := range s.instances {
		if in == nil || in.Child == nil {
			return ErrNilInstance
		}
		childDepth := depth + 1
		if childDepth > MaxInstanceDepth {
			return ErrInstanceDepthExceeded
		}
		if in.Opacity < 0 {
			return ErrNegativeOpacity
		}
		if isSingular(in.Transform) {
			return ErrSingularMatrix
		}
		// in.childVersion snapshots in.Child.version at attachment time.
		if in.Child.version != in.childVersion {
			return ErrInstanceMoved
		}
		if _, onPath := ancestors[in.Child]; onPath {
			return ErrInstanceCycle
		}
		ancestors[in.Child] = struct{}{}
		// Recurse into the child scene; its own instances carry their own
		// snapshots, so the comparison above is not re-applied to them.
		if err := in.Child.validateInstancesLocked(childDepth, ancestors); err != nil {
			return err
		}
		delete(ancestors, in.Child)
	}
	return nil
}

// ContentVersion returns a version token covering the scene and every
// transitively reachable sub-scene. It changes whenever this scene or any
// referenced sub-scene is modified, which makes it suitable as a cache-key
// component (a cached rasterization is only valid while the token is
// unchanged). DAG sharing is traversed once per scene via memoization.
func (s *Scene) ContentVersion() uint64 {
	memo := make(map[*Scene]uint64)
	return s.contentVersion(memo)
}

func (s *Scene) contentVersion(memo map[*Scene]uint64) uint64 {
	if v, ok := memo[s]; ok {
		return v
	}
	v := s.version
	for _, in := range s.instances {
		if in == nil || in.Child == nil {
			continue
		}
		// Mix depth and per-instance placement into the token so that
		// moving an instance (which bumps s.version) and editing a child
		// both invalidate. FNV-1a style mixing.
		cv := in.Child.contentVersion(memo)
		v = (v ^ cv) * 1099511628211
		v = (v ^ uint64(in.id)) * 1099511628211
	}
	memo[s] = v
	return v
}

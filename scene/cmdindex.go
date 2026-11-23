package scene

// cmdindex.go — per-tile command filtering for dirty-region rendering.
//
// The CommandIndex maps stable object IDs (the positional commandBounds index,
// see ADR-021) to the tag ranges that realize each draw command in the
// encoding stream. Given a dirty tile, the index answers exactly which
// commands can affect that tile, so RenderDirty replays only those instead of
// the whole stream.
//
// Correctness model:
//
//   - Draw commands (Fill, Stroke, FillRoundRect, Image) carry canvas-space
//     bounds recorded by Scene at encode time (transforms already applied,
//     strokes already expanded by halfWidth). The index widens them by
//     aaFringe to cover anti-aliasing bleed, so a command is skipped on a
//     tile only when its visible output provably cannot touch the tile.
//   - Clips only shrink output, layers are pass-through in the CPU tile
//     renderer, and transforms are baked into the recorded bounds — so
//     filtering by recorded bounds is consistent with clip/layer/transform
//     semantics. Commands inside a filtered layer whose Filter.ExpandBounds
//     grows the output beyond the content bounds MUST have the expanded
//     bounds recorded via RecordCommandBounds; otherwise the index cannot
//     prove safety and the caller must fall back to full replay.
//   - Commands without recorded bounds (text runs, or encodings built
//     through the low-level Encoding API) are treated as unbounded: they
//     replay on every dirty tile. If the number of bounded draw commands
//     does not match len(commandBounds), the index is invalid and the
//     renderer falls back to full replay — it never guesses.
//
// The index is immutable after construction and safe for concurrent use.

// aaFringe is how far (in pixels) a draw command may affect output outside
// its recorded bounds due to anti-aliased coverage and SDF margins.
// renderFillRoundRect uses a 1px AA margin; analytic AA coverage bleeds at
// most 1px. One pixel on every side covers both.
const aaFringe = 1.0

// drawGroup describes one draw command and the tag range that realizes it.
// For path fills/strokes the range covers [BeginPath .. Fill/Stroke]; for
// FillRoundRect/Image/Text it is the single draw tag.
type drawGroup struct {
	// id is the stable object ID: the index into Encoding.commandBounds.
	// Unbounded groups use their draw ordinal and are never skipped.
	id uint64

	// startTag..endTag-1 is the tag range executed for this command.
	startTag int
	endTag   int

	// bounds is the command's canvas-space bounds, widened by aaFringe.
	bounds Rect

	// unbounded marks commands whose effect region is unknown (e.g. text).
	// They are replayed on every dirty tile and never skipped.
	unbounded bool
}

// CommandIndex is a spatial index over an Encoding's draw commands.
// Build it with BuildCommandIndex; query per tile with PlanForTile.
type CommandIndex struct {
	// structural marks tags that must always be replayed: transforms,
	// anti-alias state, clip begin/end (and clip paths), layer push/pop,
	// brush definitions, and unbounded draw commands. Group tags start
	// false and are enabled per tile by PlanForTile.
	structural []bool

	// groups holds every draw command in stream order.
	groups []drawGroup
}

// BuildCommandIndex constructs a CommandIndex for the given encoding.
//
// Returns nil when the index cannot prove filtering is safe:
//   - enc is nil
//   - the number of bounded draw commands (Fill/Stroke/FillRoundRect/Image)
//     differs from len(enc.commandBounds) — e.g. an encoding assembled by
//     hand without RecordCommandBounds, or bounds lost in a merge
//
// A nil index must be treated as "replay everything" (full render).
func BuildCommandIndex(enc *Encoding) *CommandIndex {
	if enc == nil {
		return nil
	}

	tags := enc.tags
	idx := &CommandIndex{
		structural: make([]bool, len(tags)),
	}
	for i := range idx.structural {
		idx.structural[i] = true
	}

	bounded := 0
	pendingPath := -1 // tag index of an unconsumed BeginPath, -1 if none

	markGroup := func(start, end int) {
		for j := start; j < end; j++ {
			// Only path-construction tags and the draw tag itself belong to
			// the group. State tags (Transform/SetAntiAlias/Brush) stay
			// structural even if they appear inside the range.
			if tags[j].IsPathCommand() || j == end-1 {
				idx.structural[j] = false
			}
		}
	}

	for i, t := range tags {
		switch {
		case t == TagBeginPath:
			pendingPath = i

		case t == TagFill || t == TagStroke:
			start := i
			if pendingPath >= 0 {
				start = pendingPath
			}
			markGroup(start, i+1)
			idx.groups = append(idx.groups, drawGroup{
				id:       uint64(bounded), //nolint:gosec // bounded by command count
				startTag: start,
				endTag:   i + 1,
				bounds:   expandRect(encBoundsAt(enc, bounded), aaFringe),
			})
			bounded++
			pendingPath = -1

		case t == TagFillRoundRect || t == TagImage:
			markGroup(i, i+1)
			idx.groups = append(idx.groups, drawGroup{
				id:       uint64(bounded), //nolint:gosec // bounded by command count
				startTag: i,
				endTag:   i + 1,
				bounds:   expandRect(encBoundsAt(enc, bounded), aaFringe),
			})
			bounded++

		case t == TagText:
			// Text bounds are not recorded (glyph extents need font metrics).
			// Keep the tag structural so it replays on every dirty tile.
			idx.groups = append(idx.groups, drawGroup{
				id:        uint64(bounded), //nolint:gosec // bounded by command count
				startTag:  i,
				endTag:    i + 1,
				unbounded: true,
			})

		case t == TagBeginClip:
			// The clip path is structural: it must be rasterized on every
			// tile where any clipped content survives. Conservatively keep
			// it everywhere — clip masks are cheap relative to missed clips.
			pendingPath = -1
		}
	}

	// Safety gate: every bounded draw command must have recorded bounds.
	// A mismatch means the encoding was built without RecordCommandBounds
	// (or bounds were lost in a merge) — filtering cannot be proven safe.
	if bounded != len(enc.commandBounds) {
		return nil
	}

	return idx
}

// encBoundsAt returns commandBounds[i] if present, or an empty rect.
func encBoundsAt(enc *Encoding, i int) Rect {
	if i < len(enc.commandBounds) {
		return enc.commandBounds[i]
	}
	return EmptyRect()
}

// expandRect grows r by d pixels on every side.
func expandRect(r Rect, d float32) Rect {
	if r.IsEmpty() {
		return r
	}
	return Rect{
		MinX: r.MinX - d,
		MinY: r.MinY - d,
		MaxX: r.MaxX + d,
		MaxY: r.MaxY + d,
	}
}

// GroupCount returns the number of draw commands known to the index.
func (idx *CommandIndex) GroupCount() int {
	if idx == nil {
		return 0
	}
	return len(idx.groups)
}

// GroupsIntersecting returns the stable IDs of draw commands whose
// (fringe-expanded) bounds intersect the given canvas-space rect.
// Unbounded commands are always included. This is the query half of the
// index: given a dirty region, these are the only commands that can
// affect it.
func (idx *CommandIndex) GroupsIntersecting(r Rect) []uint64 {
	if idx == nil {
		return nil
	}
	var out []uint64
	for _, g := range idx.groups {
		if g.unbounded || rectIntersects(g.bounds, r) {
			out = append(out, g.id)
		}
	}
	return out
}

// ReplayPlan is a per-tile execution plan derived from a CommandIndex.
// It reports, for each tag in the encoding, whether the tag must be
// executed when rendering that tile.
type ReplayPlan struct {
	exec    []bool
	skipped int
}

// PlanForTile builds the replay plan for one tile. tileBounds is the tile
// rectangle in canvas coordinates. Draw commands whose expanded bounds do
// not intersect the tile are excluded; everything else (state, clips,
// layers, unbounded commands) is always included, preserving stream order.
func (idx *CommandIndex) PlanForTile(tileBounds Rect) *ReplayPlan {
	exec := make([]bool, len(idx.structural))
	copy(exec, idx.structural)

	skipped := 0
	for _, g := range idx.groups {
		if g.unbounded {
			continue // structural already — always replayed
		}
		if rectIntersects(g.bounds, tileBounds) {
			for j := g.startTag; j < g.endTag; j++ {
				exec[j] = true
			}
		} else {
			skipped++
		}
	}
	return &ReplayPlan{exec: exec, skipped: skipped}
}

// Execute reports whether the tag at the given stream position must run.
func (p *ReplayPlan) Execute(tagIdx int) bool {
	if p == nil || tagIdx < 0 || tagIdx >= len(p.exec) {
		return true // no plan or out of range: replay (conservative)
	}
	return p.exec[tagIdx]
}

// Skipped returns the number of draw commands this plan skips on its tile.
func (p *ReplayPlan) Skipped() int {
	if p == nil {
		return 0
	}
	return p.skipped
}

// skipTagData advances the decoder past the current tag's stream data
// without producing any side effects. It must consume exactly the same
// stream entries the execute path would, so subsequent tags decode
// correctly. Structural tags are never skipped by a well-formed plan, but
// every tag is handled defensively.
func skipTagData(dec *Decoder) {
	switch dec.Tag() {
	case TagTransform:
		dec.Transform()
	case TagSetAntiAlias:
		dec.AntiAlias()
	case TagMoveTo:
		dec.MoveTo()
	case TagLineTo:
		dec.LineTo()
	case TagQuadTo:
		dec.QuadTo()
	case TagCubicTo:
		dec.CubicTo()
	case TagFill:
		dec.Fill()
	case TagFillRoundRect:
		dec.FillRoundRect()
	case TagStroke:
		dec.Stroke()
	case TagPushLayer:
		dec.PushLayer()
	case TagBrush:
		dec.Brush()
	case TagImage:
		dec.Image()
	case TagText:
		dec.Text()
	case TagBeginPath, TagEndPath, TagClosePath,
		TagPopLayer, TagBeginClip, TagEndClip:
		// Marker tags carry no stream data.
	}
}

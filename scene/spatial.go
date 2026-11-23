package scene

import (
	"math"
	"sort"
)

// CommandRef identifies one draw command selected by a CommandIndex query.
//
// Index is the position of the draw command in scene recording order
// (the same order used by Encoding.RecordCommandBounds and
// Encoding.CommandBounds). Bounds is the command's recorded bounding box.
//
// Results are always returned sorted by ascending Index, i.e. in the original
// recording order, so replaying them paints in the same order as a full scan.
type CommandRef struct {
	Index  int
	Bounds Rect
}

// commandSpan stores the decoder position and draw state for one draw command.
//
// Replaying a span requires no knowledge of the preceding stream: the transform
// and anti-alias state are materialized here, and Seek positions every decoder
// stream cursor at the first tag of the segment that owns this command.
type commandSpan struct {
	tagStart   int // first tag of this command's segment
	pathStart  int // pathData cursor at tagStart
	drawStart  int // drawData cursor at tagStart
	transStart int // transforms cursor at tagStart
	textStart  int // textData cursor at tagStart

	transform Affine
	antiAlias bool
}

// CommandIndex is an immutable spatial index over the per-command bounds of a
// Scene encoding. It answers "which draw commands can affect this rectangle?"
// without walking the command stream.
//
// Construction requires one recorded bound per draw command (see
// Encoding.RecordCommandBounds). Encodings built directly through
// Encoding.EncodeFill / EncodeStroke / EncodeImage, scenes containing TagText
// (whose bounds are resolved at render time), and encodings with nested clips
// are not indexable; builders report nil so callers fall back to a full scan.
//
// A CommandIndex is safe for concurrent Query calls and is never modified
// after construction. Query only reads the index; it never touches the
// Encoding or a Decoder.
type CommandIndex struct {
	// encoding is the stream the spans point into. Kept so renderers can seek
	// their own decoders without threading the encoding separately.
	encoding *Encoding

	bounds []Rect
	spans  []commandSpan

	// Uniform grid. Entries whose bounds cover too many cells live in
	// overflow and are tested on every query (correct, just not selective).
	cellSize float64
	originX  float64
	originY  float64
	cols     int
	rows     int
	cellStart []int32 // length cols*rows+1, prefix offsets into items
	items     []int32
	overflow  []int32

	// version is the Scene version observed at build time (0 for indexes built
	// directly from an Encoding).
	version uint64
}

// Len returns the number of draw commands known to the index.
func (idx *CommandIndex) Len() int {
	if idx == nil {
		return 0
	}
	return len(idx.bounds)
}

// Version returns the Scene version recorded when the index was built.
func (idx *CommandIndex) Version() uint64 {
	if idx == nil {
		return 0
	}
	return idx.version
}

// Query returns the draw commands whose bounds intersect rect, in original
// recording order.
//
// Intersection is inclusive on both axes: a command whose bounds merely touch
// the query rectangle's edge is returned (this matches the tile renderer's
// conservative rectIntersects test).
//
// A zero-area query (a point or line) returns the commands whose bounds cover
// that point. Empty (inverted) bounds, NaN, and infinite values never match. An
// invalid query rectangle (NaN, infinite, or inverted) yields an empty result.
// None of these cases panic.
//
// The returned slice is newly allocated and owned by the caller.
func (idx *CommandIndex) Query(rect Rect) []CommandRef {
	return idx.QueryAppend(nil, rect)
}

// QueryAppend is Query, appending to dst and returning the extended slice.
// Passing a reusable dst avoids per-tile allocation in the renderer.
func (idx *CommandIndex) QueryAppend(dst []CommandRef, rect Rect) []CommandRef {
	return idx.queryReuse(dst, rect)
}

// queryReuse is the shared implementation of Query and QueryAppend.
func (idx *CommandIndex) queryReuse(dst []CommandRef, rect Rect) []CommandRef {
	if idx == nil || len(idx.bounds) == 0 {
		return dst
	}
	if !validQueryRect(rect) {
		return dst
	}

	// Grid cell range covering the query, clamped to the grid. A query that
	// lies completely outside the grid still needs the overflow entries.
	canReachOverflowOnly := idx.cols == 0 || idx.rows == 0
	var cx0, cx1, cy0, cy1 int
	if !canReachOverflowOnly {
		cx0, cx1, cy0, cy1 = idx.cellRange(rect)
	}

	// Candidate command IDs. Duplicates arise when an entry spans several
	// cells; sorting then deduplicating is cheaper than a per-query bitmap for
	// the small visible sets a single tile produces.
	//
	// An entry whose bounds span a whole grid row/column is stored in every
	// cell along that axis, so a wide query can legitimately collect the same
	// ID many times; the buffer grows to match the grid rather than panicking.
	candidates := make([]int32, 0, 32)
	candidates = append(candidates, idx.overflow...)
	if !canReachOverflowOnly {
		for cy := cy0; cy <= cy1; cy++ {
			row := cy * idx.cols
			for cx := cx0; cx <= cx1; cx++ {
				cell := row + cx
				start := int(idx.cellStart[cell])
				end := int(idx.cellStart[cell+1])
				candidates = append(candidates, idx.items[start:end]...)
			}
		}
	}

	if len(candidates) == 0 {
		return dst
	}

	sort.Slice(candidates, func(i, j int) bool { return candidates[i] < candidates[j] })

	prev := int32(-1)
	for _, id := range candidates {
		if id == prev {
			continue // duplicate from a multi-cell entry
		}
		prev = id
		b := idx.bounds[id]
		if rectsIntersectInclusive(b, rect) {
			dst = append(dst, CommandRef{Index: int(id), Bounds: b})
		}
	}
	return dst
}

// span returns the replay span for command i.
func (idx *CommandIndex) span(i int) commandSpan {
	if idx == nil || i < 0 || i >= len(idx.spans) {
		return commandSpan{}
	}
	return idx.spans[i]
}

// cellRange maps rect onto inclusive grid cell indices, clamped to the grid.
func (idx *CommandIndex) cellRange(rect Rect) (cx0, cx1, cy0, cy1 int) {
	cx0 = idx.clampCell((float64(rect.MinX)-idx.originX)/idx.cellSize, idx.cols)
	cx1 = idx.clampCell((float64(rect.MaxX)-idx.originX)/idx.cellSize, idx.cols)
	cy0 = idx.clampCell((float64(rect.MinY)-idx.originY)/idx.cellSize, idx.rows)
	cy1 = idx.clampCell((float64(rect.MaxY)-idx.originY)/idx.cellSize, idx.rows)
	return cx0, cx1, cy0, cy1
}

func (idx *CommandIndex) clampCell(v float64, limit int) int {
	if math.IsNaN(v) || v <= 0 {
		return 0
	}
	const maxCell = 1 << 30
	if v >= maxCell {
		return limit - 1
	}
	c := int(math.Floor(v))
	if c >= limit {
		c = limit - 1
	}
	return c
}

// BuildCommandIndex constructs a CommandIndex from an encoding's recorded
// per-command bounds.
//
// Returns nil when the encoding cannot be safely indexed:
//   - the number of draw commands does not match the number of recorded bounds
//     (e.g. directly-encoded commands, or TagText whose bounds are dynamic),
//   - the stream contains clip commands (clipping is stateful and cannot be
//     replayed per-command),
//   - an image's current size no longer matches the bounds recorded at draw
//     time (the image was resized after recording),
//   - a bound contains NaN or infinity,
//   - the tag stream contains an unknown tag.
//
// A nil index means "fall back to a full command scan"; it never means "no
// visible commands". Callers must not treat it as an empty index.
func BuildCommandIndex(enc *Encoding) *CommandIndex {
	return buildCommandIndex(enc, nil, 0)
}

// buildCommandIndex is the internal builder. images may be nil, in which case
// image commands are not validated against their recorded bounds. version is
// stamped onto the index for renderer cache diagnostics.
//
//nolint:gocyclo,cyclop,gocognit,funlen // exhaustive tag dispatch is the point of this builder
func buildCommandIndex(enc *Encoding, images []*Image, version uint64) *CommandIndex {
	if enc == nil {
		return nil
	}
	if len(enc.tags) == 0 {
		return &CommandIndex{version: version}
	}

	spans := make([]commandSpan, 0, len(enc.commandBounds))
	bounds := make([]Rect, 0, len(enc.commandBounds))

	dec := NewDecoder(enc)
	curTransform := IdentityAffine()
	curAA := true
	boundIdx := 0
	drawCount := 0

	segTag := 0
	segPath := 0
	segDraw := 0
	segTrans := 0
	segText := 0

	// Geometry bounds of the encoded path in the current segment (local
	// space, before the transform). Unioned with the recorded command bound so
	// a shape whose Bounds() under-reports cannot hide pixels from the index.
	geo := EmptyRect()

	advanceSegment := func() {
		segTag, segPath, segDraw, segTrans, segText = dec.Position(), dec.pathIdx, dec.drawIdx, dec.transIdx, dec.textIdx
		geo = EmptyRect()
	}

	// record appends one draw command using the next recorded command bound.
	// geoLocal is the encoded geometry in local space (EmptyRect if none); it
	// is unioned in so a shape whose Bounds() under-reports cannot hide pixels.
	record := func(geoLocal Rect) bool {
		if boundIdx >= len(enc.commandBounds) {
			return false
		}
		bound := enc.commandBounds[boundIdx]
		geo := geoLocal
		if !geo.IsEmpty() {
			if !curTransform.IsIdentity() {
				geo = transformBounds(geo, curTransform)
			}
			bound = bound.Union(geo)
		}
		spans = append(spans, makeSpan(segTag, segPath, segDraw, segTrans, segText, curTransform, curAA))
		bounds = append(bounds, bound)
		boundIdx++
		drawCount++
		advanceSegment()
		return true
	}

	for dec.Next() {
		switch tag := dec.Tag(); tag {
		case TagSetAntiAlias:
			curAA = dec.AntiAlias()

		case TagTransform:
			curTransform = dec.Transform()

		case TagBeginPath, TagEndPath, TagClosePath, TagPopLayer:
			// Pure markers: no stream data.

		case TagMoveTo:
			x, y := dec.MoveTo()
			geo = geo.UnionPoint(x, y)
		case TagLineTo:
			x, y := dec.LineTo()
			geo = geo.UnionPoint(x, y)
		case TagQuadTo:
			cx, cy, x, y := dec.QuadTo()
			geo = geo.UnionPoint(cx, cy).UnionPoint(x, y)
		case TagCubicTo:
			c1x, c1y, c2x, c2y, x, y := dec.CubicTo()
			geo = geo.UnionPoint(c1x, c1y).UnionPoint(c2x, c2y).UnionPoint(x, y)

		case TagBrush:
			dec.Brush()

		case TagPushLayer:
			// CPU tile rendering treats layers as no-ops; consume the payload so
			// the following tags stay aligned. GPU rendering does not use the
			// index.
			dec.PushLayer()

		case TagBeginClip, TagEndClip:
			// Clip state is not per-command replayable.
			return nil

		case TagFill:
			dec.Fill()
			if !record(geo) {
				return nil
			}

		case TagStroke:
			dec.Stroke()
			if !record(geo) {
				return nil
			}

		case TagFillRoundRect:
			_, _, rect, _, _ := dec.FillRoundRect()
			if !record(rect) {
				return nil
			}

		case TagImage:
			imageIndex, imgTransform := dec.Image()
			if boundIdx >= len(enc.commandBounds) {
				return nil
			}
			recorded := enc.commandBounds[boundIdx]
			if images != nil {
				if int(imageIndex) >= len(images) || images[imageIndex] == nil {
					return nil
				}
				img := images[imageIndex]
				expected := Rect{
					MinX: 0,
					MinY: 0,
					MaxX: float32(img.Width),
					MaxY: float32(img.Height),
				}
				if !imgTransform.IsIdentity() {
					expected = transformBounds(expected, imgTransform)
				}
				if !rectsEqual(expected, recorded) {
					// Image was resized after the scene recorded its bounds.
					return nil
				}
			}
			if !record(EmptyRect()) {
				return nil
			}

		case TagText:
			// Glyph bounds are resolved from fonts at render time; the scene
			// records no command bound for text, so a full scan is required.
			return nil

		default:
			return nil
		}
	}

	// Every draw command must have exactly one recorded bound; text commands
	// (already rejected) and directly-encoded commands break this invariant.
	if drawCount != len(enc.commandBounds) || boundIdx != len(enc.commandBounds) {
		return nil
	}

	for _, b := range bounds {
		if !finiteRect(b) {
			return nil
		}
	}

	idx := &CommandIndex{
		encoding: enc,
		bounds:   bounds,
		spans:    spans,
		version:  version,
	}
	idx.buildGrid()
	return idx
}

func makeSpan(tagStart, pathStart, drawStart, transStart, textStart int, transform Affine, aa bool) commandSpan {
	return commandSpan{
		tagStart: tagStart,
		pathStart: pathStart, drawStart: drawStart,
		transStart: transStart, textStart: textStart,
		transform: transform, antiAlias: aa,
	}
}

const (
	// indexCellSize is the preferred grid cell size in canvas units. It is
	// close to the tile size so a tile query touches few cells.
	indexCellSize = 64.0

	// indexMaxDim caps the grid at indexMaxDim² cells so adversarial bounds
	// cannot blow up memory. Cell size grows when the scene extent is larger.
	indexMaxDim = 512

	// indexMaxCellsPerEntry promotes entries spanning more cells than this to
	// the always-tested overflow list.
	indexMaxCellsPerEntry = 4096
)

// buildGrid lays out the uniform grid. Entries with empty/inverted bounds are
// never returned by Query (they cover no area) but remain in bounds so command
// indices stay aligned.
//
func (idx *CommandIndex) buildGrid() {
	minX, minY, maxX, maxY, ok := idx.gridExtent()
	if !ok {
		return
	}

	idx.cellSize = chooseCellSize(maxX-minX, maxY-minY)
	idx.originX = minX
	idx.originY = minY
	idx.cols = gridDim((maxX - minX) / idx.cellSize)
	idx.rows = gridDim((maxY - minY) / idx.cellSize)

	counts := make([]int32, idx.cols*idx.rows)
	for i, b := range idx.bounds {
		cx0, cy0, cx1, cy1, ok := idx.cellsFor(b)
		if !ok {
			continue
		}
		if cellSpan(cx0, cy0, cx1, cy1) > indexMaxCellsPerEntry {
			idx.overflow = append(idx.overflow, int32(i))
			continue
		}
		idx.forEachCell(cx0, cy0, cx1, cy1, func(cell int) {
			counts[cell]++
		})
	}

	// Prefix-sum the counts into per-cell start offsets.
	idx.cellStart = make([]int32, idx.cols*idx.rows+1)
	var total int32
	for i, c := range counts {
		idx.cellStart[i] = total
		total += c
	}
	idx.cellStart[len(counts)] = total
	idx.items = make([]int32, total)

	// Reuse counts as write cursors, then fill the item array.
	for i := range counts {
		counts[i] = idx.cellStart[i]
	}
	for i, b := range idx.bounds {
		cx0, cy0, cx1, cy1, ok := idx.cellsFor(b)
		if !ok || cellSpan(cx0, cy0, cx1, cy1) > indexMaxCellsPerEntry {
			continue
		}
		idx.forEachCell(cx0, cy0, cx1, cy1, func(cell int) {
			idx.items[counts[cell]] = int32(i)
			counts[cell]++
		})
	}
}

// gridExtent returns the union extent of all area-covering bounds.
func (idx *CommandIndex) gridExtent() (minX, minY, maxX, maxY float64, ok bool) {
	minX, minY = math.Inf(1), math.Inf(1)
	maxX, maxY = math.Inf(-1), math.Inf(-1)
	for _, b := range idx.bounds {
		if !validQueryRect(b) {
			continue // inverted/empty: cannot contribute to the grid
		}
		ok = true
		minX = math.Min(minX, float64(b.MinX))
		minY = math.Min(minY, float64(b.MinY))
		maxX = math.Max(maxX, float64(b.MaxX))
		maxY = math.Max(maxY, float64(b.MaxY))
	}
	return minX, minY, maxX, maxY, ok
}

// chooseCellSize picks the grid cell size, growing it past indexCellSize when
// the scene extent would otherwise exceed the grid dimension cap.
func chooseCellSize(width, height float64) float64 {
	extent := math.Max(width, height)
	if extent/indexMaxDim > indexCellSize {
		return extent / indexMaxDim
	}
	return indexCellSize
}

// gridDim converts a span in cell units to a cell count in [1, indexMaxDim+1].
func gridDim(span float64) int {
	d := int(math.Floor(span)) + 1
	if d < 1 {
		return 1
	}
	if d > indexMaxDim+1 {
		return indexMaxDim + 1
	}
	return d
}

// cellsFor maps a bound onto inclusive grid cell coordinates. It reports false
// for bounds that contribute no area.
func (idx *CommandIndex) cellsFor(b Rect) (cx0, cy0, cx1, cy1 int, ok bool) {
	if !validQueryRect(b) {
		return 0, 0, 0, 0, false
	}
	cx0 = idx.cellOf(float64(b.MinX)-idx.originX, idx.cols)
	cy0 = idx.cellOf(float64(b.MinY)-idx.originY, idx.rows)
	cx1 = idx.cellOf(float64(b.MaxX)-idx.originX, idx.cols)
	cy1 = idx.cellOf(float64(b.MaxY)-idx.originY, idx.rows)
	return cx0, cy0, cx1, cy1, true
}

// cellOf converts a coordinate offset to a clamped cell index. A bound at the
// grid's far edge maps to the last cell so inclusive edge queries see it.
func (idx *CommandIndex) cellOf(offset float64, dim int) int {
	c := int(math.Floor(offset / idx.cellSize))
	if c < 0 {
		return 0
	}
	if c >= dim {
		return dim - 1
	}
	return c
}

func cellSpan(cx0, cy0, cx1, cy1 int) int {
	return (cx1 - cx0 + 1) * (cy1 - cy0 + 1)
}

func (idx *CommandIndex) forEachCell(cx0, cy0, cx1, cy1 int, fn func(cell int)) {
	for cy := cy0; cy <= cy1; cy++ {
		row := cy * idx.cols
		for cx := cx0; cx <= cx1; cx++ {
			fn(row + cx)
		}
	}
}

// finiteRect reports whether all four coordinates are finite.
func finiteRect(r Rect) bool {
	return !math.IsNaN(float64(r.MinX)) && !math.IsInf(float64(r.MinX), 0) &&
		!math.IsNaN(float64(r.MinY)) && !math.IsInf(float64(r.MinY), 0) &&
		!math.IsNaN(float64(r.MaxX)) && !math.IsInf(float64(r.MaxX), 0) &&
		!math.IsNaN(float64(r.MaxY)) && !math.IsInf(float64(r.MaxY), 0)
}

// validQueryRect reports whether r is finite and not inverted. Zero-area
// rectangles are valid (a point or a line query).
func validQueryRect(r Rect) bool {
	return finiteRect(r) && r.MinX <= r.MaxX && r.MinY <= r.MaxY
}

// rectsIntersectInclusive reports whether a and b share any point, counting
// edge and corner contact as intersection. Empty/invalid rectangles never
// intersect anything.
func rectsIntersectInclusive(a, b Rect) bool {
	if !validQueryRect(a) || !validQueryRect(b) {
		return false
	}
	return a.MinX <= b.MaxX && a.MaxX >= b.MinX &&
		a.MinY <= b.MaxY && a.MaxY >= b.MinY
}

func rectsEqual(a, b Rect) bool {
	return a.MinX == b.MinX && a.MinY == b.MinY && a.MaxX == b.MaxX && a.MaxY == b.MaxY
}

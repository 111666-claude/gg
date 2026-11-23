package scene

import (
	"bytes"
	"image"
	"sync"
	"testing"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/internal/parallel"
	"github.com/gogpu/gg/text"
	"golang.org/x/image/font/gofont/goregular"
)

// ---------------------------------------------------------------------------
// Test scene: fixed two-frame animation (requirement 4)
//
// Canvas 256x256 (4x4 tiles of 64px). Six draw commands (stable IDs 0..5):
//
//	0: opaque white background, full canvas
//	1: red rect        (10,10,40,40)   tile (0,0)
//	2: blue circle     (200,30) r=15   tile (3,0)
//	3: green rect      (30,200,50,30)  tile (0,3)
//	4: magenta rect    (56,100,16,16)  straddles tile boundary x=64, row 1
//	5: black circle    moving: frame1 (54,60) -> frame2 (30,60), r=10
//
// The moving circle's frame-1 bounds end exactly at tile boundary x=64, so
// its anti-aliased fringe spills into tile (1,0): the damage pipeline must
// re-render that tile to erase the fringe. Its frame-2 bounds straddle the
// horizontal tile boundary y=64 — a boundary-crossing command.
// ---------------------------------------------------------------------------

const (
	twoFrameW = 256
	twoFrameH = 256
)

var (
	colWhite   = gg.RGBA{R: 1, G: 1, B: 1, A: 1}
	colRed     = gg.RGBA{R: 1, A: 1}
	colBlue    = gg.RGBA{B: 1, A: 1}
	colGreen   = gg.RGBA{G: 1, A: 1}
	colMagenta = gg.RGBA{R: 1, B: 1, A: 1}
	colBlack   = gg.RGBA{A: 1}
)

// buildTwoFrameScene builds the fixed test scene with the moving circle at
// (movX, movY). If movX < 0, the moving circle is omitted (removal test).
func buildTwoFrameScene(movX, movY float32) *Scene {
	s := NewScene()
	s.Fill(FillNonZero, IdentityAffine(), SolidBrush(colWhite), NewRectShape(0, 0, twoFrameW, twoFrameH))
	s.Fill(FillNonZero, IdentityAffine(), SolidBrush(colRed), NewRectShape(10, 10, 40, 40))
	s.Fill(FillNonZero, IdentityAffine(), SolidBrush(colBlue), NewCircleShape(200, 30, 15))
	s.Fill(FillNonZero, IdentityAffine(), SolidBrush(colGreen), NewRectShape(30, 200, 50, 30))
	s.Fill(FillNonZero, IdentityAffine(), SolidBrush(colMagenta), NewRectShape(56, 100, 16, 16))
	if movX >= 0 {
		s.Fill(FillNonZero, IdentityAffine(), SolidBrush(colBlack), NewCircleShape(movX, movY, 10))
	}
	return s
}

// fullRender renders the scene onto a fresh pixmap with the unfiltered
// full-render path — the ground truth for pixel comparisons.
func fullRender(t *testing.T, s *Scene) *gg.Pixmap {
	t.Helper()
	r := NewRenderer(twoFrameW, twoFrameH)
	if r == nil {
		t.Fatal("NewRenderer returned nil")
	}
	defer r.Close()
	target := gg.NewPixmap(twoFrameW, twoFrameH)
	if err := r.Render(target, s); err != nil {
		t.Fatalf("full render: %v", err)
	}
	return target
}

// pixmapDiff compares two pixmaps byte-by-byte, returning the first
// differing pixel location (-1,-1 if identical).
func pixmapDiff(a, b *gg.Pixmap) (x, y int) {
	da, db := a.Data(), b.Data()
	n := min(len(da), len(db))
	w := a.Width()
	for i := 0; i < n; i++ {
		if da[i] != db[i] {
			px := i / 4
			return px % w, px / w
		}
	}
	return -1, -1
}

// requirePixmapsEqual fails the test if the pixmaps differ.
func requirePixmapsEqual(t *testing.T, got, want *gg.Pixmap, what string) {
	t.Helper()
	if x, y := pixmapDiff(got, want); x >= 0 {
		t.Fatalf("%s: pixmaps differ at (%d,%d)", what, x, y)
	}
}

// renderDirtyTilesWithIndex renders the given tiles of the scene with an
// explicit command index and composites them onto target. Test helper used
// for fault injection with corrupted indexes.
func renderDirtyTilesWithIndex(r *Renderer, target *gg.Pixmap, s *Scene, idx *CommandIndex, coords [][2]int) {
	enc := s.Encoding()
	images := s.Images()
	fonts := s.FontRegistry()
	tiles := make([]*parallel.Tile, 0, len(coords))
	for _, c := range coords {
		if tile := r.tileGrid.TileAt(c[0], c[1]); tile != nil {
			tiles = append(tiles, tile)
		}
	}
	for _, tile := range tiles {
		r.renderTile(tile, enc, target, images, fonts, idx)
	}
	r.compositeTilesCopy(tiles, target)
}

// dirtyTilesForDamage mirrors the damage→tiles conversion of RenderWithDamage.
func dirtyTilesForDamage(damage image.Rectangle) [][2]int {
	damage = damage.Inset(int(aaFringe))
	var coords [][2]int
	tx0 := damage.Min.X / parallel.TileWidth
	ty0 := damage.Min.Y / parallel.TileHeight
	tx1 := (damage.Max.X + parallel.TileWidth - 1) / parallel.TileWidth
	ty1 := (damage.Max.Y + parallel.TileHeight - 1) / parallel.TileHeight
	for ty := ty0; ty < ty1; ty++ {
		for tx := tx0; tx < tx1; tx++ {
			coords = append(coords, [2]int{tx, ty})
		}
	}
	return coords
}

// ---------------------------------------------------------------------------
// Requirement 4: two-frame pixel-identical comparison + skip statistics
// ---------------------------------------------------------------------------

func TestRenderDirty_TwoFramePixelIdentical(t *testing.T) {
	r := NewRenderer(twoFrameW, twoFrameH)
	if r == nil {
		t.Fatal("NewRenderer returned nil")
	}
	defer r.Close()

	tracker := NewDamageTracker()
	target := gg.NewPixmap(twoFrameW, twoFrameH)

	// Frame 1: first render (full).
	frame1 := buildTwoFrameScene(54, 60)
	if _, err := r.RenderWithDamage(target, frame1, tracker); err != nil {
		t.Fatalf("frame1 render: %v", err)
	}

	// Frame 2: circle moves (54,60) -> (30,60). Damage render must produce
	// exactly the same pixels as a full render of frame 2.
	frame2 := buildTwoFrameScene(30, 60)
	damage, err := r.RenderWithDamage(target, frame2, tracker)
	if err != nil {
		t.Fatalf("frame2 render: %v", err)
	}
	if damage.Empty() {
		t.Fatal("expected non-empty damage for moved circle")
	}

	ref := fullRender(t, buildTwoFrameScene(30, 60))
	requirePixmapsEqual(t, target, ref, "damage render vs full render")

	// Skip statistics: 6 commands total; the fixed scene yields exactly
	// 15 skipped command executions across the 4 dirty tiles:
	//   tile (0,0): bg+red+moving run, 3 skipped
	//   tile (1,0): bg only, 5 skipped
	//   tile (0,1): bg+magenta+moving run, 3 skipped
	//   tile (1,1): bg+magenta run, 4 skipped
	stats := r.Stats()
	if stats.CommandsTotal != 6 {
		t.Errorf("CommandsTotal = %d, want 6", stats.CommandsTotal)
	}
	if stats.CommandsSkipped != 15 {
		t.Errorf("CommandsSkipped = %d, want 15", stats.CommandsSkipped)
	}
	if stats.TilesRendered != 4 {
		t.Errorf("TilesRendered = %d, want 4 (damage must cover the AA fringe)", stats.TilesRendered)
	}
}

// TestRenderDirty_FaultInjection proves the pixel comparison is sensitive:
// corrupting the index so one boundary-crossing command is dropped from the
// replay MUST produce different pixels. If this test's corrupted render ever
// matches the reference, the pixel-identity tests above are toothless.
func TestRenderDirty_FaultInjection(t *testing.T) {
	r := NewRenderer(twoFrameW, twoFrameH)
	if r == nil {
		t.Fatal("NewRenderer returned nil")
	}
	defer r.Close()

	tracker := NewDamageTracker()
	frame1 := buildTwoFrameScene(54, 60)
	frame2 := buildTwoFrameScene(30, 60)

	// Establish damage and the dirty tile set.
	target := gg.NewPixmap(twoFrameW, twoFrameH)
	if _, err := r.RenderWithDamage(target, frame1, tracker); err != nil {
		t.Fatalf("frame1 render: %v", err)
	}
	damage := tracker.ComputeDamage(frame2.TaggedBounds())
	coords := dirtyTilesForDamage(damage)
	if len(coords) == 0 {
		t.Fatal("expected dirty tiles")
	}

	ref := fullRender(t, buildTwoFrameScene(30, 60))

	// Control: uncorrupted index renders exactly the reference.
	idx := BuildCommandIndex(frame2.Encoding())
	if idx == nil {
		t.Fatal("index should be valid for the fixed scene")
	}
	control := gg.NewPixmap(twoFrameW, twoFrameH)
	copy(control.Data(), fullRender(t, frame1).Data())
	renderDirtyTilesWithIndex(r, control, frame2, idx, coords)
	requirePixmapsEqual(t, control, ref, "control render with valid index")

	// Fault injection: hide the moving circle (group 5) from the index, as
	// if the filter had wrongly excluded a boundary-crossing command.
	corrupt := BuildCommandIndex(frame2.Encoding())
	if len(corrupt.groups) != 6 {
		t.Fatalf("groups = %d, want 6", len(corrupt.groups))
	}
	corrupt.groups[5].bounds = EmptyRect()

	injected := gg.NewPixmap(twoFrameW, twoFrameH)
	copy(injected.Data(), fullRender(t, frame1).Data())
	renderDirtyTilesWithIndex(r, injected, frame2, corrupt, coords)

	if x, y := pixmapDiff(injected, ref); x < 0 {
		t.Fatal("fault injection undetected: dropping the boundary-crossing " +
			"command produced identical pixels — the pixel comparison cannot " +
			"catch a missing command")
	} else {
		t.Logf("fault correctly detected: first diff at (%d,%d)", x, y)
	}
}

// ---------------------------------------------------------------------------
// Requirement 3: defined behaviors
// ---------------------------------------------------------------------------

func TestRenderDirty_EmptyDamage(t *testing.T) {
	r := NewRenderer(twoFrameW, twoFrameH)
	defer r.Close()

	tracker := NewDamageTracker()
	target := gg.NewPixmap(twoFrameW, twoFrameH)

	frame := buildTwoFrameScene(54, 60)
	if _, err := r.RenderWithDamage(target, frame, tracker); err != nil {
		t.Fatalf("frame1: %v", err)
	}
	before := append([]byte(nil), target.Data()...)

	// Identical frame: no damage, no render, target untouched.
	damage, err := r.RenderWithDamage(target, buildTwoFrameScene(54, 60), tracker)
	if err != nil {
		t.Fatalf("frame2: %v", err)
	}
	if !damage.Empty() {
		t.Errorf("identical frames should produce empty damage, got %v", damage)
	}
	if !bytes.Equal(target.Data(), before) {
		t.Error("empty damage must leave the target untouched")
	}
}

func TestRenderDirty_FullScreenDamage(t *testing.T) {
	r := NewRenderer(twoFrameW, twoFrameH)
	defer r.Close()

	tracker := NewDamageTracker()
	target := gg.NewPixmap(twoFrameW, twoFrameH)

	if _, err := r.RenderWithDamage(target, buildTwoFrameScene(54, 60), tracker); err != nil {
		t.Fatalf("frame1: %v", err)
	}

	// Frame 2: every object moved/resized -> damage covers the full canvas.
	frame2 := NewScene()
	s := frame2
	s.Fill(FillNonZero, IdentityAffine(), SolidBrush(colWhite), NewRectShape(1, 1, twoFrameW, twoFrameH))
	s.Fill(FillNonZero, IdentityAffine(), SolidBrush(colRed), NewRectShape(20, 20, 40, 40))
	s.Fill(FillNonZero, IdentityAffine(), SolidBrush(colBlue), NewCircleShape(190, 40, 16))
	s.Fill(FillNonZero, IdentityAffine(), SolidBrush(colGreen), NewRectShape(40, 190, 50, 30))
	s.Fill(FillNonZero, IdentityAffine(), SolidBrush(colMagenta), NewRectShape(60, 110, 16, 16))
	s.Fill(FillNonZero, IdentityAffine(), SolidBrush(colBlack), NewCircleShape(30, 60, 10))

	damage, err := r.RenderWithDamage(target, frame2, tracker)
	if err != nil {
		t.Fatalf("frame2: %v", err)
	}
	if damage.Empty() {
		t.Fatal("all objects changed: expected full damage")
	}

	ref := fullRender(t, frame2)
	requirePixmapsEqual(t, target, ref, "full-screen damage render")
}

func TestRenderDirty_ObjectRemoved(t *testing.T) {
	r := NewRenderer(twoFrameW, twoFrameH)
	defer r.Close()

	tracker := NewDamageTracker()
	target := gg.NewPixmap(twoFrameW, twoFrameH)

	// Frame 1 includes the moving circle; frame 2 drops it (last object, so
	// positional IDs of the remaining objects are unchanged).
	if _, err := r.RenderWithDamage(target, buildTwoFrameScene(54, 60), tracker); err != nil {
		t.Fatalf("frame1: %v", err)
	}
	damage, err := r.RenderWithDamage(target, buildTwoFrameScene(-1, -1), tracker)
	if err != nil {
		t.Fatalf("frame2: %v", err)
	}

	// Damage must cover the removed object's old bounds (incl. AA fringe).
	old := image.Rect(44, 50, 64, 70)
	if !overlaps(damage, old) {
		t.Errorf("damage %v should cover removed object bounds %v", damage, old)
	}

	ref := fullRender(t, buildTwoFrameScene(-1, -1))
	requirePixmapsEqual(t, target, ref, "object removal render")
}

func TestRenderDirty_ClipLayerScaleConsistent(t *testing.T) {
	build := func(movX float32) *Scene {
		s := NewScene()
		s.Fill(FillNonZero, IdentityAffine(), SolidBrush(colWhite), NewRectShape(0, 0, twoFrameW, twoFrameH))

		// Non-identity device scale: bounds must be tracked in device space.
		s.PushTransform(ScaleAffine(1.25, 1.25))
		s.Fill(FillNonZero, IdentityAffine(), SolidBrush(colGreen), NewRectShape(20, 20, 30, 30))
		s.PopTransform()

		// Layer push/pop around static content.
		s.PushLayer(BlendNormal, 1.0, nil)
		s.Fill(FillNonZero, IdentityAffine(), SolidBrush(colBlue), NewCircleShape(200, 200, 20))
		s.PopLayer()

		// Clip region; the moving object lives inside it.
		s.PushClip(NewRectShape(0, 0, 128, 128))
		s.Fill(FillNonZero, IdentityAffine(), SolidBrush(colRed), NewRectShape(8, 8, 20, 20))
		s.Fill(FillNonZero, IdentityAffine(), SolidBrush(colBlack), NewCircleShape(movX, 100, 8))
		s.PopClip()
		return s
	}

	r := NewRenderer(twoFrameW, twoFrameH)
	defer r.Close()
	tracker := NewDamageTracker()
	target := gg.NewPixmap(twoFrameW, twoFrameH)

	if _, err := r.RenderWithDamage(target, build(50), tracker); err != nil {
		t.Fatalf("frame1: %v", err)
	}
	if _, err := r.RenderWithDamage(target, build(80), tracker); err != nil {
		t.Fatalf("frame2: %v", err)
	}

	ref := fullRender(t, build(80))
	requirePixmapsEqual(t, target, ref, "clip/layer/scale damage render")
}

func TestRenderDirty_UnboundedEncodingFallsBack(t *testing.T) {
	// An encoding built through the low-level API without RecordCommandBounds
	// cannot be indexed — the renderer must replay everything (full render).
	enc := NewEncoding()
	p := gg.NewPath()
	p.MoveTo(10, 10)
	p.LineTo(100, 10)
	p.LineTo(100, 100)
	p.Close()
	enc.EncodePath(p)
	enc.EncodeFill(SolidBrush(colRed), FillNonZero)

	s := NewScene()
	s.encoding = enc // in-package: inject the unbounded encoding

	if idx := BuildCommandIndex(s.Encoding()); idx != nil {
		t.Fatal("index must be nil when commandBounds are missing")
	}

	r := NewRenderer(twoFrameW, twoFrameH)
	defer r.Close()
	target := gg.NewPixmap(twoFrameW, twoFrameH)
	if err := r.RenderDirty(target, s, nil); err != nil {
		t.Fatalf("RenderDirty: %v", err)
	}

	ref := fullRender(t, s)
	requirePixmapsEqual(t, target, ref, "fallback full replay")

	if got := r.Stats().CommandsTotal; got != 0 {
		t.Errorf("CommandsTotal = %d, want 0 (no valid index)", got)
	}
}

func TestRenderDirty_TextIsUnbounded(t *testing.T) {
	source, err := text.NewFontSource(goregular.TTF)
	if err != nil {
		t.Fatalf("font: %v", err)
	}
	face := source.Face(20)

	build := func(movX float32) *Scene {
		s := NewScene()
		s.Fill(FillNonZero, IdentityAffine(), SolidBrush(colWhite), NewRectShape(0, 0, twoFrameW, twoFrameH))
		if err := s.DrawText("Hi", face, 20, 40, SolidBrush(colBlack)); err != nil {
			t.Fatalf("DrawText: %v", err)
		}
		s.Fill(FillNonZero, IdentityAffine(), SolidBrush(colRed), NewCircleShape(movX, 100, 10))
		return s
	}

	// Text has no recorded bounds: the index stays valid (bounded commands
	// still filter) but the text group is unbounded and replays everywhere.
	idx := BuildCommandIndex(build(50).Encoding())
	if idx == nil {
		t.Fatal("index should be valid; text groups are unbounded, not fatal")
	}
	ids := idx.GroupsIntersecting(Rect{MinX: 200, MinY: 200, MaxX: 264, MaxY: 264})
	foundText := false
	for _, g := range idx.groups {
		if g.unbounded {
			foundText = true
		}
	}
	if !foundText {
		t.Error("expected an unbounded group for the text command")
	}
	if len(ids) == 0 {
		t.Error("unbounded text must intersect every tile query")
	}

	r := NewRenderer(twoFrameW, twoFrameH)
	defer r.Close()
	tracker := NewDamageTracker()
	target := gg.NewPixmap(twoFrameW, twoFrameH)

	if _, err := r.RenderWithDamage(target, build(50), tracker); err != nil {
		t.Fatalf("frame1: %v", err)
	}
	if _, err := r.RenderWithDamage(target, build(90), tracker); err != nil {
		t.Fatalf("frame2: %v", err)
	}

	ref := fullRender(t, build(90))
	requirePixmapsEqual(t, target, ref, "text scene damage render")
}

func TestDamageTracker_DuplicateIDs(t *testing.T) {
	dt := NewDamageTracker()
	// Duplicate IDs in one frame: defined behavior is "last wins" for the
	// stored bounds; earlier occurrences still diff conservatively.
	dt.ComputeDamage([]TaggedBounds{
		{ID: 1, Rect: rect(0, 0, 10, 10)},
		{ID: 1, Rect: rect(100, 100, 10, 10)},
	})

	// Next frame with a single entry matching the LAST occurrence: no damage.
	damage := dt.ComputeDamage([]TaggedBounds{
		{ID: 1, Rect: rect(100, 100, 10, 10)},
	})
	if !damage.Empty() {
		t.Errorf("last-wins semantics: expected empty damage, got %v", damage)
	}

	// A frame matching only the FIRST occurrence must damage (it differs
	// from the stored last occurrence).
	damage = dt.ComputeDamage([]TaggedBounds{
		{ID: 1, Rect: rect(0, 0, 10, 10)},
	})
	if damage.Empty() {
		t.Error("expected damage when bounds differ from stored last occurrence")
	}
}

func TestRenderer_ConcurrentRenderDirty(t *testing.T) {
	r := NewRenderer(twoFrameW, twoFrameH)
	defer r.Close()

	ref := fullRender(t, buildTwoFrameScene(30, 60))

	const goroutines = 8
	var wg sync.WaitGroup
	errs := make(chan string, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			scene := buildTwoFrameScene(30, 60)
			target := gg.NewPixmap(twoFrameW, twoFrameH)
			dirty := parallel.NewDirtyRegion(4, 4)
			dirty.MarkAll()
			if err := r.RenderDirty(target, scene, dirty); err != nil {
				errs <- err.Error()
				return
			}
			if x, _ := pixmapDiff(target, ref); x >= 0 {
				errs <- "concurrent render produced wrong pixels"
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

func TestRenderer_VersionRollback(t *testing.T) {
	r := NewRenderer(twoFrameW, twoFrameH)
	defer r.Close()

	// Render a high-version scene.
	sceneA := buildTwoFrameScene(54, 60) // 6 fills -> version >= 6
	target := gg.NewPixmap(twoFrameW, twoFrameH)
	if err := r.RenderDirty(target, sceneA, nil); err != nil {
		t.Fatalf("render A: %v", err)
	}
	versionA := sceneA.Version()
	idxA := r.commandIndexFor(sceneA.Encoding(), versionA)

	// A different scene with a LOWER version must not reuse the cached index.
	sceneB := NewScene()
	sceneB.Fill(FillNonZero, IdentityAffine(), SolidBrush(colWhite), NewRectShape(0, 0, twoFrameW, twoFrameH))
	sceneB.Fill(FillNonZero, IdentityAffine(), SolidBrush(colRed), NewRectShape(100, 100, 30, 30))
	if sceneB.Version() >= versionA {
		t.Fatalf("test setup: sceneB version %d should be < sceneA version %d", sceneB.Version(), versionA)
	}
	idxB := r.commandIndexFor(sceneB.Encoding(), sceneB.Version())
	if idxB == idxA {
		t.Error("version rollback must rebuild the index, not reuse the cached one")
	}

	// Same encoding, rolled-back version: also rebuilds.
	idxC := r.commandIndexFor(sceneA.Encoding(), versionA)
	idxD := r.commandIndexFor(sceneA.Encoding(), versionA-1)
	if idxC == idxD {
		t.Error("same encoding with rolled-back version must rebuild the index")
	}
	idxE := r.commandIndexFor(sceneA.Encoding(), versionA-1)
	if idxD != idxE {
		t.Error("same encoding and version must reuse the cached index")
	}

	// And the rolled-back scene still renders correctly through dirty path.
	targetB := gg.NewPixmap(twoFrameW, twoFrameH)
	r.MarkAllDirty()
	if err := r.RenderDirty(targetB, sceneB, nil); err != nil {
		t.Fatalf("render B: %v", err)
	}
	ref := fullRender(t, sceneB)
	requirePixmapsEqual(t, targetB, ref, "rolled-back scene render")
}

// ---------------------------------------------------------------------------
// Requirement 1-2: index structure and encoding merge
// ---------------------------------------------------------------------------

func TestBuildCommandIndex_GroupsAndBounds(t *testing.T) {
	s := buildTwoFrameScene(30, 60)
	idx := BuildCommandIndex(s.Encoding())
	if idx == nil {
		t.Fatal("index should be valid")
	}
	if idx.GroupCount() != 6 {
		t.Fatalf("GroupCount = %d, want 6", idx.GroupCount())
	}

	// Stable IDs are positional and match commandBounds order.
	for i, g := range idx.groups {
		if g.id != uint64(i) {
			t.Errorf("group %d has id %d", i, g.id)
		}
		if g.unbounded {
			t.Errorf("group %d should be bounded", i)
		}
	}

	// The moving circle (id 5) at (30,60) r=10 with AA fringe:
	g := idx.groups[5]
	want := Rect{MinX: 19, MinY: 49, MaxX: 41, MaxY: 71}
	if g.bounds != want {
		t.Errorf("moving circle bounds = %+v, want %+v (bounds + 1px fringe)", g.bounds, want)
	}

	// Query: a tile far from everything still hits the full-canvas background.
	far := Rect{MinX: 192, MinY: 192, MaxX: 256, MaxY: 256}
	ids := idx.GroupsIntersecting(far)
	if len(ids) != 1 || ids[0] != 0 {
		t.Errorf("GroupsIntersecting(far tile) = %v, want [0] (background only)", ids)
	}

	// Query: the tile containing the moving circle.
	here := Rect{MinX: 0, MinY: 0, MaxX: 64, MaxY: 64}
	ids = idx.GroupsIntersecting(here)
	wantIDs := map[uint64]bool{0: true, 1: true, 5: true}
	if len(ids) != len(wantIDs) {
		t.Fatalf("GroupsIntersecting(tile 0,0) = %v, want 3 entries", ids)
	}
	for _, id := range ids {
		if !wantIDs[id] {
			t.Errorf("unexpected id %d in tile query", id)
		}
	}
}

func TestCommandIndex_PlanForTile(t *testing.T) {
	s := buildTwoFrameScene(30, 60)
	enc := s.Encoding()
	idx := BuildCommandIndex(enc)

	// Tile (3,0): only the background and the blue circle intersect.
	plan := idx.PlanForTile(Rect{MinX: 192, MinY: 0, MaxX: 256, MaxY: 64})
	if plan.Skipped() != 4 {
		t.Errorf("skipped = %d, want 4", plan.Skipped())
	}

	// Every structural tag must be executable in every plan.
	for i, tag := range enc.Tags() {
		if !tag.IsDrawCommand() && !tag.IsPathCommand() && !plan.Execute(i) {
			t.Errorf("structural tag %d (%s) must always execute", i, tag)
		}
	}

	// A plan must preserve stream order: the set of executed tags is a
	// subsequence filter, indices are monotonically increasing by construction.
	// Verify the draw tag of the blue circle (group 2) is executable while
	// the green rect (group 3) is not.
	blue := idx.groups[2]
	green := idx.groups[3]
	if !plan.Execute(blue.endTag - 1) {
		t.Error("blue circle should execute on tile (3,0)")
	}
	if plan.Execute(green.endTag - 1) {
		t.Error("green rect should be skipped on tile (3,0)")
	}
}

func TestBuildCommandIndex_CountMismatch(t *testing.T) {
	enc := NewEncoding()
	p := gg.NewPath()
	p.MoveTo(0, 0)
	p.LineTo(10, 10)
	p.Close()
	enc.EncodePath(p)
	enc.EncodeFill(SolidBrush(colRed), FillNonZero)
	// No RecordCommandBounds: 1 bounded draw, 0 bounds -> unsafe.
	if idx := BuildCommandIndex(enc); idx != nil {
		t.Error("index must be nil when draw count != commandBounds count")
	}
}

func TestEncoding_MergePreservesCommandBounds(t *testing.T) {
	// Layered scene: commandBounds must survive flattening (Append).
	s := NewScene()
	s.Fill(FillNonZero, IdentityAffine(), SolidBrush(colRed), NewRectShape(0, 0, 10, 10))
	s.PushLayer(BlendNormal, 1.0, nil)
	s.Fill(FillNonZero, IdentityAffine(), SolidBrush(colBlue), NewCircleShape(50, 50, 10))
	s.PopLayer()
	s.Fill(FillNonZero, IdentityAffine(), SolidBrush(colGreen), NewRectShape(90, 90, 10, 10))

	enc := s.Encoding()
	if len(enc.commandBounds) != 3 {
		t.Fatalf("commandBounds = %d, want 3 after layer flatten", len(enc.commandBounds))
	}
	if idx := BuildCommandIndex(enc); idx == nil {
		t.Fatal("index must be valid for a layered scene")
	}

	// Clone preserves bounds.
	clone := enc.Clone()
	if len(clone.commandBounds) != len(enc.commandBounds) {
		t.Errorf("Clone lost commandBounds: %d -> %d", len(enc.commandBounds), len(clone.commandBounds))
	}

	// AppendWithTranslation offsets bounds.
	dst := NewScene()
	dst.AppendWithTranslation(s, 100, 50)
	denc := dst.Encoding()
	if len(denc.commandBounds) != 3 {
		t.Fatalf("AppendWithTranslation commandBounds = %d, want 3", len(denc.commandBounds))
	}
	// Original first bounds (0,0,10,10) -> (100,50,110,60).
	b := denc.commandBounds[0]
	if b.MinX != 100 || b.MinY != 50 || b.MaxX != 110 || b.MaxY != 60 {
		t.Errorf("translated bounds = %+v, want (100,50,110,60)", b)
	}
}

// TestRenderDirty_LayeredScenePixelIdentical verifies end-to-end that a
// scene using explicit layers renders identically through damage rendering.
func TestRenderDirty_LayeredScenePixelIdentical(t *testing.T) {
	build := func(movX float32) *Scene {
		s := NewScene()
		s.Fill(FillNonZero, IdentityAffine(), SolidBrush(colWhite), NewRectShape(0, 0, twoFrameW, twoFrameH))
		s.PushLayer(BlendNormal, 1.0, nil)
		s.Fill(FillNonZero, IdentityAffine(), SolidBrush(colBlue), NewCircleShape(200, 30, 15))
		s.Fill(FillNonZero, IdentityAffine(), SolidBrush(colBlack), NewCircleShape(movX, 60, 10))
		s.PopLayer()
		s.Fill(FillNonZero, IdentityAffine(), SolidBrush(colRed), NewRectShape(10, 150, 40, 40))
		return s
	}

	r := NewRenderer(twoFrameW, twoFrameH)
	defer r.Close()
	tracker := NewDamageTracker()
	target := gg.NewPixmap(twoFrameW, twoFrameH)

	if _, err := r.RenderWithDamage(target, build(50), tracker); err != nil {
		t.Fatalf("frame1: %v", err)
	}
	damage, err := r.RenderWithDamage(target, build(90), tracker)
	if err != nil {
		t.Fatalf("frame2: %v", err)
	}
	if damage.Empty() {
		t.Fatal("moved object inside layer should produce damage")
	}

	ref := fullRender(t, build(90))
	requirePixmapsEqual(t, target, ref, "layered scene damage render")
}

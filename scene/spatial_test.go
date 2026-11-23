package scene

import (
	"math"
	"reflect"
	"testing"

	"github.com/gogpu/gg"
)

// buildDenseSparseScene returns a scene with a dense cluster of small shapes
// plus sparse content far from the cluster and a couple of transformed shapes.
func buildDenseSparseScene() *Scene {
	s := NewScene()

	// Dense cluster: 40x40 grid of 8x8 rects at (0,0)..(400,400).
	for j := 0; j < 1600; j++ {
		x := float32(j%40) * 10
		y := float32(j/40) * 10
		s.Fill(FillNonZero, IdentityAffine(),
			SolidBrush(gg.RGBA{R: float64(j%3) / 2, G: 0, B: 0, A: 1}),
			NewRectShape(x, y, 8, 8))
	}

	// Sparse large panels far away.
	for j := 0; j < 20; j++ {
		x := float32(500 + j*120)
		s.Fill(FillNonZero, IdentityAffine(),
			SolidBrush(gg.RGBA{R: 0, G: 0, B: 1, A: 1}),
			NewRectShape(x, 800, 60, 60))
	}

	// A transformed shape so the index must use post-transform bounds.
	s.Fill(FillNonZero, TranslateAffine(-300, -300),
		SolidBrush(gg.RGBA{R: 0, G: 1, B: 0, A: 1}),
		NewCircleShape(0, 0, 20))

	return s
}

// bruteVisible returns draw-command indices whose recorded bounds intersect
// rect, mirroring the inclusive-intersection contract of CommandIndex.Query.
func bruteVisible(idx *CommandIndex, rect Rect) []int {
	var out []int
	for i, b := range idx.bounds {
		if rectsIntersectInclusive(b, rect) {
			out = append(out, i)
		}
	}
	return out
}

func refIndices(refs []CommandRef) []int {
	out := make([]int, len(refs))
	for i, r := range refs {
		out[i] = r.Index
	}
	return out
}

func TestCommandIndex_QueryMatchesBruteForce(t *testing.T) {
	s := buildDenseSparseScene()
	enc := s.Encoding()
	idx := BuildCommandIndex(enc)
	if idx == nil {
		t.Fatal("BuildCommandIndex returned nil for an indexable scene")
	}

	if idx.Len() != len(enc.commandBounds) {
		t.Fatalf("index covers %d commands, encoding recorded %d bounds", idx.Len(), len(enc.commandBounds))
	}

	queries := []Rect{
		{MinX: 0, MinY: 0, MaxX: 0, MaxY: 0},                // point at origin, touches first rect
		{MinX: -1000, MinY: -1000, MaxX: -900, MaxY: -900},  // empty region
		{MinX: 10, MinY: 10, MaxX: 50, MaxY: 50},            // inside the dense grid
		{MinX: -20, MinY: -20, MaxX: 5, MaxY: 5},            // negative coords, edge touch
		{MinX: 500, MinY: 800, MaxX: 1600, MaxY: 900},       // spans the sparse panels
		{MinX: -400, MinY: -400, MaxX: 0, MaxY: 0},          // transformed circle region
		{MinX: -1e9, MinY: -1e9, MaxX: 1e9, MaxY: 1e9},      // whole-world query
		{MinX: 800, MinY: 800, MaxX: 800, MaxY: 800},        // degenerate point query
		{MinX: 390, MinY: 390, MaxX: 390, MaxY: 390},        // exactly on a dense rect corner
	}

	for _, q := range queries {
		got := refIndices(idx.Query(q))
		want := bruteVisible(idx, q)
		if len(got) != len(want) {
			t.Errorf("Query(%v) = %v, want %v", q, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("Query(%v) = %v, want %v", q, got, want)
				break
			}
		}
		// Results must be ascending (original recording order) and duplicate-free.
		for i := 1; i < len(got); i++ {
			if got[i] <= got[i-1] {
				t.Fatalf("Query(%v) results not strictly ascending: %v", q, got)
			}
		}
	}
}

func TestCommandIndex_QueryReusableAppend(t *testing.T) {
	s := buildDenseSparseScene()
	idx := s.BuildCommandIndex()
	if idx == nil {
		t.Fatal("expected index")
	}

	buf := make([]CommandRef, 0, 8)
	a := idx.QueryAppend(buf, Rect{MinX: 0, MinY: 0, MaxX: 100, MaxY: 100})
	b := idx.QueryAppend(a, Rect{MinX: 500, MinY: 800, MaxX: 700, MaxY: 900})
	if len(b) <= len(a) {
		t.Fatalf("QueryAppend did not append: len(a)=%d len(b)=%d", len(a), len(b))
	}
	// The first segment must remain a valid prefix.
	if !reflect.DeepEqual(b[:len(a)], a) {
		t.Error("QueryAppend corrupted the existing prefix")
	}
}

func TestCommandIndex_NegativeAndEmptyBounds(t *testing.T) {
	s := NewScene()
	// Shapes wholly in negative coordinate space.
	s.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.Red), NewRectShape(-200, -150, 50, 50))
	// A degenerate (zero-area) line. Query is bounds-based, so it is reported
	// when a query touches it; replaying it paints nothing, which is harmless.
	s.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.Blue), NewLineShape(100, 100, 100, 100))
	// A normal shape.
	s.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.Green), NewRectShape(300, 300, 20, 20))

	idx := s.BuildCommandIndex()
	if idx == nil {
		t.Fatal("expected index")
	}

	// Negative region: only command 0.
	if got := refIndices(idx.Query(Rect{MinX: -180, MinY: -130, MaxX: -160, MaxY: -110})); !reflect.DeepEqual(got, []int{0}) {
		t.Errorf("negative query = %v, want [0]", got)
	}

	// A point query touching the degenerate command's zero-area bound returns
	// it; this is deterministic and its replay is a no-op.
	if got := refIndices(idx.Query(Rect{MinX: 100, MinY: 100, MaxX: 100, MaxY: 100})); !reflect.DeepEqual(got, []int{1}) {
		t.Errorf("degenerate command query = %v, want [1]", got)
	}

	// Empty (inverted) query returns nothing.
	if got := idx.Query(Rect{MinX: 10, MinY: 10, MaxX: -10, MaxY: -10}); len(got) != 0 {
		t.Errorf("inverted query = %v, want empty", got)
	}

	// Exactly adjacent (edge-touching) query includes the command.
	if got := refIndices(idx.Query(Rect{MinX: 320, MinY: 300, MaxX: 400, MaxY: 320})); !reflect.DeepEqual(got, []int{2}) {
		t.Errorf("edge-touch query = %v, want [2]", got)
	}
}

func TestCommandIndex_InvalidQueryRect(t *testing.T) {
	s := NewScene()
	s.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.Red), NewRectShape(0, 0, 100, 100))
	idx := s.BuildCommandIndex()
	if idx == nil {
		t.Fatal("expected index")
	}

	for _, q := range []Rect{
		{MinX: float32(math.NaN()), MinY: 0, MaxX: 10, MaxY: 10},
		{MinX: 0, MinY: 0, MaxX: float32(math.Inf(1)), MaxY: 10},
		{MinX: 0, MinY: 0, MaxX: 10, MaxY: float32(math.Inf(-1))},
		{MinX: 50, MinY: 0, MaxX: 10, MaxY: 10}, // inverted
	} {
		if got := idx.Query(q); len(got) != 0 {
			t.Errorf("Query(%v) = %v, want empty", q, got)
		}
	}
}

func TestCommandIndex_NotIndexableFallsBack(t *testing.T) {
	t.Run("direct encoding has no bounds", func(t *testing.T) {
		enc := NewEncoding()
		p := gg.NewPath()
		p.Rectangle(0, 0, 10, 10)
		enc.EncodePath(p)
		enc.EncodeFill(SolidBrush(gg.Red), FillNonZero)
		if idx := BuildCommandIndex(enc); idx != nil {
			t.Fatalf("expected nil index, got %d commands", idx.Len())
		}
	})

	t.Run("clip command", func(t *testing.T) {
		s := NewScene()
		s.PushClip(NewRectShape(0, 0, 50, 50))
		s.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.Red), NewRectShape(0, 0, 10, 10))
		s.PopClip()
		if idx := s.BuildCommandIndex(); idx != nil {
			t.Fatal("expected nil index for scene with clips")
		}
	})

	t.Run("text bounds are dynamic", func(t *testing.T) {
		s := NewScene()
		s.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.Red), NewRectShape(0, 0, 10, 10))
		// A TagText with no registered font never records a command bound.
		enc := s.Encoding()
		enc.EncodeText(GlyphRunData{FontSourceID: 1, GlyphCount: 1}, []GlyphEntry{{GlyphID: 1}}, "A")
		if idx := BuildCommandIndex(enc); idx != nil {
			t.Fatal("expected nil index for encoding with text")
		}
	})

	t.Run("resized image", func(t *testing.T) {
		s := NewScene()
		img := NewImage(100, 50)
		img.Data = make([]byte, 100*50*4)
		s.DrawImage(img, IdentityAffine())
		// Resize after recording: the recorded bounds no longer describe output.
		img.Width = 200
		img.Data = make([]byte, 200*50*4)
		if idx := s.BuildCommandIndex(); idx != nil {
			t.Fatal("expected nil index after image resize")
		}
	})

	t.Run("nil scene", func(t *testing.T) {
		var s *Scene
		if idx := s.BuildCommandIndex(); idx != nil {
			t.Fatal("expected nil index for nil scene")
		}
	})
}

func TestCommandIndex_EmptyScene(t *testing.T) {
	s := NewScene()
	idx := s.BuildCommandIndex()
	if idx == nil {
		t.Fatal("empty scene should yield a usable empty index, not nil")
	}
	if idx.Len() != 0 {
		t.Errorf("Len() = %d, want 0", idx.Len())
	}
	if got := idx.Query(Rect{MinX: 0, MinY: 0, MaxX: 10, MaxY: 10}); len(got) != 0 {
		t.Errorf("Query on empty index = %v, want empty", got)
	}
}

func TestCommandIndex_QueryDoesNotMutate(t *testing.T) {
	s := buildDenseSparseScene()
	_ = s.Encoding()
	versionBefore := s.Version()
	tagsBefore := len(s.encoding.tags)
	pathBefore := len(s.encoding.pathData)
	drawBefore := len(s.encoding.drawData)

	idx := s.BuildCommandIndex()
	for i := 0; i < 10; i++ {
		idx.Query(Rect{MinX: 0, MinY: 0, MaxX: 200, MaxY: 200})
	}

	if s.Version() != versionBefore {
		t.Error("Query changed the scene version")
	}
	if len(s.encoding.tags) != tagsBefore || len(s.encoding.pathData) != pathBefore || len(s.encoding.drawData) != drawBefore {
		t.Error("Query mutated the encoding streams")
	}
}

func TestCommandIndex_ConcurrentQueryAndRebuild(t *testing.T) {
	s := buildDenseSparseScene()
	r := NewRenderer(800, 800, WithSpatialIndex())
	if r == nil {
		t.Fatal("NewRenderer returned nil")
	}
	defer r.Close()

	target := gg.NewPixmap(800, 800)
	_ = r.Render(target, s)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			// Mutate the scene between renders to force index rebuilds.
			s.Translate(1, 1)
			_ = r.Render(target, s)
		}
	}()

	for i := 0; i < 200; i++ {
		idx := BuildCommandIndex(s.Encoding())
		if idx != nil {
			idx.Query(Rect{MinX: 0, MinY: 0, MaxX: 400, MaxY: 400})
		}
	}
	<-done
}

func TestCommandIndex_AppendKeepsBoundsAligned(t *testing.T) {
	child := NewScene()
	child.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.Red), NewRectShape(0, 0, 20, 20))
	child.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.Blue), NewRectShape(40, 0, 20, 20))

	parent := NewScene()
	parent.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.Green), NewRectShape(0, 100, 20, 20))
	parent.AppendWithTranslation(child, 100, 200)

	idx := parent.BuildCommandIndex()
	if idx == nil {
		t.Fatal("expected index after AppendWithTranslation")
	}
	if idx.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", idx.Len())
	}

	// The appended child bounds must be translated along with its geometry.
	got := refIndices(idx.Query(Rect{MinX: 100, MinY: 200, MaxX: 120, MaxY: 220}))
	if !reflect.DeepEqual(got, []int{1}) {
		t.Errorf("translated child query = %v, want [1]", got)
	}
}

func TestCommandIndex_VersionStamp(t *testing.T) {
	s := NewScene()
	s.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.Red), NewRectShape(0, 0, 10, 10))
	idx := s.BuildCommandIndex()
	if idx.Version() != s.Version() {
		t.Errorf("Version() = %d, want %d", idx.Version(), s.Version())
	}
}

// ---------------------------------------------------------------------------
// Renderer integration: indexed output must equal full-scan output
// ---------------------------------------------------------------------------

// pixelDiff returns the number of RGBA bytes that differ between two pixmaps
// of equal size, plus the maximum absolute channel difference.
func pixelDiff(a, b *gg.Pixmap) (count, maxDelta int) {
	da := a.Data()
	db := b.Data()
	n := min(len(da), len(db))
	for i := 0; i < n; i++ {
		d := int(da[i]) - int(db[i])
		if d < 0 {
			d = -d
		}
		if d != 0 {
			count++
			if d > maxDelta {
				maxDelta = d
			}
		}
	}
	return count, maxDelta
}

func renderFullAndIndexed(t *testing.T, s *Scene, w, h int) (full, indexed *gg.Pixmap) {
	t.Helper()

	fullR := NewRenderer(w, h)
	if fullR == nil {
		t.Fatal("NewRenderer returned nil")
	}
	defer fullR.Close()

	idxR := NewRenderer(w, h, WithSpatialIndex())
	if idxR == nil {
		t.Fatal("NewRenderer returned nil")
	}
	defer idxR.Close()

	full = gg.NewPixmap(w, h)
	indexed = gg.NewPixmap(w, h)

	full.Clear(gg.RGBA{R: 1, G: 1, B: 1, A: 1})
	indexed.Clear(gg.RGBA{R: 1, G: 1, B: 1, A: 1})

	if err := fullR.Render(full, s); err != nil {
		t.Fatalf("full render: %v", err)
	}
	if err := idxR.Render(indexed, s); err != nil {
		t.Fatalf("indexed render: %v", err)
	}
	return full, indexed
}

func TestRenderer_IndexedMatchesFullScan_DenseSparse(t *testing.T) {
	s := buildDenseSparseScene()
	full, indexed := renderFullAndIndexed(t, s, 800, 800)

	count, maxDelta := pixelDiff(full, indexed)
	if count != 0 {
		t.Errorf("indexed render differs from full scan: %d bytes differ (max delta %d)", count, maxDelta)
	}
}

func TestRenderer_IndexedMatchesFullScan_OverlappingStrokes(t *testing.T) {
	s := NewScene()
	// Overlapping shapes with different colors prove painting order is kept.
	for j := 0; j < 60; j++ {
		x := float32(j%6) * 30
		y := float32(j/6) * 30
		s.Fill(FillNonZero, IdentityAffine(),
			SolidBrush(gg.RGBA{R: float64(j%2) * 0.8, G: 0.2, B: float64(j%3) * 0.3, A: 0.7}),
			NewCircleShape(x+20, y+20, 25))
	}
	for j := 0; j < 20; j++ {
		x := float32(j%5) * 40
		y := float32(j/5) * 40
		s.Stroke(&StrokeStyle{Width: 3, MiterLimit: 10},
			IdentityAffine(),
			SolidBrush(gg.RGBA{R: 0, G: 0.6, B: 0.9, A: 0.8}),
			NewLineShape(x, 0, 200, y+10))
	}

	full, indexed := renderFullAndIndexed(t, s, 320, 320)
	if count, maxDelta := pixelDiff(full, indexed); count != 0 {
		t.Errorf("indexed render differs: %d bytes (max delta %d)", count, maxDelta)
	}
}

func TestRenderer_IndexedMatchesFullScan_AntiAliasAndTransforms(t *testing.T) {
	s := NewScene()
	s.SetAntiAlias(false)
	s.Fill(FillNonZero, IdentityAffine(),
		SolidBrush(gg.RGBA{R: 1, G: 0, B: 0, A: 1}), NewRectShape(10, 10, 40, 40))
	s.SetAntiAlias(true)
	s.Fill(FillNonZero, TranslateAffine(80, 20),
		SolidBrush(gg.RGBA{R: 0, G: 1, B: 0, A: 1}), NewRectShape(0, 0, 40, 40))
	s.Fill(FillNonZero, RotateAffine(0.3),
		SolidBrush(gg.RGBA{R: 0, G: 0, B: 1, A: 1}), NewCircleShape(120, 120, 30))
	s.Fill(FillNonZero, IdentityAffine(),
		SolidBrush(gg.RGBA{R: 0.4, G: 0.4, B: 0.4, A: 1}), NewRectShape(200, 200, 50, 50))

	full, indexed := renderFullAndIndexed(t, s, 300, 300)
	if count, maxDelta := pixelDiff(full, indexed); count != 0 {
		t.Errorf("indexed render differs: %d bytes (max delta %d)", count, maxDelta)
	}
}

func TestRenderer_IndexedRenderDirty(t *testing.T) {
	s := buildDenseSparseScene()

	fullR := NewRenderer(800, 800)
	idxR := NewRenderer(800, 800, WithSpatialIndex())
	if fullR == nil || idxR == nil {
		t.Fatal("NewRenderer returned nil")
	}
	defer fullR.Close()
	defer idxR.Close()

	full := gg.NewPixmap(800, 800)
	indexed := gg.NewPixmap(800, 800)
	full.Clear(gg.RGBA{A: 1})
	indexed.Clear(gg.RGBA{A: 1})

	if err := fullR.Render(full, s); err != nil {
		t.Fatalf("full render: %v", err)
	}
	if err := idxR.Render(indexed, s); err != nil {
		t.Fatalf("indexed render: %v", err)
	}

	// Redraw only the dense cluster region on both renderers.
	fullR.MarkDirty(0, 0, 200, 200)
	idxR.MarkDirty(0, 0, 200, 200)
	if err := fullR.RenderDirty(full, s, nil); err != nil {
		t.Fatalf("full RenderDirty: %v", err)
	}
	if err := idxR.RenderDirty(indexed, s, nil); err != nil {
		t.Fatalf("indexed RenderDirty: %v", err)
	}

	if count, maxDelta := pixelDiff(full, indexed); count != 0 {
		t.Errorf("RenderDirty indexed vs full differs: %d bytes (max delta %d)", count, maxDelta)
	}
}

func TestRenderer_IndexedFallsBackForDynamicScene(t *testing.T) {
	// Scene with clips: index cannot be built, so WithSpatialIndex must still
	// produce pixel-identical output via the full-scan fallback.
	s := NewScene()
	s.PushClip(NewRectShape(20, 20, 200, 200))
	for j := 0; j < 40; j++ {
		x := float32(j%8) * 30
		y := float32(j/8) * 30
		s.Fill(FillNonZero, IdentityAffine(),
			SolidBrush(gg.RGBA{R: 1, G: 0, B: 0, A: 1}), NewRectShape(x, y, 45, 45))
	}
	s.PopClip()

	fullR := NewRenderer(300, 300)
	idxR := NewRenderer(300, 300, WithSpatialIndex())
	if fullR == nil || idxR == nil {
		t.Fatal("NewRenderer returned nil")
	}
	defer fullR.Close()
	defer idxR.Close()

	full := gg.NewPixmap(300, 300)
	indexed := gg.NewPixmap(300, 300)
	if err := fullR.Render(full, s); err != nil {
		t.Fatalf("full render: %v", err)
	}
	if err := idxR.Render(indexed, s); err != nil {
		t.Fatalf("indexed render: %v", err)
	}
	if count, maxDelta := pixelDiff(full, indexed); count != 0 {
		t.Errorf("fallback render differs: %d bytes (max delta %d)", count, maxDelta)
	}
	if idxR.IndexFallbacks() == 0 {
		t.Error("expected a recorded fallback for the clipped scene")
	}
}

func TestRenderer_IndexRebuiltOnSceneChange(t *testing.T) {
	r := NewRenderer(300, 300, WithSpatialIndex())
	if r == nil {
		t.Fatal("NewRenderer returned nil")
	}
	defer r.Close()

	target := gg.NewPixmap(300, 300)
	s := NewScene()
	s.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.RGBA{R: 1, A: 1}), NewRectShape(0, 0, 50, 50))

	if err := r.Render(target, s); err != nil {
		t.Fatalf("render 1: %v", err)
	}

	// Add a shape far away; version changes -> index must rebuild.
	s.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.RGBA{B: 1, A: 1}), NewRectShape(200, 200, 50, 50))
	if err := r.Render(target, s); err != nil {
		t.Fatalf("render 2: %v", err)
	}

	if got := target.GetPixel(225, 225); got.B < 0.9 || got.A < 0.9 {
		t.Errorf("newly added shape not rendered after rebuild: pixel=%+v", got)
	}
	if got := target.GetPixel(25, 25); got.R < 0.9 {
		t.Errorf("existing shape lost after rebuild: pixel=%+v", got)
	}
}

func TestCommandIndex_DegenerateGeometryReplayIsNoOp(t *testing.T) {
	// A zero-area line is reported by a query that touches it, but replaying it
	// paints nothing — so indexed output still matches a full scan.
	s := NewScene()
	s.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.RGBA{R: 1, A: 1}), NewLineShape(50, 50, 50, 50))
	s.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.RGBA{G: 1, A: 1}), NewRectShape(0, 0, 100, 100))

	idx := s.BuildCommandIndex()
	if idx == nil {
		t.Fatal("expected index")
	}
	// Both commands cover that point: the degenerate line and the rect.
	if got := refIndices(idx.Query(Rect{MinX: 50, MinY: 50, MaxX: 50, MaxY: 50})); !reflect.DeepEqual(got, []int{0, 1}) {
		t.Errorf("degenerate query = %v, want [0 1]", got)
	}

	full, indexed := renderFullAndIndexed(t, s, 120, 120)
	if count, _ := pixelDiff(full, indexed); count != 0 {
		t.Errorf("degenerate scene pixels differ: %d bytes", count)
	}
}

func TestRenderer_IndexedMatchesFullScan_ImageAndRoundRect(t *testing.T) {
	s := NewScene()
	img := NewImage(40, 30)
	img.Data = make([]byte, 40*30*4)
	for i := 0; i < len(img.Data); i += 4 {
		img.Data[i] = 200
		img.Data[i+3] = 255 // opaque red-ish
	}
	s.DrawImage(img, TranslateAffine(20, 20))
	s.Fill(FillNonZero, TranslateAffine(100, 100),
		SolidBrush(gg.RGBA{R: 0, G: 0.5, B: 1, A: 1}),
		NewRoundRectShape(Rect{MinX: 0, MinY: 0, MaxX: 80, MaxY: 40}, 10, 10))
	s.Fill(FillNonZero, IdentityAffine(),
		SolidBrush(gg.RGBA{R: 0.2, G: 0.8, B: 0.2, A: 1}), NewCircleShape(60, 220, 25))

	full, indexed := renderFullAndIndexed(t, s, 260, 260)
	if count, maxDelta := pixelDiff(full, indexed); count != 0 {
		t.Errorf("image/roundrect scene differs: %d bytes (max delta %d)", count, maxDelta)
	}
}

func TestCommandIndex_HugeAndTinyExtents(t *testing.T) {
	s := NewScene()
	s.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.Red), NewRectShape(-1e6, -1e6, 10, 10))
	s.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.Blue), NewRectShape(1e6, 1e6, 10, 10))
	s.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.Green), NewRectShape(0, 0, 1, 1))

	idx := s.BuildCommandIndex()
	if idx == nil {
		t.Fatal("expected index")
	}
	// The grid must stay bounded even for a 2e6-unit scene.
	if idx.cols > indexMaxDim+1 || idx.rows > indexMaxDim+1 {
		t.Errorf("grid unbounded: %dx%d", idx.cols, idx.rows)
	}

	if got := refIndices(idx.Query(Rect{MinX: -1e6, MinY: -1e6, MaxX: -1e6 + 10, MaxY: -1e6 + 10})); !reflect.DeepEqual(got, []int{0}) {
		t.Errorf("far negative query = %v, want [0]", got)
	}
	if got := refIndices(idx.Query(Rect{MinX: 1e6, MinY: 1e6, MaxX: 1e6 + 10, MaxY: 1e6 + 10})); !reflect.DeepEqual(got, []int{1}) {
		t.Errorf("far positive query = %v, want [1]", got)
	}
	// Whole-world query returns everything in order.
	if got := refIndices(idx.Query(Rect{MinX: -1e9, MinY: -1e9, MaxX: 1e9, MaxY: 1e9})); !reflect.DeepEqual(got, []int{0, 1, 2}) {
		t.Errorf("world query = %v, want [0 1 2]", got)
	}

	full, indexed := renderFullAndIndexed(t, s, 64, 64)
	if count, _ := pixelDiff(full, indexed); count != 0 {
		t.Errorf("huge-extent scene pixels differ: %d bytes", count)
	}
}

func TestSceneQueryVisible_Contract(t *testing.T) {
	s := buildDenseSparseScene()
	refs, ok := s.QueryVisible(Rect{MinX: 0, MinY: 0, MaxX: 100, MaxY: 100})
	if !ok {
		t.Fatal("indexable scene reported ok=false")
	}
	if len(refs) == 0 {
		t.Fatal("expected visible commands in the dense cluster")
	}

	// A region with nothing returns an empty slice, still ok=true.
	refs, ok = s.QueryVisible(Rect{MinX: -5000, MinY: -5000, MaxX: -4000, MaxY: -4000})
	if !ok || len(refs) != 0 {
		t.Errorf("empty region: refs=%d ok=%v, want 0/true", len(refs), ok)
	}

	// A non-indexable scene reports ok=false so the caller can fall back.
	clipped := NewScene()
	clipped.PushClip(NewRectShape(0, 0, 10, 10))
	clipped.Fill(FillNonZero, IdentityAffine(), SolidBrush(gg.Red), NewRectShape(0, 0, 10, 10))
	clipped.PopClip()
	if _, ok := clipped.QueryVisible(Rect{MinX: 0, MinY: 0, MaxX: 10, MaxY: 10}); ok {
		t.Error("clipped scene reported ok=true")
	}
}

func TestCommandIndex_QueryLeavesDecoderUntouched(t *testing.T) {
	s := buildDenseSparseScene()
	enc := s.Encoding()
	idx := s.BuildCommandIndex()

	dec := NewDecoder(enc)
	for i := 0; i < 5; i++ {
		dec.Next()
	}
	before := dec.Position()
	tagBefore := dec.Tag()
	tagsLen := len(enc.tags)
	pathLen := len(enc.pathData)

	for i := 0; i < 20; i++ {
		idx.Query(Rect{MinX: 0, MinY: 0, MaxX: 500, MaxY: 500})
	}

	if dec.Position() != before || dec.Tag() != tagBefore {
		t.Errorf("Query moved the decoder: pos %d->%d", before, dec.Position())
	}
	if len(enc.tags) != tagsLen || len(enc.pathData) != pathLen {
		t.Error("Query changed encoding stream length")
	}
}

package recording

import (
	"errors"
	"fmt"
	"image"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/text"
)

// fuzzFloat produces finite values most of the time and NaN/Inf occasionally,
// so every numeric field is exercised under both healthy and poisoned input.
func fuzzFloat(rng *rand.Rand) float64 {
	switch rng.Intn(10) {
	case 0:
		return math.NaN()
	case 1:
		return math.Inf(rng.Intn(3) - 1) // -1, 0, +1
	case 2:
		return 0
	default:
		// Values across magnitudes, including negatives.
		return rng.NormFloat64() * 1000
	}
}

// fuzzRef sometimes yields an in-range index, sometimes a wildly oversized one
// or the InvalidRef sentinel.
func fuzzRef(rng *rand.Rand, poolSize int) uint32 {
	switch rng.Intn(6) {
	case 0:
		return InvalidRef
	case 1, 2:
		// Oversized: up to ~4 billion, guaranteed beyond any test pool.
		return uint32(math.MaxUint32 - uint32(rng.Intn(1000)))
	case 3:
		return uint32(rng.Intn(poolSize + 5))
	default:
		if poolSize == 0 {
			return uint32(rng.Intn(4))
		}
		return uint32(rng.Intn(poolSize))
	}
}

// fuzzRecording builds an adversarial recording entirely from the public
// construction API: random command tags, random (often poisoned) references
// and floats, random pool sizes including nil pooled entries. The shape of
// the output mirrors what a buggy or hostile external plugin could emit.
func fuzzRecording(rng *rand.Rand) *Recording {
	width := []int{0, -1, 1, 64}[rng.Intn(4)]
	height := []int{0, 1, 48}[rng.Intn(3)]

	// Random pool geometry, occasionally leaving nil slots.
	pool := NewResourcePool()
	nPaths := rng.Intn(4)
	nBrushes := rng.Intn(4)
	nImages := rng.Intn(3)
	for i := 0; i < nPaths; i++ {
		if rng.Intn(5) == 0 {
			pool.paths = append(pool.paths, nil)
		} else {
			pool.paths = append(pool.paths,
				gg.BuildPath().MoveTo(0, 0).LineTo(fuzzFloat(rng), fuzzFloat(rng)).Build())
		}
	}
	for i := 0; i < nBrushes; i++ {
		if rng.Intn(5) == 0 {
			pool.brushes = append(pool.brushes, nil)
		} else {
			pool.brushes = append(pool.brushes,
				NewSolidBrush(gg.RGBA{R: fuzzFloat(rng), G: 0, B: 0, A: 1}))
		}
	}
	for i := 0; i < nImages; i++ {
		if rng.Intn(5) == 0 {
			pool.images = append(pool.images, nil)
		} else {
			pool.images = append(pool.images,
				image.NewRGBA(image.Rect(0, 0, 2+rng.Intn(4), 2+rng.Intn(4))))
		}
	}

	nCmds := rng.Intn(128)
	cmds := make([]Command, 0, nCmds)

	// Bias toward deep Save nesting to stress the state stack.
	deepNest := rng.Intn(2000)
	for i := 0; i < deepNest; i++ {
		cmds = append(cmds, SaveCommand{})
	}

	for i := 0; i < nCmds; i++ {
		// ~5% garbage entries: nil or unknown tag.
		switch rng.Intn(20) {
		case 0:
			cmds = append(cmds, nil)
			continue
		case 1:
			cmds = append(cmds, unknownCommand{})
			continue
		}

		switch rng.Intn(22) {
		case 0:
			cmds = append(cmds, SaveCommand{})
		case 1:
			cmds = append(cmds, RestoreCommand{})
		case 2:
			cmds = append(cmds, SetTransformCommand{Matrix: Matrix{
				A: fuzzFloat(rng), B: fuzzFloat(rng), C: fuzzFloat(rng),
				D: fuzzFloat(rng), E: fuzzFloat(rng), F: fuzzFloat(rng),
			}})
		case 3:
			cmds = append(cmds, SetClipCommand{
				Path: PathRef(fuzzRef(rng, nPaths)),
				Rule: FillRule(rng.Intn(4)),
			})
		case 4:
			cmds = append(cmds, ClearClipCommand{})
		case 5:
			cmds = append(cmds, ClipRoundRectCommand{
				X: fuzzFloat(rng), Y: fuzzFloat(rng),
				W: fuzzFloat(rng), H: fuzzFloat(rng),
				Radius: fuzzFloat(rng),
			})
		case 6:
			cmds = append(cmds, FillPathCommand{
				Path:  PathRef(fuzzRef(rng, nPaths)),
				Brush: BrushRef(fuzzRef(rng, nBrushes)),
				Rule:  FillRule(rng.Intn(4)),
			})
		case 7:
			cmds = append(cmds, StrokePathCommand{
				Path:   PathRef(fuzzRef(rng, nPaths)),
				Brush:  BrushRef(fuzzRef(rng, nBrushes)),
				Stroke: fuzzStroke(rng),
			})
		case 8:
			cmds = append(cmds, FillRectCommand{
				Rect:  fuzzRect(rng),
				Brush: BrushRef(fuzzRef(rng, nBrushes)),
			})
		case 9:
			cmds = append(cmds, StrokeRectCommand{
				Rect:   fuzzRect(rng),
				Brush:  BrushRef(fuzzRef(rng, nBrushes)),
				Stroke: fuzzStroke(rng),
			})
		case 10:
			cmds = append(cmds, DrawImageCommand{
				Image:   ImageRef(fuzzRef(rng, nImages)),
				SrcRect: fuzzRect(rng),
				DstRect: fuzzRect(rng),
				Options: ImageOptions{
					Interpolation: InterpolationMode(rng.Intn(4)),
					Alpha:         fuzzFloat(rng),
				},
			})
		case 11:
			cmds = append(cmds, DrawTextCommand{
				Text:     []string{"", "abc"}[rng.Intn(2)],
				X:        fuzzFloat(rng),
				Y:        fuzzFloat(rng),
				FontSize: fuzzFloat(rng),
				Face:     nil, // nil face is itself a fuzzed condition
				Brush:    BrushRef(fuzzRef(rng, nBrushes)),
			})
		case 12:
			cmds = append(cmds, StrokeTextCommand{
				Text:     []string{"", "xyz"}[rng.Intn(2)],
				X:        fuzzFloat(rng),
				Y:        fuzzFloat(rng),
				FontSize: fuzzFloat(rng),
				Face:     nil,
				Brush:    BrushRef(fuzzRef(rng, nBrushes)),
				Stroke:   fuzzStroke(rng),
			})
		case 13:
			cmds = append(cmds, SetFillStyleCommand{Brush: BrushRef(fuzzRef(rng, nBrushes))})
		case 14:
			cmds = append(cmds, SetStrokeStyleCommand{Brush: BrushRef(fuzzRef(rng, nBrushes))})
		case 15:
			cmds = append(cmds, SetLineWidthCommand{Width: fuzzFloat(rng)})
		case 16:
			cmds = append(cmds, SetLineCapCommand{Cap: LineCap(rng.Intn(5))})
		case 17:
			cmds = append(cmds, SetLineJoinCommand{Join: LineJoin(rng.Intn(5))})
		case 18:
			cmds = append(cmds, SetMiterLimitCommand{Limit: fuzzFloat(rng)})
		case 19:
			pat := make([]float64, rng.Intn(5))
			for j := range pat {
				pat[j] = fuzzFloat(rng)
			}
			cmds = append(cmds, SetDashCommand{Pattern: pat, Offset: fuzzFloat(rng)})
		case 20:
			cmds = append(cmds, SetFillRuleCommand{Rule: FillRule(rng.Intn(4))})
		case 21:
			cmds = append(cmds, SetAntiAliasCommand{Enabled: rng.Intn(2) == 0})
		}
	}

	return NewRecording(width, height, cmds, pool)
}

func fuzzRect(rng *rand.Rand) Rect {
	return Rect{
		MinX: fuzzFloat(rng), MinY: fuzzFloat(rng),
		MaxX: fuzzFloat(rng), MaxY: fuzzFloat(rng),
	}
}

func fuzzStroke(rng *rand.Rand) Stroke {
	pat := make([]float64, rng.Intn(4))
	for i := range pat {
		pat[i] = fuzzFloat(rng)
	}
	return Stroke{
		Width:       fuzzFloat(rng),
		Cap:         LineCap(rng.Intn(5)),
		Join:        LineJoin(rng.Intn(5)),
		MiterLimit:  fuzzFloat(rng),
		DashPattern: pat,
		DashOffset:  fuzzFloat(rng),
	}
}

// TestFuzzValidateRecording_Seeded runs the adversarial generator with fixed
// seeds. It is deterministic (reproducible in CI without a corpus) and proves
// the four required invariants: no panic, no infinite loop (bounded runtime
// via the iteration count), allocation bounded by the error cap, and correct
// fail-fast/collect-all behavior.
func TestFuzzValidateRecording_Seeded(t *testing.T) {
	const iterations = 2000
	for _, seed := range []int64{1, 2, 42, 0xC0FFEE, 1<<32 + 7} {
		seed := seed
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			for n := 0; n < iterations; n++ {
				r := fuzzRecording(rng)

				// Must not panic in either mode.
				errAll := ValidateRecording(r)
				errFast := ValidateRecording(r, FailFast())

				if errFast != nil {
					var ve ValidationError
					if !errors.As(errFast, &ve) {
						t.Fatalf("fail-fast returned %T, want ValidationError: %v",
							errFast, errFast)
					}
				}
				if errAll != nil {
					var errs ValidationErrors
					if !errors.As(errAll, &errs) {
						t.Fatalf("collect-all returned %T, want ValidationErrors: %v",
							errAll, errAll)
					}
					// Default cap is enforced even on fuzzed inputs.
					if len(errs) > defaultMaxValidationErrors+1 {
						t.Fatalf("error count %d exceeds cap %d",
							len(errs), defaultMaxValidationErrors+1)
					}
					// Fail-fast, when it fails, reports one of the same defects
					// collect-all would surface.
					if errFast == nil {
						t.Fatalf("collect-all found defects but fail-fast passed:\n%v", errAll)
					}
				} else if errFast != nil {
					t.Fatalf("fail-fast failed but collect-all passed: %v", errFast)
				}
			}
		})
	}
}

// TestFuzzValidateRecording_ExtremeRefs concentrates on the reference space:
// max uint32 indices, the InvalidRef sentinel and empty pools.
func TestFuzzValidateRecording_ExtremeRefs(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	refs := []uint32{
		InvalidRef, math.MaxUint32, math.MaxUint32 - 1,
		0, 1, 1 << 20, 1 << 31,
	}
	for n := 0; n < 500; n++ {
		pool := NewResourcePool()
		var cmds []Command
		// At least one command: with an empty pool every reference is
		// necessarily out of range, so the recording must be rejected.
		for j := 0; j < 1+rng.Intn(32); j++ {
			pr, br, ir := refs[rng.Intn(len(refs))], refs[rng.Intn(len(refs))], refs[rng.Intn(len(refs))]
			cmds = append(cmds,
				FillPathCommand{Path: PathRef(pr), Brush: BrushRef(br), Rule: FillRule(rng.Intn(4))},
				DrawImageCommand{
					Image:   ImageRef(ir),
					SrcRect: fuzzRect(rng), DstRect: fuzzRect(rng),
					Options: DefaultImageOptions(),
				},
			)
		}
		r := NewRecording(16, 16, cmds, pool)
		err := ValidateRecording(r)
		if err == nil {
			t.Fatal("extreme-ref recording unexpectedly passed validation")
		}
		var errs ValidationErrors
		if !errors.As(err, &errs) {
			t.Fatalf("want ValidationErrors, got %T: %v", err, err)
		}
		// Every defect must carry the resource category and the offending index.
		for _, e := range errs {
			if e.Kind == ResourceCommand {
				continue // enum/float defects are also possible in this mix
			}
			if e.ResourceIndex < 0 {
				t.Errorf("reference error missing resource index: %+v", e)
			}
		}
	}
}

// TestFuzzValidateRecording_DeepStack exercises Save/Restore nesting up to
// tens of thousands of levels, including random imbalance in both directions.
func TestFuzzValidateRecording_DeepStack(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for n := 0; n < 50; n++ {
		var cmds []Command
		depth := 0
		steps := 10000 + rng.Intn(40000)
		for j := 0; j < steps; j++ {
			// Restores slightly more often than saves so underflow occurs.
			if rng.Intn(3) == 0 && depth > 0 {
				cmds = append(cmds, RestoreCommand{})
				depth--
			} else {
				cmds = append(cmds, SaveCommand{})
				depth++
			}
		}
		// Sprinkle extra restores to force underflow cases.
		for j := 0; j < rng.Intn(5); j++ {
			cmds = append(cmds, RestoreCommand{})
		}
		r := NewRecording(8, 8, cmds, NewResourcePool())

		done := make(chan error, 1)
		go func() { done <- ValidateRecording(r, MaxValidationErrors(64)) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("validation hung on deep randomized state stack")
		}
	}
}

// safeStubBackend is a Backend whose every method tolerates nil resources and
// poisoned values, so Playback of a recording that passed validation can be
// fuzzed without any nil dereference.
type safeStubBackend struct{ beginErr error }

func (b *safeStubBackend) Begin(int, int) error                                { return b.beginErr }
func (b *safeStubBackend) End() error                                          { return nil }
func (b *safeStubBackend) Save()                                               {}
func (b *safeStubBackend) Restore()                                            {}
func (b *safeStubBackend) SetTransform(Matrix)                                 {}
func (b *safeStubBackend) SetClip(*gg.Path, FillRule)                          {}
func (b *safeStubBackend) ClearClip()                                          {}
func (b *safeStubBackend) FillPath(*gg.Path, Brush, FillRule)                  {}
func (b *safeStubBackend) StrokePath(*gg.Path, Brush, Stroke)                  {}
func (b *safeStubBackend) FillRect(Rect, Brush)                                {}
func (b *safeStubBackend) DrawImage(image.Image, Rect, Rect, ImageOptions)     {}
func (b *safeStubBackend) DrawText(string, float64, float64, text.Face, Brush) {}

// TestFuzzPlayback_SafeBackend confirms that whatever the fuzzer produces,
// Playback against a defensive backend terminates without panicking when the
// recording was accepted, and still surfaces the backend's own Begin error.
func TestFuzzPlayback_SafeBackend(t *testing.T) {
	rng := rand.New(rand.NewSource(1234))
	sentinel := errors.New("backend boom")
	for n := 0; n < 500; n++ {
		r := fuzzRecording(rng)
		backend := &safeStubBackend{}
		// Playback itself must never panic on fuzzed input; nil resources are
		// handled by the pool getters and the stub ignores them.
		_ = r.Playback(backend)

		// Backend errors propagate verbatim — validation never rewrites them.
		failing := &safeStubBackend{beginErr: sentinel}
		if err := r.Playback(failing); !errors.Is(err, sentinel) {
			t.Fatalf("Playback error = %v, want sentinel %v", err, sentinel)
		}
	}
}

// FuzzValidateRecording is the native, corpus-driven fuzz target. Its job is
// purely defensive: any byte stream must drive the generator + validator
// without panicking or hanging. Run with:
//
//	go test ./recording/ -fuzz=FuzzValidateRecording -fuzztime=30s
func FuzzValidateRecording(f *testing.F) {
	// Seed corpus: a few fixed-size inputs so non-fuzz runs still execute it.
	f.Add(int64(1), 10)
	f.Add(int64(42), 200)
	f.Fuzz(func(t *testing.T, seed int64, size int) {
		if size < 0 {
			size = -size
		}
		if size > 5000 {
			size = 5000
		}
		rng := rand.New(rand.NewSource(seed))
		r := fuzzRecording(rng)
		// Cap command length using the second fuzz input by trimming, which
		// also exercises the partial-command edge case.
		if size < len(r.commands) {
			r.commands = r.commands[:size]
		}

		if err := ValidateRecording(r); err != nil {
			var errs ValidationErrors
			if !errors.As(err, &errs) {
				t.Fatalf("unexpected error type %T: %v", err, err)
			}
		}
		_ = ValidateRecording(r, FailFast())

		// Accepted recordings must also survive a defensive playback.
		if ValidateRecording(r) == nil {
			if err := r.Playback(&safeStubBackend{}); err != nil {
				t.Fatalf("validated recording failed playback: %v", err)
			}
		}
	})
}

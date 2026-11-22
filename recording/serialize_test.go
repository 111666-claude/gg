package recording

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"testing"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/text"
	"golang.org/x/image/font/gofont/goregular"
)

// buildFixedRecording builds the fixed recording used by the serialization
// tests: text, an image, clipping, rounded rectangles, and nested transforms.
func buildFixedRecording(t *testing.T) *Recording {
	t.Helper()

	src, err := text.NewFontSource(goregular.TTF)
	if err != nil {
		t.Fatalf("NewFontSource: %v", err)
	}
	face := src.Face(18)

	// 8x8 test image with a deterministic pixel pattern.
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(x * 32),
				G: uint8(y * 32),
				B: uint8((x + y) * 16),
				A: 255,
			})
		}
	}

	rec := NewRecorder(200, 150)
	rec.ClearWithColor(gg.White)

	// Nested transforms with a filled rounded rectangle.
	rec.Save()
	rec.Translate(40, 30)
	rec.Save()
	rec.Rotate(0.25)
	rec.Scale(1.5, 1.5)
	rec.SetFillRGBA(0.8, 0.2, 0.2, 1)
	rec.DrawRoundedRectangle(0, 0, 60, 40, 8)
	rec.Fill()
	rec.Restore()

	// Clip, then draw an image inside the clip.
	rec.ClipRoundRect(10, 10, 120, 90, 12)
	rec.DrawImage(img, 20, 20)
	rec.ResetClip()

	// Stroked rounded rectangle with dash.
	rec.SetStrokeRGBA(0, 0, 0.6, 1)
	rec.SetLineWidth(2.5)
	rec.SetDash(6, 3)
	rec.DrawRoundedRectangle(100, 60, 80, 50, 10)
	rec.Stroke()
	rec.ClearDash()

	// Text with a real font face.
	rec.SetFont(face)
	rec.SetFontSize(18)
	rec.SetFillRGBA(0.1, 0.1, 0.1, 1)
	rec.DrawString("gg", 60, 120)

	rec.Restore()

	return rec.FinishRecording()
}

func TestSerializeByteStability(t *testing.T) {
	rec := buildFixedRecording(t)

	a, err := rec.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	b, err := rec.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary (2nd): %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("same recording encoded twice produced different bytes")
	}

	// Decode → re-encode must reproduce the exact same bytes.
	dec, err := Decode(bytes.NewReader(a))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	c, err := dec.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary (decoded): %v", err)
	}
	if !bytes.Equal(a, c) {
		t.Fatal("decode→encode round trip changed the bytes")
	}
}

func TestSerializeRoundTripStructure(t *testing.T) {
	rec := buildFixedRecording(t)
	data, err := rec.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	dec, err := Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if dec.Width() != rec.Width() || dec.Height() != rec.Height() {
		t.Errorf("canvas size = %dx%d, want %dx%d", dec.Width(), dec.Height(), rec.Width(), rec.Height())
	}
	if len(dec.Commands()) != len(rec.Commands()) {
		t.Fatalf("command count = %d, want %d", len(dec.Commands()), len(rec.Commands()))
	}
	for i, cmd := range rec.Commands() {
		if dec.Commands()[i].Type() != cmd.Type() {
			t.Errorf("command %d type = %v, want %v", i, dec.Commands()[i].Type(), cmd.Type())
		}
	}

	// Resource indices stay consistent: table sizes are preserved.
	rp, dp := rec.Resources(), dec.Resources()
	if rp.PathCount() != dp.PathCount() {
		t.Errorf("path count = %d, want %d", dp.PathCount(), rp.PathCount())
	}
	if rp.BrushCount() != dp.BrushCount() {
		t.Errorf("brush count = %d, want %d", dp.BrushCount(), rp.BrushCount())
	}
	if rp.ImageCount() != dp.ImageCount() {
		t.Errorf("image count = %d, want %d", dp.ImageCount(), rp.ImageCount())
	}

	// Paths survive the round trip exactly.
	for i := 0; i < rp.PathCount(); i++ {
		op, np := rp.GetPath(PathRef(i)), dp.GetPath(PathRef(i))
		if (op == nil) != (np == nil) {
			t.Fatalf("path %d nil-ness mismatch", i)
		}
		if op == nil {
			continue
		}
		ov, nv := op.Verbs(), np.Verbs()
		oc, nc := op.Coords(), np.Coords()
		if len(ov) != len(nv) || len(oc) != len(nc) {
			t.Fatalf("path %d size mismatch: (%d verbs, %d coords) vs (%d, %d)",
				i, len(nv), len(nc), len(ov), len(oc))
		}
		for j := range ov {
			if ov[j] != nv[j] {
				t.Errorf("path %d verb %d = %v, want %v", i, j, nv[j], ov[j])
			}
		}
		for j := range oc {
			if oc[j] != nc[j] {
				t.Errorf("path %d coord %d = %v, want %v", i, j, nc[j], oc[j])
			}
		}
	}

	// The decoded recording carries a real, usable font face.
	var face text.Face
	for _, cmd := range dec.Commands() {
		if tc, ok := cmd.(DrawTextCommand); ok {
			face = tc.Face
		}
	}
	if face == nil {
		t.Fatal("decoded DrawTextCommand has nil face")
	}
	if w, _ := text.Measure("gg", face); w <= 0 {
		t.Errorf("decoded face measures zero width for %q", "gg")
	}
}

func TestWriteToMatchesMarshal(t *testing.T) {
	rec := buildFixedRecording(t)
	want, err := rec.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	var buf bytes.Buffer
	n, err := rec.WriteTo(&buf)
	if err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	if n != int64(len(want)) {
		t.Errorf("WriteTo returned %d, want %d", n, len(want))
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Error("WriteTo bytes differ from MarshalBinary")
	}
}

// --------------------------------------------------------------------------
// Corrupt-input tests
// --------------------------------------------------------------------------

// smallRecording builds a minimal recording without fonts or images so every
// truncation point can be tested exhaustively.
func smallRecording(t *testing.T) *Recording {
	t.Helper()
	rec := NewRecorder(64, 64)
	rec.SetFillRGB(1, 0, 0)
	rec.DrawRectangle(4, 4, 20, 20)
	rec.Fill()
	rec.Save()
	rec.Translate(10, 10)
	rec.SetStrokeRGB(0, 0, 1)
	rec.SetLineWidth(2)
	rec.DrawCircle(30, 30, 8)
	rec.Stroke()
	rec.Restore()
	return rec.FinishRecording()
}

func TestDecodeTruncationEverywhere(t *testing.T) {
	data, err := smallRecording(t).MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	// The full input decodes; every strict prefix must fail, never panic.
	if _, err := Decode(bytes.NewReader(data)); err != nil {
		t.Fatalf("Decode(full): %v", err)
	}
	for n := 0; n < len(data); n++ {
		if _, err := Decode(bytes.NewReader(data[:n])); err == nil {
			t.Fatalf("Decode of %d-byte prefix succeeded, want error", n)
		}
	}
}

// mutate returns a copy of data with fn applied at the given offset.
func mutate(data []byte, off int, fn func(b []byte)) []byte {
	out := append([]byte(nil), data...)
	fn(out[off:])
	return out
}

func TestDecodeCorruptInputs(t *testing.T) {
	valid, err := smallRecording(t).MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}

	tests := []struct {
		name string
		data []byte
		want error
	}{
		{
			name: "bad magic",
			data: mutate(valid, 0, func(b []byte) { b[0] = 'X' }),
			want: ErrInvalidMagic,
		},
		{
			name: "unknown version",
			data: mutate(valid, 4, func(b []byte) { b[0] = 0x7F; b[1] = 0x7F }),
			want: ErrUnsupportedVersion,
		},
		{
			name: "nonzero flags",
			data: mutate(valid, 6, func(b []byte) { b[0] = 1 }),
			want: ErrMalformed,
		},
		{
			name: "negative width",
			data: mutate(valid, 8, func(b []byte) { b[0] = 0xFF; b[1] = 0xFF; b[2] = 0xFF; b[3] = 0xFF }),
			want: ErrMalformed,
		},
		{
			name: "trailing garbage",
			data: append(append([]byte(nil), valid...), 0, 0, 0, 0),
			want: ErrUnknownChunk,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Decode(bytes.NewReader(tc.data))
			if err == nil {
				t.Fatal("Decode succeeded, want error")
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("error = %v, want errors.Is %v", err, tc.want)
			}
			var de *DecodeError
			if !errors.As(err, &de) {
				t.Errorf("error is not a *DecodeError: %T", err)
			} else if de.Offset < 0 {
				t.Errorf("DecodeError has negative offset %d", de.Offset)
			}
		})
	}
}

// chunkOffsets returns the absolute offsets of each chunk's tag in data.
func chunkOffsets(t *testing.T, data []byte) map[string]int {
	t.Helper()
	out := make(map[string]int)
	off := headerSize
	for off < len(data) {
		tag := string(data[off : off+4])
		out[tag] = off
		length := int(binaryLE32(data[off+4:]))
		off += 8 + length
	}
	if off != len(data) {
		t.Fatalf("chunk walk ended at %d, len %d", off, len(data))
	}
	return out
}

func binaryLE32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func TestDecodeDuplicateResourceTable(t *testing.T) {
	valid, err := smallRecording(t).MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	chunks := chunkOffsets(t, valid)

	// Duplicate the PATH chunk right after the original.
	start := chunks[chunkPaths]
	length := 8 + int(binaryLE32(valid[start+4:]))
	pathChunk := valid[start : start+length]

	dup := make([]byte, 0, len(valid)+length)
	dup = append(dup, valid[:start+length]...)
	dup = append(dup, pathChunk...)
	dup = append(dup, valid[start+length:]...)

	_, err = Decode(bytes.NewReader(dup))
	if !errors.Is(err, ErrDuplicateChunk) {
		t.Fatalf("error = %v, want ErrDuplicateChunk", err)
	}
	var de *DecodeError
	if errors.As(err, &de) && de.Offset != int64(start+length) {
		t.Errorf("offset = %d, want %d", de.Offset, start+length)
	}
}

func TestDecodeDanglingResourceRef(t *testing.T) {
	valid, err := smallRecording(t).MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	chunks := chunkOffsets(t, valid)

	// Corrupt the brush reference of the first FillPath command to a huge
	// index. FillPath layout: type(1) pathRef(4) brushRef(4) rule(1).
	cmds := chunks[chunkCommands]
	payload := cmds + 8
	count := int(binaryLE32(valid[payload:]))
	pos := payload + 4
	corrupted := false
	for i := 0; i < count; i++ {
		if valid[pos] == byte(CmdFillPath) {
			bad := append([]byte(nil), valid...)
			bad[pos+5] = 0xFF
			bad[pos+6] = 0xFF
			bad[pos+7] = 0xFF
			bad[pos+8] = 0x7F
			_, err := Decode(bytes.NewReader(bad))
			if !errors.Is(err, ErrDanglingRef) {
				t.Fatalf("error = %v, want ErrDanglingRef", err)
			}
			var de *DecodeError
			if errors.As(err, &de) && de.Command != i {
				t.Errorf("command index = %d, want %d", de.Command, i)
			}
			corrupted = true
			break
		}
		pos += commandSize(t, valid, pos)
	}
	if !corrupted {
		t.Fatal("no FillPath command found in small recording")
	}
}

// commandSize returns the encoded size of the command at pos, so tests can
// walk the command stream. It fails the test on unexpected command types.
func commandSize(t *testing.T, data []byte, pos int) int {
	t.Helper()
	typ := CommandType(data[pos])
	switch typ {
	case CmdSave, CmdRestore, CmdClearClip:
		return 1
	case CmdSetTransform:
		return 1 + 48
	case CmdSetClip:
		return 1 + 5
	case CmdClipRoundRect:
		return 1 + 40
	case CmdFillPath:
		return 1 + 9
	case CmdStrokePath:
		return 1 + 8 + strokeSize(data, pos+9)
	case CmdFillRect:
		return 1 + 36
	case CmdStrokeRect:
		return 1 + 32 + strokeSize(data, pos+33)
	case CmdDrawImage:
		return 1 + 4 + 64 + 1 + 8
	case CmdDrawText:
		return 1 + textCmdSize(data, pos+1)
	case CmdStrokeText:
		base := textCmdSize(data, pos+1)
		return 1 + base + strokeSize(data, pos+1+base)
	case CmdSetFillStyle, CmdSetStrokeStyle:
		return 1 + 4
	case CmdSetLineWidth, CmdSetMiterLimit:
		return 1 + 8
	case CmdSetLineCap, CmdSetLineJoin, CmdSetFillRule, CmdSetAntiAlias:
		return 1 + 1
	case CmdSetDash:
		n := int(binaryLE32(data[pos+1:]))
		return 1 + 4 + n*8 + 8
	default:
		t.Fatalf("unexpected command type %d at %d", typ, pos)
		return 0
	}
}

// strokeSize returns the encoded size of a Stroke at pos:
// width(8) cap(1) join(1) miter(8) dashCount(4) dash[n*8] offset(8).
func strokeSize(data []byte, pos int) int {
	n := int(binaryLE32(data[pos+18:]))
	return 22 + n*8 + 8
}

// textCmdSize returns the encoded size of a text command body starting after
// the type byte: fontRef(4) text(4+n) x(8) y(8) fontSize(8) family(4+m) brush(4).
func textCmdSize(data []byte, pos int) int {
	textLen := int(binaryLE32(data[pos+4:]))
	famOff := pos + 4 + 4 + textLen + 8 + 8 + 8
	famLen := int(binaryLE32(data[famOff:]))
	return 4 + 4 + textLen + 24 + 4 + famLen + 4
}

func TestDecodeHugeAllocations(t *testing.T) {
	valid, err := smallRecording(t).MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	chunks := chunkOffsets(t, valid)
	paths := chunks[chunkPaths]

	// Path table count just under the limit but far beyond the remaining
	// input: must fail with a length overflow before allocating.
	bad := append([]byte(nil), valid...)
	bad[paths+8] = 0xFF
	bad[paths+9] = 0xFF
	bad[paths+10] = 0xFF
	bad[paths+11] = 0x00 // 0x00FFFFFF
	if _, err := Decode(bytes.NewReader(bad)); !errors.Is(err, ErrLengthOverflow) {
		t.Fatalf("error = %v, want ErrLengthOverflow", err)
	}

	// A single path declaring 0x02000000 verbs exceeds the per-path verb
	// limit and must fail without allocating.
	bad2 := append([]byte(nil), valid...)
	voff := paths + 8 + 4 // first path's verbCount
	bad2[voff] = 0x00
	bad2[voff+1] = 0x00
	bad2[voff+2] = 0x00
	bad2[voff+3] = 0x02
	if _, err := Decode(bytes.NewReader(bad2)); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("error = %v, want ErrLimitExceeded", err)
	}
}

func TestDecodePathCoordMismatch(t *testing.T) {
	valid, err := smallRecording(t).MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	chunks := chunkOffsets(t, valid)

	// Set the first path's coordCount to 1; its verbs consume more.
	paths := chunks[chunkPaths]
	coff := paths + 8 + 4 + 4 // first path's coordCount
	bad := append([]byte(nil), valid...)
	bad[coff] = 1
	bad[coff+1] = 0
	bad[coff+2] = 0
	bad[coff+3] = 0
	if _, err := Decode(bytes.NewReader(bad)); !errors.Is(err, ErrLengthOverflow) {
		t.Fatalf("error = %v, want ErrLengthOverflow", err)
	}
}

func TestDecodeMissingCommandsChunk(t *testing.T) {
	valid, err := smallRecording(t).MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	chunks := chunkOffsets(t, valid)

	// Drop the CMDS chunk entirely.
	truncated := valid[:chunks[chunkCommands]]
	if _, err := Decode(bytes.NewReader(truncated)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("error = %v, want ErrMalformed", err)
	}
}

func TestDecodeUnknownCommandType(t *testing.T) {
	valid, err := smallRecording(t).MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	chunks := chunkOffsets(t, valid)

	// Corrupt the first command's type byte.
	pos := chunks[chunkCommands] + 8 + 4
	bad := append([]byte(nil), valid...)
	bad[pos] = 0xFE
	if _, err := Decode(bytes.NewReader(bad)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("error = %v, want ErrMalformed", err)
	}
}

func TestDecodeFontResolver(t *testing.T) {
	// A face whose source has been closed carries no embeddable font data.
	// The font reference (name + size) still round-trips and resolves
	// through WithFontResolver.
	src, err := text.NewFontSource(goregular.TTF)
	if err != nil {
		t.Fatalf("NewFontSource: %v", err)
	}
	wantName := src.Name()
	face := src.Face(20)

	rec := NewRecorder(100, 50)
	rec.SetFont(face)
	rec.SetFontSize(20)
	rec.DrawString("hi", 5, 30)
	r := rec.FinishRecording()

	// Close the source so its data is gone before encoding.
	if err := src.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, err := r.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}

	resolvedSrc, err := text.NewFontSource(goregular.TTF)
	if err != nil {
		t.Fatalf("NewFontSource (resolver): %v", err)
	}
	resolved := resolvedSrc.Face(20)

	var gotName string
	var gotSize float64
	dec, err := Decode(bytes.NewReader(data), WithFontResolver(func(name string, size float64) text.Face {
		gotName, gotSize = name, size
		return resolved
	}))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if gotName != wantName {
		t.Errorf("resolver got name %q, want %q", gotName, wantName)
	}
	if gotSize != 20 {
		t.Errorf("resolver got size %v, want 20", gotSize)
	}
	for _, cmd := range dec.Commands() {
		if tc, ok := cmd.(DrawTextCommand); ok && tc.Face != resolved {
			t.Error("decoded command face is not the resolved face")
		}
	}

	// Without a resolver the face decodes to nil (no error, no panic).
	dec2, err := Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Decode without resolver: %v", err)
	}
	for _, cmd := range dec2.Commands() {
		if tc, ok := cmd.(DrawTextCommand); ok && tc.Face != nil {
			t.Error("face should be nil without a resolver")
		}
	}
}

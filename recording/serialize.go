package recording

// Binary persistence for Recording.
//
// Recording is otherwise an in-process object: nothing here changes the
// existing Playback/Backend interfaces or in-memory behavior. This file adds
// a versioned binary format so a recording can be written to an io.Writer and
// read back later — in another process, or after an editor restart — with
// canvas size, command order, paths, brushes, image references, font
// references, and transforms intact, and with resource indices preserved.
//
// # Wire format (all integers little-endian)
//
//	Header (16 bytes):
//	  magic      [4]byte  "GGRC"
//	  version    uint16   FormatVersion
//	  flags      uint16   reserved, must be 0
//	  width      int32    canvas width
//	  height     int32    canvas height
//
//	Chunks, in this exact order (each at most once, CMDS required and last):
//	  "PATH"  path table
//	  "IMAG"  image table
//	  "BRUS"  brush table (may reference images)
//	  "FONT"  font table
//	  "CMDS"  command stream
//
//	Chunk layout: tag [4]byte, payload length uint32, payload bytes.
//
// Encoding is deterministic: the same Recording always produces the same
// bytes, and a decode→encode round trip reproduces the input byte-for-byte.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"io"
	"math"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/text"
)

// FormatVersion is the current binary format version written by MarshalBinary
// and accepted by Decode.
const FormatVersion uint16 = 1

const (
	formatMagic = "GGRC"
	headerSize  = 16

	chunkPaths    = "PATH"
	chunkImages   = "IMAG"
	chunkBrushes  = "BRUS"
	chunkFonts    = "FONT"
	chunkCommands = "CMDS"
)

// chunkOrder defines the required on-disk chunk sequence.
var chunkOrder = map[string]int{
	chunkPaths:    0,
	chunkImages:   1,
	chunkBrushes:  2,
	chunkFonts:    3,
	chunkCommands: 4,
}

// Decoder limits. They bound allocations so a corrupt or hostile stream
// fails with an error instead of triggering huge allocations.
const (
	maxRecordingSize = 1 << 30 // 1 GiB total input
	maxResourceCount = 1 << 24 // entries per resource table
	maxCommandCount  = 1 << 24
	maxPathVerbs     = 1 << 24
	maxPathCoords    = 1 << 26
	maxStringLen     = 1 << 24
	maxDashLen       = 1 << 16
	maxGradientStops = 1 << 16
	maxImageDim      = 1 << 15
	maxImagePixels   = 1 << 30
	maxFontDataSize  = 1 << 28
)

// nilPathSentinel marks a nil path slot in the path table.
const nilPathSentinel = 0xFFFFFFFF

// Brush wire kinds.
const (
	brushKindSolid uint8 = iota
	brushKindLinear
	brushKindRadial
	brushKindSweep
	brushKindPattern
)

// Sentinel errors returned (wrapped in *DecodeError) by Decode.
var (
	// ErrInvalidMagic indicates the stream does not start with the format magic.
	ErrInvalidMagic = errors.New("recording: invalid magic")
	// ErrUnsupportedVersion indicates a format version this build cannot read.
	ErrUnsupportedVersion = errors.New("recording: unsupported format version")
	// ErrTruncated indicates the stream ended before a complete value was read.
	ErrTruncated = errors.New("recording: truncated input")
	// ErrDuplicateChunk indicates a resource table chunk appeared twice.
	ErrDuplicateChunk = errors.New("recording: duplicate resource table")
	// ErrUnknownChunk indicates an unrecognized chunk tag.
	ErrUnknownChunk = errors.New("recording: unknown chunk")
	// ErrDanglingRef indicates a command or brush references a resource
	// index that does not exist in the corresponding table.
	ErrDanglingRef = errors.New("recording: dangling resource reference")
	// ErrLengthOverflow indicates a length field overflows or is inconsistent
	// with the data it describes (e.g. a count larger than the remaining input).
	ErrLengthOverflow = errors.New("recording: length overflow")
	// ErrLimitExceeded indicates a value exceeds a decoder safety limit.
	ErrLimitExceeded = errors.New("recording: decoder limit exceeded")
	// ErrMalformed indicates structurally invalid data (bad enum value,
	// mismatched counts, trailing garbage, missing command stream, ...).
	ErrMalformed = errors.New("recording: malformed data")
)

// DecodeError describes a decode failure with the byte offset at which it
// was detected and, when decoding the command stream, the command index.
type DecodeError struct {
	// Op names what was being decoded (e.g. "header", "path table", "command").
	Op string
	// Offset is the absolute byte offset in the input stream.
	Offset int64
	// Command is the index of the command being decoded, or -1 outside the
	// command stream.
	Command int
	// Err is the underlying cause (one of the Err* sentinels above).
	Err error
}

// Error implements error.
func (e *DecodeError) Error() string {
	if e.Command >= 0 {
		return fmt.Sprintf("%s at offset %d (command %d): %v", e.Op, e.Offset, e.Command, e.Err)
	}
	return fmt.Sprintf("%s at offset %d: %v", e.Op, e.Offset, e.Err)
}

// Unwrap returns the underlying error.
func (e *DecodeError) Unwrap() error { return e.Err }

// --------------------------------------------------------------------------
// Encoding
// --------------------------------------------------------------------------

// MarshalBinary encodes the recording in the versioned binary format.
// The output is deterministic: the same Recording always encodes to the
// same bytes.
func (r *Recording) MarshalBinary() ([]byte, error) {
	var buf bytes.Buffer
	buf.Grow(4096)

	// Header.
	buf.WriteString(formatMagic)
	writeU16(&buf, FormatVersion)
	writeU16(&buf, 0) // flags
	writeI32(&buf, int32(r.width))  // #nosec G115 -- canvas dims fit int32
	writeI32(&buf, int32(r.height)) // #nosec G115 -- canvas dims fit int32

	// Resource tables, then commands.
	writeChunk(&buf, chunkPaths, encodePathTable(r.resources))
	writeChunk(&buf, chunkImages, encodeImageTable(r.resources))
	writeChunk(&buf, chunkBrushes, encodeBrushTable(r.resources))

	fonts, fontIndex := collectFonts(r.commands)
	writeChunk(&buf, chunkFonts, encodeFontTable(fonts))

	cmds, err := encodeCommands(r.commands, fontIndex)
	if err != nil {
		return nil, err
	}
	writeChunk(&buf, chunkCommands, cmds)

	return buf.Bytes(), nil
}

// WriteTo writes the binary encoding of the recording to w.
// It implements io.WriterTo.
func (r *Recording) WriteTo(w io.Writer) (int64, error) {
	data, err := r.MarshalBinary()
	if err != nil {
		return 0, err
	}
	n, err := w.Write(data)
	return int64(n), err
}

// writeChunk writes a chunk tag, payload length, and payload.
func writeChunk(buf *bytes.Buffer, tag string, payload []byte) {
	buf.WriteString(tag)
	writeU32(buf, uint32(len(payload))) // #nosec G115 -- payloads are far below 4 GiB
	buf.Write(payload)
}

func writeU16(buf *bytes.Buffer, v uint16) {
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], v)
	buf.Write(b[:])
}

func writeU32(buf *bytes.Buffer, v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	buf.Write(b[:])
}

func writeI32(buf *bytes.Buffer, v int32) {
	writeU32(buf, uint32(v))
}

func writeF64(buf *bytes.Buffer, v float64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], math.Float64bits(v))
	buf.Write(b[:])
}

func writeString(buf *bytes.Buffer, s string) {
	writeU32(buf, uint32(len(s)))
	buf.WriteString(s)
}

func writeRect(buf *bytes.Buffer, r Rect) {
	writeF64(buf, r.MinX)
	writeF64(buf, r.MinY)
	writeF64(buf, r.MaxX)
	writeF64(buf, r.MaxY)
}

func writeMatrix(buf *bytes.Buffer, m Matrix) {
	writeF64(buf, m.A)
	writeF64(buf, m.B)
	writeF64(buf, m.C)
	writeF64(buf, m.D)
	writeF64(buf, m.E)
	writeF64(buf, m.F)
}

func writeStroke(buf *bytes.Buffer, s Stroke) {
	writeF64(buf, s.Width)
	buf.WriteByte(byte(s.Cap))
	buf.WriteByte(byte(s.Join))
	writeF64(buf, s.MiterLimit)
	writeU32(buf, uint32(len(s.DashPattern)))
	for _, v := range s.DashPattern {
		writeF64(buf, v)
	}
	writeF64(buf, s.DashOffset)
}

func writeRGBA(buf *bytes.Buffer, c gg.RGBA) {
	writeF64(buf, c.R)
	writeF64(buf, c.G)
	writeF64(buf, c.B)
	writeF64(buf, c.A)
}

// encodePathTable encodes the path table payload.
func encodePathTable(pool *ResourcePool) []byte {
	var buf bytes.Buffer
	writeU32(&buf, uint32(pool.PathCount())) // #nosec G115 -- pool size bounded by memory
	for i := 0; i < pool.PathCount(); i++ {
		p := pool.paths[i]
		if p == nil {
			writeU32(&buf, nilPathSentinel)
			continue
		}
		verbs := p.Verbs()
		coords := p.Coords()
		writeU32(&buf, uint32(len(verbs)))
		writeU32(&buf, uint32(len(coords)))
		for _, v := range verbs {
			buf.WriteByte(byte(v))
		}
		for _, c := range coords {
			writeF64(&buf, c)
		}
	}
	return buf.Bytes()
}

// encodeImageTable encodes images as raw NRGBA pixels with their bounds.
// Raw pixels (not PNG) keep the encoding self-contained and exactly
// reproducible regardless of image type.
func encodeImageTable(pool *ResourcePool) []byte {
	var buf bytes.Buffer
	writeU32(&buf, uint32(pool.ImageCount())) // #nosec G115 -- pool size bounded by memory
	for i := 0; i < pool.ImageCount(); i++ {
		img := pool.images[i]
		if img == nil {
			// Nil image slot: zero bounds and zero-length payload.
			writeI32(&buf, 0)
			writeI32(&buf, 0)
			writeI32(&buf, 0)
			writeI32(&buf, 0)
			writeU32(&buf, 0)
			continue
		}
		b := img.Bounds()
		w, h := b.Dx(), b.Dy()
		nrgba := image.NewNRGBA(image.Rect(0, 0, w, h))
		draw.Draw(nrgba, nrgba.Bounds(), img, b.Min, draw.Src)
		writeI32(&buf, int32(b.Min.X)) // #nosec G115 -- image dims fit int32
		writeI32(&buf, int32(b.Min.Y)) // #nosec G115
		writeI32(&buf, int32(w))       // #nosec G115
		writeI32(&buf, int32(h))       // #nosec G115
		writeU32(&buf, uint32(len(nrgba.Pix)))
		buf.Write(nrgba.Pix)
	}
	return buf.Bytes()
}

// encodeBrushTable encodes the brush table payload.
func encodeBrushTable(pool *ResourcePool) []byte {
	var buf bytes.Buffer
	writeU32(&buf, uint32(pool.BrushCount())) // #nosec G115 -- pool size bounded by memory
	for i := 0; i < pool.BrushCount(); i++ {
		encodeBrush(&buf, pool.brushes[i])
	}
	return buf.Bytes()
}

// encodeBrush writes a single brush. Nil and unknown brush types encode as
// transparent solid black, matching the nil-brush no-op behavior of backends.
func encodeBrush(buf *bytes.Buffer, brush Brush) {
	switch b := brush.(type) {
	case SolidBrush:
		buf.WriteByte(brushKindSolid)
		writeRGBA(buf, b.Color)
	case *SolidBrush:
		buf.WriteByte(brushKindSolid)
		writeRGBA(buf, b.Color)
	case LinearGradientBrush:
		encodeLinearGradient(buf, &b)
	case *LinearGradientBrush:
		encodeLinearGradient(buf, b)
	case RadialGradientBrush:
		encodeRadialGradient(buf, &b)
	case *RadialGradientBrush:
		encodeRadialGradient(buf, b)
	case SweepGradientBrush:
		encodeSweepGradient(buf, &b)
	case *SweepGradientBrush:
		encodeSweepGradient(buf, b)
	case PatternBrush:
		encodePattern(buf, &b)
	case *PatternBrush:
		encodePattern(buf, b)
	default:
		buf.WriteByte(brushKindSolid)
		writeRGBA(buf, gg.RGBA{})
	}
}

func encodeGradientStops(buf *bytes.Buffer, stops []GradientStop) {
	writeU32(buf, uint32(len(stops)))
	for _, s := range stops {
		writeF64(buf, s.Offset)
		writeRGBA(buf, s.Color)
	}
}

func encodeLinearGradient(buf *bytes.Buffer, g *LinearGradientBrush) {
	buf.WriteByte(brushKindLinear)
	writeF64(buf, g.Start.X)
	writeF64(buf, g.Start.Y)
	writeF64(buf, g.End.X)
	writeF64(buf, g.End.Y)
	writeI32(buf, int32(g.Extend))
	encodeGradientStops(buf, g.Stops)
}

func encodeRadialGradient(buf *bytes.Buffer, g *RadialGradientBrush) {
	buf.WriteByte(brushKindRadial)
	writeF64(buf, g.Center.X)
	writeF64(buf, g.Center.Y)
	writeF64(buf, g.Focus.X)
	writeF64(buf, g.Focus.Y)
	writeF64(buf, g.StartRadius)
	writeF64(buf, g.EndRadius)
	writeI32(buf, int32(g.Extend))
	encodeGradientStops(buf, g.Stops)
}

func encodeSweepGradient(buf *bytes.Buffer, g *SweepGradientBrush) {
	buf.WriteByte(brushKindSweep)
	writeF64(buf, g.Center.X)
	writeF64(buf, g.Center.Y)
	writeF64(buf, g.StartAngle)
	writeF64(buf, g.EndAngle)
	writeI32(buf, int32(g.Extend))
	encodeGradientStops(buf, g.Stops)
}

func encodePattern(buf *bytes.Buffer, p *PatternBrush) {
	buf.WriteByte(brushKindPattern)
	writeU32(buf, uint32(p.Image))
	writeI32(buf, int32(p.Repeat))
	writeF64(buf, p.Transform.A)
	writeF64(buf, p.Transform.B)
	writeF64(buf, p.Transform.C)
	writeF64(buf, p.Transform.D)
	writeF64(buf, p.Transform.E)
	writeF64(buf, p.Transform.F)
}

// fontEntry is one row of the font table built at encode time.
type fontEntry struct {
	name string
	size float64
	data []byte // raw font file bytes; nil if unavailable
}

// collectFonts gathers the distinct faces referenced by text commands, in
// order of first appearance, so the font table is deterministic.
func collectFonts(cmds []Command) ([]fontEntry, map[text.Face]uint32) {
	index := make(map[text.Face]uint32)
	var fonts []fontEntry
	add := func(f text.Face) {
		if f == nil {
			return
		}
		if _, ok := index[f]; ok {
			return
		}
		e := fontEntry{size: f.Size()}
		if src := f.Source(); src != nil {
			e.name = src.Name()
			e.data = src.Data()
		}
		index[f] = uint32(len(fonts)) // #nosec G115 -- font count bounded by command count
		fonts = append(fonts, e)
	}
	for _, c := range cmds {
		switch c := c.(type) {
		case DrawTextCommand:
			add(c.Face)
		case StrokeTextCommand:
			add(c.Face)
		}
	}
	return fonts, index
}

// encodeFontTable encodes the font table payload. Fonts with available
// source data are embedded so the decoded recording renders identically;
// fonts without data keep only name+size for a Decode font resolver.
func encodeFontTable(fonts []fontEntry) []byte {
	var buf bytes.Buffer
	writeU32(&buf, uint32(len(fonts)))
	for _, f := range fonts {
		writeString(&buf, f.name)
		writeF64(&buf, f.size)
		writeU32(&buf, uint32(len(f.data)))
		buf.Write(f.data)
	}
	return buf.Bytes()
}

// encodeCommands encodes the command stream payload.
func encodeCommands(cmds []Command, fontIndex map[text.Face]uint32) ([]byte, error) {
	var buf bytes.Buffer
	writeU32(&buf, uint32(len(cmds))) // #nosec G115 -- command count bounded by memory
	for i, cmd := range cmds {
		if err := encodeCommand(&buf, cmd, fontIndex); err != nil {
			return nil, fmt.Errorf("recording: encode command %d: %w", i, err)
		}
	}
	return buf.Bytes(), nil
}

func encodeCommand(buf *bytes.Buffer, cmd Command, fontIndex map[text.Face]uint32) error {
	buf.WriteByte(byte(cmd.Type()))
	if encodeStatePayload(buf, cmd) {
		return nil
	}
	if encodeDrawPayload(buf, cmd, fontIndex) {
		return nil
	}
	if encodeStylePayload(buf, cmd) {
		return nil
	}
	return fmt.Errorf("unsupported command type %T", cmd)
}

// encodeStatePayload encodes state and clip commands. It reports whether cmd
// was handled.
func encodeStatePayload(buf *bytes.Buffer, cmd Command) bool {
	switch c := cmd.(type) {
	case SaveCommand, RestoreCommand, ClearClipCommand:
		// No payload.
	case SetTransformCommand:
		writeMatrix(buf, c.Matrix)
	case SetClipCommand:
		writeU32(buf, uint32(c.Path))
		buf.WriteByte(byte(c.Rule))
	case ClipRoundRectCommand:
		writeF64(buf, c.X)
		writeF64(buf, c.Y)
		writeF64(buf, c.W)
		writeF64(buf, c.H)
		writeF64(buf, c.Radius)
	default:
		return false
	}
	return true
}

// encodeDrawPayload encodes drawing commands. It reports whether cmd was
// handled.
func encodeDrawPayload(buf *bytes.Buffer, cmd Command, fontIndex map[text.Face]uint32) bool {
	fontRef := func(f text.Face) uint32 {
		if f == nil {
			return InvalidRef
		}
		return fontIndex[f]
	}

	switch c := cmd.(type) {
	case FillPathCommand:
		writeU32(buf, uint32(c.Path))
		writeU32(buf, uint32(c.Brush))
		buf.WriteByte(byte(c.Rule))
	case StrokePathCommand:
		writeU32(buf, uint32(c.Path))
		writeU32(buf, uint32(c.Brush))
		writeStroke(buf, c.Stroke)
	case FillRectCommand:
		writeRect(buf, c.Rect)
		writeU32(buf, uint32(c.Brush))
	case StrokeRectCommand:
		writeRect(buf, c.Rect)
		writeU32(buf, uint32(c.Brush))
		writeStroke(buf, c.Stroke)
	case DrawImageCommand:
		writeU32(buf, uint32(c.Image))
		writeRect(buf, c.SrcRect)
		writeRect(buf, c.DstRect)
		buf.WriteByte(byte(c.Options.Interpolation))
		writeF64(buf, c.Options.Alpha)
	case DrawTextCommand:
		writeU32(buf, fontRef(c.Face))
		writeString(buf, c.Text)
		writeF64(buf, c.X)
		writeF64(buf, c.Y)
		writeF64(buf, c.FontSize)
		writeString(buf, c.FontFamily)
		writeU32(buf, uint32(c.Brush))
	case StrokeTextCommand:
		writeU32(buf, fontRef(c.Face))
		writeString(buf, c.Text)
		writeF64(buf, c.X)
		writeF64(buf, c.Y)
		writeF64(buf, c.FontSize)
		writeString(buf, c.FontFamily)
		writeU32(buf, uint32(c.Brush))
		writeStroke(buf, c.Stroke)
	default:
		return false
	}
	return true
}

// encodeStylePayload encodes style commands. It reports whether cmd was
// handled.
func encodeStylePayload(buf *bytes.Buffer, cmd Command) bool {
	switch c := cmd.(type) {
	case SetFillStyleCommand:
		writeU32(buf, uint32(c.Brush))
	case SetStrokeStyleCommand:
		writeU32(buf, uint32(c.Brush))
	case SetLineWidthCommand:
		writeF64(buf, c.Width)
	case SetLineCapCommand:
		buf.WriteByte(byte(c.Cap))
	case SetLineJoinCommand:
		buf.WriteByte(byte(c.Join))
	case SetMiterLimitCommand:
		writeF64(buf, c.Limit)
	case SetDashCommand:
		writeU32(buf, uint32(len(c.Pattern)))
		for _, v := range c.Pattern {
			writeF64(buf, v)
		}
		writeF64(buf, c.Offset)
	case SetFillRuleCommand:
		buf.WriteByte(byte(c.Rule))
	case SetAntiAliasCommand:
		if c.Enabled {
			buf.WriteByte(1)
		} else {
			buf.WriteByte(0)
		}
	default:
		return false
	}
	return true
}

// --------------------------------------------------------------------------
// Decoding
// --------------------------------------------------------------------------

// decoder is a cursor over a byte slice that produces offset-carrying errors.
// base is the absolute stream offset of data[0] (nonzero for chunk payloads).
type decoder struct {
	data []byte
	off  int
	base int64
	cmd  int // current command index, -1 outside the command stream
}

// DecodeOption configures Decode.
type DecodeOption func(*decodeConfig)

type decodeConfig struct {
	resolveFont func(name string, size float64) text.Face
}

// WithFontResolver sets a resolver used for font table entries that carry no
// embedded font data (e.g. faces whose source data was unavailable at encode
// time). The resolver receives the recorded font name and size and returns a
// face, or nil to leave the command's face unset.
func WithFontResolver(resolve func(name string, size float64) text.Face) DecodeOption {
	return func(c *decodeConfig) { c.resolveFont = resolve }
}

// Decode reads a recording previously written with MarshalBinary or WriteTo.
//
// The returned Recording replays exactly like the original: canvas size,
// command order, paths, brushes, images, fonts, and transforms are preserved,
// and resource indices are identical to those in the encoded stream.
//
// Corrupt input never panics: truncation, unknown versions, inconsistent
// lengths, dangling resource references, duplicate resource tables, and
// over-limit allocations all return a *DecodeError carrying the byte offset
// and, where applicable, the command index.
func Decode(r io.Reader, opts ...DecodeOption) (*Recording, error) {
	var cfg decodeConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	data, err := readAllLimited(r, maxRecordingSize)
	if err != nil {
		return nil, err
	}

	d := &decoder{data: data, cmd: -1}
	width, height, err := d.decodeHeader()
	if err != nil {
		return nil, err
	}

	rec := &Recording{
		width:     width,
		height:    height,
		resources: NewResourcePool(),
	}
	if err := d.decodeChunks(rec, &cfg); err != nil {
		return nil, err
	}
	return rec, nil
}

// decodeHeader reads and validates the 16-byte file header.
func (d *decoder) decodeHeader() (width, height int, err error) {
	const op = "header"
	magic, err := d.take(4, op)
	if err != nil {
		return 0, 0, err
	}
	if string(magic) != formatMagic {
		return 0, 0, d.failAt(op, 0, ErrInvalidMagic)
	}
	version, err := d.u16(op)
	if err != nil {
		return 0, 0, err
	}
	if version != FormatVersion {
		return 0, 0, d.fail(op, fmt.Errorf("%w: %d", ErrUnsupportedVersion, version))
	}
	flags, err := d.u16(op)
	if err != nil {
		return 0, 0, err
	}
	if flags != 0 {
		return 0, 0, d.fail(op, fmt.Errorf("%w: nonzero flags %d", ErrMalformed, flags))
	}
	w, err := d.i32(op)
	if err != nil {
		return 0, 0, err
	}
	h, err := d.i32(op)
	if err != nil {
		return 0, 0, err
	}
	if w < 0 || h < 0 {
		return 0, 0, d.fail(op, fmt.Errorf("%w: negative canvas size %dx%d", ErrMalformed, w, h))
	}
	return int(w), int(h), nil
}

// decodeChunks reads the chunk sequence after the header.
func (d *decoder) decodeChunks(rec *Recording, cfg *decodeConfig) error {
	var fonts []text.Face
	seen := make(map[string]bool)
	lastOrder := -1
	gotCommands := false

	for d.remaining() > 0 {
		chunkStart := d.off
		tag, order, err := d.chunkTag(seen, lastOrder, chunkStart)
		if err != nil {
			return err
		}
		seen[tag] = true
		lastOrder = order

		length, err := d.u32("chunk header")
		if err != nil {
			return err
		}
		payload, err := d.take(int(length), "chunk "+tag)
		if err != nil {
			return err
		}
		sub := &decoder{data: payload, base: int64(chunkStart) + 8, cmd: -1}

		switch tag {
		case chunkPaths:
			err = sub.decodePathTable(rec.resources)
		case chunkImages:
			err = sub.decodeImageTable(rec.resources)
		case chunkBrushes:
			err = sub.decodeBrushTable(rec.resources)
		case chunkFonts:
			fonts, err = sub.decodeFontTable(cfg)
		case chunkCommands:
			rec.commands, err = sub.decodeCommandStream(rec.resources, fonts)
			gotCommands = err == nil
		}
		if err != nil {
			return err
		}
		if sub.remaining() != 0 {
			return sub.fail("chunk "+tag, fmt.Errorf("%w: %d trailing bytes in chunk", ErrMalformed, sub.remaining()))
		}
	}
	if !gotCommands {
		return d.fail("stream", fmt.Errorf("%w: missing commands chunk", ErrMalformed))
	}
	return nil
}

// chunkTag reads a chunk tag and validates it against the required order.
func (d *decoder) chunkTag(seen map[string]bool, lastOrder, chunkStart int) (string, int, error) {
	tagBytes, err := d.take(4, "chunk header")
	if err != nil {
		return "", 0, err
	}
	tag := string(tagBytes)
	order, known := chunkOrder[tag]
	if !known {
		return "", 0, d.failAt("chunk header", chunkStart, fmt.Errorf("%w: %q", ErrUnknownChunk, tag))
	}
	if seen[tag] {
		return "", 0, d.failAt("chunk header", chunkStart, fmt.Errorf("%w: %q", ErrDuplicateChunk, tag))
	}
	if order <= lastOrder {
		return "", 0, d.failAt("chunk header", chunkStart,
			fmt.Errorf("%w: chunk %q out of order", ErrMalformed, tag))
	}
	return tag, order, nil
}

// readAllLimited reads r fully, failing if it exceeds limit bytes.
func readAllLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, &DecodeError{Op: "read", Offset: limit, Command: -1, Err: ErrLimitExceeded}
	}
	return data, nil
}

func (d *decoder) remaining() int { return len(d.data) - d.off }

// fail builds a DecodeError at the current offset.
func (d *decoder) fail(op string, err error) *DecodeError {
	return &DecodeError{Op: op, Offset: d.base + int64(d.off), Command: d.cmd, Err: err}
}

// failAt builds a DecodeError at an absolute (decoder-relative) offset.
func (d *decoder) failAt(op string, off int, err error) *DecodeError {
	return &DecodeError{Op: op, Offset: d.base + int64(off), Command: d.cmd, Err: err}
}

func (d *decoder) take(n int, op string) ([]byte, error) {
	if n < 0 || d.remaining() < n {
		return nil, d.fail(op, fmt.Errorf("%w: need %d bytes, have %d", ErrTruncated, n, d.remaining()))
	}
	b := d.data[d.off : d.off+n]
	d.off += n
	return b, nil
}

func (d *decoder) u8(op string) (uint8, error) {
	b, err := d.take(1, op)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func (d *decoder) u16(op string) (uint16, error) {
	b, err := d.take(2, op)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(b), nil
}

func (d *decoder) u32(op string) (uint32, error) {
	b, err := d.take(4, op)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b), nil
}

func (d *decoder) i32(op string) (int32, error) {
	v, err := d.u32(op)
	return int32(v), err
}

func (d *decoder) f64(op string) (float64, error) {
	b, err := d.take(8, op)
	if err != nil {
		return 0, err
	}
	return math.Float64frombits(binary.LittleEndian.Uint64(b)), nil
}

func (d *decoder) str(op string) (string, error) {
	n, err := d.u32(op)
	if err != nil {
		return "", err
	}
	if n > maxStringLen {
		return "", d.fail(op, fmt.Errorf("%w: string length %d", ErrLimitExceeded, n))
	}
	b, err := d.take(int(n), op)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// count reads a table entry count and verifies it against limit and the
// remaining input (each entry needs at least minEntrySize bytes), so a
// corrupt count fails before any large allocation.
func (d *decoder) count(op string, limit, minEntrySize int) (int, error) {
	n, err := d.u32(op)
	if err != nil {
		return 0, err
	}
	if uint64(n) > uint64(limit) {
		return 0, d.fail(op, fmt.Errorf("%w: count %d exceeds limit %d", ErrLimitExceeded, n, limit))
	}
	if minEntrySize > 0 && uint64(n) > uint64(d.remaining())/uint64(minEntrySize) {
		return 0, d.fail(op, fmt.Errorf("%w: count %d exceeds remaining input %d",
			ErrLengthOverflow, n, d.remaining()))
	}
	return int(n), nil
}

func (d *decoder) rect() (Rect, error) {
	var r Rect
	var err error
	if r.MinX, err = d.f64("command"); err != nil {
		return r, err
	}
	if r.MinY, err = d.f64("command"); err != nil {
		return r, err
	}
	if r.MaxX, err = d.f64("command"); err != nil {
		return r, err
	}
	if r.MaxY, err = d.f64("command"); err != nil {
		return r, err
	}
	return r, nil
}

func (d *decoder) matrix(op string) (Matrix, error) {
	var m Matrix
	var err error
	if m.A, err = d.f64(op); err != nil {
		return m, err
	}
	if m.B, err = d.f64(op); err != nil {
		return m, err
	}
	if m.C, err = d.f64(op); err != nil {
		return m, err
	}
	if m.D, err = d.f64(op); err != nil {
		return m, err
	}
	if m.E, err = d.f64(op); err != nil {
		return m, err
	}
	if m.F, err = d.f64(op); err != nil {
		return m, err
	}
	return m, nil
}

func (d *decoder) stroke(op string) (Stroke, error) {
	var s Stroke
	var err error
	if s.Width, err = d.f64(op); err != nil {
		return s, err
	}
	capByte, err := d.u8(op)
	if err != nil {
		return s, err
	}
	if capByte > byte(LineCapSquare) {
		return s, d.fail(op, fmt.Errorf("%w: line cap %d", ErrMalformed, capByte))
	}
	s.Cap = LineCap(capByte)
	joinByte, err := d.u8(op)
	if err != nil {
		return s, err
	}
	if joinByte > byte(LineJoinBevel) {
		return s, d.fail(op, fmt.Errorf("%w: line join %d", ErrMalformed, joinByte))
	}
	s.Join = LineJoin(joinByte)
	if s.MiterLimit, err = d.f64(op); err != nil {
		return s, err
	}
	n, err := d.count(op, maxDashLen, 8)
	if err != nil {
		return s, err
	}
	if n > 0 {
		s.DashPattern = make([]float64, n)
		for i := range s.DashPattern {
			if s.DashPattern[i], err = d.f64(op); err != nil {
				return s, err
			}
		}
	}
	if s.DashOffset, err = d.f64(op); err != nil {
		return s, err
	}
	return s, nil
}

func (d *decoder) rgba(op string) (gg.RGBA, error) {
	var c gg.RGBA
	var err error
	if c.R, err = d.f64(op); err != nil {
		return c, err
	}
	if c.G, err = d.f64(op); err != nil {
		return c, err
	}
	if c.B, err = d.f64(op); err != nil {
		return c, err
	}
	if c.A, err = d.f64(op); err != nil {
		return c, err
	}
	return c, nil
}

func (d *decoder) fillRule(op string) (FillRule, error) {
	b, err := d.u8(op)
	if err != nil {
		return 0, err
	}
	if b > byte(FillRuleEvenOdd) {
		return 0, d.fail(op, fmt.Errorf("%w: fill rule %d", ErrMalformed, b))
	}
	return FillRule(b), nil
}

// pathVerbCoords mirrors gg's per-verb coordinate counts for validation.
func pathVerbCoords(v byte) (int, bool) {
	switch gg.PathVerb(v) {
	case gg.MoveTo, gg.LineTo:
		return 2, true
	case gg.QuadTo:
		return 4, true
	case gg.CubicTo:
		return 6, true
	case gg.Close:
		return 0, true
	}
	return 0, false
}

// decodePathTable decodes the PATH chunk payload into the pool.
func (d *decoder) decodePathTable(pool *ResourcePool) error {
	const op = "path table"
	n, err := d.count(op, maxResourceCount, 4)
	if err != nil {
		return err
	}
	pool.paths = make([]*gg.Path, 0, n)
	for i := 0; i < n; i++ {
		verbCount, err := d.u32(op)
		if err != nil {
			return err
		}
		if verbCount == nilPathSentinel {
			pool.paths = append(pool.paths, nil)
			continue
		}
		coordCount, err := d.u32(op)
		if err != nil {
			return err
		}
		if verbCount > maxPathVerbs || coordCount > maxPathCoords {
			return d.fail(op, fmt.Errorf("%w: verbs=%d coords=%d", ErrLimitExceeded, verbCount, coordCount))
		}
		// Each verb is 1 byte, each coord 8; reject before allocating.
		if uint64(verbCount)+8*uint64(coordCount) > uint64(d.remaining()) {
			return d.fail(op, fmt.Errorf("%w: path %d declares %d verbs + %d coords with %d bytes left",
				ErrLengthOverflow, i, verbCount, coordCount, d.remaining()))
		}
		verbs, err := d.take(int(verbCount), op)
		if err != nil {
			return err
		}
		coords := make([]float64, coordCount)
		for j := range coords {
			if coords[j], err = d.f64(op); err != nil {
				return err
			}
		}
		// Validate verbs and that they consume exactly coordCount coords.
		need := 0
		for _, v := range verbs {
			c, ok := pathVerbCoords(v)
			if !ok {
				return d.fail(op, fmt.Errorf("%w: path %d has invalid verb %d", ErrMalformed, i, v))
			}
			need += c
		}
		if need != int(coordCount) {
			return d.fail(op, fmt.Errorf("%w: path %d verbs consume %d coords, declared %d",
				ErrLengthOverflow, i, need, coordCount))
		}
		pool.paths = append(pool.paths, buildPath(verbs, coords))
	}
	return nil
}

// buildPath reconstructs a gg.Path from validated verbs and coords.
func buildPath(verbs []byte, coords []float64) *gg.Path {
	p := gg.NewPath()
	ci := 0
	for _, v := range verbs {
		switch gg.PathVerb(v) {
		case gg.MoveTo:
			p.MoveTo(coords[ci], coords[ci+1])
			ci += 2
		case gg.LineTo:
			p.LineTo(coords[ci], coords[ci+1])
			ci += 2
		case gg.QuadTo:
			p.QuadraticTo(coords[ci], coords[ci+1], coords[ci+2], coords[ci+3])
			ci += 4
		case gg.CubicTo:
			p.CubicTo(coords[ci], coords[ci+1], coords[ci+2], coords[ci+3], coords[ci+4], coords[ci+5])
			ci += 6
		case gg.Close:
			p.Close()
		}
	}
	return p
}

// decodeImageTable decodes the IMAG chunk payload into the pool.
func (d *decoder) decodeImageTable(pool *ResourcePool) error {
	const op = "image table"
	n, err := d.count(op, maxResourceCount, 20)
	if err != nil {
		return err
	}
	pool.images = make([]image.Image, 0, n)
	for i := 0; i < n; i++ {
		minX, err := d.i32(op)
		if err != nil {
			return err
		}
		minY, err := d.i32(op)
		if err != nil {
			return err
		}
		w, err := d.i32(op)
		if err != nil {
			return err
		}
		h, err := d.i32(op)
		if err != nil {
			return err
		}
		dataLen, err := d.u32(op)
		if err != nil {
			return err
		}
		if w == 0 && h == 0 && dataLen == 0 {
			pool.images = append(pool.images, nil)
			continue
		}
		if w <= 0 || h <= 0 || w > maxImageDim || h > maxImageDim {
			return d.fail(op, fmt.Errorf("%w: image %d size %dx%d", ErrLimitExceeded, i, w, h))
		}
		pixels := uint64(w) * uint64(h)
		if pixels > maxImagePixels {
			return d.fail(op, fmt.Errorf("%w: image %d has %d pixels", ErrLimitExceeded, i, pixels))
		}
		if uint64(dataLen) != pixels*4 {
			return d.fail(op, fmt.Errorf("%w: image %d declares %d bytes for %dx%d NRGBA",
				ErrLengthOverflow, i, dataLen, w, h))
		}
		pix, err := d.take(int(dataLen), op)
		if err != nil {
			return err
		}
		img := &image.NRGBA{
			Pix:    append([]byte(nil), pix...),
			Stride: int(w) * 4,
			Rect:   image.Rect(int(minX), int(minY), int(minX)+int(w), int(minY)+int(h)),
		}
		pool.images = append(pool.images, img)
	}
	return nil
}

// decodeBrushTable decodes the BRUS chunk payload into the pool.
// Pattern brushes may reference the image table, which is decoded earlier.
func (d *decoder) decodeBrushTable(pool *ResourcePool) error {
	const op = "brush table"
	n, err := d.count(op, maxResourceCount, 1)
	if err != nil {
		return err
	}
	pool.brushes = make([]Brush, 0, n)
	for i := 0; i < n; i++ {
		brush, err := d.decodeBrush(pool, i)
		if err != nil {
			return err
		}
		pool.brushes = append(pool.brushes, brush)
	}
	return nil
}

// point reads two float64 coordinates as a gg.Point in the brush table.
func (d *decoder) point() (gg.Point, error) {
	var p gg.Point
	var err error
	if p.X, err = d.f64("brush table"); err != nil {
		return p, err
	}
	if p.Y, err = d.f64("brush table"); err != nil {
		return p, err
	}
	return p, nil
}

func (d *decoder) decodeBrush(pool *ResourcePool, index int) (Brush, error) {
	const op = "brush table"
	kind, err := d.u8(op)
	if err != nil {
		return nil, err
	}
	switch kind {
	case brushKindSolid:
		c, err := d.rgba(op)
		if err != nil {
			return nil, err
		}
		return SolidBrush{Color: c}, nil
	case brushKindLinear:
		return d.decodeLinearGradient()
	case brushKindRadial:
		return d.decodeRadialGradient()
	case brushKindSweep:
		return d.decodeSweepGradient()
	case brushKindPattern:
		return d.decodePatternBrush(pool, index)
	default:
		return nil, d.fail(op, fmt.Errorf("%w: brush %d has unknown kind %d", ErrMalformed, index, kind))
	}
}

// gradientTail reads the extend mode and color stops shared by all gradients.
func (d *decoder) gradientTail() (ExtendMode, []GradientStop, error) {
	const op = "brush table"
	extend, err := d.extendMode(op)
	if err != nil {
		return 0, nil, err
	}
	stops, err := d.gradientStops(op)
	if err != nil {
		return 0, nil, err
	}
	return extend, stops, nil
}

func (d *decoder) decodeLinearGradient() (Brush, error) {
	g := &LinearGradientBrush{}
	var err error
	if g.Start, err = d.point(); err != nil {
		return nil, err
	}
	if g.End, err = d.point(); err != nil {
		return nil, err
	}
	if g.Extend, g.Stops, err = d.gradientTail(); err != nil {
		return nil, err
	}
	return g, nil
}

func (d *decoder) decodeRadialGradient() (Brush, error) {
	const op = "brush table"
	g := &RadialGradientBrush{}
	var err error
	if g.Center, err = d.point(); err != nil {
		return nil, err
	}
	if g.Focus, err = d.point(); err != nil {
		return nil, err
	}
	if g.StartRadius, err = d.f64(op); err != nil {
		return nil, err
	}
	if g.EndRadius, err = d.f64(op); err != nil {
		return nil, err
	}
	if g.Extend, g.Stops, err = d.gradientTail(); err != nil {
		return nil, err
	}
	return g, nil
}

func (d *decoder) decodeSweepGradient() (Brush, error) {
	const op = "brush table"
	g := &SweepGradientBrush{}
	var err error
	if g.Center, err = d.point(); err != nil {
		return nil, err
	}
	if g.StartAngle, err = d.f64(op); err != nil {
		return nil, err
	}
	if g.EndAngle, err = d.f64(op); err != nil {
		return nil, err
	}
	if g.Extend, g.Stops, err = d.gradientTail(); err != nil {
		return nil, err
	}
	return g, nil
}

func (d *decoder) decodePatternBrush(pool *ResourcePool, index int) (Brush, error) {
	const op = "brush table"
	imgRef, err := d.u32(op)
	if err != nil {
		return nil, err
	}
	if int(imgRef) >= len(pool.images) {
		return nil, d.fail(op, fmt.Errorf("%w: brush %d references image %d of %d",
			ErrDanglingRef, index, imgRef, len(pool.images)))
	}
	repeat, err := d.i32(op)
	if err != nil {
		return nil, err
	}
	if repeat < int32(RepeatBoth) || repeat > int32(RepeatNone) {
		return nil, d.fail(op, fmt.Errorf("%w: repeat mode %d", ErrMalformed, repeat))
	}
	p := &PatternBrush{Image: ImageRef(imgRef), Repeat: RepeatMode(repeat)}
	if p.Transform.A, err = d.f64(op); err != nil {
		return nil, err
	}
	if p.Transform.B, err = d.f64(op); err != nil {
		return nil, err
	}
	if p.Transform.C, err = d.f64(op); err != nil {
		return nil, err
	}
	if p.Transform.D, err = d.f64(op); err != nil {
		return nil, err
	}
	if p.Transform.E, err = d.f64(op); err != nil {
		return nil, err
	}
	if p.Transform.F, err = d.f64(op); err != nil {
		return nil, err
	}
	return p, nil
}

func (d *decoder) extendMode(op string) (ExtendMode, error) {
	v, err := d.i32(op)
	if err != nil {
		return 0, err
	}
	if v < int32(ExtendPad) || v > int32(ExtendReflect) {
		return 0, d.fail(op, fmt.Errorf("%w: extend mode %d", ErrMalformed, v))
	}
	return ExtendMode(v), nil
}

func (d *decoder) gradientStops(op string) ([]GradientStop, error) {
	n, err := d.count(op, maxGradientStops, 40)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	stops := make([]GradientStop, n)
	for i := range stops {
		if stops[i].Offset, err = d.f64(op); err != nil {
			return nil, err
		}
		if stops[i].Color, err = d.rgba(op); err != nil {
			return nil, err
		}
	}
	return stops, nil
}

// decodeFontTable decodes the FONT chunk payload. Entries with embedded font
// data are reconstructed into real faces; entries without data go through the
// configured font resolver (or yield nil faces).
func (d *decoder) decodeFontTable(cfg *decodeConfig) ([]text.Face, error) {
	const op = "font table"
	n, err := d.count(op, maxResourceCount, 12)
	if err != nil {
		return nil, err
	}
	faces := make([]text.Face, 0, n)
	for i := 0; i < n; i++ {
		name, err := d.str(op)
		if err != nil {
			return nil, err
		}
		size, err := d.f64(op)
		if err != nil {
			return nil, err
		}
		dataLen, err := d.u32(op)
		if err != nil {
			return nil, err
		}
		if dataLen > maxFontDataSize {
			return nil, d.fail(op, fmt.Errorf("%w: font %d data %d bytes", ErrLimitExceeded, i, dataLen))
		}
		data, err := d.take(int(dataLen), op)
		if err != nil {
			return nil, err
		}
		var face text.Face
		if len(data) > 0 {
			src, err := text.NewFontSource(data)
			if err != nil {
				return nil, d.fail(op, fmt.Errorf("%w: font %d (%q): %w", ErrMalformed, i, name, err))
			}
			face = src.Face(size)
		} else if cfg.resolveFont != nil {
			face = cfg.resolveFont(name, size)
		}
		faces = append(faces, face)
	}
	return faces, nil
}

// decodeCommandStream decodes the CMDS chunk payload.
func (d *decoder) decodeCommandStream(pool *ResourcePool, fonts []text.Face) ([]Command, error) {
	n, err := d.count("command stream", maxCommandCount, 1)
	if err != nil {
		return nil, err
	}
	cmds := make([]Command, 0, n)
	for i := 0; i < n; i++ {
		d.cmd = i
		cmd, err := d.decodeCommand(pool, fonts)
		if err != nil {
			return nil, err
		}
		cmds = append(cmds, cmd)
	}
	d.cmd = -1
	return cmds, nil
}

// pathRef validates a path reference against the pool.
func (d *decoder) pathRef(pool *ResourcePool) (PathRef, error) {
	v, err := d.u32("command")
	if err != nil {
		return 0, err
	}
	if int(v) >= len(pool.paths) {
		return 0, d.fail("command", fmt.Errorf("%w: path %d of %d", ErrDanglingRef, v, len(pool.paths)))
	}
	return PathRef(v), nil
}

// brushRef validates a brush reference against the pool.
func (d *decoder) brushRef(pool *ResourcePool) (BrushRef, error) {
	v, err := d.u32("command")
	if err != nil {
		return 0, err
	}
	if int(v) >= len(pool.brushes) {
		return 0, d.fail("command", fmt.Errorf("%w: brush %d of %d", ErrDanglingRef, v, len(pool.brushes)))
	}
	return BrushRef(v), nil
}

// fontFace reads a font reference and resolves it against the font table.
// InvalidRef resolves to a nil face (text commands without a font).
func (d *decoder) fontFace(fonts []text.Face) (text.Face, error) {
	v, err := d.u32("command")
	if err != nil {
		return nil, err
	}
	if v == InvalidRef {
		return nil, nil //nolint:nilnil // nil face is a valid decoded value here
	}
	if int(v) >= len(fonts) {
		return nil, d.fail("command", fmt.Errorf("%w: font %d of %d", ErrDanglingRef, v, len(fonts)))
	}
	return fonts[v], nil
}

func (d *decoder) decodeCommand(pool *ResourcePool, fonts []text.Face) (Command, error) {
	typ, err := d.u8("command")
	if err != nil {
		return nil, err
	}
	t := CommandType(typ)
	if t >= CmdSetFillStyle && t <= CmdSetAntiAlias {
		return d.decodeStyleCommand(pool, t)
	}
	switch t {
	case CmdSave:
		return SaveCommand{}, nil
	case CmdRestore:
		return RestoreCommand{}, nil
	case CmdClearClip:
		return ClearClipCommand{}, nil
	case CmdSetTransform:
		m, err := d.matrix("command")
		if err != nil {
			return nil, err
		}
		return SetTransformCommand{Matrix: m}, nil
	case CmdSetClip:
		return d.decodeSetClip(pool)
	case CmdClipRoundRect:
		return d.decodeClipRoundRect()
	case CmdFillPath:
		return d.decodeFillPath(pool)
	case CmdStrokePath:
		return d.decodeStrokePath(pool)
	case CmdFillRect:
		return d.decodeFillRect(pool)
	case CmdStrokeRect:
		return d.decodeStrokeRect(pool)
	case CmdDrawImage:
		return d.decodeDrawImage(pool)
	case CmdDrawText, CmdStrokeText:
		return d.decodeTextCommand(pool, fonts, t)
	default:
		return nil, d.fail("command", fmt.Errorf("%w: unknown command type %d", ErrMalformed, typ))
	}
}

// decodeStyleCommand decodes style commands (CmdSetFillStyle..CmdSetAntiAlias).
func (d *decoder) decodeStyleCommand(pool *ResourcePool, t CommandType) (Command, error) {
	switch t {
	case CmdSetFillStyle:
		brush, err := d.brushRef(pool)
		if err != nil {
			return nil, err
		}
		return SetFillStyleCommand{Brush: brush}, nil
	case CmdSetStrokeStyle:
		brush, err := d.brushRef(pool)
		if err != nil {
			return nil, err
		}
		return SetStrokeStyleCommand{Brush: brush}, nil
	case CmdSetLineWidth:
		w, err := d.f64("command")
		if err != nil {
			return nil, err
		}
		return SetLineWidthCommand{Width: w}, nil
	case CmdSetLineCap:
		return d.decodeLineCap()
	case CmdSetLineJoin:
		return d.decodeLineJoin()
	case CmdSetMiterLimit:
		l, err := d.f64("command")
		if err != nil {
			return nil, err
		}
		return SetMiterLimitCommand{Limit: l}, nil
	case CmdSetDash:
		return d.decodeSetDash()
	case CmdSetFillRule:
		rule, err := d.fillRule("command")
		if err != nil {
			return nil, err
		}
		return SetFillRuleCommand{Rule: rule}, nil
	case CmdSetAntiAlias:
		return d.decodeSetAntiAlias()
	default:
		return nil, d.fail("command", fmt.Errorf("%w: unknown command type %d", ErrMalformed, t))
	}
}

func (d *decoder) decodeSetClip(pool *ResourcePool) (Command, error) {
	path, err := d.pathRef(pool)
	if err != nil {
		return nil, err
	}
	rule, err := d.fillRule("command")
	if err != nil {
		return nil, err
	}
	return SetClipCommand{Path: path, Rule: rule}, nil
}

func (d *decoder) decodeClipRoundRect() (Command, error) {
	var c ClipRoundRectCommand
	var err error
	if c.X, err = d.f64("command"); err != nil {
		return nil, err
	}
	if c.Y, err = d.f64("command"); err != nil {
		return nil, err
	}
	if c.W, err = d.f64("command"); err != nil {
		return nil, err
	}
	if c.H, err = d.f64("command"); err != nil {
		return nil, err
	}
	if c.Radius, err = d.f64("command"); err != nil {
		return nil, err
	}
	return c, nil
}

func (d *decoder) decodeFillPath(pool *ResourcePool) (Command, error) {
	path, err := d.pathRef(pool)
	if err != nil {
		return nil, err
	}
	brush, err := d.brushRef(pool)
	if err != nil {
		return nil, err
	}
	rule, err := d.fillRule("command")
	if err != nil {
		return nil, err
	}
	return FillPathCommand{Path: path, Brush: brush, Rule: rule}, nil
}

func (d *decoder) decodeStrokePath(pool *ResourcePool) (Command, error) {
	path, err := d.pathRef(pool)
	if err != nil {
		return nil, err
	}
	brush, err := d.brushRef(pool)
	if err != nil {
		return nil, err
	}
	stroke, err := d.stroke("command")
	if err != nil {
		return nil, err
	}
	return StrokePathCommand{Path: path, Brush: brush, Stroke: stroke}, nil
}

func (d *decoder) decodeFillRect(pool *ResourcePool) (Command, error) {
	r, err := d.rect()
	if err != nil {
		return nil, err
	}
	brush, err := d.brushRef(pool)
	if err != nil {
		return nil, err
	}
	return FillRectCommand{Rect: r, Brush: brush}, nil
}

func (d *decoder) decodeStrokeRect(pool *ResourcePool) (Command, error) {
	r, err := d.rect()
	if err != nil {
		return nil, err
	}
	brush, err := d.brushRef(pool)
	if err != nil {
		return nil, err
	}
	stroke, err := d.stroke("command")
	if err != nil {
		return nil, err
	}
	return StrokeRectCommand{Rect: r, Brush: brush, Stroke: stroke}, nil
}

func (d *decoder) decodeDrawImage(pool *ResourcePool) (Command, error) {
	const op = "command"
	imgRef, err := d.u32(op)
	if err != nil {
		return nil, err
	}
	if int(imgRef) >= len(pool.images) {
		return nil, d.fail(op, fmt.Errorf("%w: image %d of %d", ErrDanglingRef, imgRef, len(pool.images)))
	}
	src, err := d.rect()
	if err != nil {
		return nil, err
	}
	dst, err := d.rect()
	if err != nil {
		return nil, err
	}
	interp, err := d.u8(op)
	if err != nil {
		return nil, err
	}
	if interp > byte(InterpolationBilinear) {
		return nil, d.fail(op, fmt.Errorf("%w: interpolation %d", ErrMalformed, interp))
	}
	alpha, err := d.f64(op)
	if err != nil {
		return nil, err
	}
	return DrawImageCommand{
		Image:   ImageRef(imgRef),
		SrcRect: src,
		DstRect: dst,
		Options: ImageOptions{Interpolation: InterpolationMode(interp), Alpha: alpha},
	}, nil
}

func (d *decoder) decodeTextCommand(pool *ResourcePool, fonts []text.Face, typ CommandType) (Command, error) {
	const op = "command"
	face, err := d.fontFace(fonts)
	if err != nil {
		return nil, err
	}
	str, err := d.str(op)
	if err != nil {
		return nil, err
	}
	x, err := d.f64(op)
	if err != nil {
		return nil, err
	}
	y, err := d.f64(op)
	if err != nil {
		return nil, err
	}
	fontSize, err := d.f64(op)
	if err != nil {
		return nil, err
	}
	family, err := d.str(op)
	if err != nil {
		return nil, err
	}
	brush, err := d.brushRef(pool)
	if err != nil {
		return nil, err
	}
	if typ == CmdDrawText {
		return DrawTextCommand{
			Text: str, X: x, Y: y, FontSize: fontSize,
			FontFamily: family, Face: face, Brush: brush,
		}, nil
	}
	stroke, err := d.stroke(op)
	if err != nil {
		return nil, err
	}
	return StrokeTextCommand{
		Text: str, X: x, Y: y, FontSize: fontSize,
		FontFamily: family, Face: face, Brush: brush, Stroke: stroke,
	}, nil
}

func (d *decoder) decodeLineCap() (Command, error) {
	b, err := d.u8("command")
	if err != nil {
		return nil, err
	}
	if b > byte(LineCapSquare) {
		return nil, d.fail("command", fmt.Errorf("%w: line cap %d", ErrMalformed, b))
	}
	return SetLineCapCommand{Cap: LineCap(b)}, nil
}

func (d *decoder) decodeLineJoin() (Command, error) {
	b, err := d.u8("command")
	if err != nil {
		return nil, err
	}
	if b > byte(LineJoinBevel) {
		return nil, d.fail("command", fmt.Errorf("%w: line join %d", ErrMalformed, b))
	}
	return SetLineJoinCommand{Join: LineJoin(b)}, nil
}

func (d *decoder) decodeSetDash() (Command, error) {
	const op = "command"
	n, err := d.count(op, maxDashLen, 8)
	if err != nil {
		return nil, err
	}
	var pattern []float64
	if n > 0 {
		pattern = make([]float64, n)
		for i := range pattern {
			if pattern[i], err = d.f64(op); err != nil {
				return nil, err
			}
		}
	}
	offset, err := d.f64(op)
	if err != nil {
		return nil, err
	}
	return SetDashCommand{Pattern: pattern, Offset: offset}, nil
}

func (d *decoder) decodeSetAntiAlias() (Command, error) {
	b, err := d.u8("command")
	if err != nil {
		return nil, err
	}
	if b > 1 {
		return nil, d.fail("command", fmt.Errorf("%w: antialias flag %d", ErrMalformed, b))
	}
	return SetAntiAliasCommand{Enabled: b == 1}, nil
}

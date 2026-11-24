package svg

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gogpu/gg/text"
	"golang.org/x/text/unicode/bidi"
)

type textItem struct {
	text string
	font fontState
}

type textChunk struct {
	items  []textItem
	coord  *textCoords
	anchor string
	dir    string
}

type shapedPiece struct {
	text   string
	face   text.Face
	glyphs []text.ShapedGlyph
	width  float64
	rtl    bool
}

type laidRun struct {
	rtl    bool
	pieces []shapedPiece
	width  float64
}

func defaultFontState() fontState {
	return fontState{family: "sans-serif", size: 16, weight: 400, anchor: "start"}
}

func inheritFontState(a Attrs, parent fontState) fontState {
	fs := parent
	if v := strings.TrimSpace(a.FontFamily); v != "" {
		fs.family = v
	}
	if a.FontSizeSet || strings.TrimSpace(a.FontSizeRaw) != "" {
		fs.size = resolveFontSize(a.FontSizeRaw, a.FontSize, parent.size)
	} else if a.FontSize > 0 {
		fs.size = a.FontSize
	}
	if a.FontWeightSet || strings.TrimSpace(a.FontWeight) != "" {
		w := a.FontWeightValue
		if w == 0 {
			w = parseFontWeightValue(a.FontWeight)
		}
		switch strings.ToLower(strings.TrimSpace(a.FontWeight)) {
		case "bolder":
			w = min900(parent.weight + 200)
		case "lighter":
			w = max100(parent.weight - 200)
		}
		fs.weight = w
	}
	if v := strings.TrimSpace(a.FontStyle); v != "" {
		fs.italic = strings.EqualFold(v, "italic") || strings.EqualFold(v, "oblique")
	}
	if v := strings.TrimSpace(a.TextAnchor); v != "" {
		fs.anchor = v
	}
	return fs
}

func min900(v int) int {
	if v > 900 {
		return 900
	}
	return v
}

func max100(v int) int {
	if v < 100 {
		return 100
	}
	return v
}

func resolveFontSize(raw string, parsed float64, parent float64) float64 {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		if parsed > 0 {
			return parsed
		}
		return parent
	}
	base := parsed
	if base == 0 {
		if v, err := parseDimension(raw); err == nil {
			base = v
		}
	}
	switch {
	case strings.HasSuffix(raw, "rem"):
		v := parseFloatPrefix(raw)
		if v <= 0 {
			return parent
		}
		return v * 16
	case strings.HasSuffix(raw, "em"):
		if base <= 0 {
			return parent
		}
		return base * parent
	case strings.HasSuffix(raw, "%"):
		if base <= 0 {
			return parent
		}
		return base / 100 * parent
	case strings.HasSuffix(raw, "pt"):
		if base <= 0 {
			return parent
		}
		return base * 96 / 72
	default:
		if base > 0 {
			return base
		}
		return parent
	}
}

func parseFloatPrefix(raw string) float64 {
	raw = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), "rem")
	v, _ := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	return v
}

// textLimiter enforces byte/rune budgets while flattening parsed text.
type textLimiter struct {
	maxBytes int
	maxRunes int
	bytes    int
	runes    int
}

func newTextLimiter(o TextOptions) *textLimiter {
	return &textLimiter{maxBytes: o.byteLimit(), maxRunes: o.runeLimit()}
}

func (l *textLimiter) take(s string) string {
	if l == nil || s == "" {
		return s
	}
	if l.maxBytes >= 0 {
		remainingBytes := l.maxBytes - l.bytes
		if remainingBytes <= 0 {
			return ""
		}
		if len(s) > remainingBytes {
			s = cutAtByteLimit(s, remainingBytes)
		}
	}
	if l.maxRunes >= 0 {
		remainingRunes := l.maxRunes - l.runes
		if remainingRunes <= 0 {
			return ""
		}
		if utf8.RuneCountInString(s) > remainingRunes {
			r := []rune(s)
			s = string(r[:remainingRunes])
		}
	}
	l.bytes += len(s)
	l.runes += utf8.RuneCountInString(s)
	return s
}

func (l *textLimiter) exceeded(original string) bool {
	if l == nil {
		return false
	}
	if l.maxBytes >= 0 && len(original) > l.maxBytes {
		return true
	}
	if l.maxRunes >= 0 && utf8.RuneCountInString(original) > l.maxRunes {
		return true
	}
	return false
}

func cutAtByteLimit(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func effectiveTextChildren(e *TextElement) []Element {
	if e == nil {
		return nil
	}
	if len(e.Children) > 0 {
		return e.Children
	}
	children := make([]Element, 0, len(e.TSpans)+1)
	if s := firstNonEmpty(e.DirectContent, e.Content, e.Text, e.Value); s != "" && len(e.TSpans) == 0 {
		children = append(children, &TextRun{Content: s, Text: s, Value: s})
	}
	for i := range e.TSpans {
		if e.TSpans[i] != nil {
			children = append(children, e.TSpans[i])
		}
	}
	if len(e.TSpans) == 0 {
		for i := range e.Spans {
			span := e.Spans[i]
			children = append(children, &span)
		}
	}
	if len(children) == 0 {
		if s := firstNonEmpty(e.Content, e.Text, e.Value); s != "" {
			children = append(children, &TextRun{Content: s, Text: s, Value: s})
		}
	}
	return children
}

func effectiveSpanChildren(e *TSpanElement) []Element {
	if e == nil {
		return nil
	}
	if len(e.Children) > 0 {
		return e.Children
	}
	children := make([]Element, 0, len(e.TSpans)+1)
	if s := firstNonEmpty(e.Content, e.Text, e.Value); s != "" && len(e.TSpans) == 0 {
		children = append(children, &TextRun{Content: s, Text: s, Value: s})
	}
	for i := range e.TSpans {
		if e.TSpans[i] != nil {
			children = append(children, e.TSpans[i])
		}
	}
	if len(e.TSpans) == 0 {
		for i := range e.Spans {
			span := e.Spans[i]
			children = append(children, &span)
		}
	}
	if len(children) == 0 {
		if s := firstNonEmpty(e.Content, e.Text, e.Value); s != "" {
			children = append(children, &TextRun{Content: s, Text: s, Value: s})
		}
	}
	return children
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func coordsPresent(c textCoords) bool {
	return c.xSet || c.ySet || c.dxSet || c.dySet
}

func buildTextChunks(e *TextElement, parent fontState, limiter *textLimiter) []*textChunk {
	rootFont := inheritFontState(e.Attrs, parent)
	rootCoord := textCoords{
		x: e.X, y: e.Y, dx: e.DX, dy: e.DY,
		xs: e.XValues, ys: e.YValues, dxs: e.DXValues, dys: e.DYValues,
		xSet: e.XSet, ySet: e.YSet, dxSet: e.DXSet, dySet: e.DYSet,
	}
	root := &textChunk{coord: &rootCoord, anchor: rootFont.anchor, dir: e.Attrs.Direction}
	chunks := []*textChunk{root}
	current := root

	var addNodes func(nodes []Element, font fontState)
	addNodes = func(nodes []Element, font fontState) {
		for _, node := range nodes {
			switch n := node.(type) {
			case *TextRun:
				s := limiter.take(n.Text)
				if strings.TrimSpace(s) == "" {
					continue
				}
				current.items = append(current.items, textItem{text: s, font: font})
			case *TSpanElement:
				spanFont := inheritFontState(n.Attrs, font)
				saved := current
				coord := textCoords{
					x: n.X, y: n.Y, dx: n.DX, dy: n.DY,
					xs: n.XValues, ys: n.YValues, dxs: n.DXValues, dys: n.DYValues,
					xSet: n.XSet, ySet: n.YSet, dxSet: n.DXSet, dySet: n.DYSet,
				}
				if coordsPresent(coord) {
					chunk := &textChunk{coord: &coord, anchor: spanFont.anchor, dir: n.Attrs.Direction}
					chunks = append(chunks, chunk)
					current = chunk
				}
				addNodes(effectiveSpanChildren(n), spanFont)
				current = saved
			}
		}
	}

	addNodes(effectiveTextChildren(e), rootFont)

	// Keep empty coordinate-only chunks: they carry the root/tspan position
	// state needed by the renderer even when all character data is elsewhere.
	return chunks
}

func chunkText(chunk *textChunk) string {
	var b strings.Builder
	for _, item := range chunk.items {
		b.WriteString(item.text)
	}
	return b.String()
}

func layoutChunk(chunk *textChunk, doc *Document) ([]laidRun, float64, bool, []rune) {
	full := chunkText(chunk)
	if full == "" {
		return nil, 0, strings.EqualFold(chunk.dir, "rtl"), nil
	}
	runes := []rune(full)
	itemOfRune := make([]int, len(runes))
	pos := 0
	for i, item := range chunk.items {
		n := utf8.RuneCountInString(item.text)
		for j := 0; j < n && pos+j < len(itemOfRune); j++ {
			itemOfRune[pos+j] = i
		}
		pos += n
	}

	baseRTL := strings.EqualFold(chunk.dir, "rtl")
	var p bidi.Paragraph
	_, _ = p.SetString(full)
	if ord, err := p.Order(); err == nil {
		if ord.Direction() == bidi.RightToLeft {
			baseRTL = true
		}
	}

	// Recompute bidi runs and split their rune ranges at style boundaries.
	var para bidi.Paragraph
	_, _ = para.SetString(full, bidi.DefaultDirection(bidi.Neutral))
	ord, err := para.Order()
	if err != nil || ord.NumRuns() == 0 {
		fallbackFont := defaultFontState()
		if len(chunk.items) > 0 {
			fallbackFont = chunk.items[0].font
		}
		pieces, missing := shapeRange(chunk, doc, 0, len(runes), 0, false, fallbackFont)
		width := piecesWidth(pieces)
		return []laidRun{{rtl: false, pieces: pieces, width: width}}, width, baseRTL, missing
	}

	var missing []rune
	var runs []laidRun
	totalWidth := 0.0
	for i := 0; i < ord.NumRuns(); i++ {
		br := ord.Run(i)
		start, end := br.Pos()
		if start < 0 || end >= len(runes) || start > end {
			continue
		}
		rtl := br.Direction() == bidi.RightToLeft
		rangeStart := start
		for r := start + 1; r <= end+1; r++ {
			if r == end+1 || itemOfRune[r] != itemOfRune[rangeStart] {
				itemIndex := itemOfRune[rangeStart]
				var font fontState
				if itemIndex >= 0 && itemIndex < len(chunk.items) {
					font = chunk.items[itemIndex].font
				}
				pieces, miss := shapeRange(chunk, doc, rangeStart, r, itemIndex, rtl, font)
				missing = appendMissing(missing, miss...)
				w := piecesWidth(pieces)
				runs = append(runs, laidRun{
					rtl:    rtl,
					pieces: pieces,
					width:  w,
				})
				totalWidth += w
				if r <= end {
					rangeStart = r
				}
			}
		}
	}
	return runs, totalWidth, baseRTL, missing
}

func piecesWidth(pieces []shapedPiece) float64 {
	w := 0.0
	for _, p := range pieces {
		w += p.width
	}
	return w
}

func appendMissing(dst []rune, values ...rune) []rune {
	seen := make(map[rune]bool, len(dst))
	for _, r := range dst {
		seen[r] = true
	}
	for _, r := range values {
		if !seen[r] {
			dst = append(dst, r)
			seen[r] = true
		}
	}
	return dst
}

func shapeRange(
	chunk *textChunk,
	doc *Document,
	startRune, endRune, itemIndex int,
	rtl bool,
	font fontState,
) ([]shapedPiece, []rune) {
	full := []rune(chunkText(chunk))
	if startRune < 0 {
		startRune = 0
	}
	if endRune > len(full) {
		endRune = len(full)
	}
	if startRune >= endRune {
		return nil, nil
	}
	if font.size <= 0 {
		font = defaultFontState()
	}
	sub := string(full[startRune:endRune])
	lang := languageForText(sub)
	faces := doc.resolveFaces(font, lang)
	if len(faces) == 0 {
		return nil, nil
	}

	var pieces []shapedPiece
	var missing []rune
	var currentFace text.Face
	var current strings.Builder
	flush := func() {
		if current.Len() == 0 || currentFace == nil {
			return
		}
		s := current.String()
		piece := shapedPiece{text: s, face: currentFace, rtl: rtl}
		if currentFace.Source() != nil {
			piece.glyphs = shapeOrRaw(s, currentFace)
			w := 0.0
			for _, g := range piece.glyphs {
				if g.X+g.XAdvance > w {
					w = g.X + g.XAdvance
				}
			}
			if w == 0 {
				w = currentFace.Advance(s)
			}
			piece.width = w
		} else {
			piece.width = currentFace.Advance(s)
		}
		pieces = append(pieces, piece)
		current.Reset()
	}

	for _, r := range sub {
		chosen := -1
		for i, face := range faces {
			if face != nil && face.HasGlyph(r) {
				chosen = i
				break
			}
		}
		if chosen < 0 {
			missing = appendMissing(missing, r)
			chosen = 0
		}
		face := faces[chosen]
		if currentFace != nil && face != currentFace {
			flush()
		}
		currentFace = face
		current.WriteRune(r)
	}
	flush()
	return pieces, missing
}

func shapeOrRaw(s string, face text.Face) []text.ShapedGlyph {
	if glyphs := text.Shape(s, face); len(glyphs) > 0 {
		return glyphs
	}
	raw := face.AppendGlyphs(nil, s)
	out := make([]text.ShapedGlyph, 0, len(raw))
	for _, g := range raw {
		out = append(out, text.ShapedGlyph{
			GID:      g.GID,
			Cluster:  g.Cluster,
			X:        g.X,
			Y:        g.Y,
			XAdvance: g.Advance,
		})
	}
	return out
}

func languageForText(s string) string {
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Arabic, r):
			return "ar"
		case unicode.Is(unicode.Hebrew, r):
			return "he"
		case text.IsCJKRune(r):
			return "zh"
		}
	}
	return ""
}

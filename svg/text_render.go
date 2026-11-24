package svg

import (
	"fmt"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/text"
)

func renderSVGText(dc *gg.Context, e *TextElement, state *renderState) {
	if dc == nil || e == nil || state == nil {
		return
	}
	doc := state.doc
	if doc == nil {
		doc = &Document{}
	}
	if contextFace := dc.Font(); contextFace != nil {
		if doc.Font == nil && doc.FontFace == nil && doc.TextFont == nil {
			doc.Font = contextFace
			doc.FontFace = contextFace
			doc.TextFont = contextFace
		} else {
			doc.FallbackFonts = append([]text.Face{contextFace}, doc.FallbackFonts...)
		}
	}
	for _, perr := range e.ParseErrors {
		doc.reportTextError(perr)
	}

	style := resolveStyle(&e.Attrs, state)
	if !style.fill.present && !style.stroke.present {
		return
	}

	limiter := newTextLimiter(doc.TextOptions)
	if full := firstNonEmpty(e.Content, e.Text); full != "" && limiter.exceeded(full) {
		doc.reportTextError(&TextError{
			Code: ErrTextTooLong,
			Text: fmt.Sprintf("%d bytes/%d runes", len(full), len([]rune(full))),
		})
	}

	chunks := buildTextChunks(e, state.font, limiter)
	if len(chunks) == 0 {
		return
	}

	dc.Push()
	dc.Transform(elementTransformMatrix(&e.Attrs))
	if style.fill.present {
		setResolvedColor(dc, style.fill.color)
	}

	penX, penY := 0.0, 0.0
	var missing []rune
	for chunkIndex, chunk := range chunks {
		if c := chunk.coord; c != nil {
			if c.xSet {
				penX = c.x
			} else if chunkIndex == 0 {
				penX = 0
			}
			if c.ySet {
				penY = c.y
			} else if chunkIndex == 0 {
				penY = 0
			}
			if c.dxSet {
				penX += c.dx
			}
			if c.dySet {
				penY += c.dy
			}
		}

		runs, width, baseRTL, miss := layoutChunk(chunk, doc)
		missing = appendMissing(missing, miss...)
		anchor := chunk.anchor
		if anchor == "" {
			anchor = "start"
		}
		offset := 0.0
		switch anchor {
		case "middle":
			offset = width / 2
		case "end":
			offset = width
		case "start":
			if baseRTL {
				offset = width
			}
		}
		if style.fill.present {
			drawLaidChunk(dc, runs, width, baseRTL, penX-offset, penY)
		}
		if style.stroke.present {
			strokeLaidChunk(dc, runs, baseRTL, penX-offset, penY, style)
		}
		penX += width
	}
	dc.Pop()

	if len(missing) > 0 {
		sample := string(missing)
		if len([]rune(sample)) > 20 {
			sample = string([]rune(sample)[:20])
		}
		doc.reportTextError(&TextError{Code: ErrMissingGlyph, Text: sample})
	}
}

func drawLaidChunk(dc *gg.Context, runs []laidRun, width float64, baseRTL bool, x, y float64) {
	if !baseRTL {
		cursor := 0.0
		for _, run := range runs {
			drawLaidRun(dc, run, x+cursor, y)
			cursor += run.width
		}
		return
	}
	cursor := width
	for _, run := range runs {
		cursor -= run.width
		drawLaidRun(dc, run, x+cursor, y)
	}
}

func drawLaidRun(dc *gg.Context, run laidRun, x, y float64) {
	if !run.rtl {
		cursor := 0.0
		for _, piece := range run.pieces {
			drawShapedPiece(dc, piece, x+cursor, y, false)
			cursor += piece.width
		}
		return
	}
	cursor := run.width
	for _, piece := range run.pieces {
		cursor -= piece.width
		drawShapedPiece(dc, piece, x+cursor, y, true)
	}
}

func drawShapedPiece(dc *gg.Context, piece shapedPiece, x, y float64, runRTL bool) {
	if piece.face == nil {
		return
	}
	rtl := runRTL || piece.rtl
	if len(piece.glyphs) == 0 || piece.face.Source() == nil {
		old := dc.Font()
		dc.SetFont(piece.face)
		dc.DrawString(piece.text, x, y)
		if old != nil {
			dc.SetFont(old)
		}
		return
	}

	glyphs := piece.glyphs
	if rtl {
		glyphs = append([]text.ShapedGlyph(nil), piece.glyphs...)
		for i := range glyphs {
			g := &glyphs[i]
			advance := g.XAdvance
			if advance == 0 {
				advance = piece.face.Advance(string(rune(g.Cluster)))
			}
			g.X = piece.width - g.X - advance
			if g.X < 0 {
				g.X = 0
			}
		}
	}
	dc.DrawShapedGlyphs(glyphs, piece.face, x, y)
}

func strokeLaidChunk(dc *gg.Context, runs []laidRun, baseRTL bool, x, y float64, style resolvedStyle) {
	dc.Push()
	dc.SetLineWidth(style.stroke.width)
	dc.SetLineCap(style.stroke.cap)
	dc.SetLineJoin(style.stroke.join)
	setResolvedColor(dc, style.stroke.color)
	// Stroke support uses gg's shaping outline path for source faces. RTL and
	// MultiFace fallback still get fill rendering; this keeps stroke metrics
	// identical for the common LTR label case.
	if !baseRTL {
		cursor := 0.0
		for _, run := range runs {
			for _, piece := range run.pieces {
				if piece.face != nil {
					dc.SetFont(piece.face)
					dc.StrokeString(piece.text, x+cursor, y)
				}
				cursor += piece.width
			}
		}
	}
	dc.Pop()
}

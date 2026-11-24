package svg

import (
	"github.com/gogpu/gg/scene"
	"github.com/gogpu/gg/text"
)

func renderSceneText(target *scene.Scene, e *TextElement, state *renderState) {
	if target == nil || e == nil || state == nil || state.doc == nil {
		return
	}
	doc := state.doc
	style := resolveStyle(&e.Attrs, state)
	if !style.fill.present {
		return
	}

	chunks := buildTextChunks(e, state.font, newTextLimiter(doc.TextOptions))
	if len(chunks) == 0 {
		return
	}

	complete := state.matrix.Multiply(elementTransformMatrix(&e.Attrs))
	target.PushTransform(scene.AffineFromMatrix(complete))
	defer target.PopTransform()
	brush := scene.SolidBrush(style.fill.color)

	penX, penY := 0.0, 0.0
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

		runs, width, baseRTL, _ := layoutChunk(chunk, doc)
		offset := 0.0
		switch chunk.anchor {
		case "middle":
			offset = width / 2
		case "end":
			offset = width
		default:
			if baseRTL {
				offset = width
			}
		}
		x := penX - offset
		if !baseRTL {
			cursor := 0.0
			for _, run := range runs {
				for _, piece := range run.pieces {
					drawScenePiece(target, piece, x+cursor, penY, false, brush)
					cursor += piece.width
				}
			}
		} else {
			cursor := width
			for _, run := range runs {
				cursor -= run.width
				runCursor := run.width
				for _, piece := range run.pieces {
					runCursor -= piece.width
					drawScenePiece(target, piece, x+cursor+runCursor, penY, run.rtl, brush)
				}
			}
		}
		penX += width
	}
}

func drawScenePiece(target *scene.Scene, piece shapedPiece, x, y float64, rtl bool, brush scene.Brush) {
	if piece.face == nil {
		return
	}
	if len(piece.glyphs) == 0 || piece.face.Source() == nil {
		_ = target.DrawText(piece.text, piece.face, float32(x), float32(y), brush)
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
	_ = target.DrawGlyphs(piece.text, glyphs, piece.face, float32(x), float32(y), brush)
}

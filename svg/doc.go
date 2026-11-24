// Package svg provides an SVG renderer built on top of the gg 2D graphics library.
//
// It parses a subset of SVG XML sufficient for rendering icon-style SVGs
// (as used by JetBrains IntelliJ, Material Design, etc.) and renders them
// to RGBA images using [github.com/gogpu/gg.Context].
//
// # Supported SVG Elements
//
//   - <svg> with viewBox, width, height
//   - <path> with d attribute (full SVG path command set)
//   - <circle> cx, cy, r
//   - <rect> x, y, width, height, rx, ry
//   - <ellipse> cx, cy, rx, ry
//   - <line> x1, y1, x2, y2
//   - <polygon> points
//   - <polyline> points
//   - <text> and <tspan> with x/y/dx/dy, font and text-anchor inheritance
//   - <g> grouping with inherited attributes and transform
//
// # Supported Attributes
//
//   - fill, fill-rule, fill-opacity
//   - stroke, stroke-width, stroke-linecap, stroke-linejoin, stroke-opacity
//   - transform (translate, rotate, scale, matrix)
//   - opacity
//   - font-family, font-size, font-weight, font-style, text-anchor, direction
//
// Text uses github.com/gogpu/gg/text shaping and font fallback. Supply a face
// with [WithFont], set [Document.Font], call [SetDefaultFont], or configure a
// [FontResolver]; platform fonts are used as a best-effort fallback. Use
// [Document.RenderToChecked] or [Document.RenderChecked] to collect recoverable
// missing-glyph, invalid-coordinate and length diagnostics.
//
// # Usage
//
//	img, err := svg.Render(svgBytes, 64, 64)
//
//	// With color override (theming):
//	img, err := svg.RenderWithColor(svgBytes, 64, 64, color.White)
//
//	// Parse once, render many times:
//	doc, err := svg.Parse(svgBytes)
//	img1 := doc.Render(16, 16)
//	img2 := doc.Render(32, 32)
//	img3 := doc.RenderWithColor(24, 24, themeColor)
//
//	// Retain ordinary vector fill/stroke commands in a Scene:
//	s := scene.NewScene()
//	doc.RenderToScene(s, 0, 0, 24, 24)
//
// RenderToScene keeps geometry vector-based. For targets no larger than 32
// physical pixels it conservatively aligns thin horizontal and vertical stroke
// centers when the complete transform is translation plus uniform axis scale.
package svg

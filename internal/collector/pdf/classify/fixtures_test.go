package classify

import (
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// Test fixtures shared across the classify_test.go suite. Kept in a
// separate file to hold classify_test.go under the 300-line per-file
// project cap.

// mkRun builds a TextRun with explicit size + style flags.
// len(Glyphs) becomes the glyph weight for body-font calibration.
func mkRun(size float64, font string, mono, bold, italic bool, txt string) text.TextRun {
	g := make([]uint16, len(txt))
	for i := range g {
		g[i] = uint16(txt[i])
	}
	return text.TextRun{
		Text: txt, Glyphs: g, X: 72, Y: 700, Width: float64(len(txt)) * 6,
		Height: size, Size: size, FontKey: "F1", FontName: font,
		Mono: mono, Bold: bold, Italic: italic,
	}
}

// body is a 12pt Helvetica plain-text run.
func body(txt string) text.TextRun {
	return mkRun(12, "Helvetica", false, false, false, txt)
}

// mkBlock wraps runs into a single-line block (page 0) at the given
// y range. Tests that need a non-zero PageIndex set b.PageIndex
// directly after this returns.
func mkBlock(y0, y1 float64, runs ...text.TextRun) layout.Block {
	bbox := layout.Rect{X0: 72, Y0: y0, X1: 540, Y1: y1}
	return layout.Block{
		Lines: []layout.Line{{Runs: runs, BBox: bbox}},
		BBox:  bbox,
	}
}

// mkMultiLineBlock builds a block (page 0) from one run-slice per line,
// with each line stacked 14pt below the previous (top-down).
func mkMultiLineBlock(y0 float64, lines [][]text.TextRun) layout.Block {
	out := make([]layout.Line, len(lines))
	curY := y0
	for i, runs := range lines {
		out[i] = layout.Line{
			Runs: runs,
			BBox: layout.Rect{X0: 72, Y0: curY, X1: 540, Y1: curY + 12},
		}
		curY += 14
	}
	return layout.Block{
		Lines: out,
		BBox:  layout.Rect{X0: 72, Y0: y0, X1: 540, Y1: curY},
	}
}

// repeat returns n copies of body(txt) for glyph-weight tests.
func repeat(txt string, n int) []text.TextRun {
	out := make([]text.TextRun, n)
	for i := range out {
		out[i] = body(txt)
	}
	return out
}

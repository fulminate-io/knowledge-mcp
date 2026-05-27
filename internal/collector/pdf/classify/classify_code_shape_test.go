package classify

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// nonMonoRun builds a sans-serif TextRun whose Mono flag is false.
// Glyphs are sized to len(txt) so monospaceFraction sees the correct
// glyph weight (it would compute mono=0 / total=N, so 0%).
func nonMonoRun(txt string) text.TextRun {
	g := make([]uint16, len(txt))
	for i := range g {
		g[i] = uint16(txt[i])
	}
	return text.TextRun{
		Text: txt, Glyphs: g, Width: float64(len(txt)) * 5,
		Height: 9, Size: 9, FontKey: "F1", FontName: "PublicSans-Thin",
		Mono: false,
	}
}

// codeShapeBlock builds a multi-line block with custom per-line X0
// (lineStarts) so distinctIndentColumns can be exercised. Lines are
// stacked top-down at Y0=700 with a 14pt stride; vertical positioning
// doesn't matter for the shape-axis tests (they read X0 only).
func codeShapeBlock(lineStarts []float64, lineRuns [][]text.TextRun) layout.Block {
	if len(lineStarts) != len(lineRuns) {
		panic("codeShapeBlock: lineStarts and lineRuns must be parallel")
	}
	const y0 = 700.0
	out := make([]layout.Line, len(lineRuns))
	curY := y0
	for i, runs := range lineRuns {
		out[i] = layout.Line{
			Runs: runs,
			BBox: layout.Rect{X0: lineStarts[i], Y0: curY, X1: lineStarts[i] + 200, Y1: curY + 12},
		}
		curY += 14
	}
	return layout.Block{
		Lines: out,
		BBox:  layout.Rect{X0: lineStarts[0], Y0: y0, X1: lineStarts[0] + 200, Y1: curY},
	}
}

func TestHasNonMonoCodeShape_AcceptsSansSerifCypher(t *testing.T) {
	t.Parallel()
	// Sans-serif MERGE-heavy Cypher block matching DDIA / Graph
	// Algorithms Comprehensive's typical sample shape — every line is
	// structural (parens + braces + colons), 4 distinct indent
	// columns, no terminator. Should fire.
	blk := codeShapeBlock([]float64{72, 72, 72, 80, 88, 96}, [][]text.TextRun{
		{nonMonoRun("MERGE (a:Loc {name:\"A\"})")},
		{nonMonoRun("MERGE (b:Loc {name:\"B\"})")},
		{nonMonoRun("MERGE (c:Loc {name:\"C\"})")},
		{nonMonoRun("  MERGE (a)-[:LINK {cost:50}]->(b)")},
		{nonMonoRun("    MERGE (b)-[:LINK {cost:40}]->(c)")},
		{nonMonoRun("      MERGE (c)-[:LINK {cost:30}]->(a)")},
	})
	if !hasNonMonoCodeShape(blk) {
		t.Errorf("expected hasNonMonoCodeShape=true on sans-serif Cypher; "+
			"density=%.2f, columns=%d, lines=%d, endsTerminator=%v",
			punctuationDensity(blk), distinctIndentColumns(blk),
			len(blk.Lines), endsInProseTerminator(blk))
	}
}

func TestHasNonMonoCodeShape_RejectsProseParagraph(t *testing.T) {
	t.Parallel()
	// Plain prose — minimal punct, single column, ends in `.`.
	blk := codeShapeBlock([]float64{72, 72, 72}, [][]text.TextRun{
		{nonMonoRun("Lorem ipsum dolor sit amet, consectetur adipiscing elit.")},
		{nonMonoRun("Sed do eiusmod tempor incididunt ut labore et dolore magna.")},
		{nonMonoRun("Ut enim ad minim veniam, quis nostrud exercitation ullamco.")},
	})
	if hasNonMonoCodeShape(blk) {
		t.Errorf("prose paragraph should not be code; density=%.2f, columns=%d",
			punctuationDensity(blk), distinctIndentColumns(blk))
	}
}

func TestHasNonMonoCodeShape_RejectsFigureCaptionShape(t *testing.T) {
	t.Parallel()
	// DDIA p.291-style figure annotation: one line of `(a) (b) (c)`
	// labels (heavy parens), then sparse non-punct lines. Aggregate
	// density may clear the threshold but the per-line ratio gate
	// must reject — only one of three lines has structural punct.
	blk := codeShapeBlock([]float64{100, 200, 300}, [][]text.TextRun{
		{nonMonoRun("(a) (b) (c) time")},
		{nonMonoRun("??? ??? ???")},
		{nonMonoRun("Client")},
	})
	if hasNonMonoCodeShape(blk) {
		t.Errorf("figure caption should not be code; columns=%d, density=%.2f",
			distinctIndentColumns(blk), punctuationDensity(blk))
	}
}

func TestHasNonMonoCodeShape_RejectsSingleLine(t *testing.T) {
	t.Parallel()
	// Even a heavily-punctuated single line is too short to safely
	// classify without surrounding mono-block context.
	blk := codeShapeBlock([]float64{72}, [][]text.TextRun{
		{nonMonoRun("MATCH (a:Loc {name:\"A\"})")},
	})
	if hasNonMonoCodeShape(blk) {
		t.Errorf("single-line block should not fire non-mono branch")
	}
}

func TestHasNonMonoCodeShape_RejectsRfcStyleProse(t *testing.T) {
	t.Parallel()
	// All-mono RFC-style paragraph — would pass the non-mono branch
	// otherwise (high incidental punct on bracket-heavy headers), but
	// the terminator gate catches it.
	blk := codeShapeBlock([]float64{72, 80, 88, 96}, [][]text.TextRun{
		{nonMonoRun("The Cache-Control header (RFC 7234, section 5.2) is:")},
		{nonMonoRun("[directive] (=, value)? [, directive ...]")},
		{nonMonoRun("with directives like max-age=N, s-maxage=N, public, private.")},
		{nonMonoRun("Senders can combine these to express caching policy.")},
	})
	if hasNonMonoCodeShape(blk) {
		t.Errorf("RFC-style prose should not be code (terminator gate)")
	}
}

func TestPunctuationDensity_CodeVsProse(t *testing.T) {
	t.Parallel()
	codeBlk := codeShapeBlock([]float64{72, 80}, [][]text.TextRun{
		{nonMonoRun("MERGE (a:Loc {name:\"A\"})")},
		{nonMonoRun("RETURN a, count(*) AS c;")},
	})
	proseBlk := codeShapeBlock([]float64{72, 72}, [][]text.TextRun{
		{nonMonoRun("This sentence has minimal structural punctuation overall.")},
		{nonMonoRun("Another plain prose sentence with the usual comma here.")},
	})
	if d := punctuationDensity(codeBlk); d < codeShapePunctRatio {
		t.Errorf("code-shape density=%.2f, want ≥ %.2f", d, codeShapePunctRatio)
	}
	if d := punctuationDensity(proseBlk); d >= codeShapePunctRatio {
		t.Errorf("prose density=%.2f, want < %.2f", d, codeShapePunctRatio)
	}
}

func TestDistinctIndentColumns_TolerateNoise(t *testing.T) {
	t.Parallel()
	// Three lines with X0 within 2pt of each other → 1 column.
	blk := codeShapeBlock([]float64{72.0, 73.0, 72.5}, [][]text.TextRun{
		{nonMonoRun("first line")},
		{nonMonoRun("second line")},
		{nonMonoRun("third line")},
	})
	if got := distinctIndentColumns(blk); got != 1 {
		t.Errorf("distinctIndentColumns = %d, want 1 (within tolerance)", got)
	}
	// Now spread them out far enough to cross codeShapeIndentTolPt.
	blk2 := codeShapeBlock([]float64{72, 80, 90, 100}, [][]text.TextRun{
		{nonMonoRun("a")}, {nonMonoRun("b")}, {nonMonoRun("c")}, {nonMonoRun("d")},
	})
	if got := distinctIndentColumns(blk2); got != 4 {
		t.Errorf("distinctIndentColumns = %d, want 4", got)
	}
}

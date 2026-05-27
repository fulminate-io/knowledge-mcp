package classify

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// monoRun is a monospaced 9pt code run with the given text. Width
// scales with text length so monospaceFraction returns a meaningful
// glyph-weight.
func monoRun(txt string) text.TextRun {
	return mkRun(9, "LiberationMono", true, false, false, txt)
}

// monoBoldRun is a bold-monospaced 9pt code run (used for bold-flip
// fragmentation tests — Ruby's `end`, SQL's `WHERE`).
func monoBoldRun(txt string) text.TextRun {
	return mkRun(9, "LiberationMono-Bold", true, true, false, txt)
}

// codeBlock builds a multi-line BlockCode at the given X column /
// y0 with each line at +14pt vertical stride. PageIndex defaults
// to 0; callers may override.
func codeBlock(x0, y0 float64, lines [][]text.TextRun) layout.Block {
	b := mkMultiLineBlock(y0, lines)
	for i := range b.Lines {
		b.Lines[i].BBox.X0 = x0
		b.Lines[i].BBox.X1 = x0 + 200
	}
	b.BBox.X0 = x0
	b.BBox.X1 = x0 + 200
	b.Kind = layout.BlockCode
	return b
}

func TestMergeAdjacentCodeBlocks_AdjacentSamePage(t *testing.T) {
	t.Parallel()
	// Two adjacent code blocks at the same X column with a small gap
	// (one blank line worth) — the bold-flip / paragraph-gap
	// fragmentation pattern. Should merge into one block.
	a := codeBlock(89, 700, [][]text.TextRun{
		{monoRun("File.open(path) do |f|")},
		{monoRun("  f.each { |line| ... }")},
	})
	// b sits ~26pt below a (one blank line gap on a 14pt stride).
	b := codeBlock(89, 740, [][]text.TextRun{
		{monoBoldRun("end")},
	})
	out := mergeAdjacentCodeBlocks([]layout.Block{a, b})
	if got := len(out); got != 1 {
		t.Fatalf("len(out) = %d, want 1; out=%+v", got, out)
	}
	if got := len(out[0].Lines); got != 3 {
		t.Errorf("merged Lines = %d, want 3", got)
	}
}

func TestMergeAdjacentCodeBlocks_LeadingKeyword(t *testing.T) {
	t.Parallel()
	// SQL "leading keyword" pattern: a single-line all-mono block
	// classified as BlockHeading sits just above a multi-line code
	// block. Reclassify and merge.
	keyword := mkBlock(700, 712, monoBoldRun("SELECT"))
	keyword.Kind = layout.BlockHeading
	keyword.Lines[0].BBox.X0 = 89
	keyword.BBox.X0 = 89
	body := codeBlock(89, 720, [][]text.TextRun{
		{monoRun("  dim_date.weekday,")},
		{monoRun("  SUM(quantity)")},
	})
	out := mergeAdjacentCodeBlocks([]layout.Block{keyword, body})
	if got := len(out); got != 1 {
		t.Fatalf("len(out) = %d, want 1", got)
	}
	if out[0].Kind != layout.BlockCode {
		t.Errorf("merged Kind = %v, want BlockCode", out[0].Kind)
	}
	if got := len(out[0].Lines); got != 3 {
		t.Errorf("merged Lines = %d, want 3", got)
	}
}

func TestMergeAdjacentCodeBlocks_InteriorInterloper(t *testing.T) {
	t.Parallel()
	// SQL pattern: code, [WHERE heading-shaped keyword], code →
	// reclassify the middle as code and stitch all three.
	left := codeBlock(89, 700, [][]text.TextRun{
		{monoRun("  dim_date.weekday,")},
		{monoRun("  SUM(quantity)")},
	})
	mid := mkBlock(728, 740, monoBoldRun("WHERE"))
	mid.Kind = layout.BlockHeading
	mid.Lines[0].BBox.X0 = 89
	mid.BBox.X0 = 89
	right := codeBlock(89, 752, [][]text.TextRun{
		{monoRun("  dim_date.year = 2013")},
	})
	out := mergeAdjacentCodeBlocks([]layout.Block{left, mid, right})
	if got := len(out); got != 1 {
		t.Fatalf("len(out) = %d, want 1", got)
	}
	if got := len(out[0].Lines); got != 4 {
		t.Errorf("merged Lines = %d, want 4", got)
	}
}

func TestMergeAdjacentCodeBlocks_TerminatorRejection(t *testing.T) {
	t.Parallel()
	// Two multi-line mono blocks where the first ends in a sentence
	// terminator (the RFC-style plain-text-paragraph pattern). The
	// terminator gate should keep them separate even though both
	// are code-classified, same column, small gap.
	a := codeBlock(72, 700, [][]text.TextRun{
		{monoRun("First sentence one.")},
		{monoRun("Second sentence two.")},
	})
	b := codeBlock(72, 740, [][]text.TextRun{
		{monoRun("Third sentence three.")},
		{monoRun("Fourth sentence four.")},
	})
	out := mergeAdjacentCodeBlocks([]layout.Block{a, b})
	if got := len(out); got != 2 {
		t.Errorf("len(out) = %d, want 2 (terminator should reject merge)", got)
	}
}

func TestMergeAdjacentCodeBlocks_DifferentColumnNoMerge(t *testing.T) {
	t.Parallel()
	// Two code blocks at clearly different X columns — distinct code
	// snippets, not one program. Must not merge.
	a := codeBlock(89, 700, [][]text.TextRun{
		{monoRun("snippet_one()")},
		{monoRun("more_one()")},
	})
	b := codeBlock(200, 740, [][]text.TextRun{
		{monoRun("snippet_two()")},
		{monoRun("more_two()")},
	})
	out := mergeAdjacentCodeBlocks([]layout.Block{a, b})
	if got := len(out); got != 2 {
		t.Errorf("len(out) = %d, want 2 (different X column)", got)
	}
}

func TestMergeAdjacentCodeBlocks_LargeGapNoMerge(t *testing.T) {
	t.Parallel()
	// Code blocks separated by a large vertical gap (well above
	// codeVerticalGapPt) — not the same program. Must not merge.
	a := codeBlock(89, 700, [][]text.TextRun{
		{monoRun("first()")},
		{monoRun("more_first()")},
	})
	b := codeBlock(89, 800, [][]text.TextRun{
		{monoRun("second()")},
		{monoRun("more_second()")},
	})
	out := mergeAdjacentCodeBlocks([]layout.Block{a, b})
	if got := len(out); got != 2 {
		t.Errorf("len(out) = %d, want 2 (gap too large)", got)
	}
}

func TestMergeAdjacentCodeBlocks_DifferentPagesNoMerge(t *testing.T) {
	t.Parallel()
	// mergeAdjacentCodeBlocks is per-page — cross-page stitching is
	// the chunker's continuity pass. Two code blocks on different
	// pages must not merge here even when geometrically close.
	a := codeBlock(89, 700, [][]text.TextRun{{monoRun("counts = Hash.new(0)")}})
	a.PageIndex = 0
	b := codeBlock(89, 720, [][]text.TextRun{{monoRun("File.open() do |f|")}})
	b.PageIndex = 1
	out := mergeAdjacentCodeBlocks([]layout.Block{a, b})
	if got := len(out); got != 2 {
		t.Errorf("len(out) = %d, want 2 (different pages)", got)
	}
}

func TestMergeAdjacentCodeBlocks_NonCodeBlocksUntouched(t *testing.T) {
	t.Parallel()
	// Paragraph blocks must pass through unmodified — only adjacent
	// code blocks (and bold-keyword interlopers between them) are
	// the merge target.
	p1 := mkBlock(700, 712, body("This is prose."))
	p1.Kind = layout.BlockParagraph
	p2 := mkBlock(720, 732, body("Another paragraph."))
	p2.Kind = layout.BlockParagraph
	out := mergeAdjacentCodeBlocks([]layout.Block{p1, p2})
	if got := len(out); got != 2 {
		t.Errorf("len(out) = %d, want 2 (paragraphs untouched)", got)
	}
}

package chunk

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// continuityBlock builds a single-line block with the supplied text,
// font, size, and X0 for the continuity-heuristic tests. PageIndex
// is set explicitly because the heuristic gates on pageIdx==prev+1.
func continuityBlock(pageIdx int, kind layout.BlockKind, x0, size float64, font, txt string) layout.Block {
	bbox := layout.Rect{X0: x0, Y0: 700, X1: x0 + 200, Y1: 712}
	run := text.TextRun{
		Text:     txt,
		Size:     size,
		Height:   size,
		FontKey:  font,
		FontName: font,
	}
	return layout.Block{
		Kind:      kind,
		Lines:     []layout.Line{{Runs: []text.TextRun{run}, BBox: bbox}},
		BBox:      bbox,
		PageIndex: pageIdx,
	}
}

// continuityCodeBlock builds a single-line BlockCode at X0=72 / 9pt
// whose run carries Mono=true and a glyph slice sized to len(txt) so
// blockMonoFraction returns 1.0. Used by the cross-page code-branch
// tests; the mono signal is what the relaxed sameMonoFamily check
// now requires.
func continuityCodeBlock(pageIdx int, font, txt string) layout.Block {
	b := continuityBlock(pageIdx, layout.BlockCode, 72, 9, font, txt)
	r := &b.Lines[0].Runs[0]
	r.Mono = true
	r.Glyphs = make([]uint16, len(txt))
	return b
}

// TestMergeAcrossPages_All3SignalsMerge — last block of page 0 ends
// with no terminator, first block of page 1 starts lowercase with
// matching font + X0 → merged into 1 mergedBlock spanning pages
// [0,1].
func TestMergeAcrossPages_All3SignalsMerge(t *testing.T) {
	t.Parallel()
	perPage := [][]layout.Block{
		{continuityBlock(0, layout.BlockParagraph, 72, 12, "Helvetica", "continued from")},
		{continuityBlock(1, layout.BlockParagraph, 72, 12, "Helvetica", "lowercase start of next")},
	}
	out := mergeAcrossPages(perPage)
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1 merged block", len(out))
	}
	if got := out[0].PageRange; got != [2]int{0, 1} {
		t.Errorf("PageRange = %v, want [0,1]", got)
	}
}

// TestMergeAcrossPages_TerminatorBlocksMerge — tail ends with '.',
// signal 1 fails → no merge.
func TestMergeAcrossPages_TerminatorBlocksMerge(t *testing.T) {
	t.Parallel()
	perPage := [][]layout.Block{
		{continuityBlock(0, layout.BlockParagraph, 72, 12, "Helvetica", "ends here.")},
		{continuityBlock(1, layout.BlockParagraph, 72, 12, "Helvetica", "lowercase start")},
	}
	out := mergeAcrossPages(perPage)
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2 (terminator blocks merge)", len(out))
	}
	if out[0].PageRange != [2]int{0, 0} {
		t.Errorf("out[0].PageRange = %v, want [0,0]", out[0].PageRange)
	}
	if out[1].PageRange != [2]int{1, 1} {
		t.Errorf("out[1].PageRange = %v, want [1,1]", out[1].PageRange)
	}
}

// TestMergeAcrossPages_HeadingNeverMerges — last block on page 0 is
// a heading (no terminator); does not merge with page 1's lowercase
// paragraph.
func TestMergeAcrossPages_HeadingNeverMerges(t *testing.T) {
	t.Parallel()
	perPage := [][]layout.Block{
		{continuityBlock(0, layout.BlockHeading, 72, 14, "Helvetica-Bold", "Heading No Period")},
		{continuityBlock(1, layout.BlockParagraph, 72, 12, "Helvetica", "lowercase start")},
	}
	out := mergeAcrossPages(perPage)
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2 (heading never merges)", len(out))
	}
}

// TestMergeAcrossPages_DifferentFontBlocksMerge — fonts differ
// between tail and head; signal 3a fails → no merge.
func TestMergeAcrossPages_DifferentFontBlocksMerge(t *testing.T) {
	t.Parallel()
	perPage := [][]layout.Block{
		{continuityBlock(0, layout.BlockParagraph, 72, 12, "Helvetica", "no period here")},
		{continuityBlock(1, layout.BlockParagraph, 72, 12, "Times", "lowercase next")},
	}
	out := mergeAcrossPages(perPage)
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2 (different font blocks merge)", len(out))
	}
}

// TestMergeAcrossPages_DifferentXStartBlocksMerge — tail.BBox.X0
// differs from head.BBox.X0 by >2pt; signal 3b fails → no merge.
func TestMergeAcrossPages_DifferentXStartBlocksMerge(t *testing.T) {
	t.Parallel()
	perPage := [][]layout.Block{
		{continuityBlock(0, layout.BlockParagraph, 72, 12, "Helvetica", "no period here")},
		{continuityBlock(1, layout.BlockParagraph, 100, 12, "Helvetica", "lowercase next")},
	}
	out := mergeAcrossPages(perPage)
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2 (different X0 blocks merge)", len(out))
	}
}

// TestMergeAcrossPages_CodeToCodeMergesAcrossUppercaseStart — DDIA's
// Ruby program: page 0 ends with `counts = Hash.new(0)` (closes with
// `)`), page 1 starts with `File.open(...)` (uppercase `F`). The
// prose path would reject — terminator on `)` is fine but the head's
// uppercase `F` fails the lowercase-head rule. The relaxed code-to-code
// branch should still fire on matching mono fraction + X0.
func TestMergeAcrossPages_CodeToCodeMergesAcrossUppercaseStart(t *testing.T) {
	t.Parallel()
	perPage := [][]layout.Block{
		{continuityCodeBlock(0, "LiberationMono", "counts = Hash.new(0)")},
		{continuityCodeBlock(1, "LiberationMono", "File.open(path) do |f|")},
	}
	out := mergeAcrossPages(perPage)
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1 (code-to-code branch should merge)", len(out))
	}
	if out[0].PageRange != [2]int{0, 1} {
		t.Errorf("PageRange = %v, want [0,1]", out[0].PageRange)
	}
}

// TestMergeAcrossPages_CodeToCodeMergesAcrossBoldMonoVariant — Graph
// Databases 2e p.30→p.31 SQL split: `... JOIN Person p2` (regular-mono)
// → `ON PersonFriend.FriendID = p2.ID WHERE p2.Person = 'Bob'`
// (bold-mono on the leading keyword). FontName equality rejects the
// merge incorrectly; the mono-fraction sameness check should fire.
func TestMergeAcrossPages_CodeToCodeMergesAcrossBoldMonoVariant(t *testing.T) {
	t.Parallel()
	perPage := [][]layout.Block{
		{continuityCodeBlock(0, "LiberationMono", "FROM Person p1 JOIN Person p2")},
		{continuityCodeBlock(1, "LiberationMono-Bold", "ON PersonFriend.FriendID = p2.ID")},
	}
	out := mergeAcrossPages(perPage)
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1 (bold-mono variant should still merge)", len(out))
	}
	if out[0].PageRange != [2]int{0, 1} {
		t.Errorf("PageRange = %v, want [0,1]", out[0].PageRange)
	}
}

// TestMergeAcrossPages_CodeToCodeRejectsNonMono — safety case: a code
// block followed by a block with no monospace runs (Mono flag absent)
// must not merge under the relaxed branch. Mono-fraction sameness is
// the load-bearing signal that prevents bridging into prose.
func TestMergeAcrossPages_CodeToCodeRejectsNonMono(t *testing.T) {
	t.Parallel()
	tail := continuityCodeBlock(0, "LiberationMono", "func a()")
	// Head looks like code (BlockCode kind, same X0) but its sole run
	// has no Mono flag — e.g. a prose block misclassified as code by
	// some upstream signal. Must not merge.
	head := continuityBlock(1, layout.BlockCode, 72, 9, "Helvetica", "func b()")
	out := mergeAcrossPages([][]layout.Block{{tail}, {head}})
	if len(out) != 2 {
		t.Errorf("len(out) = %d, want 2 (head not mono — must not merge)", len(out))
	}
}

// TestMergeAcrossPages_EmptyPageBreaksContinuation — page 0 ends
// without terminator, page 1 is empty, page 2 starts lowercase with
// matching font + X0; the empty page resets the tail tracker so no
// merge occurs across the gap.
func TestMergeAcrossPages_EmptyPageBreaksContinuation(t *testing.T) {
	t.Parallel()
	perPage := [][]layout.Block{
		{continuityBlock(0, layout.BlockParagraph, 72, 12, "Helvetica", "no period")},
		{}, // empty page 1
		{continuityBlock(2, layout.BlockParagraph, 72, 12, "Helvetica", "lowercase next")},
	}
	out := mergeAcrossPages(perPage)
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2 (empty page breaks continuation)", len(out))
	}
	if out[0].PageRange != [2]int{0, 0} || out[1].PageRange != [2]int{2, 2} {
		t.Errorf("PageRanges = %v / %v, want [0,0] / [2,2]", out[0].PageRange, out[1].PageRange)
	}
}

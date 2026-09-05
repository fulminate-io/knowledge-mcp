package chunk

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/classify"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// TestChunk_HappyPath is the T1 compile-only smoke. Builds a parent
// Chunk with one child to confirm self-reference compiles, and exercises
// the cross-package layout.BlockKind binding so a binding regression
// breaks loudly here rather than at first chunker integration.
func TestChunk_HappyPath(t *testing.T) {
	t.Parallel()

	child := Chunk{
		Kind:      layout.BlockParagraph,
		Text:      "hello world",
		PageRange: [2]int{0, 0},
	}
	parent := Chunk{
		Kind:         layout.BlockHeading,
		Text:         "Section A",
		HeadingLevel: 1,
		PageRange:    [2]int{0, 0},
		Children:     []Chunk{child},
		Metadata:     map[string]string{"section_id": "1"},
	}
	if parent.Kind != layout.BlockHeading {
		t.Errorf("parent.Kind = %q, want BlockHeading", parent.Kind)
	}
	if got := len(parent.Children); got != 1 {
		t.Fatalf("Children len = %d, want 1", got)
	}
	if parent.Children[0].Kind != layout.BlockParagraph {
		t.Errorf("child.Kind = %q, want BlockParagraph", parent.Children[0].Kind)
	}
	if parent.Children[0].Text != "hello world" {
		t.Errorf("child.Text = %q, want 'hello world'", parent.Children[0].Text)
	}
	if parent.Metadata["section_id"] != "1" {
		t.Errorf("Metadata[section_id] = %q, want 1", parent.Metadata["section_id"])
	}
}

// TestMode_Constants asserts ModeParagraph / ModeSection have the
// documented string values. Tripwire for accidental drift in chunker
// configuration vocabulary.
func TestMode_Constants(t *testing.T) {
	t.Parallel()
	if string(ModeParagraph) != "paragraph" {
		t.Errorf("ModeParagraph = %q, want \"paragraph\"", ModeParagraph)
	}
	if string(ModeSection) != "section" {
		t.Errorf("ModeSection = %q, want \"section\"", ModeSection)
	}
}

// TestDefaultOptions_AllFieldsPopulated pins the Phase 1 default
// surface. Mode=ModeSection is a deliberate flip from the ticket-pinned
// ModeParagraph (resolved Q3) — section context is the better default
// for recipe-driven pattern extraction. LayoutParams + ClassifyParams
// borrow their respective package defaults; MinChunkChars is 0.
func TestDefaultOptions_AllFieldsPopulated(t *testing.T) {
	t.Parallel()
	if DefaultOptions.Mode != ModeSection {
		t.Errorf("DefaultOptions.Mode = %q, want ModeSection (resolved Q3 — flipped from ticket-pinned ModeParagraph)", DefaultOptions.Mode)
	}
	if DefaultOptions.LayoutParams != layout.DefaultLayoutParams {
		t.Errorf("DefaultOptions.LayoutParams = %#v, want layout.DefaultLayoutParams", DefaultOptions.LayoutParams)
	}
	// classify.ClassifyParams contains a *regexp.Regexp pointer; struct
	// equality would cover the simple float fields but the pointer
	// requires field-by-field comparison.
	if got, want := DefaultOptions.ClassifyParams.HeadingFontSizeRatio, classify.DefaultClassifyParams.HeadingFontSizeRatio; got != want {
		t.Errorf("ClassifyParams.HeadingFontSizeRatio = %v, want %v", got, want)
	}
	if got, want := DefaultOptions.ClassifyParams.HeadingMinBoldOnly, classify.DefaultClassifyParams.HeadingMinBoldOnly; got != want {
		t.Errorf("ClassifyParams.HeadingMinBoldOnly = %v, want %v", got, want)
	}
	if got, want := DefaultOptions.ClassifyParams.CodeMonospaceRatio, classify.DefaultClassifyParams.CodeMonospaceRatio; got != want {
		t.Errorf("ClassifyParams.CodeMonospaceRatio = %v, want %v", got, want)
	}
	if DefaultOptions.ClassifyParams.ListMarkerPattern != classify.DefaultClassifyParams.ListMarkerPattern {
		t.Errorf("ClassifyParams.ListMarkerPattern pointer mismatch — DefaultOptions did not borrow the package default regexp")
	}
	if DefaultOptions.MinChunkChars != 0 {
		t.Errorf("DefaultOptions.MinChunkChars = %d, want 0", DefaultOptions.MinChunkChars)
	}
}

// TestNormalizeBlockText_ProseCollapsesWhitespace checks the
// prose / heading / list / unknown path: trim + collapse internal
// whitespace runs (any combination of spaces, tabs, newlines) to a
// single space.
func TestNormalizeBlockText_ProseCollapsesWhitespace(t *testing.T) {
	t.Parallel()
	block := layout.Block{
		Kind: layout.BlockParagraph,
		Lines: []layout.Line{
			{Runs: []text.TextRun{txtRun("  hello  "), txtRun("world  ")}},
			{Runs: []text.TextRun{txtRun("\tfoo\n  bar")}},
		},
	}
	got := normalizeBlockText(block)
	want := "hello world foo bar"
	if got != want {
		t.Errorf("normalizeBlockText prose = %q, want %q", got, want)
	}
}

// TestNormalizeBlockText_CodePreservesNewlines checks the code path:
// preserve '\n' between lines; preserve per-line leading whitespace
// (indentation is the whole point of code formatting); collapse runs
// of 2+ internal whitespace to a single space; strip trailing
// whitespace per line.
func TestNormalizeBlockText_CodePreservesNewlines(t *testing.T) {
	t.Parallel()
	block := layout.Block{
		Kind: layout.BlockCode,
		Lines: []layout.Line{
			{Runs: []text.TextRun{txtRun("  func main() {")}},
			{Runs: []text.TextRun{txtRun("    println(\"hi\")")}},
			{Runs: []text.TextRun{txtRun("}  ")}},
		},
	}
	got := normalizeBlockText(block)
	want := "  func main() {\n    println(\"hi\")\n}"
	if got != want {
		t.Errorf("normalizeBlockText code = %q, want %q", got, want)
	}
}

// TestNormalizeBlockText_ListItemKeepsMarker confirms that the
// list-item path uses the prose collapse: the marker ("- ", "* ", etc)
// the layout grouper captured as the first run survives intact.
func TestNormalizeBlockText_ListItemKeepsMarker(t *testing.T) {
	t.Parallel()
	block := layout.Block{
		Kind: layout.BlockListItem,
		Lines: []layout.Line{
			{Runs: []text.TextRun{txtRun("- "), txtRun("first item ")}},
		},
	}
	got := normalizeBlockText(block)
	want := "- first item"
	if got != want {
		t.Errorf("normalizeBlockText list-item = %q, want %q", got, want)
	}
}

// TestBuildParagraphs_FlatPreservesKindAndHeadingLevel feeds 1 H1 + 3
// paragraph blocks to buildParagraphs and verifies the output order +
// per-Chunk Kind/HeadingLevel.
func TestBuildParagraphs_FlatPreservesKindAndHeadingLevel(t *testing.T) {
	t.Parallel()
	mb := []mergedBlock{
		{Block: layout.Block{
			Kind:         layout.BlockHeading,
			HeadingLevel: 1,
			Lines:        []layout.Line{{Runs: []text.TextRun{txtRun("Section A")}}},
			BBox:         layout.Rect{X0: 72, Y0: 720, X1: 540, Y1: 740},
		}, PageRange: [2]int{0, 0}},
		{Block: layout.Block{
			Kind:  layout.BlockParagraph,
			Lines: []layout.Line{{Runs: []text.TextRun{txtRun("first prose")}}},
			BBox:  layout.Rect{X0: 72, Y0: 700, X1: 540, Y1: 712},
		}, PageRange: [2]int{0, 0}},
		{Block: layout.Block{
			Kind:  layout.BlockParagraph,
			Lines: []layout.Line{{Runs: []text.TextRun{txtRun("second prose")}}},
			BBox:  layout.Rect{X0: 72, Y0: 680, X1: 540, Y1: 692},
		}, PageRange: [2]int{0, 0}},
		{Block: layout.Block{
			Kind:  layout.BlockParagraph,
			Lines: []layout.Line{{Runs: []text.TextRun{txtRun("third prose")}}},
			BBox:  layout.Rect{X0: 72, Y0: 660, X1: 540, Y1: 672},
		}, PageRange: [2]int{0, 0}},
	}
	out := buildParagraphs(mb)
	if len(out) != 4 {
		t.Fatalf("buildParagraphs len = %d, want 4", len(out))
	}
	if out[0].Kind != layout.BlockHeading || out[0].HeadingLevel != 1 {
		t.Errorf("out[0] kind/level = %s/%d, want heading/1", out[0].Kind, out[0].HeadingLevel)
	}
	for i := 1; i < 4; i++ {
		if out[i].Kind != layout.BlockParagraph {
			t.Errorf("out[%d].Kind = %s, want paragraph", i, out[i].Kind)
		}
		if out[i].HeadingLevel != 0 {
			t.Errorf("out[%d].HeadingLevel = %d, want 0", i, out[i].HeadingLevel)
		}
	}
	if out[0].Text != "Section A" || out[1].Text != "first prose" {
		t.Errorf("text mismatch: %q / %q", out[0].Text, out[1].Text)
	}
}

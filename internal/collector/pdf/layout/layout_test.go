package layout

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// TestBlockKind_Constants asserts the 6 BlockKind constants exist with
// the documented string values. Tripwire for accidental drift in the
// classifier's output vocabulary.
func TestBlockKind_Constants(t *testing.T) {
	t.Parallel()
	cases := map[BlockKind]string{
		BlockUnknown:   "",
		BlockHeading:   "heading",
		BlockParagraph: "paragraph",
		BlockCode:      "code_block",
		BlockListItem:  "list_item",
		BlockTable:     "table",
	}
	for k, want := range cases {
		if string(k) != want {
			t.Errorf("BlockKind %v: got %q, want %q", k, string(k), want)
		}
	}
}

// TestBlockLineLayout_ZeroValue is a compile-only smoke that builds a
// zero-value Block, Line, and LayoutParams to confirm field shapes. T4
// replaces this with real grouping tests.
func TestBlockLineLayout_ZeroValue(t *testing.T) {
	t.Parallel()
	var b Block
	if b.Kind != BlockUnknown {
		t.Errorf("zero-value Kind = %q, want BlockUnknown (\"\")", b.Kind)
	}
	if b.PageIndex != 0 || b.HeadingLevel != 0 {
		t.Errorf("zero-value Block index/heading non-zero: %+v", b)
	}
	if b.Metadata != nil {
		t.Errorf("zero-value Block.Metadata should be nil, got %v", b.Metadata)
	}

	var l Line
	if l.Runs != nil {
		t.Errorf("zero-value Line.Runs should be nil, got %v", l.Runs)
	}
	// Line.Runs should accept []text.TextRun without error — proves the
	// cross-package type binding is intact.
	l.Runs = []text.TextRun{{Text: "hello"}}
	if got := l.Runs[0].Text; got != "hello" {
		t.Errorf("Line.Runs[0].Text = %q, want %q", got, "hello")
	}

	// T4 replaced T1's zero-value DefaultLayoutParams with
	// pdfminer.six's LAParams structure plus median-based threshold
	// values. The 6 T4-relevant fields are populated; the 4 T5-owned
	// fields stay zero until T5 lands.
	want := LayoutParams{
		LineMargin:        0.4,
		CharMargin:        2.0,
		WordMargin:        0.1,
		BoxesFlow:         0.5,
		ParagraphGapRatio: 1.6,
		DetectVertical:    false,
	}
	if DefaultLayoutParams != want {
		t.Errorf("DefaultLayoutParams = %+v, want %+v", DefaultLayoutParams, want)
	}
}

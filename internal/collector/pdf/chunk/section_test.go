package chunk

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// hb is a heading-block helper for section tests.
func hb(level int, txt string) mergedBlock {
	return mergedBlock{Block: layout.Block{
		Kind:         layout.BlockHeading,
		HeadingLevel: level,
		Lines:        []layout.Line{{Runs: []text.TextRun{txtRun(txt)}}},
		BBox:         layout.Rect{X0: 72, Y0: 700, X1: 540, Y1: 712},
	}, PageRange: [2]int{0, 0}}
}

// pb is a paragraph-block helper for section tests.
func pb(txt string) mergedBlock {
	return mergedBlock{Block: layout.Block{
		Kind:  layout.BlockParagraph,
		Lines: []layout.Line{{Runs: []text.TextRun{txtRun(txt)}}},
		BBox:  layout.Rect{X0: 72, Y0: 700, X1: 540, Y1: 712},
	}, PageRange: [2]int{0, 0}}
}

// TestBuildSections_H1Plus3Paragraphs_OneRootThreeChildren — input
// H1 + 3 paragraphs → 1 top-level Chunk (H1) with 3 Children, all
// body chunks.
func TestBuildSections_H1Plus3Paragraphs_OneRootThreeChildren(t *testing.T) {
	t.Parallel()
	blocks := []mergedBlock{
		hb(1, "Section A"),
		pb("first"),
		pb("second"),
		pb("third"),
	}
	out := buildSections(blocks)
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1 root", len(out))
	}
	if out[0].Kind != layout.BlockHeading || out[0].HeadingLevel != 1 {
		t.Errorf("root kind/level = %s/%d, want heading/1", out[0].Kind, out[0].HeadingLevel)
	}
	if len(out[0].Children) != 3 {
		t.Fatalf("len(children) = %d, want 3", len(out[0].Children))
	}
	for i, c := range out[0].Children {
		if c.Kind != layout.BlockParagraph {
			t.Errorf("child[%d].Kind = %s, want paragraph", i, c.Kind)
		}
	}
}

// TestBuildSections_H1H2H2H3Paragraph — H1 + H2 + H2 + H3 + paragraph
// → 1 H1 with 2 H2 children; second H2 contains 1 H3 child; H3 has
// 1 paragraph child.
func TestBuildSections_H1H2H2H3Paragraph(t *testing.T) {
	t.Parallel()
	blocks := []mergedBlock{
		hb(1, "H1 root"),
		hb(2, "H2 first"),
		hb(2, "H2 second"),
		hb(3, "H3 nested"),
		pb("body under H3"),
	}
	out := buildSections(blocks)
	if len(out) != 1 {
		t.Fatalf("top-level len = %d, want 1", len(out))
	}
	h1 := out[0]
	if h1.HeadingLevel != 1 {
		t.Errorf("h1.HeadingLevel = %d, want 1", h1.HeadingLevel)
	}
	if len(h1.Children) != 2 {
		t.Fatalf("h1.Children len = %d, want 2 (the two H2)", len(h1.Children))
	}
	for i, c := range h1.Children {
		if c.HeadingLevel != 2 {
			t.Errorf("h1.Children[%d].HeadingLevel = %d, want 2", i, c.HeadingLevel)
		}
	}
	secondH2 := h1.Children[1]
	if len(secondH2.Children) != 1 {
		t.Fatalf("secondH2.Children len = %d, want 1 (the H3)", len(secondH2.Children))
	}
	h3 := secondH2.Children[0]
	if h3.HeadingLevel != 3 {
		t.Errorf("h3.HeadingLevel = %d, want 3", h3.HeadingLevel)
	}
	if len(h3.Children) != 1 || h3.Children[0].Kind != layout.BlockParagraph {
		t.Errorf("h3.Children = %#v, want one paragraph child", h3.Children)
	}
}

// TestBuildSections_OrphanBodyBeforeFirstHeading — body at index 0 +
// H1 + paragraph → top-level slice has [synthetic root with orphan
// body, H1 with 1 paragraph child].
func TestBuildSections_OrphanBodyBeforeFirstHeading(t *testing.T) {
	t.Parallel()
	blocks := []mergedBlock{
		pb("orphan body"),
		hb(1, "H1 first"),
		pb("body under H1"),
	}
	out := buildSections(blocks)
	if len(out) != 2 {
		t.Fatalf("top-level len = %d, want 2 (synthetic + H1)", len(out))
	}
	syn := out[0]
	if syn.Kind != layout.BlockUnknown || syn.Text != "" {
		t.Errorf("synthetic root kind/text = %s/%q, want BlockUnknown/empty", syn.Kind, syn.Text)
	}
	if len(syn.Children) != 1 || syn.Children[0].Text != "orphan body" {
		t.Errorf("synthetic root children = %#v, want one orphan body", syn.Children)
	}
	h1 := out[1]
	if h1.HeadingLevel != 1 || len(h1.Children) != 1 {
		t.Errorf("h1 shape: level=%d, children=%d; want 1/1", h1.HeadingLevel, len(h1.Children))
	}
}

// TestBuildSections_NoHeadings_SingleSyntheticRoot — 5 paragraphs
// no headings → top-level slice is exactly 1 synthetic-root Chunk
// with all 5 paragraphs as Children.
func TestBuildSections_NoHeadings_SingleSyntheticRoot(t *testing.T) {
	t.Parallel()
	blocks := []mergedBlock{pb("a"), pb("b"), pb("c"), pb("d"), pb("e")}
	out := buildSections(blocks)
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1 synthetic root", len(out))
	}
	root := out[0]
	if root.Kind != layout.BlockUnknown || root.Text != "" {
		t.Errorf("root kind/text = %s/%q, want BlockUnknown/empty", root.Kind, root.Text)
	}
	if len(root.Children) != 5 {
		t.Fatalf("root.Children len = %d, want 5", len(root.Children))
	}
}

// TestBuildSections_HeadingLevelGap — H1 + H3 + paragraph (no H2)
// → H1 has 1 H3 child; H3 has 1 paragraph child; NO synthetic H2
// inserted (resolved Q5 — flat-into-nearest-ancestor).
func TestBuildSections_HeadingLevelGap(t *testing.T) {
	t.Parallel()
	blocks := []mergedBlock{
		hb(1, "H1 root"),
		hb(3, "H3 jumps two"),
		pb("body"),
	}
	out := buildSections(blocks)
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1", len(out))
	}
	h1 := out[0]
	if len(h1.Children) != 1 {
		t.Fatalf("h1.Children len = %d, want 1 (H3 directly)", len(h1.Children))
	}
	h3 := h1.Children[0]
	if h3.HeadingLevel != 3 {
		t.Errorf("h1.Children[0].HeadingLevel = %d, want 3 (no synthetic H2)", h3.HeadingLevel)
	}
	if len(h3.Children) != 1 || h3.Children[0].Kind != layout.BlockParagraph {
		t.Errorf("h3.Children = %#v, want one paragraph child", h3.Children)
	}
}

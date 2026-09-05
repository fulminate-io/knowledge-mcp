package recipe

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/pdfcollector"
)

// interpret_expr_pdf_content_test.go — the DSL's `body` accessor read
// against a section node the PRODUCTION pdf collector emitted, not a
// hand-built one.
//
// The distinction is the point. readNodeField's own unit tests build a
// node literal, which agrees with whatever the test author believed the
// emitter does; this drives a real fixture through the real collector,
// so the accessor and the emitter are checked against each other rather
// than against one author's assumption.

// taggedFixture is the only in-repo pdf fixture that emits a section.
const taggedFixture = "../collector/pdf/testdata/corpus/tagged-libreoffice-sample/source.pdf"

// TestContentConvention_PdfSectionBodyThroughTheRecipeAccessor asserts
// the intended semantics of the Content convention as a recipe sees
// them: a pdf section's `body` IS its heading. A recipe that wants leaf
// text only selects leaves.
func TestContentConvention_PdfSectionBodyThroughTheRecipeAccessor(t *testing.T) {
	t.Parallel()

	abs, err := filepath.Abs(taggedFixture)
	if err != nil {
		t.Fatalf("resolve fixture: %v", err)
	}
	c := &pdfcollector.PDFCollector{}
	res, err := c.Collect(context.Background(), abs, collector.CollectOptions{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	sections := 0
	for _, n := range res.Nodes {
		if n.GetType() != "section" {
			continue
		}
		heading := n.GetSymbolName()
		if heading == "" {
			// A section with no heading has no text to reach; it is not
			// a witness either way.
			continue
		}
		sections++
		if got := readNodeField(n, []string{"body"}); got != heading {
			t.Errorf("emitted section %s: recipe body field = %q, want the heading %q - a pdf section's body IS its heading under the Content convention",
				n.GetId(), got, heading)
		} else {
			t.Logf("emitted section %s: symbol=%q content=%q recipe-body=%q",
				n.GetId(), heading, n.GetContent(), got)
		}
	}

	// Without this the test passes over an empty section set, which is
	// exactly the state a tree with an unreachable tagged path is in.
	if sections == 0 {
		t.Fatalf("the tagged fixture emitted no section carrying a heading (nodes=%d) - there is nothing for the accessor to read, so this test would pass vacuously", len(res.Nodes))
	}
}

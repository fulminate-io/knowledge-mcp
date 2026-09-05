// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"path/filepath"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/pdfcollector"
)

// interpret_render_pdf_title_test.go — heading_path read against the nodes the
// PRODUCTION pdf collector emits for a document whose Info dict carries no
// title, which is the surface the defect was reported against.
//
// It drives the real collector over a checked-in fixture rather than building
// node literals, for the reason interpret_expr_pdf_content_test.go states: a
// hand-built graph agrees with whatever the test author believed the emitter
// does, and the claim here is about the emitter and the reader TOGETHER.

// TestHeadingPath_TopLevelSectionOfATitlelessPDF asserts that a section whose
// only ancestor is the document root still renders a locality path.
//
// Before the title derivation, a titleless PDF's document root carried an empty
// SymbolName, headingPath skipped it as an empty ancestor value, and every
// top-level section rendered the empty string — no locality at all for exactly
// the sections a reader most needs to place.
func TestHeadingPath_TopLevelSectionOfATitlelessPDF(t *testing.T) {
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
	if len(res.Edges) == 0 {
		t.Fatalf("the tagged fixture emitted no edges (nodes=%d); there is no graph to walk", len(res.Nodes))
	}

	// TAKE THE EDGE TYPE OFF THE EMISSION. The collector stamps
	// kgtypes.EdgeContains, whose value is the UPPERCASE "CONTAINS", while the
	// DSL examples in the docs spell it lowercase. sourceView.edgesFrom
	// compares the type exactly, so a hardcoded lowercase spelling would find
	// no parents and green this test vacuously over an empty section set.
	edges := make([]*knowledgev1.Edge, 0, len(res.Edges))
	for _, e := range res.Edges {
		edges = append(edges, &knowledgev1.Edge{
			FromId:   e.FromID,
			ToId:     e.ToID,
			Type:     string(e.Type),
			Evidence: e.Evidence,
		})
	}
	edgeType := edges[0].GetType()
	t.Logf("collector stamped edge type %q", edgeType)

	var rootID string
	roots := 0
	for _, n := range res.Nodes {
		if n.GetType() == "document" {
			roots++
			rootID = n.GetId()
		}
	}
	if roots != 1 {
		t.Fatalf("the tagged fixture emitted %d document roots, want exactly 1", roots)
	}

	sv := renderView(res.Nodes, edges)

	checked := 0
	for _, n := range res.Nodes {
		if n.GetType() != "section" {
			continue
		}
		parents := sv.edgesFrom(n.GetId(), edgeType, incomingEdges)
		if len(parents) != 1 || parents[0] != rootID {
			continue
		}
		checked++
		row := &Row{NodeID: n.GetId(), Node: n, Vars: map[string]string{}}
		got := mustEval(t, newEnv(), row, fn("heading_path", edgeType, "symbol_name", " > "), sv)
		t.Logf("top-level section %s (%q): heading_path = %q", n.GetId(), n.GetSymbolName(), got)
		if got == "" {
			t.Errorf("top-level section %s (%q): heading_path rendered the empty string; a reader gets no locality at all for a section whose only ancestor is the document root",
				n.GetId(), n.GetSymbolName())
		}
	}

	// VACUITY GUARD. Without it this test passes over an empty set, which is
	// precisely the state an unreachable tagged path leaves behind — and a
	// wrong edge-type spelling produces the same empty set.
	if checked == 0 {
		t.Fatalf("the tagged fixture emitted no section attached directly to the document root (nodes=%d edges=%d edge type %q) - nothing was walked, so this test would pass vacuously",
			len(res.Nodes), len(edges), edgeType)
	}
}

//go:build pdf_ddia_acceptance

// collector_ddia_test.go — build-tag-gated end-to-end acceptance test
// against the Designing Data-Intensive Applications PDF. Run via:
//
//	PDF_DDIA_PATH=/path/to/ddia.pdf go test -tags pdf_ddia_acceptance \
//	    -run TestCollect_DDIA_Acceptance ./collector/pdf/pdfcollector/
//
// CI does NOT include this build tag — the fixture is a ~13 MB book
// PDF that we are not licensed to redistribute. Operators run the
// acceptance test locally against their own copy. Test t.Skips when
// PDF_DDIA_PATH is unset OR points at a missing file.

package pdfcollector

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestCollect_DDIA_Acceptance is the headline acceptance gate from the
// T10 ticket DoD: collect(type:"pdf", id:DDIA) produces a GraphPDFRaw
// graph with hundreds of nodes (mirroring the chunker's ~512 chunks
// over the 600+ page book). The exact number is architecture-dependent
// (line/heading/code classification), so the assertion is a generous
// ≥200-nodes lower bound.
func TestCollect_DDIA_Acceptance(t *testing.T) {
	// Clean the operator-supplied path before it reaches any file operation. The
	// value comes straight from the environment, so it is tainted by construction
	// even though the only caller is a human running this acceptance gate by hand.
	path := filepath.Clean(os.Getenv("PDF_DDIA_PATH"))
	if path == "" || path == "." {
		t.Skip("PDF_DDIA_PATH not set; skipping DDIA acceptance gate (set to absolute path of the book PDF to run)")
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("PDF_DDIA_PATH=%q not readable: %v", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("resolve %q: %v", path, err)
	}

	c := &PDFCollector{}
	res, err := c.Collect(context.Background(), abs, collector.CollectOptions{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.GraphType != kgtypes.GraphPDFRaw {
		t.Errorf("GraphType = %q, want %q", res.GraphType, kgtypes.GraphPDFRaw)
	}
	if len(res.Nodes) < 200 {
		t.Errorf("nodes len = %d, want ≥200 (DDIA chunks should produce hundreds of nodes)", len(res.Nodes))
	}
	if len(res.Edges) < len(res.Nodes)-1 {
		t.Errorf("edges len = %d, want ≥nodes-1 = %d (every non-root node needs an inbound contains edge)",
			len(res.Edges), len(res.Nodes)-1)
	}
	t.Logf("DDIA acceptance: graph_name=%q nodes=%d edges=%d", res.GraphName, len(res.Nodes), len(res.Edges))

	// Spot-check the document root.
	if res.Nodes[0].Type != "document" {
		t.Errorf("nodes[0].Type = %q, want document", res.Nodes[0].Type)
	}
	if res.Nodes[0].Metadata["source"] != "pdf" {
		t.Errorf("nodes[0].Metadata[source] = %q, want pdf", res.Nodes[0].Metadata["source"])
	}

	// Sanity-check kind distribution: there should be at least one
	// non-document, non-block node — i.e. the chunker actually
	// classified some chunks.
	classified := 0
	for _, n := range res.Nodes {
		if n.Type != "document" && n.Type != "block" {
			classified++
		}
	}
	if classified == 0 {
		t.Errorf("no classified non-document/non-block nodes; chunker produced only fallback BlockUnknown chunks")
	}
}

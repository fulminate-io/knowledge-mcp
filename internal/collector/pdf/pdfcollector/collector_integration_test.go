// SPDX-License-Identifier: Apache-2.0

package pdfcollector

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// integrationFixture is the canonical small fixture used elsewhere in
// the collector/pdf package's chunker integration tests. Its absolute
// path is recomputed inside each test because t.Helper / TempDir don't
// flow into a package-level const.
const integrationFixture = "../testdata/t4_paragraph_simple.pdf"

// TestCollect_HappyPath_RealFixture exercises the full Collect entry
// point against a real PDF. Asserts the result targets GraphPDFRaw,
// has at least one document + one chunk-derived node, and emits at
// least one EdgeContains edge per non-document node.
func TestCollect_HappyPath_RealFixture(t *testing.T) {
	t.Parallel()
	abs, err := filepath.Abs(integrationFixture)
	if err != nil {
		t.Fatalf("resolve fixture: %v", err)
	}

	c := &PDFCollector{}
	res, err := c.Collect(context.Background(), abs, collector.CollectOptions{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.GraphType != kgtypes.GraphPDFRaw {
		t.Errorf("GraphType = %q, want %q", res.GraphType, kgtypes.GraphPDFRaw)
	}
	if res.GraphName == "" {
		t.Errorf("GraphName empty; want a slug")
	}
	if !strings.Contains(res.GraphName, "t4_paragraph_simple") {
		t.Errorf("GraphName = %q, want substring t4_paragraph_simple", res.GraphName)
	}
	if len(res.Nodes) < 2 {
		t.Fatalf("nodes len = %d, want ≥2 (doc + ≥1 chunk)", len(res.Nodes))
	}
	if res.Nodes[0].Type != "document" {
		t.Errorf("nodes[0].Type = %q, want document", res.Nodes[0].Type)
	}
	// Every non-document node should have at least one inbound contains
	// edge so the document tree is connected. Build a set of edge
	// targets and check.
	targets := map[string]bool{}
	for _, e := range res.Edges {
		if e.Type == kgtypes.EdgeContains {
			targets[e.ToID] = true
		}
	}
	for i, n := range res.Nodes {
		if i == 0 {
			continue // document is the root, no parent
		}
		if !targets[n.Id] {
			t.Errorf("node %s (Type=%q) has no inbound contains edge", n.Id, n.Type)
		}
	}
}

// TestCollect_RejectsRelativePath asserts a bare "foo.pdf" or "./foo.pdf"
// id is rejected before any file I/O so the per-source graph name
// remains stable across invocations regardless of caller cwd.
func TestCollect_RejectsRelativePath(t *testing.T) {
	t.Parallel()
	cases := []string{"foo.pdf", "./foo.pdf", "testdata/x.pdf", ""}
	c := &PDFCollector{}
	for _, id := range cases {
		_, err := c.Collect(context.Background(), id, collector.CollectOptions{})
		if err == nil {
			t.Errorf("Collect(%q) returned nil error, want validation failure", id)
		}
	}
}

// TestCollect_PropagatesErrEncrypted is a behavior-pinning test for the
// encrypted-PDF error path. Per locked decision Q8, the encrypted
// fixture is deferred to a follow-up ticket — this test skips
// explicitly so the assertion shape is checked-in and the contract is
// re-enabled the moment the fixture lands.
func TestCollect_PropagatesErrEncrypted(t *testing.T) {
	t.Parallel()
	t.Skip("encrypted PDF fixture not present; deferred per user-locked decision #8")

	// When the fixture lands, the body becomes:
	//   c := &PDFCollector{}
	//   _, err := c.Collect(ctx, abs, collector.CollectOptions{})
	//   if !errors.Is(err, pdf.ErrEncrypted) {
	//       t.Errorf("err = %v, want errors.Is(pdf.ErrEncrypted)", err)
	//   }
	_ = errors.Is(nil, pdf.ErrEncrypted)
}

// TestCollect_RegisteredByName proves the side-effect init() Register
// fired by retrieving the collector from the registry by name and
// confirming the Name() round-trip.
func TestCollect_RegisteredByName(t *testing.T) {
	t.Parallel()
	c, err := collector.Lookup("pdf")
	if err != nil {
		t.Fatalf("collector.Lookup(\"pdf\"): %v; init() Register did not fire", err)
	}
	if got := c.Name(); got != "pdf" {
		t.Errorf("Name() = %q, want pdf", got)
	}
}

// TestSourceSlug_Stability pins the slug shape: lowercase, dash-only
// separators, basename-derived, hash-suffixed for uniqueness. Same
// path → same slug; different paths sharing a basename → different
// slugs.
func TestSourceSlug_Stability(t *testing.T) {
	t.Parallel()
	a := sourceSlug("/tmp/Foo Bar.pdf")
	b := sourceSlug("/tmp/Foo Bar.pdf")
	if a != b {
		t.Errorf("sourceSlug not deterministic: %q vs %q", a, b)
	}
	c := sourceSlug("/other/Foo Bar.pdf")
	if a == c {
		t.Errorf("sourceSlug collided across distinct paths sharing basename: both %q", a)
	}
	if !strings.HasPrefix(a, "foo-bar-") {
		t.Errorf("sourceSlug = %q, want lowercase dash-separated basename prefix", a)
	}
}

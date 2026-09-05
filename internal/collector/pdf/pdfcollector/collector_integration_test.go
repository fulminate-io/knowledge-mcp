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

// TestSourceSlug_IsThePlainBasename pins the slug shape after the hash
// suffix was retired: lowercase, dash-separated, basename-derived, and
// NOTHING ELSE. Same path → same slug, as before.
//
// THE PATH-SENSITIVITY LEG IS DELIBERATELY INVERTED. This test used to
// require two documents sharing a basename to derive DIFFERENT slugs; it
// now requires them to derive the SAME one. That is correct because the
// name no longer carries the job of telling two documents apart — the
// collect-time collision refusal does, by comparing the incoming file
// against the path recorded on the target graph's document root and
// refusing rather than merging. A name that disambiguated would have to be
// unreadable to do it, which is the whole defect this replaces.
//
// The wanted values are written out as literals rather than computed from
// SourceSlug, so the production code does not supply its own answer key.
func TestSourceSlug_IsThePlainBasename(t *testing.T) {
	t.Parallel()
	a := SourceSlug("/tmp/Foo Bar.pdf")
	b := SourceSlug("/tmp/Foo Bar.pdf")
	if a != b {
		t.Errorf("SourceSlug not deterministic: %q vs %q", a, b)
	}
	if a != "foo-bar" {
		t.Errorf("SourceSlug(/tmp/Foo Bar.pdf) = %q, want the plain sanitized basename foo-bar", a)
	}
	c := SourceSlug("/other/Foo Bar.pdf")
	if a != c {
		t.Errorf("SourceSlug differed across directories sharing a basename: %q vs %q; "+
			"the name is the basename alone and the collision refusal separates the documents", a, c)
	}
	// A basename that sanitizes away entirely still has to name a graph.
	if got := SourceSlug("/tmp/---.pdf"); got != "pdf" {
		t.Errorf("SourceSlug(/tmp/---.pdf) = %q, want the pdf fallback", got)
	}
}

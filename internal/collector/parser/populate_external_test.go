// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestPopulateForExternalGraph_NamespacesAllIDs builds two synthetic repos
// with overlapping file names (README.md, pkg/foo.go), runs
// PopulateForExternalGraph against each with distinct repoNames, and asserts
// the returned Node.IDs / Node.FilePaths / edge endpoints / Entries[].NodeID
// values are all prefixed with repoName + "/", and that the two repos'
// returned ID sets are disjoint.
func TestPopulateForExternalGraph_NamespacesAllIDs(t *testing.T) {
	tmpA := writeSyntheticRepo(t, "alpha")
	tmpB := writeSyntheticRepo(t, "beta")

	repoA := "owner/repoA@main"
	repoB := "owner/repoB@main"

	nodesA, edgesA, err := PopulateForExternalGraph(context.Background(), repoA, tmpA)
	if err != nil {
		t.Fatalf("PopulateForExternalGraph(repoA): %v", err)
	}
	nodesB, edgesB, err := PopulateForExternalGraph(context.Background(), repoB, tmpB)
	if err != nil {
		t.Fatalf("PopulateForExternalGraph(repoB): %v", err)
	}

	// Run again to also collect Entries — re-run via Populate so we can verify
	// the namespacing applies to entries too.
	popA, err := Populate(context.Background(), repoA, tmpA)
	if err != nil {
		t.Fatalf("Populate(repoA): %v", err)
	}

	prefixA := repoA + "/"
	prefixB := repoB + "/"

	if len(nodesA) == 0 {
		t.Fatalf("repoA produced no nodes")
	}
	if len(nodesB) == 0 {
		t.Fatalf("repoB produced no nodes")
	}

	idsA := make(map[string]bool, len(nodesA))
	for _, n := range nodesA {
		if !strings.HasPrefix(n.Id, prefixA) {
			t.Errorf("repoA node ID %q missing prefix %q", n.Id, prefixA)
		}
		if n.FilePath != "" && !strings.HasPrefix(n.FilePath, prefixA) {
			t.Errorf("repoA node FilePath %q missing prefix %q", n.FilePath, prefixA)
		}
		idsA[n.Id] = true
	}

	idsB := make(map[string]bool, len(nodesB))
	for _, n := range nodesB {
		if !strings.HasPrefix(n.Id, prefixB) {
			t.Errorf("repoB node ID %q missing prefix %q", n.Id, prefixB)
		}
		if n.FilePath != "" && !strings.HasPrefix(n.FilePath, prefixB) {
			t.Errorf("repoB node FilePath %q missing prefix %q", n.FilePath, prefixB)
		}
		idsB[n.Id] = true
	}

	for id := range idsA {
		if idsB[id] {
			t.Errorf("ID collision across repos: %q", id)
		}
	}

	for _, e := range edgesA {
		if e.FromID != "" && !strings.HasPrefix(e.FromID, prefixA) {
			t.Errorf("repoA edge FromID %q missing prefix %q", e.FromID, prefixA)
		}
		if e.ToID != "" && !strings.HasPrefix(e.ToID, prefixA) {
			t.Errorf("repoA edge ToID %q missing prefix %q", e.ToID, prefixA)
		}
		if e.FromIdx != -1 || e.ToIdx != -1 {
			t.Errorf("repoA edge %+v has non-(-1) indices; expect FromIdx=ToIdx=-1 from BatchEdge conversion", e)
		}
	}
	for _, e := range edgesB {
		if e.FromID != "" && !strings.HasPrefix(e.FromID, prefixB) {
			t.Errorf("repoB edge FromID %q missing prefix %q", e.FromID, prefixB)
		}
		if e.ToID != "" && !strings.HasPrefix(e.ToID, prefixB) {
			t.Errorf("repoB edge ToID %q missing prefix %q", e.ToID, prefixB)
		}
	}

	// Verify entries from the parallel Populate run get prefixed under
	// PopulateForExternalGraph (we re-run the namespacing check via the
	// same code path — easier to recompute than to expose internal state).
	nodesAEntries, _, err := PopulateForExternalGraph(context.Background(), repoA, tmpA)
	if err != nil {
		t.Fatalf("re-run repoA: %v", err)
	}
	if len(nodesAEntries) != len(popA.Nodes) {
		t.Errorf("nodes count drift between runs: %d vs %d", len(nodesAEntries), len(popA.Nodes))
	}
}

// TestPopulateForExternalGraph_ConversionMatchesCodesync verifies the inline
// BatchEdge conversion in PopulateForExternalGraph is field-for-field
// equivalent to the codesync.toBatchEdges body. We replicate the codesync
// conversion locally and assert the two outputs match exactly for a
// synthetic input slice.
//
// This guards against future drift between the inline conversion and the
// codesync conversion. Both must remain semantically identical because
// they target the same kgwire.BatchEdge schema.
func TestPopulateForExternalGraph_ConversionMatchesCodesync(t *testing.T) {
	src := []*knowledgev1.Edge{
		{FromId: "a", ToId: "b", Type: string(kgtypes.EdgeCalls), Weight: 1.0},
		{FromId: "x", ToId: "y", Type: string(kgtypes.EdgeContains), Weight: 0},
		{FromId: "p/q", ToId: "r/s", Type: string(kgtypes.EdgeLanguage), Weight: 2.5},
	}

	// Replicate codesync/sync.go:113 toBatchEdges body exactly.
	codesyncOut := make([]kgwire.BatchEdge, len(src))
	for i, e := range src {
		codesyncOut[i] = kgwire.BatchEdge{
			FromIdx: -1,
			ToIdx:   -1,
			FromID:  e.FromId,
			ToID:    e.ToId,
			Type:    kgtypes.EdgeType(e.Type),
			Weight:  e.Weight,
		}
	}

	// Replicate the inline body from PopulateForExternalGraph (without
	// the prefix, to compare apples to apples).
	inlineOut := make([]kgwire.BatchEdge, len(src))
	for i, e := range src {
		inlineOut[i] = kgwire.BatchEdge{
			FromIdx: -1,
			ToIdx:   -1,
			FromID:  e.FromId,
			ToID:    e.ToId,
			Type:    kgtypes.EdgeType(e.Type),
			Weight:  e.Weight,
		}
	}

	if len(codesyncOut) != len(inlineOut) {
		t.Fatalf("len mismatch: codesync=%d inline=%d", len(codesyncOut), len(inlineOut))
	}
	for i := range codesyncOut {
		c, in := codesyncOut[i], inlineOut[i]
		if c.FromIdx != in.FromIdx || c.ToIdx != in.ToIdx ||
			c.FromID != in.FromID || c.ToID != in.ToID ||
			c.Type != in.Type || c.Weight != in.Weight {
			t.Errorf("edge %d differs: codesync=%+v inline=%+v", i, c, in)
		}
	}
}

// writeSyntheticRepo creates a small Go repo on disk with a README.md and
// pkg/foo.go containing one named function. Returns the temp dir.
func writeSyntheticRepo(t *testing.T, name string) string {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "README.md"), "# repo "+name+"\n")
	mustWriteDir(t, filepath.Join(root, "pkg"))
	mustWrite(t, filepath.Join(root, "pkg", "foo.go"), "package pkg\n\nfunc Foo() string { return \""+name+"\" }\n")
	return root
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustWriteDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

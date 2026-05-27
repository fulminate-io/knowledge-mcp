// SPDX-License-Identifier: Apache-2.0

package codesync

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestPopulate_EmitsLanguageNodesAndEdges runs Populate against a small
// fixture repo containing Go and TypeScript files and asserts the
// produced PopulateResult includes:
//
//   - one NodeLanguage per distinct language seen
//   - one EdgeLanguage per non-comment chunk
//   - IsExported populated correctly for chunks whose grammar exposes
//     the export attribute (TS export_statement)
//   - no remaining "exported" metadata key on any node
//
// This is the integration counterpart to TestChunkResultsToPopulate_*
// — the unit tests construct treesitter.Result fixtures by hand; this
// test exercises the real DiscoverFiles + ChunkFilesParallel pipeline.
//
// IsExported is grammar-specific. The tree-sitter chunker sets
// chunk.Exported only when the declaration node is an
// `export_statement` (JS/TS). Go capitalized identifiers do NOT get
// IsExported=true today — that mirrors the legacy "exported" metadata
// key behavior we replaced.
func TestPopulate_EmitsLanguageNodesAndEdges(t *testing.T) {
	dir := t.TempDir()
	writePopulateFixtureFile(t, filepath.Join(dir, "pkg", "auth.go"), `package pkg

// Authenticate is a public function.
func Authenticate(token string) bool {
	return true
}

func helper() bool {
	return false
}
`)
	writePopulateFixtureFile(t, filepath.Join(dir, "pkg", "user.go"), `package pkg

func GetUser(id string) string {
	return id
}
`)
	writePopulateFixtureFile(t, filepath.Join(dir, "web", "api.ts"), `export function PublicAPI(): string {
	return "ok";
}

function localHelper(): string {
	return "x";
}
`)

	const repoName = "fixture"
	pop, err := parser.Populate(context.Background(), repoName, dir)
	require.NoError(t, err)

	// One NodeLanguage per distinct language (go + typescript).
	langByID := map[string]*knowledgev1.Node{}
	for _, n := range pop.Nodes {
		if kgtypes.NodeType(n.Type) == kgtypes.NodeLanguage {
			langByID[n.Id] = n
		}
	}
	require.Len(t, langByID, 2, "expected 2 NodeLanguage (go + typescript), got %d", len(langByID))
	for _, want := range []string{"lang:" + repoName + ":go", "lang:" + repoName + ":typescript"} {
		n, ok := langByID[want]
		require.True(t, ok, "missing NodeLanguage %q", want)
		assert.NotEmpty(t, n.SymbolName, "lang_node %q SymbolName should be set", want)
		assert.NotEmpty(t, n.Language, "lang_node %q Language should be set", want)
	}

	// At least 4 EdgeLanguage edges (Authenticate, helper, GetUser, PublicAPI; localHelper too).
	var langEdges []*knowledgev1.Edge
	for _, e := range pop.Edges {
		if kgtypes.EdgeType(e.Type) == kgtypes.EdgeLanguage {
			langEdges = append(langEdges, e)
		}
	}
	require.GreaterOrEqual(t, len(langEdges), 4, "expected at least 4 EdgeLanguage edges; got %d", len(langEdges))
	// Every edge target must be one of the lang_node IDs we created.
	for _, e := range langEdges {
		_, ok := langByID[e.ToId]
		assert.True(t, ok, "EdgeLanguage ToID %q is not a known lang_node", e.ToId)
	}

	// IsExported = true for the TS exported function (export_statement).
	var sawTSExported bool
	for _, n := range pop.Nodes {
		if nt := kgtypes.NodeType(n.Type); nt == kgtypes.NodeFile || nt == kgtypes.NodeLanguage {
			continue
		}
		if n.SymbolName == "PublicAPI" && n.IsExported {
			sawTSExported = true
		}
		// No node should still carry the legacy "exported" metadata key.
		assert.Empty(t, n.Metadata["exported"], "node %q still has legacy exported=%q metadata", n.Id, n.Metadata["exported"])
	}
	assert.True(t, sawTSExported, "did not see exported TypeScript PublicAPI function with IsExported=true")
}

func writePopulateFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// SPDX-License-Identifier: Apache-2.0

package codesync

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// newTestCtx returns a plain background context. CodeCollector.Collect runs
// the pure parser collection path (parser.Populate + DetectBranch + go.mod/layer
// reads) — none of which read a store txn from the context — so the former
// store.NewTxn/WithTxn wrapper was vestigial. The client is wire-only: tests
// never spin up a store engine.
func newTestCtx(t testing.TB) context.Context {
	t.Helper()
	return context.Background()
}

// nodeIndex holds the per-node lookups a corpus-scale measurement needs to
// classify an edge by its caller: the caller's language and the file it was
// declared in.
type nodeIndex struct {
	langByID map[string]string
	fileByID map[string]string
}

// buildNodeIndex builds both lookups in a single traversal of nodes.
func buildNodeIndex(nodes []*knowledgev1.Node) nodeIndex {
	idx := nodeIndex{
		langByID: make(map[string]string, len(nodes)),
		fileByID: make(map[string]string, len(nodes)),
	}
	for _, n := range nodes {
		if n.Id == "" {
			continue
		}
		idx.langByID[n.Id] = n.Language
		if n.FilePath != "" {
			idx.fileByID[n.Id] = filepath.ToSlash(n.FilePath)
		}
	}
	return idx
}

// writeFile creates a file at path with content, creating intermediate
// directories as needed.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

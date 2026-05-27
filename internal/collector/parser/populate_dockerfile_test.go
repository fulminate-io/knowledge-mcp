// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestPopulate_PopulatesDockerfileContent verifies the FUL-241 Phase 7
// client-side Dockerfile content capture: after Populate runs over a
// repo containing a Dockerfile, the NodeFile entry for that file
// carries Content equal to the file bytes.
func TestPopulate_PopulatesDockerfileContent(t *testing.T) {
	dir := t.TempDir()
	dockerfile := "FROM golang:1.22 AS build\nWORKDIR /src\nCOPY . .\nRUN go build -o app ./cmd/app\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o600))
	// Need at least one Go file so the repo discovery accepts the tree.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o600))

	pop, err := Populate(context.Background(), "myrepo", dir)
	require.NoError(t, err)

	// Find the Dockerfile NodeFile.
	var dockerfileNode *knowledgev1.Node
	var goFileNode *knowledgev1.Node
	for _, n := range pop.Nodes {
		if kgtypes.NodeType(n.Type) != kgtypes.NodeFile {
			continue
		}
		if n.FilePath == "Dockerfile" || filepath.Base(n.FilePath) == "Dockerfile" {
			dockerfileNode = n
		}
		if filepath.Base(n.FilePath) == "main.go" {
			goFileNode = n
		}
	}
	require.NotNil(t, dockerfileNode, "Dockerfile NodeFile not found")
	assert.Equal(t, dockerfile, dockerfileNode.Content, "Dockerfile NodeFile.Content should match file bytes")

	// Regression: non-Dockerfile NodeFile entries keep empty Content.
	require.NotNil(t, goFileNode, "Go NodeFile not found")
	assert.Empty(t, goFileNode.Content, "Go NodeFile.Content should stay empty (chunk-level Content carries the body)")
}

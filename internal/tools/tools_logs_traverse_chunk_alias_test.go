// SPDX-License-Identifier: Apache-2.0

// Package tools — stream-traverse chunk-alias rendering test.
//
// Pins the polish ticket #2: chunk lines in stream traverse output
// must surface the template alias alongside the hex.
package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestStreamTraverse_ChunkLineSurfacesTemplateAlias asserts the
// chunk listing now renders "(template: <alias> · <short-hex>)"
// instead of bare hex, when an alias is persisted on the template
// node.
func TestStreamTraverse_ChunkLineSurfacesTemplateAlias(t *testing.T) {
	queryID := "q-chunk-alias"
	nodes, _ := buildLogCorpus(t, queryID)
	streamID := firstNodeIDOfType(t, nodes, kgtypes.NodeLogStream)

	h := setupLogTestHandler(t, queryID)
	result := h.traverseLogs(context.Background(), traverseArgs{
		Graph: "logs", Name: queryID, Start: streamID, Direction: "both",
	})
	require.False(t, result.IsError, "traverse: %s", resultText(result))
	text := resultText(result)

	// At least one chunk line should carry "(template: " — the prefix
	// the renderer emits regardless of whether the alias is present.
	assert.Contains(t, text, "(template: ", "chunk lines should annotate template")

	// The synthetic fixture's templates are short patterns ("connection
	// refused host-..." etc.), so they always produce a non-empty alias.
	// Find a chunk line with the alias separator and assert the format.
	hasFormatted := strings.Contains(text, " · ")
	assert.True(t, hasFormatted,
		"at least one chunk line should render alias · hex, got:\n%s", text)
}

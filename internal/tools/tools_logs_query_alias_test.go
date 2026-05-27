// SPDX-License-Identifier: Apache-2.0

// Package tools — alias-resolution tests for handleLogsQuery's id path.
//
// The id parameter is template-detail-only. These tests verify it now
// accepts a template alias transparently and returns a helpful guidance
// message when the caller mistakenly passes a stream alias.
package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestHandleLogsQuery_TemplateAliasInID(t *testing.T) {
	queryID := "q-id-template-alias"
	nodes, edges := buildLogCorpus(t, queryID)
	fake := newFakeLogGraphCaller()
	fake.seedLogGraph(queryID, nodes, edges)
	h := &Handler{graphCallerOverride: fake}
	ctx := context.Background()

	// Pick a real template ID and resolve its alias the same way the handler
	// does — rebuild the engine from the corpus (the LookupEngine registry is
	// not populated under the fake).
	templateID := firstNodeIDOfType(t, nodes, kgtypes.NodeLogTemplate)
	alias := engineFromCorpus(nodes).AliasForTemplateID(templateID)
	require.NotEmpty(t, alias, "template %q must have an alias", templateID)

	byHex := h.handleLogsQuery(ctx, queryArgs{Graph: "logs", Name: queryID, ID: templateID})
	require.False(t, byHex.IsError, "hex: %s", resultText(byHex))

	byAlias := h.handleLogsQuery(ctx, queryArgs{Graph: "logs", Name: queryID, ID: alias})
	require.False(t, byAlias.IsError, "alias: %s", resultText(byAlias))

	// Outputs must be identical — alias is a transparent rewrite.
	assert.Equal(t, resultText(byHex), resultText(byAlias),
		"alias and hex must produce identical template-detail output")
}

func TestHandleLogsQuery_TemplateAliasCaseInsensitive(t *testing.T) {
	queryID := "q-id-template-mixed"
	nodes, edges := buildLogCorpus(t, queryID)
	fake := newFakeLogGraphCaller()
	fake.seedLogGraph(queryID, nodes, edges)
	h := &Handler{graphCallerOverride: fake}

	templateID := firstNodeIDOfType(t, nodes, kgtypes.NodeLogTemplate)
	alias := engineFromCorpus(nodes).AliasForTemplateID(templateID)
	require.NotEmpty(t, alias)

	mixed := h.handleLogsQuery(context.Background(), queryArgs{
		Graph: "logs", Name: queryID, ID: strings.ToUpper(alias),
	})
	require.False(t, mixed.IsError, "uppercase alias: %s", resultText(mixed))
	assert.Contains(t, resultText(mixed), "Log template",
		"mixed-case alias must still resolve to template detail")
}

func TestHandleLogsQuery_StreamAliasInID_ReturnsGuidance(t *testing.T) {
	queryID := "q-id-stream-alias"
	nodes, edges := buildLogCorpus(t, queryID)
	fake := newFakeLogGraphCaller()
	fake.seedLogGraph(queryID, nodes, edges)
	h := &Handler{graphCallerOverride: fake}

	streamID := firstNodeIDOfType(t, nodes, kgtypes.NodeLogStream)
	alias := engineFromCorpus(nodes).AliasForStreamID(streamID)
	require.NotEmpty(t, alias)

	result := h.handleLogsQuery(context.Background(), queryArgs{
		Graph: "logs", Name: queryID, ID: alias,
	})
	require.True(t, result.IsError, "stream alias in id should be an error")
	msg := resultText(result)
	// Error must explain the situation and point to traverse.
	assert.Contains(t, msg, "stream", "guidance message must mention 'stream'")
	assert.Contains(t, msg, "traverse", "guidance message must point to traverse")
}

func TestHandleLogsQuery_HexPassthrough(t *testing.T) {
	// Identical to TestLogsQuery_TemplateDetail but isolated under the
	// alias-resolution feature so a regression here is easy to spot.
	queryID := "q-id-hex"
	nodes, edges := buildLogCorpus(t, queryID)
	fake := newFakeLogGraphCaller()
	fake.seedLogGraph(queryID, nodes, edges)
	h := &Handler{graphCallerOverride: fake}

	templateID := firstNodeIDOfType(t, nodes, kgtypes.NodeLogTemplate)

	result := h.handleLogsQuery(context.Background(), queryArgs{Graph: "logs", Name: queryID, ID: templateID})
	require.False(t, result.IsError, "hex passthrough: %s", resultText(result))
	assert.Contains(t, resultText(result), templateID,
		"hex template ID must resolve to template detail")
}

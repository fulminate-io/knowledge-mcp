// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collectorconfig"
	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
)

// collect_custom_describe_test.go — the COLLECT PATH's half of the describe
// requirement: the tool is re-verified on every collect, and a family whose
// registration declares no type vocabulary says so ONCE per collect.

// addCustomDescribeTool installs the required describe tool on a stub provider.
// A nil declaration serves the smallest conforming one.
func addCustomDescribeTool(server *mcp.Server, declaration map[string]any) {
	doc := declaration
	if doc == nil {
		doc = map[string]any{
			"behavior":    map[string]any{"summarizable": false, "embeddable": false, "syncable": true},
			"node_types":  []any{"issue"},
			"edge_types":  []any{},
			"environment": []any{},
		}
	}
	var schema map[string]any
	if err := json.Unmarshal(externalcollector.DescribeContractJSON(), &schema); err != nil {
		panic(err)
	}
	server.AddTool(&mcp.Tool{
		Name:         externalcollector.DescribeToolName,
		Description:  "stub declaration",
		InputSchema:  map[string]any{"type": "object"},
		OutputSchema: schema,
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{StructuredContent: doc}, nil
	})
}

// TestCustomCollect_AFamilyWithNoDeclaredVocabularySaysSoOncePerCollect is R4's
// accept-all arm, from the side that can see it.
//
// THE SERVER CANNOT SAY THIS. It sees an absent field on a record and cannot
// tell a registration written before the describe tool existed from one whose
// collector declares nothing. The client resolves the entry on every collect and
// knows, so the notice is the client's and needs no response-message field.
//
// ONCE PER COLLECT, NOT ONCE PER CHUNK: a collect issues one chunk per 4 MiB of
// nodes and another per 4 MiB of edges, and a per-chunk notice would repeat
// itself however the payload happened to divide.
func TestCustomCollect_AFamilyWithNoDeclaredVocabularySaysSoOncePerCollect(t *testing.T) {
	url := startCustomProvider(t, conformingCustomPayload())
	deps := newCustomDeps(t, namedCustomDef("tickets", url))

	handled, res := callCollect(deps, `{"type":"tickets","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	text := resultText(res)
	assert.Contains(t, text, "declares NO node or edge type vocabulary",
		"a family collected under accept-all must say so; silence is a widening nobody can see")
	assert.Contains(t, text, "tickets", "the notice names the family")
	assert.Contains(t, text, "collectors.json", "and the file whose entry to re-run the add against")
	// ONCE, AND THE PROPERTY IS STRUCTURAL RATHER THAN MEASURED HERE. This
	// fixture's payload is far under the 4 MiB chunk bound, so it produces one
	// chunk and a per-chunk notice would count 1 as well; what makes the count
	// per-COLLECT is that the notice is appended in customCollectRunner after the
	// sink write, outside any chunk loop — the chunker is the sink's, and this
	// frame never sees it. A fixture that chunked would cost 4 MiB of test data
	// to observe what the call site already settles, so the placement is stated
	// here and the count guards a duplicate emission inside this frame.
	assert.Equal(t, 1, strings.Count(text, "declares NO node or edge type vocabulary"),
		"once per collect: the notice is composed in the runner, not in the chunk loop")
}

// TestCustomCollect_AFamilyWITHADeclaredVocabularyIsSilent is the control the
// row above needs: a notice printed on every collect is a line nobody reads, and
// without this row an implementation that always emitted it would pass.
func TestCustomCollect_AFamilyWITHADeclaredVocabularyIsSilent(t *testing.T) {
	url := startCustomProvider(t, conformingCustomPayload())
	def := namedCustomDef("tickets", url)
	def.entry.Vocabulary = &collectorconfig.Vocabulary{
		NodeTypes: []string{"issue"},
		EdgeTypes: []string{},
	}
	deps := newCustomDeps(t, def)

	handled, res := callCollect(deps, `{"type":"tickets","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))
	assert.NotContains(t, resultText(res), "declares NO node or edge type vocabulary",
		"a family whose entry declares its vocabulary has nothing to be told")
}

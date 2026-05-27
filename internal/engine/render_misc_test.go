// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
)

// nodesResp builds the typed-wire node-carrier ExecuteResponse via the shared
// enginetest builder (P2-T5 deleted the nodes_json blob). Total rides native.
func nodesResp(t *testing.T, nodes []*knowledgev1.Node, total int) *knowledgev1.ExecuteResponse {
	t.Helper()
	resp := enginetest.ResponseWithNodes(nodes...)
	resp.Total = int64(total)
	return resp
}

func TestRenderBrowse_NumberedListAndPagination(t *testing.T) {
	nodes := []*knowledgev1.Node{
		{Id: "n1", SymbolName: "First", Status: "open", Description: "desc one"},
		{Id: "n2", SymbolName: "Second", Status: "closed"},
	}
	resp := nodesResp(t, nodes, 5) // total 5 > offset(2)+len(2) → pagination footer.
	out, err := renderBrowseResponse(resp, browseContext{Label: "knowledge", NodeType: "finding", Offset: 2, Format: "text"})
	require.NoError(t, err)
	text := out.Content[0].Text
	assert.Contains(t, text, "## knowledge — 2 finding nodes (offset 2)")
	assert.Contains(t, text, "3. **First** [open]\n   ID: n1\n   desc one")
	assert.Contains(t, text, "4. **Second** [closed]\n   ID: n2")
	assert.Contains(t, text, "_Use offset=4 to see more._")
}

func TestRenderBrowse_EmptyTyped(t *testing.T) {
	out, err := renderBrowseResponse(nodesResp(t, nil, 0), browseContext{Label: "knowledge", NodeType: "finding", Format: "text"})
	require.NoError(t, err)
	assert.Contains(t, out.Content[0].Text, "No finding nodes in knowledge graph.")
}

func TestRenderBrowse_EmptyUntyped(t *testing.T) {
	out, err := renderBrowseResponse(nodesResp(t, nil, 0), browseContext{Label: "knowledge", NodeType: "", Format: "text"})
	require.NoError(t, err)
	assert.Contains(t, out.Content[0].Text, "No nodes in knowledge graph match the requested filters.")
}

func TestRenderBrowse_MetaInline(t *testing.T) {
	nodes := []*knowledgev1.Node{
		{Id: "n1", SymbolName: "X", Metadata: map[string]string{"dsl_pattern": "p1"}},
	}
	out, err := renderBrowseResponse(nodesResp(t, nodes, 1), browseContext{
		Label: "knowledge", NodeType: "finding", Format: "text", MetaKeys: []string{"dsl_pattern"},
	})
	require.NoError(t, err)
	assert.Contains(t, out.Content[0].Text, "\n   dsl_pattern: p1")
}

func TestRenderBrowse_JSON(t *testing.T) {
	nodes := []*knowledgev1.Node{
		{Id: "n1", SymbolName: "X", Type: "finding", Status: "open"},
	}
	out, err := renderBrowseResponse(nodesResp(t, nodes, 1), browseContext{Label: "knowledge", NodeType: "finding", Format: "json"})
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &payload))
	assert.Equal(t, "knowledge", payload["graph"])
	assert.Equal(t, "finding", payload["type"])
	assert.InEpsilon(t, float64(1), payload["total"], 0.0001)
}

func TestRenderTraversal_FlatList(t *testing.T) {
	results := []TraversalResult{
		{Distance: 0, Node: &knowledgev1.Node{Id: "n0", SymbolName: "Root", Type: "plan"}},
		{Distance: 1, Node: &knowledgev1.Node{Id: "n1", SymbolName: "Child", Type: "phase"}},
	}
	resp := &knowledgev1.ExecuteResponse{TraversalResults: traversalResultsToProtoForTest(results)}

	out, rerr := renderTraversalResponse(resp, traverseContext{Start: "n0", Direction: "both", Format: "text"})
	require.NoError(t, rerr)
	text := out.Content[0].Text
	assert.Contains(t, text, "## Traversal from n0 (graph=knowledge, direction=both)")
	assert.Contains(t, text, "- [plan] Root (n0) at depth 0")
	assert.Contains(t, text, "- [phase] Child (n1) at depth 1")
}

func TestRenderTraversal_Empty(t *testing.T) {
	out, err := renderTraversalResponse(&knowledgev1.ExecuteResponse{}, traverseContext{Start: "n0", Direction: "out", Format: "text"})
	require.NoError(t, err)
	assert.Contains(t, out.Content[0].Text, "No nodes reached.")
}

func TestRenderTraversal_JSON_EdgesAlwaysEmpty(t *testing.T) {
	results := []TraversalResult{{Distance: 0, Node: &knowledgev1.Node{Id: "n0", SymbolName: "Root", Type: "plan"}}}
	resp := &knowledgev1.ExecuteResponse{TraversalResults: traversalResultsToProtoForTest(results)}
	out, err := renderTraversalResponse(resp, traverseContext{Start: "n0", GraphName: "code", Direction: "out", Format: "json"})
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &payload))
	assert.Equal(t, "code", payload["graph"])
	assert.Empty(t, payload["edges"], "include_edge_metadata is denied → edges always empty")
}

func TestRenderNodesByIDs_JSON(t *testing.T) {
	nodes := []*knowledgev1.Node{
		{Id: "a", SymbolName: "A"},
		{Id: "b", SymbolName: "B"},
	}
	out, err := renderNodesByIDsResponse(nodesResp(t, nodes, 2), "knowledge", "json", nil)
	require.NoError(t, err)
	var payload struct {
		Label string              `json:"label"`
		Nodes []*knowledgev1.Node `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &payload))
	assert.Equal(t, "knowledge", payload.Label)
	require.Len(t, payload.Nodes, 2)
	assert.Equal(t, "a", payload.Nodes[0].Id)
}

func TestRenderNodesByIDs_DefaultIsJSON(t *testing.T) {
	nodes := []*knowledgev1.Node{
		{Id: "a", SymbolName: "A"},
	}
	out, err := renderNodesByIDsResponse(nodesResp(t, nodes, 1), "knowledge", "", nil)
	require.NoError(t, err)
	assert.Contains(t, out.Content[0].Text, `"label":"knowledge"`)
}

func TestRenderMutation_CreateIDs(t *testing.T) {
	t.Run("single text", func(t *testing.T) {
		resp := &knowledgev1.ExecuteResponse{Ids: []string{"new-id"}}
		out := renderMutationResponse(resp, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, "text")
		assert.Contains(t, out.Content[0].Text, "Created → ID: new-id")
	})
	t.Run("batch text", func(t *testing.T) {
		resp := &knowledgev1.ExecuteResponse{Ids: []string{"a", "b"}}
		out := renderMutationResponse(resp, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, "text")
		assert.Contains(t, out.Content[0].Text, "Created 2 nodes → IDs: a, b")
	})
	t.Run("json", func(t *testing.T) {
		resp := &knowledgev1.ExecuteResponse{Ids: []string{"a", "b"}}
		out := renderMutationResponse(resp, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, "json")
		assert.Contains(t, out.Content[0].Text, `"ids":["a","b"]`)
	})
}

func TestRenderMutation_AffectedCount(t *testing.T) {
	cases := []struct {
		kind knowledgev1.MutationPlan_MutationKind
		verb string
	}{
		{knowledgev1.MutationPlan_MUTATION_KIND_UPDATE, "Updated"},
		{knowledgev1.MutationPlan_MUTATION_KIND_DELETE, "Deleted"},
		{knowledgev1.MutationPlan_MUTATION_KIND_LINK, "Linked"},
		{knowledgev1.MutationPlan_MUTATION_KIND_UNLINK, "Unlinked"},
	}
	for _, tc := range cases {
		t.Run(tc.verb, func(t *testing.T) {
			resp := &knowledgev1.ExecuteResponse{AffectedCount: 3}
			out := renderMutationResponse(resp, tc.kind, "text")
			assert.Contains(t, out.Content[0].Text, tc.verb+" 3 node(s)")
		})
	}
}

// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// nodeResp builds the single-node typed-wire ExecuteResponse via the shared
// enginetest builder (P2-T5 deleted the nodes_json blob).
func nodeResp(_ *testing.T, n *knowledgev1.Node) *knowledgev1.ExecuteResponse {
	return enginetest.ResponseWithNode(n)
}

func TestRenderNodeResponse_BodyFieldOrder(t *testing.T) {
	n := &knowledgev1.Node{
		Id:          "n1",
		SymbolName:  "My Finding",
		Type:        "finding",
		Status:      "open",
		Source:      "llm:claude",
		Description: "the description",
		Summary:     "the summary",
		Content:     "the content",
		Metadata:    map[string]string{"zeta": "z", "alpha": "a", "empty": ""},
	}
	out, err := renderNodeResponse(nodeResp(t, n), "cloud:prod", "n1", false, nil)
	require.NoError(t, err)
	require.False(t, out.IsError)
	text := out.Content[0].Text

	assert.Contains(t, text, "## cloud:prod node\n\n")
	assert.Contains(t, text, "**My Finding**\nID: n1\nType: finding\nStatus: open\nSource: llm:claude\n")
	assert.Contains(t, text, "\nthe description\n")
	assert.Contains(t, text, "\n**Summary:** the summary\n")
	assert.Contains(t, text, "\nthe content\n")
	// Metadata in stable key order, empty value skipped.
	assert.Contains(t, text, "\n### Metadata\n- alpha: a\n- zeta: z\n")
	assert.NotContains(t, text, "empty:")
}

func TestRenderNodeResponse_NameFallsBackToID(t *testing.T) {
	n := &knowledgev1.Node{Id: "bare-id", Type: "document"}
	out, err := renderNodeResponse(nodeResp(t, n), "cloud:prod", "bare-id", false, nil)
	require.NoError(t, err)
	assert.Contains(t, out.Content[0].Text, "**bare-id**\nID: bare-id\n")
}

func TestRenderNodeResponse_NotFound(t *testing.T) {
	out, err := renderNodeResponse(&knowledgev1.ExecuteResponse{}, "knowledge", "missing", true, nil)
	require.NoError(t, err)
	assert.True(t, out.IsError)
	assert.Contains(t, out.Content[0].Text, "node missing not found in knowledge graph")
}

// TestRenderNodeResponse_GraphLabel exercises a non-knowledge label (the
// dispatcher passes the target graph label — cloud/practice/cicd/etc).
func TestRenderNodeResponse_GraphLabel(t *testing.T) {
	n := &knowledgev1.Node{Id: "ec2-1", SymbolName: "i-abc", Type: "ec2:instance"}
	out, err := renderNodeResponse(nodeResp(t, n), "cloud:prod", "ec2-1", false, nil)
	require.NoError(t, err)
	assert.Contains(t, out.Content[0].Text, "## cloud:prod node\n\n")
}

// TestRenderNodeResponse_KnowledgeJSON asserts the knowledge-graph query-id
// shape is JSON (handleGetNode → json.MarshalIndent(node)), NOT markdown.
func TestRenderNodeResponse_KnowledgeJSON(t *testing.T) {
	n := &knowledgev1.Node{Id: "n1", SymbolName: "Doc", Type: "document", Summary: "s"}
	out, err := renderNodeResponse(nodeResp(t, n), "knowledge", "n1", true, nil)
	require.NoError(t, err)
	text := out.Content[0].Text
	assert.NotContains(t, text, "## knowledge node", "knowledge id renders JSON, not markdown")
	var decoded knowledgev1.Node
	require.NoError(t, json.Unmarshal([]byte(text), &decoded))
	assert.Equal(t, "n1", decoded.Id)
	assert.Equal(t, "Doc", decoded.SymbolName)
}

// TestRenderKnowledgeNode_WithEdges asserts the knowledge include_edges shape is
// jsonResult(NodeWithEdges{Node, Edges}). The absorption
// composition moved into dispatchQueryByID, which passes the shaped []nodeEdgeInfo
// directly into renderKnowledgeNode — so this test drives that renderer directly
// (the rendering logic is unchanged; only the carrier-decode entry point moved).
func TestRenderKnowledgeNode_WithEdges(t *testing.T) {
	n := &knowledgev1.Node{Id: "n1", SymbolName: "Hub", Type: "plan"}
	edges := []nodeEdgeInfo{{PeerID: "a", PeerName: "A", Relationship: "contains", Direction: "outgoing"}}

	out := renderKnowledgeNode(n, edges, nil)
	var nwe struct {
		Node  knowledgev1.Node `json:"node"`
		Edges []nodeEdgeInfo   `json:"edges"`
	}
	require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &nwe))
	assert.Equal(t, "n1", nwe.Node.Id)
	require.Len(t, nwe.Edges, 1)
	assert.Equal(t, "a", nwe.Edges[0].PeerID)
}

func TestRenderGenericNode_IncludeEdges(t *testing.T) {
	n := &knowledgev1.Node{Id: "n1", SymbolName: "Hub", Type: "plan"}
	edges := []nodeEdgeInfo{
		{PeerID: "a", PeerName: "A", Relationship: "contains", Direction: "outgoing"},
		{PeerID: "b", PeerName: "B", Relationship: "contains", Direction: "outgoing"},
		{PeerID: "c", PeerName: "C", Relationship: "informed-by", Direction: "incoming"},
	}

	out := renderGenericNode(n, "cloud:prod", edges, nil)
	text := out.Content[0].Text
	assert.Contains(t, text, "\n### Edges\n\n")
	assert.Contains(t, text, "- Outgoing: contains×2\n")
	assert.Contains(t, text, "- Incoming: informed-by×1\n")
	assert.Contains(t, text, "Use `traverse({ start: \"n1\" })` to see per-edge detail.\n")
}

func TestRenderGenericNode_IncludeEdges_EmptySide(t *testing.T) {
	n := &knowledgev1.Node{Id: "n1", SymbolName: "Leaf", Type: "step"}
	edges := []nodeEdgeInfo{{PeerID: "a", PeerName: "A", Relationship: "contains", Direction: "incoming"}}

	out := renderGenericNode(n, "cloud:prod", edges, nil)
	text := out.Content[0].Text
	assert.Contains(t, text, "- Outgoing: (none)\n", "empty outgoing side renders (none)")
	assert.Contains(t, text, "- Incoming: contains×1\n")
}

func TestRenderKnowledgeNode_WithCrossLinks(t *testing.T) {
	n := &knowledgev1.Node{Id: "n1", SymbolName: "Symbol", Type: "decision"}
	links := []crossLink{
		{
			EdgeType:  "uses",
			Direction: "outgoing",
			Peer:      &knowledgev1.Node{Id: "p1", SymbolName: "PatternX"},
			PeerInfo:  &knowledgev1.ProxyTarget{GraphType: string(kgtypes.GraphPractice), Name: "go"},
		},
		{
			EdgeType:  "backs",
			Direction: "incoming",
			Peer:      &knowledgev1.Node{Id: "p2", SymbolName: "CallerY"},
			PeerInfo:  &knowledgev1.ProxyTarget{GraphType: string(kgtypes.GraphCode), Name: "knowledge"},
		},
	}

	// Cross-links append to the knowledge JSON shape (the common
	// include_cross_links case). The composition lives in dispatchQueryByID; this
	// drives the renderer directly with the shaped []crossLink.
	out := renderKnowledgeNode(n, nil, links)
	text := out.Content[0].Text
	assert.Contains(t, text, "\n\n## Cross-Graph Links\n\n")
	assert.Contains(t, text, "- --uses--> PatternX [practice:go]\n")
	assert.Contains(t, text, "- <--backs-- CallerY [code:knowledge]\n")
}

func TestProxyTargetLabel(t *testing.T) {
	assert.Empty(t, proxyTargetLabel(nil))
	assert.Equal(t, "[code:knowledge]", proxyTargetLabel(&knowledgev1.ProxyTarget{GraphType: string(kgtypes.GraphCode), Name: "knowledge"}))
	assert.Equal(t, "[main]", proxyTargetLabel(&knowledgev1.ProxyTarget{GraphType: "main"}))
}

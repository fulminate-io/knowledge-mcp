// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/stretchr/testify/assert"
)

// TestRenderInspectNode_Golden asserts the three documented sections match the
// server tools_query_inspect.go markdown: Composite View, Ancestry (back-arrow
// chain with id truncated to 12), Edges (peer type+name).
func TestRenderInspectNode_Golden(t *testing.T) {
	data := InspectData{
		Node: &knowledgev1.Node{
			Id: "step-1234567890abcdef", SymbolName: "My Step", Type: "step", Status: "active", Source: "plan",
		},
		Ancestry: []InspectAncestor{
			{ID: "phase-abcdef0123456789", Name: "My Phase", Type: "phase", Status: "active", DepthAbove: 1},
			{ID: "plan-9876543210fedcba", Name: "My Plan", Type: "plan", Status: "active", DepthAbove: 2},
		},
		Edges: []InspectEdge{
			{Direction: "out", Type: "implements", Peer: "file-xyz", PeerType: "file", PeerName: "foo.go"},
			{Direction: "in", Type: "contains", Peer: "phase-abcdef0123456789", PeerType: "phase", PeerName: "My Phase"},
		},
	}
	got := RenderInspectNode(data)

	assert.Contains(t, got, "# Inspect: My Step\n\n")
	assert.Contains(t, got, "## Composite View\n")
	assert.Contains(t, got, "- **ID:** step-1234567890abcdef\n")
	assert.Contains(t, got, "- **Type:** step\n")
	assert.Contains(t, got, "- **Status:** active\n")
	assert.Contains(t, got, "- **Source:** plan\n")
	// Ancestry: depth-1 no indent, depth-2 two-space indent; id truncated to 12.
	assert.Contains(t, got, "← [phase] My Phase (status: active, id: phase-abcdef)\n")
	assert.Contains(t, got, "  ← [plan] My Plan (status: active, id: plan-9876543)\n")
	// Edges: out arrow then in arrow with peer type+name.
	assert.Contains(t, got, "  → [implements] [file] foo.go\n")
	assert.Contains(t, got, "  ← [contains] [phase] My Phase\n")
}

// TestRenderInspectNode_OrphanAndNoEdges asserts the empty cases: orphan node
// (no ancestry) and a node with no edges.
func TestRenderInspectNode_OrphanAndNoEdges(t *testing.T) {
	data := InspectData{
		Node: &knowledgev1.Node{Id: "lonely", SymbolName: "Lonely", Type: "finding", Status: "open"},
	}
	got := RenderInspectNode(data)
	assert.Contains(t, got, "## Ancestry\n(no parent — orphan node)\n")
	assert.Contains(t, got, "## Edges\n(no edges)\n")
}

// TestRenderInspectNode_DanglingEdge asserts a peer that did not resolve renders
// as a dangling edge.
func TestRenderInspectNode_DanglingEdge(t *testing.T) {
	data := InspectData{
		Node:  &knowledgev1.Node{Id: "n", SymbolName: "N", Type: "step", Status: "active"},
		Edges: []InspectEdge{{Direction: "out", Type: "relates-to", Peer: "missing-9999999999"}},
	}
	got := RenderInspectNode(data)
	assert.Contains(t, got, "  → [relates-to] [missing] missing-9999 (dangling edge)\n")
}

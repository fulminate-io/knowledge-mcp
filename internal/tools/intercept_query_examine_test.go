// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
)

// examineFake routes a fake Execute by QueryPlan shape and counts how many bulk
// ids[] hydrate calls fire (the no-N+1 guarantee under test). It models a small
// fixture: subject S with edges S→A (out) and B→S (in), and an ancestry chain
// S ← P1 ← P2 (two CONTAINS-parents).
type examineFake struct {
	subjectID string
	bulkCalls int
}

func (f *examineFake) exec(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	switch {
	case q.GetById() == f.subjectID && q.GetReturnMode() != knowledgev1.ReturnMode_RETURN_MODE_EDGES:
		// (1) subject node read (return mode unset → server defaults to nodes).
		return nodesResp(t2nodes(knowledgev1.Node{Id: f.subjectID, SymbolName: "Subject", Type: "step", Status: "active"}))
	case q.GetById() == f.subjectID && q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES:
		// (2) edge neighborhood.
		return edgesResp([]*knowledgev1.Edge{
			{FromId: f.subjectID, ToId: "A", Type: "relates-to"},
			{FromId: "B", ToId: f.subjectID, Type: "informed-by"},
		})
	case len(q.GetSelection().GetFromId()) > 0:
		// (3) ancestry hop — return the parent for the current node, up to 2 hops.
		from := q.GetSelection().GetFromId()[0]
		switch from {
		case f.subjectID:
			return traversalResp([]engine.TraversalResult{{Distance: 1, Node: &knowledgev1.Node{Id: "P1"}}})
		case "P1":
			return traversalResp([]engine.TraversalResult{{Distance: 1, Node: &knowledgev1.Node{Id: "P2"}}})
		default:
			return traversalResp(nil) // P2 is the root.
		}
	case len(q.GetIds()) > 0:
		// (4) bulk hydrate over the combined peer+ancestor id set.
		f.bulkCalls++
		nodes := []knowledgev1.Node{
			{Id: "A", SymbolName: "Alpha", Type: "finding"},
			{Id: "B", SymbolName: "Bravo", Type: "decision"},
			{Id: "P1", SymbolName: "Phase One", Type: "phase", Status: "active"},
			{Id: "P2", SymbolName: "Plan Two", Type: "plan", Status: "active"},
		}
		return nodesResp(nodes)
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

func t2nodes(n ...knowledgev1.Node) []knowledgev1.Node { return n }

func nodesResp(nodes []knowledgev1.Node) (*knowledgev1.ExecuteResponse, error) {
	ptrs := make([]*knowledgev1.Node, len(nodes))
	for i := range nodes {
		ptrs[i] = &nodes[i]
	}
	return enginetest.ResponseWithNodes(ptrs...), nil
}

func edgesResp(edges []*knowledgev1.Edge) (*knowledgev1.ExecuteResponse, error) {
	return &knowledgev1.ExecuteResponse{Edges: edges}, nil
}

func traversalResp(results []engine.TraversalResult) (*knowledgev1.ExecuteResponse, error) {
	if len(results) == 0 {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	return &knowledgev1.ExecuteResponse{TraversalResults: traversalResultsToProtoForTest(results)}, nil
}

// TestComposeInspectData_OneBulkHydrate asserts the composer fires exactly ONE
// bulk ids[] hydrate over the combined peer+ancestor set (no per-edge / per-hop
// N+1) and shapes the subject, edges, and ancestry correctly.
func TestComposeInspectData_OneBulkHydrate(t *testing.T) {
	f := &examineFake{subjectID: "S"}
	data, found, err := composeInspectData(context.Background(), f.exec, "S")
	require.NoError(t, err)
	require.True(t, found)

	assert.Equal(t, 1, f.bulkCalls, "exactly one bulk hydrate Execute should fire")
	assert.Equal(t, "Subject", data.Node.SymbolName)

	// Two edges, out first then in, with resolved peer type+name.
	require.Len(t, data.Edges, 2)
	assert.Equal(t, "out", data.Edges[0].Direction)
	assert.Equal(t, "A", data.Edges[0].Peer)
	assert.Equal(t, "Alpha", data.Edges[0].PeerName)
	assert.Equal(t, "finding", data.Edges[0].PeerType)
	assert.Equal(t, "in", data.Edges[1].Direction)
	assert.Equal(t, "Bravo", data.Edges[1].PeerName)

	// Two-hop ancestry, depth-tagged, resolved from the bulk hydrate.
	require.Len(t, data.Ancestry, 2)
	assert.Equal(t, "P1", data.Ancestry[0].ID)
	assert.Equal(t, 1, data.Ancestry[0].DepthAbove)
	assert.Equal(t, "phase", data.Ancestry[0].Type)
	assert.Equal(t, "P2", data.Ancestry[1].ID)
	assert.Equal(t, 2, data.Ancestry[1].DepthAbove)
}

// TestComposeInspectData_NotFound asserts found=false for a missing subject.
func TestComposeInspectData_NotFound(t *testing.T) {
	f := &examineFake{subjectID: "OTHER"}
	_, found, err := composeInspectData(context.Background(), f.exec, "S")
	require.NoError(t, err)
	assert.False(t, found)
}

// TestBuildInspectJSON_Shape asserts the json branch reproduces the server
// buildInspectJSON keys (id/name/type/status/source + ancestry + edges).
func TestBuildInspectJSON_Shape(t *testing.T) {
	data := engine.InspectData{
		Node: &knowledgev1.Node{Id: "S", SymbolName: "Subject", Type: "step", Status: "active"},
		Ancestry: []engine.InspectAncestor{
			{ID: "P1", Name: "Phase One", Type: "phase", Status: "active", DepthAbove: 1},
		},
		Edges: []engine.InspectEdge{
			{Direction: "out", Type: "relates-to", Peer: "A", PeerType: "finding", PeerName: "Alpha"},
		},
	}
	out := buildInspectJSON(data)
	assert.Equal(t, "S", out["id"])
	assert.Equal(t, "Subject", out["name"])
	assert.Equal(t, "step", out["type"])
	assert.Equal(t, "active", out["status"])

	b, err := json.Marshal(out)
	require.NoError(t, err)
	var round map[string]any
	require.NoError(t, json.Unmarshal(b, &round))
	anc, ok := round["ancestry"].([]any)
	require.True(t, ok)
	require.Len(t, anc, 1)
	edges, ok := round["edges"].([]any)
	require.True(t, ok)
	require.Len(t, edges, 1)
}

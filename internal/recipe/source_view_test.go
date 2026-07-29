// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fakeGraphCaller is a scripted foundation.GraphCaller for the recipe tests: it
// returns a fixed node set for the all-nodes browse plan and a fixed edge set
// for the RETURN_MODE_EDGES plan, counting Execute calls so the N+1-avoidance
// property (exactly 2 RPCs per source load) can be asserted. Mirrors
// foundation's wire_test fakeCaller.
type fakeGraphCaller struct {
	nodes []*knowledgev1.Node
	edges []*knowledgev1.Edge

	calls        int
	lastEdgePlan *knowledgev1.QueryPlan
	mutations    []*knowledgev1.MutationPlan
	mutationErr  error
}

func (f *fakeGraphCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.calls++
	if m := req.GetMutation(); m != nil {
		f.mutations = append(f.mutations, m)
		return &knowledgev1.ExecuteResponse{}, f.mutationErr
	}
	q := req.GetQuery()
	resp := &knowledgev1.ExecuteResponse{}
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		f.lastEdgePlan = q
		resp.Edges = f.edges
		return resp, nil
	}
	resp.Nodes = f.nodes
	return resp, nil
}

func svNode(id, typ, name string) *knowledgev1.Node {
	return &knowledgev1.Node{Id: id, Type: typ, SymbolName: name}
}

func svEdge(from, to, typ string) *knowledgev1.Edge {
	return &knowledgev1.Edge{FromId: from, ToId: to, Type: typ}
}

func TestLoadSourceView_TwoExecuteRPCs_PopulatesIndexes(t *testing.T) {
	f := &fakeGraphCaller{
		nodes: []*knowledgev1.Node{
			svNode("s1", "section", "Intro"),
			svNode("s2", "section", "Body"),
			svNode("p1", "paragraph", "para one"),
		},
		edges: []*knowledgev1.Edge{
			svEdge("s1", "p1", "contains"),
			svEdge("s2", "s1", "relates-to"),
		},
	}

	sv, err := loadSourceView(context.Background(), f, kgtypes.GraphWebRaw, "src")
	require.NoError(t, err)

	// N+1 avoidance: exactly two Execute RPCs (FetchAllNodes + FetchAllEdges).
	assert.Equal(t, 2, f.calls, "loadSourceView must issue exactly 2 Execute RPCs")

	// The edge read is the MATCH-ALL form: this view indexes every node, so it
	// asks for the graph's edges rather than listing every id as a pivot.
	require.NotNil(t, f.lastEdgePlan, "an edges plan must have been issued")
	assert.Empty(t, f.lastEdgePlan.GetIds(), "match-all plan carries no ids[] pivot")
	assert.Empty(t, f.lastEdgePlan.GetById(), "match-all plan carries no by_id pivot")
	assert.Empty(t, f.lastEdgePlan.GetSelection().GetFromId(), "match-all plan carries no from_id pivot")

	// byID indexes every node.
	assert.Len(t, sv.byID, 3)
	got, ok := sv.nodeByID("s1")
	require.True(t, ok)
	assert.Equal(t, "Intro", got.SymbolName)

	// byType groups by node type.
	assert.Len(t, sv.byType["section"], 2)
	assert.Len(t, sv.byType["paragraph"], 1)

	// outEdges / inEdges index by endpoint.
	assert.Len(t, sv.outEdges["s1"], 1)
	assert.Len(t, sv.inEdges["p1"], 1)
	assert.Len(t, sv.inEdges["s1"], 1) // s2 --relates-to--> s1
}

func TestLoadSourceView_EmptyGraph_OneRPC(t *testing.T) {
	f := &fakeGraphCaller{} // no nodes
	sv, err := loadSourceView(context.Background(), f, kgtypes.GraphWebRaw, "src")
	require.NoError(t, err)
	// With no nodes indexed there is nothing to attach edges to; the edges RPC is
	// skipped (mirrors foundation.materializeEdges' empty-node-set short-circuit).
	assert.Equal(t, 1, f.calls)
	assert.Empty(t, sv.byID)
}

func TestSourceView_ReadMethods(t *testing.T) {
	sv := &sourceView{
		byID: map[string]*knowledgev1.Node{
			"a": svNode("a", "section", "A"),
			"b": svNode("b", "section", "B"),
			"c": svNode("c", "paragraph", "C"),
		},
		byType: map[string][]*knowledgev1.Node{
			"section":   {svNode("a", "section", "A"), svNode("b", "section", "B")},
			"paragraph": {svNode("c", "paragraph", "C")},
		},
		outEdges: map[string][]*knowledgev1.Edge{
			"a": {{FromId: "a", ToId: "c", Type: "contains"}, {FromId: "a", ToId: "b", Type: "relates-to"}},
		},
		inEdges: map[string][]*knowledgev1.Edge{
			"c": {{FromId: "a", ToId: "c", Type: "contains"}},
			"b": {{FromId: "a", ToId: "b", Type: "relates-to"}},
		},
	}

	// nodesByType returns only matching-type nodes; unknown type → nil.
	assert.Len(t, sv.nodesByType("section"), 2)
	assert.Len(t, sv.nodesByType("paragraph"), 1)
	assert.Nil(t, sv.nodesByType("missing"))

	// nodeByID hit / miss.
	n, ok := sv.nodeByID("a")
	require.True(t, ok)
	assert.Equal(t, "A", n.SymbolName)
	_, ok = sv.nodeByID("zzz")
	assert.False(t, ok)

	// edgesFrom: outgoing filters by edge type → only the contains neighbor.
	assert.Equal(t, []string{"c"}, sv.edgesFrom("a", "contains", outgoingEdges))
	assert.Equal(t, []string{"b"}, sv.edgesFrom("a", "relates-to", outgoingEdges))

	// edgesFrom: incoming returns the FromId end.
	assert.Equal(t, []string{"a"}, sv.edgesFrom("c", "contains", incomingEdges))

	// edgesFrom: a node with no edges of that type → nil.
	assert.Nil(t, sv.edgesFrom("a", "nonexistent", outgoingEdges))

	// edgesFrom: both unions out (ToId) and in (FromId). For "c" with
	// "contains": out has none, in has a--contains-->c → FromId "a".
	assert.Equal(t, []string{"a"}, sv.edgesFrom("c", "contains", bothEdges))
}

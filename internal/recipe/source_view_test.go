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

	// The edge read is the BOUNDED PIVOT form: this view indexes every node, so
	// every indexed id goes back as the pivot set, paged, under an explicit
	// positive limit. The unbounded match-all plan it used to send is retired.
	require.NotNil(t, f.lastEdgePlan, "an edges plan must have been issued")
	assert.ElementsMatch(t, []string{"s1", "s2", "p1"}, f.lastEdgePlan.GetIds(),
		"the edge page pivots on every indexed id")
	assert.Positive(t, f.lastEdgePlan.GetLimit(), "the edge page carries an explicit positive limit")

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

// TestSourceView_ChildEdgesOrdered covers the whole ordering rule, one subtest
// per obligation. The two unpositioned cases are the half no realistic source
// graph exercises — both collectors always stamp a position — so without their
// own subtests a fixture that simply omitted them would satisfy the test.
func TestSourceView_ChildEdgesOrdered(t *testing.T) {
	// posEvidence renders the Evidence blob a contains edge carries.
	posEvidence := func(pos string) string { return `{"position":"` + pos + `"}` }
	edge := func(to, evidence string) *knowledgev1.Edge {
		return &knowledgev1.Edge{FromId: "sec1", ToId: to, Type: "contains", Evidence: evidence}
	}
	ids := func(edges []*knowledgev1.Edge) []string {
		out := make([]string, 0, len(edges))
		for _, e := range edges {
			out = append(out, e.ToId)
		}
		return out
	}

	t.Run("position_order", func(t *testing.T) {
		// Supplied 2,0,1 — distinct targets, so the returned order is
		// observable rather than incidental.
		sv := &sourceView{outEdges: map[string][]*knowledgev1.Edge{
			"sec1": {edge("two", posEvidence("2")), edge("zero", posEvidence("0")), edge("one", posEvidence("1"))},
		}}
		assert.Equal(t, []string{"zero", "one", "two"}, ids(sv.childEdgesOrdered("sec1", "contains")))
	})

	t.Run("unpositioned_last", func(t *testing.T) {
		sv := &sourceView{outEdges: map[string][]*knowledgev1.Edge{
			"sec1": {edge("nopos", ""), edge("one", posEvidence("1")), edge("zero", posEvidence("0"))},
		}}
		assert.Equal(t, []string{"zero", "one", "nopos"}, ids(sv.childEdgesOrdered("sec1", "contains")))
	})

	t.Run("unparseable_position", func(t *testing.T) {
		// Evidence present but the position is not an integer. It sorts with
		// the unpositioned edges, and the two of them keep the order they were
		// materialized in relative to each other.
		sv := &sourceView{outEdges: map[string][]*knowledgev1.Edge{
			"sec1": {edge("bad", posEvidence("not-a-number")), edge("empty", ""), edge("zero", posEvidence("0"))},
		}}
		assert.Equal(t, []string{"zero", "bad", "empty"}, ids(sv.childEdgesOrdered("sec1", "contains")))
	})

	t.Run("edge_type_filtered", func(t *testing.T) {
		// A known-negative for the filter: a differently-typed edge with a
		// winning position must not appear at all.
		other := &knowledgev1.Edge{FromId: "sec1", ToId: "ref", Type: "references", Evidence: posEvidence("0")}
		sv := &sourceView{outEdges: map[string][]*knowledgev1.Edge{
			"sec1": {other, edge("one", posEvidence("1"))},
		}}
		assert.Equal(t, []string{"one"}, ids(sv.childEdgesOrdered("sec1", "contains")))
	})
}

// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// community_read_shape_test.go — pins BuildAdjacency's bulk edge read. With no
// NodeFilter the surviving set IS the whole graph, so the read takes the
// match-all form; a filtered build keeps the pivot form. Both must build the
// same adjacency for the surviving nodes, because `link` drops an edge unless
// BOTH endpoints survived.

// adjRecordingCaller wraps graphFixture and captures every RETURN_MODE_EDGES
// plan, so a test can assert the read shape as well as the adjacency.
type adjRecordingCaller struct {
	*graphFixture
	edgePlans []*knowledgev1.QueryPlan
}

func (r *adjRecordingCaller) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if q := req.GetQuery(); q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		r.edgePlans = append(r.edgePlans, q)
	}
	return r.graphFixture.Execute(ctx, req)
}

// triangleFixture: a-b-c mutually linked plus an isolated d.
func triangleFixture() *graphFixture {
	f := newGraphFixture()
	for _, id := range []string{"a", "b", "c", "d"} {
		f.AddNode(id, kgtypes.NodeType("function"))
	}
	f.AddEdge("a", "b", kgtypes.EdgeCalls)
	f.AddEdge("b", "c", kgtypes.EdgeCalls)
	f.AddEdge("c", "a", kgtypes.EdgeCalls)
	return f
}

func TestBuildAdjacency_EdgeReadShape(t *testing.T) {
	t.Run("no NodeFilter pivots on every node id under an explicit limit", func(t *testing.T) {
		rc := &adjRecordingCaller{graphFixture: triangleFixture()}
		ids, adj, err := BuildAdjacency(context.Background(), rc, kgtypes.GraphCode, "repo", BuildAdjacencyOpts{})
		require.NoError(t, err)
		require.Len(t, rc.edgePlans, 1, "the fixture fits a single page")
		assert.ElementsMatch(t, []string{"a", "b", "c", "d"}, rc.edgePlans[0].GetIds(),
			"a whole-graph build pivots on every node id rather than sending no pivot at all")
		assert.Positive(t, rc.edgePlans[0].GetLimit(), "every page must carry an explicit positive limit")
		assert.ElementsMatch(t, []string{"a", "b", "c", "d"}, ids)
		assert.ElementsMatch(t, []string{"b", "c"}, adj["a"], "undirected neighbors of a")
		assert.Empty(t, adj["d"], "the isolated node has no neighbors")
	})

	t.Run("NodeFilter keeps the pivot plan", func(t *testing.T) {
		rc := &adjRecordingCaller{graphFixture: triangleFixture()}
		_, _, err := BuildAdjacency(context.Background(), rc, kgtypes.GraphCode, "repo", BuildAdjacencyOpts{
			NodeFilter: func(n *knowledgev1.Node) bool { return n.GetId() != "d" },
		})
		require.NoError(t, err)
		require.Len(t, rc.edgePlans, 1)
		assert.ElementsMatch(t, []string{"a", "b", "c"}, rc.edgePlans[0].GetIds(),
			"a filtered build pivots on exactly the surviving ids")
	})
}

// TestBuildAdjacency_FilteredAdjacencyUnchangedByReadShape is the equivalence
// check behind the split: a filtered build fed EVERY edge (what a match-all read
// would hand back) produces the same adjacency as the pivot read, because `link`
// requires both endpoints to be in the surviving set.
func TestBuildAdjacency_FilteredAdjacencyUnchangedByReadShape(t *testing.T) {
	filter := func(n *knowledgev1.Node) bool { return n.GetId() != "d" }

	_, pivotAdj, err := BuildAdjacency(context.Background(), triangleFixture(),
		kgtypes.GraphCode, "repo", BuildAdjacencyOpts{NodeFilter: filter})
	require.NoError(t, err)

	_, allAdj, err := BuildAdjacency(context.Background(), &adjAllEdgesCaller{graphFixture: triangleFixture()},
		kgtypes.GraphCode, "repo", BuildAdjacencyOpts{NodeFilter: filter})
	require.NoError(t, err)

	assert.Equal(t, pivotAdj, allAdj,
		"link drops an edge unless both endpoints survived, so the read shape cannot change the adjacency")
}

// adjAllEdgesCaller answers every RETURN_MODE_EDGES plan with the whole edge
// set, ignoring any pivot — standing in for the broadest match-all response.
type adjAllEdgesCaller struct{ *graphFixture }

func (a *adjAllEdgesCaller) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if q := req.GetQuery(); q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		return &knowledgev1.ExecuteResponse{Edges: a.graphFixture.edgesFor(nil, q.GetSelection().GetEdgeTypes())}, nil
	}
	return a.graphFixture.Execute(ctx, req)
}

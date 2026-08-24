// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// allTypesFake serves the two reads the scope="all_types" composition makes: the
// keyset node browse over EVERY type, and the edge read. It records the edge read's
// requested type filter so a test can assert on the WIRE CALL rather than inferring
// the path from the result.
type allTypesFake struct {
	nodes     []*knowledgev1.Node
	edges     []*knowledgev1.Edge
	edgeCalls [][]string
}

func (f *allTypesFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		types := q.GetSelection().GetEdgeTypes()
		f.edgeCalls = append(f.edgeCalls, append([]string(nil), types...))
		want := map[string]bool{}
		for _, et := range types {
			want[et] = true
		}
		var out []*knowledgev1.Edge
		for _, e := range f.edges {
			if len(want) == 0 || want[e.GetType()] {
				out = append(out, e)
			}
		}
		return &knowledgev1.ExecuteResponse{Edges: bandNarrow(out, q)}, nil
	}
	if q.GetAfterId() != "" {
		return &knowledgev1.ExecuteResponse{}, nil // one short page terminates the drain.
	}
	return &knowledgev1.ExecuteResponse{Nodes: f.nodes}, nil
}

// TestFetchAdjacency_AllTypesNotNarrowedByMemo guards a LIVE PRODUCTION SURFACE that
// had no end-to-end coverage: thoughts(adjacency, scope:"all_types") is advertised as
// the cross-type bulk read and reaches this composition. The unified pivot-edge memo
// carries only the 7 types in unifiedPivotEdgeTypes, so wiring all_types to it would
// silently narrow an every-type read — and nothing else in the suite would notice.
//
// PASSING A *passReads AS src IS THE WHOLE POINT. The memo is what would do the
// narrowing, so a version of this test with a nil src could not detect it: with no
// memo present there is nothing to narrow and the test would pass either way.
func TestFetchAdjacency_AllTypesNotNarrowedByMemo(t *testing.T) {
	ctx := context.Background()
	const thoughtID, findingID = "th-1", "fnd-1"

	// EdgeSupports sits OUTSIDE unifiedPivotEdgeTypes, so it is exactly the edge a
	// memo-narrowed read would lose.
	gc := &allTypesFake{
		nodes: []*knowledgev1.Node{
			{Id: thoughtID, Type: string(kgtypes.NodeThought)},
			{Id: findingID, Type: string(kgtypes.NodeFinding)},
		},
		edges: []*knowledgev1.Edge{
			{FromId: thoughtID, ToId: findingID, Type: string(kgtypes.EdgeSupports)},
		},
	}

	nodeIDs, adj, err := fetchAdjacency(ctx, gc, "all_types", nil, newPassReads(nil))
	require.NoError(t, err)
	require.ElementsMatch(t, []string{thoughtID, findingID}, nodeIDs,
		"all_types keeps both node types")

	assert.Contains(t, adj[thoughtID], findingID,
		"an edge whose type is OUTSIDE unifiedPivotEdgeTypes must survive the all_types read")
	assert.Contains(t, adj[findingID], thoughtID,
		"and bidirectionally, as buildAdjacencyFromEdges projects it")

	// The direct wire evidence: all_types asks for NO type filter. A 7-type request
	// here would mean the memo path had captured this read.
	require.Len(t, gc.edgeCalls, 1, "all_types issues exactly one edge read")
	assert.Empty(t, gc.edgeCalls[0],
		"all_types reads with a NIL type filter — every type — not the 7-type unified set")
}

// TestAllTypesAdjacency_ExcludesAgentSkillHubs is the structural guard for the
// load-bearing invariant: origin/agent + skill edges must NEVER enter the
// clustering adjacency — they would reinforce genre/instruction clustering, the
// exact contamination the developer-origin facet must not leak into clustering.
//
// It binds to the SAME predicate production uses (keepInAllTypesIDSet, called by
// fetchAdjacencyNodeIDs' all_types branch) and the SAME pure projection
// (buildAdjacencyFromEdges) — a test asserting a re-implemented copy of the drop
// logic would be worthless.
//
// INVARIANT (asserted by construction, not code here): runClusterDetection
// (scope=all) and fetchTensionEdges take their idSets from
// fetchAllThoughtNodes, which never drains agent/skill nodes, so they are
// thoughts-only and unaffected by the all_types drop — only the all_types path
// ever drained agent nodes in, and this is the path the predicate guards.
func TestAllTypesAdjacency_ExcludesAgentSkillHubs(t *testing.T) {
	// PRIMARY: feed the all_types id-set drop predicate a node slice of
	// {thought, agent, skill, proxy} and assert ONLY the thought survives.
	nodes := []*knowledgev1.Node{
		{Id: "th-1", Type: string(kgtypes.NodeThought)},
		{Id: "agent-planner", Type: string(kgtypes.NodeAgent)},
		{Id: "skill-implement", Type: string(kgtypes.NodeSkill)},
		{Id: "proxy-x", Type: string(kgtypes.NodeProxy)},
	}
	var survived []string
	for _, n := range nodes {
		if keepInAllTypesIDSet(kgtypes.NodeType(n.Type)) {
			survived = append(survived, n.Id)
		}
	}
	assert.Equal(t, []string{"th-1"}, survived,
		"all_types idSet must keep only the thought; agent/skill/proxy hubs are dropped")

	// Build the surviving thoughts-only idSet (what fetchAdjacencyNodeIDs would
	// produce after the drop) and add a second thought for the positive control.
	idSet := map[string]bool{}
	for _, id := range survived {
		idSet[id] = true
	}
	idSet["th-2"] = true

	// Edge set mixes an agent--produced-->thought hub edge (must be dropped, the
	// agent endpoint is out of idSet) and a thought<->thought relates-to edge
	// (positive control, must survive).
	edges := []knowledgev1.Edge{
		{FromId: "agent-planner", ToId: "th-1", Type: string(kgtypes.EdgeProduced)},
		{FromId: "th-1", ToId: "th-2", Type: string(kgtypes.EdgeRelatesTo)},
	}
	// nil keepTypes: this is the all_types path, which reads every edge type. It is
	// also what exercises the keep-everything branch of the new type predicate.
	adj := buildAdjacencyFromEdges(edges, idSet, nil)

	// The agent hub is excluded entirely.
	assert.NotContains(t, adj, "agent-planner",
		"the out-of-idSet agent endpoint must not appear as an adjacency key")
	assert.NotContains(t, adj["th-1"], "agent-planner",
		"th-1 must not gain the agent node as a neighbor (origin edge dropped structurally)")

	// SECONDARY (positive control): the thought<->thought relates-to edge IS
	// present in both directions, so the exclusion is specific to the out-of-idSet
	// agent endpoint, not a blanket drop of th-1's neighbors.
	assert.Contains(t, adj["th-1"], "th-2", "the thought<->thought edge must survive")
	assert.Contains(t, adj["th-2"], "th-1", "the thought<->thought edge is bidirectional")
}

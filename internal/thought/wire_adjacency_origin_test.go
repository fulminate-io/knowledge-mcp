// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"testing"

	"github.com/stretchr/testify/assert"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

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
	adj := buildAdjacencyFromEdges(edges, idSet)

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

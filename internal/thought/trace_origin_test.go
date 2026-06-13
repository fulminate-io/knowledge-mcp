// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// originTraceFakeCaller serves exactly the two reads TraceThoughts issues against
// a thought that carries an origin hub edge: (1) an INBOUND traverse filtered to
// EdgeProduced from the thought returns the originating agent node; every other
// traverse returns empty; (2) a by-ids node query hydrates from a node map. This
// is deliberately gated on edge_type=="produced" + inbound direction so that if a
// future change drops EdgeProduced from thoughtEdgeTypes, expandTraceNeighbors is
// never called with et=produced, the agent is never surfaced, and the test goes
// RED (the fails-when-absent property the criterion requires).
type originTraceFakeCaller struct {
	thoughtID string
	agent     *knowledgev1.Node
}

func (c *originTraceFakeCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	switch q.GetReturnMode() {
	case knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL:
		// Only an INBOUND traverse over the produced edge from the thought
		// surfaces the agent hub. Forward is set+false for direction "in".
		sel := q.GetSelection()
		inbound := q.Forward != nil && !q.GetForward()
		if !inbound || !slices.Contains(sel.GetEdgeTypes(), string(kgtypes.EdgeProduced)) {
			return &knowledgev1.ExecuteResponse{}, nil
		}
		if !slices.Contains(sel.GetFromId(), c.thoughtID) {
			return &knowledgev1.ExecuteResponse{}, nil
		}
		return &knowledgev1.ExecuteResponse{
			TraversalResults: []*knowledgev1.TraversalResult{{Node: c.agent, Distance: 1}},
		}, nil
	default:
		// By-ids hydration: the ids[] bulk query lowers to QueryPlan.Ids.
		ids := q.GetIds()
		var nodes []*knowledgev1.Node
		for _, id := range ids {
			if id == c.agent.GetId() {
				nodes = append(nodes, c.agent)
			}
		}
		return &knowledgev1.ExecuteResponse{Nodes: nodes}, nil
	}
}

// TestTraceThoughts_SurfacesOriginAgentHub asserts a thought carrying an origin
// hub edge surfaces the originating agent node over an EdgeProduced step when
// traced — provenance lineage that is intentionally NOT filtered. Fails-when-
// absent: drop EdgeProduced from thoughtEdgeTypes (or add an agent-endpoint
// filter) and the agent step disappears, RED.
func TestTraceThoughts_SurfacesOriginAgentHub(t *testing.T) {
	const thoughtID = "th-planner"
	agent := &knowledgev1.Node{Id: "agent-planner", Type: string(kgtypes.NodeAgent), SymbolName: "planner"}
	fc := &originTraceFakeCaller{thoughtID: thoughtID, agent: agent}

	steps, err := TraceThoughts(context.Background(), fc, thoughtID, "both", 5, false, false)
	require.NoError(t, err)

	var found *TraceStep
	for i := range steps {
		if steps[i].Node.GetId() == agent.Id {
			found = &steps[i]
			break
		}
	}
	require.NotNil(t, found, "trace must surface the originating agent node over the produced hub edge")
	assert.Equal(t, kgtypes.EdgeProduced, found.EdgeType, "the agent hub is reached over an EdgeProduced edge")
	assert.Equal(t, string(kgtypes.NodeAgent), found.Node.GetType())
}

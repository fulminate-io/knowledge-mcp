// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestInterceptMutate_StatusRollup_OneTraverse_OneUpdate asserts that completing
// a container fires exactly the bounded carrier sequence: one ByID lookup
// Execute, one traversal Execute (descendants), one UPDATE Mutation Execute —
// regardless of descendant count (T-GTB3 Phase 6 carrier path).
func TestInterceptMutate_StatusRollup_OneTraverse_OneUpdate(t *testing.T) {
	fc := &fakeRollupGraphCaller{
		rootNode: knowledgev1.Node{Id: "plan-1", Type: string(kgtypes.NodePlan)},
		descendants: []knowledgev1.Node{
			{Id: "phase-1", Type: string(kgtypes.NodePhase), Status: "pending"},
			{Id: "step-1", Type: string(kgtypes.NodeStep), Status: "pending"},
			{Id: "step-2", Type: string(kgtypes.NodeStep), Status: "pending"},
		},
	}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","id":"plan-1","status":"completed"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "rollup should succeed: %s", toolResultText(res))
	assert.Equal(t, 1, fc.traversalExecutes, "exactly 1 traversal Execute to collect descendants")
	assert.Equal(t, 1, fc.updateExecutes, "exactly 1 UPDATE Mutation Execute regardless of descendant count")
}

// TestInterceptMutate_StatusRollup_TerminalDescendants_Skipped asserts the
// UPDATE Mutation's Selection.Ids excludes failed + completed descendants.
func TestInterceptMutate_StatusRollup_TerminalDescendants_Skipped(t *testing.T) {
	fc := &fakeRollupGraphCaller{
		rootNode: knowledgev1.Node{Id: "plan-1", Type: string(kgtypes.NodePlan)},
		descendants: []knowledgev1.Node{
			{Id: "step-1", Type: string(kgtypes.NodeStep), Status: "pending"},
			{Id: "step-2", Type: string(kgtypes.NodeStep), Status: "failed"},
			{Id: "step-3", Type: string(kgtypes.NodeStep), Status: "completed"},
		},
	}
	deps := interceptTestDeps{gc: fc}
	_, res := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","id":"plan-1","status":"completed"}`),
	})
	require.False(t, res.IsError)
	require.NotNil(t, fc.lastUpdate, "an UPDATE Mutation must have fired")
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPDATE, fc.lastUpdate.GetKind())
	ids := fc.lastUpdate.GetSelection().GetIds()
	// Root + step-1 only; step-2 (failed) + step-3 (completed) skipped.
	require.Len(t, ids, 2)
	assert.Contains(t, ids, "plan-1")
	assert.Contains(t, ids, "step-1")
}

// fakeRollupGraphCaller answers the carrier sequence the rollup drives: a ByID
// lookup (root), a RETURN_MODE_TRAVERSAL (descendants), and an UPDATE Mutation.
type fakeRollupGraphCaller struct {
	rootNode    knowledgev1.Node
	descendants []knowledgev1.Node

	traversalExecutes int
	updateExecutes    int
	lastUpdate        *knowledgev1.MutationPlan
}

// Call satisfies the interface; the rollup flow's reads/writes all ride the
// Execute carrier seam now (lookupNodeBackend → render.FetchNode, the
// descendants traversal, and the UPDATE mutation).
func (f *fakeRollupGraphCaller) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.ToolResult{}, nil
}

func (f *fakeRollupGraphCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if m := req.GetMutation(); m != nil {
		f.updateExecutes++
		f.lastUpdate = m
		return &knowledgev1.ExecuteResponse{AffectedCount: int64(len(m.GetSelection().GetIds()))}, nil
	}
	q := req.GetQuery()
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL {
		f.traversalExecutes++
		results := make([]engine.TraversalResult, len(f.descendants))
		for i := range f.descendants {
			results[i] = engine.TraversalResult{Distance: 1, Node: &f.descendants[i]}
		}
		return &knowledgev1.ExecuteResponse{TraversalResults: traversalResultsToProtoForTest(results)}, nil
	}
	// ByID root lookup (lookupNodeBackend → render.FetchNode): answer the seeded
	// root node via the nodes_json carrier.
	if q.GetById() == f.rootNode.Id {
		resp := enginetest.ResponseWithNodes([]*knowledgev1.Node{&f.rootNode}...)
		return resp, nil
	}
	// Any other Execute → empty (not-found).
	return &knowledgev1.ExecuteResponse{}, nil
}

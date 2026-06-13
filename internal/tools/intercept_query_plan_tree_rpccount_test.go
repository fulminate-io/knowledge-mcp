// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// countingPlanTreeCaller is a fixture-backed GraphCaller that counts Execute
// calls and answers the three query shapes the plan_tree intercept emits:
//
//   - ById node query (RETURN_MODE_NODES default) → the bare node carrier
//     (root FetchNode).
//   - RETURN_MODE_TRAVERSAL (Selection.FromId) → the whole descendant set plus
//     the subtree's contains edges via traversal_edges.
//   - RETURN_MODE_EDGES with node-SET Ids → the depends-on edges among the id
//     set (the text path's batched depends-on fetch).
//
// It serves a flat plan→phase→step fixture; the point of the test is that the
// Execute count stays a small constant regardless of how many nodes the fixture
// holds.
type countingPlanTreeCaller struct {
	nodes        map[string]*knowledgev1.Node
	containsFrom map[string][]string // parent → ordered child ids
	dependsOn    map[string]string   // child → depends-on target
	execCalls    int
	dependsOnErr error // when set, the RETURN_MODE_EDGES (depends-on) Execute fails
}

func (c *countingPlanTreeCaller) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.TextResult(""), nil
}

func (c *countingPlanTreeCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	c.execCalls++
	q := req.GetQuery()

	switch q.GetReturnMode() {
	case knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL:
		root := q.GetSelection().GetFromId()[0]
		var results []engine.TraversalResult
		var edges []knowledgev1.Edge
		c.walkSubtree(root, 1, &results, &edges)
		return &knowledgev1.ExecuteResponse{
			TraversalResults: traversalResultsToProtoForTest(results),
			TraversalEdges:   edgePtrsForTest(edges),
		}, nil
	case knowledgev1.ReturnMode_RETURN_MODE_EDGES:
		if c.dependsOnErr != nil {
			return nil, c.dependsOnErr
		}
		// node-SET depends-on fetch: emit a depends-on edge for each seeded id.
		var out []*knowledgev1.Edge
		for _, id := range q.GetIds() {
			if tgt, ok := c.dependsOn[id]; ok {
				out = append(out, &knowledgev1.Edge{
					FromId: id, ToId: tgt, Type: string(kgtypes.EdgeDependsOn),
				})
			}
		}
		return &knowledgev1.ExecuteResponse{Edges: out}, nil
	default:
		// ById node fetch (root).
		if n, ok := c.nodes[q.GetById()]; ok {
			return enginetest.ResponseWithNode(n), nil
		}
		return &knowledgev1.ExecuteResponse{}, nil
	}
}

// walkSubtree appends every descendant (root excluded) and its contains edge.
func (c *countingPlanTreeCaller) walkSubtree(parent string, dist int, results *[]engine.TraversalResult, edges *[]knowledgev1.Edge) {
	for _, child := range c.containsFrom[parent] {
		*edges = append(*edges, knowledgev1.Edge{
			FromId: parent, ToId: child, Type: string(kgtypes.EdgeKGContains),
		})
		if n, ok := c.nodes[child]; ok {
			*results = append(*results, engine.TraversalResult{Node: n, Distance: dist})
		}
		c.walkSubtree(child, dist+1, results, edges)
	}
}

// seedWidePlanTree builds a plan with `phases` phases, each holding `steps`
// steps — a (1 + phases + phases*steps)-node fixture.
func seedWidePlanTree(phases, steps int) (*countingPlanTreeCaller, string) {
	c := &countingPlanTreeCaller{
		nodes:        map[string]*knowledgev1.Node{},
		containsFrom: map[string][]string{},
		dependsOn:    map[string]string{},
	}
	rootID := "plan-root"
	c.nodes[rootID] = &knowledgev1.Node{Id: rootID, Type: string(kgtypes.NodePlan), SymbolName: "plan", Status: "active"}
	for p := range phases {
		phaseID := fmt.Sprintf("phase-%d", p)
		c.nodes[phaseID] = &knowledgev1.Node{Id: phaseID, Type: string(kgtypes.NodePhase), SymbolName: phaseID, Status: "pending"}
		c.containsFrom[rootID] = append(c.containsFrom[rootID], phaseID)
		for s := range steps {
			stepID := fmt.Sprintf("step-%d-%d", p, s)
			c.nodes[stepID] = &knowledgev1.Node{Id: stepID, Type: string(kgtypes.NodeStep), SymbolName: stepID, Status: "pending"}
			c.containsFrom[phaseID] = append(c.containsFrom[phaseID], stepID)
		}
	}
	return c, rootID
}

// TestInterceptQueryPlanTree_JSON_RPCCountConstant asserts the json path issues
// exactly 2 Execute calls (root fetch + subtree traversal) regardless of node
// count.
func TestInterceptQueryPlanTree_JSON_RPCCountConstant(t *testing.T) {
	for _, dim := range []struct{ phases, steps int }{{4, 5}, {8, 10}} {
		c, rootID := seedWidePlanTree(dim.phases, dim.steps)
		deps := &parityDeps{gc: c}
		args, err := json.Marshal(map[string]any{"mode": "plan_tree", "id": rootID, "format": "json"})
		require.NoError(t, err)

		handled, res := InterceptQueryPlanTree(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
		require.True(t, handled)
		require.False(t, res.IsError, "intercept error: %v", res.Content)

		assert.Equal(t, 2, c.execCalls,
			"json path must be exactly 2 Execute (root fetch + subtree traversal), got %d for %dx%d", c.execCalls, dim.phases, dim.steps)
	}
}

// TestInterceptQueryPlanTree_Text_RPCCountConstant asserts the text path issues
// exactly 3 Execute calls (root fetch + subtree traversal + batched depends-on)
// regardless of node count.
func TestInterceptQueryPlanTree_Text_RPCCountConstant(t *testing.T) {
	for _, dim := range []struct{ phases, steps int }{{4, 5}, {8, 10}} {
		c, rootID := seedWidePlanTree(dim.phases, dim.steps)
		deps := &parityDeps{gc: c}
		args, err := json.Marshal(map[string]any{"mode": "plan_tree", "id": rootID})
		require.NoError(t, err)

		handled, res := InterceptQueryPlanTree(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
		require.True(t, handled)
		require.False(t, res.IsError, "intercept error: %v", res.Content)

		assert.Equal(t, 3, c.execCalls,
			"text path must be exactly 3 Execute (root + traversal + depends-on), got %d for %dx%d", c.execCalls, dim.phases, dim.steps)
	}
}

// TestInterceptQueryPlanTree_Text_DependsOnFetchError_StillRenders asserts the
// text path degrades gracefully: when the batched depends-on fetch errors, the
// tree still renders (in structure-edge order) rather than erroring the whole
// call — preserving the per-node firstDependsOn error tolerance.
func TestInterceptQueryPlanTree_Text_DependsOnFetchError_StillRenders(t *testing.T) {
	c, rootID := seedWidePlanTree(2, 2)
	c.dependsOnErr = fmt.Errorf("simulated depends-on fetch failure")
	deps := &parityDeps{gc: c}
	args, err := json.Marshal(map[string]any{"mode": "plan_tree", "id": rootID})
	require.NoError(t, err)

	handled, res := InterceptQueryPlanTree(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError, "depends-on fetch error must NOT error the whole render: %v", res.Content)

	out := extractText(res)
	assert.Contains(t, out, "ID: phase-0", "the tree still renders despite the depends-on fetch failure")
	assert.Contains(t, out, "ID: step-0-0", "descendants still render in structure-edge order")
}

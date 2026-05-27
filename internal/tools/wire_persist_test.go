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
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// TestPersistBatch_OneRPC asserts a single create_batch Mutation Execute (the
// carrier path) regardless of node/edge count, and that the created IDs come
// back from resp.GetIds().
func TestPersistBatch_OneRPC(t *testing.T) {
	fc := &fakeGraphCaller{mutateIDs: []string{"a", "b", "c"}}
	nodes := []*knowledgev1.Node{
		{Type: string(kgtypes.NodePlan), SymbolName: "p"},
		{Type: string(kgtypes.NodePhase), SymbolName: "ph1"},
		{Type: string(kgtypes.NodeStep), SymbolName: "s1"},
	}
	edges := []kgwire.BatchEdge{
		{FromIdx: 0, ToIdx: 1, Type: kgtypes.EdgeKGContains},
		{FromIdx: 1, ToIdx: 2, Type: kgtypes.EdgeKGContains},
	}
	ids, err := PersistBatch(context.Background(), fc, nodes, edges, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, ids)
	require.Len(t, fc.execMutations, 1, "PersistBatch must issue exactly one Mutation Execute")
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, fc.execMutations[0].GetKind())
	require.Len(t, fc.execMutations[0].GetNodeBodies(), 3, "all three node bodies ride the create_batch plan")
}

// TestUpdateBatchStatus_OneUpdate asserts UpdateBatchStatus issues a single
// uniform UPDATE Mutation Execute over Selection.Ids with set_fields{status}.
func TestUpdateBatchStatus_OneUpdate(t *testing.T) {
	fc := &fakeGraphCaller{}
	ids := []string{"id-1", "id-2", "id-3", "id-4", "id-5"}
	err := UpdateBatchStatus(context.Background(), fc, ids, "completed", "bundle-x")
	require.NoError(t, err)
	require.Len(t, fc.execMutations, 1, "UpdateBatchStatus must issue exactly one Mutation Execute")
	m := fc.execMutations[0]
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPDATE, m.GetKind())
	assert.Equal(t, ids, m.GetSelection().GetIds())
	assert.Equal(t, "completed", m.GetSetFields()["status"])
}

// TestUpdateBatchStatus_EmptyIDs_NoCall asserts no Execute is issued for an
// empty id list.
func TestUpdateBatchStatus_EmptyIDs_NoCall(t *testing.T) {
	fc := &fakeGraphCaller{}
	err := UpdateBatchStatus(context.Background(), fc, nil, "completed", "")
	require.NoError(t, err)
	assert.Empty(t, fc.execMutations)
}

// TestLinkOne_FiresLinkMutation asserts LinkOne issues one LINK Mutation Execute
// with the from→to edge.
func TestLinkOne_FiresLinkMutation(t *testing.T) {
	fc := &fakeGraphCaller{}
	err := LinkOne(context.Background(), fc, "from-id", "to-id", kgtypes.EdgeInformedBy)
	require.NoError(t, err)
	require.Len(t, fc.execMutations, 1)
	m := fc.execMutations[0]
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_LINK, m.GetKind())
	assert.Equal(t, []string{"from-id"}, m.GetSelection().GetIds())
	assert.Equal(t, "to-id", m.GetEdgeSpec().GetToId())
	assert.Equal(t, string(kgtypes.EdgeInformedBy), m.GetEdgeSpec().GetRelationship())
}

// TestLookupNode_DelegatesToFetchNode asserts LookupNode goes through
// render.FetchNode (now the Execute carrier path) and decodes the node.
func TestLookupNode_DelegatesToFetchNode(t *testing.T) {
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"node-1": nodeResultJSON(t, "node-1", "plan", map[string]string{"foo": "bar"}),
		},
	}
	node, err := LookupNode(context.Background(), fc, "node-1")
	require.NoError(t, err)
	assert.Equal(t, "node-1", node.Id)
	assert.Equal(t, "plan", node.Type)
}

// TestTraverseDescendants_OneRPC_FiltersRoot asserts TraverseDescendants issues
// one RETURN_MODE_TRAVERSAL Execute and filters the rootID from the result.
func TestTraverseDescendants_OneRPC_FiltersRoot(t *testing.T) {
	fc := &fakeGraphCallerWithTraverse{
		results: []engine.TraversalResult{
			{Distance: 0, Node: &knowledgev1.Node{Id: "root", Type: string(kgtypes.NodePlan)}},
			{Distance: 1, Node: &knowledgev1.Node{Id: "child-1", Type: string(kgtypes.NodePhase), Status: "pending"}},
			{Distance: 1, Node: &knowledgev1.Node{Id: "child-2", Type: string(kgtypes.NodeStep), Status: "pending"}},
		},
	}
	nodes, err := TraverseDescendants(context.Background(), fc, "root", kgtypes.EdgeKGContains, 16)
	require.NoError(t, err)
	assert.Equal(t, 1, fc.execCalls, "exactly one traversal Execute")
	require.Len(t, nodes, 2, "rootID must be filtered out")
	assert.Equal(t, "child-1", nodes[0].Id)
	assert.Equal(t, "child-2", nodes[1].Id)
}

// fakeGraphCallerWithTraverse answers a RETURN_MODE_TRAVERSAL Execute with a
// seeded traversal_results_json carrier.
type fakeGraphCallerWithTraverse struct {
	results   []engine.TraversalResult
	execErr   error
	execCalls int
}

func (f *fakeGraphCallerWithTraverse) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.TextResult(""), nil
}

func (f *fakeGraphCallerWithTraverse) Execute(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.execCalls++
	if f.execErr != nil {
		return nil, f.execErr
	}
	return &knowledgev1.ExecuteResponse{TraversalResults: traversalResultsToProtoForTest(f.results)}, nil
}

// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
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

// TestPersistBatch_EdgeMetadataSurvivesProjection is the durable guard against
// the persistBatchEdge projection silently dropping edge metadata (the bug that
// would have lost the born-link Method=code-ref tag). A BatchEdge with all five
// metadata fields set, run through PersistBatch's marshal + engine.Compile,
// yields a lowered MutationPlan whose GetEdges()[0] carries every field; an
// all-unset edge marshals with NONE of the metadata keys (omitempty), so existing
// callers stay byte-identical.
func TestPersistBatch_EdgeMetadataSurvivesProjection(t *testing.T) {
	lastVal := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

	t.Run("all fields survive to the decoded create_batch edge", func(t *testing.T) {
		fc := &fakeGraphCaller{mutateIDs: []string{"n0"}}
		nodes := []*knowledgev1.Node{{Type: string(kgtypes.NodeThought), SymbolName: "t"}}
		edges := []kgwire.BatchEdge{{
			FromIdx:       0,
			ToIdx:         -1,
			ToID:          "proxy:knowledge:tools/wire.go:PersistBatch",
			Type:          kgtypes.EdgeRelatesTo,
			Weight:        2.5,
			Confidence:    0.75,
			Method:        "code-ref",
			Evidence:      "cited in content",
			LastValidated: lastVal,
		}}
		_, err := PersistBatch(context.Background(), fc, nodes, edges, "")
		require.NoError(t, err)
		require.Len(t, fc.execMutations, 1)
		specs := fc.execMutations[0].GetEdges()
		require.Len(t, specs, 1)
		e := specs[0]
		assert.Equal(t, "code-ref", e.GetMethod(), "Method must survive the projection")
		assert.InDelta(t, 2.5, e.GetWeight(), 1e-9)
		assert.InDelta(t, 0.75, e.GetConfidence(), 1e-9)
		assert.Equal(t, "cited in content", e.GetEvidence())
		assert.Equal(t, lastVal.UnixNano(), e.GetLastValidated(),
			"last_validated RFC3339 string round-trips to unix-nanos on the spec")
	})

	t.Run("all-unset edge marshals with no metadata keys (omitempty)", func(t *testing.T) {
		// Marshal the projection envelope directly and assert the metadata keys are
		// absent from the wire bytes — the omitempty guarantee that keeps existing
		// PersistBatch callers (e.g. the Method-less origin EdgeProduced) byte-identical.
		we := persistBatchEdge{FromIdx: 0, ToIdx: 1, Type: string(kgtypes.EdgeKGContains)}
		b, err := json.Marshal(we)
		require.NoError(t, err)
		s := string(b)
		for _, key := range []string{"weight", "confidence", "method", "evidence", "last_validated"} {
			assert.NotContainsf(t, s, key, "all-unset edge must omit %q (omitempty)", key)
		}
	})
}

// TestPersistBatch_CallerSuppliedIDSurvives is the durable guard against the
// persistBatchNode projection dropping a caller-supplied node id. It asserts on
// the DECODED create_batch NodeBody — post marshal + engine.Compile — because a
// field carrying the wrong json tag round-trips to an empty Id there while an
// in-memory struct assertion would still pass. The second leg is the omitempty
// guarantee: a node with an empty Id marshals with no "id" key at all, which is
// what keeps every existing PersistBatch caller byte-identical.
func TestPersistBatch_CallerSuppliedIDSurvives(t *testing.T) {
	t.Run("a caller-supplied id reaches the decoded node body", func(t *testing.T) {
		fc := &fakeGraphCaller{mutateIDs: []string{"caller-supplied-id"}}
		nodes := []*knowledgev1.Node{{
			Id:         "caller-supplied-id",
			Type:       string(kgtypes.NodeDocument),
			SymbolName: "doc",
		}}
		_, err := PersistBatch(context.Background(), fc, nodes, nil, "")
		require.NoError(t, err)
		require.Len(t, fc.execMutations, 1)
		bodies := fc.execMutations[0].GetNodeBodies()
		require.Len(t, bodies, 1)
		assert.Equal(t, "caller-supplied-id", bodies[0].GetId(),
			"the id must survive marshal + Compile onto the create_batch node body")
	})

	t.Run("an empty id marshals with no id key (omitempty)", func(t *testing.T) {
		wn := persistBatchNode{Type: string(kgtypes.NodeThought), Name: "t"}
		b, err := json.Marshal(wn)
		require.NoError(t, err)
		assert.NotContains(t, string(b), `"id"`,
			"an unset id must omit the key so existing callers stay byte-identical")
	})
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
	nodes, err := render.TraverseDescendants(context.Background(), fc, "root", kgtypes.EdgeKGContains, 16)
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

// fakeTraverseEdgesCaller answers a RETURN_MODE_TRAVERSAL Execute with seeded
// traversal_results AND a seeded traversal_edges carrier, and captures the last
// QueryPlan so the test can assert its shape.
type fakeTraverseEdgesCaller struct {
	results   []engine.TraversalResult
	edges     []knowledgev1.Edge
	truncated bool
	execCalls int
	lastPlan  *knowledgev1.QueryPlan
}

func (f *fakeTraverseEdgesCaller) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.TextResult(""), nil
}

func (f *fakeTraverseEdgesCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.execCalls++
	f.lastPlan = req.GetQuery()
	return &knowledgev1.ExecuteResponse{
		TraversalResults: traversalResultsToProtoForTest(f.results),
		TraversalEdges:   edgePtrsForTest(f.edges),
		Truncated:        f.truncated,
	}, nil
}

// edgePtrsForTest converts seeded edge values into the pointer carrier shape the
// traversal_edges field holds.
func edgePtrsForTest(edges []knowledgev1.Edge) []*knowledgev1.Edge {
	out := make([]*knowledgev1.Edge, len(edges))
	for i := range edges {
		e := &edges[i]
		out[i] = &knowledgev1.Edge{FromId: e.FromId, ToId: e.ToId, Type: e.Type}
	}
	return out
}

// TestTraverseDescendantsWithEdges asserts the new sibling issues exactly one
// RETURN_MODE_TRAVERSAL Execute carrying Forward=&true, IncludeEdgeMetadata=true,
// MaxHops==depth, the contains edge-type selection, and NO IncludeTombstones; and
// that it returns the root-filtered descendant nodes plus the seeded contains
// edges. The truncated return is asserted false here against an untruncated
// response — TestTraverseDescendantsWithEdges_TruncatedPropagates below is its
// known-positive, without which "false" could equally mean the flag is never
// read at all.
func TestTraverseDescendantsWithEdges(t *testing.T) {
	const depth = 16
	fc := &fakeTraverseEdgesCaller{
		results: []engine.TraversalResult{
			{Distance: 0, Node: &knowledgev1.Node{Id: "root", Type: string(kgtypes.NodePlan)}},
			{Distance: 1, Node: &knowledgev1.Node{Id: "child-1", Type: string(kgtypes.NodePhase), Status: "pending"}},
			{Distance: 1, Node: &knowledgev1.Node{Id: "child-2", Type: string(kgtypes.NodeStep), Status: "pending"}},
		},
		edges: []knowledgev1.Edge{
			{FromId: "root", ToId: "child-1", Type: string(kgtypes.EdgeKGContains)},
			{FromId: "root", ToId: "child-2", Type: string(kgtypes.EdgeKGContains)},
		},
	}

	nodes, edges, truncated, err := render.TraverseDescendantsWithEdges(context.Background(), fc, "root", kgtypes.EdgeKGContains, depth)
	require.NoError(t, err)
	assert.False(t, truncated, "an untruncated response must not report truncation")

	assert.Equal(t, 1, fc.execCalls, "exactly one traversal Execute")

	plan := fc.lastPlan
	require.NotNil(t, plan)
	require.NotNil(t, plan.Forward)
	assert.True(t, plan.GetForward(), "Forward must be true (outgoing walk)")
	assert.True(t, plan.GetIncludeEdgeMetadata(), "IncludeEdgeMetadata must be set so traversal_edges is populated")
	assert.False(t, plan.GetIncludeTombstones(), "IncludeTombstones must NOT be set")
	assert.Equal(t, knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL, plan.GetReturnMode())
	assert.Equal(t, int32(depth), plan.GetMaxHops())
	assert.Equal(t, []string{"root"}, plan.GetSelection().GetFromId())
	assert.Equal(t, []string{string(kgtypes.EdgeKGContains)}, plan.GetSelection().GetEdgeTypes())

	require.Len(t, nodes, 2, "rootID must be filtered out of the node set")
	assert.Equal(t, "child-1", nodes[0].Id)
	assert.Equal(t, "child-2", nodes[1].Id)

	require.Len(t, edges, 2, "the seeded contains edges come back via traversal_edges")
	assert.Equal(t, "child-1", edges[0].ToId)
	assert.Equal(t, "child-2", edges[1].ToId)
}

// TestTraverseDescendantsWithEdges_TruncatedPropagates is the known-positive for
// the truncated return: a response carrying Truncated=true must surface as true
// to the caller, alongside the partial node/edge set it clamped. Without this,
// the assert.False above would hold just as well against a helper that never
// reads resp.Truncated at all.
func TestTraverseDescendantsWithEdges_TruncatedPropagates(t *testing.T) {
	fc := &fakeTraverseEdgesCaller{
		truncated: true,
		results: []engine.TraversalResult{
			{Distance: 0, Node: &knowledgev1.Node{Id: "root", Type: string(kgtypes.NodePlan)}},
			{Distance: 1, Node: &knowledgev1.Node{Id: "child-1", Type: string(kgtypes.NodePhase)}},
		},
		edges: []knowledgev1.Edge{
			{FromId: "root", ToId: "child-1", Type: string(kgtypes.EdgeKGContains)},
		},
	}

	nodes, edges, truncated, err := render.TraverseDescendantsWithEdges(context.Background(), fc, "root", kgtypes.EdgeKGContains, 16)
	require.NoError(t, err)
	assert.True(t, truncated, "resp.Truncated must reach the caller, not be dropped on the floor")
	// The clamped result still comes back — truncation reports partiality, it
	// does not suppress the rows already walked.
	require.Len(t, nodes, 1)
	require.Len(t, edges, 1)
}

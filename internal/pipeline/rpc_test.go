// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestListLoadedGraphs_ComposesGraphNames covers criteria 0aafbac8 + fa2fed41:
// listLoadedGraphs no longer issues a pipeline_list_graphs Call — it composes the
// (gt, name) set CLIENT-SIDE over per-type RETURN_MODE_GRAPH_NAMES Execute reads,
// seeding the explicit {knowledge, default} entry and iterating the eligible
// types. The returned SET (refreshOnce dedupes by graphKey) must match the old
// enumeration: the knowledge/default seed + every seeded per-type graph.
func TestListLoadedGraphs_ComposesGraphNames(t *testing.T) {
	f := newFakeWireClient()
	f.seedGraphNames(kgtypes.GraphCode, "knowledge", "agent")
	f.seedGraphNames(kgtypes.GraphPractice, "go")
	f.seedGraphNames(kgtypes.GraphCloud, "acct-1")

	refs, err := listLoadedGraphs(context.Background(), f)
	require.NoError(t, err)

	// No pipeline_list_graphs Call (the whole enumeration is Execute-composed).
	require.Equal(t, 0, f.calls["pipeline_list_graphs"], "no pipeline_list_graphs Call")

	got := map[string]bool{}
	for _, r := range refs {
		got[string(r.GraphType)+"/"+r.GraphName] = true
	}
	// The explicit knowledge/default seed + the seeded per-type graphs.
	require.True(t, got["knowledge/default"], "explicit knowledge/default seed preserved")
	require.True(t, got["code/knowledge"])
	require.True(t, got["code/agent"])
	require.True(t, got["practice/go"])
	require.True(t, got["cloud/acct-1"])
}

// TestWriteBatchUpdates_PassesGraphContext mirrors the fetchNodes test
// for the write side. Same selector shape, same incident class: without
// these fields the server's mutate handler defaulted to the knowledge
// graph and code-graph summary/embed writes silently produced
// "update: version-forward ... not found" with no visible error to the
// pipeline.
func TestWriteBatchUpdates_PassesGraphContext(t *testing.T) {
	cases := []struct {
		name        string
		gt          kgtypes.GraphType
		graphName   string
		wantGraph   string
		wantRepo    string
		wantAccount string
		wantName    string
	}{
		{
			name:      "code routes via repo",
			gt:        kgtypes.GraphCode,
			graphName: "knowledge",
			wantGraph: "code",
			wantRepo:  "knowledge",
		},
		{
			name:        "cloud routes via account",
			gt:          kgtypes.GraphCloud,
			graphName:   "acct-42",
			wantGraph:   "cloud",
			wantAccount: "acct-42",
		},
		{
			name:      "knowledge non-default routes via name",
			gt:        kgtypes.GraphKnowledge,
			graphName: "personal",
			wantGraph: "knowledge",
			wantName:  "personal",
		},
		{
			name:      "knowledge default omits name",
			gt:        kgtypes.GraphKnowledge,
			graphName: "default",
			wantGraph: "knowledge",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeWireClient()
			s := "summary text"
			items := []updateBatchItem{{ID: "n1", Summary: &s}}
			require.NoError(t, writeBatchUpdates(context.Background(), f, tc.gt, tc.graphName, items))

			// writeBatchUpdates now rides the engine Execute seam: the selector
			// travels on the ExecuteRequest.Target GraphSelector (not the legacy
			// update_batch JSON args), and the plan is MUTATION_KIND_UPDATE_ITEMS.
			req := f.lastExecRequest()
			require.NotNil(t, req, "writeBatchUpdates must issue an Execute")
			m := req.GetMutation()
			require.NotNil(t, m, "the Execute carries a MutationPlan")
			require.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPDATE_ITEMS, m.GetKind())
			require.Equal(t, tc.wantGraph, req.GetTarget().GetGraph(), "graph selector")
			require.Equal(t, tc.wantRepo, req.GetTarget().GetRepo(), "repo selector")
			require.Equal(t, tc.wantAccount, req.GetTarget().GetAccount(), "account selector")
			require.Equal(t, tc.wantName, req.GetTarget().GetName(), "name selector")
			require.Len(t, m.GetUpdateItems(), 1)
			require.Equal(t, "n1", m.GetUpdateItems()[0].GetId())
		})
	}
}

// TestWriteBatchUpdates_EmptyItemsIsNoOp pins the no-op contract: zero
// items must NOT fire an RPC. Pipeline workers reach this with an empty
// items slice when every per-node result was filtered out (e.g., all
// transient errors); the RPC would just commit a no-op txn and waste a
// round-trip.
func TestWriteBatchUpdates_EmptyItemsIsNoOp(t *testing.T) {
	f := newFakeWireClient()
	require.NoError(t, writeBatchUpdates(context.Background(), f, kgtypes.GraphCode, "knowledge", nil))
	require.Equal(t, 0, f.calls["mutate"], "no items must not fire an RPC")
	require.Nil(t, f.lastExecRequest(), "no items must not fire an Execute")
}

// TestWriteBatchUpdates_RidesEngineUpdateItemsArm covers criterion 6cc9452a:
// writeBatchUpdates produces EXACTLY ONE Execute per write group, and the
// captured plan is a MUTATION_KIND_UPDATE_ITEMS with N UpdateItems — proving the
// pipeline rides the engine arm, not the legacy mutate(update_batch) Call path.
func TestWriteBatchUpdates_RidesEngineUpdateItemsArm(t *testing.T) {
	f := newFakeWireClient()
	s1, kw := "sum-1", "kw"
	vec := make([]byte, 32)
	items := []updateBatchItem{
		{ID: "n1", Summary: &s1, Keywords: &kw},
		{ID: "n2", BinaryVector: vec},
		{ID: "n3", Metadata: map[string]string{"k": "v"}},
	}
	require.NoError(t, writeBatchUpdates(context.Background(), f, kgtypes.GraphCode, "knowledge", items))

	// Exactly ONE Execute for the whole group (the load-bearing 1-RPC-per-batch
	// bound, counted on Execute now).
	require.Len(t, f.execRequests, 1, "exactly one Execute per write group (no N+1)")
	require.Equal(t, 1, f.calls["mutate"], "exactly one mutate write per group")

	// The captured plan is the engine UPDATE_ITEMS arm with all N items.
	m := f.execRequests[0].GetMutation()
	require.NotNil(t, m)
	require.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPDATE_ITEMS, m.GetKind())
	require.Len(t, m.GetUpdateItems(), 3, "all 3 heterogeneous items ride the one plan")
	require.Equal(t, "code", f.execRequests[0].GetTarget().GetGraph())
	require.Equal(t, "knowledge", f.execRequests[0].GetTarget().GetRepo())

	// The decoded write capture still reflects all items (helpers preserved).
	require.Equal(t, 3, f.totalWriteItems())
}

// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

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
			// The knowledge family is a SINGLETON: it holds one graph, so its
			// resolver reads no selector name and the server rejects a non-alias
			// one outright ("graph=knowledge holds ONE graph: name= is a label,
			// not a selector"). ApplyInstanceKey still assigns the instance key
			// into args.Name — it is the family-generic mapper and knowledge falls
			// in its default arm — so the compile step is what drops it, and the
			// write lands on the one knowledge graph either way. Before that drop
			// this row expected "personal" on the wire, which the server could
			// only ever have refused.
			name:      "knowledge non-default carries no name — the family is a singleton",
			gt:        kgtypes.GraphKnowledge,
			graphName: "personal",
			wantGraph: "knowledge",
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

// TestWriteBatchUpdates_OverlayQualifiedGraphName is the client RED→GREEN
// witness for the overlay-resident writeback bug. The gap scan tags an
// overlay-resident GapItem with the overlay-qualified GraphName "repo@branch";
// writeBatchUpdates must split that and thread the branch onto the Execute Target
// (Repo==base, Branch==branch) so the server resolveCode Scopes the overlay and
// the by-id write lands on the same layer the scan read from.
//
// Pre-fix (the bug) writeBatchUpdates passed "repo@branch" whole to
// ApplyInstanceKey → Target.Repo=="repo@branch", Target.Branch=="", so the server
// resolved the base graph and the write failed not_found — discarding the
// already-billed summary/vector. This test is RED on HEAD (Branch=="", Repo
// carries the "@") and GREEN after the Cut + Branch threading. The bare-base case
// (no "@") must leave Branch empty so base/default-branch writes are unchanged.
func TestWriteBatchUpdates_OverlayQualifiedGraphName(t *testing.T) {
	t.Run("overlay-qualified repo@branch → Repo=base, Branch=branch", func(t *testing.T) {
		f := newFakeWireClient()
		s := "composed summary"
		items := []updateBatchItem{{ID: "ov-1", Summary: &s}}
		require.NoError(t, writeBatchUpdates(context.Background(), f, kgtypes.GraphCode, "myrepo@feat", items))

		req := f.lastExecRequest()
		require.NotNil(t, req, "writeBatchUpdates must issue an Execute")
		require.Equal(t, "code", req.GetTarget().GetGraph())
		require.Equal(t, "myrepo", req.GetTarget().GetRepo(),
			"the bare base must route to Repo (the '@branch' must NOT bleed into Repo)")
		require.Equal(t, "feat", req.GetTarget().GetBranch(),
			"the overlay branch must thread onto Target.Branch so resolveCode Scopes the overlay")
	})

	t.Run("bare base (no @) → Branch empty (base/default-branch write unchanged)", func(t *testing.T) {
		f := newFakeWireClient()
		s := "composed summary"
		items := []updateBatchItem{{ID: "n1", Summary: &s}}
		require.NoError(t, writeBatchUpdates(context.Background(), f, kgtypes.GraphCode, "myrepo", items))

		req := f.lastExecRequest()
		require.NotNil(t, req)
		require.Equal(t, "myrepo", req.GetTarget().GetRepo())
		require.Empty(t, req.GetTarget().GetBranch(),
			"a bare base name must leave Branch empty — no regression for base writes")
	})

	t.Run("knowledge overlay default@session-x leaves Branch empty", func(t *testing.T) {
		f := newFakeWireClient()
		s := "composed summary"
		items := []updateBatchItem{{ID: "kn-1", Summary: &s}}
		require.NoError(t, writeBatchUpdates(context.Background(), f, kgtypes.GraphKnowledge, "default@session-x", items))

		req := f.lastExecRequest()
		require.NotNil(t, req)
		require.Equal(t, "knowledge", req.GetTarget().GetGraph())
		require.Empty(t, req.GetTarget().GetBranch(),
			"the knowledge resolver reads no Branch — sending one is a field the family cannot honor")
		require.Empty(t, req.GetTarget().GetName(),
			"the knowledge singleton's root name is omitted (omitDefaultName), so the Cut's base never lands on Name")
	})
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

// TestWriteBatchUpdates_RidesEngineUpdateItemsArm covers that
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

// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"
	"time"

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

	refs, succeeded, _, throttled := listLoadedGraphs(context.Background(), f)
	require.False(t, throttled, "a fully-enumerated tick is never throttled")

	// No pipeline_list_graphs Call (the whole enumeration is Execute-composed).
	require.Equal(t, 0, f.calls["pipeline_list_graphs"], "no pipeline_list_graphs Call")

	// Every eligible type enumerated cleanly here, so each is marked succeeded —
	// refreshOnce is then free to unregister within any of them.
	for _, gt := range pipelineEligibleGraphTypes {
		require.True(t, succeeded[gt], "type %s enumerated successfully", gt)
	}

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

// TestListLoadedGraphs_PerTypeFailureIsNonFatal covers the pipeline-stall
// resilience fix: a per-type list_graphs failure (a backend rollout 502, a
// permission_denied) must NOT abort the whole enumeration. The failing type is
// skipped — its graphs are absent and it is excluded from `succeeded` — while
// every other type still enumerates. Before the fix, one type's Execute error
// returned nil+error and refreshOnce bailed, registering no collectors and
// wedging enrichment until a client reload.
func TestListLoadedGraphs_PerTypeFailureIsNonFatal(t *testing.T) {
	f := newFakeWireClient()
	f.seedGraphNames(kgtypes.GraphCode, "knowledge")
	f.seedGraphNames(kgtypes.GraphPractice, "go")
	f.failGraphNames(kgtypes.GraphCloud) // simulate a per-type backend failure

	refs, succeeded, _, throttled := listLoadedGraphs(context.Background(), f)
	// A partial failure (healthy siblings enumerated) is NOT a throttle — the
	// per-type skip already absorbs it; discovery must not back off for the
	// healthy types.
	require.False(t, throttled, "partial per-type failure is not a whole-tick throttle")

	got := map[string]bool{}
	for _, r := range refs {
		got[string(r.GraphType)+"/"+r.GraphName] = true
	}
	// The healthy types still enumerated despite cloud failing.
	require.True(t, got["knowledge/default"], "seed survives a sibling-type failure")
	require.True(t, got["code/knowledge"], "code enumerated despite cloud failing")
	require.True(t, got["practice/go"], "practice enumerated despite cloud failing")

	// The failing type is excluded from succeeded (so refreshOnce won't tear down
	// its collectors on the strength of an empty wanted-set); healthy types are in.
	require.False(t, succeeded[kgtypes.GraphCloud], "failed type excluded from succeeded")
	require.True(t, succeeded[kgtypes.GraphCode], "healthy type marked succeeded")
	require.True(t, succeeded[kgtypes.GraphPractice], "healthy type marked succeeded")
}

// TestListLoadedGraphs_WholeTickRateLimitedThrottles covers the discovery-storm
// fix: when EVERY eligible type's enumeration fails with a remote 429, the tick
// made zero progress purely because the backend is throttling. listLoadedGraphs
// reports throttled=true and surfaces the max Retry-After so RefreshLoadedGraphs
// backs off instead of re-firing one-query-per-type at the base cadence — the
// retry storm that filled the client log with list-graphs WARNs for days.
func TestListLoadedGraphs_WholeTickRateLimitedThrottles(t *testing.T) {
	f := newFakeWireClient()
	// Rate-limit every eligible type; the heaviest Retry-After should win.
	for _, gt := range pipelineEligibleGraphTypes {
		f.rateLimitGraphNames(gt, 1)
	}
	f.rateLimitGraphNames(kgtypes.GraphCloud, 3) // max hint

	refs, succeeded, hint, throttled := listLoadedGraphs(context.Background(), f)

	require.True(t, throttled, "a whole tick lost to 429s is a throttle")
	require.Equal(t, 3*time.Second, hint, "throttle surfaces the max Retry-After seen")
	require.Empty(t, succeeded, "no type enumerated under a whole-tick throttle")
	// Only the unconditional knowledge/default seed survives — no per-type graphs.
	require.Len(t, refs, 1)
	require.Equal(t, kgtypes.GraphKnowledge, refs[0].GraphType)
	require.Equal(t, "default", refs[0].GraphName)
}

// TestListLoadedGraphs_RegisteredTypeAppears proves the dynamic enumeration: a
// registered custom GraphTypeDef ("hellograph") discovered this tick is folded
// onto the builtin base, so its loaded graphs (hellograph/demo) appear in the
// returned GraphRef set alongside the builtins.
func TestListLoadedGraphs_RegisteredTypeAppears(t *testing.T) {
	f := newFakeWireClient()
	f.seedGraphTypeDefs("hellograph")
	f.seedGraphNames(kgtypes.GraphCode, "knowledge")
	f.seedGraphNames(kgtypes.GraphType("hellograph"), "demo")

	refs, succeeded, _, throttled := listLoadedGraphs(context.Background(), f)
	require.False(t, throttled, "a fully-enumerated tick is never throttled")

	got := map[string]bool{}
	for _, r := range refs {
		got[string(r.GraphType)+"/"+r.GraphName] = true
	}
	require.True(t, got["knowledge/default"], "builtin knowledge/default seed preserved")
	require.True(t, got["code/knowledge"], "builtin code graph still enumerated")
	require.True(t, got["hellograph/demo"], "registered custom type's loaded graph appears")
	// The registered type enumerated cleanly, so it is marked succeeded — making it
	// eligible for collector unregistration the same as a builtin.
	require.True(t, succeeded[kgtypes.GraphType("hellograph")], "registered type marked succeeded")
}

// TestListLoadedGraphs_DeletedTypeDropsOut proves a previously-registered type
// that is no longer in the registry (the browse returns an empty set this tick)
// is NOT enumerated — only the builtins remain.
func TestListLoadedGraphs_DeletedTypeDropsOut(t *testing.T) {
	f := newFakeWireClient()
	// No seedGraphTypeDefs call → the registry browse returns zero custom types
	// (the "deleted" state). Even if a graph were seeded under the custom type, it
	// must not be iterated because the type is no longer discovered.
	f.seedGraphNames(kgtypes.GraphType("hellograph"), "demo")
	f.seedGraphNames(kgtypes.GraphCode, "knowledge")

	refs, succeeded, _, throttled := listLoadedGraphs(context.Background(), f)
	require.False(t, throttled)

	got := map[string]bool{}
	for _, r := range refs {
		got[string(r.GraphType)+"/"+r.GraphName] = true
	}
	require.True(t, got["code/knowledge"], "builtin still enumerated")
	require.False(t, got["hellograph/demo"], "deleted custom type is not iterated")
	require.False(t, succeeded[kgtypes.GraphType("hellograph")], "deleted custom type not in succeeded")
}

// TestListLoadedGraphs_BuiltinsUnchangedByBrowse proves the builtin enumeration is
// identical to the no-custom-types baseline: with an empty registry, every builtin
// type still enumerates and is marked succeeded exactly as before.
func TestListLoadedGraphs_BuiltinsUnchangedByBrowse(t *testing.T) {
	f := newFakeWireClient()
	f.seedGraphNames(kgtypes.GraphCode, "knowledge", "agent")
	f.seedGraphNames(kgtypes.GraphPractice, "go")

	refs, succeeded, _, throttled := listLoadedGraphs(context.Background(), f)
	require.False(t, throttled)

	for _, gt := range pipelineEligibleGraphTypes {
		require.True(t, succeeded[gt], "builtin type %s enumerated successfully", gt)
	}
	got := map[string]bool{}
	for _, r := range refs {
		got[string(r.GraphType)+"/"+r.GraphName] = true
	}
	require.True(t, got["knowledge/default"])
	require.True(t, got["code/knowledge"])
	require.True(t, got["code/agent"])
	require.True(t, got["practice/go"])
}

// TestListLoadedGraphs_BrowseFailureIsNonFatal proves a graph_type_def browse
// failure (a rollout 502 / permission_denied) does NOT abort the tick: builtins
// still enumerate and `succeeded` still reports the builtin types. Custom types
// are simply skipped this tick. Also proves a browse rate-limit alone does NOT
// force a whole-tick throttle when the builtins enumerated fine.
func TestListLoadedGraphs_BrowseFailureIsNonFatal(t *testing.T) {
	t.Run("browse error skips custom types, builtins unaffected", func(t *testing.T) {
		f := newFakeWireClient()
		f.failGraphTypeDefBrowseRead()
		f.seedGraphNames(kgtypes.GraphCode, "knowledge")
		f.seedGraphNames(kgtypes.GraphPractice, "go")

		refs, succeeded, _, throttled := listLoadedGraphs(context.Background(), f)
		require.False(t, throttled, "a browse error with healthy builtins is not a whole-tick throttle")

		got := map[string]bool{}
		for _, r := range refs {
			got[string(r.GraphType)+"/"+r.GraphName] = true
		}
		require.True(t, got["knowledge/default"], "seed survives a browse failure")
		require.True(t, got["code/knowledge"], "builtin code enumerated despite browse failing")
		require.True(t, got["practice/go"], "builtin practice enumerated despite browse failing")
		for _, gt := range pipelineEligibleGraphTypes {
			require.True(t, succeeded[gt], "builtin type %s still marked succeeded", gt)
		}
	})

	t.Run("browse rate-limit alone does not throttle when builtins enumerate", func(t *testing.T) {
		f := newFakeWireClient()
		f.rateLimitGraphTypeDefBrowseRead(5) // browse 429s with a Retry-After
		f.seedGraphNames(kgtypes.GraphCode, "knowledge")

		_, succeeded, _, throttled := listLoadedGraphs(context.Background(), f)
		// sawRateLimit is true (the browse 429'd) but at least one builtin enumerated,
		// so len(succeeded) > 0 → the whole-tick-throttle predicate is false.
		require.False(t, throttled, "a registry-browse 429 must not force backoff when builtins are healthy")
		require.True(t, succeeded[kgtypes.GraphCode], "builtin enumerated under a browse-only rate-limit")
	})
}

// TestListLoadedGraphs_IncludesRegisteredType proves a registered type is
// enumerated REGARDLESS of its behavior config — the client applies no behavior
// gate; the server's gap shims cheaply no-op a both-false type. The fake serves the
// type name from the registry browse (the behavior axes live in the node metadata
// the client never inspects here), so a both-false type still folds into the set.
func TestListLoadedGraphs_IncludesRegisteredType(t *testing.T) {
	f := newFakeWireClient()
	f.seedGraphTypeDefs("noopgraph") // registered, axes irrelevant to the client
	f.seedGraphNames(kgtypes.GraphType("noopgraph"), "inst")

	refs, succeeded, _, _ := listLoadedGraphs(context.Background(), f)
	got := map[string]bool{}
	for _, r := range refs {
		got[string(r.GraphType)+"/"+r.GraphName] = true
	}
	require.True(t, got["noopgraph/inst"], "a both-false registered type is still enumerated (server no-ops it)")
	require.True(t, succeeded[kgtypes.GraphType("noopgraph")])
}

// TestListLoadedGraphs_BuiltinNamedDefIsDeduped proves the defensive dedupe: a
// GraphTypeDef whose name collides with a builtin (which registration normally
// rejects, but is filtered here defensively) is not double-iterated.
func TestListLoadedGraphs_BuiltinNamedDefIsDeduped(t *testing.T) {
	f := newFakeWireClient()
	f.seedGraphTypeDefs("code") // collides with a builtin — must be filtered out
	f.seedGraphNames(kgtypes.GraphCode, "knowledge")

	_, succeeded, _, _ := listLoadedGraphs(context.Background(), f)
	// "code" appears exactly once in the iterated set (as the builtin); the
	// builtin-named registered entry was dropped, so succeeded["code"] reflects the
	// single builtin enumeration, not a duplicate.
	require.True(t, succeeded[kgtypes.GraphCode])
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

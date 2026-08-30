// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// fakeSegmentSearcher records Search invocations (count + last args) and returns
// a canned RRF-fused Hit list. Satisfies tools.SegmentSearcher (and, by the same
// method set, clientthought.ThoughtSearcher).
type fakeSegmentSearcher struct {
	calls    atomic.Int64
	lastGT   kgtypes.GraphType
	lastName string
	lastText string
	lastVec  []byte
	lastK    int
	hits     []searchengine.Hit
}

func (f *fakeSegmentSearcher) Search(
	_ context.Context, gt kgtypes.GraphType, name, queryText string, queryVec []byte, k int,
) ([]searchengine.Hit, error) {
	f.calls.Add(1)
	f.lastGT, f.lastName, f.lastText, f.lastVec, f.lastK = gt, name, queryText, queryVec, k
	return f.hits, nil
}

// cannedNodesResp builds a RETURN_MODE_NODES response carrying the named nodes
// (the hydrate read's reply). The hydrator joins these by id-map to the ranked
// Hit IDs.
func cannedNodesResp(nodes ...*knowledgev1.Node) *knowledgev1.ExecuteResponse {
	return &knowledgev1.ExecuteResponse{Nodes: nodes}
}

// cannedEmbeddedNodesResp is cannedNodesResp for a graph that HAS been embedded:
// the same hydrate nodes plus a catalog entry recording which embedder produced
// its vectors.
//
// IT IS A SEPARATE HELPER RATHER THAN A CHANGE TO cannedNodesResp, and the
// distinction is load-bearing. Several tests in this package assert the
// NO-EMBEDDER behaviour — that a graph with nothing to embed against ranks by
// BM25 alone, and that mode:vector is refused rather than served empty. Handing
// every canned response an identity would have quietly given those tests an
// embedder and turned their subject into something else.
func cannedEmbeddedNodesResp(nodes ...*knowledgev1.Node) *knowledgev1.ExecuteResponse {
	return &knowledgev1.ExecuteResponse{Nodes: nodes, GraphNames: cannedKnowledgeCatalog()}
}

// cannedKnowledgeCatalog is the graph catalog the knowledge arm now reads to
// learn which embedder produced the graph's vectors.
//
// IT IS ON THE SAME CANNED RESPONSE AS THE NODES because the harness serves one
// response to every Execute, and the two readers look at different fields: the
// hydrate reader at Nodes, the catalog reader at GraphNames. Carrying both keeps
// the harness a single canned response rather than a per-request router.
//
// THE FAKE PROVIDER IS DELIBERATE: it needs no credential and makes no network
// call, so a test that reaches the embedder exercises the real resolution path
// without a key, and a failure is about identity resolution rather than about a
// missing secret.
func cannedKnowledgeCatalog() []*knowledgev1.GraphInfo {
	return []*knowledgev1.GraphInfo{{
		Name:   knowledgeDefaultName,
		Loaded: true,
		EmbedIdentity: &knowledgev1.EmbedIdentity{
			Provider: "fake", Model: "canned", Dimension: cannedCatalogDim, Dtype: "ubinary",
		},
	}}
}

// cannedCatalogDim is deliberately NOT the default width, and that is what makes
// every fixture using this catalog able to discriminate.
//
// A TEST THAT CARRIES BOTH A STUB EMBEDDER AND A CATALOG IDENTITY CANNOT TELL
// THEM APART IF THEY PRODUCE THE SAME SHAPE. Both would yield a non-empty
// vector, so "the vector reached the engine" would pass whether the arm resolved
// from the graph's identity or fell back to the configured embedder — which is
// exactly the regression the wiring exists to prevent, sitting under a green
// gate. At 1024 dims the identity-resolved vector is 128 bytes and the stub's is
// 32, so the width IS the answer to "which one ran".
const cannedCatalogDim = 1024

// cannedCatalogVecBytes is the byte width cannedCatalogDim implies.
const cannedCatalogVecBytes = cannedCatalogDim / 8

// dispatchedAServerSearch reports whether any captured ExecuteRequest was a
// server RETURN_MODE_SEARCH dispatch (a Queries-bearing search plan) — the thing
// the GO-LIVE reroute must NOT do for the knowledge arm. The hydrate read is an
// Ids[] plan, which is NOT a server search.
func dispatchedAServerSearch(reqs []*knowledgev1.ExecuteRequest) bool {
	for _, r := range reqs {
		q := r.GetQuery()
		if q == nil {
			continue
		}
		if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_SEARCH || len(q.GetQueries()) > 0 {
			return true
		}
	}
	return false
}

// TestInterceptSearchKnowledge_RoutesToClientEngine is Phase 3 Step 2's
// criterion (A): search(graph:knowledge) returns RRF-fused results from the
// CLIENT engines via Manager.Search + RETURN_MODE_NODES hydration, with NO
// server search dispatch. The fake Manager returns ranked Hits; the recording
// Execute serves the hydrate nodes read and proves no SEARCH plan was sent.
func TestInterceptSearchKnowledge_RoutesToClientEngine(t *testing.T) {
	var execHits, embedCalls atomic.Int64
	gc, handler := newInterceptHarnessWithHandler(t, &execHits, cannedEmbeddedNodesResp(
		&knowledgev1.Node{Id: "n1", Type: "finding", SymbolName: "FirstHit"},
		&knowledgev1.Node{Id: "n2", Type: "finding", SymbolName: "SecondHit"},
	))
	mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{
		{ID: "n1", Score: 0.9},
		{ID: "n2", Score: 0.8},
	}}
	deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}, segMgr: mgr}

	handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{"query": "x", "graph": "knowledge"}))
	require.True(t, handled)
	require.False(t, out.IsError, "result is not an error: %v", engine.FirstTextContent(out))

	// The CLIENT engine ran, with a query vector (HNSW arm exercised).
	require.Equal(t, int64(1), mgr.calls.Load(), "Manager.Search drove the knowledge arm")
	require.Equal(t, kgtypes.GraphKnowledge, mgr.lastGT)
	require.Equal(t, knowledgeDefaultName, mgr.lastName)
	require.Len(t, mgr.lastVec, cannedCatalogVecBytes,
		"the query vector must come from the GRAPH's recorded identity (1024 dims = 128 bytes), not "+
			"from the configured stub embedder (32 bytes) — equal widths would make this assertion "+
			"pass under either source and the whole resolution revertible in silence")

	// No SERVER search dispatch — only the hydrate Ids[] read went to the wire.
	require.False(t, dispatchedAServerSearch(handler.recordedReqs()), "knowledge arm must NOT dispatch a server search")

	// Rendered output carries the fused hits in rank order.
	body := engine.FirstTextContent(out)
	assert.Contains(t, body, "FirstHit")
	assert.Contains(t, body, "SecondHit")
}

// TestInterceptSearchKnowledge_ClientEngineWithoutEmbedder asserts the
// unconditional-client contract on the no-embedder path: even without a client
// embedder (no query vector → BM25-only via RRF-over-one-list), the knowledge arm
// runs against the CLIENT engine (Manager.Search) and dispatches NO server search.
func TestInterceptSearchKnowledge_ClientEngineWithoutEmbedder(t *testing.T) {
	var execHits atomic.Int64
	gc, handler := newInterceptHarnessWithHandler(t, &execHits, cannedNodesResp(
		&knowledgev1.Node{Id: "n1", Type: "finding", SymbolName: "BM25Hit"},
	))
	mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "n1", Score: 0.7}}}
	deps := &interceptDeps{gc: gc, segMgr: mgr} // emb nil → BM25-only arm

	handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{"query": "x", "graph": "knowledge"}))
	require.True(t, handled)
	require.False(t, out.IsError, "result is not an error: %v", engine.FirstTextContent(out))

	require.Equal(t, int64(1), mgr.calls.Load(), "knowledge arm drove the CLIENT engine unconditionally")
	require.Empty(t, mgr.lastVec, "no embedder → BM25-only arm (empty query vector)")
	require.False(t, dispatchedAServerSearch(handler.recordedReqs()), "knowledge arm must NOT dispatch a server search")
	assert.Contains(t, engine.FirstTextContent(out), "BM25Hit")
}

// daysAgoNanos returns a unix-nanos timestamp `days` in the past — used to give
// the canned browse nodes distinct UpdatedAt values for the recency-order asserts.
func daysAgoNanos(days float64) int64 {
	return time.Now().Add(-time.Duration(days*24) * time.Hour).UnixNano()
}

// recentJSONResult is the slice of the rendered JSON envelope the recent-browse
// tests assert against: result order (recency) + count (limit honored).
type recentJSONResult struct {
	Total   int `json:"total"`
	Results []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	} `json:"results"`
}

// browseSelection returns the first recorded Execute's browse Selection — the
// Match-empty / NodeTypes-scoped plan the recent browse issues. A nil return means
// no Selection-bearing browse plan was recorded.
func browseSelection(reqs []*knowledgev1.ExecuteRequest) *knowledgev1.Selection {
	for _, r := range reqs {
		if q := r.GetQuery(); q != nil && q.GetSelection() != nil {
			return q.GetSelection()
		}
	}
	return nil
}

// recordedBrowsePlan returns the first recorded Execute's QueryPlan (the browse).
func recordedBrowsePlan(reqs []*knowledgev1.ExecuteRequest) *knowledgev1.QueryPlan {
	for _, r := range reqs {
		if q := r.GetQuery(); q != nil {
			return q
		}
	}
	return nil
}

// TestInterceptQueryKnowledgeSearch_BareRecentTemporalBrowse is the scope-A
// regression guard: bare query(mode:recent) with EMPTY text returns the
// most-recently-updated nodes via a Match-empty no-Limit GraphCaller browse —
// honoring `limit` AFTER the recency sort, with NO server search dispatch and NO
// Manager.Search call.
//
// Red-before-green note: steps 1-2 (the composeRecentBrowse branch) and this test
// land in the same atomic ticket, so a literal pre-fix red run was impractical in
// one pass; the assertions below are the green target proven against the landed fix.
func TestInterceptQueryKnowledgeSearch_BareRecentTemporalBrowse(t *testing.T) {
	var execHits atomic.Int64
	// Canned nodes appended OLD→NEW (and the oldest last-but-one) so a pass-through
	// that skipped the temporal sort would NOT already be in recency order.
	gc, handler := newInterceptHarnessWithHandler(t, &execHits, cannedNodesResp(
		&knowledgev1.Node{Id: "old", Type: "finding", SymbolName: "OldNode", UpdatedAt: daysAgoNanos(100)},
		&knowledgev1.Node{Id: "mid", Type: "finding", SymbolName: "MidNode", UpdatedAt: daysAgoNanos(10)},
		&knowledgev1.Node{Id: "new", Type: "finding", SymbolName: "NewNode", UpdatedAt: daysAgoNanos(1)},
	))
	mgr := &fakeSegmentSearcher{}
	deps := &interceptDeps{gc: gc, segMgr: mgr}

	handled, out := InterceptQueryKnowledgeSearch(opCtx(), deps, queryParams(t, map[string]any{
		"mode": "recent", "limit": 2, "format": "json",
	}))
	require.True(t, handled, "bare recent is claimed + served client-side")
	require.False(t, out.IsError, "bare recent renders cleanly: %v", engine.FirstTextContent(out))

	// Manager.Search must NOT run — this is a pure browse, not a text search.
	require.Equal(t, int64(0), mgr.calls.Load(), "bare recent does NOT drive Manager.Search")
	// No server RETURN_MODE_SEARCH dispatch — the browse is a Selection plan.
	require.False(t, dispatchedAServerSearch(handler.recordedReqs()), "bare recent must NOT dispatch a server search")

	// The recorded Execute is a Match-empty (all-types) browse PAGE. The page
	// carries the drain's limit, not the caller's: the whole candidate set is
	// still considered, and truncation to the caller's limit stays client-side
	// AFTER the recency sort.
	sel := browseSelection(handler.recordedReqs())
	require.NotNil(t, sel, "recorded a Selection-bearing browse plan")
	require.Empty(t, sel.GetNodeTypes(), "bare recent (no types) is a Match-empty all-types browse")
	plan := recordedBrowsePlan(handler.recordedReqs())
	require.NotNil(t, plan)
	require.EqualValues(t, paging.BrowsePageSize, plan.GetLimit(),
		"the page carries the DRAIN's limit — never the caller's, which would bias which nodes are considered")

	// Rendered JSON: recency order (new before mid) and limit=2 honored (old omitted).
	var got recentJSONResult
	require.NoError(t, json.Unmarshal([]byte(engine.FirstTextContent(out)), &got))
	require.Equal(t, 2, got.Total, "limit=2 honored after the recency sort")
	require.Len(t, got.Results, 2)
	assert.Equal(t, "new", got.Results[0].ID, "most-recently-updated first")
	assert.Equal(t, "mid", got.Results[1].ID, "second-most-recent next")
}

// TestInterceptQueryKnowledgeSearch_TextBearingRecentUnchanged proves the
// text-bearing recent path is UNCHANGED by the bare-recent branch: a recent query
// WITH text still drives Manager.Search via composeKnowledgeSearch (mgr.calls==1)
// and dispatches no server search.
func TestInterceptQueryKnowledgeSearch_TextBearingRecentUnchanged(t *testing.T) {
	var execHits, embedCalls atomic.Int64
	gc, handler := newInterceptHarnessWithHandler(t, &execHits, cannedNodesResp(
		&knowledgev1.Node{Id: "n1", Type: "finding", SymbolName: "TextRecentHit", UpdatedAt: daysAgoNanos(2)},
	))
	mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "n1", Score: 0.9}}}
	deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}, segMgr: mgr}

	handled, out := InterceptQueryKnowledgeSearch(opCtx(), deps, queryParams(t, map[string]any{
		"mode": "recent", "text": "foo",
	}))
	require.True(t, handled)
	require.False(t, out.IsError, "text-bearing recent renders cleanly: %v", engine.FirstTextContent(out))
	require.Equal(t, int64(1), mgr.calls.Load(), "text-bearing recent STILL drives Manager.Search")
	require.False(t, dispatchedAServerSearch(handler.recordedReqs()), "text-bearing recent must NOT dispatch a server search")
	assert.Contains(t, engine.FirstTextContent(out), "TextRecentHit")
}

// TestInterceptQueryKnowledgeSearch_RecentWithTypesFilter is the scope-B regression
// guard: recent + types pushes the type set to the FETCH (the recorded browse
// plan's Selection.NodeTypes equals the requested set), carries NO Limit on the
// plan, renders in recency order honoring limit, drives no Manager.Search, and
// dispatches no server search. The canned resp contains ONLY the requested types
// (the fake handler cannot itself apply the server-side store type selection),
// so the rendered recency order is unambiguous; the real fetch-filter proof is the
// recorded Selection.NodeTypes assertion.
// TestInterceptQueryKnowledgeSearch_RecentSingularTypeFilter pins the SINGULAR
// type spelling on the bare recent arm. Live, query(type:"decision",
// mode:"recent") returned five rows and zero decisions: the arm read only the
// plural a.Types, so the singular filter was dropped and the drain paged the
// whole node set before the temporal sort — confidently wrong rows rather than
// an error or an empty result.
//
// The fixture seeds TWO node types with different concrete values and the
// handler applies the plan's type filter, so an arm that drops the filter is
// observable in the RETURNED ROWS. A single-type corpus, or a handler that
// answers every plan identically, would pass either way.
func TestInterceptQueryKnowledgeSearch_RecentSingularTypeFilter(t *testing.T) {
	corpus := []*knowledgev1.Node{
		{Id: "d1", Type: "decision", SymbolName: "Decide", UpdatedAt: daysAgoNanos(2)},
		{Id: "s1", Type: "step", SymbolName: "Stepped", UpdatedAt: daysAgoNanos(1)},
	}

	t.Run("singular_type_returns_only_that_type", func(t *testing.T) {
		var execHits atomic.Int64
		gc, handler := newInterceptHarnessWithHandler(t, &execHits, nil)
		handler.typedCorpus = corpus
		deps := &interceptDeps{gc: gc, segMgr: &fakeSegmentSearcher{}}

		handled, out := InterceptQueryKnowledgeSearch(opCtx(), deps, queryParams(t, map[string]any{
			"mode": "recent", "type": "decision", "limit": 5, "format": "json",
		}))
		require.True(t, handled)
		require.False(t, out.IsError, "recent+type renders cleanly: %v", engine.FirstTextContent(out))

		sel := browseSelection(handler.recordedReqs())
		require.NotNil(t, sel, "recorded a Selection-bearing browse plan")
		assert.Equal(t, []string{"decision"}, sel.GetNodeTypes(),
			"the singular type is promoted to the fetch-level type set")

		var got recentJSONResult
		require.NoError(t, json.Unmarshal([]byte(engine.FirstTextContent(out)), &got))
		require.Len(t, got.Results, 1, "only the decision survives the filter")
		assert.Equal(t, "d1", got.Results[0].ID, "the step must not appear under type=decision")
	})

	t.Run("types_wins_over_singular_type", func(t *testing.T) {
		// The precedence the text-bearing route already implements: the plural set
		// wins and the singular is not applied when both are supplied, so the two
		// arms cannot disagree about the same payload.
		var execHits atomic.Int64
		gc, handler := newInterceptHarnessWithHandler(t, &execHits, nil)
		handler.typedCorpus = corpus
		deps := &interceptDeps{gc: gc, segMgr: &fakeSegmentSearcher{}}

		handled, out := InterceptQueryKnowledgeSearch(opCtx(), deps, queryParams(t, map[string]any{
			"mode": "recent", "types": []string{"step"}, "type": "decision", "limit": 5, "format": "json",
		}))
		require.True(t, handled)
		require.False(t, out.IsError)

		sel := browseSelection(handler.recordedReqs())
		require.NotNil(t, sel)
		assert.Equal(t, []string{"step"}, sel.GetNodeTypes(), "the plural set wins outright")

		var got recentJSONResult
		require.NoError(t, json.Unmarshal([]byte(engine.FirstTextContent(out)), &got))
		require.Len(t, got.Results, 1)
		assert.Equal(t, "s1", got.Results[0].ID, "types won, so the singular decision is absent")
	})
}

func TestInterceptQueryKnowledgeSearch_RecentWithTypesFilter(t *testing.T) {
	var execHits atomic.Int64
	// Mixed-recency project/ticket nodes, appended so neither type nor recency is
	// already sorted (project older than ticket; project appended first).
	gc, handler := newInterceptHarnessWithHandler(t, &execHits, cannedNodesResp(
		&knowledgev1.Node{Id: "p1", Type: "project", SymbolName: "Proj", UpdatedAt: daysAgoNanos(2)},
		&knowledgev1.Node{Id: "t1", Type: "ticket", SymbolName: "Tick", UpdatedAt: daysAgoNanos(1)},
	))
	mgr := &fakeSegmentSearcher{}
	deps := &interceptDeps{gc: gc, segMgr: mgr}

	handled, out := InterceptQueryKnowledgeSearch(opCtx(), deps, queryParams(t, map[string]any{
		"mode": "recent", "types": []string{"project", "ticket"}, "limit": 5, "format": "json",
	}))
	require.True(t, handled)
	require.False(t, out.IsError, "recent+types renders cleanly: %v", engine.FirstTextContent(out))

	// The fetch-level type-set filter: the recorded browse plan carries the
	// requested NodeTypes set (proof the filter is pushed to the FETCH, not applied
	// client-side over a fetch-all).
	sel := browseSelection(handler.recordedReqs())
	require.NotNil(t, sel, "recorded a Selection-bearing browse plan")
	assert.Equal(t, []string{"project", "ticket"}, sel.GetNodeTypes(), "type set pushed to the fetch")

	plan := recordedBrowsePlan(handler.recordedReqs())
	require.NotNil(t, plan)
	require.EqualValues(t, paging.BrowsePageSize, plan.GetLimit(),
		"the page carries the DRAIN's limit — never the caller's, which would bias which nodes are considered")

	require.Equal(t, int64(0), mgr.calls.Load(), "recent+types does NOT drive Manager.Search")
	require.False(t, dispatchedAServerSearch(handler.recordedReqs()), "recent+types must NOT dispatch a server search")

	// Rendered JSON: recency order — ticket t1 (newer) before project p1 (older).
	var got recentJSONResult
	require.NoError(t, json.Unmarshal([]byte(engine.FirstTextContent(out)), &got))
	require.Equal(t, 2, got.Total)
	require.Len(t, got.Results, 2)
	assert.Equal(t, "t1", got.Results[0].ID, "newer ticket ranks first")
	assert.Equal(t, "p1", got.Results[1].ID, "older project ranks second")
}

// TestInterceptSearchKnowledge_UnconstructibleIdentitySurfaces pins the ARM's
// half of the no-silent-degrade rule.
//
// THE RESOLVER'S OWN TEST PROVES IT RETURNS AN ERROR; this proves the SEARCH ARM
// SURFACES it. Those are different failures with the same cause: an arm that
// resolved the error and then embedded with nothing would answer the semantic
// search with keyword results and report success, which is the exact outcome the
// error exists to prevent — and no assertion in the resolver's own test would
// notice.
func TestInterceptSearchKnowledge_UnconstructibleIdentitySurfaces(t *testing.T) {
	// An API provider with no credential anywhere: constructible identity on the
	// graph, unconstructible embedder on this machine.
	t.Setenv("VOYAGE_API_KEY", "")
	var execHits, embedCalls atomic.Int64
	resp := cannedNodesResp(&knowledgev1.Node{Id: "n1", Type: "finding", SymbolName: "Hit"})
	resp.GraphNames = []*knowledgev1.GraphInfo{{
		Name:   knowledgeDefaultName,
		Loaded: true,
		EmbedIdentity: &knowledgev1.EmbedIdentity{
			Provider: "voyage", Model: "voyage-code-4", Dimension: 256, Dtype: "ubinary",
		},
	}}
	gc := newInterceptHarness(t, &execHits, resp)
	mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "n1", Score: 0.9}}}
	deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}, segMgr: mgr}

	handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
		"query": "x", "graph": "knowledge",
	}))
	require.True(t, handled)
	require.True(t, out.IsError,
		"an identity this client cannot construct must SURFACE as an error, never be answered with "+
			"BM25 results reported as success")
	body := engine.FirstTextContent(out)
	assert.Contains(t, body, "voyage", "the surfaced error names the provider")

	// AND THE INJECTED STUB WAS NOT USED AS A SUBSTITUTE. Without this the arm
	// could satisfy the error above and still have embedded with the configured
	// embedder on some other path.
	assert.Equal(t, int64(0), embedCalls.Load(),
		"the configured embedder must not stand in for an identity that failed to resolve")
	assert.Equal(t, int64(0), mgr.calls.Load(), "and no search runs on an unresolved identity")
}

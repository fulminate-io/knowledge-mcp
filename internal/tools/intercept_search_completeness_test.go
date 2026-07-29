// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestInterceptSearch_CloudCICDClientServed is the SEARCH-tool completeness
// criterion for the targeted-account graphs: search(graph in {cloud,cicd}) is
// served by the CLIENT engine (Manager.Search → hydrate), never a server
// RETURN_MODE_SEARCH. Practice is no longer a targeted single-graph search — it
// ALWAYS fans out, so its coverage lives in TestInterceptSearch_PracticeClientServedViaFanOut.
func TestInterceptSearch_CloudCICDClientServed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   map[string]any
		wantGT kgtypes.GraphType
		want   string
	}{
		{"cloud", map[string]any{"graph": "cloud", "account": "acct", "query": "x"}, kgtypes.GraphCloud, "acct"},
		{"cicd", map[string]any{"graph": "cicd", "account": "org", "query": "x"}, kgtypes.GraphCICD, "org"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var execHits atomic.Int64
			gc, handler := newInterceptHarnessWithHandler(t, &execHits, &knowledgev1.ExecuteResponse{})
			mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{}}
			deps := &interceptDeps{gc: gc, segMgr: mgr}

			handled, out := InterceptSearch(opCtx(), deps, searchParams(t, tc.args))
			require.True(t, handled, "%s search must be claimed client-side", tc.name)
			require.False(t, out.IsError, "%s: %v", tc.name, engine.FirstTextContent(out))

			require.Equal(t, int64(1), mgr.calls.Load(), "%s drove the CLIENT engine", tc.name)
			require.Equal(t, tc.wantGT, mgr.lastGT)
			require.Equal(t, tc.want, mgr.lastName)
			require.False(t, dispatchedAServerSearch(handler.recordedReqs()),
				"%s search must NOT dispatch a server search", tc.name)
		})
	}
}

// TestInterceptSearch_PracticeClientServedViaFanOut is the SEARCH-tool
// completeness criterion for practice: search(graph:practice) is served by the
// CLIENT engine across EVERY loaded practice graph (fan-out), never a server
// RETURN_MODE_SEARCH. Practice always fans out, so it is driven against the
// fan-out-aware harness (>=2 graph names enumerated, per-graph hits) rather than
// the single-graph dispatch harness.
func TestInterceptSearch_PracticeClientServedViaFanOut(t *testing.T) {
	gc, handler := newFanOutHarnessWithHandler(t, []string{"go", "python"},
		practiceNode("p:go", "GoWorkerPool", "bounded goroutines"),
		practiceNode("p:py", "PyThreadPool", "thread pool executor"),
	)
	mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
		"go":     {{ID: "p:go", Score: 0.90}},
		"python": {{ID: "p:py", Score: 0.70}},
	})
	deps := &interceptDeps{gc: gc, segMgr: mgr}

	handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{"graph": "practice", "query": "x"}))
	require.True(t, handled, "practice search must be claimed client-side")
	require.False(t, out.IsError, "%v", engine.FirstTextContent(out))

	// The CLIENT engine was driven across BOTH enumerated practice graphs.
	require.Equal(t, []string{"go", "python"}, mgr.searchedNames(), "fan-out searched every practice graph")
	// No server ranked search — the only server reqs are GRAPH_NAMES + ids[] hydrate.
	require.False(t, dispatchedAServerSearch(handler.recordedReqs()),
		"practice fan-out must NOT dispatch a server search")
}

// TestInterceptQueryKnowledgeSearch_RecentClientServed is the Phase 2
// temporal criterion: query(mode:recent) is served by the CLIENT knowledge engine
// (Manager.Search → hydrate → UpdatedAt rerank), never a server RETURN_MODE_SEARCH.
func TestInterceptQueryKnowledgeSearch_RecentClientServed(t *testing.T) {
	var execHits atomic.Int64
	gc, handler := newInterceptHarnessWithHandler(t, &execHits, cannedNodesResp(
		&knowledgev1.Node{Id: "n1", Type: "finding", SymbolName: "RecentHit", UpdatedAt: 1},
	))
	mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "n1", Score: 0.9}}}
	deps := &interceptDeps{gc: gc, segMgr: mgr}

	handled, out := InterceptQueryKnowledgeSearch(opCtx(), deps, queryParams(t, map[string]any{
		"graph": "knowledge", "mode": "recent", "text": "fresh changes",
	}))
	require.True(t, handled, "mode=recent is claimed client-side")
	require.False(t, out.IsError, "%v", engine.FirstTextContent(out))

	require.Equal(t, int64(1), mgr.calls.Load(), "recent drove the CLIENT knowledge engine")
	require.Equal(t, kgtypes.GraphKnowledge, mgr.lastGT)
	require.False(t, dispatchedAServerSearch(handler.recordedReqs()),
		"recent must NOT dispatch a server search")
	assert.Contains(t, engine.FirstTextContent(out), "RecentHit")
}

// TestInterceptQueryKnowledgeSearch_TextAndDefaultTextClientServed is the
// Phase 2 reroute criterion: query mode=text AND the default mode carrying a text
// query are both served by the CLIENT knowledge engine, never a server search.
func TestInterceptQueryKnowledgeSearch_TextAndDefaultTextClientServed(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"mode=text", map[string]any{"graph": "knowledge", "mode": "text", "text": "auth"}},
		{"default-text", map[string]any{"graph": "knowledge", "text": "auth"}}, // empty mode + text
		{"default-text-empty-graph", map[string]any{"text": "auth"}},           // graph "" = knowledge
	} {
		t.Run(tc.name, func(t *testing.T) {
			var execHits atomic.Int64
			gc, handler := newInterceptHarnessWithHandler(t, &execHits, &knowledgev1.ExecuteResponse{})
			mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{}}
			deps := &interceptDeps{gc: gc, segMgr: mgr}

			handled, out := InterceptQueryKnowledgeSearch(opCtx(), deps, queryParams(t, tc.args))
			require.True(t, handled, "%s claimed client-side", tc.name)
			require.False(t, out.IsError, "%s empty knowledge → graceful empty: %v", tc.name, engine.FirstTextContent(out))

			require.Equal(t, int64(1), mgr.calls.Load(), "%s drove the CLIENT engine", tc.name)
			require.Equal(t, kgtypes.GraphKnowledge, mgr.lastGT)
			require.False(t, dispatchedAServerSearch(handler.recordedReqs()),
				"%s must NOT dispatch a server search", tc.name)
		})
	}
}

// TestPivotSeedSearch_ClientServed is the Phase 2 pivot-seed criterion:
// the pivot text-seed gathers its candidate node set via the CLIENT segment engine
// (mgr.Search + hydrate), never a server RETURN_MODE_SEARCH. An empty knowledge
// graph yields no candidates → graceful empty pivot.
func TestPivotSeedSearch_ClientServed(t *testing.T) {
	var execHits atomic.Int64
	gc, handler := newInterceptHarnessWithHandler(t, &execHits, &knowledgev1.ExecuteResponse{})
	mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{}}
	deps := &interceptDeps{gc: gc, segMgr: mgr}

	res := composePivot(context.Background(), deps, queryArgs{
		Mode: "pivot", Graph: "knowledge", Rows: "type", Cols: "status", Text: "seed query",
	})
	require.False(t, res.IsError, engine.FirstTextContent(res))
	require.Equal(t, int64(1), mgr.calls.Load(), "pivot seed drove the CLIENT engine")
	require.Equal(t, kgtypes.GraphKnowledge, mgr.lastGT)
	require.False(t, dispatchedAServerSearch(handler.recordedReqs()),
		"pivot seed must NOT dispatch a server search")
}

// TestInterceptQueryKnowledgeSearch_GateMisses asserts the claim falls through
// (handled=false) for non-recent modes and non-knowledge graphs so the rest of
// the chain owns them. NOTE: bare empty-text recent is NO LONGER a gate-miss — it
// is now served client-side as a temporal browse (composeRecentBrowse), covered by
// TestInterceptQueryKnowledgeSearch_BareRecentTemporalBrowse; only NON-recent
// empty-text modes still fall through to precheck/deny.
func TestInterceptQueryKnowledgeSearch_GateMisses(t *testing.T) {
	deps := &interceptDeps{segMgr: &fakeSegmentSearcher{}}
	for _, args := range []map[string]any{
		{"graph": "practice", "mode": "recent", "text": "x"},                  // non-knowledge graph
		{"graph": "knowledge", "mode": "recent", "session": "s", "text": "x"}, // thought filter
		{"graph": "knowledge", "mode": "stats"},                               // non-search mode
		{"graph": "knowledge", "id": "n1"},                                    // default-mode getNode (not text)
		{"graph": "knowledge", "type": "finding"},                             // default-mode type-browse (not text)
	} {
		handled, _ := InterceptQueryKnowledgeSearch(opCtx(), deps, queryParams(t, args))
		assert.False(t, handled, "args %v must fall through", args)
	}
}

// TestInterceptSearch_LinkageWebPDFRetired is the SEARCH-tool retirement
// criterion: search(graph in {linkage,web,pdf}) returns the ranked-search-retired
// result and dispatches NOTHING (no server search, no RPC at all).
func TestInterceptSearch_LinkageWebPDFRetired(t *testing.T) {
	for _, graph := range []string{"linkage", "web", "pdf"} {
		t.Run(graph, func(t *testing.T) {
			var execHits atomic.Int64
			gc, handler := newInterceptHarnessWithHandler(t, &execHits, &knowledgev1.ExecuteResponse{})
			mgr := &fakeSegmentSearcher{}
			deps := &interceptDeps{gc: gc, segMgr: mgr}

			handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{"graph": graph, "query": "x"}))
			require.True(t, handled, "%s search is claimed (and retired)", graph)
			require.False(t, out.IsError)

			body := engine.FirstTextContent(out)
			assert.Contains(t, body, "retired", "%s ranked search returns the retired result", graph)
			assert.Contains(t, body, graph, "the retired message names the graph")

			require.Equal(t, int64(0), mgr.calls.Load(), "%s retired arm hits no client engine", graph)
			require.Empty(t, handler.recordedReqs(), "%s retired arm dispatches no RPC", graph)
		})
	}
}

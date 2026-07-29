// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestRegisteredGraphSearch_SearchToolRoutesToClientEngine proves the SEARCH-tool
// arm: search(graph:<custom>, name:<n>, query:...) is claimed by
// interceptSearchReducibleGraph and routed to composeRegisteredGraphSearch — it
// drives the CLIENT segment engine (Manager.Search keyed on (gt, name) → hydrate)
// and NEVER dispatches a server RETURN_MODE_SEARCH (the retired path that returns
// 0 hits for a custom graph).
func TestRegisteredGraphSearch_SearchToolRoutesToClientEngine(t *testing.T) {
	var execHits, embedCalls atomic.Int64
	gc, handler := newInterceptHarnessWithHandler(t, &execHits, cannedNodesResp(
		&knowledgev1.Node{Id: "h1", Type: "fact", SymbolName: "HelloWorld"},
	))
	mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "h1", Score: 0.9}}}
	deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}, segMgr: mgr}

	handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
		"graph": "hellograph",
		"name":  "demo",
		"query": "world",
	}))
	require.True(t, handled, "a registered custom graph search is claimed client-side")
	require.False(t, out.IsError, "render is not an error: %v", engine.FirstTextContent(out))

	// The CLIENT engine ran against the right (gt, name) instance key.
	require.Equal(t, int64(1), mgr.calls.Load(), "Manager.Search drove the custom-graph arm")
	require.Equal(t, kgtypes.GraphType("hellograph"), mgr.lastGT)
	require.Equal(t, "demo", mgr.lastName)
	require.Equal(t, "world", mgr.lastText)
	assert.NotEmpty(t, mgr.lastVec, "the client-embedded query vector reached the HNSW arm")

	// No SERVER search dispatch — the only Execute is the ids[] hydrate read.
	require.False(t, dispatchedAServerSearch(handler.recordedReqs()),
		"custom-graph search must NOT dispatch a server RETURN_MODE_SEARCH")

	// The hydrated hit rendered.
	assert.Contains(t, engine.FirstTextContent(out), "HelloWorld")
}

// TestRegisteredGraphSearch_EmptyNameGracefulEmpty proves an un-collected / empty
// instance key renders zero results cleanly (Manager.Search over an unkeyed set
// returns no hits, NOT an error) — graceful empty, the cloud-arm contract.
func TestRegisteredGraphSearch_EmptyNameGracefulEmpty(t *testing.T) {
	var execHits, embedCalls atomic.Int64
	gc, handler := newInterceptHarnessWithHandler(t, &execHits, &knowledgev1.ExecuteResponse{})
	mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{}}
	deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}, segMgr: mgr}

	handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
		"graph": "hellograph",
		"query": "world", // no name → empty instance key
	}))
	require.True(t, handled)
	require.False(t, out.IsError, "empty name renders zero results, not an error")
	require.Equal(t, int64(1), mgr.calls.Load(), "Manager.Search still ran (returned empty)")
	require.Empty(t, mgr.lastName, "empty name threaded through verbatim")
	require.False(t, dispatchedAServerSearch(handler.recordedReqs()),
		"empty-name custom search must NOT dispatch a server search")
}

// TestRegisteredGraphSearch_BuiltinGraphNotClaimed proves the search arm gate is
// scoped to NON-builtin graphs: a builtin graph (practice) is NOT claimed by the
// custom-graph default branch (it stays on its own reducible arm), so the
// custom-graph Manager.Search key is never the builtin's.
func TestRegisteredGraphSearch_BuiltinGraphNotClaimed(t *testing.T) {
	var execHits, embedCalls atomic.Int64
	gc := newInterceptHarness(t, &execHits, &knowledgev1.ExecuteResponse{})
	mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{}}
	deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}, segMgr: mgr}

	// practice is a builtin reducible graph: it must be served by its OWN fan-out
	// arm, never the custom-graph default branch. The custom branch would key
	// Manager.Search on the RAW GraphType("practice"); the practice fan-out keys it
	// on GraphPractice (a per-language scatter, which with no loaded practice graphs
	// may not call Search at all, leaving lastGT zero). Either way, the custom branch
	// must NOT be what claimed it — so lastGT is never the raw custom-keyed
	// GraphType("practice").
	handled, _ := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
		"graph": "practice",
		"query": "x",
	}))
	require.True(t, handled, "practice is claimed by its own reducible arm")
	require.NotEqual(t, kgtypes.GraphType("practice"), mgr.lastGT,
		"practice must NOT be claimed by the custom-graph branch")
}

// TestInterceptQueryRegisteredGraphSearch proves the QUERY-tool arm: a custom-graph
// hybrid/text query drives composeRegisteredGraphSearch (Manager.Search keyed on
// (gt, name)) with NO server dispatch, while builtin-graph, id-shape, and empty-text
// cases fall through (handled=false) so the chain proceeds.
func TestInterceptQueryRegisteredGraphSearch(t *testing.T) {
	t.Run("custom hybrid query routes to the client engine", func(t *testing.T) {
		var execHits, embedCalls atomic.Int64
		gc, handler := newInterceptHarnessWithHandler(t, &execHits, cannedNodesResp(
			&knowledgev1.Node{Id: "h1", Type: "fact", SymbolName: "HelloWorld"},
		))
		mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "h1", Score: 0.9}}}
		deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}, segMgr: mgr}

		// mode default (hybrid) carrying ONLY a text query is a claimed text-search shape.
		handled, out := InterceptQueryRegisteredGraphSearch(opCtx(), deps, queryParams(t, map[string]any{
			"graph": "hellograph",
			"name":  "demo",
			"mode":  "hybrid",
			"text":  "world",
		}))
		require.True(t, handled, "custom-graph hybrid text query is claimed")
		require.False(t, out.IsError)
		require.Equal(t, int64(1), mgr.calls.Load(), "Manager.Search drove the query-tool custom arm")
		require.Equal(t, kgtypes.GraphType("hellograph"), mgr.lastGT)
		require.Equal(t, "demo", mgr.lastName)
		require.Equal(t, "world", mgr.lastText)
		require.False(t, dispatchedAServerSearch(handler.recordedReqs()),
			"query-tool custom search must NOT dispatch a server RETURN_MODE_SEARCH")
		assert.Contains(t, engine.FirstTextContent(out), "HelloWorld")
	})

	t.Run("mode=text custom query is claimed", func(t *testing.T) {
		var execHits, embedCalls atomic.Int64
		gc := newInterceptHarness(t, &execHits, cannedNodesResp())
		mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{}}
		deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}, segMgr: mgr}

		handled, _ := InterceptQueryRegisteredGraphSearch(opCtx(), deps, queryParams(t, map[string]any{
			"graph": "hellograph", "name": "demo", "mode": "text", "text": "world",
		}))
		require.True(t, handled, "mode=text custom query is claimed")
		require.Equal(t, int64(1), mgr.calls.Load())
	})

	t.Run("builtin graph falls through", func(t *testing.T) {
		var execHits, embedCalls atomic.Int64
		gc := newInterceptHarness(t, &execHits, cannedNodesResp())
		mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{}}
		deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}, segMgr: mgr}

		// knowledge is a builtin → owned by InterceptQueryKnowledgeSearch, not here.
		handled, _ := InterceptQueryRegisteredGraphSearch(opCtx(), deps, queryParams(t, map[string]any{
			"graph": "knowledge", "mode": "hybrid", "text": "world",
		}))
		require.False(t, handled, "a builtin graph is not claimed by the custom arm")
		require.Equal(t, int64(0), mgr.calls.Load(), "no client engine call on a fall-through")
	})

	t.Run("id-shape query falls through", func(t *testing.T) {
		var execHits, embedCalls atomic.Int64
		gc := newInterceptHarness(t, &execHits, cannedNodesResp())
		mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{}}
		deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}, segMgr: mgr}

		// An id getNode is a browse/getNode shape, not a text search → fall through,
		// even when a text field is also present (the id signal wins). Default mode.
		handled, _ := InterceptQueryRegisteredGraphSearch(opCtx(), deps, queryParams(t, map[string]any{
			"graph": "hellograph", "name": "demo", "id": "h1", "text": "world",
		}))
		require.False(t, handled, "an id-shape query is not a text search")
		require.Equal(t, int64(0), mgr.calls.Load())
	})

	t.Run("empty text falls through", func(t *testing.T) {
		var execHits, embedCalls atomic.Int64
		gc := newInterceptHarness(t, &execHits, cannedNodesResp())
		mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{}}
		deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}, segMgr: mgr}

		handled, _ := InterceptQueryRegisteredGraphSearch(opCtx(), deps, queryParams(t, map[string]any{
			"graph": "hellograph", "name": "demo", "mode": "text",
		}))
		require.False(t, handled, "empty text → precheck/deny owns the message")
		require.Equal(t, int64(0), mgr.calls.Load())
	})
}

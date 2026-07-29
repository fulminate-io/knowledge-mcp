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

// TestCapstone_RegisteredGraphBothToolsReturnHits is PART B of the end-to-end
// capstone (the read-tool half): with a CLIENT segment engine seeded for
// (hellograph, demo), BOTH the search tool (search graph:hellograph name:demo
// query:world) AND the query tool (query graph:hellograph name:demo mode:hybrid
// text:world) return the seeded content (symbol_name "world") — and NEITHER goes
// through a server RETURN_MODE_SEARCH dispatch (the retired path that returns 0
// hits for a custom graph).
//
// EACH half is FAILS-WHEN-ABSENT: reverting the search-tool arm
// (interceptSearchReducibleGraph custom branch) makes the search subtest fall to
// the knowledge/server tail and return empty; reverting the query-tool arm
// (InterceptQueryRegisteredGraphSearch registration) makes the query subtest
// engine.Dispatch to the retired server search. Verified by temporarily reverting
// each arm during development.
//
// The LIVE hellograph:demo verification post-deploy is the true end-to-end and is
// deferred to the orchestrator (the deploy-then-live-verify deferral pattern).
func TestCapstone_RegisteredGraphBothToolsReturnHits(t *testing.T) {
	const (
		customGraph = "hellograph"
		customName  = "demo"
		hitID       = "world-node"
		hitSymbol   = "world"
	)

	// The seeded hit the client engine ranks + the node the hydrate read returns.
	newDeps := func(t *testing.T) (*interceptDeps, *fakeSegmentSearcher, *dispatchEngineHandler) {
		t.Helper()
		var execHits, embedCalls atomic.Int64
		gc, handler := newInterceptHarnessWithHandler(t, &execHits, cannedNodesResp(
			&knowledgev1.Node{Id: hitID, Type: "fact", SymbolName: hitSymbol},
		))
		mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: hitID, Score: 0.9}}}
		return &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}, segMgr: mgr}, mgr, handler
	}

	t.Run("search tool returns the seeded content via the client engine", func(t *testing.T) {
		deps, mgr, handler := newDeps(t)

		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": customGraph,
			"name":  customName,
			"query": hitSymbol,
		}))
		require.True(t, handled)
		require.False(t, out.IsError)
		require.Equal(t, kgtypes.GraphType(customGraph), mgr.lastGT)
		require.Equal(t, customName, mgr.lastName)
		require.False(t, dispatchedAServerSearch(handler.recordedReqs()),
			"search tool must NOT dispatch a server search for a custom graph")
		assert.Contains(t, engine.FirstTextContent(out), hitSymbol,
			"the seeded content is returned (search tool)")
	})

	t.Run("query tool returns the seeded content via the client engine", func(t *testing.T) {
		deps, mgr, handler := newDeps(t)

		handled, out := InterceptQueryRegisteredGraphSearch(opCtx(), deps, queryParams(t, map[string]any{
			"graph": customGraph,
			"name":  customName,
			"mode":  "hybrid",
			"text":  hitSymbol,
		}))
		require.True(t, handled)
		require.False(t, out.IsError)
		require.Equal(t, kgtypes.GraphType(customGraph), mgr.lastGT)
		require.Equal(t, customName, mgr.lastName)
		require.False(t, dispatchedAServerSearch(handler.recordedReqs()),
			"query tool must NOT dispatch a server search for a custom graph")
		assert.Contains(t, engine.FirstTextContent(out), hitSymbol,
			"the seeded content is returned (query tool)")
	})
}

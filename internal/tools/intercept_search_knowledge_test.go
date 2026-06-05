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
	gc, handler := newInterceptHarnessWithHandler(t, &execHits, cannedNodesResp(
		&knowledgev1.Node{Id: "n1", Type: "finding", SymbolName: "FirstHit"},
		&knowledgev1.Node{Id: "n2", Type: "finding", SymbolName: "SecondHit"},
	))
	mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{
		{ID: "n1", Score: 0.9},
		{ID: "n2", Score: 0.8},
	}}
	deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}, segMgr: mgr}

	handled, out := InterceptSearch(deps, searchParams(t, map[string]any{"query": "x", "graph": "knowledge"}))
	require.True(t, handled)
	require.False(t, out.IsError, "result is not an error: %v", engine.FirstTextContent(out))

	// The CLIENT engine ran, with a query vector (HNSW arm exercised).
	require.Equal(t, int64(1), mgr.calls.Load(), "Manager.Search drove the knowledge arm")
	require.Equal(t, kgtypes.GraphKnowledge, mgr.lastGT)
	require.Equal(t, knowledgeDefaultName, mgr.lastName)
	require.NotEmpty(t, mgr.lastVec, "client-embedded query vector reached the HNSW arm")

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

	handled, out := InterceptSearch(deps, searchParams(t, map[string]any{"query": "x", "graph": "knowledge"}))
	require.True(t, handled)
	require.False(t, out.IsError, "result is not an error: %v", engine.FirstTextContent(out))

	require.Equal(t, int64(1), mgr.calls.Load(), "knowledge arm drove the CLIENT engine unconditionally")
	require.Empty(t, mgr.lastVec, "no embedder → BM25-only arm (empty query vector)")
	require.False(t, dispatchedAServerSearch(handler.recordedReqs()), "knowledge arm must NOT dispatch a server search")
	assert.Contains(t, engine.FirstTextContent(out), "BM25Hit")
}

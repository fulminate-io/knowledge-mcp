// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestInterceptManage_RebuildBM25_SingleNamedGraph asserts a single named graph
// issues EXACTLY ONE rebuild Index RPC and NO separate persist op (durability is
// server-side per T-GTB1b).
func TestInterceptManage_RebuildBM25_SingleNamedGraph(t *testing.T) {
	ix := &fakeIndexer{}
	handled, res := manageCall(t, ix,
		`{"operation":"rebuild_bm25","graph":"cloud","name":"acct-1"}`)
	require.True(t, handled)
	require.False(t, res.IsError, "rebuild: %s", toolResultText(res))

	require.EqualValues(t, 1, ix.indexCalls.Load(), "exactly one Index RPC, no separate persist op")
	reqs := ix.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, knowledgev1.IndexRequest_INDEX_OP_REBUILD_BM25, reqs[0].GetOperation())
	assert.Equal(t, "cloud", reqs[0].GetTarget().GetGraph())
	assert.Equal(t, "acct-1", reqs[0].GetTarget().GetName())
	assert.Contains(t, toolResultText(res), `BM25 index rebuilt for cloud graph "acct-1" in`)
}

// TestInterceptManage_RebuildHNSW_Knowledge asserts the knowledge-root rebuild
// targets the nil-name knowledge selector (the server skips persist for the root
// graph — it flushes via its root saver) and renders the knowledge-graph ack.
func TestInterceptManage_RebuildHNSW_Knowledge(t *testing.T) {
	ix := &fakeIndexer{}
	handled, res := manageCall(t, ix, `{"operation":"rebuild_hnsw"}`)
	require.True(t, handled)
	require.False(t, res.IsError, "rebuild: %s", toolResultText(res))

	require.EqualValues(t, 1, ix.indexCalls.Load())
	reqs := ix.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, knowledgev1.IndexRequest_INDEX_OP_REBUILD_HNSW, reqs[0].GetOperation())
	assert.Empty(t, reqs[0].GetTarget().GetName(), "knowledge root rebuild carries no name (server skips persist)")
	assert.Contains(t, toolResultText(res), "HNSW index rebuilt for knowledge graph in")
}

// TestInterceptManage_RebuildBM25_MultiGraphParallelFanOut asserts the empty-name
// 'rebuild all practice graphs' path resolves the graph list client-side then
// issues ONE rebuild Index RPC PER resolved graph (counting fake), NOT a serial
// per-graph loop (the bounded fan-out invariant). Three resolved graphs → three
// Index RPCs, all REBUILD_BM25, one per practice graph name.
func TestInterceptManage_RebuildBM25_MultiGraphParallelFanOut(t *testing.T) {
	ix := &fakeIndexer{listGraphsBody: `{"graphs":[
		{"graph_type":"practice","graph_name":"go"},
		{"graph_type":"practice","graph_name":"python"},
		{"graph_type":"practice","graph_name":"typescript"}
	]}`}
	handled, res := manageCall(t, ix,
		`{"operation":"rebuild_bm25","graph":"practice"}`)
	require.True(t, handled)
	require.False(t, res.IsError, "rebuild: %s", toolResultText(res))

	require.EqualValues(t, 3, ix.indexCalls.Load(), "one rebuild Index RPC per resolved practice graph")
	reqs := ix.requests()
	require.Len(t, reqs, 3)
	gotNames := map[string]bool{}
	for _, r := range reqs {
		assert.Equal(t, knowledgev1.IndexRequest_INDEX_OP_REBUILD_BM25, r.GetOperation())
		assert.Equal(t, "practice", r.GetTarget().GetGraph())
		gotNames[r.GetTarget().GetLanguage()] = true
	}
	assert.True(t, gotNames["go"] && gotNames["python"] && gotNames["typescript"], "one RPC per practice graph")
	assert.Contains(t, toolResultText(res), "BM25 index rebuilt for 3 practice graph(s) in")
}

// TestInterceptManage_RebuildBM25_CloudRequiresName asserts cloud/cicd rebuild
// requires a name (no all-cloud fan-out — the account key is mandatory).
func TestInterceptManage_RebuildBM25_CloudRequiresName(t *testing.T) {
	ix := &fakeIndexer{}
	handled, res := manageCall(t, ix, `{"operation":"rebuild_bm25","graph":"cloud"}`)
	require.True(t, handled)
	assert.True(t, res.IsError, "cloud rebuild without name must error")
	assert.Empty(t, ix.requests(), "no Index RPC when the required name is missing")
}

// SPDX-License-Identifier: Apache-2.0

package tools

// search_mode_honor_test.go pins the search tool's contract for the declared
// `mode` vocabulary on the knowledge arm: which retrieval arms run, which
// pre-steps are suppressed, which conflicting payloads are refused, and what
// the rendered footer discloses.
//
// EVERY test here begins with t.Setenv("VOYAGE_API_KEY", ""). That is a
// precondition, not hygiene: the key resolver falls through to the process
// environment whenever the config value is empty, which is what an unloaded
// config singleton produces under `go test`, and the variable is set on
// developer machines. Without the Setenv these tests would inherit a real key,
// issue billed rerank calls, and assert against wire limits the rerank rewrite
// has already widened — so the observations below would be measuring a
// different code path than the one they name.

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// modeHonorNodes is the two-row hydrate fixture the mode tests share.
func modeHonorNodes() []*knowledgev1.Node {
	return []*knowledgev1.Node{
		{Id: "m1", Type: "finding", SymbolName: "ModeRowOne"},
		{Id: "m2", Type: "finding", SymbolName: "ModeRowTwo"},
	}
}

// modeHonorHits ranks both fixture rows.
func modeHonorHits() []searchengine.Hit {
	return []searchengine.Hit{{ID: "m1", Score: 0.9}, {ID: "m2", Score: 0.8}}
}

// newModeHonorDeps wires the knowledge-arm fixture: recording GraphClient for
// the ids[] hydrate read, fake segment engine, and a stub embedder whose call
// counter the caller reads to prove an embed did or did not happen. Pass
// withEmbedder=false for the no-semantic-index case.
func newModeHonorDeps(
	t *testing.T, nodes []*knowledgev1.Node, hits []searchengine.Hit, withEmbedder bool,
) (*interceptDeps, *fakeSegmentSearcher, *atomic.Int64) {
	t.Helper()
	var execHits, embedCalls atomic.Int64
	gc := newInterceptHarness(t, &execHits, cannedNodesResp(nodes...))
	mgr := &fakeSegmentSearcher{hits: hits}
	deps := &interceptDeps{gc: gc, segMgr: mgr}
	if withEmbedder {
		deps.emb = stubEmbedder{calls: &embedCalls}
	}
	return deps, mgr, &embedCalls
}

// TestInterceptSearch_ModeTextSuppressesEmbedAndRerankRewrite pins the ticket's
// headline: mode:"text" means BM25 only. No query is embedded, no vector
// reaches the segment engine, and the footer says so rather than claiming a
// vector arm ran.
func TestInterceptSearch_ModeTextSuppressesEmbedAndRerankRewrite(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")
	deps, mgr, embedCalls := newModeHonorDeps(t, modeHonorNodes(), modeHonorHits(), true)

	handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
		"graph": "knowledge", "mode": "text", "query": "gate56-probe",
	}))
	require.True(t, handled)
	require.False(t, out.IsError, "render is not an error: %v", engine.FirstTextContent(out))

	assert.Equal(t, int64(0), embedCalls.Load(), "mode:text must not embed the query")
	assert.Empty(t, mgr.lastVec, "no vector may reach the segment engine under mode:text")
	assert.Contains(t, engine.FirstTextContent(out), "_search mode: BM25-only_")
}

// TestInterceptSearch_ModeTextConflictingParamsRefused covers the two payloads
// that ask for BM25-only retrieval and a vector operation in the same breath.
// Serving either silently would honor one half of the request and drop the
// other, so both are refused by name.
func TestInterceptSearch_ModeTextConflictingParamsRefused(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")

	t.Run("rerank_true", func(t *testing.T) {
		deps, _, _ := newModeHonorDeps(t, modeHonorNodes(), modeHonorHits(), true)
		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": "knowledge", "mode": "text", "query": "x", "rerank": true,
		}))
		require.True(t, handled)
		require.True(t, out.IsError, "mode:text with rerank:true must be refused, not served")
		msg := engine.FirstTextContent(out)
		assert.Contains(t, msg, "mode", "the refusal names the mode param")
		assert.Contains(t, msg, "rerank", "the refusal names the conflicting param")
	})

	t.Run("query_vector_supplied", func(t *testing.T) {
		deps, _, _ := newModeHonorDeps(t, modeHonorNodes(), modeHonorHits(), true)
		// Supplied as raw bytes: encoding/json base64-encodes a []byte, which is
		// the wire shape. A hand-written base64 string would test the decoder
		// rather than the refusal.
		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": "knowledge", "mode": "text", "query": "x",
			"query_vector": make([]byte, 32),
		}))
		require.True(t, handled)
		require.True(t, out.IsError, "mode:text with a supplied query_vector must be refused")
		msg := engine.FirstTextContent(out)
		assert.Contains(t, msg, "mode")
		assert.Contains(t, msg, "query_vector")
	})
}

// TestInterceptSearch_ModeTextClampsCallerLimit proves the declared caller
// maximum binds on this arm too: a limit of 500 reaches the segment engine as
// the declared ceiling, and the caller is told the clamp engaged.
func TestInterceptSearch_ModeTextClampsCallerLimit(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")
	deps, mgr, _ := newModeHonorDeps(t, modeHonorNodes(), modeHonorHits(), true)

	handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
		"graph": "knowledge", "mode": "text", "query": "x", "limit": 500,
	}))
	require.True(t, handled)
	require.False(t, out.IsError, "render is not an error: %v", engine.FirstTextContent(out))

	assert.Equal(t, rerankCallerLimitCeiling, mgr.lastK,
		"the declared caller maximum is what reaches the segment engine")
	// The disclosure rides as a SEPARATE content block so a json body stays
	// parseable, so this reads every block rather than only the first.
	assert.True(t, blocksCarryClampNotice(out.Content),
		"a clamp the caller is not told about is a silent narrowing")
}

// TestInterceptSearch_ModeVectorSkipsTheBM25Arm pins the vector-only arm: the
// engine receives the embedding and an EMPTY query text, so the BM25 segment
// returns no hits and the fusion reduces to the vector ranking.
func TestInterceptSearch_ModeVectorSkipsTheBM25Arm(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")
	deps, mgr, _ := newModeHonorDeps(t, modeHonorNodes(), modeHonorHits(), true)

	handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
		"graph": "knowledge", "mode": "vector", "query": "x",
	}))
	require.True(t, handled)
	require.False(t, out.IsError, "render is not an error: %v", engine.FirstTextContent(out))

	assert.Empty(t, mgr.lastText, "mode:vector must not drive the BM25 arm with query text")
	assert.NotEmpty(t, mgr.lastVec, "mode:vector must still supply the embedding")
}

// TestInterceptSearch_ModeVectorWithoutEmbedderRefused covers the install with
// no semantic index: serving mode:vector there renders zero rows, which reads
// as "no matches" when the truth is "no vector arm available". The refusal
// makes the difference legible.
func TestInterceptSearch_ModeVectorWithoutEmbedderRefused(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")
	deps, _, _ := newModeHonorDeps(t, modeHonorNodes(), modeHonorHits(), false)

	handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
		"graph": "knowledge", "mode": "vector", "query": "x",
	}))
	require.True(t, handled)
	require.True(t, out.IsError, "mode:vector with no embedder must be refused, not served empty")
	msg := engine.FirstTextContent(out)
	assert.Contains(t, msg, "vector", "the refusal names the requested mode")
	assert.Contains(t, msg, "embedder", "the refusal names what is missing")
}

// TestInterceptSearch_ModeTemporalAppliesTheRecencyRerank closes the gap
// between what the tool declares and what it runs: the schema publishes
// recent/temporal as one recency boost, so temporal must apply the same
// UpdatedAt half-life rerank recent does.
func TestInterceptSearch_ModeTemporalAppliesTheRecencyRerank(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")
	now := time.Now()
	nodes := []*knowledgev1.Node{
		{Id: "m1", Type: "finding", SymbolName: "FreshRow", UpdatedAt: now.UnixNano()},
		{Id: "m2", Type: "finding", SymbolName: "StaleRow", UpdatedAt: now.Add(-3650 * 24 * time.Hour).UnixNano()},
	}
	// BASE order is the INVERSE of the recency order, so a rerank that ran
	// inverts the pair and one that did not leaves it alone.
	hits := []searchengine.Hit{{ID: "m2", Score: 0.9}, {ID: "m1", Score: 0.8}}
	deps, _, _ := newModeHonorDeps(t, nodes, hits, true)

	handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
		"graph": "knowledge", "mode": "temporal", "query": "x",
	}))
	require.True(t, handled)
	require.False(t, out.IsError, "render is not an error: %v", engine.FirstTextContent(out))

	body := engine.FirstTextContent(out)
	require.Contains(t, body, "FreshRow")
	require.Contains(t, body, "StaleRow")
	assert.Less(t, strings.Index(body, "FreshRow"), strings.Index(body, "StaleRow"),
		"temporal is declared as a recency boost, so it must apply the half-life rerank")
}

// TestInterceptSearch_ModeHybridStillFusesBothArms is a CHARACTERIZATION
// CONTROL: green BEFORE the mode fix and green after. Its job is to fail if
// the fix over-applies and collapses hybrid — the default retrieval path — into
// BM25-only. A suppression that catches every mode is not a fix, and this is
// the only test here that would notice.
func TestInterceptSearch_ModeHybridStillFusesBothArms(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")
	deps, mgr, embedCalls := newModeHonorDeps(t, modeHonorNodes(), modeHonorHits(), true)

	handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
		"graph": "knowledge", "mode": "hybrid", "query": "x",
	}))
	require.True(t, handled)
	require.False(t, out.IsError, "render is not an error: %v", engine.FirstTextContent(out))

	assert.GreaterOrEqual(t, embedCalls.Load(), int64(1), "hybrid still embeds")
	assert.NotEmpty(t, mgr.lastVec, "hybrid still drives the vector arm")
	assert.Equal(t, "x", mgr.lastText, "hybrid still drives the BM25 arm")
	assert.Contains(t, engine.FirstTextContent(out), "_search mode: vector+text_")
}

// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// intercept_search_bindfirst_test.go holds the bind-first startup change readiness-gate regressions
// for the segment-engine search arms: the not-ready (wiring-window) gate, the
// permanent-degrade nil-Manager guard, the mode:similar gate, and the
// non-pipeline-ungated complement. They share the interceptDeps harness defined in
// intercept_search_query_dispatch_test.go (same package).

// TestSegmentSearchArms_NotReadyGate is the root-cause regression for the T2-A
// panic (bind-first startup): during the bind-first wiring window the segment Manager is a
// nil interface, and the knowledge / practice / code segment-search arms
// dereference mgr.Search with no nil-check — a panic (in goroutines for the code
// and practice-fan-out arms → daemon crash). With a nil segMgr AND
// PipelineReady()=false, every gated arm (search tool + query tool) must return
// the uniform "daemon still starting" not-ready error instead of panicking. The
// fails-when-absent property: drop the PipelineReady gate and these calls panic on
// the nil deref (the test crashes) instead of returning the error result. The
// cloud/cicd + registered arms (already nil-safe) must emit the SAME window
// message rather than the permanent-degrade string. segMgr is left nil
// deliberately — that is the exact window state the gate must intercept.
func TestSegmentSearchArms_NotReadyGate(t *testing.T) {
	var execHits atomic.Int64
	gc := newInterceptHarness(t, &execHits, cannedSearchResp(t))
	// pipelineNotReady → PipelineReady()=false; segMgr nil → the window state.
	deps := &interceptDeps{gc: gc, pipelineNotReady: true}

	// expectNotReady drives one intercept entry point and asserts it returned the
	// uniform not-ready error WITHOUT panicking (a panic fails the subtest).
	expectNotReady := func(t *testing.T, handled bool, res kgtools.ToolResult) {
		t.Helper()
		require.True(t, handled, "the arm must claim + handle the call")
		require.True(t, res.IsError, "not-ready must be an error result")
		require.NotEmpty(t, res.Content)
		body := res.Content[0].Text
		assert.Contains(t, body, "daemon still starting", "uniform not-ready message family")
	}

	t.Run("search-knowledge", func(t *testing.T) {
		h, res := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{"query": "x", "graph": "knowledge"}))
		expectNotReady(t, h, res)
	})
	t.Run("query-knowledge", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{"text": "x", "mode": "text"})
		h, res := InterceptQueryKnowledgeSearch(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: raw})
		expectNotReady(t, h, res)
	})
	t.Run("search-code", func(t *testing.T) {
		h, res := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{"query": "x", "graph": "code", "repo": "knowledge"}))
		expectNotReady(t, h, res)
	})
	t.Run("query-code", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{"text": "x", "graph": "code", "repo": "knowledge"})
		h, res := InterceptQueryCodeSearch(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: raw})
		expectNotReady(t, h, res)
	})
	t.Run("query-practice-language", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{"graph": "practice", "language": "go", "text": "x"})
		h, res := InterceptQueryPracticeLinkage(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: raw})
		expectNotReady(t, h, res)
	})
	t.Run("query-practice-fanout", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{"graph": "practice", "language": "all", "text": "x"})
		h, res := InterceptQueryPracticeLinkage(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: raw})
		expectNotReady(t, h, res)
	})
	t.Run("query-cloud", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{"graph": "cloud", "account": "acct", "text": "x"})
		h, res := InterceptQueryCloudCICD(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: raw})
		expectNotReady(t, h, res)
	})
	t.Run("query-registered", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{"graph": "myCustomGraph", "text": "x"})
		h, res := InterceptQueryRegisteredGraphSearch(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: raw})
		expectNotReady(t, h, res)
	})
}

// TestSegmentSearchArms_DegradedNilManager (FAILS-WHEN-ABSENT) proves the
// permanent-degrade guard (bind-first startup): markPipelineReady is set even when
// wirePipelineRuntime DEGRADED at boot (no embedder/summarizer → nil segment
// Manager), so PipelineReady()==true while SegmentManager()==nil. The knowledge /
// practice / code arms must then return a loud "segment engine unavailable" error
// rather than nil-deref panicking on mgr.Search. Dropping the nil-mgr guard
// re-introduces the panic (the test crashes).
func TestSegmentSearchArms_DegradedNilManager(t *testing.T) {
	var execHits atomic.Int64
	gc := newInterceptHarness(t, &execHits, cannedSearchResp(t))
	// PipelineReady()=true (default, pipelineNotReady false) but segMgr left nil —
	// the permanent-degrade state.
	deps := &interceptDeps{gc: gc}
	require.True(t, deps.PipelineReady(), "guard precondition: pipeline reports ready")

	expectDegraded := func(t *testing.T, handled bool, res kgtools.ToolResult) {
		t.Helper()
		require.True(t, handled)
		require.True(t, res.IsError, "a nil Manager must be a loud error, not a panic")
		require.NotEmpty(t, res.Content)
		assert.Contains(t, res.Content[0].Text, "segment engine unavailable")
	}

	t.Run("search-knowledge", func(t *testing.T) {
		h, res := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{"query": "x", "graph": "knowledge"}))
		expectDegraded(t, h, res)
	})
	t.Run("query-code", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{"text": "x", "graph": "code", "repo": "knowledge"})
		h, res := InterceptQueryCodeSearch(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: raw})
		expectDegraded(t, h, res)
	})
	t.Run("query-practice-language", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{"graph": "practice", "language": "go", "text": "x"})
		h, res := InterceptQueryPracticeLinkage(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: raw})
		expectDegraded(t, h, res)
	})
	t.Run("query-practice-fanout", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{"graph": "practice", "language": "all", "text": "x"})
		h, res := InterceptQueryPracticeLinkage(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: raw})
		expectDegraded(t, h, res)
	})
}

// TestSearchSimilar_NotReadyGate (FAILS-WHEN-ABSENT) proves the bind-first
// wiring-window gate (bind-first startup) on search mode:similar — which resolves the stored
// vector through the pipeline-backed SegmentVectorResolver. With PipelineReady()=false
// it returns the uniform "daemon still starting" error before the nil-handle check.
func TestSearchSimilar_NotReadyGate(t *testing.T) {
	var execHits atomic.Int64
	gc := newInterceptHarness(t, &execHits, cannedSearchResp(t))
	deps := &interceptDeps{gc: gc, pipelineNotReady: true}
	handled, res := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
		"graph": "knowledge", "mode": "similar", "node_id": "n1",
	}))
	require.True(t, handled, "mode:similar is claimed unconditionally")
	require.True(t, res.IsError)
	require.NotEmpty(t, res.Content)
	assert.Contains(t, res.Content[0].Text, "daemon still starting")
}

// TestSearchBM25_UngatedDuringWindow proves the complement of the readiness gates:
// a search that does NOT touch a pipeline-backed surface stays ungated during the
// wiring window. The logs short-circuit (no segment engine, no PipelineReady
// consult) handles the call WITHOUT emitting the not-ready error even with
// PipelineReady()=false — so non-pipeline reads keep working immediately after bind.
func TestSearchBM25_UngatedDuringWindow(t *testing.T) {
	var execHits, embedCalls atomic.Int64
	gc := newInterceptHarness(t, &execHits, cannedSearchResp(t))
	deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}, pipelineNotReady: true}
	handled, res := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{"graph": "logs", "name": "q1", "text": "err"}))
	require.True(t, handled, "logs search is handled client-side")
	if res.IsError {
		assert.NotContains(t, res.Content[0].Text, "daemon still starting",
			"a non-pipeline search must NOT be gated by PipelineReady during the wiring window")
	}
}

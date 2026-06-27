// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// dispatchEngineHandler serves Health (so gc.Healthy passes) + Engine (counting
// Execute hits, returning a canned response) for the intercept reroute tests.
// The ToolService methods (Call/Stream/GetToolSchemas) were removed — the wire
// is deleted; a non-compilable shape is denied client-side in Dispatch, never
// forwarded, so there is no legacy Call to count.
type dispatchEngineHandler struct {
	execHits *atomic.Int64
	resp     *knowledgev1.ExecuteResponse
	lastReq  *knowledgev1.ExecuteRequest
	// reqs records EVERY ExecuteRequest under mu. A multi-query code search fans
	// out the per-query Executes CONCURRENTLY (searchAllQueries goroutines), so a
	// single lastReq both races and loses all-but-one request; reqs (mutex-guarded)
	// captures the full set for per-query assertions.
	mu   sync.Mutex
	reqs []*knowledgev1.ExecuteRequest
}

func (h *dispatchEngineHandler) Check(
	_ context.Context, _ *connect.Request[knowledgev1.HealthCheckRequest],
) (*connect.Response[knowledgev1.HealthCheckResponse], error) {
	return connect.NewResponse(&knowledgev1.HealthCheckResponse{}), nil
}

func (h *dispatchEngineHandler) Status(
	_ context.Context, _ *connect.Request[knowledgev1.StatusRequest],
) (*connect.Response[knowledgev1.StatusResponse], error) {
	return connect.NewResponse(&knowledgev1.StatusResponse{}), nil
}

func (h *dispatchEngineHandler) Execute(
	_ context.Context, req *connect.Request[knowledgev1.ExecuteRequest],
) (*connect.Response[knowledgev1.ExecuteResponse], error) {
	h.execHits.Add(1)
	h.mu.Lock()
	h.lastReq = req.Msg
	h.reqs = append(h.reqs, req.Msg)
	h.mu.Unlock()
	return connect.NewResponse(h.resp), nil
}

// recordedReqs returns a copy of every ExecuteRequest the handler captured,
// taken under the lock so callers race neither with in-flight goroutines nor
// the append.
func (h *dispatchEngineHandler) recordedReqs() []*knowledgev1.ExecuteRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*knowledgev1.ExecuteRequest, len(h.reqs))
	copy(out, h.reqs)
	return out
}

// Stats/MetadataStats/Index satisfy the generated EngineServiceHandler
// interface; these intercept-reroute tests assert only Execute hits.
func (h *dispatchEngineHandler) Stats(
	context.Context, *connect.Request[knowledgev1.StatsRequest],
) (*connect.Response[knowledgev1.StatsResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (h *dispatchEngineHandler) MetadataStats(
	context.Context, *connect.Request[knowledgev1.MetadataStatsRequest],
) (*connect.Response[knowledgev1.MetadataStatsResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (h *dispatchEngineHandler) Index(
	context.Context, *connect.Request[knowledgev1.IndexRequest],
) (*connect.Response[knowledgev1.IndexResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (h *dispatchEngineHandler) Hive(
	context.Context, *connect.Request[knowledgev1.HiveRequest],
) (*connect.Response[knowledgev1.HiveResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (h *dispatchEngineHandler) PipelineScan(
	context.Context, *connect.Request[knowledgev1.PipelineScanRequest],
) (*connect.Response[knowledgev1.PipelineScanResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (h *dispatchEngineHandler) PipelineGenPoll(
	context.Context, *connect.Request[knowledgev1.PipelineGenPollRequest],
) (*connect.Response[knowledgev1.PipelineGenPollResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (h *dispatchEngineHandler) ExportGraph(
	context.Context, *connect.Request[knowledgev1.ExportGraphRequest],
) (*connect.Response[knowledgev1.ExportGraphResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (h *dispatchEngineHandler) OverwriteGraph(
	context.Context, *connect.Request[knowledgev1.OverwriteGraphRequest],
) (*connect.Response[knowledgev1.OverwriteGraphResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

// stubEmbedder is a fake BinaryEmbedder recording invocations and deriving a
// DISTINCT deterministic 32-byte vector per input text (sha256(text)[:32]) so
// per-query tests can assert each query carries its OWN embedding (not a shared
// or queries[0]-only vector).
type stubEmbedder struct {
	calls      *atomic.Int64
	batchCalls *atomic.Int64
}

func (s stubEmbedder) Available() bool { return true }

// stubVec is the deterministic 32-byte vector stubEmbedder derives for text.
func stubVec(text string) []byte {
	sum := sha256.Sum256([]byte(text))
	return sum[:32]
}

func (s stubEmbedder) EmbedBinary(_ context.Context, text string) ([]byte, error) {
	s.calls.Add(1)
	return stubVec(text), nil
}

func (s stubEmbedder) EmbedBinaryBatch(_ context.Context, texts []string) ([][]byte, error) {
	if s.batchCalls != nil {
		s.batchCalls.Add(1)
	}
	out := make([][]byte, len(texts))
	for i, t := range texts {
		out[i] = stubVec(t)
	}
	return out, nil
}

// interceptDeps satisfies ClientDeps for the intercept reroute tests: a real
// GraphClient (pointed at the fake server) + an optional embedder.
type interceptDeps struct {
	gc     *graphclient.GraphClient
	emb    embed.BinaryEmbedder
	segMgr SegmentSearcher
	segRes SegmentVectorResolver
	// pipelineNotReady flips PipelineReady() to false so a test can exercise the
	// bind-first wiring-window gate (bind-first startup) on the segment-engine search arms.
	// Zero value keeps the pipeline ready, so every pre-existing test exercises
	// the wired path.
	pipelineNotReady bool
}

func (d *interceptDeps) LocalLiveness() LocalLiveness                 { return d.gc }
func (d *interceptDeps) Sink() collector.Sink                         { return nil }
func (d *interceptDeps) RootDir() string                              { return "" }
func (d *interceptDeps) WorkerRuntime() WorkerRuntimeAPI              { return nil }
func (d *interceptDeps) WorkerReady() bool                            { return true }
func (d *interceptDeps) PropReady() bool                              { return true }
func (d *interceptDeps) PipelineReady() bool                          { return !d.pipelineNotReady }
func (d *interceptDeps) ClaimRegistry() *hivemonitor.Registry         { return nil }
func (d *interceptDeps) BanSet() *hivemonitor.BanSet                  { return nil }
func (d *interceptDeps) WorkerCRUD() WorkerCRUDAPI                    { return nil }
func (d *interceptDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return nil }
func (d *interceptDeps) Embedder() embed.BinaryEmbedder               { return d.emb }
func (d *interceptDeps) BackendResolver() BackendResolver             { return nil }
func (d *interceptDeps) GraphCaller() GraphCaller                     { return d.gc }
func (d *interceptDeps) LocalGraphCaller() GraphCaller                { return d.gc }
func (d *interceptDeps) SegmentManager() SegmentSearcher              { return d.segMgr }
func (d *interceptDeps) SegmentVectorResolver() SegmentVectorResolver { return d.segRes }
func (d *interceptDeps) SegmentShipper() SegmentShipper               { return nil }
func (d *interceptDeps) SegmentPruner() SegmentPruner                 { return nil }
func (d *interceptDeps) SegmentCoverage() SegmentCoverageReader       { return nil }
func (d *interceptDeps) PipelineScanner() PipelineScanner             { return nil }
func (d *interceptDeps) ReflectionForcer() ReflectionForcer           { return nil }
func (d *interceptDeps) SimilarityForcer() SimilarityForcer           { return nil }

func (d *interceptDeps) BlindSpotProvider() BlindSpotProvider { return nil }
func (d *interceptDeps) ClusterProvider() ClusterProvider     { return nil }
func (d *interceptDeps) TensionsProvider() TensionsProvider   { return nil }

func newInterceptHarness(t *testing.T, execHits *atomic.Int64, resp *knowledgev1.ExecuteResponse) *graphclient.GraphClient {
	t.Helper()
	gc, _ := newInterceptHarnessWithHandler(t, execHits, resp)
	return gc
}

// newInterceptHarnessWithHandler is newInterceptHarness plus a handle on the
// dispatchEngineHandler, so per-query tests can read recordedReqs() to inspect
// every captured ExecuteRequest (not just the racy lastReq).
func newInterceptHarnessWithHandler(t *testing.T, execHits *atomic.Int64, resp *knowledgev1.ExecuteResponse) (*graphclient.GraphClient, *dispatchEngineHandler) {
	t.Helper()
	h := &dispatchEngineHandler{execHits: execHits, resp: resp}
	mux := http.NewServeMux()
	hp, hh := knowledgev1connect.NewHealthServiceHandler(h)
	mux.Handle(hp, hh)
	ep, eh := knowledgev1connect.NewEngineServiceHandler(h)
	mux.Handle(ep, eh)

	h2s := &http2.Server{}
	srv := httptest.NewServer(h2c.NewHandler(mux, h2s))
	t.Cleanup(srv.Close)
	return graphclient.NewGraphClientForURL(srv.URL), h
}

func cannedSearchResp(t *testing.T) *knowledgev1.ExecuteResponse {
	t.Helper()
	results := []engine.SearchResult{
		{Score: 0.9, Node: &knowledgev1.Node{Id: "n1", Type: string(kgtypes.NodeType("finding")), SymbolName: "Hit"}},
	}
	return &knowledgev1.ExecuteResponse{SearchResults: searchResultsToProtoForTest(results)}
}

func searchParams(t *testing.T, args map[string]any) kgtools.CallToolParams {
	t.Helper()
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	return kgtools.CallToolParams{Name: "search", Arguments: raw}
}

// TestInterceptSearch_EmbedThenClientEngine asserts InterceptSearch embeds the
// query client-side then routes the knowledge arm through the CLIENT engine
// (Manager.Search → hydrate), NOT a server search dispatch. The single
// Execute is the ids[] hydrate read, which is NOT a server search plan.
func TestInterceptSearch_EmbedThenClientEngine(t *testing.T) {
	var execHits, embedCalls atomic.Int64
	gc, handler := newInterceptHarnessWithHandler(t, &execHits, cannedNodesResp(
		&knowledgev1.Node{Id: "n1", Type: "finding", SymbolName: "Hit"},
	))
	mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "n1", Score: 0.9}}}
	deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}, segMgr: mgr}

	handled, out := InterceptSearch(deps, searchParams(t, map[string]any{"query": "x", "graph": "knowledge"}))
	require.True(t, handled)
	assert.GreaterOrEqual(t, embedCalls.Load(), int64(1), "client embed pre-step ran")
	assert.Equal(t, int64(1), mgr.calls.Load(), "knowledge arm drove the CLIENT engine")
	assert.NotEmpty(t, mgr.lastVec, "the client-embedded query vector reached the HNSW arm")
	assert.False(t, dispatchedAServerSearch(handler.recordedReqs()), "knowledge arm must NOT dispatch a server search")
	assert.Contains(t, out.Content[0].Text, "Hit")
}

// TestInterceptSearch_LogsShortCircuit asserts graph=logs never reaches the
// embed/dispatch pipeline (the log short-circuit owns it). Execute is not hit.
func TestInterceptSearch_LogsShortCircuit(t *testing.T) {
	var execHits, embedCalls atomic.Int64
	gc := newInterceptHarness(t, &execHits, cannedSearchResp(t))
	deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}}

	handled, _ := InterceptSearch(deps, searchParams(t, map[string]any{"graph": "logs", "name": "q1", "text": "err"}))
	require.True(t, handled, "logs search is handled client-side")
	assert.Equal(t, int64(0), execHits.Load(), "logs short-circuit does NOT hit Execute")
}

// TestInterceptQuery_EmbedThenNonCompilableDenied asserts InterceptQuery embeds
// the default-hybrid query (maybeEmbedQuery reads the "query" field) then routes
// the tail through engine.Dispatch. A "query"-field-only call is not a
// §A-compilable shape (compileQuery keys on text/id/type/ids/meta), so
// Dispatch DENIES it (the legacy ToolService.Call fall-through is deleted) — exec
// is not hit and the result is the explicit deny.
func TestInterceptQuery_EmbedThenNonCompilableDenied(t *testing.T) {
	var execHits, embedCalls atomic.Int64
	gc := newInterceptHarness(t, &execHits, cannedSearchResp(t))
	deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}}

	raw, err := json.Marshal(map[string]any{"query": "x"})
	require.NoError(t, err)
	handled, out := InterceptQuery(deps, kgtools.CallToolParams{Name: "query", Arguments: raw})
	require.True(t, handled)
	assert.GreaterOrEqual(t, embedCalls.Load(), int64(1), "client embed pre-step ran")
	// query-field-only is not compilable → Dispatch DENIES (no legacy fallback).
	assert.Equal(t, int64(0), execHits.Load(), "non-compilable query is denied, NOT Execute")
	assert.True(t, out.IsError, "non-compilable query → explicit deny")
	assert.Contains(t, out.Content[0].Text, "denied")
}

// TestInterceptQuery_TextModeCompilesToExecute asserts a query carrying the
// mode=text shape (a §A-reducible text search) routes through Dispatch and
// compiles to Engine.Execute. This exercises the InterceptQuery → Dispatch →
// Execute happy path with a real compilable shape.
func TestInterceptQuery_TextModeCompilesToExecute(t *testing.T) {
	var execHits, embedCalls atomic.Int64
	gc := newInterceptHarness(t, &execHits, cannedSearchResp(t))
	deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}}

	// InterceptQuery only fires for queryModesNeedingEmbedding (""/hybrid) AND
	// when maybeEmbedQuery embeds the "query" field. Supply both: query (so the
	// embed fires) + text (so compileQuery's default-mode text arm builds a
	// search plan that compiles to Execute).
	raw, err := json.Marshal(map[string]any{"query": "x", "text": "x"})
	require.NoError(t, err)
	handled, out := InterceptQuery(deps, kgtools.CallToolParams{Name: "query", Arguments: raw})
	require.True(t, handled)
	assert.GreaterOrEqual(t, embedCalls.Load(), int64(1), "client embed pre-step ran")
	assert.Equal(t, int64(1), execHits.Load(), "compilable query routes through Engine.Execute")
	assert.Contains(t, out.Content[0].Text, "[finding] Hit")
}

// recordingCodeSearcher captures the per-query queryVec handed to Manager.Search
// (keyed by query text) so the per-query-distinct-vector property can be asserted
// at the CLIENT-engine seam (code search is client-side, so the vector
// rides Manager.Search, not a server search plan). Returns no hits (the assertions
// are over the threaded vectors, not the render).
type recordingCodeSearcher struct {
	mu      sync.Mutex
	byQuery map[string][]byte
}

func (r *recordingCodeSearcher) Search(
	_ context.Context, _ kgtypes.GraphType, _, queryText string, queryVec []byte, _ int,
) ([]searchengine.Hit, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byQuery == nil {
		r.byQuery = make(map[string][]byte)
	}
	r.byQuery[queryText] = queryVec
	return nil, nil
}

func (r *recordingCodeSearcher) vecFor(q string) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byQuery[q]
}

// TestInterceptSearchCode_DistinctCallerVectors drives InterceptSearch(graph:code)
// twice with two DIFFERENT caller-supplied query_vector values and asserts the
// vector handed to the CLIENT engine (Manager.Search) differs between calls — the
// client-boundary faithful analog of the reporter's "two different query_vectors
// → identical scores" bug. Proves the client threads the caller vector through to
// the engine instead of dropping it.
func TestInterceptSearchCode_DistinctCallerVectors(t *testing.T) {
	vecA := stubVec("alpha-caller-vector")
	vecB := stubVec("beta-caller-vector")
	require.NotEqual(t, vecA, vecB)

	run := func(vec []byte) []byte {
		var execHits atomic.Int64
		gc := newInterceptHarness(t, &execHits, &knowledgev1.ExecuteResponse{})
		mgr := &recordingCodeSearcher{}
		// No embedder: the caller vector is the only vector source.
		deps := &interceptDeps{gc: gc, emb: nil, segMgr: mgr}
		handled, _ := InterceptSearch(deps, searchParams(t, map[string]any{
			"graph": "code", "query": "x", "repo": "knowledge", "query_vector": vec,
		}))
		require.True(t, handled, "graph=code is claimed client-side")
		return mgr.vecFor("x")
	}

	gotA := run(vecA)
	gotB := run(vecB)
	assert.Equal(t, vecA, gotA, "caller vector A threaded to Manager.Search")
	assert.Equal(t, vecB, gotB, "caller vector B threaded to Manager.Search")
	assert.NotEqual(t, gotA, gotB, "distinct caller vectors yield distinct engine vectors")
}

// TestInterceptSearchCode_ConceptualQueryAutoEmbeds asserts a conceptual query
// with an embedder wired and NO caller vector triggers EmbedBinaryBatch and
// threads a non-empty vector to the CLIENT engine — proving the conceptual query
// is no longer reduced to raw BM25 (the second half of the reporter's symptom).
func TestInterceptSearchCode_ConceptualQueryAutoEmbeds(t *testing.T) {
	var execHits, embedCalls, batchCalls atomic.Int64
	gc := newInterceptHarness(t, &execHits, &knowledgev1.ExecuteResponse{})
	mgr := &recordingCodeSearcher{}
	deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls, batchCalls: &batchCalls}, segMgr: mgr}

	const q = "verify a user identity before granting access"
	handled, _ := InterceptSearch(deps, searchParams(t, map[string]any{
		"graph": "code", "query": q, "repo": "knowledge",
	}))
	require.True(t, handled)
	assert.GreaterOrEqual(t, batchCalls.Load(), int64(1), "EmbedBinaryBatch was called for the conceptual query")
	got := mgr.vecFor(q)
	assert.NotEmpty(t, got, "auto-embedded vector threaded to the engine (not BM25-only)")
	assert.Equal(t, stubVec(q), got, "the threaded vector is this query's embedding")
}

// TestInterceptSearchCode_PerQueryDistinctVectors is the load-bearing proof of
// the per-query correctness property: a 2-query code search hands the CLIENT
// engine TWO per-query vectors that DIFFER and each equals its OWN query's stub
// embedding — proving per-query threading, not a queries[0]-only broadcast. The
// 2-query fan-out runs the per-query Searches CONCURRENTLY, so this also exercises
// the mutex-guarded recorder (run with -race).
func TestInterceptSearchCode_PerQueryDistinctVectors(t *testing.T) {
	var execHits, embedCalls, batchCalls atomic.Int64
	gc := newInterceptHarness(t, &execHits, &knowledgev1.ExecuteResponse{})
	mgr := &recordingCodeSearcher{}
	deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls, batchCalls: &batchCalls}, segMgr: mgr}

	const q1 = "authentication and identity verification"
	const q2 = "binary tree traversal algorithm"
	handled, _ := InterceptSearch(deps, searchParams(t, map[string]any{
		"graph": "code", "queries": []string{q1, q2}, "repo": "knowledge",
	}))
	require.True(t, handled)

	assert.Equal(t, stubVec(q1), mgr.vecFor(q1), "q1's engine vector is q1's own embedding")
	assert.Equal(t, stubVec(q2), mgr.vecFor(q2), "q2's engine vector is q2's own embedding")
	assert.NotEqual(t, mgr.vecFor(q1), mgr.vecFor(q2), "per-query vectors DIFFER (not queries[0]-only)")
}

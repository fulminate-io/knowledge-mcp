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
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
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
	// typedCorpus, when set, makes the handler serve NODES FILTERED BY the plan's
	// Selection.NodeTypes instead of the fixed resp — the server's type push-down,
	// modeled. A canned resp answers every plan identically, which cannot tell a
	// browse that carries a type filter from one that dropped it. Nil (the zero
	// value) keeps the fixed-resp behavior every other test relies on.
	typedCorpus []*knowledgev1.Node
	// graphNames is what a RETURN_MODE_GRAPH_NAMES read answers — the set of
	// COLLECTED graph names, discriminated by return mode the same way
	// fanOutEngineHandler does it. Nil (the zero value) answers an EMPTY catalog,
	// which is the never-collected state, so a fixture that means "this graph HAS
	// been collected" has to name it.
	graphNames []string
}

func (h *dispatchEngineHandler) Check(
	_ context.Context, _ *connect.Request[knowledgev1.CheckRequest],
) (*connect.Response[knowledgev1.CheckResponse], error) {
	return connect.NewResponse(&knowledgev1.CheckResponse{}), nil
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
	corpus := h.typedCorpus
	names := h.graphNames
	h.mu.Unlock()
	// A seeded catalog answers the graph-names read; an UNSEEDED one falls through
	// to the canned resp, which is how the fixtures that hand-build a
	// catalog-bearing response (the graph's recorded embedding identity rides the
	// same carrier) keep serving their own.
	if q := req.Msg.GetQuery(); names != nil && q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
		infos := make([]*knowledgev1.GraphInfo, 0, len(names))
		for _, n := range names {
			infos = append(infos, &knowledgev1.GraphInfo{Name: n})
		}
		return connect.NewResponse(&knowledgev1.ExecuteResponse{GraphNames: infos}), nil
	}
	if corpus != nil {
		return connect.NewResponse(h.serveTypedCorpus(req.Msg.GetQuery(), corpus)), nil
	}
	return connect.NewResponse(h.resp), nil
}

// serveTypedCorpus applies the plan's Selection.NodeTypes to the seeded corpus,
// the way the server's type push-down does. An EMPTY NodeTypes matches
// everything — which is exactly what makes a dropped type filter observable in
// the ROWS rather than only in the recorded plan.
func (h *dispatchEngineHandler) serveTypedCorpus(
	q *knowledgev1.QueryPlan, corpus []*knowledgev1.Node,
) *knowledgev1.ExecuteResponse {
	want := map[string]bool{}
	for _, t := range q.GetSelection().GetNodeTypes() {
		want[t] = true
	}
	out := make([]*knowledgev1.Node, 0, len(corpus))
	for _, n := range corpus {
		if len(want) > 0 && !want[n.GetType()] {
			continue
		}
		out = append(out, n)
	}
	return &knowledgev1.ExecuteResponse{Nodes: out}
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

func (h *dispatchEngineHandler) CorpusDelta(
	context.Context, *connect.Request[knowledgev1.CorpusDeltaRequest],
) (*connect.Response[knowledgev1.CorpusDeltaResponse], error) {
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
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	gc := graphclient.NewGraphClientForURL(srv.URL)
	t.Cleanup(gc.Close)
	return gc, h
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
	// The catalog-bearing response is what makes this a graph that HAS been
	// embedded: the arm resolves its query embedder from the graph's RECORDED
	// identity, so a fixture with no identity would (correctly) run BM25-only.
	gc, handler := newInterceptHarnessWithHandler(t, &execHits, cannedEmbeddedNodesResp(
		&knowledgev1.Node{Id: "n1", Type: "finding", SymbolName: "Hit"},
	))
	mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "n1", Score: 0.9}}}
	deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}, segMgr: mgr}

	handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{"query": "x", "graph": "knowledge"}))
	require.True(t, handled)
	// The embed pre-step is proven by the VECTOR REACHING THE ENGINE below, not
	// by the injected stub's counter: the arm no longer embeds with the
	// process-wide configured embedder, it resolves one from the graph.
	assert.Equal(t, int64(1), mgr.calls.Load(), "knowledge arm drove the CLIENT engine")
	assert.Len(t, mgr.lastVec, cannedCatalogVecBytes,
		"the vector reaching the HNSW arm must be the one built from the GRAPH's recorded identity, "+
			"not the configured stub embedder's — the widths differ precisely so this can tell them apart")
	assert.False(t, dispatchedAServerSearch(handler.recordedReqs()), "knowledge arm must NOT dispatch a server search")
	assert.Contains(t, out.Content[0].Text, "Hit")
}

// TestInterceptSearch_LogsShortCircuit asserts graph=logs never reaches the
// embed/dispatch pipeline — the log short-circuit owns it.
//
// THE ASSERTION IS "NO SERVER SEARCH", NOT "NO EXECUTE", and the difference is
// the whole point. The logs arm serves the log graph by issuing its OWN
// client-side reads through the graph caller, so Execute is legitimately hit.
// An earlier form asserted zero Execute calls and passed only because the
// payload carried the query under a key this tool does not read, leaving the
// log search with nothing to run: it measured an empty query, not a
// short-circuit.
func TestInterceptSearch_LogsShortCircuit(t *testing.T) {
	var execHits, embedCalls atomic.Int64
	gc, handler := newInterceptHarnessWithHandler(t, &execHits, cannedSearchResp(t))
	deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}}

	// `query`, not `text` — see the sibling note in intercept_search_bindfirst_test.go.
	// With `text` this test would stay green while asserting nothing: the
	// rejection satisfies both handled==true and execHits==0 trivially, so the
	// logs short-circuit would no longer be under test.
	handled, _ := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{"graph": "logs", "name": "q1", "query": "err"}))
	require.True(t, handled, "logs search is handled client-side")
	assert.False(t, dispatchedAServerSearch(handler.recordedReqs()),
		"the logs arm must not dispatch a server SEARCH — it serves the log graph with its own client-side reads")
}

// BOTH TESTS BELOW CHANGED MEANING when the query param-accounting gate went
// live, and the reason is a finding rather than a test detail.
//
// InterceptQuery claims a query call ONLY when maybeEmbedQuery succeeds
// (query.go), and maybeEmbedQuery keys on the payload field "query"
// (search.go:397). The query TOOL has no `query` param — its search text rides
// `text` — so the only payload that reaches the engine-dispatch arm carries a
// field QueryToolDef does not declare. The Phase-1 census could not see it: that
// walk derives payload fields from arg STRUCTS, and maybeEmbedQuery reads a raw
// map. The accounting gate's unknown-key sweep does see it, and rejects it.
//
// The consequence, stated plainly so nobody reads these tests as a happy path:
// InterceptQuery's client-side embed pre-step is now UNREACHABLE for the query
// tool. No capability is lost — callers reach client-side embedding through
// `text`, which InterceptQueryKnowledgeSearch claims and embeds itself — but the
// engine-dispatch arm keeps a registry cell and a gate call site because the
// bijection requires them. engine.Dispatch's own query branch keeps its direct
// coverage in cmd/knowledge/internal/engine (dispatch_test.go).

// TestInterceptQuery_UndeclaredQueryKeyRejected pins the rejection for the
// query-field-only payload. Before the gate this reached engine.Dispatch and got
// the generic non-compilable deny; now it is refused up front by name.
func TestInterceptQuery_UndeclaredQueryKeyRejected(t *testing.T) {
	var execHits, embedCalls atomic.Int64
	gc := newInterceptHarness(t, &execHits, cannedSearchResp(t))
	deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}}

	raw, err := json.Marshal(map[string]any{"query": "x"})
	require.NoError(t, err)
	handled, out := InterceptQuery(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: raw})
	require.True(t, handled, "a rejection must terminate the chain, never fall through")
	assert.GreaterOrEqual(t, embedCalls.Load(), int64(1),
		"the embed pre-step still runs before the claim — it is what makes this intercept claim at all")
	assert.Equal(t, int64(0), execHits.Load(), "the rejection issues NO read")
	require.True(t, out.IsError, "an undeclared top-level key is a caller error")
	assert.Contains(t, out.Content[0].Text, `unknown parameter "query"`,
		"the error names the offending field rather than failing generically")
	assert.Contains(t, out.Content[0].Text, "text",
		"and enumerates the valid params, which is how a caller finds the right spelling")
}

// TestInterceptQuery_DeclaredParamDoesNotRescueAnUndeclaredOne pins that the
// sweep is per-KEY, not per-payload: supplying a legitimate `text` alongside the
// undeclared `query` still fails on `query`. Without this, a caller could
// smuggle an unknown key in beside a known one.
func TestInterceptQuery_DeclaredParamDoesNotRescueAnUndeclaredOne(t *testing.T) {
	var execHits, embedCalls atomic.Int64
	gc := newInterceptHarness(t, &execHits, cannedSearchResp(t))
	deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}}

	raw, err := json.Marshal(map[string]any{"query": "x", "text": "x"})
	require.NoError(t, err)
	handled, out := InterceptQuery(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: raw})
	require.True(t, handled)
	assert.Equal(t, int64(0), execHits.Load(), "still no read")
	require.True(t, out.IsError)
	assert.Contains(t, out.Content[0].Text, `unknown parameter "query"`)
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
		handled, _ := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
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
	handled, _ := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
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
	handled, _ := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
		"graph": "code", "queries": []string{q1, q2}, "repo": "knowledge",
	}))
	require.True(t, handled)

	assert.Equal(t, stubVec(q1), mgr.vecFor(q1), "q1's engine vector is q1's own embedding")
	assert.Equal(t, stubVec(q2), mgr.vecFor(q2), "q2's engine vector is q2's own embedding")
	assert.NotEqual(t, mgr.vecFor(q1), mgr.vecFor(q2), "per-query vectors DIFFER (not queries[0]-only)")
}

// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// dispatchEngineHandler serves Health (so gc.Healthy passes) + Engine (counting
// Execute hits, returning a canned response) for the intercept reroute tests.
// T-GTB4 removed the ToolService methods (Call/Stream/GetToolSchemas) — the wire
// is deleted; a non-compilable shape is denied client-side in Dispatch, never
// forwarded, so there is no legacy Call to count.
type dispatchEngineHandler struct {
	execHits *atomic.Int64
	resp     *knowledgev1.ExecuteResponse
	lastReq  *knowledgev1.ExecuteRequest
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
	h.lastReq = req.Msg
	return connect.NewResponse(h.resp), nil
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

func (h *dispatchEngineHandler) ExportGraph(
	context.Context, *connect.Request[knowledgev1.ExportGraphRequest],
) (*connect.Response[knowledgev1.ExportGraphResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

// stubEmbedder is a fake BinaryEmbedder recording invocations and returning a
// fixed 32-byte vector.
type stubEmbedder struct{ calls *atomic.Int64 }

func (s stubEmbedder) Available() bool { return true }

func (s stubEmbedder) EmbedBinary(_ context.Context, _ string) ([]byte, error) {
	s.calls.Add(1)
	return make([]byte, 32), nil
}

func (s stubEmbedder) EmbedBinaryBatch(_ context.Context, texts []string) ([][]byte, error) {
	out := make([][]byte, len(texts))
	for i := range texts {
		out[i] = make([]byte, 32)
	}
	return out, nil
}

// interceptDeps satisfies ClientDeps for the intercept reroute tests: a real
// GraphClient (pointed at the fake server) + an optional embedder.
type interceptDeps struct {
	gc  *graphclient.GraphClient
	emb embed.BinaryEmbedder
}

func (d *interceptDeps) GraphClient() *graphclient.GraphClient { return d.gc }
func (d *interceptDeps) Sink() collector.Sink                  { return nil }
func (d *interceptDeps) RootDir() string                       { return "" }
func (d *interceptDeps) WorkerRuntime() WorkerRuntimeAPI       { return nil }
func (d *interceptDeps) WorkerCRUD() WorkerCRUDAPI             { return nil }
func (d *interceptDeps) Embedder() embed.BinaryEmbedder        { return d.emb }
func (d *interceptDeps) BackendResolver() BackendResolver      { return nil }
func (d *interceptDeps) GraphCaller() GraphCaller              { return d.gc }
func (d *interceptDeps) LocalGraphCaller() GraphCaller         { return d.gc }
func (d *interceptDeps) RepoResolver() *RepoResolver           { return nil }

func newInterceptHarness(t *testing.T, execHits *atomic.Int64, resp *knowledgev1.ExecuteResponse) *graphclient.GraphClient {
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
	return graphclient.NewGraphClientForURL(srv.URL)
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

// TestInterceptSearch_EmbedThenDispatchExecute asserts InterceptSearch embeds
// the query client-side then routes the tail through Engine.Execute.
func TestInterceptSearch_EmbedThenDispatchExecute(t *testing.T) {
	var execHits, embedCalls atomic.Int64
	gc := newInterceptHarness(t, &execHits, cannedSearchResp(t))
	deps := &interceptDeps{gc: gc, emb: stubEmbedder{calls: &embedCalls}}

	handled, out := InterceptSearch(deps, searchParams(t, map[string]any{"query": "x", "graph": "knowledge"}))
	require.True(t, handled)
	assert.GreaterOrEqual(t, embedCalls.Load(), int64(1), "client embed pre-step ran")
	assert.Equal(t, int64(1), execHits.Load(), "tail routed through Engine.Execute")
	assert.Contains(t, out.Content[0].Text, "[finding] Hit")
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
// §A-compilable shape (compileQuery keys on text/id/type/ids/meta), so post-T-GTB4
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

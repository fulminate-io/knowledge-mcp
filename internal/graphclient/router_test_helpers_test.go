// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// router_test_helpers_test.go holds the shared EngineService test double + the
// httptest harness the router routing tests stand up. Split out of
// router_test.go to keep that file under the file-length cap.

// countingEngine is an EngineService handler that counts hits per RPC kind
// and replies with a minimal, valid response. Each test fixture stands up
// two of these (local + cloud) so we can assert which backend serviced a
// given call.
type countingEngine struct {
	execute         atomic.Int32
	index           atomic.Int32
	hive            atomic.Int32
	metadataStats   atomic.Int32
	exportGraph     atomic.Int32
	stats           atomic.Int32
	pipelineScan    atomic.Int32
	pipelineGenPoll atomic.Int32
}

func (e *countingEngine) Execute(
	_ context.Context,
	_ *connect.Request[knowledgev1.ExecuteRequest],
) (*connect.Response[knowledgev1.ExecuteResponse], error) {
	e.execute.Add(1)
	return connect.NewResponse(&knowledgev1.ExecuteResponse{}), nil
}

func (e *countingEngine) Stats(
	_ context.Context,
	_ *connect.Request[knowledgev1.StatsRequest],
) (*connect.Response[knowledgev1.StatsResponse], error) {
	e.stats.Add(1)
	return connect.NewResponse(&knowledgev1.StatsResponse{}), nil
}

func (e *countingEngine) MetadataStats(
	_ context.Context,
	_ *connect.Request[knowledgev1.MetadataStatsRequest],
) (*connect.Response[knowledgev1.MetadataStatsResponse], error) {
	e.metadataStats.Add(1)
	return connect.NewResponse(&knowledgev1.MetadataStatsResponse{}), nil
}

func (e *countingEngine) Index(
	_ context.Context,
	_ *connect.Request[knowledgev1.IndexRequest],
) (*connect.Response[knowledgev1.IndexResponse], error) {
	e.index.Add(1)
	return connect.NewResponse(&knowledgev1.IndexResponse{}), nil
}

func (e *countingEngine) Hive(
	_ context.Context,
	_ *connect.Request[knowledgev1.HiveRequest],
) (*connect.Response[knowledgev1.HiveResponse], error) {
	e.hive.Add(1)
	return connect.NewResponse(&knowledgev1.HiveResponse{}), nil
}

func (e *countingEngine) PipelineScan(
	_ context.Context,
	_ *connect.Request[knowledgev1.PipelineScanRequest],
) (*connect.Response[knowledgev1.PipelineScanResponse], error) {
	e.pipelineScan.Add(1)
	return connect.NewResponse(&knowledgev1.PipelineScanResponse{}), nil
}

func (e *countingEngine) PipelineGenPoll(
	_ context.Context,
	_ *connect.Request[knowledgev1.PipelineGenPollRequest],
) (*connect.Response[knowledgev1.PipelineGenPollResponse], error) {
	e.pipelineGenPoll.Add(1)
	return connect.NewResponse(&knowledgev1.PipelineGenPollResponse{}), nil
}

func (e *countingEngine) CorpusDelta(
	_ context.Context,
	_ *connect.Request[knowledgev1.CorpusDeltaRequest],
) (*connect.Response[knowledgev1.CorpusDeltaResponse], error) {
	return connect.NewResponse(&knowledgev1.CorpusDeltaResponse{}), nil
}

func (e *countingEngine) ExportGraph(_ context.Context, _ *connect.Request[knowledgev1.ExportGraphRequest]) (*connect.Response[knowledgev1.ExportGraphResponse], error) {
	e.exportGraph.Add(1)
	return connect.NewResponse(&knowledgev1.ExportGraphResponse{}), nil
}

func (e *countingEngine) OverwriteGraph(_ context.Context, _ *connect.Request[knowledgev1.OverwriteGraphRequest]) (*connect.Response[knowledgev1.OverwriteGraphResponse], error) {
	return connect.NewResponse(&knowledgev1.OverwriteGraphResponse{}), nil
}

// startCountingEngine stands up an h2c httptest.Server in front of a
// countingEngine handler. Returns the server URL and the engine pointer
// so tests can read hit counters.
func startCountingEngine(t *testing.T) (string, *countingEngine) {
	t.Helper()
	eng := &countingEngine{}
	mux := http.NewServeMux()
	path, hdlr := knowledgev1connect.NewEngineServiceHandler(eng)
	mux.Handle(path, hdlr)
	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(srv.Close)
	return srv.URL, eng
}

// staticTokenSource is a non-refreshing token source for tests that need
// AuthState=true but do not exercise the 401 retry path.
type staticTokenSource struct{ tok string }

func (s staticTokenSource) Token(_ context.Context) (string, auth.PermissionSet, error) {
	return s.tok, nil, nil
}

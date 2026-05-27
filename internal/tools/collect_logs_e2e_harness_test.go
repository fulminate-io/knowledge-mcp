// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
)

// capturedRequest is a thread-safe holder for the WriteResultRequest
// observed by the test ingest server. The struct holds the lock and the
// req so the test can read the captured payload without racing.
type capturedRequest struct {
	mu  sync.Mutex
	req *knowledgev1.WriteResultRequest
}

func (c *capturedRequest) set(r *knowledgev1.WriteResultRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.req = r
}

// logsIngestHandler wraps testIngestHandler so the logs-e2e test can also
// drive FetchCloudSubgraph (which the base test handler stubs out as
// Unimplemented) and capture the WriteResult payload for shape assertions.
type logsIngestHandler struct {
	*testIngestHandler
	subgraph *knowledgev1.FetchCloudSubgraphResponse
	fetchErr error
	captured *capturedRequest
}

func (h *logsIngestHandler) WriteResult(
	ctx context.Context,
	req *connect.Request[knowledgev1.WriteResultRequest],
) (*connect.Response[knowledgev1.WriteResultResponse], error) {
	h.captured.set(req.Msg)
	return h.testIngestHandler.WriteResult(ctx, req)
}

func (h *logsIngestHandler) FetchCloudSubgraph(
	_ context.Context,
	_ *connect.Request[knowledgev1.FetchCloudSubgraphRequest],
) (*connect.Response[knowledgev1.FetchCloudSubgraphResponse], error) {
	if h.fetchErr != nil {
		return nil, h.fetchErr
	}
	if h.subgraph == nil {
		return connect.NewResponse(&knowledgev1.FetchCloudSubgraphResponse{}), nil
	}
	return connect.NewResponse(h.subgraph), nil
}

// inProcessIngestServer wires the logs ingest handler + an h2c httptest
// server + the matching IngestService client. Tests use this so the full
// UploadChunks → WriteResult flow runs end-to-end without a real TCP
// connection.
type inProcessIngestServer struct {
	srv    *httptest.Server
	client knowledgev1connect.IngestServiceClient
}

func (s *inProcessIngestServer) close() {
	if s != nil && s.srv != nil {
		s.srv.Close()
	}
}

// startInProcessIngestServer boots the in-process ingest harness. subgraph is
// the FetchCloudSubgraph response; fetchErr forces the fetch to fail (subgraph
// is ignored when fetchErr is non-nil). The embedded testIngestHandler captures
// the materialized CollectResult on WriteResult instead of persisting it — no
// real store engine is linked. Tests read the captured batch via
// handler.sink.last() (the returned handler) and the WriteResultRequest via the
// returned *capturedRequest.
func startInProcessIngestServer(
	t *testing.T,
	subgraph *knowledgev1.FetchCloudSubgraphResponse,
	fetchErr error,
) (*inProcessIngestServer, *capturedRequest, *logsIngestHandler) {
	t.Helper()
	captured := &capturedRequest{}
	handler := &logsIngestHandler{
		testIngestHandler: newTestIngestHandler(),
		subgraph:          subgraph,
		fetchErr:          fetchErr,
		captured:          captured,
	}
	mux := http.NewServeMux()
	path, h := knowledgev1connect.NewIngestServiceHandler(handler)
	mux.Handle(path, h)
	h2s := &http2.Server{}
	srv := httptest.NewServer(h2c.NewHandler(mux, h2s))
	client := knowledgev1connect.NewIngestServiceClient(h2cClient(), srv.URL, connect.WithGRPC())
	return &inProcessIngestServer{srv: srv, client: client}, captured, handler
}

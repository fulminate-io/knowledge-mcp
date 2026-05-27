// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
)

// DefaultPort is the canonical TCP port the stdio client connects to
// and the OSS knowledge-server listens on. Duplicated in
// cmd/knowledge-server/internal/server/server.go — keep both in sync.
const DefaultPort = 15022

// GraphClient calls the graph server over connect-go. EngineService (Execute /
// Topology / Stats / MetadataStats / Index / PipelineScan / Sync) is the only
// LLM-facing dispatch surface, alongside Health (Check/Status) and Ingest (chunk
// upload).
type GraphClient struct {
	baseURL    string
	httpClient *http.Client

	health knowledgev1connect.HealthServiceClient
	ingest knowledgev1connect.IngestServiceClient
	engine knowledgev1connect.EngineServiceClient
}

// NewGraphClient creates a GraphClient that connects to the given TCP
// port. Uses an h2c transport so bi-di streaming RPCs (IngestService
// UploadChunks) work over cleartext HTTP/2 — the server listens with
// h2c.NewHandler so both protocols negotiate on the same port.
//
// All four service clients are wrapped in the unary reconnect
// interceptor — a unary RPC that hits a transient transport failure
// (server restart, ECONNREFUSED, io.EOF on the h2c conn) retries on
// an exponential-backoff schedule (~4.25s total window) instead of
// surfacing the error to the caller. Streaming RPCs are pass-through
// at the interceptor layer; streaming resumption lives in
// collector/remote and is handled via UploadChunks chunk redial
// (UploadChunks is content-hash idempotent).
func NewGraphClient(port int) *GraphClient {
	return NewGraphClientForURL(fmt.Sprintf("http://127.0.0.1:%d", port))
}

// NewGraphClientForURL is the URL-shaped constructor used by tests that need
// to point a GraphClient at an httptest.Server. The h2c-aware transport and
// the reconnect interceptor are wired identically to NewGraphClient — only
// the base URL differs. Production code stays on the port-shaped form.
func NewGraphClientForURL(baseURL string) *GraphClient {
	httpClient := &http.Client{
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			},
			// No keep-alive config here — http2.Transport manages its
			// own connection pool; when a server restart invalidates
			// the cached connection, the next request fails with
			// io.EOF / ECONNREFUSED and the reconnectInterceptor
			// retries over the backoff window until the new server
			// accepts the request.
		},
		// No global timeout — reindex operations on large repos can take
		// hours. Per-request timeouts are handled via context.
	}
	retry := connect.WithInterceptors(newReconnectInterceptor())
	return &GraphClient{
		baseURL:    baseURL,
		httpClient: httpClient,
		health:     knowledgev1connect.NewHealthServiceClient(httpClient, baseURL, retry),
		ingest:     knowledgev1connect.NewIngestServiceClient(httpClient, baseURL, retry),
		engine:     knowledgev1connect.NewEngineServiceClient(httpClient, baseURL, retry),
	}
}

// IngestClient exposes the IngestService client so the collector's
// RemoteUploadSink can stream chunks + WriteResult calls to the graph
// server.
func (c *GraphClient) IngestClient() knowledgev1connect.IngestServiceClient {
	return c.ingest
}

// Execute issues one EngineService.Execute RPC carrying a declarative
// QueryPlan/MutationPlan and returns the typed ExecuteResponse. On a
// transport/RPC error it returns (nil, err) — it does NOT coerce the failure
// into a kgtools.ToolResult; the client-side dispatcher renders engine errors
// (e.g. CodeInvalidArgument validation messages) into the LLM-facing output.
func (c *GraphClient) Execute(
	ctx context.Context,
	req *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	resp, err := c.engine.Execute(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// Stats issues one EngineService.Stats RPC and returns the typed response. Thin
// connect-client passthrough mirroring Execute.
func (c *GraphClient) Stats(
	ctx context.Context,
	req *knowledgev1.StatsRequest,
) (*knowledgev1.StatsResponse, error) {
	resp, err := c.engine.Stats(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// MetadataStats issues one EngineService.MetadataStats RPC and returns the typed
// response. Thin connect-client passthrough mirroring Execute.
func (c *GraphClient) MetadataStats(
	ctx context.Context,
	req *knowledgev1.MetadataStatsRequest,
) (*knowledgev1.MetadataStatsResponse, error) {
	resp, err := c.engine.MetadataStats(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// Index issues one EngineService.Index RPC and returns the typed response. Thin
// connect-client passthrough mirroring Execute.
func (c *GraphClient) Index(
	ctx context.Context,
	req *knowledgev1.IndexRequest,
) (*knowledgev1.IndexResponse, error) {
	resp, err := c.engine.Index(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// PipelineScan issues one EngineService.PipelineScan RPC and returns the typed
// response. Thin connect-client passthrough mirroring Index — the client-side
// LLM pipeline's scanGaps rides this.
func (c *GraphClient) PipelineScan(
	ctx context.Context,
	req *knowledgev1.PipelineScanRequest,
) (*knowledgev1.PipelineScanResponse, error) {
	resp, err := c.engine.PipelineScan(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// ExportGraph issues one EngineService.ExportGraph RPC and returns the typed
// response. Thin connect-client passthrough mirroring Sync — the client-side
// push orchestration (InterceptSync) fetches the serialized OSS graph bytes via
// this read, then uploads them to Fulminate Cloud over the auth.Transport.
func (c *GraphClient) ExportGraph(
	ctx context.Context,
	req *knowledgev1.ExportGraphRequest,
) (*knowledgev1.ExportGraphResponse, error) {
	resp, err := c.engine.ExportGraph(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// Healthy checks if the graph server is running and responsive. Uses
// HealthService.Check — an empty request/response ping.
func (c *GraphClient) Healthy() bool {
	ctx := context.Background()
	_, err := c.health.Check(ctx, connect.NewRequest(&knowledgev1.HealthCheckRequest{}))
	return err == nil
}

// HealthyCtx is the ctx-aware variant of Healthy. Callers needing a
// bounded per-attempt probe budget pass context.WithTimeout — useful
// for poll loops where a wedged server (TCP open but Check never
// returns) must not stall the caller through the unary reconnect
// interceptor's full retry budget. Healthy's context.Background is the
// right shape for the keepalive loop; HealthyCtx covers everything
// else (diagnostics, install-time sanity checks).
func (c *GraphClient) HealthyCtx(ctx context.Context) bool {
	_, err := c.health.Check(ctx, connect.NewRequest(&knowledgev1.HealthCheckRequest{}))
	return err == nil
}

// Status returns server status as a map keyed exactly as the legacy
// /status JSON shape: pid, nodes, edges, binary_vectors, bm25_docs,
// graph_path. handleServerStatus at tools_glue.go:335-352 reads these
// keys directly.
func (c *GraphClient) Status() (map[string]any, error) {
	ctx := context.Background()
	resp, err := c.health.Status(ctx, connect.NewRequest(&knowledgev1.StatusRequest{}))
	if err != nil {
		return nil, err
	}
	msg := resp.Msg
	return map[string]any{
		"pid":               float64(msg.Pid),
		"nodes":             float64(msg.Nodes),
		"edges":             float64(msg.Edges),
		"binary_vectors":    float64(msg.BinaryVectors),
		"bm25_docs":         float64(msg.Bm25Docs),
		"graph_path":        msg.GraphPath,
		"summary_queued":    float64(msg.SummarizationPending),
		"summary_running":   float64(msg.SummarizationSummarizing),
		"summary_succeeded": float64(msg.SummarizationSummarized),
		"summary_failed":    float64(msg.SummarizationFailed),
		"embed_queued":      float64(msg.EmbedQueued),
		"embed_running":     float64(msg.EmbedRunning),
		"embed_succeeded":   float64(msg.EmbedSucceeded),
		"embed_failed":      float64(msg.EmbedFailed),
	}, nil
}

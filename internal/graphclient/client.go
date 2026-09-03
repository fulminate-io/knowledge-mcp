// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
)

// DefaultPort is the canonical TCP port the knowledge client connects to
// and the OSS knowledge-server listens on. Duplicated in
// cmd/knowledge-server/internal/server/server.go — keep both in sync.
const DefaultPort = 15022

// DefaultMCPHTTPPort is the loopback TCP port the `knowledge serve` daemon
// binds its streamable-HTTP MCP endpoint (/mcp) on. Deliberately distinct
// from DefaultPort (the graph-server port) so the daemon and the graph
// server can run side by side on one host.
const DefaultMCPHTTPPort = 15023

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

	// probe is the health client WITHOUT the reconnect interceptor. Liveness
	// probes (Healthy / HealthyCtx) are questions, not operations: "the server
	// is down" is a valid answer, so a probe must fail fast on ECONNREFUSED
	// instead of sleeping through the ~4.25s retry-backoff window. Real RPCs
	// (including HealthService.Status) stay on the retrying clients.
	probe knowledgev1connect.HealthServiceClient

	// freshnessGen holds the last non-zero account freshness watermark the
	// observer interceptor read off a response. It is a pointer because the
	// interceptor closure is built before the struct literal and the two must
	// share one cell.
	freshnessGen *atomic.Uint64
}

// FreshnessGen returns the most recent NON-ZERO account freshness watermark this
// client has observed on a response. 0 means "never observed one".
//
// Nil-safe on both the receiver and the cell: a bare &GraphClient{} literal
// (client_keepalive_test.go stands two up) has no observer wired, and a nil
// *GraphClient is the cloud-only user's absent local client, as on Healthy.
func (c *GraphClient) FreshnessGen() uint64 {
	if c == nil || c.freshnessGen == nil {
		return 0
	}
	return c.freshnessGen.Load()
}

// NewGraphClient creates a GraphClient that connects to the given TCP
// port. Uses an h2c transport over cleartext HTTP/2 — the server listens
// with h2c.NewHandler so both protocols negotiate on the same port.
//
// All service clients are wrapped in the unary reconnect interceptor — a
// unary RPC that hits a transient transport failure (server restart,
// ECONNREFUSED, io.EOF on the h2c conn) retries on an exponential-backoff
// schedule (~4.25s total window) instead of surfacing the error to the
// caller. The IngestService write path is the unary CollectChunk×N +
// Finalize flow (no bidi stream); each chunk is content-idempotent under its
// per-collection epoch, so a redial-and-resend through this interceptor
// re-lands identically via the server's carry-forward upsert.
func NewGraphClient(port int) *GraphClient {
	return NewGraphClientForURL(fmt.Sprintf("http://127.0.0.1:%d", port))
}

// NewGraphClientForURL is the URL-shaped constructor used by tests that need
// to point a GraphClient at an httptest.Server. The h2c-aware transport and
// the reconnect interceptor are wired identically to NewGraphClient — only
// the base URL differs. Production code stays on the port-shaped form.
// CloseIdleConnections releases the connections this client is holding idle in its
// transport pool, mirroring http.Client.CloseIdleConnections.
//
// WHY IT EXISTS. A GraphClient owns an http2.Transport, and an HTTP/2 connection
// kept alive in that pool keeps the SERVER's serve goroutine alive with it — an
// httptest server's Close does not unwind a connection the client still holds. In
// the daemon that is exactly right and nothing needs this: the pool outliving any
// one request is the point of a pool, and process exit ends it. It matters for any
// caller that creates clients repeatedly inside ONE process — test binaries above
// all — where each abandoned client otherwise pins a connection and a goroutine for
// the life of the process.
//
// It is safe to call more than once and on a nil client.
func (c *GraphClient) CloseIdleConnections() {
	if c == nil || c.httpClient == nil {
		return
	}
	c.httpClient.CloseIdleConnections()
}

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
	// Both stampers run BEFORE the reconnect retry loop so a retried attempt
	// re-sends the already-stamped request rather than re-deriving per attempt.
	// They write to different places: the operation stamper writes a MESSAGE
	// FIELD (client_context.operation), the session stamper writes a HEADER
	// (the harness session-id). The health client gets both and is unaffected by
	// either: its request messages carry no client_context field, so the
	// operation stamper no-ops, and a health probe carries the harness header
	// harmlessly when one is in context. The health client gets the freshness
	// observer too, and no-ops the same way: its response messages declare no
	// freshness_gen field.
	//
	// The freshness observer is PREPENDED, and the position is load-bearing:
	// connect composes interceptors first-in-slice OUTERMOST, so an outermost
	// interceptor's post-next code runs LAST on the response path and sees the
	// response finally returned to the caller. Inside the reconnect interceptor
	// it would instead record a retried attempt's intermediate response.
	gens := &atomic.Uint64{}
	retry := connect.WithInterceptors(newFreshnessObserver(gens), newOperationInterceptor(), newReconnectInterceptor())
	return &GraphClient{
		baseURL:      baseURL,
		httpClient:   httpClient,
		health:       knowledgev1connect.NewHealthServiceClient(httpClient, baseURL, retry),
		ingest:       knowledgev1connect.NewIngestServiceClient(httpClient, baseURL, retry),
		engine:       knowledgev1connect.NewEngineServiceClient(httpClient, baseURL, retry),
		probe:        knowledgev1connect.NewHealthServiceClient(httpClient, baseURL),
		freshnessGen: gens,
	}
}

// IngestClient exposes the IngestService client so the collector's
// RemoteUploadSink can send CollectChunk + Finalize calls to the graph
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

// CorpusDelta issues one EngineService.CorpusDelta RPC and returns the typed
// response. Thin connect-client passthrough mirroring PipelineScan — the daemon's
// resident thought-corpus cache rides this on dirty ticks.
func (c *GraphClient) CorpusDelta(
	ctx context.Context,
	req *knowledgev1.CorpusDeltaRequest,
) (*knowledgev1.CorpusDeltaResponse, error) {
	resp, err := c.engine.CorpusDelta(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// PipelineGenPoll issues one EngineService.PipelineGenPoll RPC and returns the
// typed response. Thin connect-client passthrough mirroring PipelineScan — the
// client-side LLM pipeline's central gen-poll loop (one bulk poll per tick) rides
// this.
func (c *GraphClient) PipelineGenPoll(
	ctx context.Context,
	req *knowledgev1.PipelineGenPollRequest,
) (*knowledgev1.PipelineGenPollResponse, error) {
	resp, err := c.engine.PipelineGenPoll(ctx, connect.NewRequest(req))
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

// OverwriteGraph issues one EngineService.OverwriteGraph RPC and returns the
// typed response. Thin connect-client passthrough mirroring ExportGraph — the
// mutating local-apply inverse of ExportGraph. The client pull arm
// (InterceptSync) drives it through the LOCAL graph caller only (cloud bytes are
// fetched via ExportGraph, then applied here); it is never routed cloud-side.
func (c *GraphClient) OverwriteGraph(
	ctx context.Context,
	req *knowledgev1.OverwriteGraphRequest,
) (*knowledgev1.OverwriteGraphResponse, error) {
	resp, err := c.engine.OverwriteGraph(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// Healthy checks if the graph server is running and responsive. Uses
// HealthService.Check — an empty request/response ping. SINGLE attempt, no
// retries, bounded at 1s: a liveness probe's "down" is a definitive answer
// (ECONNREFUSED on loopback is instant), so it never rides the reconnect
// interceptor's backoff window; an install pointed at a remote backend has no
// local server at all and must not pay a retry ladder to learn that.
// Nil-safe: a nil *GraphClient (a cloud-only user has no local client — the
// c.local field is a typed nil, e.g. when manage(status)'s addLocalDaemonJSON
// probes local liveness on the logged-in cloud path) reports NOT healthy
// rather than panicking on the nil-receiver field access.
func (c *GraphClient) Healthy() bool {
	if c == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return c.HealthyCtx(ctx)
}

// HealthyCtx is the ctx-aware variant of Healthy: same single-attempt no-retry
// probe, with the attempt budget owned by the caller's ctx. Poll loops
// (waitForServer) supply their own cadence; a probe that failed IS the poll
// result, so per-attempt retries would only skew the loop's timing.
func (c *GraphClient) HealthyCtx(ctx context.Context) bool {
	if c == nil {
		return false
	}
	_, err := c.probe.Check(ctx, connect.NewRequest(&knowledgev1.CheckRequest{}))
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

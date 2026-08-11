// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"

	"connectrpc.com/connect"

	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// NewCloudGraphClient builds a *GraphClient pointed at the Fulminate cloud
// EngineService endpoint (HTTPS), with an OAuth bearer credential attached on
// every outgoing request and a single 401-driven token-refresh retry.
//
// Wire shape, contrasted with NewGraphClientForURL:
//   - HTTPS transport (pinned http.Transport with ForceAttemptHTTP2:true and
//     an explicit empty *tls.Config) instead of cleartext h2c. The local
//     server speaks h2c over loopback; the cloud agent speaks plain HTTPS.
//   - Authorization: Bearer <token> attached per request by a
//     bearerRoundTripper. The token is read from ts.Token(ctx) each call so
//     refreshes (whether time-based via OAuthTokenSource or 401-driven via
//     ForceRefresh) take effect on the next outbound request.
//   - On HTTP 401, if the TokenSource implements auth.RefreshingTokenSource,
//     the round-tripper invokes ForceRefresh(ctx) and retries the request ONCE
//     with the rotated bearer. This mirrors the recovery shape in
//     auth/sync_transport.go for the sync push endpoint.
//
// The reconnect interceptor that NewGraphClientForURL installs targets
// transient h2c transport failures against a local server (ECONNREFUSED on
// restart, io.EOF on conn loss). The cloud round-tripper does NOT need it —
// connect-go surfaces 401 + 5xx normally and the retry budget on those classes
// belongs to the auth-refresh logic above, not a generic backoff loop.
func NewCloudGraphClient(baseURL string, ts auth.TokenSource) *GraphClient {
	httpClient := &http.Client{
		Transport: &bearerRoundTripper{
			ts: ts,
			base: &http.Transport{
				ForceAttemptHTTP2: true,
				TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
			},
		},
		// No global timeout — long-running tool calls (large Execute payloads,
		// cloud-side computation) honor caller context.WithTimeout.
	}
	// The cloud path is exactly where the operation label matters — it is the
	// deployment that reads the per-tag metrics — so the stamper is installed
	// here as well as on the local client. The health client gets it too and is
	// unaffected: its request messages carry no client_context field.
	// The session stamper rides here too: the cloud is the deployment that reads
	// the harness session-id off the wire to key a hive member.
	//
	// The freshness observer is PREPENDED for the same reason it is on the local
	// client: connect composes interceptors first-in-slice OUTERMOST, so it sees
	// the response finally returned to the caller. This is the constructor that
	// matters most for it — the cloud is the deployment serving a non-zero
	// watermark today, and a miss here is silent rather than compile-caught.
	gens := &atomic.Uint64{}
	stamp := connect.WithInterceptors(newFreshnessObserver(gens), newOperationInterceptor(), newSessionInterceptor())
	return &GraphClient{
		baseURL:      baseURL,
		httpClient:   httpClient,
		health:       knowledgev1connect.NewHealthServiceClient(httpClient, baseURL, stamp),
		ingest:       knowledgev1connect.NewIngestServiceClient(httpClient, baseURL, stamp),
		engine:       knowledgev1connect.NewEngineServiceClient(httpClient, baseURL, stamp),
		freshnessGen: gens,
	}
}

// bearerRoundTripper attaches an Authorization: Bearer <token> header to every
// outgoing request and transparently retries once on HTTP 401 when the token
// source supports ForceRefresh. The retry is the auth-rotation recovery path
// described in NewCloudGraphClient.
//
// Concurrency: safe — bearerRoundTripper holds no per-request state. The
// caller-supplied auth.TokenSource is documented as safe for concurrent use
// (TokenSource godoc in cmd/knowledge/internal/auth/tokensource.go:18-24),
// and the wrapped http.Transport handles its own connection pooling.
type bearerRoundTripper struct {
	ts   auth.TokenSource
	base http.RoundTripper
}

// RoundTrip implements http.RoundTripper. It acquires a token, sends the
// request, and — on HTTP 401 — force-refreshes (if the source implements
// auth.RefreshingTokenSource) and retries the request exactly once.
//
// The request body is captured once before the first dispatch so the retry
// can re-send identical bytes. http.Request.Body is io.ReadCloser and is
// drained on the first send; without buffering, the retry would carry an
// empty body and the server would reject it on payload-shape grounds rather
// than auth.
func (b *bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	token, _, err := b.ts.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("graphclient: acquire token for %s %s: %w",
			req.Method, req.URL.Path, err)
	}

	bodyBytes, err := captureBody(req)
	if err != nil {
		return nil, err
	}

	first := cloneRequestWithBearer(req, bodyBytes, token)
	resp, err := b.base.RoundTrip(first)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}

	refresher, ok := b.ts.(auth.RefreshingTokenSource)
	if !ok {
		// No refresh capability — let the caller see the 401.
		return resp, nil
	}
	// Drain + close the 401 response body so the underlying connection can
	// be reused for the retry.
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	newToken, _, refreshErr := refresher.ForceRefresh(ctx)
	if refreshErr != nil {
		return nil, fmt.Errorf("graphclient: force-refresh after 401 on %s %s: %w",
			req.Method, req.URL.Path, refreshErr)
	}
	retry := cloneRequestWithBearer(req, bodyBytes, newToken)
	return b.base.RoundTrip(retry)
}

// captureBody reads the request body fully into memory and replaces
// req.Body with a no-op closer over the captured bytes. Returns the buffered
// bytes so the retry path can construct a fresh body reader.
//
// For nil or http.NoBody, returns (nil, nil) and leaves the request alone.
func captureBody(req *http.Request) ([]byte, error) {
	if req.Body == nil || req.Body == http.NoBody {
		return nil, nil
	}
	buf, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, fmt.Errorf("graphclient: buffer request body for %s %s: %w",
			req.Method, req.URL.Path, err)
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(buf))
	return buf, nil
}

// cloneRequestWithBearer returns a shallow clone of req carrying a fresh
// Authorization: Bearer header (replacing any value already set) and a body
// reader sourced from the captured bytes. The clone shares the original
// request's context (so cancellation propagates) and URL.
func cloneRequestWithBearer(req *http.Request, body []byte, token string) *http.Request {
	clone := req.Clone(req.Context())
	if body == nil {
		clone.Body = http.NoBody
		clone.ContentLength = 0
	} else {
		clone.Body = io.NopCloser(bytes.NewReader(body))
		clone.ContentLength = int64(len(body))
	}
	if clone.Header == nil {
		clone.Header = make(http.Header)
	}
	clone.Header.Set("Authorization", "Bearer "+token)
	return clone
}

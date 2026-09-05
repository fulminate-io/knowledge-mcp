// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"

	"connectrpc.com/connect"

	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/clientver"
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
	sel := auth.SelectedAccount()
	httpClient := &http.Client{
		Transport: &bearerRoundTripper{
			ts:  ts,
			sel: sel,
			base: &http.Transport{
				ForceAttemptHTTP2: true,
				TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
				// Socket-write meter. Wraps the RAW conn beneath TLS, so ALPN
				// and h2 negotiation are untouched — ForceAttemptHTTP2 above is
				// what keeps h2 alive beside a custom dialer. See timedConn.
				DialContext: dialInstrumented,
			},
		},
		// No global timeout — long-running tool calls (large Execute payloads,
		// cloud-side computation) honor caller context.WithTimeout.
	}
	// The cloud path is exactly where the operation label matters — it is the
	// deployment that reads the per-tag metrics — so the stamper is installed
	// here as well as on the local client. The health client gets it too and is
	// unaffected: its request messages carry no client_context field.
	//
	// The freshness observer is PREPENDED for the same reason it is on the local
	// client: connect composes interceptors first-in-slice OUTERMOST, so it sees
	// the response finally returned to the caller. This is the constructor that
	// matters most for it — the cloud is the deployment serving a non-zero
	// watermark today, and a miss here is silent rather than compile-caught.
	// The account stamper is CLOUD-ONLY: it puts the selected Fulminate
	// account on every outbound RPC and refuses a selection already known to
	// be rejected. It is kept last in the slice so the freshness observer
	// stays first-in-slice (outermost), as documented above.
	gens := &atomic.Uint64{}
	stamp := connect.WithInterceptors(
		newFreshnessObserver(gens),
		newOperationInterceptor(),
		newAccountInterceptor(sel),
	)
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
	// sel is the same account selection the request-side interceptor reads.
	// This round-tripper is the one place on the Connect chain holding the raw
	// *http.Response, so response classification lives here.
	sel *auth.AccountSelection
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
		if refusal := b.classifyGatewayRejection(ctx, resp); refusal != nil {
			return nil, refusal
		}
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
	retried, retryErr := b.base.RoundTrip(retry)
	if retryErr != nil {
		return nil, retryErr
	}
	if refusal := b.classifyGatewayRejection(ctx, retried); refusal != nil {
		return nil, refusal
	}
	return retried, nil
}

// classifyGatewayRejection inspects a non-401 4xx response for the two gateway
// rejections this client must act on locally: a VERSION refusal, which it
// returns as an error so the call fails instructively, and an ACCOUNT
// rejection, which marks the selection invalid so the NEXT cloud call fails
// fast locally.
//
// This lives here rather than in the request interceptor because a Connect
// interceptor cannot see the gateway's rejection body: connect-go parses a
// non-200 body into a wire error declaring only code/message/details, so the
// gateway's JSON body unmarshals cleanly with the message left EMPTY. This
// round-tripper is the one place on the chain holding the raw *http.Response.
// The version refusal has EXACTLY that shape and for exactly that reason: the
// gateway emits it as a plain HTTP response before forwarding, so it never
// spoke connect's wire format either.
//
// The version refusal is classified BEFORE and INDEPENDENTLY of the account
// selection: it is a statement about this binary, not about which account was
// selected, and a client with no selection is refused over its version just the
// same.
//
// It never alters routing: there is no retry-without-the-header for either
// rejection and no retry against another account. Dropping the account header
// would route the user's writes into a DIFFERENT account than the one they
// selected; dropping the version header is what the gateway refuses over.
//
// The original body is DRAINED AND CLOSED before the replayable copy is
// substituted. io.LimitReader stops at the cap WITHOUT reaching EOF, so a
// rejection body larger than the cap would otherwise leave the original body
// unread and unclosed and the underlying connection would never be released —
// the same reason the 401 branch above drains and closes.
func (b *bearerRoundTripper) classifyGatewayRejection(ctx context.Context, resp *http.Response) error {
	if resp.StatusCode < 400 || resp.StatusCode >= 500 {
		return nil
	}
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, auth.MaxErrorBodyBytes))
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(raw))

	// THE HEADERS TRAVEL WITH THE BYTES, and on THIS transport that is the whole
	// difference between a remedy and a shrug. connect-go sets `Accept-Encoding:
	// gzip` on every unary call, which turns OFF net/http's transparent
	// decompression — so the bytes above are whatever encoding the gateway chose,
	// and a classifier handed the bytes alone parses gzip as JSON, fails, and
	// reports the gateway's perfectly good refusal as unreadable.
	//
	// resp.Body keeps the bytes AS THEY ARRIVED, undecoded, because the
	// non-refusal 4xx path below returns the response to connect, which reads it
	// against these same headers. Decoding happens inside the classifier, on a
	// copy, and never rewrites what the caller sees.
	if refusal, ok := auth.LatchVersionRefusal(auth.RefusalObservation{
		Status:    resp.StatusCode,
		Header:    resp.Header,
		Body:      raw,
		ReadErr:   readErr,
		Transport: "connect",
		Path:      responsePath(resp),
	}); ok {
		return refusal
	}

	if b.sel == nil {
		return nil
	}
	reason, latch := auth.ClassifyAccountRejection(resp.StatusCode, raw)
	if !latch {
		return nil
	}
	if id := b.sel.ID(ctx); id != "" {
		b.sel.MarkInvalid(id, reason)
	}
	return nil
}

// responsePath names the route a response came from, for error text. A
// *http.Response built by a RoundTripper always carries its Request, but the
// type permits nil, and a refusal error whose text said "<nil>" would be worse
// than one that says the route is unknown.
func responsePath(resp *http.Response) string {
	if resp.Request == nil || resp.Request.URL == nil {
		return "(unknown route)"
	}
	return resp.Request.URL.Path
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
	// The client's build identity rides every cloud-bound request. Stamping it
	// HERE rather than in RoundTrip is what puts it on the 401-refresh retry as
	// well as the first dispatch — both go through this clone.
	clientver.Stamp(clone.Header)
	return clone
}

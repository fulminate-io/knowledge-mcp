// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
)

// cloudCaptureHandler is a minimal HTTP handler that records every
// Authorization header it receives and returns a canned, valid connect-go
// EngineService.Execute response on the FIRST and any subsequent calls — or
// returns 401 once if the first401 flag is set, then 200 thereafter.
//
// We intentionally do NOT mount the full knowledgev1connect handler here.
// The cloud client is exercised at the http.RoundTripper layer — the
// bearerRoundTripper wraps and dispatches every request, regardless of which
// EngineService method connect-go is calling. Capturing the Authorization
// header on a single in-process handler is the smallest test vehicle.
type cloudCaptureHandler struct {
	mu        sync.Mutex
	seenAuth  []string
	first401  atomic.Bool
	cannedBin []byte
}

// ServeHTTP records the request and replies with either a 401 (if first401
// is armed, ONCE) or a 2xx + an empty protobuf body (connect-go's smallest
// valid response body for a unary RPC). The test cases under this harness
// only inspect headers and call counts — the cloud client returns an error
// because the response payload is empty, but that error is irrelevant to
// the auth-attachment + 401-retry assertions.
func (h *cloudCaptureHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.seenAuth = append(h.seenAuth, r.Header.Get("Authorization"))
	h.mu.Unlock()

	if h.first401.CompareAndSwap(true, false) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`unauthorized`))
		return
	}
	// Minimal connect-go unary response: protobuf content-type + empty body.
	// The client will fail to decode the response, but that is downstream of
	// the RoundTripper behavior we are verifying.
	w.Header().Set("Content-Type", "application/proto")
	w.WriteHeader(http.StatusOK)
	// G705 (gosec XSS-via-taint) flags w.Write(field) conservatively: cannedBin
	// is canned protobuf bytes fixed at construction (never request-derived),
	// written by an httptest server under an application/proto content-type to
	// exercise the RoundTripper's auth-retry. No untrusted source, no XSS surface.
	_, _ = w.Write(h.cannedBin) //nolint:gosec // G705 false positive: canned proto bytes, not request-derived
}

// observed returns a snapshot of the Authorization headers seen so far.
func (h *cloudCaptureHandler) observed() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.seenAuth))
	copy(out, h.seenAuth)
	return out
}

// TestNewCloudGraphClient_AttachesBearer proves the bearerRoundTripper
// attaches Authorization: Bearer <token> on every outgoing request. We do
// not care whether the response payload decodes cleanly — only that the
// server saw the right credential on the wire.
func TestNewCloudGraphClient_AttachesBearer(t *testing.T) {
	h := &cloudCaptureHandler{}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	src := auth.StaticTokenSource{AccessToken: "tok-abc"}
	gc := NewCloudGraphClient(srv.URL, src)

	// Issue one Execute. The handler returns a malformed payload — we ignore
	// the decode error; the assertion is on the request header captured server-side.
	_, _ = gc.Execute(opCtx(), &knowledgev1.ExecuteRequest{})

	got := h.observed()
	require.NotEmpty(t, got, "handler should have seen at least one request")
	assert.Equal(t, "Bearer tok-abc", got[0],
		"first request must carry the bearer token from the StaticTokenSource")
}

// cloudRefresher is a RefreshingTokenSource that rotates its token on
// every ForceRefresh call, mirroring auth/sync_transport_bytes_test.go:106
// refreshingStub but local to graphclient_test (no cycle).
type cloudRefresher struct {
	mu         sync.Mutex
	current    string
	refreshCnt int
}

func (c *cloudRefresher) Token(_ context.Context) (string, auth.PermissionSet, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current, nil, nil
}

func (c *cloudRefresher) ForceRefresh(_ context.Context) (string, auth.PermissionSet, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshCnt++
	c.current = "tok-rotated"
	return c.current, nil, nil
}

// TestNewCloudGraphClient_RetriesOn401 proves the bearerRoundTripper path
// implements the 401 → ForceRefresh → retry-once contract. We stub a handler
// that returns 401 ONCE and 200 thereafter; the client's retry must reach
// the handler a second time with the rotated bearer.
func TestNewCloudGraphClient_RetriesOn401(t *testing.T) {
	h := &cloudCaptureHandler{}
	h.first401.Store(true)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	src := &cloudRefresher{current: "tok-stale"}
	gc := NewCloudGraphClient(srv.URL, src)

	_, _ = gc.Execute(opCtx(), &knowledgev1.ExecuteRequest{})

	got := h.observed()
	require.GreaterOrEqual(t, len(got), 2,
		"expected at least 2 requests (first 401 + 1 retry), got %d", len(got))
	assert.Equal(t, 1, src.refreshCnt,
		"ForceRefresh should fire exactly once after the 401")
	assert.Equal(t, "Bearer tok-stale", got[0], "first request used the original token")
	assert.Equal(t, "Bearer tok-rotated", got[1], "retry must carry the rotated token")
}

// TestCloudAuth_StaticTokenSource_NoRetryOn401 proves the headless
// machine-auth contract: a StaticTokenSource does NOT implement
// RefreshingTokenSource, so a 401 from the backend is surfaced to the caller
// with exactly ONE upstream request — the bearerRoundTripper has no
// force-refresh capability to exercise and must not retry. The machine bearer
// is opaque to the client; recovering from a rejected token is the operator's
// job (rotate the token), not a client-side refresh loop.
func TestCloudAuth_StaticTokenSource_NoRetryOn401(t *testing.T) {
	h := &cloudCaptureHandler{}
	h.first401.Store(true) // armed, but a non-refreshing source must NOT retry past it
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	src := auth.StaticTokenSource{AccessToken: "machine-tok"}
	gc := NewCloudGraphClient(srv.URL, src)

	// The Execute returns an error (the 401 surfaces, the payload never decodes);
	// the assertion is purely on the upstream request count + bearer attachment.
	_, _ = gc.Execute(opCtx(), &knowledgev1.ExecuteRequest{})

	got := h.observed()
	require.Len(t, got, 1,
		"a 401 under a non-refreshing StaticTokenSource must surface with exactly one upstream request (no force-refresh retry)")
	assert.Equal(t, "Bearer machine-tok", got[0],
		"the single request carries the machine bearer token")
}

// protoCapture wraps a downstream handler and records, for the FIRST request it
// observes, the protocol major version, HTTP method, and Content-Type header —
// the wire facts the HTTP/1.1-determinism assertions inspect. It is the only new
// glue this test needs; no existing harness records r.ProtoMajor.
type protoCapture struct {
	next        http.Handler
	mu          sync.Mutex
	protoMajor  int
	method      string
	contentType string
	seen        bool
}

func (p *protoCapture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	if !p.seen {
		p.protoMajor = r.ProtoMajor
		p.method = r.Method
		p.contentType = r.Header.Get("Content-Type")
		p.seen = true
	}
	p.mu.Unlock()
	p.next.ServeHTTP(w, r)
}

// TestCloudClient_RoundTripAdvancesSocketMeter proves the socket-write meter is
// actually INSTALLED on the shared cloud transport, not merely declared. One
// real Execute through NewCloudGraphClient must move the process-wide counters;
// if DialContext were dropped from the transport literal the traffic would
// still succeed and only this assertion would go red — which a field-presence
// check could never catch.
//
// Cleartext httptest server for the same reason TestCloudExecute_UnaryPOSTOverHTTP11
// uses one (see its SCOPE OF THE CLAIM note): NewCloudGraphClient hardcodes
// TLSClientConfig with no RootCAs injection seam. The dialer under test sits
// BELOW TLS, so a cleartext vehicle exercises exactly the code path that matters.
func TestCloudClient_RoundTripAdvancesSocketMeter(t *testing.T) {
	canned := enginetest.ResponseWithNodes(&knowledgev1.Node{Id: "n1"})
	handler := &stubEngine{respond: func(_ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
		return canned, nil
	}}
	mux := http.NewServeMux()
	path, hdlr := knowledgev1connect.NewEngineServiceHandler(handler)
	mux.Handle(path, hdlr)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	gc := NewCloudGraphClient(srv.URL, auth.StaticTokenSource{AccessToken: "tok"})

	before := SocketWriteSnapshot()
	resp, err := gc.Execute(opCtx(), &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: "x"}},
	})
	require.NoError(t, err, "the round trip itself must succeed")
	require.NotNil(t, resp)
	after := SocketWriteSnapshot()

	assert.Greater(t, after.Writes, before.Writes,
		"the instrumented dialer must be wired into the shared cloud transport")
	assert.Greater(t, after.Bytes, before.Bytes,
		"request bytes travel through the timed Write")

	// The h2-preservation claim gets a compiled check, not just a grep: a custom
	// DialContext disables HTTP/2 unless ForceAttemptHTTP2 is set.
	rt, ok := gc.httpClient.Transport.(*bearerRoundTripper)
	require.True(t, ok, "the cloud client wraps its transport in a bearerRoundTripper")
	base, ok := rt.base.(*http.Transport)
	require.True(t, ok, "the bearer round-tripper's base is the shared http.Transport")
	assert.True(t, base.ForceAttemptHTTP2,
		"ForceAttemptHTTP2 must stay true beside the custom dialer or h2 is silently lost")
	assert.NotNil(t, base.DialContext, "the meter's dialer is installed on that same transport")
}

// TestCloudExecute_UnaryPOSTOverHTTP11 pins that NewCloudGraphClient issues
// EngineService.Execute as a Connect UNARY POST that succeeds over HTTP/1.1 with
// no gRPC/gRPC-Web option on the client. The backend is the REAL generated
// EngineService handler (reusing the in-package stubEngine), mounted behind a
// plain cleartext httptest.NewServer — which is HTTP/1.1-only — so a successful
// round-trip plus ProtoMajor==1 proves cloud Execute reaches the engine
// deterministically over h1.1.
//
// SCOPE OF THE CLAIM (reviewer T3-1): this is a CLEARTEXT server, so it does NOT
// exercise the TLS-ALPN h2-negotiate-then-fall-back-to-h1.1 path the ticket
// rationale names — over cleartext, h2 is never attempted at all. Wiring the
// client to a TLS test cert is impractical without a production change:
// NewCloudGraphClient (cloud_auth.go:40) hardcodes its base http.Transport's
// TLSClientConfig with no RootCAs/CA-injection seam, and adding one is out of
// scope (tests-only ticket). So this test proves the narrower, still-load-bearing
// fact — Connect unary POST works over HTTP/1.1 and carries a non-gRPC
// content-type — NOT that ForceAttemptHTTP2 "fell back via ALPN". GREEN confirms
// NO production transport change is needed.
func TestCloudExecute_UnaryPOSTOverHTTP11(t *testing.T) {
	canned := enginetest.ResponseWithNodes(&knowledgev1.Node{Id: "n1"})

	handler := &stubEngine{respond: func(_ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
		return canned, nil
	}}
	mux := http.NewServeMux()
	path, hdlr := knowledgev1connect.NewEngineServiceHandler(handler)
	mux.Handle(path, hdlr)

	cap := &protoCapture{next: mux}
	// Plain (non-h2c) httptest server: HTTP/1.1-only front door.
	srv := httptest.NewServer(cap)
	t.Cleanup(srv.Close)

	gc := NewCloudGraphClient(srv.URL, auth.StaticTokenSource{AccessToken: "tok"})

	req := &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{
			Query: &knowledgev1.QueryPlan{ById: "x"},
		},
	}
	resp, err := gc.Execute(opCtx(), req)
	require.NoError(t, err, "cloud Execute must succeed over HTTP/1.1")
	require.NotNil(t, resp)
	require.Len(t, resp.GetNodes(), 1, "the canned node round-trips")
	assert.Equal(t, "n1", resp.GetNodes()[0].GetId())

	cap.mu.Lock()
	defer cap.mu.Unlock()
	require.True(t, cap.seen, "the backend must have observed at least one request")
	assert.Equal(t, 1, cap.protoMajor, "Execute travels over HTTP/1.1 (ProtoMajor==1)")
	assert.Equal(t, http.MethodPost, cap.method, "Connect unary is a POST")
	assert.False(t, strings.HasPrefix(cap.contentType, "application/grpc"),
		"content-type must NOT be gRPC/gRPC-Web (got %q) — no gRPC option on the cloud client", cap.contentType)
}

// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
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
	_, _ = gc.Execute(context.Background(), &knowledgev1.ExecuteRequest{})

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

	_, _ = gc.Execute(context.Background(), &knowledgev1.ExecuteRequest{})

	got := h.observed()
	require.GreaterOrEqual(t, len(got), 2,
		"expected at least 2 requests (first 401 + 1 retry), got %d", len(got))
	assert.Equal(t, 1, src.refreshCnt,
		"ForceRefresh should fire exactly once after the 401")
	assert.Equal(t, "Bearer tok-stale", got[0], "first request used the original token")
	assert.Equal(t, "Bearer tok-rotated", got[1], "retry must carry the rotated token")
}

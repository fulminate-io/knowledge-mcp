// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
)

// refuseHandler is a minimal fault-injection HTTP middleware: it
// returns 503 (StatusServiceUnavailable) for the next refuseRemaining
// requests, then self-heals back to delegating to inner. This is the
// "brief server downtime then recovery" shape the unary reconnect
// interceptor is built to survive. Kept self-contained inside package
// server so the in-package test exercises the REAL production
// newReconnectInterceptor() without importing any client- or
// server-internal package.
type refuseHandler struct {
	inner           http.Handler
	mu              sync.Mutex
	refuseRemaining int
	faults          atomic.Int32
}

// refuseNext arms the handler to 503 the next n requests before
// reverting to healthy delegation.
func (h *refuseHandler) refuseNext(n int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.refuseRemaining = n
}

func (h *refuseHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	if h.refuseRemaining > 0 {
		h.refuseRemaining--
		h.faults.Add(1)
		h.mu.Unlock()
		http.Error(w, "simulated unavailable", http.StatusServiceUnavailable)
		return
	}
	h.mu.Unlock()
	h.inner.ServeHTTP(w, r)
}

// newRefuseTestServer stands up an h2c-wrapped httptest.Server whose
// handler is h. Cleanup is registered on t.
func newRefuseTestServer(t *testing.T, h *refuseHandler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h2c.NewHandler(h, &http2.Server{}))
	t.Cleanup(srv.Close)
	return srv
}

// newH2CClient returns an h2c-capable http.Client matching the
// production wiring (cleartext HTTP/2 over a loopback dial).
func newH2CClient() *http.Client {
	return &http.Client{
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(_ context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				return net.Dial(network, addr)
			},
		},
	}
}

// TestSelfHealingWire_UnarySurvivesRestart verifies that after two
// 503 responses (simulating a brief server downtime), the production
// unary reconnect interceptor retries past them and surfaces success
// to the caller. Wall-clock elapsed should be within the first two
// backoff windows, not the full 4.25s.
//
// This is the in-package home for what used to live as a rebuilt
// interceptor in cmd/knowledge-server/bootstrap — here it drives the
// real newReconnectInterceptor() directly.
func TestSelfHealingWire_UnarySurvivesRestart(t *testing.T) {
	mux := http.NewServeMux()
	var counter atomic.Int32
	stub := &stubHealth{attempt: &counter, respond: func(_ int32) error { return nil }}
	path, hdlr := knowledgev1connect.NewHealthServiceHandler(stub)
	mux.Handle(path, hdlr)

	flaky := &refuseHandler{inner: mux}
	srv := newRefuseTestServer(t, flaky)

	httpClient := newH2CClient()
	retry := connect.WithInterceptors(newReconnectInterceptor())
	client := knowledgev1connect.NewHealthServiceClient(httpClient, srv.URL, retry)

	// Warm the conn.
	_, err := client.Check(context.Background(),
		connect.NewRequest(&knowledgev1.HealthCheckRequest{}))
	require.NoError(t, err)

	// Refuse the next 2 calls; the interceptor retries past them.
	flaky.refuseNext(2)

	start := time.Now()
	_, err = client.Check(context.Background(),
		connect.NewRequest(&knowledgev1.HealthCheckRequest{}))
	elapsed := time.Since(start)
	require.NoError(t, err, "interceptor should retry past the two 503s")

	// First two backoff windows = 50ms + 200ms = 250ms total.
	assert.Less(t, elapsed, 2*time.Second,
		"retry should complete within first two backoff windows; took %s", elapsed)
	assert.GreaterOrEqual(t, flaky.faults.Load(), int32(2),
		"at least two refuse responses observed")
}

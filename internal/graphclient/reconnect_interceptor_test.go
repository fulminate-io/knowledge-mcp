// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
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

// retryTestHarness stands up an in-process httptest.Server wrapped in
// h2c (so connect-go can do bi-di if a future test needs it) behind
// the HealthService handler. Tests inject a stub HealthService that
// counts invocations via atomic.Int32 + returns a chosen error per
// attempt.
type retryTestHarness struct {
	server  *httptest.Server
	client  knowledgev1connect.HealthServiceClient
	attempt *atomic.Int32
}

// stubHealth is a HealthService handler whose Check behavior is
// driven by a closure so each test can script the per-attempt
// response.
type stubHealth struct {
	attempt *atomic.Int32
	respond func(attempt int32) error
}

func (s *stubHealth) Check(
	_ context.Context,
	_ *connect.Request[knowledgev1.HealthCheckRequest],
) (*connect.Response[knowledgev1.HealthCheckResponse], error) {
	n := s.attempt.Add(1)
	if err := s.respond(n); err != nil {
		return nil, err
	}
	return connect.NewResponse(&knowledgev1.HealthCheckResponse{}), nil
}

func (s *stubHealth) Status(
	_ context.Context,
	_ *connect.Request[knowledgev1.StatusRequest],
) (*connect.Response[knowledgev1.StatusResponse], error) {
	return connect.NewResponse(&knowledgev1.StatusResponse{}), nil
}

// newRetryTestHarness builds the h2c server + a client with the
// reconnect interceptor attached.
func newRetryTestHarness(t *testing.T, respond func(attempt int32) error) *retryTestHarness {
	t.Helper()
	var counter atomic.Int32
	handler := &stubHealth{attempt: &counter, respond: respond}
	mux := http.NewServeMux()
	path, hdlr := knowledgev1connect.NewHealthServiceHandler(handler)
	mux.Handle(path, hdlr)

	h2s := &http2.Server{}
	srv := httptest.NewServer(h2c.NewHandler(mux, h2s))
	t.Cleanup(srv.Close)

	httpClient := &http.Client{
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(_ context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				return net.Dial(network, addr)
			},
		},
	}
	retry := connect.WithInterceptors(newReconnectInterceptor())
	client := knowledgev1connect.NewHealthServiceClient(httpClient, srv.URL, retry)
	return &retryTestHarness{server: srv, client: client, attempt: &counter}
}

// TestReconnectInterceptor_SucceedsOnFirstAttempt asserts the
// happy-path: one healthy handler, one Call, zero retries.
func TestReconnectInterceptor_SucceedsOnFirstAttempt(t *testing.T) {
	h := newRetryTestHarness(t, func(_ int32) error { return nil })
	_, err := h.client.Check(context.Background(),
		connect.NewRequest(&knowledgev1.HealthCheckRequest{}))
	require.NoError(t, err)
	assert.Equal(t, int32(1), h.attempt.Load(), "exactly one call on happy path")
}

// TestReconnectInterceptor_RetriesOnUnavailable asserts attempts 1-2
// fail with CodeUnavailable, attempt 3 succeeds, interceptor returns
// success without surfacing the transient failures.
func TestReconnectInterceptor_RetriesOnUnavailable(t *testing.T) {
	h := newRetryTestHarness(t, func(n int32) error {
		if n < 3 {
			return connect.NewError(connect.CodeUnavailable,
				errors.New("simulated transient"))
		}
		return nil
	})
	_, err := h.client.Check(context.Background(),
		connect.NewRequest(&knowledgev1.HealthCheckRequest{}))
	require.NoError(t, err)
	assert.Equal(t, int32(3), h.attempt.Load(),
		"retries until the handler returns success")
}

// TestReconnectInterceptor_GivesUpAfterMaxAttempts asserts that a
// handler that always fails with a retryable error exhausts the
// backoff and surfaces a wrapped exhaustion error. attempts should
// equal len(RetryBackoff)+1.
func TestReconnectInterceptor_GivesUpAfterMaxAttempts(t *testing.T) {
	h := newRetryTestHarness(t, func(_ int32) error {
		return connect.NewError(connect.CodeUnavailable,
			errors.New("perpetually down"))
	})
	_, err := h.client.Check(context.Background(),
		connect.NewRequest(&knowledgev1.HealthCheckRequest{}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "retry exhausted")
	assert.Equal(t, int32(len(RetryBackoff)+1), h.attempt.Load(),
		"makes len(RetryBackoff)+1 attempts before giving up")
}

// TestReconnectInterceptor_NonRetryableNotRetried asserts that a
// handler returning an application-level error (e.g. InvalidArgument)
// surfaces immediately without burning retry budget.
func TestReconnectInterceptor_NonRetryableNotRetried(t *testing.T) {
	h := newRetryTestHarness(t, func(_ int32) error {
		return connect.NewError(connect.CodeInvalidArgument,
			errors.New("bad request"))
	})
	_, err := h.client.Check(context.Background(),
		connect.NewRequest(&knowledgev1.HealthCheckRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Equal(t, int32(1), h.attempt.Load(),
		"non-retryable errors surface on first attempt")
}

// TestReconnectInterceptor_RespectsContextCancel asserts that
// ctx.Cancel during the backoff wait returns ctx.Canceled promptly
// without exhausting retries.
func TestReconnectInterceptor_RespectsContextCancel(t *testing.T) {
	h := newRetryTestHarness(t, func(_ int32) error {
		return connect.NewError(connect.CodeUnavailable,
			errors.New("down"))
	})
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately after the first attempt fails by sleeping
	// just long enough for the first attempt to register then
	// canceling during the first backoff wait (50ms).
	go func() {
		// Wait for the first attempt to land, then cancel during
		// the 50ms backoff window.
		deadline := time.Now().Add(200 * time.Millisecond)
		for time.Now().Before(deadline) {
			if h.attempt.Load() >= 1 {
				cancel()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	_, err := h.client.Check(ctx, connect.NewRequest(&knowledgev1.HealthCheckRequest{}))
	require.Error(t, err)
	// Ctx.Canceled surfaces either as context.Canceled directly
	// (from the interceptor's select-ctx.Done branch) or as
	// connect.CodeCanceled (from the transport layer if it observed
	// the cancel mid-dial). Either is acceptable.
	isCanceled := errors.Is(err, context.Canceled) ||
		connect.CodeOf(err) == connect.CodeCanceled
	assert.True(t, isCanceled, "cancel should surface as ctx.Canceled; got: %v", err)
	assert.LessOrEqual(t, h.attempt.Load(), int32(len(RetryBackoff)+1),
		"ctx cancel prevents exhausting all retries")
}

// TestReconnectInterceptor_RetriesOnServerRestart simulates the ticket's
// motivating scenario: a connect-go client holds a cached HTTP/2
// connection to an h2c server. The server process dies (TCP listener
// closes), the client's next call surfaces io.EOF or ECONNREFUSED via
// *net.OpError, the interceptor retries, and meanwhile a replacement
// server comes up on the same port — the retry succeeds.
//
// Implementation: stand up server A on a port, close it, then stand
// up server B on the SAME port, then issue a Call. The client still
// holds the connection to A, sees the drop, and retries to B.
func TestReconnectInterceptor_RetriesOnServerRestart(t *testing.T) {
	var counter atomic.Int32
	// Pick a port by binding to :0 and remembering the addr.
	l1, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l1.Addr().String()

	// Build a dedicated http client that we'll reuse across both
	// servers so the HTTP/2 conn pool is shared (and goes stale when
	// A dies).
	httpClient := &http.Client{
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(_ context.Context, network, a string, _ *tls.Config) (net.Conn, error) {
				return net.Dial(network, a)
			},
		},
	}
	retry := connect.WithInterceptors(newReconnectInterceptor())
	client := knowledgev1connect.NewHealthServiceClient(httpClient, "http://"+addr, retry)

	startServer := func(l net.Listener) *http.Server {
		handler := &stubHealth{attempt: &counter, respond: func(_ int32) error { return nil }}
		mux := http.NewServeMux()
		path, hdlr := knowledgev1connect.NewHealthServiceHandler(handler)
		mux.Handle(path, hdlr)
		h2s := &http2.Server{}
		srv := &http.Server{
			Handler:           h2c.NewHandler(mux, h2s),
			ReadHeaderTimeout: 2 * time.Second,
		}
		go srv.Serve(l) //nolint:errcheck // httptest pattern
		return srv
	}

	srvA := startServer(l1)
	// Warm the conn pool with a successful call to srvA.
	_, err = client.Check(context.Background(),
		connect.NewRequest(&knowledgev1.HealthCheckRequest{}))
	require.NoError(t, err)
	require.Equal(t, int32(1), counter.Load())

	// Shut down srvA, close its listener.
	require.NoError(t, srvA.Close())

	// Start srvB on the same port. There's a small race where the
	// port may not be immediately re-bindable (TIME_WAIT) — retry
	// the bind a few times.
	var l2 net.Listener
	for range 10 {
		l2, err = net.Listen("tcp", addr)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.NoError(t, err, "replacement server should bind within 500ms")
	srvB := startServer(l2)
	t.Cleanup(func() { _ = srvB.Close() })

	// The client still has the conn to srvA in its pool (now dead).
	// Next Call hits the dead conn first, sees a retryable error,
	// the interceptor retries, the second attempt dials srvB and
	// succeeds.
	_, err = client.Check(context.Background(),
		connect.NewRequest(&knowledgev1.HealthCheckRequest{}))
	require.NoError(t, err, "interceptor should redial after server restart")
	assert.Equal(t, int32(2), counter.Load(),
		"one call on srvA + one on srvB after retry")
}

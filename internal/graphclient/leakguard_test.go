// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"net/http"
	"testing"

	"go.uber.org/goleak"
	"golang.org/x/net/http2"
)

// closeIdleOnCleanup registers a teardown that drops the client's idle HTTP/2
// connections, and returns the client so it can wrap a constructor call inline.
//
// WHY THE TESTS NEED IT, and why it is TEST HYGIENE rather than a production
// defect. Every harness here already does `t.Cleanup(srv.Close)`. That is not
// enough for h2c: httptest.Server.Close waits for outstanding requests and closes
// the listener, but the CLIENT is holding an established HTTP/2 connection open in
// its pool, and the server's serverConn.serve goroutine lives as long as that
// connection does. The result is one leaked serve goroutine (plus its readFrames
// child) per harness, none of which fails any test — this package's goleak gate is
// what surfaced them.
//
// Nothing in production leaks here: a real client's transport outlives any single
// request ON PURPOSE, which is the point of a connection pool. It is the test's
// job to end that lifetime when the test ends.
func closeIdleOnCleanup(t *testing.T, gc *GraphClient) *GraphClient {
	t.Helper()
	t.Cleanup(func() {
		if gc == nil || gc.httpClient == nil {
			return
		}
		type idleCloser interface{ CloseIdleConnections() }
		switch tr := gc.httpClient.Transport.(type) {
		case *http2.Transport:
			tr.CloseIdleConnections()
		case *http.Transport:
			tr.CloseIdleConnections()
		case idleCloser:
			tr.CloseIdleConnections()
		}
	})
	return gc
}

// TestMain runs this package's tests under a goroutine-leak gate.
//
// StartKeepalive spawns the keepalive ticker (client_keepalive.go:35) and Run
// spawns both the session reaper (mcp_http.go:141) and the serve loop (:174). The
// keepalive is the dangerous one for tests: it holds an HTTP connection open on a
// ticker, so a client minted and dropped keeps talking to a test server that has
// already asserted and returned.
//
// THE PER-TEST SYMPTOM IS NOTHING AT ALL, which is exactly why this has to be a
// package-level gate rather than an assertion someone remembers to write: a leaked
// goroutine does not fail the test that leaked it, it fails whatever runs after it,
// or nothing at all until the leak becomes a resource exhaustion in production.
//
// The allowlist is deliberately EMPTY. An entry added here later must name the
// goroutine and say why its lifetime legitimately exceeds the test that started it.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
)

// closeIdleOnCleanup registers a teardown that closes the client's HTTP/2
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
//
// The name is kept for its call sites; the release underneath is GraphClient.Close,
// which owns the connections it dialed rather than asking the pool whether it
// considers them idle. The pool-level release this helper used to do reaches a
// connection only once the transport has finished retiring its last stream, and a
// test that asserts and returns does not wait for that.
func closeIdleOnCleanup(t *testing.T, gc *GraphClient) *GraphClient {
	t.Helper()
	t.Cleanup(gc.Close)
	return gc
}

// newOwnedH2CClient builds the cleartext-HTTP/2 client these tests dial their
// stubs with, and registers a teardown that CLOSES the connections it dialed.
//
// WHY IT IS NOT A BARE http.Client WITH A POOL RELEASE, which is what every one
// of its call sites used to build for itself. A GraphClient reaches its
// connections through GraphClient.Close; a hand-built transport has no such
// handle, so those harnesses could only call CloseIdleConnections — a release
// that reaches a connection only once the transport has finished retiring its
// last stream. A test that asserts and returns does not wait for that, and what
// the release skips nothing else reaps: this transport sets no idle timeout, so
// the connection, its read loop and the stub's serve goroutines live to the end
// of the binary and land on the package's goleak gate with no failing test to
// point at.
//
// It tracks through the SAME ownedConns the production client uses rather than
// re-implementing the bookkeeping, so a test client and a real one lose their
// connections by one mechanism.
func newOwnedH2CClient(t *testing.T) *http.Client {
	t.Helper()
	owned := &ownedConns{}
	tr := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			conn, err := d.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return owned.track(conn), nil
		},
	}
	t.Cleanup(func() {
		owned.closeAll()
		tr.CloseIdleConnections()
	})
	return &http.Client{Transport: tr}
}

// TestNewOwnedH2CClient_CleanupClosesWhatItDialed is the wiring gate on the
// helper above: it proves the teardown it registers actually reaches the
// connection, rather than tracking a dialer nobody consults.
//
// THE SUBTEST IS THE INSTRUMENT. A helper's cleanup runs when ITS test ends, so
// no assertion inside that test can observe the state after it. Driving the
// client inside a subtest moves that boundary inward: the subtest's cleanup has
// run by the time the parent asserts, and the parent is looking at the world the
// helper left behind.
//
// THE STUB STAYS OPEN across the assertion, for the reason it stays open
// everywhere else in this change — closing it first would reap the client's read
// loop from the peer's side and the assertion would pass against a helper that
// releases nothing.
//
// WHAT IT DISCRIMINATES, measured rather than claimed: deleting the helper's
// t.Cleanup entirely turns it red. It does NOT reproduce the pool-release race —
// that property is pinned deterministically by
// TestGraphClientClose_ReachesAConnectionThePoolReleaseCannot, which forces an
// in-flight request. This one answers the cheaper question of whether the
// teardown is wired at all.
func TestNewOwnedH2CClient_CleanupClosesWhatItDialed(t *testing.T) {
	var counter atomic.Int32
	mux := http.NewServeMux()
	path, hdlr := knowledgev1connect.NewHealthServiceHandler(
		&stubHealth{attempt: &counter, respond: func(int32) error { return nil }})
	mux.Handle(path, hdlr)

	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	ignore := goleak.IgnoreCurrent()

	t.Run("a client dials, answers, and is torn down", func(t *testing.T) {
		client := knowledgev1connect.NewHealthServiceClient(newOwnedH2CClient(t), srv.URL)
		_, err := client.Check(context.Background(), connect.NewRequest(&knowledgev1.CheckRequest{}))
		require.NoError(t, err, "the stub answers Check, so the round trip must succeed and a connection must exist to leak")
	})

	require.NoError(t, goleak.Find(ignore),
		"the subtest's client has been torn down and its stub is still open, yet goroutines started since the snapshot are running: "+
			"the helper left the connection it dialed open, and the stub's serve and readFrames goroutines with it")
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

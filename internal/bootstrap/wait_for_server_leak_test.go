// SPDX-License-Identifier: Apache-2.0

package bootstrap

// wait_for_server_leak_test.go pins the connection OWNERSHIP half of the
// readiness wait, which TestWaitForServer_Healthy cannot see.
//
// WHY A SEPARATE PROPERTY. TestWaitForServer_Healthy asserts the functional
// contract — a live health handler makes the wait return nil — and it tears its
// stub down with CloseClientConnections, which reaps the connection from the
// PEER's side. That teardown is correct for that test and it makes the test
// blind to who owns the connection: a waitForServer that abandons everything it
// dialed passes it just as happily.
//
// The property here is the one the suite-level goleak gate charges to the whole
// package rather than to any test: waitForServer's release must reach the
// connection it dialed. A pool-level CloseIdleConnections only reaches
// connections the transport already considers IDLE, and the health round trip
// has only just completed when the deferred release runs — the transport's own
// goroutine has not necessarily finished retiring the stream yet. When the
// release loses that race the connection stays up, the peer's x/net/http2
// serverConn.serve and readFrames goroutines stay with it, and all three outlive
// the call. On a loaded machine the race loses often enough to flake the
// package's goleak gate with no failing test to point at.
//
// THE STUB IS DELIBERATELY LEFT OPEN across the assertion. Calling
// srv.CloseClientConnections() first would tear the connections down from the
// peer's side and reap the client's read loops as a side effect, which is
// exactly what makes the functional test blind. The cleanup closes it after.
//
// WHAT THIS TEST DOES AND DOES NOT DISCRIMINATE, measured rather than assumed.
// Deleting waitForServer's release outright turns it red in about a second, so
// it is a live gate on the release existing and working. It does NOT reproduce
// the RACE: with the pool-level release restored, this loop passes on an
// unloaded machine at 30 attempts, under GOMAXPROCS=1, and with the health probe
// aborted mid-flight — the transport retires the stream before the release runs
// every time here. The race that flakes the runner is scheduling-dependent and
// is not reproducible on demand. The property that a release must reach a
// connection the pool declines to close is pinned deterministically one layer
// down, in graphclient's TestGraphClientClose_ReachesAConnectionThePoolReleaseCannot,
// which forces that state with an in-flight request; this test is the wiring
// half — that waitForServer uses such a release and leaves nothing running.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
)

func TestWaitForServer_ReleasesEveryConnectionItDialed(t *testing.T) {
	const attempts = 30

	mux := http.NewServeMux()
	path, handler := knowledgev1connect.NewHealthServiceHandler(&fakeHealthHandler{})
	mux.Handle(path, handler)

	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	port := portFromURL(t, srv.URL)

	// Snapshotted AFTER the stub is up, so its accept loop and anything an
	// earlier test in this package left running are outside the assertion.
	ignore := goleak.IgnoreCurrent()

	for i := range attempts {
		require.NoErrorf(t, waitForServer(port, 3*time.Second),
			"attempt %d: the stub answers HealthService.Check, so the wait must succeed", i)
	}

	// goleak.Find retries with backoff, so an exit that is merely asynchronous
	// is tolerated; what it will not tolerate is a connection nobody closed.
	require.NoErrorf(t, goleak.Find(ignore),
		"after %d readiness waits against a stub that is still open, goroutines started during the loop are still running: "+
			"waitForServer left the connections it dialed open, so each one — plus the peer's serve and readFrames "+
			"goroutines — outlived the call that opened it", attempts)
}

// SPDX-License-Identifier: Apache-2.0

package graphclient

// owned_conns_test.go pins the difference between the two releases a
// GraphClient offers, using the state where they provably differ: a connection
// carrying a request the transport has not retired.
//
// WHY THAT STATE. Both releases look identical on a client whose round trips
// have all completed and been retired — the pool considers the connection idle
// and closes it, so a test written against that state passes on a client that
// owns nothing. The busy connection separates them: a pool-level release reaches
// only what the transport already calls idle, and this transport sets no idle
// timeout, so what the release skips nothing else reaps.
//
// The stub is deliberately left OPEN across both assertions. Closing it first
// would tear the connection down from the peer's side and end the client's read
// loop as a side effect, which is what makes a functional test blind to
// ownership in the first place.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
)

// blockingHealthHandler answers Check only once released is closed, so a test
// can hold a connection in the state where the transport has an open stream.
type blockingHealthHandler struct {
	entered  chan struct{}
	released chan struct{}
}

func (h *blockingHealthHandler) Check(ctx context.Context, _ *connect.Request[knowledgev1.CheckRequest]) (*connect.Response[knowledgev1.CheckResponse], error) {
	close(h.entered)
	select {
	case <-h.released:
	case <-ctx.Done():
	}
	return connect.NewResponse(&knowledgev1.CheckResponse{}), nil
}

func (h *blockingHealthHandler) Status(_ context.Context, _ *connect.Request[knowledgev1.StatusRequest]) (*connect.Response[knowledgev1.StatusResponse], error) {
	return connect.NewResponse(&knowledgev1.StatusResponse{}), nil
}

func TestGraphClientClose_ReachesAConnectionThePoolReleaseCannot(t *testing.T) {
	handler := &blockingHealthHandler{entered: make(chan struct{}), released: make(chan struct{})}
	mux := http.NewServeMux()
	path, h := knowledgev1connect.NewHealthServiceHandler(handler)
	mux.Handle(path, h)

	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	gc := NewGraphClientForURL(srv.URL)

	// Snapshotted after the stub is up: its accept loop is not part of the
	// question, the goroutines the client conn brings with it are.
	ignore := goleak.IgnoreCurrent()

	healthy := make(chan bool, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		healthy <- gc.HealthyCtx(ctx)
	}()

	select {
	case <-handler.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the stub's Check handler was never entered — the client never reached the server, so this test never established the busy-connection state it exists to test")
	}

	// CONTROL. The connection is carrying an open stream, so the pool declines
	// to close it. Without this arm, the assertion below could not distinguish
	// "Close tore the connection down" from "there was nothing left to tear
	// down by then".
	gc.CloseIdleConnections()
	require.Error(t, goleak.Find(ignore),
		"a pool-level release closed a connection with a request still in flight — "+
			"if the transport has started doing that, the ownership this file tests is no longer the thing keeping the connection reachable, and the assertion below proves nothing")
	select {
	case <-healthy:
		t.Fatal("the health call returned while the stub was still blocked in its handler — the request was not in flight during the control arm")
	default:
	}

	// THE PROPERTY. Close owns what it dialed, so it reaches the same connection.
	gc.Close()

	select {
	case ok := <-healthy:
		require.False(t, ok, "the health call rode a connection Close had already torn down and still reported the server healthy")
	case <-time.After(10 * time.Second):
		t.Fatal("the in-flight health call did not return within 10s of Close — its connection was not closed under it")
	}

	// Release the handler before the leak assertion: a stub goroutine parked in
	// a test channel is a goroutine started after the snapshot, and it would be
	// reported alongside the connections this test is actually asking about.
	close(handler.released)

	require.NoError(t, goleak.Find(ignore),
		"after Close, goroutines started since the snapshot are still running: the client's read loop and the peer's "+
			"serve goroutines outlived the client that opened their connection")
}

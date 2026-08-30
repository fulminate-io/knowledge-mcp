// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// stampingEngine is a countingEngine whose Execute stamps a fixed freshness
// watermark, standing in for the server-side response stamper. Two of these with
// DIFFERENT values is what makes "the ACTIVE backend's value" an assertion
// rather than a coincidence — a shared slot would read the same number either
// way round.
type stampingEngine struct {
	*countingEngine
	gen uint64
}

func (e *stampingEngine) Execute(
	_ context.Context,
	_ *connect.Request[knowledgev1.ExecuteRequest],
) (*connect.Response[knowledgev1.ExecuteResponse], error) {
	e.countingEngine.execute.Add(1)
	return connect.NewResponse(&knowledgev1.ExecuteResponse{FreshnessGen: e.gen}), nil
}

// startStampingEngine stands up an EngineService returning gen on every Execute.
// h2c wraps the handler for the local client (HTTP/2 with prior knowledge); the
// cloud client speaks plain HTTP over its own transport, so it takes h2c=false.
func startStampingEngine(t *testing.T, gen uint64, withH2C bool) string {
	t.Helper()
	mux := http.NewServeMux()
	path, hdlr := knowledgev1connect.NewEngineServiceHandler(
		&stampingEngine{countingEngine: &countingEngine{}, gen: gen})
	mux.Handle(path, hdlr)

	var handler http.Handler = mux
	if withH2C {
		handler = h2c.NewHandler(mux, &http2.Server{})
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	return srv.URL
}

// TestRouterFreshnessGenPicksBackend proves the forwarder reads the backend the
// CALL routed to, not a process-wide slot: local and cloud serve different
// watermarks for different accounts, so a shared cell would flap between them.
// The ErrNoBackend leg reads 0 — and the two preceding legs are its known
// positives, without which a forwarder hardcoded to 0 would look correct.
func TestRouterFreshnessGenPicksBackend(t *testing.T) {
	localURL := startStampingEngine(t, 11, true)
	cloudURL := startStampingEngine(t, 22, false)
	localGC := closeIdleOnCleanup(t, NewGraphClientForURL(localURL))

	store := newFakeAuthStore() // empty → not logged in
	as := auth.NewAuthState(store, time.Millisecond)
	r := NewRouter(localGC, cloudURL, staticTokenSource{tok: "tok"}, as)
	ctx := opCtx()

	// Nothing observed yet: the forwarder reads 0 before any traffic.
	assert.Equal(t, uint64(0), r.FreshnessGen(ctx), "no response observed yet reads as no watermark")

	// Not logged in → the call routes local, so the local backend's watermark
	// is what the forwarder reports.
	_, err := r.Execute(ctx, &knowledgev1.ExecuteRequest{})
	require.NoError(t, err)
	assert.Equal(t, uint64(11), r.FreshnessGen(ctx), "must report the local backend's observed watermark")

	// User runs `knowledge login`; wait past the AuthState TTL so the next pick
	// re-reads. The routed backend changes, and so must the reported watermark.
	require.NoError(t, store.Set(ctx, auth.KeyRefreshToken, "frt-fresh"))
	time.Sleep(50 * time.Millisecond)

	_, err = r.Execute(ctx, &knowledgev1.ExecuteRequest{})
	require.NoError(t, err)
	assert.Equal(t, uint64(22), r.FreshnessGen(ctx), "after the login flip it must report the CLOUD backend's watermark")
	assert.Equal(t, uint64(11), localGC.FreshnessGen(),
		"the local client keeps its own value — the two backends' counters are per-account and must not share a cell")

	// No local server and no auth: pick returns ErrNoBackend, which the wire
	// contract's "no watermark" reads as 0.
	rNone := NewRouter(nil, "http://cloud.invalid", staticTokenSource{}, auth.NewAuthState(newFakeAuthStore(), time.Hour))
	_, pickErr := rNone.Backend(ctx)
	require.ErrorIs(t, pickErr, ErrNoBackend, "the fixture must actually be in the no-backend state")
	assert.Equal(t, uint64(0), rNone.FreshnessGen(ctx), "ErrNoBackend reads as no watermark")
}

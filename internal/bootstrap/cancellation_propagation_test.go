// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// cancelProbeEngine is an EngineService handler whose Execute arm reports
// whether the caller's cancellation reached the in-flight RPC. It announces
// arrival on `entered` (so the test can cancel only once the RPC is actually in
// flight), races ctx.Done() against a 2-second bound recording sawCancel only on
// the ctx.Done() arm, then announces completion on `done`. The bound is what
// keeps a run against a non-propagating seam BOUNDED instead of hanging.
//
// `done` is load-bearing, not decoration: a cancelled connect RPC returns to the
// CLIENT as soon as the stream aborts, without waiting for the server handler to
// unwind. Reading sawCancel straight after the client call therefore races the
// handler's own observation of ctx.Done() — the flag's writer. The test waits on
// `done` so the read happens after the write, making the outcome deterministic
// rather than load-dependent.
//
// Local to this file on purpose: countingEngine (router_e2e_test.go:81) is
// shared by the e2e suites and must not gain blocking behavior.
type cancelProbeEngine struct {
	enteredOnce sync.Once
	entered     chan struct{}
	doneOnce    sync.Once
	done        chan struct{}
	sawCancel   atomic.Bool
}

func (e *cancelProbeEngine) Execute(
	ctx context.Context,
	_ *connect.Request[knowledgev1.ExecuteRequest],
) (*connect.Response[knowledgev1.ExecuteResponse], error) {
	e.enteredOnce.Do(func() { close(e.entered) })
	defer e.doneOnce.Do(func() { close(e.done) })
	select {
	case <-ctx.Done():
		e.sawCancel.Store(true)
	case <-time.After(2 * time.Second):
	}
	return connect.NewResponse(&knowledgev1.ExecuteResponse{}), nil
}

func (e *cancelProbeEngine) Stats(
	context.Context, *connect.Request[knowledgev1.StatsRequest],
) (*connect.Response[knowledgev1.StatsResponse], error) {
	return connect.NewResponse(&knowledgev1.StatsResponse{}), nil
}

func (e *cancelProbeEngine) MetadataStats(
	context.Context, *connect.Request[knowledgev1.MetadataStatsRequest],
) (*connect.Response[knowledgev1.MetadataStatsResponse], error) {
	return connect.NewResponse(&knowledgev1.MetadataStatsResponse{}), nil
}

func (e *cancelProbeEngine) Index(
	context.Context, *connect.Request[knowledgev1.IndexRequest],
) (*connect.Response[knowledgev1.IndexResponse], error) {
	return connect.NewResponse(&knowledgev1.IndexResponse{}), nil
}

func (e *cancelProbeEngine) PipelineScan(
	context.Context, *connect.Request[knowledgev1.PipelineScanRequest],
) (*connect.Response[knowledgev1.PipelineScanResponse], error) {
	return connect.NewResponse(&knowledgev1.PipelineScanResponse{}), nil
}

func (e *cancelProbeEngine) PipelineGenPoll(
	context.Context, *connect.Request[knowledgev1.PipelineGenPollRequest],
) (*connect.Response[knowledgev1.PipelineGenPollResponse], error) {
	return connect.NewResponse(&knowledgev1.PipelineGenPollResponse{}), nil
}

func (e *cancelProbeEngine) CorpusDelta(
	context.Context, *connect.Request[knowledgev1.CorpusDeltaRequest],
) (*connect.Response[knowledgev1.CorpusDeltaResponse], error) {
	return connect.NewResponse(&knowledgev1.CorpusDeltaResponse{}), nil
}

func (e *cancelProbeEngine) ExportGraph(
	context.Context, *connect.Request[knowledgev1.ExportGraphRequest],
) (*connect.Response[knowledgev1.ExportGraphResponse], error) {
	return connect.NewResponse(&knowledgev1.ExportGraphResponse{}), nil
}

func (e *cancelProbeEngine) OverwriteGraph(
	context.Context, *connect.Request[knowledgev1.OverwriteGraphRequest],
) (*connect.Response[knowledgev1.OverwriteGraphResponse], error) {
	return connect.NewResponse(&knowledgev1.OverwriteGraphResponse{}), nil
}

// startCancelProbeEngine stands up an h2c httptest.Server in front of a
// cancelProbeEngine, mirroring startCountingEngine (router_e2e_test.go:156).
func startCancelProbeEngine(t *testing.T) (string, *cancelProbeEngine) {
	t.Helper()
	eng := &cancelProbeEngine{entered: make(chan struct{}), done: make(chan struct{})}
	mux := http.NewServeMux()
	path, hdlr := knowledgev1connect.NewEngineServiceHandler(eng)
	mux.Handle(path, hdlr)
	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	return srv.URL, eng
}

// TestToolCallCancellationStopsWork proves an LLM client canceling a tool call
// actually stops the work that call started: the context the dispatcher hands
// runInterceptChain must be the one the intercept issues its RPC on, so a
// cancel lands on the in-flight request rather than on a context nothing reads.
//
// Route, traced in current source: InterceptMutate (intercept_mutate.go:166)
// claims tool==mutate, needs a non-nil GraphCaller (the Router is wired here),
// and for graph=="" + operation=="update" reaches handleInterceptMutateUpdate
// (intercept_mutate.go:265). With a single non-empty id its first unconditional
// action is lookupNodeBackend (intercept_mutate.go:293), which issues Execute
// over the graph caller. That Execute is the blocking seam this test probes.
//
// Cancellation is fired only AFTER the engine reports the RPC in flight, so the
// test measures propagation into live work rather than a pre-cancelled context
// short-circuiting before dispatch.
func TestToolCallCancellationStopsWork(t *testing.T) {
	srvURL, eng := startCancelProbeEngine(t)
	local := graphclient.NewGraphClientForURL(srvURL)
	t.Cleanup(local.Close)
	// Empty auth store → logged out → the Router selects the local engine.
	c := closeRouterOnCleanup(t, buildE2EClient(local, "http://cloud.invalid", newFakeAuthStore(), 0))

	ctx, cancel := context.WithCancel(opCtx())
	defer cancel()
	go func() {
		<-eng.entered
		cancel()
	}()

	c.runInterceptChain(ctx, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","id":"ful1014-cancel-probe","status":"completed"}`),
	})

	// Precondition FIRST: a probe that never reached the engine must fail as a
	// broken probe, not as a false red about cancellation.
	select {
	case <-eng.entered:
	default:
		t.Fatal("mutate(update,single-id) did not reach GraphCaller.Execute — the probe is broken, not the property")
	}

	// Then wait for the handler to unwind, so sawCancel is read after its writer
	// ran. The client call above returns the moment the stream aborts, which is
	// strictly before the server observes the cancellation. The bound is generous
	// (the handler's own bound is 2s) and exists only so a wedged handler fails
	// loudly instead of hanging the suite.
	select {
	case <-eng.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the probe engine's Execute never returned — cannot read sawCancel")
	}

	require.True(t, eng.sawCancel.Load(),
		"entered=true sawCancel=false: the intercept issued its RPC on a context the caller's cancel could not reach")
}

// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// router_test_helpers_test.go holds the shared EngineService test double + the
// httptest harness the router routing tests stand up. Split out of
// router_test.go to keep that file under the file-length cap.

// countingEngine is an EngineService handler that counts hits per RPC kind
// and replies with a minimal, valid response. Each test fixture stands up
// two of these (local + cloud) so we can assert which backend serviced a
// given call.
type countingEngine struct {
	execute         atomic.Int32
	index           atomic.Int32
	metadataStats   atomic.Int32
	exportGraph     atomic.Int32
	stats           atomic.Int32
	pipelineScan    atomic.Int32
	pipelineGenPoll atomic.Int32
	// graphNames is the catalog this leg answers a RETURN_MODE_GRAPH_NAMES read
	// with. Nil — the zero value — keeps the empty response every pre-existing
	// fixture was written against, so only a test that SETS one sees a catalog.
	// Stored atomically because the leg serves on the httptest server's own
	// goroutines while the test holds the pointer.
	graphNames atomic.Pointer[[]*knowledgev1.GraphInfo]
}

// serveGraphNames makes this leg answer every Execute with the given catalog.
func (e *countingEngine) serveGraphNames(infos []*knowledgev1.GraphInfo) {
	e.graphNames.Store(&infos)
}

func (e *countingEngine) Execute(
	_ context.Context,
	_ *connect.Request[knowledgev1.ExecuteRequest],
) (*connect.Response[knowledgev1.ExecuteResponse], error) {
	e.execute.Add(1)
	resp := &knowledgev1.ExecuteResponse{}
	if gn := e.graphNames.Load(); gn != nil {
		resp.GraphNames = *gn
	}
	return connect.NewResponse(resp), nil
}

func (e *countingEngine) Stats(
	_ context.Context,
	_ *connect.Request[knowledgev1.StatsRequest],
) (*connect.Response[knowledgev1.StatsResponse], error) {
	e.stats.Add(1)
	return connect.NewResponse(&knowledgev1.StatsResponse{}), nil
}

func (e *countingEngine) MetadataStats(
	_ context.Context,
	_ *connect.Request[knowledgev1.MetadataStatsRequest],
) (*connect.Response[knowledgev1.MetadataStatsResponse], error) {
	e.metadataStats.Add(1)
	return connect.NewResponse(&knowledgev1.MetadataStatsResponse{}), nil
}

func (e *countingEngine) Index(
	_ context.Context,
	_ *connect.Request[knowledgev1.IndexRequest],
) (*connect.Response[knowledgev1.IndexResponse], error) {
	e.index.Add(1)
	return connect.NewResponse(&knowledgev1.IndexResponse{}), nil
}

func (e *countingEngine) PipelineScan(
	_ context.Context,
	_ *connect.Request[knowledgev1.PipelineScanRequest],
) (*connect.Response[knowledgev1.PipelineScanResponse], error) {
	e.pipelineScan.Add(1)
	return connect.NewResponse(&knowledgev1.PipelineScanResponse{}), nil
}

func (e *countingEngine) PipelineGenPoll(
	_ context.Context,
	_ *connect.Request[knowledgev1.PipelineGenPollRequest],
) (*connect.Response[knowledgev1.PipelineGenPollResponse], error) {
	e.pipelineGenPoll.Add(1)
	return connect.NewResponse(&knowledgev1.PipelineGenPollResponse{}), nil
}

func (e *countingEngine) CorpusDelta(
	_ context.Context,
	_ *connect.Request[knowledgev1.CorpusDeltaRequest],
) (*connect.Response[knowledgev1.CorpusDeltaResponse], error) {
	return connect.NewResponse(&knowledgev1.CorpusDeltaResponse{}), nil
}

func (e *countingEngine) ExportGraph(_ context.Context, _ *connect.Request[knowledgev1.ExportGraphRequest]) (*connect.Response[knowledgev1.ExportGraphResponse], error) {
	e.exportGraph.Add(1)
	return connect.NewResponse(&knowledgev1.ExportGraphResponse{}), nil
}

func (e *countingEngine) OverwriteGraph(_ context.Context, _ *connect.Request[knowledgev1.OverwriteGraphRequest]) (*connect.Response[knowledgev1.OverwriteGraphResponse], error) {
	return connect.NewResponse(&knowledgev1.OverwriteGraphResponse{}), nil
}

// startCountingEngine stands up an h2c httptest.Server in front of a
// countingEngine handler. Returns the server URL and the engine pointer
// so tests can read hit counters.
func startCountingEngine(t *testing.T) (string, *countingEngine) {
	t.Helper()
	eng := &countingEngine{}
	mux := http.NewServeMux()
	path, hdlr := knowledgev1connect.NewEngineServiceHandler(eng)
	mux.Handle(path, hdlr)
	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	return srv.URL, eng
}

// startHealthyCountingEngine stands up a countingEngine that ALSO serves
// HealthService. BindSearch probes each destination's health before it binds
// that destination, and startCountingEngine above mounts EngineService alone,
// so a leg backed by that one fails the preflight however well it answers
// Execute.
//
// The returned func takes the server off the air mid-test, which is how a test
// models a local knowledge-server that is simply not running. It is registered
// as a cleanup as well, and is safe to call more than once.
func startHealthyCountingEngine(t *testing.T) (string, *countingEngine, func()) {
	t.Helper()
	return startCountingEngineWithHealth(t, &healthyToolHandler{})
}

// unauthenticatedHealth refuses every health check with CodeUnauthenticated,
// the way a cloud endpoint answers a client whose credentials it rejects. A
// refused credential is NOT a store that is merely off the air, and the two
// must never be confused: an unreachable OPTIONAL store is skipped, while an
// auth failure is fatal.
type unauthenticatedHealth struct{}

func (h *unauthenticatedHealth) Check(
	_ context.Context, _ *connect.Request[knowledgev1.CheckRequest],
) (*connect.Response[knowledgev1.CheckResponse], error) {
	return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("credentials rejected"))
}

func (h *unauthenticatedHealth) Status(
	_ context.Context, _ *connect.Request[knowledgev1.StatusRequest],
) (*connect.Response[knowledgev1.StatusResponse], error) {
	return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("credentials rejected"))
}

// startUnauthenticatedCountingEngine stands up an engine whose HealthService
// rejects the caller's credentials while EngineService still answers — what a
// reachable but unauthenticated cloud leg looks like to the preflight.
func startUnauthenticatedCountingEngine(t *testing.T) (string, *countingEngine, func()) {
	t.Helper()
	return startCountingEngineWithHealth(t, &unauthenticatedHealth{})
}

// startCountingEngineWithHealth is the shared body of the two starters above:
// one countingEngine, the caller's HealthService handler, one h2c server.
func startCountingEngineWithHealth(
	t *testing.T, health knowledgev1connect.HealthServiceHandler,
) (string, *countingEngine, func()) {
	t.Helper()
	eng := &countingEngine{}
	mux := http.NewServeMux()
	enginePath, engineHandler := knowledgev1connect.NewEngineServiceHandler(eng)
	mux.Handle(enginePath, engineHandler)
	healthPath, healthHandler := knowledgev1connect.NewHealthServiceHandler(health)
	mux.Handle(healthPath, healthHandler)
	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	var once sync.Once
	stop := func() {
		once.Do(func() {
			srv.CloseClientConnections()
			srv.Close()
		})
	}
	t.Cleanup(stop)
	return srv.URL, eng, stop
}

// accountRoutedEngine is ONE cloud endpoint that counts Execute per STAMPED
// account and can refuse a named account the way the gateway does.
//
// It is one server rather than two because the Router holds ONE cloud URL
// (NewRouterWithMachineAuth) and cloudForDestination builds every per-account
// client against it: "two counting cloud engines keyed by account" is therefore
// one h2c server reading the inbound auth.AccountHeaderName, which is also what
// the gateway itself keys on. The unstamped case counts under "" so a call that
// sent no header is distinguishable from one that sent an account.
type accountRoutedEngine struct {
	next http.Handler

	mu        sync.Mutex
	executes  map[string]int
	stamped   []string
	forbidden map[string]bool
}

// forbid makes the endpoint answer every request stamped with id the way the
// gateway answers a non-member: 403 account_forbidden, with the body the
// classifier reads.
func (a *accountRoutedEngine) forbid(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.forbidden[id] = true
}

// executesFor reports how many Execute calls arrived stamped with id.
func (a *accountRoutedEngine) executesFor(id string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.executes[id]
}

// stampedAccounts reports, in order, the account header of every request that
// reached the endpoint — the observable that distinguishes "refused" from
// "refused, then retried as somebody else".
func (a *accountRoutedEngine) stampedAccounts() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.stamped)
}

func (a *accountRoutedEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := r.Header.Get(auth.AccountHeaderName)
	a.mu.Lock()
	a.stamped = append(a.stamped, id)
	refuse := a.forbidden[id]
	if !refuse && strings.HasSuffix(r.URL.Path, "/Execute") {
		a.executes[id]++
	}
	a.mu.Unlock()
	if refuse {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"account_forbidden","error_description":"caller is not a member of the requested account"}`))
		return
	}
	a.next.ServeHTTP(w, r)
}

// startAccountRoutedEngine stands up the account-routed endpoint in front of a
// real EngineService + HealthService pair, so a bound search reaches its health
// preflight on the same account the Execute is stamped with.
func startAccountRoutedEngine(t *testing.T) (string, *accountRoutedEngine) {
	t.Helper()
	mux := http.NewServeMux()
	enginePath, engineHandler := knowledgev1connect.NewEngineServiceHandler(&countingEngine{})
	mux.Handle(enginePath, engineHandler)
	healthPath, healthHandler := knowledgev1connect.NewHealthServiceHandler(&healthyToolHandler{})
	mux.Handle(healthPath, healthHandler)
	routed := &accountRoutedEngine{
		next:      mux,
		executes:  map[string]int{},
		forbidden: map[string]bool{},
	}
	srv := httptest.NewServer(h2c.NewHandler(routed, &http2.Server{}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	return srv.URL, routed
}

// selectCloudAccountForTest points the process-wide account selection at a
// config file under the test's own temp dir, so a signed-in fixture never reads
// the developer's real selection and never writes one.
func selectCloudAccountForTest(t *testing.T, id string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := config.WriteSelectedAccountID(path, id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(auth.SetSelectedAccountForTest(auth.NewAccountSelection(path, 0)))
}

// graphNamesQuery is the cheapest ExecuteRequest that reaches a backend: the
// search fan-out counts one Execute per bound destination, which is how a test
// reads WHICH stores a bound search actually consulted.
func graphNamesQuery() *knowledgev1.ExecuteRequest {
	return &knowledgev1.ExecuteRequest{
		Target: &knowledgev1.GraphSelector{Graph: "knowledge"},
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES,
		}},
	}
}

// staticTokenSource is a non-refreshing token source for tests that need
// AuthState=true but do not exercise the 401 retry path.
type staticTokenSource struct{ tok string }

func (s staticTokenSource) Token(_ context.Context) (string, auth.PermissionSet, error) {
	return s.tok, nil, nil
}

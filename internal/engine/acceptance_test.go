// SPDX-License-Identifier: Apache-2.0

package engine

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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// cloudEngineBackend is a real EngineService handler standing in for the cloud
// EngineService.Execute backend. It counts Execute hits (for the non-degrade
// assertion) and returns a canned search response so the engine.Render search
// path produces non-empty output. The other RPCs return Unimplemented — this
// acceptance/deny phase exercises only Execute.
//
// This mirrors startCountingEngine + countingEngine (graphclient/router_test.go:69,
// :129) and stubEngine (graphclient/client_execute_test.go:29). Those helpers
// are _test.go-local to package graphclient and therefore not importable from an
// engine test, so the backend is re-declared here. It is a deliberate mirror
// with a cited source, justified by Go's rule that _test.go symbols are not
// exported across packages — not a snowflake.
type cloudEngineBackend struct {
	execute atomic.Int32
	resp    *knowledgev1.ExecuteResponse
}

func (e *cloudEngineBackend) Execute(
	_ context.Context,
	_ *connect.Request[knowledgev1.ExecuteRequest],
) (*connect.Response[knowledgev1.ExecuteResponse], error) {
	e.execute.Add(1)
	return connect.NewResponse(e.resp), nil
}

func (e *cloudEngineBackend) Stats(
	context.Context, *connect.Request[knowledgev1.StatsRequest],
) (*connect.Response[knowledgev1.StatsResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (e *cloudEngineBackend) MetadataStats(
	context.Context, *connect.Request[knowledgev1.MetadataStatsRequest],
) (*connect.Response[knowledgev1.MetadataStatsResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (e *cloudEngineBackend) Index(
	context.Context, *connect.Request[knowledgev1.IndexRequest],
) (*connect.Response[knowledgev1.IndexResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (e *cloudEngineBackend) PipelineScan(
	context.Context, *connect.Request[knowledgev1.PipelineScanRequest],
) (*connect.Response[knowledgev1.PipelineScanResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (e *cloudEngineBackend) ExportGraph(
	context.Context, *connect.Request[knowledgev1.ExportGraphRequest],
) (*connect.Response[knowledgev1.ExportGraphResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (e *cloudEngineBackend) OverwriteGraph(
	context.Context, *connect.Request[knowledgev1.OverwriteGraphRequest],
) (*connect.Response[knowledgev1.OverwriteGraphResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

// newCloudEngineBackend stands up an h2c httptest.Server in front of a
// cloudEngineBackend handler and returns the server URL plus the handler so the
// test can read the Execute counter. The canned Execute response carries a
// SearchResults entry (not a Nodes-only payload): the search render path
// (renderSearchTool → renderSearchResponseFiltered) reads SearchResults, so a
// Nodes-only response would render empty and make the acceptance assertion
// vacuous. Mirrors dispatch_test.go:63's SearchResponseWith fixture.
//
// h2c (not the Phase-1 cleartext-h1.1 front door) is correct here: this phase
// asserts routing/acceptance/deny, NOT the HTTP/1.1-determinism Phase 1 owns.
func newCloudEngineBackend(t *testing.T) (string, *cloudEngineBackend) {
	t.Helper()
	results := []SearchResult{
		{Score: 0.9, Node: &knowledgev1.Node{Id: "n1", Type: "finding", SymbolName: "Hit"}},
	}
	backend := &cloudEngineBackend{
		resp: enginetest.SearchResponseWith(searchResultsToProtoForTest(results)...),
	}
	mux := http.NewServeMux()
	path, hdlr := knowledgev1connect.NewEngineServiceHandler(backend)
	mux.Handle(path, hdlr)
	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(srv.Close)
	return srv.URL, backend
}

// acceptanceAuthStore is a tiny in-memory auth.Store the conjunction test seeds
// logged-in. The graphclient fakeAuthStore (router_test.go:29) is _test.go-local
// to package graphclient and not importable here, so this is a mirror (Get/Set/
// Delete over a guarded map), not an import.
type acceptanceAuthStore struct {
	mu   sync.Mutex
	data map[string]string
}

func (s *acceptanceAuthStore) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[key]
	if !ok {
		return "", auth.ErrNotFound
	}
	return v, nil
}

func (s *acceptanceAuthStore) Set(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	return nil
}

func (s *acceptanceAuthStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[key]; !ok {
		return auth.ErrNotFound
	}
	delete(s.data, key)
	return nil
}

// acceptanceTokenSource is a non-refreshing token source mirroring
// graphclient/router_test.go:142 staticTokenSource (also _test.go-local and not
// importable here).
type acceptanceTokenSource struct{ tok string }

func (s acceptanceTokenSource) Token(_ context.Context) (string, auth.PermissionSet, error) {
	return s.tok, nil, nil
}

// TestCloudAcceptanceAndDenyConjunction proves BOTH halves of the
// cloud-mode-no-local contract in ONE test body (conjunction, not disjunction),
// driving the EXACT production chokepoint bootstrap/mcp.go:199 uses
// (engine.Dispatch(ctx, router.Execute, ...)) against a REAL EngineService wire
// backend through a graphclient.Router with a nil local server and auth=true
// (the cloud-route shape of TestRouter_NoLocal_Auth_RoutesCloud@router_test.go:198):
//
//   - HALF (a) ACCEPTANCE: a reducible op (search) routes to the cloud backend,
//     SUCCEEDS (IsError==false), renders the canned hit, and advances the backend
//     Execute counter by exactly 1.
//   - HALF (b) DENY: query{mode:stats} is EXPLICITLY DENIED via the deny-flip
//     (dispatch.go:93-95) — IsError==true, message names the tool and is legible
//     ("query" + "denied") — and the backend Execute counter does NOT advance
//     (the Compile-miss short-circuits BEFORE exec), proving the op is denied,
//     NOT silently degraded to the backend.
//
// This is the wire-level conjunction counterpart of the closure-stub
// TestDispatch_DenyOnOkFalse (dispatch_test.go:43): that test asserts the deny
// against an ExecuteFn closure; this one asserts the SAME deny AND a real cloud
// acceptance through the production Router.Execute path, with backend-call-count
// deltas proving non-degradation.
func TestCloudAcceptanceAndDenyConjunction(t *testing.T) {
	backendURL, backend := newCloudEngineBackend(t)

	store := &acceptanceAuthStore{data: map[string]string{}}
	require.NoError(t, store.Set(context.Background(), auth.KeyRefreshToken, "frt-stub"),
		"seed the store logged-in so the Router routes to cloud")
	authState := auth.NewAuthState(store, time.Hour)

	// nil local server + auth=true → every Execute routes to the cloud backend.
	r := graphclient.NewRouter(nil, backendURL, acceptanceTokenSource{tok: "t"}, authState)

	ctx := context.Background()

	// HALF (a) ACCEPTANCE: a reducible search SUCCEEDS against the cloud backend.
	// Dispatch is unqualified because this test lives IN package engine; the
	// production chokepoint (bootstrap/mcp.go:199) calls it as engine.Dispatch
	// against this same function through Router.Execute.
	out, err := Dispatch(ctx, r.Execute, "search",
		json.RawMessage(`{"query":"x","graph":"knowledge"}`))
	require.NoError(t, err, "a successful Execute is rendered, not returned as a Go error")
	assert.False(t, out.IsError, "the cloud-routed search must succeed (no local server)")
	require.NotEmpty(t, out.Content, "the search render must be non-empty")
	assert.Contains(t, out.Content[0].Text, "[finding] Hit",
		"the render reflects the canned cloud backend search result")
	assert.Equal(t, int32(1), backend.execute.Load(),
		"the acceptance op routed to the cloud backend EXACTLY once")

	// HALF (b) DENY: query{mode:stats} is EXPLICITLY denied, not degraded.
	denyOut, denyErr := Dispatch(ctx, r.Execute, "query",
		json.RawMessage(`{"mode":"stats"}`))
	require.NoError(t, denyErr, "a deny is RENDERED as an error result, not returned as a Go error")
	assert.True(t, denyOut.IsError, "query{mode:stats} is an explicit deny (IsError)")
	require.NotEmpty(t, denyOut.Content)
	assert.Contains(t, denyOut.Content[0].Text, "query", "the deny message names the offending tool")
	assert.Contains(t, denyOut.Content[0].Text, "denied", "the deny message is legible")
	assert.Equal(t, int32(1), backend.execute.Load(),
		"the deny did NOT advance the backend counter — denied, not silently degraded to the backend")
}

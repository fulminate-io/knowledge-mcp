// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// healthyToolHandler serves Health (so EnsureServer passes) for the bare-forwarder
// routing test. The ToolService was deleted — tool calls route exclusively
// through the injected Dispatch, so the harness only needs a live Health surface.
type healthyToolHandler struct{}

func (h *healthyToolHandler) Check(
	_ context.Context, _ *connect.Request[knowledgev1.CheckRequest],
) (*connect.Response[knowledgev1.CheckResponse], error) {
	return connect.NewResponse(&knowledgev1.CheckResponse{}), nil
}

func (h *healthyToolHandler) Status(
	_ context.Context, _ *connect.Request[knowledgev1.StatusRequest],
) (*connect.Response[knowledgev1.StatusResponse], error) {
	return connect.NewResponse(&knowledgev1.StatusResponse{}), nil
}

func newForwarderHarness(t *testing.T) *GraphClient {
	t.Helper()
	h := &healthyToolHandler{}
	mux := http.NewServeMux()
	hp, hh := knowledgev1connect.NewHealthServiceHandler(h)
	mux.Handle(hp, hh)

	h2s := &http2.Server{}
	srv := httptest.NewServer(h2c.NewHandler(mux, h2s))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	return closeIdleOnCleanup(t, NewGraphClientForURL(srv.URL))
}

func toolCallReq(t *testing.T, name string, args map[string]any) kgtools.JSONRPCRequest {
	t.Helper()
	rawArgs, err := json.Marshal(args)
	require.NoError(t, err)
	params, err := json.Marshal(kgtools.CallToolParams{Name: name, Arguments: rawArgs})
	require.NoError(t, err)
	return kgtools.JSONRPCRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Params: params}
}

// TestHandleMCPToolCall_RoutesThroughDispatch asserts the bare forwarder routes
// every tool call through the injected Dispatch (the only tool-call path now).
func TestHandleMCPToolCall_RoutesThroughDispatch(t *testing.T) {
	gc := newForwarderHarness(t)

	var dispatchHits atomic.Int64
	m := NewMCPClient(MCPClientConfig{
		Client: gc,
		Dispatch: func(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
			dispatchHits.Add(1)
			return kgtools.TextResult("dispatched output"), nil
		},
	})

	resp := m.handleMCPToolCall(toolCallReq(t, "query", map[string]any{"id": "n1"}))
	result, ok := resp.Result.(kgtools.ToolResult)
	require.True(t, ok)
	assert.Equal(t, "dispatched output", result.Content[0].Text)
	assert.Equal(t, int64(1), dispatchHits.Load(), "Dispatch invoked")
}

// newUnhealthyLocalClient returns a *GraphClient pointed at a server that is
// already closed, so Healthy() (and therefore EnsureServer) fails. Models a
// logged-in cloud user who has no local knowledge-server running.
func newUnhealthyLocalClient(t *testing.T) *GraphClient {
	t.Helper()
	h2s := &http2.Server{}
	srv := httptest.NewServer(h2c.NewHandler(http.NewServeMux(), h2s))
	url := srv.URL
	srv.Close() // close immediately so the port is dead and Healthy()=false.
	return closeIdleOnCleanup(t, NewGraphClientForURL(url))
}

// TestHandleMCPToolCall_LoggedIn_SkipsEnsureServer proves a logged-in user
// dispatches every fall-through op WITHOUT a healthy local server: the
// EnsureServer gate is skipped when cfg.LoggedIn(ctx) reports true. Table-driven
// over representative members of the engine-reducible / cloud-routed op set so
// the named set is exercised, not one generic call.
func TestHandleMCPToolCall_LoggedIn_SkipsEnsureServer(t *testing.T) {
	ops := []struct {
		name string
		args map[string]any
	}{
		{"traverse", map[string]any{"start": "n1", "graph": "knowledge"}},
		{"query", map[string]any{"id": "n1"}},
		{"mutate", map[string]any{"operation": "link", "from": "k1", "to": "k2", "relationship": "relates-to"}},
		{"mutate", map[string]any{"operation": "create_batch", "nodes": []any{}}},
	}
	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			gc := newUnhealthyLocalClient(t)
			var dispatchHits atomic.Int64
			m := NewMCPClient(MCPClientConfig{
				Client:   gc,
				LoggedIn: func(context.Context) bool { return true },
				Dispatch: func(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
					dispatchHits.Add(1)
					return kgtools.TextResult("dispatched output"), nil
				},
			})

			resp := m.handleMCPToolCall(toolCallReq(t, op.name, op.args))
			result, ok := resp.Result.(kgtools.ToolResult)
			require.True(t, ok)
			assert.False(t, result.IsError,
				"logged-in dispatch must not surface the EnsureServer error")
			assert.Equal(t, "dispatched output", result.Content[0].Text)
			assert.Equal(t, int64(1), dispatchHits.Load(),
				"Dispatch invoked — EnsureServer gate was skipped for logged-in user")
		})
	}
}

// TestHandleMCPToolCall_LoggedOut_GatesLocal is the sibling: a logged-out user
// (LoggedIn nil) with an unhealthy local hits the EnsureServer gate and never
// reaches Dispatch.
func TestHandleMCPToolCall_LoggedOut_GatesLocal(t *testing.T) {
	gc := newUnhealthyLocalClient(t)
	var dispatchHits atomic.Int64
	m := NewMCPClient(MCPClientConfig{
		Client:   gc,
		LoggedIn: nil, // nil always gates (logged-out / test-fixture default).
		Dispatch: func(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
			dispatchHits.Add(1)
			return kgtools.TextResult("dispatched output"), nil
		},
	})

	resp := m.handleMCPToolCall(toolCallReq(t, "query", map[string]any{"id": "n1"}))
	result, ok := resp.Result.(kgtools.ToolResult)
	require.True(t, ok)
	assert.True(t, result.IsError, "logged-out unhealthy local gates on EnsureServer")
	assert.Contains(t, result.Content[0].Text, "not reachable",
		"surfaces the EnsureServer 'not reachable' error")
	assert.Equal(t, int64(0), dispatchHits.Load(), "Dispatch NOT hit when the gate fails")
}

// TestHandleMCPToolCall_NilDispatchErrors asserts the contract: with no
// Dispatch wired there is no legacy Client.Call to fall through to (the ToolService
// wire is deleted), so the forwarder surfaces a wiring error rather than silently
// dropping the call.
func TestHandleMCPToolCall_NilDispatchErrors(t *testing.T) {
	gc := newForwarderHarness(t)

	m := NewMCPClient(MCPClientConfig{Client: gc}) // Dispatch nil.

	resp := m.handleMCPToolCall(toolCallReq(t, "query", map[string]any{"mode": "stats"}))
	result, ok := resp.Result.(kgtools.ToolResult)
	require.True(t, ok)
	assert.True(t, result.IsError, "nil Dispatch surfaces an error")
	assert.Contains(t, result.Content[0].Text, "Dispatch",
		"error names the missing Dispatch wiring")
}

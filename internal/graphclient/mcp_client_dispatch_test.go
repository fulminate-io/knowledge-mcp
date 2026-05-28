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
// routing test. T-GTB4 deleted the ToolService — tool calls route exclusively
// through the injected Dispatch, so the harness only needs a live Health surface.
type healthyToolHandler struct{}

func (h *healthyToolHandler) Check(
	_ context.Context, _ *connect.Request[knowledgev1.HealthCheckRequest],
) (*connect.Response[knowledgev1.HealthCheckResponse], error) {
	return connect.NewResponse(&knowledgev1.HealthCheckResponse{}), nil
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
	t.Cleanup(srv.Close)
	return NewGraphClientForURL(srv.URL)
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

// TestHandleMCPToolCall_NilDispatchErrors asserts the T-GTB4 contract: with no
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

// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// mcpToolJSON is the wire shape MCP hosts expect under tools/list result.
// Matches the camelCase JSON tag the MCP spec requires for inputSchema.
type mcpToolJSON struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// maybeSpawnLocalServer proactively reaches/spawns the LOCAL knowledge-server,
// but ONLY when the active backend is local (logged-out). A logged-in client
// never needs a local server: its ops route to cloud via Dispatch, so the spawn
// is skipped for them. When local, the proactive spawn avoids first-call latency
// for the worker-runtime wiring (which dials the server during ListWorkers) and
// tool calls. A spawn failure is surfaced via slog (stderr); tool calls retain
// EnsureServer's per-call dial fallback as a backstop for the logged-out path.
// Called from the `serve` daemon bootstrap (daemon.go).
func maybeSpawnLocalServer(c *client, f Config) {
	if c.router.LoggedIn(context.Background()) {
		return
	}
	if err := ensureServerReachable(f.Port, f.RootDir, f.GraphStorage, f.Pprof); err != nil {
		slog.Warn("knowledge-server not reachable and spawn failed; tool calls will return errors until the server is started", "error", err)
	}
}

// engineDispatch is the MCPClientConfig.Dispatch closure: it routes every
// post-intercept tool call through the compile-or-DENY engine dispatcher. The §A
// reducible shapes compile to Engine.Execute; an unrecognized shape is denied
// legibly — every LLM-facing tool either compiles here or is claimed by a client
// intercept upstream. Defined as a method (not an inline closure) so the
// daemon bootstrap (serve) stays import-clean of the higher-level
// cmd/knowledge/internal tool packages (the func-field injection mirrors
// InterceptChain).
func (c *client) engineDispatch(ctx context.Context, tool string, args json.RawMessage) (kgtools.ToolResult, error) {
	return engine.Dispatch(ctx, c.router.Execute, tool, args)
}

// handleToolsList answers a tools/list JSON-RPC request from the cached
// schema set. On first call it builds the client-owned tool catalog
// (tools.AllToolSchemas) locally — the client is the source of truth for its own
// tool surface. Subsequent calls serve from the in-process cache.
func (c *client) handleToolsList(req kgtools.JSONRPCRequest) *kgtools.JSONRPCResponse {
	schemas, err := c.loadSchemas(context.Background())
	if err != nil {
		return &kgtools.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &kgtools.RPCError{
				Code:    -32603, // JSON-RPC internal error
				Message: "schema handshake failed: " + err.Error() + " — server may still be starting; retry tools/list",
			},
		}
	}

	tools := make([]mcpToolJSON, len(schemas))
	for i, s := range schemas {
		tools[i] = mcpToolJSON(s)
	}
	return &kgtools.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]any{"tools": tools},
	}
}

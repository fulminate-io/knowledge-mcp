// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// MCPClientConfig holds all dependencies needed by the MCP stdio client.
type MCPClientConfig struct {
	// Client is the TCP client used for server liveness checks (EnsureServer →
	// Healthy). Tool calls route exclusively through Dispatch.
	Client *GraphClient

	// Version is the binary version string, injected at build time via ldflags.
	// Reported in the MCP initialize response as serverInfo.version.
	Version string

	// Port is the TCP port the graph server listens on.
	Port int

	// HandleToolsList handles a tools/list JSON-RPC request.
	// In MCP client mode this is served locally from static metadata.
	HandleToolsList func(req kgtools.JSONRPCRequest) *kgtools.JSONRPCResponse

	// InterceptChain runs the client-side intercept chain over every
	// incoming MCP tool call. The chain may either handle the call
	// inline (handled=true) or merely REWRITE params (e.g.
	// InjectRepoIfCodeGraph filling repo:+branch: on code-graph calls)
	// and fall through. The returned CallToolParams MUST be used by the
	// caller when proxying — discarding it strips rewrites and the
	// server sees the original (incomplete) args, defeating the
	// rewrite pass entirely.
	InterceptChain func(params kgtools.CallToolParams) (kgtools.CallToolParams, bool, kgtools.ToolResult)

	// Dispatch is the compile-or-DENY tool router injected by the client
	// bootstrap (the engine.Dispatch closure). It routes every post-intercept
	// tool call so the §A reducible shapes compile to Engine.Execute and every
	// other shape is denied legibly. It is the ONLY tool-call path; a nil Dispatch
	// is a wiring bug surfaced as an error. The func-field injection mirrors
	// InterceptChain, keeping graphclient import-clean of the higher-level cmd/knowledge/internal tool packages.
	Dispatch func(ctx context.Context, tool string, args json.RawMessage) (kgtools.ToolResult, error)
}

// MCPClient runs the MCP stdio loop, proxying tool calls to the graph server.
type MCPClient struct {
	cfg MCPClientConfig

	// In-flight tool call cancellation. Protected by mu.
	mu           sync.Mutex
	activeCancel context.CancelFunc
	activeReqID  string // JSON-encoded request ID of the in-flight call
}

// NewMCPClient creates an MCPClient with the given configuration.
func NewMCPClient(cfg MCPClientConfig) *MCPClient {
	return &MCPClient{cfg: cfg}
}

// EnsureServer probes the graph server for liveness. If the server is not
// reachable on the configured port, it returns a fail-fast error naming the
// binary + flag the human should run to bring the server up. There is no
// auto-start — the client does not launch child processes.
func (m *MCPClient) EnsureServer() error {
	if m.cfg.Client.Healthy() {
		return nil
	}
	return fmt.Errorf("knowledge-server not reachable on port %d (start it with `./bin/knowledge-server --port %d --root .`)", m.cfg.Port, m.cfg.Port)
}

// Run starts the MCP stdio loop, blocking until stdin is closed.
// Tool calls run in a goroutine so the stdin reader remains live for
// notifications/cancelled messages during long-running operations.
func (m *MCPClient) Run() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 10*1024*1024), 10*1024*1024)

	// Serialize writes to stdout — tool call goroutines and the main loop
	// may both need to write (progress notifications vs responses).
	var writeMu sync.Mutex
	writeLine := func(data []byte) {
		writeMu.Lock()
		fmt.Println(string(data))
		writeMu.Unlock()
	}

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		var req kgtools.JSONRPCRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			slog.Warn("invalid JSON-RPC request", "error", err)
			continue
		}

		// Handle cancellation notifications immediately — don't block.
		if req.Method == "notifications/cancelled" {
			m.handleCancellation(req)
			continue
		}

		// Tool calls run concurrently so we can receive cancellations.
		if req.Method == "tools/call" {
			// Run in a goroutine so the scanner loop stays live.
			go func(r kgtools.JSONRPCRequest) {
				resp := m.handleMCPToolCall(r)
				if resp == nil {
					return
				}
				out, err := json.Marshal(resp)
				if err != nil {
					slog.Error("failed to marshal JSON-RPC response", "error", err)
					return
				}
				writeLine(out)
			}(req)
			continue
		}

		// All other methods (initialize, tools/list, ping) are fast — handle inline.
		resp := m.handleMCPRequest(req)
		if resp == nil {
			continue
		}
		out, err := json.Marshal(resp)
		if err != nil {
			slog.Error("failed to marshal JSON-RPC response", "error", err)
			continue
		}
		writeLine(out)
	}
}

// handleCancellation processes a notifications/cancelled message by canceling
// the in-flight tool call if the request ID matches.
func (m *MCPClient) handleCancellation(req kgtools.JSONRPCRequest) {
	var params struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		slog.Warn("invalid cancellation params", "error", err)
		return
	}

	cancelID := string(params.RequestID)
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.activeCancel != nil && m.activeReqID == cancelID {
		slog.Info("canceling in-flight tool call", "requestId", cancelID)
		m.activeCancel()
		m.activeCancel = nil
		m.activeReqID = ""
	}
}

// handleMCPRequest routes a single JSON-RPC request and returns a response.
// Returns nil for notifications that require no response.
func (m *MCPClient) handleMCPRequest(req kgtools.JSONRPCRequest) *kgtools.JSONRPCResponse {
	switch req.Method {
	case "initialize":
		return m.handleInitialize(req)
	case "notifications/initialized":
		return nil
	case "tools/list":
		if m.cfg.HandleToolsList != nil {
			return m.cfg.HandleToolsList(req)
		}
		return &kgtools.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": []any{}}}
	case "tools/call":
		return m.handleMCPToolCall(req)
	case "ping":
		return &kgtools.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}
	default:
		return &kgtools.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &kgtools.RPCError{Code: -32601, Message: "method not found"}}
	}
}

// handleMCPToolCall handles a tools/call JSON-RPC request, proxying to the graph server.
func (m *MCPClient) handleMCPToolCall(req kgtools.JSONRPCRequest) *kgtools.JSONRPCResponse {
	var params kgtools.CallToolParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &kgtools.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &kgtools.RPCError{Code: -32602, Message: "invalid params"}}
	}

	// Run the client-side intercept chain. The chain may handle the call
	// inline OR rewrite params (e.g. repo:+branch: injection for code-graph
	// calls) and fall through. Capture the rewritten params so the proxy
	// below uses them — discarding them strips rewrites silently.
	if m.cfg.InterceptChain != nil {
		rewritten, intercepted, interceptResult := m.cfg.InterceptChain(params)
		if intercepted {
			return &kgtools.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: interceptResult}
		}
		params = rewritten
	}

	// License gating is per-tool (paid features only) and lives inside the
	// tools package on the server side. The MCP client proxies all calls
	// unconditionally; free tools always reach the server, paid tools get
	// a clean kgtools.ToolResult denial from the handler when no license is present.

	// Ensure server is running before proxying.
	slog.Info("mcpClient: ensuring server for tool", "tool", params.Name)
	if err := m.EnsureServer(); err != nil {
		slog.Error("mcpClient: ensureServer failed", "tool", params.Name, "error", err)
		return &kgtools.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: "Error: " + err.Error()}},
			IsError: true,
		}}
	}
	slog.Info("mcpClient: server ready, proxying call", "tool", params.Name)

	// Create a cancellable context so notifications/cancelled can stop this call.
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.activeCancel = cancel
	m.activeReqID = string(req.ID)
	m.mu.Unlock()

	start := time.Now()
	// Route through the injected compile-or-DENY dispatcher (the engine.Dispatch
	// closure compiles reducible shapes to Engine.Execute and denies the rest).
	// Dispatch is the ONLY tool-call path, so a nil Dispatch is a wiring bug,
	// surfaced as an error rather than silently dropped.
	var callResult kgtools.ToolResult
	var err error
	if m.cfg.Dispatch != nil {
		callResult, err = m.cfg.Dispatch(ctx, params.Name, params.Arguments)
	} else {
		err = fmt.Errorf("mcpClient: no Dispatch wired — the client bootstrap must inject the engine dispatcher")
	}

	// Clear active state.
	m.mu.Lock()
	m.activeCancel = nil
	m.activeReqID = ""
	m.mu.Unlock()
	cancel() // release resources

	slog.Info("mcpClient: call returned", "tool", params.Name, "duration", time.Since(start).Round(time.Millisecond), "error", err)
	if err != nil {
		return &kgtools.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: "Error: graph server unavailable: " + err.Error()}},
			IsError: true,
		}}
	}
	return &kgtools.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: callResult}
}

func (m *MCPClient) handleInitialize(req kgtools.JSONRPCRequest) *kgtools.JSONRPCResponse {
	return &kgtools.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "knowledge", "version": m.cfg.Version},
		},
	}
}

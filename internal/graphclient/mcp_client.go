// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// MCPClientConfig holds all dependencies needed by the MCP client backing the
// HTTP daemon.
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
	//
	// ctx carries the per-session workspace cwd (HTTP transport) so
	// InjectRepoIfCodeGraph can resolve code-graph calls against the
	// session's repo rather than the process-global --root; the
	// context.Background() fallback path (handleMCPToolCall) carries no
	// per-session cwd. Aligns with the Dispatch field, which already takes
	// ctx first.
	InterceptChain func(ctx context.Context, params kgtools.CallToolParams) (kgtools.CallToolParams, bool, kgtools.ToolResult)

	// Dispatch is the compile-or-DENY tool router injected by the client
	// bootstrap (the engine.Dispatch closure). It routes every post-intercept
	// tool call so the §A reducible shapes compile to Engine.Execute and every
	// other shape is denied legibly. It is the ONLY tool-call path; a nil Dispatch
	// is a wiring bug surfaced as an error. The func-field injection mirrors
	// InterceptChain, keeping graphclient import-clean of the higher-level cmd/knowledge/internal tool packages.
	Dispatch func(ctx context.Context, tool string, args json.RawMessage) (kgtools.ToolResult, error)

	// LoggedIn reports the live active-backend selection: true means a logged-in
	// user whose fall-through ops route to cloud via Dispatch, so the per-call
	// local-health gate (EnsureServer) is skipped — a logged-in client operates
	// with no local knowledge-server. Nil-tolerant: a nil LoggedIn always gates
	// (the local default), preserving behavior for logged-out users and
	// router-less test fixtures. Injected as a func field mirroring InterceptChain
	// and Dispatch, keeping graphclient import-clean of Router/auth.
	LoggedIn func(ctx context.Context) bool
}

// MCPClient routes MCP tool calls to the graph server. It backs the HTTP
// daemon (HTTPServer wraps an *MCPClient); the daemon threads a per-session
// ctx into handleMCPRequestCtx for every request.
type MCPClient struct {
	cfg MCPClientConfig

	// Process-level in-flight cancellation slot, used only when no per-session
	// *httpSession is stamped onto the dispatch ctx (the context.Background()
	// fallback path — see cancelSinkForContext). Protected by mu. The daemon's
	// real cancellation runs per-session: each HTTP session carries its OWN
	// cancel slot on its *httpSession (the cancelSink stamped onto the dispatch
	// ctx). Both satisfy the cancelSink seam so dispatchToolCall registers
	// uniformly.
	mu           sync.Mutex
	activeCancel context.CancelFunc
	activeReqID  string // JSON-encoded request ID of the in-flight call
}

// NewMCPClient creates an MCPClient with the given configuration.
func NewMCPClient(cfg MCPClientConfig) *MCPClient {
	return &MCPClient{cfg: cfg}
}

// cancelSinkForContext returns the cancel sink dispatchToolCall registers its
// in-flight cancellation against: the per-session *httpSession when the HTTP
// transport stamped one onto ctx, else the MCPClient itself (the
// context.Background() fallback, e.g. handleMCPToolCall).
func (m *MCPClient) cancelSinkForContext(ctx context.Context) cancelSink {
	if s, ok := cancelSinkFromContext(ctx); ok {
		return s
	}
	return m
}

// registerCancel records the in-flight tool call's cancel func + request ID on
// the MCPClient's process-level slot (the context.Background() fallback path).
// Implements the cancelSink seam, mirroring httpSession.registerCancel.
func (m *MCPClient) registerCancel(reqID string, cancel context.CancelFunc) {
	m.mu.Lock()
	m.activeCancel = cancel
	m.activeReqID = reqID
	m.mu.Unlock()
}

// clearCancel drops the MCPClient's in-flight registration for reqID. A no-op
// if a newer call already replaced the slot. Implements the cancelSink seam.
func (m *MCPClient) clearCancel(reqID string) {
	m.mu.Lock()
	if m.activeReqID == reqID {
		m.activeCancel = nil
		m.activeReqID = ""
	}
	m.mu.Unlock()
}

// EnsureServer probes the graph server for liveness. If the server is not
// reachable on the configured port, it returns a fail-fast error naming the
// binary + flag the human should run to bring the server up. There is no
// auto-start — the client does not launch child processes.
//
// Callers gate this on the active-backend selection: a logged-in user routes
// every fall-through op to cloud via Dispatch and operates with no local
// knowledge-server, so handleMCPToolCall skips EnsureServer when
// cfg.LoggedIn(ctx) is true. Only the local (logged-out) path runs it.
func (m *MCPClient) EnsureServer() error {
	if m.cfg.Client.Healthy() {
		return nil
	}
	return fmt.Errorf("knowledge-server not reachable on port %d (start it with `./bin/knowledge-server --port %d --root .`)", m.cfg.Port, m.cfg.Port)
}

// handleMCPRequestCtx routes a single JSON-RPC request, threading ctx into the
// tools/call path. The HTTP transport passes a ctx carrying the session id +
// workspace cwd (stamped in handlePOST) so InjectRepoIfCodeGraph resolves
// code-graph calls against the session's repo and the per-session cancel slot
// is keyed off the session. Only the tools/call arm consumes ctx; the other
// arms are ctx-independent.
func (m *MCPClient) handleMCPRequestCtx(ctx context.Context, req kgtools.JSONRPCRequest) *kgtools.JSONRPCResponse {
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
		return m.dispatchToolCall(ctx, req)
	case "ping":
		return &kgtools.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}
	default:
		return &kgtools.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &kgtools.RPCError{Code: -32601, Message: "method not found"}}
	}
}

// clipArgs truncates a tool-arguments string for bounded log output, appending
// the original byte length when elided. Keeps panic/debug logs from dumping
// large payloads (e.g. mutate batches).
func clipArgs(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return fmt.Sprintf("%s…(%dB total)", s[:max], len(s))
}

// handleMCPToolCall handles a tools/call JSON-RPC request with a background
// context — dispatching with no per-session workspace cwd. The HTTP daemon
// calls dispatchToolCall directly with a session-stamped ctx (see
// handleMCPRequestCtx → handlePOST); this background-ctx entry is the
// session-less convenience wrapper that the dispatch tests exercise. Both share
// the one dispatchToolCall body — there is no duplicated dispatch logic.
func (m *MCPClient) handleMCPToolCall(req kgtools.JSONRPCRequest) *kgtools.JSONRPCResponse {
	return m.dispatchToolCall(context.Background(), req)
}

// dispatchToolCall runs the full tools/call path — intercept chain → local
// server ensure → compile-or-DENY Dispatch — deriving its cancellable context
// from the PASSED ctx. The HTTP daemon passes a ctx carrying the session id +
// workspace cwd, so InjectRepoIfCodeGraph (inside the intercept chain) resolves
// code-graph calls against the session's repo; the background-ctx wrapper
// (handleMCPToolCall) passes context.Background(). The WithCancel registration
// is the single-in-flight cancellation slot for this call, registered against
// the cancelSink resolved from ctx (per-session *httpSession, else the
// MCPClient process-level slot).
func (m *MCPClient) dispatchToolCall(ctx context.Context, req kgtools.JSONRPCRequest) (resp *kgtools.JSONRPCResponse) {
	var params kgtools.CallToolParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &kgtools.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &kgtools.RPCError{Code: -32602, Message: "invalid params"}}
	}

	// Panic safety net: a panic anywhere in the intercept chain or the dispatch
	// path must be logged with its full stack and returned as a tool error —
	// never allowed to escape and crash the daemon process (which would drop
	// the connection and lose the stack). Diagnostic instrumentation for the
	// client-hosted search path.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("mcpClient: PANIC recovered in tool call",
				"tool", params.Name,
				"args", clipArgs(string(params.Arguments), 400),
				"panic", fmt.Sprintf("%v", r),
				"stack", string(debug.Stack()))
			resp = &kgtools.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: kgtools.ToolResult{
				Content: []kgtools.ContentBlock{{Type: "text", Text: fmt.Sprintf("Error: internal panic in tool %q: %v", params.Name, r)}},
				IsError: true,
			}}
		}
	}()

	slog.Debug("mcpClient: tool call entry", "tool", params.Name, "args", clipArgs(string(params.Arguments), 400))

	// Stamp the query-origin operation for the whole call. This is THE tool-side
	// entry point: every covered RPC issued while handling this call — by the
	// intercept chain, by a wire helper, by the engine dispatch below — inherits
	// it from ctx, so no call site does its own bookkeeping and none can be
	// forgotten. A sub-path that wants finer attribution (a fallback scan, a
	// hydrate pass) re-stamps a refinement term on its own derived ctx.
	ctx = WithOperation(ctx, OperationForTool(params.Name))

	// Run the client-side intercept chain. The chain may handle the call
	// inline OR rewrite params (e.g. repo:+branch: injection for code-graph
	// calls) and fall through. Capture the rewritten params so the proxy
	// below uses them — discarding them strips rewrites silently. ctx carries
	// the per-session workspace cwd on the HTTP path.
	if m.cfg.InterceptChain != nil {
		slog.Debug("mcpClient: running intercept chain", "tool", params.Name)
		rewritten, intercepted, interceptResult := m.cfg.InterceptChain(ctx, params)
		slog.Debug("mcpClient: intercept chain returned", "tool", params.Name, "intercepted", intercepted)
		if intercepted {
			return &kgtools.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: interceptResult}
		}
		params = rewritten
	}

	// License gating is per-tool (paid features only) and lives inside the
	// tools package on the server side. The MCP client proxies all calls
	// unconditionally; free tools always reach the server, paid tools get
	// a clean kgtools.ToolResult denial from the handler when no license is present.

	// Ensure the LOCAL server is running before proxying — but only when the
	// active backend is local. A logged-in user routes to cloud via Dispatch,
	// so this gate is skipped for them (cfg.LoggedIn(ctx) == true); a nil
	// LoggedIn always gates, preserving the logged-out / test-fixture default.
	slog.Info("mcpClient: ensuring server for tool", "tool", params.Name)
	if m.cfg.LoggedIn == nil || !m.cfg.LoggedIn(ctx) {
		if err := m.EnsureServer(); err != nil {
			slog.Error("mcpClient: ensureServer failed", "tool", params.Name, "error", err)
			return &kgtools.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: kgtools.ToolResult{
				Content: []kgtools.ContentBlock{{Type: "text", Text: "Error: " + err.Error()}},
				IsError: true,
			}}
		}
	}
	slog.Info("mcpClient: server ready, proxying call", "tool", params.Name)

	// Create a cancellable context (derived from the passed ctx) so
	// notifications/cancelled can stop this call. The single-in-flight cancel
	// slot lives on the per-session *httpSession when the HTTP transport
	// stamped one onto ctx, else on the MCPClient itself (the background-ctx
	// fallback). Both satisfy the cancelSink seam, so registration is uniform.
	ctx, cancel := context.WithCancel(ctx)
	reqID := string(req.ID)
	sink := m.cancelSinkForContext(ctx)
	sink.registerCancel(reqID, cancel)

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
	sink.clearCancel(reqID)
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

// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/session"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// Supported streamable-HTTP MCP protocol versions, newest first. The HTTP
// initialize handler echoes the client's requested version when it is one
// of these, otherwise it falls back to defaultHTTPProtocolVersion. The
// fallback handleInitialize arm (handleMCPRequestCtx) still answers on the
// older 2024-11-05 version — only handleHTTPInitialize speaks these.
const (
	defaultHTTPProtocolVersion = "2025-11-25"
	altHTTPProtocolVersion     = "2025-06-18"
)

// mcpSessionHeader is the streamable-HTTP MCP session header. The daemon
// mints it on initialize and requires it on every subsequent request.
const mcpSessionHeader = "Mcp-Session-Id"

// HTTPServer serves the MCP tool surface over streamable-HTTP on a
// loopback endpoint, delegating every request to an *MCPClient — its
// intercept chain + compile-or-DENY dispatch handle the tool calls; the
// HTTPServer only adds the transport framing and per-session state.
//
// Multi-session scope (Ticket B): the daemon serves N concurrent sessions.
// Each Mcp-Session-Id maps to an *httpSession holding that connection's
// peer-resolved workspace cwd + cancellation state. The session cwd is
// threaded onto the dispatch ctx so concurrent sessions from different repos
// resolve the correct code graph. The LLM pipeline is process-shared (one per
// daemon, NOT per session) — the resource fix.
type HTTPServer struct {
	mc      *MCPClient // tool-call dispatcher — intercept + Dispatch
	port    int        // loopback TCP port to bind
	version string     // protocol echo target lives on mc.cfg.Version; cached for clarity

	// allowedOrigins is the O(1) lookup set of browser web origins the
	// corsMiddleware reflects back in Access-Control-Allow-Origin. Built once in
	// NewHTTPServer from the constructor's allowedOrigins slice. A request whose
	// Origin is absent gets NO Access-Control-Allow-Origin header — the set is
	// never widened to '*'. Empty/nil disables cross-origin reflection entirely.
	allowedOrigins map[string]struct{}

	// mu guards sessions. A plain map + RWMutex (NOT xsync) keeps the client
	// module dependency-free — xsync/v4 is a server-module dep absent here,
	// and this mirrors A's prior sync.Mutex idiom for the single session.
	mu       sync.RWMutex
	sessions map[string]*httpSession

	// idleTTL is the per-session idle window the reaper enforces (Phase 3).
	// Zero disables the reaper (used by tests that drive sessions directly).
	idleTTL time.Duration
}

// NewHTTPServer builds an HTTPServer wrapping the given MCPClient (the
// tool-call dispatcher) bound to the loopback port. The MCPClient carries the
// MCPClientConfig (InterceptChain/Dispatch/HandleToolsList/LoggedIn) the
// daemon dispatches through.
//
// allowedOrigins is the browser web-origin allow-list for the CORS middleware:
// a cross-origin request whose Origin appears here gets that exact Origin
// reflected back in Access-Control-Allow-Origin (never '*'); any other Origin
// gets no such header. It is collapsed into an O(1) set once here. An
// empty/nil slice disables cross-origin reflection (e.g. tests that don't
// exercise CORS).
func NewHTTPServer(mc *MCPClient, port int, allowedOrigins []string) *HTTPServer {
	originSet := make(map[string]struct{}, len(allowedOrigins))
	for _, o := range allowedOrigins {
		if o != "" {
			originSet[o] = struct{}{}
		}
	}
	return &HTTPServer{
		mc:             mc,
		port:           port,
		version:        mc.cfg.Version,
		sessions:       make(map[string]*httpSession),
		idleTTL:        defaultSessionIdleTTL,
		allowedOrigins: originSet,
	}
}

// Run mounts /mcp and serves it over h2c (HTTP/2 cleartext) on a loopback
// listener until ctx is cancelled. It mirrors the graph server's transport
// idiom (cmd/knowledge-server/internal/server/server.go:131-192) — h2c
// wrapper, a ReadHeaderTimeout with no ReadTimeout (the streaming GET
// outlives any body timeout), goroutine Serve + ctx-driven Shutdown — but
// re-uses the http2/h2c std-ecosystem packages directly rather than
// importing the server-internal package (client/server boundary, AGENTS.md).
func (h *HTTPServer) Run(ctx context.Context) error {
	// Idle-session reaper: Codex reconnects per turn and may skip DELETE
	// /mcp, so its prior sessions go idle and must be swept to keep the
	// client-side session map from growing unbounded. Skipped when idleTTL is
	// zero (tests drive sessions directly). Stopped on ctx.Done.
	if h.idleTTL > 0 {
		go h.runReaper(ctx)
	}

	// Loopback-only bind with an IsLoopback tripwire so a refactor that
	// swaps the literal host can't quietly expose the daemon to the
	// network — mirrors cmd/knowledge-server/internal/bootstrap/server.go:225.
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(h.port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("serve: listen on %s: %w", addr, err)
	}
	if tcp, ok := ln.Addr().(*net.TCPAddr); !ok || !tcp.IP.IsLoopback() {
		bound := ln.Addr().String()
		_ = ln.Close()
		return fmt.Errorf("serve: refusing to serve on a non-loopback address %s", bound)
	}

	// Wrap the mux in h2c so HTTP/2-over-cleartext clients work on the
	// same loopback listener as HTTP/1.1 clients. No ReadTimeout — the
	// server→client SSE GET is a long-lived stream that outlives any body
	// timeout.
	h2s := &http2.Server{}
	srv := &http.Server{
		Addr: ln.Addr().String(),
		// corsMiddleware wraps the /mcp mux so browser preflight (OPTIONS) and
		// cross-origin reads get the restricted CORS + Private-Network-Access
		// headers before routing; non-OPTIONS requests fall through to the mux's
		// method switch unchanged.
		Handler:           h2c.NewHandler(h.corsMiddleware(h.mux()), h2s),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("knowledge serve: HTTP MCP daemon listening", "addr", ln.Addr().String())
		if serveErr := srv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		slog.Info("knowledge serve: shutting down HTTP MCP daemon")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if shutErr := srv.Shutdown(shutdownCtx); shutErr != nil {
			slog.Warn("knowledge serve: shutdown encountered error", "error", shutErr)
		}
		return nil
	case serveErr := <-errCh:
		return serveErr
	}
}

// mux builds the /mcp ServeMux with the method-switch handler. Extracted from
// Run so the composed handler (corsMiddleware(mux)) can be exercised directly
// from tests via httptest without binding a TCP socket — the OPTIONS preflight
// is mux/middleware-level routing the handler-direct tests cannot reach. Pure
// relocation: the POST/GET/DELETE routing and the default-405 arm are unchanged.
func (h *HTTPServer) mux() *http.ServeMux {
	mux := http.NewServeMux()

	// /mcp is served plainly — there is no bearer gate. Routing to cloud is
	// decided by the keychain auth state (`knowledge login`), not by a per-request
	// editor bearer, so an unauthenticated MCP request is served without a 401
	// challenge. Cloud access requires the user to have run `knowledge login`.
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			h.handlePOST(w, r)
		case http.MethodGet:
			h.handleGET(w, r)
		case http.MethodDelete:
			h.handleDELETE(w, r)
		default:
			w.Header().Set("Allow", "GET, POST, DELETE")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	return mux
}

// handlePOST serves the client→server JSON-RPC leg of streamable-HTTP MCP.
// It unmarshals the request body and routes:
//
//   - initialize → handleHTTPInitialize, which mints the session, resolves the
//     peer workspace cwd, stores the per-session state keyed by the minted id,
//     sets the Mcp-Session-Id response header, and echoes the requested
//     protocol.
//   - every other method → look the Mcp-Session-Id request header up in the
//     session map (HTTP 404 on miss, per the streamable-HTTP spec for an
//     unknown/expired session) then DELEGATE to h.mc.handleMCPRequestCtx with a
//     session-stamped ctx, which routes tools/call through dispatchToolCall →
//     intercept chain → Dispatch.
//
// handleMCPRequestCtx returns nil for notifications (initialized/cancelled);
// in that case the POST gets HTTP 202 Accepted with an empty body (per
// spec, notifications and responses without a body get 202). A non-nil
// *JSONRPCResponse is marshaled and written as application/json.
//
// Concurrency model (Ticket B): each POST is its own net/http request served
// on its own goroutine, and each session carries its OWN cancellation slot on
// its *httpSession (Phase 3), so concurrent tool calls in DISTINCT sessions
// are fully isolated. Within a single session the model is single-in-flight
// (see the httpSession invariant) — the MCP streamable-HTTP single-flight
// contract — so the per-session cancel slot holds one in-flight call at a time
// and a notifications/cancelled targets exactly it.
func (h *HTTPServer) handlePOST(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	var req kgtools.JSONRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON-RPC request: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.Method == "initialize" {
		resp := h.handleHTTPInitialize(w, r, req)
		writeJSONRPC(w, resp)
		return
	}

	// Every non-initialize method requires a valid session.
	sess, ok := h.validSession(r)
	if !ok {
		writeSessionError(w, req)
		return
	}
	// Bump lastSeen so the idle reaper does not evict an actively-used session.
	sess.touch(time.Now())

	// notifications/cancelled targets THIS session's in-flight call only —
	// route it to the per-session cancel slot rather than the dispatch path
	// (the HTTP transport receives it as a POST). A concurrent call in another
	// session is untouched. No response body (it is a notification) → HTTP 202.
	if req.Method == "notifications/cancelled" {
		sess.cancelMatching(cancelRequestID(req.Params))
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Stamp the session id + resolved workspace cwd onto the dispatch ctx so
	// the intercept chain (InjectRepoIfCodeGraph) routes code-graph calls to
	// this session's repo, and the *httpSession itself so dispatchToolCall
	// registers its cancel slot at session scope. An empty cwd is a no-op
	// carrier (the session falls back to --root).
	ctx := session.ContextWithSessionID(r.Context(), sess.id)
	ctx = session.ContextWithWorkspaceCwd(ctx, sess.cwd)
	ctx = contextWithHTTPSession(ctx, sess)

	resp := h.mc.handleMCPRequestCtx(ctx, req)
	if resp == nil {
		// Notification (initialized/cancelled) — no response body per spec.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSONRPC(w, resp)
}

// handleGET serves the server→client SSE leg of streamable-HTTP MCP. It
// validates the session, opens a flushing text/event-stream channel, and
// holds it open until the request context is cancelled, emitting periodic
// keep-alive comments so intermediaries do not reap an idle connection.
//
// The daemon has no server-initiated requests to push (roots/list, sampling
// would arrive with later feature work), so the stream carries only
// keep-alives; the structural requirement is that GET opens a valid,
// flushing, long-lived SSE channel a compliant client (e.g. Claude Code) can
// attach to.
func (h *HTTPServer) handleGET(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.validSession(r); !ok {
		http.Error(w, "unknown or expired session", http.StatusNotFound)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	keepAlive := time.NewTicker(25 * time.Second)
	defer keepAlive.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-keepAlive.C:
			// SSE comment line — keeps the connection warm without
			// delivering an event the client would try to parse.
			if _, err := io.WriteString(w, ":\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// handleDELETE serves the streamable-HTTP DELETE /mcp leg: a client may end
// its session by sending DELETE with its Mcp-Session-Id. It drops the
// session's client-side state (cwd cache + cancel registry) and returns 204
// No Content. An unknown/absent id returns 404 (per the unknown-session spec
// posture) — a no-op delete still surfaces the miss legibly. This removes only
// the CLIENT-SIDE entry; the daemon holds no server-side per-session state.
func (h *HTTPServer) handleDELETE(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.validSession(r)
	if !ok {
		http.Error(w, "unknown or expired session", http.StatusNotFound)
		return
	}
	h.deleteSession(sess.id)
	w.WriteHeader(http.StatusNoContent)
}

// handleHTTPInitialize answers the HTTP `initialize` request: it echoes the
// client's requested protocolVersion when supported (else defaults to
// defaultHTTPProtocolVersion), mints a fresh session id, resolves the peer
// process's workspace cwd from the connection's ephemeral port, stores the
// per-session state in h.sessions keyed by the minted id, and sets the id on
// the response via the Mcp-Session-Id header. The JSON-RPC result mirrors the
// fallback handleInitialize shape (mcp_client.go) but with the echoed protocol
// and the HTTP session header. The fallback handleInitialize arm stays on
// 2024-11-05.
//
// Peer-cwd resolution is best-effort: a failure (lsof unavailable, race on a
// torn-down ephemeral socket) logs a warning and stores an empty cwd. An empty
// cwd makes the session fall back to deps.RootDir() for repo resolution (the
// pre-B behavior), so a resolution miss degrades rather than breaking the
// session.
func (h *HTTPServer) handleHTTPInitialize(w http.ResponseWriter, r *http.Request, req kgtools.JSONRPCRequest) *kgtools.JSONRPCResponse {
	protocol := negotiateProtocol(req.Params)

	sid, err := mintSessionID()
	if err != nil {
		return &kgtools.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &kgtools.RPCError{Code: -32603, Message: "failed to mint session id: " + err.Error()},
		}
	}

	cwd, pid, comm := h.resolvePeerCwdForRequest(r)
	h.ensureSession(sid, cwd, pid, comm)

	w.Header().Set(mcpSessionHeader, sid)

	return &kgtools.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"protocolVersion": protocol,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "knowledge", "version": h.version},
		},
	}
}

// resolvePeerCwdForRequest extracts the client's ephemeral port from the
// request's RemoteAddr and resolves the owning process's cwd, PID, and comm via
// resolvePeerCwd. Returns ("", 0, "") (logged) on any failure so the caller
// stores a session that falls back to deps.RootDir() for repo resolution; the
// pid + comm are retained for the hive daemon monitor's transcript binding.
func (h *HTTPServer) resolvePeerCwdForRequest(r *http.Request) (cwd string, pid int, comm string) {
	_, portStr, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		slog.Warn("knowledge serve: cannot parse RemoteAddr for peer-cwd resolution", "remoteAddr", r.RemoteAddr, "error", err)
		return "", 0, ""
	}
	ephemeralPort, err := strconv.Atoi(portStr)
	if err != nil {
		slog.Warn("knowledge serve: non-numeric ephemeral port in RemoteAddr", "remoteAddr", r.RemoteAddr, "error", err)
		return "", 0, ""
	}
	cwd, pid, comm, err = resolvePeerCwd(r.Context(), h.port, ephemeralPort)
	if err != nil {
		slog.Warn("knowledge serve: peer-cwd resolution failed; session will fall back to --root", "ephemeralPort", ephemeralPort, "error", err)
		return "", 0, ""
	}
	slog.Info("knowledge serve: resolved session workspace", "ephemeralPort", ephemeralPort, "cwd", cwd, "pid", pid, "comm", comm)
	return cwd, pid, comm
}

// validSession looks up the request's Mcp-Session-Id header in the session
// map and returns the session plus whether it exists. A missing/unknown id
// (including before any session is minted) returns (nil, false).
func (h *HTTPServer) validSession(r *http.Request) (*httpSession, bool) {
	id := r.Header.Get(mcpSessionHeader)
	if id == "" {
		return nil, false
	}
	return h.lookupSession(id)
}

// cancelRequestID extracts the JSON-encoded requestId from a
// notifications/cancelled params payload, matching the encoding
// httpSession.cancelMatching compares against (string(json.RawMessage)).
// Returns "" on a malformed payload (no in-flight call will match "").
func cancelRequestID(rawParams json.RawMessage) string {
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if len(rawParams) > 0 {
		_ = json.Unmarshal(rawParams, &p)
	}
	return string(p.RequestID)
}

// negotiateProtocol reads the client's requested protocolVersion from the
// initialize params and returns it when supported, else the default.
func negotiateProtocol(params json.RawMessage) string {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(params) > 0 {
		_ = json.Unmarshal(params, &p)
	}
	switch p.ProtocolVersion {
	case defaultHTTPProtocolVersion, altHTTPProtocolVersion:
		return p.ProtocolVersion
	default:
		return defaultHTTPProtocolVersion
	}
}

// mintSessionID returns a fresh 16-byte crypto/rand session id as hex.
// There is no shared id generator on the client transport path, so a small
// inline crypto/rand read is the right call here.
func mintSessionID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// writeJSONRPC marshals a JSON-RPC response and writes it as
// application/json. A marshal failure is logged and surfaced as HTTP 500.
func writeJSONRPC(w http.ResponseWriter, resp *kgtools.JSONRPCResponse) {
	out, err := json.Marshal(resp)
	if err != nil {
		slog.Error("knowledge serve: failed to marshal JSON-RPC response", "error", err)
		http.Error(w, "marshal response: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// writeSessionError responds to a request that carried a wrong/absent
// Mcp-Session-Id with HTTP 404 and a JSON-RPC error body, per the
// streamable-HTTP spec for an unknown/expired session.
func writeSessionError(w http.ResponseWriter, req kgtools.JSONRPCRequest) {
	out, err := json.Marshal(&kgtools.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Error:   &kgtools.RPCError{Code: -32600, Message: "unknown or expired session: missing/invalid Mcp-Session-Id"},
	})
	if err != nil {
		// req.ID is opaque client-supplied JSON; on the rare chance it is
		// unmarshalable, drop it and emit a bare error envelope so the
		// 404 still carries a parseable JSON-RPC error.
		out = []byte(`{"jsonrpc":"2.0","error":{"code":-32600,"message":"unknown or expired session: missing/invalid Mcp-Session-Id"}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write(out)
}

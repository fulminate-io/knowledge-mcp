// SPDX-License-Identifier: Apache-2.0

// mcp_http_session_handlers.go — the MCP session's own handlers on the
// streamable-HTTP transport: establishing one (initialize, which mints the id,
// negotiates the protocol version and resolves the peer workspace), validating
// one on an inbound request, and ending one
// (DELETE /mcp). The transport framing itself — Run, the mux, and the POST/GET
// legs — lives in mcp_http.go alongside the HTTPServer type; the per-session
// state those handlers read and write lives in http_session.go.

package graphclient

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strconv"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// handleDELETE serves the streamable-HTTP DELETE /mcp leg: a client may end
// its session by sending DELETE with its Mcp-Session-Id. It drops the
// session's client-side state (cwd cache + cancel registry) and returns 204
// No Content. An unknown/absent id returns 404 (per the unknown-session spec
// posture) — a no-op delete still surfaces the miss legibly. This removes only
// the CLIENT-SIDE entry; the daemon holds no server-side per-session state.
//
// Deleting the session also ends any hive session it was running. When it is the
// LAST hive-active one, the 204 can be delayed by up to two daemonStopDeadline
// budgets (~6s) while the hive reaper and monitor drain — acceptable because the
// connection is going away regardless, and preferable to an unbounded or
// fire-and-forget stop. The same budget is disclosed on the hive tool intercept,
// the other seam that can block on the loop controller.
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

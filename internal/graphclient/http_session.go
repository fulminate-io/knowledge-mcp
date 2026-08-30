// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// httpSession is the CLIENT-SIDE per-connection state for one MCP session,
// keyed by its minted Mcp-Session-Id. It holds the session's resolved
// workspace cwd (used to route code-graph tool calls to the right repo) and
// the per-session single-flight cancellation slot.
//
// WITHIN-SESSION SINGLE-IN-FLIGHT INVARIANT (T3-1): each session assumes ONE
// in-flight tool call at a time — the MCP streamable-HTTP single-flight model.
// The cancel slot (activeCancel/activeReqID) is a SINGLE slot per session, so
// a notifications/cancelled targets that one in-flight call. Overlapping
// concurrent tool calls within the SAME session are NOT a supported
// concurrency mode: a second in-flight call would overwrite the first's cancel
// slot. Concurrency across DISTINCT sessions is fully supported (each has its
// own slot); concurrency within one session is not.
type httpSession struct {
	id        string
	cwd       string // resolved peer-process workspace cwd ("" if resolution failed)
	createdAt time.Time

	// lastSeen is bumped on every validated request and read by the idle
	// reaper. Guarded by lastMu (separate from mu so a touch never contends
	// with an in-flight cancel registration).
	lastMu   sync.Mutex
	lastSeen time.Time

	// Per-session single-flight cancellation. Guarded by mu. Holds the
	// in-flight tool call's cancel func + JSON-encoded request ID. See the
	// within-session single-in-flight invariant above. This is the relocation
	// of A's single MCPClient.{activeCancel,activeReqID} slot to session scope:
	// a notifications/cancelled in one session cancels only that session's
	// in-flight call, leaving concurrent calls in other sessions untouched.
	mu           sync.Mutex
	activeCancel context.CancelFunc
	activeReqID  string
}

// touch bumps the session's lastSeen to now so the idle reaper does not evict
// an actively-used session.
func (s *httpSession) touch(now time.Time) {
	s.lastMu.Lock()
	s.lastSeen = now
	s.lastMu.Unlock()
}

// idleSince reports the session's lastSeen for the reaper's age comparison.
func (s *httpSession) idleSince() time.Time {
	s.lastMu.Lock()
	defer s.lastMu.Unlock()
	return s.lastSeen
}

// registerCancel records the in-flight tool call's cancel func + request ID on
// the session, replacing any prior registration (the within-session
// single-in-flight invariant means there is at most one). Implements the
// cancelSink seam consumed by MCPClient.dispatchToolCall.
func (s *httpSession) registerCancel(reqID string, cancel context.CancelFunc) {
	s.mu.Lock()
	s.activeCancel = cancel
	s.activeReqID = reqID
	s.mu.Unlock()
}

// clearCancel drops the in-flight registration for reqID. A no-op if a newer
// call already replaced the slot. Implements the cancelSink seam.
func (s *httpSession) clearCancel(reqID string) {
	s.mu.Lock()
	if s.activeReqID == reqID {
		s.activeCancel = nil
		s.activeReqID = ""
	}
	s.mu.Unlock()
}

// cancelMatching cancels the in-flight call iff its request ID matches reqID,
// then clears the slot. Called from the HTTP notifications/cancelled path.
func (s *httpSession) cancelMatching(reqID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeCancel != nil && s.activeReqID == reqID {
		s.activeCancel()
		s.activeCancel = nil
		s.activeReqID = ""
	}
}

// httpSessionKey carries the active *httpSession through the dispatch ctx so
// MCPClient.dispatchToolCall registers its cancel slot at SESSION scope on the
// HTTP path. A private type avoids cross-package collisions.
type httpSessionKey struct{}

// contextWithHTTPSession attaches the session as the cancel sink for the
// dispatch ctx.
func contextWithHTTPSession(ctx context.Context, s *httpSession) context.Context {
	return context.WithValue(ctx, httpSessionKey{}, s)
}

// cancelSinkFromContext returns the per-session cancel sink carried on ctx, or
// (nil, false) when none is set (the background-ctx fallback, which uses the
// MCPClient's own single slot).
func cancelSinkFromContext(ctx context.Context) (cancelSink, bool) {
	s, ok := ctx.Value(httpSessionKey{}).(*httpSession)
	return s, ok
}

// cancelSink is the seam MCPClient.dispatchToolCall registers its in-flight
// cancellation against: the per-session *httpSession on the HTTP path, or the
// MCPClient's own slot on the background-ctx fallback.
type cancelSink interface {
	registerCancel(reqID string, cancel context.CancelFunc)
	clearCancel(reqID string)
}

// defaultSessionIdleTTL is the idle window after which the reaper evicts a
// session whose connection went away without a DELETE /mcp. Claude holds a
// persistent connection; Codex reconnects per turn, so its prior sessions go
// idle and must be reaped. A generous window avoids evicting a slow-but-live
// session.
const defaultSessionIdleTTL = 30 * time.Minute

// ensureSession stores a session for id carrying cwd, unless one already exists
// (idempotent: a repeat initialize with the same minted id reuses the stored
// session). Minted session ids are unique per initialize, so the already-exists
// branch is defensive.
func (h *HTTPServer) ensureSession(id, cwd string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.sessions[id]; ok {
		return
	}
	now := time.Now()
	h.sessions[id] = &httpSession{id: id, cwd: cwd, createdAt: now, lastSeen: now}
}

// lookupSession returns the session for id, or (nil, false) if no such
// session exists.
func (h *HTTPServer) lookupSession(id string) (*httpSession, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	s, ok := h.sessions[id]
	return s, ok
}

// deleteSession removes the session's client-side entry (cwd cache + cancel
// registry). Idempotent — deleting an absent id is a no-op. This is the
// CLIENT-SIDE HTTPServer.sessions map only; the daemon holds no server-side
// per-session state (the server is session-oblivious).
func (h *HTTPServer) deleteSession(id string) {
	h.mu.Lock()
	delete(h.sessions, id)
	h.mu.Unlock()
}

// reapIdle deletes every session whose lastSeen is older than h.idleTTL
// relative to now. A zero idleTTL disables reaping (returns immediately) —
// used by tests that drive sessions directly. Returns the number of sessions
// evicted (for test assertions). Reaping is the second teardown path; the first
// is DELETE /mcp.
func (h *HTTPServer) reapIdle(now time.Time) int {
	if h.idleTTL <= 0 {
		return 0
	}
	var stale []string
	h.mu.RLock()
	for id, s := range h.sessions {
		if now.Sub(s.idleSince()) > h.idleTTL {
			stale = append(stale, id)
		}
	}
	h.mu.RUnlock()
	if len(stale) == 0 {
		return 0
	}
	h.mu.Lock()
	for _, id := range stale {
		delete(h.sessions, id)
	}
	h.mu.Unlock()

	return len(stale)
}

// runReaper sweeps idle sessions on a ticker until ctx is cancelled. The tick
// interval is idleTTL (a session can survive at most ~2 ticks of idleness),
// which is plenty granular for the 30-minute default while staying cheap.
func (h *HTTPServer) runReaper(ctx context.Context) {
	t := time.NewTicker(h.idleTTL)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n := h.reapIdle(time.Now()); n > 0 {
				slog.Info("knowledge serve: reaped idle sessions", "count", n)
			}
		}
	}
}

// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/session"
)

// timeAfter is the per-wait timeout for the concurrency tests — generous
// enough to avoid flakes on a loaded CI box, short enough to fail fast.
func timeAfter() <-chan time.Time { return time.After(5 * time.Second) }

// newTestHTTPServer builds an HTTPServer wrapping a minimal MCPClient, with no
// hive claim Registry. The reaper is disabled (idleTTL 0) so tests drive
// sessions deterministically.
func newTestHTTPServer() *HTTPServer { return newTestHTTPServerWithHive(nil) }

// newTestHTTPServerWithHive is newTestHTTPServer with a hive claim Registry
// wired, for the session-lifecycle tests. The two share one constructor call so
// the package keeps a single NewHTTPServer call site for the helper.
func newTestHTTPServerWithHive(hiveSessions *hivemonitor.Registry) *HTTPServer {
	mc := NewMCPClient(MCPClientConfig{Version: "test"})
	h := NewHTTPServer(mc, 15023, nil, hiveSessions)
	h.idleTTL = 0
	return h
}

// injectPeerCwd wires the peerCwdRunner seam so the ephemeral port → cwd
// mapping is deterministic: portToCwd maps an ephemeral port to the cwd the
// fake lsof should report, via a synthetic PID derived from the port.
func injectPeerCwd(t *testing.T, portToCwd map[int]string) {
	t.Helper()
	orig := peerCwdRunner
	t.Cleanup(func() { peerCwdRunner = orig })
	peerCwdRunner = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		// First leg: lsof -nP -iTCP:<port>. Emit a client line whose LOCAL
		// side is <port>, with PID = port (synthetic, unique per port).
		for port := range portToCwd {
			marker := "-iTCP:" + strconv.Itoa(port)
			if strings.Contains(joined, marker) {
				line := "node       " + strconv.Itoa(port) + " u   23u  IPv4 0xdead      0t0  TCP 127.0.0.1:" + strconv.Itoa(port) + "->127.0.0.1:15023 (ESTABLISHED)"
				return []byte("COMMAND PID USER FD TYPE DEVICE OFF NODE NAME\n" + line), nil
			}
		}
		// Second leg: lsof -a -p <pid> -d cwd -Fn. PID == port, so map back.
		for port, cwd := range portToCwd {
			if strings.Contains(joined, "-p "+strconv.Itoa(port)) {
				return []byte("p" + strconv.Itoa(port) + "\nfcwd\nn" + cwd + "\n"), nil
			}
		}
		t.Fatalf("unexpected lsof invocation: %v", args)
		return nil, nil
	}
}

// doInitialize drives one initialize POST from the given client ephemeral
// port and returns the minted Mcp-Session-Id.
func doInitialize(t *testing.T, h *HTTPServer, ephemeralPort int) string {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:" + strconv.Itoa(ephemeralPort)
	rec := httptest.NewRecorder()
	h.handlePOST(rec, req)
	if rec.Code != 200 {
		t.Fatalf("initialize: got HTTP %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	sid := rec.Header().Get(mcpSessionHeader)
	if sid == "" {
		t.Fatal("initialize: no Mcp-Session-Id header on response")
	}
	return sid
}

// TestTwoSessionsDistinctCwd drives two initialize calls from distinct
// ephemeral ports mapping to distinct cwds and asserts two sessions exist,
// each carrying its own resolved cwd.
func TestTwoSessionsDistinctCwd(t *testing.T) {
	injectPeerCwd(t, map[int]string{
		54321: "/Users/jonathan/code/knowledge",
		54322: "/Users/jonathan/code/agent",
	})
	h := newTestHTTPServer()

	sidK := doInitialize(t, h, 54321)
	sidA := doInitialize(t, h, 54322)

	if sidK == sidA {
		t.Fatal("expected two distinct minted session ids")
	}

	sK, ok := h.lookupSession(sidK)
	if !ok {
		t.Fatalf("session %s not found", sidK)
	}
	if sK.cwd != "/Users/jonathan/code/knowledge" {
		t.Fatalf("session K cwd = %q, want /Users/jonathan/code/knowledge", sK.cwd)
	}

	sA, ok := h.lookupSession(sidA)
	if !ok {
		t.Fatalf("session %s not found", sidA)
	}
	if sA.cwd != "/Users/jonathan/code/agent" {
		t.Fatalf("session A cwd = %q, want /Users/jonathan/code/agent", sA.cwd)
	}

	h.mu.RLock()
	n := len(h.sessions)
	h.mu.RUnlock()
	if n != 2 {
		t.Fatalf("expected 2 sessions, got %d", n)
	}
}

// TestInitializeResolutionFailureDegrades asserts a peer-cwd resolution miss
// stores a session with empty cwd (falls back to --root) rather than failing
// the initialize.
func TestInitializeResolutionFailureDegrades(t *testing.T) {
	orig := peerCwdRunner
	t.Cleanup(func() { peerCwdRunner = orig })
	peerCwdRunner = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte("COMMAND PID USER FD TYPE DEVICE OFF NODE NAME\n"), nil // no matching line → resolve error
	}
	h := newTestHTTPServer()
	sid := doInitialize(t, h, 60000)
	s, ok := h.lookupSession(sid)
	if !ok {
		t.Fatal("session not stored despite resolution failure")
	}
	if s.cwd != "" {
		t.Fatalf("expected empty cwd on resolution failure, got %q", s.cwd)
	}
}

// postWithSession drives a non-initialize POST carrying the given
// Mcp-Session-Id header and returns the recorder.
func postWithSession(h *HTTPServer, sid, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	if sid != "" {
		req.Header.Set(mcpSessionHeader, sid)
	}
	req.RemoteAddr = "127.0.0.1:55555"
	rec := httptest.NewRecorder()
	h.handlePOST(rec, req)
	return rec
}

// TestSessionValidation asserts the Mcp-Session-Id is validated against the
// session map: a POST with a valid minted id is accepted and routed; a POST
// with an unknown or absent id is rejected with HTTP 404.
func TestSessionValidation(t *testing.T) {
	injectPeerCwd(t, map[int]string{54321: "/Users/jonathan/code/knowledge"})
	h := newTestHTTPServer()
	sid := doInitialize(t, h, 54321)

	// ping is a fast, side-effect-free method handleMCPRequestCtx answers with
	// a 200 result — confirms the valid-session arm routes through.
	ping := `{"jsonrpc":"2.0","id":2,"method":"ping"}`
	if rec := postWithSession(h, sid, ping); rec.Code != 200 {
		t.Fatalf("valid session ping: got HTTP %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// Unknown id → 404.
	if rec := postWithSession(h, "deadbeefdeadbeef", ping); rec.Code != 404 {
		t.Fatalf("unknown session: got HTTP %d, want 404", rec.Code)
	}

	// Absent id → 404.
	if rec := postWithSession(h, "", ping); rec.Code != 404 {
		t.Fatalf("absent session: got HTTP %d, want 404", rec.Code)
	}
}

// TestHTTPDispatchStampsSessionCwd is the end-to-end assertion: two HTTP
// sessions with different injected cwds each issue a tools/call, and the
// intercept chain (which on the HTTP path receives the session-stamped ctx)
// sees each session's own workspace cwd. A nil-cwd ctx (stdio) sees "".
func TestHTTPDispatchStampsSessionCwd(t *testing.T) {
	injectPeerCwd(t, map[int]string{
		54321: "/Users/jonathan/code/knowledge",
		54322: "/Users/jonathan/code/agent",
	})

	// Capture, per call, the workspace cwd visible to the intercept chain.
	var mu sync.Mutex
	seen := map[string]string{} // toolCallID(arg "tag") → cwd seen

	mc := NewMCPClient(MCPClientConfig{
		Version: "test",
		InterceptChain: func(ctx context.Context, params kgtools.CallToolParams) (kgtools.CallToolParams, bool, kgtools.ToolResult) {
			cwd := session.WorkspaceCwdFromContext(ctx)
			mu.Lock()
			seen[string(params.Arguments)] = cwd
			mu.Unlock()
			// Handle inline so no server is needed.
			return params, true, kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: "ok"}}}
		},
	})
	h := NewHTTPServer(mc, 15023, nil, nil)
	h.idleTTL = 0

	sidK := doInitialize(t, h, 54321)
	sidA := doInitialize(t, h, 54322)

	callBody := func(tag string) string {
		return `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"search","arguments":` + tag + `}}`
	}
	if rec := postWithSession(h, sidK, callBody(`"K"`)); rec.Code != 200 {
		t.Fatalf("session K tools/call: HTTP %d", rec.Code)
	}
	if rec := postWithSession(h, sidA, callBody(`"A"`)); rec.Code != 200 {
		t.Fatalf("session A tools/call: HTTP %d", rec.Code)
	}

	mu.Lock()
	defer mu.Unlock()
	if seen[`"K"`] != "/Users/jonathan/code/knowledge" {
		t.Fatalf("session K intercept saw cwd %q, want /Users/jonathan/code/knowledge", seen[`"K"`])
	}
	if seen[`"A"`] != "/Users/jonathan/code/agent" {
		t.Fatalf("session A intercept saw cwd %q, want /Users/jonathan/code/agent", seen[`"A"`])
	}
}

// TestPerSessionCancellationIsolation drives two sessions with overlapping
// in-flight tool calls and a notifications/cancelled targeting session A: A's
// call must observe ctx cancellation while B's concurrent call is unaffected.
func TestPerSessionCancellationIsolation(t *testing.T) {
	injectPeerCwd(t, map[int]string{
		54321: "/Users/jonathan/code/knowledge",
		54322: "/Users/jonathan/code/agent",
	})

	// Each tool call blocks in Dispatch until its ctx is cancelled OR a
	// release signal fires, recording which path won.
	type outcome struct{ cancelled bool }
	results := make(map[string]chan outcome, 2)
	results["A"] = make(chan outcome, 1)
	results["B"] = make(chan outcome, 1)
	releaseB := make(chan struct{})

	started := make(map[string]chan struct{}, 2)
	started["A"] = make(chan struct{}, 1)
	started["B"] = make(chan struct{}, 1)

	mc := NewMCPClient(MCPClientConfig{
		Version: "test",
		// No InterceptChain → fall through to Dispatch with the cancellable ctx.
		Dispatch: func(ctx context.Context, _ string, args json.RawMessage) (kgtools.ToolResult, error) {
			tag := strings.Trim(string(args), `"`)
			if ch, ok := started[tag]; ok {
				ch <- struct{}{}
			}
			switch tag {
			case "A":
				<-ctx.Done() // A is the cancellation target
				results["A"] <- outcome{cancelled: true}
			case "B":
				select {
				case <-ctx.Done():
					results["B"] <- outcome{cancelled: true}
				case <-releaseB:
					results["B"] <- outcome{cancelled: false}
				}
			}
			return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: "done"}}}, nil
		},
		LoggedIn: func(context.Context) bool { return true }, // skip EnsureServer
	})
	h := NewHTTPServer(mc, 15023, nil, nil)
	h.idleTTL = 0

	sidA := doInitialize(t, h, 54321)
	sidB := doInitialize(t, h, 54322)

	call := func(sid, tag, id string) {
		body := `{"jsonrpc":"2.0","id":` + id + `,"method":"tools/call","params":{"name":"search","arguments":"` + tag + `"}}`
		go func() {
			rec := postWithSession(h, sid, body)
			_ = rec
		}()
	}
	call(sidA, "A", "100")
	call(sidB, "B", "200")

	// Wait until both calls are in-flight inside Dispatch.
	<-started["A"]
	<-started["B"]

	// Cancel session A's in-flight call (requestId == A's tools/call id "100").
	cancelBody := `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":100}}`
	if rec := postWithSession(h, sidA, cancelBody); rec.Code != 202 {
		t.Fatalf("cancel POST: HTTP %d, want 202", rec.Code)
	}

	// A must report cancellation.
	select {
	case got := <-results["A"]:
		if !got.cancelled {
			t.Fatal("session A call did not observe cancellation")
		}
	case <-timeAfter():
		t.Fatal("session A call did not return after cancel")
	}

	// B must still be running (not cancelled by A's cancel). Release it and
	// confirm it completed WITHOUT cancellation.
	close(releaseB)
	select {
	case got := <-results["B"]:
		if got.cancelled {
			t.Fatal("session B call was cancelled by session A's notifications/cancelled — sessions are not isolated")
		}
	case <-timeAfter():
		t.Fatal("session B call did not return after release")
	}
}

// TestSessionsHoldNoPipeline asserts the shared-pipeline invariant
// structurally: neither HTTPServer nor httpSession carries a pipeline
// reference, so N sessions cannot each spin up their own pipeline — the one
// process-shared pipeline (wired on *client) is the only one: HTTPServer and
// httpSession hold no pipeline reference.
func TestSessionsHoldNoPipeline(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeFor[HTTPServer](), reflect.TypeFor[httpSession]()} {
		for _, f := range reflect.VisibleFields(typ) {
			if strings.Contains(strings.ToLower(f.Name), "pipeline") {
				t.Fatalf("%s has a pipeline-related field %q — the pipeline must be process-shared on *client, never per-session", typ.Name(), f.Name)
			}
		}
	}

	// Creating many sessions touches only the in-memory session map; there is
	// no pipeline field to construct, so this stays O(sessions) bookkeeping
	// with a single shared pipeline living on the *client (out of band here).
	injectPeerCwd(t, map[int]string{54321: "/Users/jonathan/code/knowledge"})
	h := newTestHTTPServer()
	for i := range 5 {
		h.ensureSession("sess-"+strconv.Itoa(i), "/tmp", 0, "")
	}
	h.mu.RLock()
	n := len(h.sessions)
	h.mu.RUnlock()
	if n != 5 {
		t.Fatalf("expected 5 sessions, got %d", n)
	}
}

// TestDeleteRemovesSession asserts DELETE /mcp with a valid Mcp-Session-Id
// removes the session (204) and a subsequent POST with the same id gets 404.
func TestDeleteRemovesSession(t *testing.T) {
	injectPeerCwd(t, map[int]string{54321: "/Users/jonathan/code/knowledge"})
	h := newTestHTTPServer()
	sid := doInitialize(t, h, 54321)

	// DELETE with the valid id → 204.
	delReq := httptest.NewRequest("DELETE", "/mcp", nil)
	delReq.Header.Set(mcpSessionHeader, sid)
	delRec := httptest.NewRecorder()
	h.handleDELETE(delRec, delReq)
	if delRec.Code != 204 {
		t.Fatalf("DELETE valid session: HTTP %d, want 204", delRec.Code)
	}

	// Subsequent POST with the same id → 404 (session gone).
	if rec := postWithSession(h, sid, `{"jsonrpc":"2.0","id":2,"method":"ping"}`); rec.Code != 404 {
		t.Fatalf("POST after DELETE: HTTP %d, want 404", rec.Code)
	}

	// DELETE an unknown id → 404.
	delReq2 := httptest.NewRequest("DELETE", "/mcp", nil)
	delReq2.Header.Set(mcpSessionHeader, "deadbeef")
	delRec2 := httptest.NewRecorder()
	h.handleDELETE(delRec2, delReq2)
	if delRec2.Code != 404 {
		t.Fatalf("DELETE unknown session: HTTP %d, want 404", delRec2.Code)
	}
}

// TestReaperEvictsStaleSession drives the idle reaper directly with a short
// TTL and an injected clock: a session whose lastSeen is older than the TTL is
// evicted, a freshly-touched one survives.
func TestReaperEvictsStaleSession(t *testing.T) {
	injectPeerCwd(t, map[int]string{54321: "/Users/jonathan/code/knowledge", 54322: "/Users/jonathan/code/agent"})
	h := newTestHTTPServer()
	h.idleTTL = 100 * time.Millisecond // short window for the reaper

	stale := doInitialize(t, h, 54321)
	fresh := doInitialize(t, h, 54322)

	// Age the stale session's lastSeen well past the TTL; keep fresh recent.
	now := time.Now()
	if s, ok := h.lookupSession(stale); ok {
		s.touch(now.Add(-time.Hour))
	}
	if s, ok := h.lookupSession(fresh); ok {
		s.touch(now)
	}

	evicted := h.reapIdle(now)
	if evicted != 1 {
		t.Fatalf("reapIdle evicted %d sessions, want 1", evicted)
	}
	if _, ok := h.lookupSession(stale); ok {
		t.Fatal("stale session was not evicted")
	}
	if _, ok := h.lookupSession(fresh); !ok {
		t.Fatal("fresh session was wrongly evicted")
	}
}

// TestReaperDisabledWhenTTLZero asserts a zero idleTTL disables reaping.
func TestReaperDisabledWhenTTLZero(t *testing.T) {
	injectPeerCwd(t, map[int]string{54321: "/Users/jonathan/code/knowledge"})
	h := newTestHTTPServer() // idleTTL = 0
	sid := doInitialize(t, h, 54321)
	if s, ok := h.lookupSession(sid); ok {
		s.touch(time.Now().Add(-time.Hour))
	}
	if n := h.reapIdle(time.Now()); n != 0 {
		t.Fatalf("reapIdle with zero TTL evicted %d, want 0", n)
	}
}

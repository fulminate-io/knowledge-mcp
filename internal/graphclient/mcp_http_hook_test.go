// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newHookTestServer builds an HTTPServer with the reaper disabled so the tests
// drive the sweep themselves with an explicit `now`, the way reapIdle is
// already written for. Port 0 binds nothing — every request goes through the
// mux via httptest.
func newHookTestServer() *HTTPServer {
	h := NewHTTPServer(NewMCPClient(MCPClientConfig{Version: "test"}), 0, nil)
	h.idleTTL = 0
	return h
}

// postHook delivers body to path through the mux and returns the status code.
// remoteAddr overrides the peer address; "" means the loopback peer a real
// delivery has. httptest.NewRequest's own default RemoteAddr is 192.0.2.1
// (TEST-NET-1), which is NOT loopback, so a test that wants the accept path
// must say so — leaving it at the default would exercise the refusal and read
// as the accept row passing.
func postHook(t *testing.T, h *HTTPServer, path, body, remoteAddr string, headers ...string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:54321"
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, req)
	return rec.Code
}

// claudeHookBody is a well-formed Claude PreToolUse delivery. It carries the
// undeclared extra keys the real harness sends (permission_mode, tool_input,
// prompt_id) so the decode is exercised against the shape that actually
// arrives, not a trimmed one.
func claudeHookBody(sessionID, toolUseID string) string {
	return `{"session_id":"` + sessionID + `","tool_use_id":"` + toolUseID + `",` +
		`"cwd":"/tmp/work","transcript_path":"/tmp/t.jsonl","hook_event_name":"PreToolUse",` +
		`"permission_mode":"default","prompt_id":"p1","tool_name":"mcp__knowledge__search",` +
		`"tool_input":{"query":"x"}}`
}

// TestHookEndpoint_AcceptsAndStores is the accept row AND the 404-before/2xx-
// after transition: nothing but /mcp was mounted before, so an unmounted path
// falls to the mux's own 404 and a test must assert the transition explicitly.
func TestHookEndpoint_AcceptsAndStores(t *testing.T) {
	h := newHookTestServer()
	now := time.Now()

	if code := postHook(t, h, hookPathClaude, claudeHookBody("sess-a", "toolu_1"), ""); code != http.StatusNoContent {
		t.Fatalf("POST %s = %d, want 204", hookPathClaude, code)
	}
	d, ok := h.lookupHookDelivery(hookHarnessClaude, "toolu_1", now)
	if !ok {
		t.Fatal("a delivered hook is not retrievable by its tool_use_id")
	}
	if d.sessionID != "sess-a" || d.cwd != "/tmp/work" || d.transcriptPath != "/tmp/t.jsonl" {
		t.Errorf("stored delivery = %+v, want the delivered session_id/cwd/transcript_path", *d)
	}
	if _, ok := h.lookupHookDelivery(hookHarnessCodex, "toolu_1", now); ok {
		t.Error("a claude delivery is visible in the codex namespace; the harness is part of the key")
	}

	if code := postHook(t, h, hookPathCodex, claudeHookBody("sess-b", "call_1"), ""); code != http.StatusNoContent {
		t.Fatalf("POST %s = %d, want 204", hookPathCodex, code)
	}
	if _, ok := h.lookupHookDelivery(hookHarnessCodex, "call_1", now); !ok {
		t.Error("a codex delivery is not retrievable in the codex namespace")
	}

	// An unmounted sibling path still 404s: mounting the hook paths did not
	// widen the mux to everything.
	req := httptest.NewRequest(http.MethodPost, "/hook/unknown-harness", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST /hook/unknown-harness = %d, want 404", rec.Code)
	}
}

// TestHookEndpoint_Refusals is the error-arm matrix. Every arm asserts the
// NEGATIVE by a following lookup that MISSES, not by the status code alone: a
// refusal that still stored the delivery would pass a status-only assertion.
func TestHookEndpoint_Refusals(t *testing.T) {
	for _, tc := range []struct {
		name       string
		body       string
		remoteAddr string
		key        string
		wantCode   int
	}{
		{name: "not JSON", body: `not json at all`, key: "toolu_x", wantCode: http.StatusBadRequest},
		{name: "JSON but an array", body: `[{"session_id":"s","tool_use_id":"toolu_arr"}]`, key: "toolu_arr", wantCode: http.StatusBadRequest},
		{name: "JSON but a string", body: `"nope"`, key: "toolu_str", wantCode: http.StatusBadRequest},
		{name: "JSON null", body: `null`, key: "toolu_null", wantCode: http.StatusBadRequest},
		{name: "session_id absent", body: `{"tool_use_id":"toolu_nosess"}`, key: "toolu_nosess", wantCode: http.StatusBadRequest},
		{name: "session_id empty", body: `{"session_id":"","tool_use_id":"toolu_emptysess"}`, key: "toolu_emptysess", wantCode: http.StatusBadRequest},
		{name: "tool_use_id absent", body: `{"session_id":"s"}`, key: "", wantCode: http.StatusBadRequest},
		{name: "tool_use_id empty", body: `{"session_id":"s","tool_use_id":""}`, key: "", wantCode: http.StatusBadRequest},
		{
			name: "body over the size cap", key: "toolu_big", wantCode: http.StatusBadRequest,
			body: `{"session_id":"s","tool_use_id":"toolu_big","pad":"` + strings.Repeat("x", hookBodyMaxBytes+1) + `"}`,
		},
		{
			name: "non-loopback peer", body: claudeHookBody("s", "toolu_remote"),
			remoteAddr: "203.0.113.7:4242", key: "toolu_remote", wantCode: http.StatusForbidden,
		},
		{
			name: "unparseable peer address", body: claudeHookBody("s", "toolu_badaddr"),
			remoteAddr: "not-an-address", key: "toolu_badaddr", wantCode: http.StatusForbidden,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHookTestServer()
			if code := postHook(t, h, hookPathClaude, tc.body, tc.remoteAddr); code != tc.wantCode {
				t.Errorf("status = %d, want %d", code, tc.wantCode)
			}
			if tc.key != "" {
				if _, ok := h.lookupHookDelivery(hookHarnessClaude, tc.key, time.Now()); ok {
					t.Errorf("a REFUSED delivery was stored under %q", tc.key)
				}
			}
			if n := len(h.hooks); n != 0 {
				t.Errorf("a refused delivery left %d entries in the map, want 0", n)
			}
		})
	}
}

// TestHookEndpoint_RefusesNonPOST: the hook path answers only POST, and a
// refused method stores nothing.
func TestHookEndpoint_RefusesNonPOST(t *testing.T) {
	h := newHookTestServer()
	for _, method := range []string{http.MethodGet, http.MethodDelete, http.MethodPut} {
		req := httptest.NewRequest(method, hookPathClaude, nil)
		rec := httptest.NewRecorder()
		h.mux().ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405", method, hookPathClaude, rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != http.MethodPost {
			t.Errorf("%s Allow = %q, want POST", method, got)
		}
	}
	if n := len(h.hooks); n != 0 {
		t.Errorf("a refused method left %d entries in the map, want 0", n)
	}
}

// TestHookCorrelationMap_Transitions covers the state transitions: deliver then
// resolve (hit); deliver then wait past the TTL then resolve (miss); deliver
// twice under one id (one entry, latest wins).
func TestHookCorrelationMap_Transitions(t *testing.T) {
	h := newHookTestServer()
	base := time.Now()

	h.storeHookDelivery(hookPayload{SessionID: "s1", ToolUseID: "k"}, hookHarnessClaude, base)
	if _, ok := h.lookupHookDelivery(hookHarnessClaude, "k", base); !ok {
		t.Fatal("deliver → resolve: miss, want hit")
	}
	if _, ok := h.lookupHookDelivery(hookHarnessClaude, "k", base.Add(hookCorrelationTTL+time.Second)); ok {
		t.Error("deliver → past TTL → resolve: hit, want miss")
	}

	h.storeHookDelivery(hookPayload{SessionID: "s2", ToolUseID: "k"}, hookHarnessClaude, base)
	if len(h.hooks) != 1 {
		t.Errorf("re-delivery under one id made %d entries, want 1", len(h.hooks))
	}
	if d, _ := h.lookupHookDelivery(hookHarnessClaude, "k", base); d.sessionID != "s2" {
		t.Errorf("re-delivery session = %q, want the latest (s2)", d.sessionID)
	}
	if len(h.hookOrder) != 1 {
		t.Errorf("re-delivery grew the eviction order to %d, want 1", len(h.hookOrder))
	}
}

// TestHookCorrelationMap_SizeCap is the row the TTL alone cannot hold: a
// harness firing hooks for calls that are then DENIED grows the map between
// sweeps, so the cap must evict the oldest and keep the newest resolvable.
// Removing the cap and keeping only the TTL reds this test.
func TestHookCorrelationMap_SizeCap(t *testing.T) {
	h := newHookTestServer()
	now := time.Now()
	for i := range hookCorrelationMax + 10 {
		h.storeHookDelivery(hookPayload{SessionID: "s", ToolUseID: "k" + strconv.Itoa(i)}, hookHarnessClaude, now)
	}
	if len(h.hooks) != hookCorrelationMax {
		t.Errorf("map holds %d entries, want the cap %d", len(h.hooks), hookCorrelationMax)
	}
	if _, ok := h.lookupHookDelivery(hookHarnessClaude, "k0", now); ok {
		t.Error("the OLDEST delivery survived the cap")
	}
	newest := "k" + strconv.Itoa(hookCorrelationMax+9)
	if _, ok := h.lookupHookDelivery(hookHarnessClaude, newest, now); !ok {
		t.Errorf("the newest delivery %q was evicted", newest)
	}
}

// TestHookCorrelationMap_Sweep drives the sweep deterministically: only the
// expired entries go, the live ones stay resolvable, and the eviction order
// shrinks with the map rather than retaining dead keys.
func TestHookCorrelationMap_Sweep(t *testing.T) {
	h := newHookTestServer()
	base := time.Now()
	h.storeHookDelivery(hookPayload{SessionID: "old", ToolUseID: "k-old"}, hookHarnessClaude, base)
	h.storeHookDelivery(hookPayload{SessionID: "new", ToolUseID: "k-new"}, hookHarnessCodex, base.Add(hookCorrelationTTL))

	if n := h.reapHookDeliveries(base.Add(hookCorrelationTTL + time.Second)); n != 1 {
		t.Errorf("sweep evicted %d, want 1", n)
	}
	if _, ok := h.hooks[hookKey{harness: hookHarnessClaude, id: "k-old"}]; ok {
		t.Error("the expired delivery survived the sweep")
	}
	if _, ok := h.lookupHookDelivery(hookHarnessCodex, "k-new", base.Add(hookCorrelationTTL+time.Second)); !ok {
		t.Error("the live delivery was swept")
	}
	if len(h.hookOrder) != 1 {
		t.Errorf("eviction order holds %d keys after the sweep, want 1", len(h.hookOrder))
	}
}

// TestHookCorrelationMap_HarnessNamespacesDoNotAlias is the account-isolation
// row. Codex callIds are shaped exec-<uuid> and are only observed unique WITHIN
// a session, so two harnesses — or, once the id spaces are shared, two Codex
// sessions — presenting the same id inside one TTL window must not overwrite
// each other. A single namespace let the second delivery replace the first, and
// the first session's next call then resolved to the SECOND session's identity:
// a misbinding no later reader could detect.
func TestHookCorrelationMap_HarnessNamespacesDoNotAlias(t *testing.T) {
	h := newHookTestServer()
	now := time.Now()
	const shared = "exec-3e8c6f9f-71e7-442b-a834-7ffac3bff995"

	h.storeHookDelivery(hookPayload{SessionID: "session-A", ToolUseID: shared}, hookHarnessClaude, now)
	h.storeHookDelivery(hookPayload{SessionID: "session-B", ToolUseID: shared}, hookHarnessCodex, now)

	a, ok := h.lookupHookDelivery(hookHarnessClaude, shared, now)
	if !ok || a.sessionID != "session-A" {
		t.Errorf("claude namespace resolved %+v, want session-A — the second delivery aliased the first", a)
	}
	b, ok := h.lookupHookDelivery(hookHarnessCodex, shared, now)
	if !ok || b.sessionID != "session-B" {
		t.Errorf("codex namespace resolved %+v, want session-B", b)
	}
	if len(h.hooks) != 2 {
		t.Errorf("map holds %d entries, want 2 — one per harness namespace", len(h.hooks))
	}
}

// TestHookEndpoint_RefusesABrowserOriginatedDelivery: a page in the user's own
// browser can POST here — RemoteAddr is loopback because the browser runs here,
// and a text/plain body is a CORS SIMPLE request that no preflight can stop. A
// harness sends neither Origin nor Sec-Fetch-Site, so either one present is a
// refusal. The harness-shaped delivery in the same test is the known positive:
// the refusal is discriminating, not a blanket close.
func TestHookEndpoint_RefusesABrowserOriginatedDelivery(t *testing.T) {
	for _, tc := range []struct {
		name, header, value, key string
	}{
		{name: "Origin", header: "Origin", value: "https://fulminate.io", key: "toolu_origin"},
		{name: "Sec-Fetch-Site", header: "Sec-Fetch-Site", value: "cross-site", key: "toolu_sfs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHookTestServer()
			code := postHook(t, h, hookPathClaude, claudeHookBody("browser-sess", tc.key), "", tc.header, tc.value)
			if code != http.StatusForbidden {
				t.Errorf("browser-originated delivery = %d, want 403", code)
			}
			if _, ok := h.lookupHookDelivery(hookHarnessClaude, tc.key, time.Now()); ok {
				t.Errorf("a refused browser delivery was stored under %q", tc.key)
			}
			if n := len(h.hooks); n != 0 {
				t.Errorf("a refused browser delivery left %d entries in the map, want 0", n)
			}
		})
	}

	// KNOWN POSITIVE, same instrument and path: no Origin, no Sec-Fetch-Site.
	h := newHookTestServer()
	if code := postHook(t, h, hookPathClaude, claudeHookBody("harness-sess", "toolu_ok"), ""); code != http.StatusNoContent {
		t.Fatalf("harness-shaped delivery = %d, want 204 — the refusal is not discriminating", code)
	}
	if _, ok := h.lookupHookDelivery(hookHarnessClaude, "toolu_ok", time.Now()); !ok {
		t.Error("the harness-shaped delivery was not stored")
	}
}

// TestRunReaper_SweepsHookDeliveriesAtZeroIdleTTL is finding 4: the hook sweep
// rides the reaper goroutine, whose start used to be gated on idleTTL > 0. A
// zero SESSION ttl therefore silently disabled the HOOK sweep as well, leaving
// the correlation map to grow to its size cap and evict by age instead of
// expiring by time — two maps with two windows, one of them quietly turned off
// by a knob that names the other.
//
// It drives Run's REAL goroutine, because the defect was in the GATE and not in
// the sweep: calling reapHookDeliveries directly passes with the goroutine
// never started. Run binds an ephemeral loopback port (h.port is 0) — never
// 15022 or 15023, so no developer daemon is contacted.
func TestRunReaper_SweepsHookDeliveriesAtZeroIdleTTL(t *testing.T) {
	h := newHookTestServer() // idleTTL = 0
	if h.idleTTL != 0 {
		t.Fatalf("idleTTL = %v, want 0 — this row exists for the zero case", h.idleTTL)
	}
	h.sweepInterval = 10 * time.Millisecond
	h.storeHookDelivery(hookPayload{SessionID: "s", ToolUseID: "stale"},
		hookHarnessClaude, time.Now().Add(-2*hookCorrelationTTL))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- h.Run(ctx) }()

	deadline := time.After(10 * time.Second)
	for {
		h.hookMu.Lock()
		n := len(h.hooks)
		h.hookMu.Unlock()
		if n == 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the expired delivery was never swept at idleTTL=0: the sweeper is gated on the SESSION ttl")
		case err := <-served:
			t.Fatalf("Run returned early: %v", err)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-served
}

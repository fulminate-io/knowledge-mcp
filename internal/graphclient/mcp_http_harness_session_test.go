// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/session"
)

// resolveRequest builds a tools/call request carrying header and params and
// runs it through the resolver at now.
func resolveRequest(h *HTTPServer, header, params string, now time.Time) session.HarnessSession {
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
	if header != "" {
		req.Header.Set(knowledgeSessionHeader, header)
	}
	var raw json.RawMessage
	if params != "" {
		raw = json.RawMessage(params)
	}
	return h.resolveHarnessSession(req, raw, now)
}

// The `_meta` shapes each harness sends, spelled once. The keys come from the
// kgtools.CallToolMeta json tags, so a change to those tags moves these with it.
const (
	claudeMeta   = `{"_meta":{"claudecode/toolUseId":"toolu_p","progressToken":1}}`
	codexMeta    = `{"_meta":{"callId":"call_p","threadId":"th","x-codex-turn-metadata":{"session_id":"codex-sess"}}}`
	codexNoTurn  = `{"_meta":{"callId":"call_p"}}`
	codexEmptyID = `{"_meta":{"callId":"call_p","x-codex-turn-metadata":{"session_id":"  "}}}`
	// The turn metadata AGREES with the hook delivery this file stores
	// ("hook-sess"), which is the healthy Codex shape: two carriers, one answer.
	codexMetaAgreeing = `{"_meta":{"callId":"call_p","x-codex-turn-metadata":{"session_id":"hook-sess"}}}`
)

// TestResolveHarnessSession_PrecedenceMatrix walks every cell of
// {header set, unset} × {hook delivered, not} × {codex _meta present, absent},
// stating the resolved id AND the source in each. The cells that must not be
// skipped: the header outranks both carriers, and a hook delivery outranks
// Codex's own `_meta` (the owner ruling, which the ticket body orders the other
// way round).
func TestResolveHarnessSession_PrecedenceMatrix(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name       string
		header     string
		params     string
		deliver    string // tool-call id to pre-deliver a hook for, "" for none
		harness    string
		wantID     string
		wantSource string
		wantReason string
	}{
		{
			name: "header + hook + codex meta → header wins", header: "hdr-sess", params: codexMeta,
			deliver: "call_p", harness: hookHarnessCodex, wantID: "hdr-sess", wantSource: session.HarnessSourceHeader,
		},
		{
			name: "header + claude meta, no hook → header wins", header: "hdr-sess", params: claudeMeta,
			wantID: "hdr-sess", wantSource: session.HarnessSourceHeader,
		},
		{
			name: "no header + codex hook + codex meta → the HOOK, not codex-meta", params: codexMeta,
			deliver: "call_p", harness: hookHarnessCodex, wantID: "hook-sess", wantSource: session.HarnessSourceCodexHook,
			wantReason: session.ReasonHookMetaConflict,
		},
		{
			name: "codex hook agreeing with codex meta → the hook, and NO conflict reason", params: codexMetaAgreeing,
			deliver: "call_p", harness: hookHarnessCodex, wantID: "hook-sess", wantSource: session.HarnessSourceCodexHook,
		},
		{
			name: "no header + claude hook → claude-hook", params: claudeMeta,
			deliver: "toolu_p", harness: hookHarnessClaude, wantID: "hook-sess", wantSource: session.HarnessSourceClaudeHook,
		},
		{
			name: "no header + no hook + codex meta → codex-meta", params: codexMeta,
			wantID: "codex-sess", wantSource: session.HarnessSourceCodexMeta,
		},
		{
			name: "no header + no hook + no meta → none", params: `{"name":"search","arguments":{}}`,
			wantSource: session.HarnessSourceNone,
		},
		{
			name: "claude toolUseId but no delivery → none", params: claudeMeta,
			wantSource: session.HarnessSourceNone,
		},
		{
			name: "codex meta present but turn metadata absent → none", params: codexNoTurn,
			wantSource: session.HarnessSourceNone,
		},
		{
			name: "codex turn metadata session_id blank → none", params: codexEmptyID,
			wantSource: session.HarnessSourceNone,
		},
		{name: "empty-string header falls through to meta", header: "", params: codexMeta, wantID: "codex-sess", wantSource: session.HarnessSourceCodexMeta},
		{name: "whitespace-only header falls through to meta", header: "   ", params: codexMeta, wantID: "codex-sess", wantSource: session.HarnessSourceCodexMeta},
		{name: "no params at all → none", params: "", wantSource: session.HarnessSourceNone},
		{name: "unparseable params → none, not a crash", params: `{{{`, wantSource: session.HarnessSourceNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHookTestServer()
			if tc.deliver != "" {
				h.storeHookDelivery(hookPayload{SessionID: "hook-sess", ToolUseID: tc.deliver}, tc.harness, now)
			}
			got := resolveRequest(h, tc.header, tc.params, now)
			if got.ID != tc.wantID {
				t.Errorf("id = %q, want %q", got.ID, tc.wantID)
			}
			if got.Source != tc.wantSource {
				t.Errorf("source = %q, want %q", got.Source, tc.wantSource)
			}
			if tc.wantReason != "" && !strings.Contains(got.Reason, tc.wantReason) {
				t.Errorf("reason = %q, want it to name %q", got.Reason, tc.wantReason)
			}
			if tc.wantReason == "" && strings.Contains(got.Reason, session.ReasonHookMetaConflict) {
				t.Errorf("reason = %q wrongly reports a hook/_meta conflict", got.Reason)
			}
		})
	}
}

// TestResolveHarnessSession_HeaderOnANonToolsCallMethod: the explicit header is
// honored whatever the method — it is an assertion by the caller, not a
// property of a tools/call params blob.
func TestResolveHarnessSession_HeaderOnANonToolsCallMethod(t *testing.T) {
	h := newHookTestServer()
	got := resolveRequest(h, "hdr-sess", `{"cursor":""}`, time.Now())
	if got.ID != "hdr-sess" || got.Source != session.HarnessSourceHeader {
		t.Errorf("resolution = %+v, want the header session", got)
	}
}

// TestResolveHarnessSession_NoFallback is the owner's "no fallback" row. In the
// `none` cell the resolved id must be EMPTY and must not be any of the values
// the daemon HAS to hand — the minted transport session id or the session's
// peer workspace cwd — so this asserts against specific known-wrong values
// rather than making a vacuous non-empty check.
func TestResolveHarnessSession_NoFallback(t *testing.T) {
	h := newHookTestServer()
	const mintedID, peerCwd = "minted-transport-session", "/tmp/peer/workspace"
	h.ensureSession(mintedID, peerCwd)

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
	req.Header.Set(mcpSessionHeader, mintedID)
	got := h.resolveHarnessSession(req, json.RawMessage(`{"name":"search"}`), time.Now())

	if got.ID != "" {
		t.Errorf("unresolved id = %q, want empty", got.ID)
	}
	for _, wrong := range []string{mintedID, peerCwd} {
		if got.ID == wrong {
			t.Errorf("unresolved resolution fell back to %q", wrong)
		}
	}
	if got.Source != session.HarnessSourceNone {
		t.Errorf("source = %q, want none", got.Source)
	}
	if got.Resolved() {
		t.Error("an unresolved session reports Resolved() true")
	}
}

// TestResolveHarnessSession_ReasonVocabulary pins the four closed reasons, each
// to the state it names. Collapsing them into one generic "unresolved" reds
// every row here — which is the point: a user must be able to tell "you sent no
// header" from "your Claude hook is not firing" from "your Codex hook is
// installed but untrusted".
func TestResolveHarnessSession_ReasonVocabulary(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name     string
		params   string
		deliver  string
		wantHas  []string
		wantNone []string
	}{
		{
			name: "no header and no _meta", params: `{"name":"search"}`,
			wantHas:  []string{session.ReasonNoHeader, session.ReasonNoMeta},
			wantNone: []string{session.ReasonNoClaudeHook, session.ReasonCodexHookInert},
		},
		{
			name: "claude toolUseId with no hook delivery", params: claudeMeta,
			wantHas:  []string{session.ReasonNoHeader, session.ReasonNoClaudeHook},
			wantNone: []string{session.ReasonNoMeta, session.ReasonCodexHookInert},
		},
		{
			name: "codex resolved by _meta with no hook delivery", params: codexMeta,
			wantHas:  []string{session.ReasonCodexHookInert},
			wantNone: []string{session.ReasonNoMeta, session.ReasonNoClaudeHook},
		},
		{
			// Codex DID send `_meta` — it named a callId — but the turn
			// metadata carried no session_id. Saying "no _meta" here points a
			// user at the wrong half of their install, and the turn-metadata
			// key is undocumented so its disappearance is a shape to expect.
			name: "codex callId with no turn metadata at all", params: codexNoTurn,
			wantHas:  []string{session.ReasonNoHeader, session.ReasonNoCodexTurn},
			wantNone: []string{session.ReasonNoMeta, session.ReasonNoClaudeHook, session.ReasonCodexHookInert},
		},
		{
			name: "codex callId whose turn metadata session_id is blank", params: codexEmptyID,
			wantHas:  []string{session.ReasonNoHeader, session.ReasonNoCodexTurn},
			wantNone: []string{session.ReasonNoMeta, session.ReasonNoClaudeHook, session.ReasonCodexHookInert},
		},
		{
			// The hook and the turn metadata name DIFFERENT sessions. The hook
			// wins by the owner's precedence; the disagreement is reported,
			// because two carriers that should always agree and do not is a
			// fact about the install and the resolution is one nobody can
			// check afterwards.
			name: "codex hook and _meta disagree", params: codexMeta, deliver: "call_p",
			wantHas:  []string{session.ReasonHookMetaConflict},
			wantNone: []string{session.ReasonNoMeta, session.ReasonCodexHookInert},
		},
		{
			name: "codex resolved by its hook, agreeing with _meta", params: codexMetaAgreeing, deliver: "call_p",
			wantNone: []string{
				session.ReasonHookMetaConflict,
				session.ReasonNoHeader, session.ReasonNoMeta,
				session.ReasonNoClaudeHook, session.ReasonCodexHookInert,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHookTestServer()
			if tc.deliver != "" {
				h.storeHookDelivery(hookPayload{SessionID: "hook-sess", ToolUseID: tc.deliver}, hookHarnessCodex, now)
			}
			got := resolveRequest(h, "", tc.params, now).Reason
			for _, want := range tc.wantHas {
				if !strings.Contains(got, want) {
					t.Errorf("reason %q does not name %q", got, want)
				}
			}
			for _, unwanted := range tc.wantNone {
				if strings.Contains(got, unwanted) {
					t.Errorf("reason %q wrongly names %q", got, unwanted)
				}
			}
		})
	}
}

// TestResolveHarnessSession_ConcurrentSessions is R5's in-process row: two
// harness sessions delivering under two different tool-call ids resolve to two
// different session ids, driven CONCURRENTLY so the map's locking is exercised
// and not just its contents. The same session under a NEW tool-call id still
// resolves to that session — the map is keyed on the CALL, not the session.
func TestResolveHarnessSession_ConcurrentSessions(t *testing.T) {
	h := newHookTestServer()
	now := time.Now()
	pairs := map[string]string{"toolu_A": "sess-workA", "toolu_B": "sess-workB", "toolu_A2": "sess-workA"}

	var wg sync.WaitGroup
	for key, sess := range pairs {
		wg.Go(func() {
			h.storeHookDelivery(hookPayload{SessionID: sess, ToolUseID: key}, hookHarnessClaude, now)
		})
	}
	wg.Wait()

	var mu sync.Mutex
	got := map[string]string{}
	wg = sync.WaitGroup{}
	for key := range pairs {
		wg.Go(func() {
			hs := resolveRequest(h, "", `{"_meta":{"claudecode/toolUseId":"`+key+`"}}`, now)
			mu.Lock()
			got[key] = hs.ID
			mu.Unlock()
		})
	}
	wg.Wait()

	for key, want := range pairs {
		if got[key] != want {
			t.Errorf("tool-call %s resolved to %q, want %q", key, got[key], want)
		}
	}
	if got["toolu_A"] == got["toolu_B"] {
		t.Errorf("two concurrent sessions resolved to the same id %q", got["toolu_A"])
	}
}

// TestHookDeliveryThenToolCall_OverTheRealTransport is requirement 7's
// end-to-end row on the REAL MCP HTTP stack: a hook delivery POSTed to the hook
// path on the SAME httptest server, then a tools/call carrying the matching
// _meta, with the resolved session read off the dispatch ctx. The negative is
// the same run with no delivery.
func TestHookDeliveryThenToolCall_OverTheRealTransport(t *testing.T) {
	var mu sync.Mutex
	var seen session.HarnessSession
	m := NewMCPClient(MCPClientConfig{
		Version: "test",
		// LoggedIn true skips the EnsureServer gate: this row is about the
		// TRANSPORT carrying a resolved session to the dispatcher, not about
		// starting a local graph server.
		LoggedIn: func(context.Context) bool { return true },
		Dispatch: func(ctx context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
			mu.Lock()
			seen = session.HarnessSessionFromContext(ctx)
			mu.Unlock()
			return kgtools.TextResult("ok"), nil
		},
	})
	h := NewHTTPServer(m, 0, nil)
	h.idleTTL = 0
	srv := httptest.NewServer(h.mux())
	t.Cleanup(srv.Close)

	mcpSession := realStackInitialize(t, srv.URL)

	t.Run("delivery then call resolves the hook's session", func(t *testing.T) {
		postJSON(t, srv.URL+hookPathClaude, claudeHookBody("real-sess", "toolu_real"), "")
		realStackToolCall(t, srv.URL, mcpSession, `{"name":"search","arguments":{},"_meta":{"claudecode/toolUseId":"toolu_real"}}`)
		mu.Lock()
		defer mu.Unlock()
		if seen.ID != "real-sess" || seen.Source != session.HarnessSourceClaudeHook {
			t.Errorf("dispatch ctx carried %+v, want real-sess via claude-hook", seen)
		}
	})

	t.Run("no delivery resolves to none over the same transport", func(t *testing.T) {
		realStackToolCall(t, srv.URL, mcpSession, `{"name":"search","arguments":{},"_meta":{"claudecode/toolUseId":"toolu_undelivered"}}`)
		mu.Lock()
		defer mu.Unlock()
		if seen.ID != "" || seen.Source != session.HarnessSourceNone {
			t.Errorf("dispatch ctx carried %+v, want an empty id with source none", seen)
		}
		if !strings.Contains(seen.Reason, session.ReasonNoClaudeHook) {
			t.Errorf("reason %q does not name the undelivered hook", seen.Reason)
		}
	})
}

// postJSON POSTs body to url with the MCP session header when one is given. It
// drives the REAL transport (a net/http client against an httptest server),
// which is what makes these rows an end-to-end observation rather than a
// handler-direct one.
//
// It returns nothing, for the same reason realStackToolCall does: every row
// here asserts on what the DISPATCHER saw on its context, never on the response
// body, and a transport failure already fails the test inside. Returning a body
// nobody reads would invite the next author to start reading it against a shape
// no row ever asserted.
func postJSON(t *testing.T, url, body, mcpSession string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(body)) //nolint:gosec // G704: url is the httptest server this test started
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if mcpSession != "" {
		req.Header.Set(mcpSessionHeader, mcpSession)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode >= 400 {
		t.Fatalf("POST %s = %d: %s", url, resp.StatusCode, out)
	}
}

// realStackInitialize completes the MCP handshake against base and returns the
// minted Mcp-Session-Id every later request must carry.
func realStackInitialize(t *testing.T, base string) string {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",` +
		`"capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/mcp", strings.NewReader(body)) //nolint:gosec // G704: base is the httptest server this test started
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	id := resp.Header.Get(mcpSessionHeader)
	if id == "" {
		t.Fatal("initialize minted no Mcp-Session-Id")
	}
	return id
}

// realStackToolCall posts a tools/call whose params blob is given verbatim, so a
// row can spell the exact `_meta` shape the harness sends. It returns nothing:
// these rows assert on what the DISPATCHER saw on its context, never on the
// tool result, and postJSON already fails the test on a transport error.
func realStackToolCall(t *testing.T, base, mcpSession, params string) {
	t.Helper()
	postJSON(t, base+"/mcp", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":`+params+`}`, mcpSession)
}

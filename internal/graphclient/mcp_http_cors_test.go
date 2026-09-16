// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// newCORSTestServer builds an HTTPServer with an explicit single-origin
// allow-list (https://fulminate.io) and the reaper disabled, for driving the
// composed corsMiddleware(mux) handler through httptest.
func newCORSTestServer() *HTTPServer {
	return newCORSTestServerForOrigins("https://fulminate.io")
}

// newCORSTestServerForOrigins is the same fixture over a caller-chosen
// allow-list, so a case can drive the SECOND shipped web origin through the
// same composed handler rather than assuming the two behave alike.
func newCORSTestServerForOrigins(origins ...string) *HTTPServer {
	mc := NewMCPClient(MCPClientConfig{Version: "test"})
	h := NewHTTPServer(mc, 15023, origins)
	h.idleTTL = 0
	return h
}

// preflightAllowHeaders drives one OPTIONS preflight from origin and returns
// the status and the advertised Access-Control-Allow-Headers value.
func preflightAllowHeaders(t *testing.T, h *HTTPServer, origin string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodOptions, "/mcp", nil)
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type,knowledge-account-id")
	rec := httptest.NewRecorder()
	h.corsMiddleware(h.mux()).ServeHTTP(rec, req)
	return rec.Code, rec.Header().Get("Access-Control-Allow-Headers")
}

// TestCORSPreflightAdmitsTheAccountHeader is requirement 1's CORS row: the
// preflight for BOTH shipped web origins advertises the inbound account header
// by its one canonical name, and an origin outside the allow-list is advertised
// nothing at all — the negative control that keeps a green row from coming from
// an unconditional advertisement.
func TestCORSPreflightAdmitsTheAccountHeader(t *testing.T) {
	const dev = "https://dev.fulminate.io"
	const prod = "https://fulminate.io"
	h := newCORSTestServerForOrigins(prod, dev)

	for _, origin := range []string{prod, dev} {
		t.Run("allowed origin "+origin, func(t *testing.T) {
			status, allow := preflightAllowHeaders(t, h, origin)
			if status != http.StatusNoContent {
				t.Fatalf("preflight status = %d, want %d", status, http.StatusNoContent)
			}
			if !strings.Contains(allow, auth.AccountHeaderName) {
				t.Fatalf("Allow-Headers = %q, want it to contain %q so a browser page may send the account header",
					allow, auth.AccountHeaderName)
			}
		})
	}

	t.Run("disallowed origin is advertised nothing", func(t *testing.T) {
		status, allow := preflightAllowHeaders(t, h, "https://evil.example")
		if status != http.StatusNoContent {
			t.Fatalf("preflight status = %d, want %d", status, http.StatusNoContent)
		}
		if allow != "" {
			t.Fatalf("Allow-Headers = %q for a disallowed origin, want no header at all", allow)
		}
	})
}

// TestCORSPreflightAndSimpleRequest exercises the composed CORS+mux handler:
// the OPTIONS preflight short-circuits 204 with the full CORS+PNA header set,
// a simple cross-origin request carries ACAO + Expose-Headers, and a
// disallowed/absent origin never receives an Access-Control-Allow-Origin
// header (and '*' is never emitted under any path).
func TestCORSPreflightAndSimpleRequest(t *testing.T) {
	h := newCORSTestServer()
	handler := h.corsMiddleware(h.mux())

	t.Run("preflight allowed origin -> 204 with full CORS+PNA headers", func(t *testing.T) {
		req := httptest.NewRequest("OPTIONS", "/mcp", nil)
		req.Header.Set("Origin", "https://fulminate.io")
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "content-type,mcp-session-id")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != 204 {
			t.Fatalf("preflight status = %d, want 204", rec.Code)
		}
		hdr := rec.Header()
		if got := hdr.Get("Access-Control-Allow-Origin"); got != "https://fulminate.io" {
			t.Fatalf("ACAO = %q, want exact reflected origin https://fulminate.io (never '*')", got)
		}
		if got := hdr.Get("Access-Control-Allow-Origin"); got == "*" {
			t.Fatal("ACAO must never be '*'")
		}
		if m := hdr.Get("Access-Control-Allow-Methods"); !strings.Contains(m, "POST") || !strings.Contains(m, "OPTIONS") {
			t.Fatalf("Allow-Methods = %q, want POST and OPTIONS", m)
		}
		if ah := hdr.Get("Access-Control-Allow-Headers"); !strings.Contains(ah, "Mcp-Session-Id") {
			t.Fatalf("Allow-Headers = %q, want Mcp-Session-Id", ah)
		}
		if ah := hdr.Get("Access-Control-Allow-Headers"); !strings.Contains(ah, "Mcp-Protocol-Version") {
			t.Fatalf("Allow-Headers = %q, want Mcp-Protocol-Version (valid MCP-spec header, kept deliberately)", ah)
		}
		// The explicit harness-session header: a browser client cannot send it
		// unless the preflight advertises it, so this line is what makes
		// requirement 3(a) reachable from the web app at all.
		if ah := hdr.Get("Access-Control-Allow-Headers"); !strings.Contains(ah, knowledgeSessionHeader) {
			t.Fatalf("Allow-Headers = %q, want %s", ah, knowledgeSessionHeader)
		}
		if pna := hdr.Get("Access-Control-Allow-Private-Network"); pna != "true" {
			t.Fatalf("Allow-Private-Network = %q, want true", pna)
		}
		if v := hdr.Get("Vary"); !strings.Contains(v, "Origin") {
			t.Fatalf("Vary = %q, want Origin", v)
		}
	})

	t.Run("simple cross-origin request -> ACAO + Expose-Headers", func(t *testing.T) {
		// A GET with an allowed Origin: the middleware sets the CORS headers
		// before delegating to the mux (which 404s for the missing session) —
		// the CORS headers are what this case asserts, not the body status.
		req := httptest.NewRequest("GET", "/mcp", nil)
		req.Header.Set("Origin", "https://fulminate.io")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		hdr := rec.Header()
		if got := hdr.Get("Access-Control-Allow-Origin"); got != "https://fulminate.io" {
			t.Fatalf("simple-request ACAO = %q, want https://fulminate.io", got)
		}
		if eh := hdr.Get("Access-Control-Expose-Headers"); !strings.Contains(eh, "Mcp-Session-Id") {
			t.Fatalf("Expose-Headers = %q, want Mcp-Session-Id (browser must read minted session id)", eh)
		}
	})

	t.Run("disallowed origin -> no ACAO, never '*'", func(t *testing.T) {
		for _, method := range []string{"OPTIONS", "GET"} {
			req := httptest.NewRequest(method, "/mcp", nil)
			req.Header.Set("Origin", "https://evil.example")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
				t.Fatalf("%s disallowed origin: ACAO = %q, want empty (no header)", method, got)
			}
		}
	})

	t.Run("no origin -> no ACAO header", func(t *testing.T) {
		req := httptest.NewRequest("OPTIONS", "/mcp", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		// OPTIONS still short-circuits 204, but with no Origin there are no
		// Access-Control-* headers.
		if rec.Code != 204 {
			t.Fatalf("no-origin OPTIONS status = %d, want 204", rec.Code)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("no-origin: ACAO = %q, want empty", got)
		}
		if got := rec.Header().Get("Access-Control-Allow-Private-Network"); got != "" {
			t.Fatalf("no-origin: Allow-Private-Network = %q, want empty (not an allowed-origin preflight)", got)
		}
	})
}

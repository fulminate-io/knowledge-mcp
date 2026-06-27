// SPDX-License-Identifier: Apache-2.0

package graphclient

import "net/http"

// CORS + Private-Network-Access header names and the fixed preflight response
// values. Hoisted to package consts so corsMiddleware sets them without
// per-request literals. The header NAMES are spelled here once; the
// Mcp-Session-Id value reuses the existing mcpSessionHeader const (mcp_http.go)
// rather than re-literaling it.
const (
	headerOrigin                = "Origin"
	headerVary                  = "Vary"
	headerACAllowOrigin         = "Access-Control-Allow-Origin"
	headerACAllowMethods        = "Access-Control-Allow-Methods"
	headerACAllowHeaders        = "Access-Control-Allow-Headers"
	headerACExposeHeaders       = "Access-Control-Expose-Headers"
	headerACAllowPrivateNetwork = "Access-Control-Allow-Private-Network"
	headerACMaxAge              = "Access-Control-Max-Age"

	// corsAllowMethods is the preflight-advertised method set — the /mcp mux
	// switches POST/GET/DELETE and the middleware itself answers OPTIONS.
	corsAllowMethods = "GET, POST, DELETE, OPTIONS"

	// corsMaxAge caches a successful preflight for 10 minutes so a browser does
	// not re-OPTIONS every request in a session.
	corsMaxAge = "600"
)

// corsAllowHeaders is the request-header allow-list advertised on a successful
// preflight: Content-Type (JSON-RPC body), Mcp-Session-Id (the stateful session
// header the daemon mints + requires), Mcp-Protocol-Version (a valid MCP-spec
// header browser clients may send — kept deliberately, NOT dead), and Accept.
// mcpSessionHeader is reused for the session header name (no re-literal).
var corsAllowHeaders = "Content-Type, " + mcpSessionHeader + ", Mcp-Protocol-Version, Accept"

// corsMiddleware wraps the /mcp handler with restricted CORS + Chrome
// Private-Network-Access handling so a browser page served from an allowed
// public https origin can fetch the loopback daemon.
//
// Origin handling is RESTRICTED reflection: an Origin present in
// h.allowedOrigins is echoed verbatim into Access-Control-Allow-Origin (with
// Vary: Origin so a shared cache keys on it) — the response is NEVER widened to
// '*', and an Origin absent from the set (or no Origin at all) gets no
// Access-Control-* headers, so a non-browser/same-origin caller is unaffected
// and an unauthorized cross-origin browser read is denied by the browser.
//
//   - OPTIONS preflight: short-circuits with 204 No Content. When the Origin is
//     allowed it additionally advertises Allow-Methods, Allow-Headers,
//     Access-Control-Allow-Private-Network: true (the Chrome PNA requirement for
//     a public-origin → loopback request) and Max-Age; it never falls through to
//     the mux (which would 405 the OPTIONS).
//   - non-OPTIONS allowed-origin: also sets Access-Control-Expose-Headers:
//     Mcp-Session-Id so the browser fetch() can READ the minted session id off
//     the initialize response — the stateful MCP handshake breaks cross-origin
//     without it — then delegates to next.
//
// Deliberately does NOT set Access-Control-Allow-Credentials: daemon auth is the
// server-side keychain, not browser cookies, so the fetch is non-credentialed
// and reflecting a specific origin without credentials is the correct posture.
func (h *HTTPServer) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get(headerOrigin)
		_, allowed := h.allowedOrigins[origin]
		allowed = allowed && origin != ""

		if allowed {
			w.Header().Set(headerACAllowOrigin, origin)
			w.Header().Add(headerVary, headerOrigin)
		}

		if r.Method == http.MethodOptions {
			// CORS preflight: answer here, never fall through to the mux (it
			// would 405 the OPTIONS). The Allow-* / PNA headers are only
			// meaningful — and only emitted — for an allowed origin.
			if allowed {
				w.Header().Set(headerACAllowMethods, corsAllowMethods)
				w.Header().Set(headerACAllowHeaders, corsAllowHeaders)
				w.Header().Set(headerACAllowPrivateNetwork, "true")
				w.Header().Set(headerACMaxAge, corsMaxAge)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if allowed {
			// Let the cross-origin fetch() read the minted session id off the
			// initialize response — required for the stateful handshake.
			w.Header().Set(headerACExposeHeaders, mcpSessionHeader)
		}

		next.ServeHTTP(w, r)
	})
}

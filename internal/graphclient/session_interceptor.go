// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"

	"connectrpc.com/connect"

	"github.com/fulminate-io/knowledge-mcp/internal/session"
)

// harnessSessionHeader carries the caller's HARNESS session-id — the daemon-
// resolved, agent-uncontrolled identity the server keys a hive member on.
// The server declares its own copy of this constant; the two declarations
// must stay in sync.
//
// It is deliberately NOT mcpSessionHeader: that header names the daemon's own
// inbound streamable-HTTP session and carries a different value on a different
// hop.
const harnessSessionHeader = "Knowledge-Harness-Session-Id"

// newSessionInterceptor returns the outbound interceptor that carries the
// harness session-id from the dispatch context onto every outbound RPC as a
// header. connect serializes messages and headers, never context values, so an
// identity resolved on the daemon reaches the server only by being written here.
//
// There is exactly ONE of these, installed on every constructed client, for the
// reason the operation stamper states about itself: "which RPCs get labeled"
// must not be a per-call-site decision.
//
// It is a context read plus a header write — no I/O. The resolution that
// produces the value happens once per MCP session in handlePOST, behind the
// per-session cache.
//
// When the context carries no harness id the interceptor sets NO header at all,
// rather than an empty one. That branch is live, not defensive: a session whose
// transcript has not resolved yet, the daemon's own hive loops (which name their
// target through the request's member_session field instead), and a dream worker
// (which has no transcript) all issue RPCs on a context with no harness stamp.
func newSessionInterceptor() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if hid := session.HarnessSessionIDFromContext(ctx); hid != "" {
				req.Header().Set(harnessSessionHeader, hid)
			}
			return next(ctx, req)
		}
	})
}

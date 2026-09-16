// SPDX-License-Identifier: Apache-2.0

// mcp_http_harness_session.go — resolving WHICH harness session made a request.
//
// THE PRECEDENCE, in order, and nothing else is ever consulted:
//
//  1. an explicit Knowledge-Session-Id request header;
//  2. a PreToolUse hook delivery correlated by the request's own per-call id —
//     Claude's _meta["claudecode/toolUseId"], Codex's _meta.callId;
//  3. Codex's _meta["x-codex-turn-metadata"].session_id, read directly;
//  4. none.
//
// The hook arm outranks Codex's `_meta` by owner ruling. For CODEX that ordering
// is a carrier preference, not a reliability ladder: a Codex hook is INERT until
// the user trusts it in the TUI's /hooks and Codex says nothing when it skips
// one, so `codex-meta` is the steady state of a fresh install and `codex-hook`
// the state after trust. The difference is REPORTED (see session.Reason* and
// manage(status)), never papered over.
//
// NO FALLBACK (owner, verbatim). On `none` nothing is inferred: not the minted
// Mcp-Session-Id, not the peer workspace cwd, not a transcript. The peer cwd
// keeps its own production role — routing code-graph calls to the right repo —
// and that role is untouched here; it is simply never a stand-in for identity.

package graphclient

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/session"
)

// knowledgeSessionHeader is the explicit harness-session header, the highest
// arm of the precedence. It is named once here and reused by the CORS
// allow-list (mcp_http_cors.go) so a browser preflight admits the same spelling
// the resolver reads.
const knowledgeSessionHeader = "Knowledge-Session-Id"

// callMetaEnvelope is the narrow decode of a tools/call params blob: the `_meta`
// sidecar and nothing else. handlePOST holds only the raw params (dispatchToolCall
// is what decodes the full CallToolParams downstream), and it is also the only
// frame holding r.Header — which the explicit-header arm needs — so the second,
// narrow decode happens HERE rather than threading a header into dispatch.
type callMetaEnvelope struct {
	Meta *kgtools.CallToolMeta `json:"_meta"`
}

// resolveHarnessSession resolves the harness session for one request, walking
// the precedence above. now is passed in so the hook arm's TTL decision is
// deterministic under test.
//
// params is the raw JSON-RPC params blob; a blob that does not decode is
// treated as carrying no `_meta` (a malformed params is the dispatcher's error
// to report, not this resolver's to crash on).
func (h *HTTPServer) resolveHarnessSession(r *http.Request, params json.RawMessage, now time.Time) session.HarnessSession {
	if id := strings.TrimSpace(r.Header.Get(knowledgeSessionHeader)); id != "" {
		return session.HarnessSession{ID: id, Source: session.HarnessSourceHeader}
	}
	meta := decodeCallMeta(params)
	if hs, ok := h.resolveFromHook(meta, now); ok {
		return hs
	}
	if codexID := codexMetaSessionID(meta); codexID != "" {
		// Codex named itself, but no hook delivery was correlated for this
		// call. Report that state rather than presenting a clean resolution:
		// on a fresh install it means the hook is installed and untrusted.
		return session.HarnessSession{
			ID:     codexID,
			Source: session.HarnessSourceCodexMeta,
			Reason: session.ReasonCodexHookInert,
		}
	}
	return session.HarnessSession{Source: session.HarnessSourceNone, Reason: unresolvedReason(meta)}
}

// resolveFromHook looks the request's own per-call id up in the hook-fed map,
// in ITS OWN HARNESS'S NAMESPACE. Which namespace is decided by which `_meta`
// key the request carries — Claude sends claudecode/toolUseId and Codex sends
// callId, never both — so the harness is read off the request rather than
// guessed, and the two id spaces cannot collide (see hookKey).
func (h *HTTPServer) resolveFromHook(meta *kgtools.CallToolMeta, now time.Time) (session.HarnessSession, bool) {
	if meta == nil {
		return session.HarnessSession{}, false
	}
	for _, c := range []struct {
		harness string
		id      string
		source  string
	}{
		{hookHarnessClaude, meta.ClaudeToolUseID, session.HarnessSourceClaudeHook},
		{hookHarnessCodex, meta.CallID, session.HarnessSourceCodexHook},
	} {
		d, ok := h.lookupHookDelivery(c.harness, c.id, now)
		if !ok {
			continue
		}
		return session.HarnessSession{
			ID:             d.sessionID,
			Source:         c.source,
			Reason:         hookMetaConflictReason(d.sessionID, meta),
			Cwd:            d.cwd,
			TranscriptPath: d.transcriptPath,
		}, true
	}
	return session.HarnessSession{}, false
}

// hookMetaConflictReason reports the disagreement when a Codex call carries a
// correlated hook delivery AND turn metadata that names a DIFFERENT session.
//
// The hook wins by the owner's precedence and that is not in question; what is
// in question is whether the loser is worth mentioning. It is: the two carriers
// are independent readings of one fact and are observed to agree, so a
// disagreement means one of them is wrong about which session is calling, and
// the resolution that comes out is the one no later reader can check. Reporting
// it costs a string on a call that is already degraded; dropping it makes an
// account bound to the wrong session indistinguishable from a healthy one.
//
// Empty (no reason) is the overwhelmingly common case: no turn metadata, or the
// two agreeing.
func hookMetaConflictReason(hookSessionID string, meta *kgtools.CallToolMeta) string {
	metaID := codexMetaSessionID(meta)
	if metaID == "" || metaID == hookSessionID {
		return ""
	}
	return session.ReasonHookMetaConflict
}

// unresolvedReason names every carrier that was missing, joined with "; ". The
// header is always named (the resolver got here, so it was absent); the second
// clause names WHICH carrier fell short, and the three cases are deliberately
// distinct because manage(status) reports the string verbatim and a user reads
// it to decide which half of their install to look at:
//
//   - a Claude tool-call id with no hook delivery — a hook not installed or not
//     firing;
//   - a Codex callId whose turn metadata carried no session_id — Codex DID send
//     `_meta`, so saying otherwise sends the user hunting the wrong thing;
//   - neither — the request carried no session carrier at all.
func unresolvedReason(meta *kgtools.CallToolMeta) string {
	switch {
	case meta != nil && meta.ClaudeToolUseID != "":
		return session.ReasonNoHeader + "; " + session.ReasonNoClaudeHook
	case meta != nil && meta.CallID != "":
		return session.ReasonNoHeader + "; " + session.ReasonNoCodexTurn
	default:
		return session.ReasonNoHeader + "; " + session.ReasonNoMeta
	}
}

// decodeCallMeta pulls the `_meta` sidecar out of a params blob, returning nil
// when the blob is empty, undecodable, or carries no `_meta`. All three are
// ordinary shapes: initialize and tools/list carry no session carrier at all.
func decodeCallMeta(params json.RawMessage) *kgtools.CallToolMeta {
	if len(params) == 0 {
		return nil
	}
	var env callMetaEnvelope
	if err := json.Unmarshal(params, &env); err != nil {
		return nil
	}
	return env.Meta
}

// codexMetaSessionID returns the Codex turn metadata's session_id, or "" when
// the turn metadata is absent or carries an empty one. Both are expected shapes:
// the field is undocumented and may disappear, and an empty value is not an
// identity.
func codexMetaSessionID(meta *kgtools.CallToolMeta) string {
	if meta == nil || meta.CodexTurn == nil {
		return ""
	}
	return strings.TrimSpace(meta.CodexTurn.SessionID)
}

// SPDX-License-Identifier: Apache-2.0

// harness_session.go — the HARNESS session identity carried through the tool
// dispatch call stack, and the ONE exported accessor every consumer reads it
// through.
//
// This is a different thing from the session ID in session.go: that one is the
// daemon's own minted TRANSPORT session (Mcp-Session-Id), a value the daemon
// invented. A HarnessSession is the identity of the CLAUDE CODE or CODEX
// session that made the call — the value the harness itself knows a session by,
// delivered by a PreToolUse hook, read off the request's `_meta`, or named
// outright in a Knowledge-Session-Id header.
//
// NO FALLBACK (owner ruling): when no carrier delivered an identity, the
// resolution is Source=none with a Reason naming which carrier was missing, and
// ID is EMPTY. Nothing is inferred from the transport session, the peer
// workspace cwd or a transcript. A consumer that needs an identity fails on
// none; it never substitutes one.

package session

import "context"

// The closed vocabulary of resolution sources. A consumer switches on these
// exact strings and they are reported verbatim by manage(status), so they are
// a contract with the web app and with every caller that reads that field —
// spelled once here, never re-literaled at a call site.
const (
	// HarnessSourceHeader — an explicit Knowledge-Session-Id request header.
	HarnessSourceHeader = "header"
	// HarnessSourceClaudeHook — a Claude Code PreToolUse hook delivery
	// correlated by _meta["claudecode/toolUseId"].
	HarnessSourceClaudeHook = "claude-hook"
	// HarnessSourceCodexHook — a Codex PreToolUse hook delivery correlated by
	// _meta.callId.
	HarnessSourceCodexHook = "codex-hook"
	// HarnessSourceCodexMeta — Codex's _meta["x-codex-turn-metadata"].session_id,
	// read directly off the request with no hook involved.
	HarnessSourceCodexMeta = "codex-meta"
	// HarnessSourceNone — no carrier delivered an identity. ID is empty and
	// Reason names what was missing.
	HarnessSourceNone = "none"
)

// The closed vocabulary of `none`/degraded reasons. Each names ONE missing
// carrier; a resolution joins the ones that apply with "; ". They are distinct
// strings on purpose: a single generic "unresolved" would satisfy every caller
// while telling a user nothing about which half of the install is broken.
const (
	// ReasonNoHeader — no Knowledge-Session-Id request header was sent.
	ReasonNoHeader = "no header"
	// ReasonNoMeta — the request carried no `_meta` session carrier at all
	// (neither Claude's toolUseId nor Codex's turn metadata).
	ReasonNoMeta = "no _meta"
	// ReasonNoCodexTurn — the request named a Codex callId, so Codex DID send
	// `_meta`, but its x-codex-turn-metadata carried no session_id (the object
	// was absent, or its session_id was blank). Distinct from ReasonNoMeta:
	// telling a Codex user "no _meta" when the harness sent one points the
	// diagnosis at the wrong half of the install, and the key is undocumented
	// so its disappearance is a shape to expect.
	ReasonNoCodexTurn = "callId delivered but no x-codex-turn-metadata.session_id"
	// ReasonNoClaudeHook — the request named a Claude toolUseId but no hook
	// delivery for it ever arrived.
	ReasonNoClaudeHook = "toolUseId not delivered by a hook (Claude hook not installed or not firing)"
	// ReasonHookMetaConflict — a Codex call carried BOTH a correlated hook
	// delivery and an x-codex-turn-metadata.session_id, and the two named
	// DIFFERENT sessions. The hook wins by the owner's precedence, but the
	// disagreement is reported rather than dropped: two carriers that should
	// always agree and do not is a fact about the caller's install, and the
	// resolution it produces is the one nobody can check afterwards.
	ReasonHookMetaConflict = "hook and _meta named different sessions; the hook delivery won"
	// ReasonCodexHookInert — Codex's own `_meta` resolved the session, but no
	// Codex hook delivery was correlated for this call. This is the state EVERY
	// fresh Codex install sits in until the user trusts the hook in the TUI's
	// /hooks, because Codex skips an untrusted hook silently.
	ReasonCodexHookInert = "codex hook installed but not observed firing"
	// ReasonNotResolved — the call never ran through the harness-session
	// resolver at all (a non-HTTP dispatch: stdio, a background context, a
	// direct in-process call). Distinct from a resolver that ran and found
	// nothing, so a reader can tell "no carrier" from "no resolution".
	ReasonNotResolved = "no harness session resolution ran for this call"
)

// HarnessSession is the resolved identity of the harness session that made the
// current tool call, plus the provenance of that resolution. Cwd and
// TranscriptPath are populated ONLY from a hook delivery (the only carrier that
// sends them) and are empty otherwise; neither is ever used to derive ID.
type HarnessSession struct {
	// ID is the harness's own session id, or "" when Source is none.
	ID string
	// Source is one of the HarnessSource* constants.
	Source string
	// Reason names the missing carrier(s) when the resolution is none or
	// degraded, joined with "; " from the Reason* constants. Empty when the
	// resolution is clean.
	Reason string
	// Cwd is the working directory the hook delivery reported, "" otherwise.
	Cwd string
	// TranscriptPath is the transcript path the hook delivery reported, ""
	// otherwise.
	TranscriptPath string
}

// Resolved reports whether an identity was actually delivered. A caller that
// requires a session (manage(account_for_session)) fails when this is false
// rather than acting on a guess.
func (h HarnessSession) Resolved() bool {
	return h.ID != "" && h.Source != "" && h.Source != HarnessSourceNone
}

// harnessSessionKey is the typed context key carrying the resolved harness
// session. A private type avoids cross-package collisions, mirroring
// sessionIDKey.
type harnessSessionKey struct{}

// ContextWithHarnessSession returns a copy of ctx carrying the resolved harness
// session. An UNRESOLVED resolution is stamped too — the `none` verdict and its
// reason are exactly what a consumer must see, so they travel like any other.
func ContextWithHarnessSession(ctx context.Context, hs HarnessSession) context.Context {
	return context.WithValue(ctx, harnessSessionKey{}, hs)
}

// HarnessSessionFromContext returns the harness session resolved for this call.
//
// THIS IS THE ONE ACCESSOR. Every consumer — manage(status)'s reporting today,
// manage(account_for_session)'s account selection next — reads the identity
// here and nowhere else.
//
// When no resolution was stamped (a dispatch that did not come through the HTTP
// tools/call path) it returns a `none` verdict carrying ReasonNotResolved rather
// than a zero struct, so a caller switching on Source never has to treat "" as a
// fifth case, and never sees an empty Source it might read as "fine".
func HarnessSessionFromContext(ctx context.Context) HarnessSession {
	hs, ok := ctx.Value(harnessSessionKey{}).(HarnessSession)
	if !ok {
		return HarnessSession{Source: HarnessSourceNone, Reason: ReasonNotResolved}
	}
	return hs
}

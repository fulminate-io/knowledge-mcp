// SPDX-License-Identifier: Apache-2.0

// harness_session.go — THE ONE SITE that consumes the harness session identity.
//
// The identity itself is produced elsewhere: the MCP request carries it, and the
// resolver that reads it off the request is the session-identity work's to write.
// This file declares what this package REQUIRES of that producer — an id, the
// carrier it came from, and, when nothing resolved, the reason to report — so the
// day the producer lands, exactly one function body changes and every consumer
// below (the account ladder, the manage arms, the status render) is untouched.
//
// THE PRODUCER AND THE ONE-LINE ADAPTATION, named rather than described. The
// accessor to consume is session.HarnessSessionFromContext(ctx) in
// cmd/knowledge/internal/session/harness_session.go (paired with
// session.ContextWithHarnessSession), whose value carries ID / Source / Reason /
// Cwd / TranscriptPath and the Resolved() predicate, and whose unstamped context
// answers Source none with a not-resolved reason rather than a zero struct. When
// that package is on this branch, harnessSessionResolver below becomes a forward
// to it — id, source and reason across, Cwd and TranscriptPath ignored because no
// account decision reads them — and nothing else in this package moves. The
// vocabularies here are the same five sources and are mapped one to one.
//
// IT NEVER FALLS BACK TO THE TRANSPORT SESSION. The daemon mints its own
// Mcp-Session-Id and carries it on the dispatch context; that id identifies the
// CONNECTION, not the harness turn, and resolving a harness session from it would
// bind an account to a session the user never named. The owner's rule is "no
// fallback": with no harness carrier the answer is "none" plus a reason, and every
// caller that needs a session fails loudly with it.

package auth

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/session"
)

// HarnessSource names where a resolved harness session came from, or the absence
// of one. The vocabulary is the contract: a consumer reports it verbatim and
// never invents a sixth value.
type HarnessSource string

const (
	// HarnessSourceHeader is an explicit session request header.
	HarnessSourceHeader HarnessSource = session.HarnessSourceHeader
	// HarnessSourceClaudeHook is the Claude PreToolUse hook's stamp.
	HarnessSourceClaudeHook HarnessSource = session.HarnessSourceClaudeHook
	// HarnessSourceCodexHook is the Codex hook's stamp.
	HarnessSourceCodexHook HarnessSource = session.HarnessSourceCodexHook
	// HarnessSourceCodexMeta is the Codex turn metadata on the call.
	HarnessSourceCodexMeta HarnessSource = session.HarnessSourceCodexMeta
	// HarnessSourceNone is the absence of any carrier.
	HarnessSourceNone HarnessSource = session.HarnessSourceNone
)

// HarnessReasonNotResolved is the reason this package reports while no producer
// is wired: nothing stamped the request, so nothing could resolve. Once the
// producer lands it supplies its own reason from the same kind of vocabulary
// (which header was absent, which hook was inert), and this constant is only the
// default's answer.
const HarnessReasonNotResolved = session.ReasonNotResolved

// UnresolvedHarnessSessionReason is the verbatim text reported to a USER whenever
// no harness session resolves. It names every carrier that could have supplied
// one, because the remedy differs per harness and a generic "no session" tells
// them nothing about which install step is missing. It is reported alongside the
// producer's own machine-readable reason, never instead of it.
const UnresolvedHarnessSessionReason = "no harness session on this call: " +
	"the Claude PreToolUse hook is not installed or not firing / " +
	"no Codex turn metadata / no Knowledge-Session-Id header"

// HarnessSession is the resolved (or unresolved) identity of the harness turn
// that issued the current request.
//
// ID is empty exactly when Source is HarnessSourceNone, and in that case Reason
// names which carrier was looked for and missing. The three fields move together:
// a consumer branches on Resolved() and reports Reason.
type HarnessSession struct {
	ID     string
	Source HarnessSource
	Reason string
}

// Resolved reports whether a harness session was identified for this call.
func (h HarnessSession) Resolved() bool { return h.ID != "" }

// RefusalText is what a caller that REQUIRES a session says when it has none: the
// carrier-naming sentence a user can act on, carrying the producer's own reason
// so a support reader can tell which carrier was looked for.
func (h HarnessSession) RefusalText() string {
	reason := h.Reason
	if reason == "" {
		reason = HarnessReasonNotResolved
	}
	return UnresolvedHarnessSessionReason + " (" + reason + ")"
}

// harnessSessionResolver is the seam the producer is wired into. THE PRODUCER
// HAS LANDED: the body forwards to session.HarnessSessionFromContext, carrying
// the id, the source and the reason across and ignoring the cwd and transcript
// path, which no account decision reads. The seam stays because a test drives
// it, and because this remains the ONE site this package consumes that identity
// at — the reason this file was ever separate from its consumers.
//
//nolint:gochecknoglobals // test/landing seam, mirrors segmentdist's accountSelectionID.
var harnessSessionResolver = func(ctx context.Context) HarnessSession {
	hs := session.HarnessSessionFromContext(ctx)
	return HarnessSession{ID: hs.ID, Source: HarnessSource(hs.Source), Reason: hs.Reason}
}

// ResolveHarnessSession reports the harness session identity for this request.
func ResolveHarnessSession(ctx context.Context) HarnessSession {
	return harnessSessionResolver(ctx)
}

// SetHarnessSessionResolverForTest installs a resolver and returns the restore
// closure. TEST ONLY — production installs nothing, so the default above answers.
func SetHarnessSessionResolverForTest(fn func(context.Context) HarnessSession) func() {
	prior := harnessSessionResolver
	harnessSessionResolver = fn
	return func() { harnessSessionResolver = prior }
}

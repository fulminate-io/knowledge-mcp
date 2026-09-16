// SPDX-License-Identifier: Apache-2.0

// request_account.go — THE ONE LADDER that answers "which Fulminate account is
// THIS request routed to?", in the owner's order: per-request header, then the
// calling harness session's binding, then the machine-wide selection.
//
// IT LIVES IN THIS PACKAGE BECAUSE BOTH STAMPING CHOKEPOINTS DO. The Connect
// interceptor stamps through graphclient and the raw /v1/sync surface stamps
// inside Transport.issueBytes here; graphclient imports auth and auth cannot
// import graphclient, so a ladder anywhere else would be reachable from one
// chokepoint and not the other, and the two would disagree — the exact failure
// the single-selection design was built to prevent.
//
// EACH RUNG IS A SOURCE, NOT A FALLBACK. A rung that resolves an account answers;
// a rung with nothing to say is absent rather than degraded; and a rung that
// resolves an account the gateway has already rejected REFUSES rather than
// sliding down to the next one. Sliding down would route a call to an account the
// caller did not name, which is worse than the refusal it replaced.

package auth

import (
	"context"
	"errors"
)

// AccountSource names which rung answered. It is reported verbatim by
// manage(status) as account_source, so the four values are a wire contract.
type AccountSource string

const (
	// AccountSourceHeader is the per-request account header on the call.
	AccountSourceHeader AccountSource = "header"
	// AccountSourceSession is the calling harness session's stored binding.
	AccountSourceSession AccountSource = "session"
	// AccountSourceGlobal is the machine-wide selection in the config file.
	AccountSourceGlobal AccountSource = "global"
	// AccountSourceNone is "no account resolved" — the gateway then resolves the
	// caller's primary account, exactly as it did before any of this existed.
	AccountSourceNone AccountSource = "none"
)

// SessionAccountResolver answers "which account is this harness session bound
// to?". The binding store satisfies it; the daemon installs the store on the
// process selection at construction.
//
// AN ERROR IS A REFUSAL, NOT AN ABSENCE. A store that cannot be read returns one,
// and the ladder surfaces it instead of resolving the request some other way: a
// binding that exists but cannot be read is not the same fact as no binding, and
// answering with the machine-wide account would silently route a session's writes
// into the wrong account. An unbound session is reported as ("", nil).
type SessionAccountResolver interface {
	AccountForSession(sessionID string) (string, error)
}

// ErrSessionBindingsUnreadable is what a resolver returns when the binding store
// itself cannot be read. It is declared HERE, beside the interface it travels
// through, so a consumer can tell an INTERNAL FAULT from a permission decision
// without importing the package that implements the store: an unreadable file
// is this daemon failing to read its own state, and reporting it to a caller as
// "permission denied" sends them looking at their account.
var ErrSessionBindingsUnreadable = errors.New("session binding store unreadable")

type headerAccountKey struct{}

// headerAccount is what the transport read off the request: the account the
// header named, and — when the header was deliberately NOT used — the reason.
// The two travel together because "no header" and "a header this daemon could
// not honor" are different facts with different answers, and a reader that
// could not tell them apart would report the second as the first.
type headerAccount struct {
	id string
	// ignoredReason is set when the header named an account that did not decide
	// the request. The request then resolves to NO account at all rather than
	// falling to the session or the global rung: the caller asked for one
	// specific account and did not get it, and answering with another is the
	// wrong-cause claim this field exists to prevent.
	ignoredReason string
}

// WithHeaderAccount carries the account named by a per-request header into the
// request context. It is the header rung's only producer; the transport layer
// that reads and validates the header calls it once per request.
func WithHeaderAccount(ctx context.Context, accountID string) context.Context {
	if accountID == "" {
		return ctx
	}
	return context.WithValue(ctx, headerAccountKey{}, headerAccount{id: accountID})
}

// WithIgnoredHeaderAccount records that a request CARRIED an account header
// which did not decide its account, and why. The ladder then resolves to none
// rather than sliding down to the session or global rung — see headerAccount.
func WithIgnoredHeaderAccount(ctx context.Context, accountID, reason string) context.Context {
	if accountID == "" {
		return ctx
	}
	return context.WithValue(ctx, headerAccountKey{}, headerAccount{id: accountID, ignoredReason: reason})
}

// HeaderAccount returns the per-request header account, or "" when none rode the
// request. An IGNORED header reports "": it named an account that did not
// decide this request, so it is not this request's account.
func HeaderAccount(ctx context.Context) string {
	h, _ := ctx.Value(headerAccountKey{}).(headerAccount)
	if h.ignoredReason != "" {
		return ""
	}
	return h.id
}

// HeaderAccountIgnored reports the header this request carried and the reason it
// did not decide the account, or ("", "") when there is nothing to explain.
func HeaderAccountIgnored(ctx context.Context) (string, string) {
	h, _ := ctx.Value(headerAccountKey{}).(headerAccount)
	if h.ignoredReason == "" {
		return "", ""
	}
	return h.id, h.ignoredReason
}

// SetSessionAccountResolver installs the per-session binding source on this
// selection and returns the restore closure.
//
// It hangs off the selection rather than off the package so a test that already
// swaps the process selection (SetSelectedAccountForTest) gets an isolated
// binding source with it, and two selections can coexist in one process.
// THE RESTORE IS COMPARE-AND-SWAP, not an unconditional write. Installs are not
// guaranteed to unwind in the order they were made — a process can build a
// second client before the first shuts down, which a test binary does routinely
// — and a closure that restored its own prior unconditionally would then undo a
// LATER install and leave the rung answering from nothing. Restoring only while
// this install is still the current one makes the closure safe in any order: a
// superseded install's release is a no-op, which is the truthful outcome.
func (s *AccountSelection) SetSessionAccountResolver(r SessionAccountResolver) func() {
	s.mu.Lock()
	prior, priorGen := s.sessions, s.sessionsGen
	s.sessionsGen++
	mine := s.sessionsGen
	s.sessions = r
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.sessionsGen != mine {
			return
		}
		s.sessions, s.sessionsGen = prior, priorGen
	}
}

// sessionResolver reads the installed binding source. Nil means this process has
// no per-session layer at all — a CLI invocation, not a daemon — which is an
// absent rung rather than a degraded one.
func (s *AccountSelection) sessionResolver() SessionAccountResolver {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions
}

// RequestAccount resolves the account for one request: header, then the calling
// session's binding, then the machine-wide selection.
//
// The returned source names the rung that answered, and is AccountSourceNone
// whenever the id is empty or an error is returned.
func (s *AccountSelection) RequestAccount(ctx context.Context) (string, AccountSource, error) {
	if _, ignored := HeaderAccountIgnored(ctx); ignored != "" {
		// The header was read and deliberately not used. The request resolves
		// to NO account rather than to whatever the lower rungs would have
		// said: the caller named one, and the answer is that it was not used.
		return "", AccountSourceNone, nil
	}
	if id := HeaderAccount(ctx); id != "" {
		if err := s.RejectionFor(id); err != nil {
			return "", AccountSourceNone, err
		}
		return id, AccountSourceHeader, nil
	}
	bound, err := s.sessionAccount(ctx)
	if err != nil {
		return "", AccountSourceNone, err
	}
	if bound != "" {
		if err := s.RejectionFor(bound); err != nil {
			return "", AccountSourceNone, err
		}
		return bound, AccountSourceSession, nil
	}
	id, err := s.IDForRequest(ctx)
	if err != nil {
		return "", AccountSourceNone, err
	}
	if id == "" {
		return "", AccountSourceNone, nil
	}
	return id, AccountSourceGlobal, nil
}

// sessionAccount reads the calling harness session's binding. It consults the
// store ONLY when a harness session actually resolved, which is what keeps a
// request with no session identity on the machine-wide selection even when the
// store is unreadable.
func (s *AccountSelection) sessionAccount(ctx context.Context) (string, error) {
	resolver := s.sessionResolver()
	if resolver == nil {
		return "", nil
	}
	harness := ResolveHarnessSession(ctx)
	if !harness.Resolved() {
		return "", nil
	}
	return resolver.AccountForSession(harness.ID)
}

// RequestAccount resolves the account for one request against the process-wide
// selection. The method above is the body; this is the spelling every call site
// that does not already hold a selection uses.
func RequestAccount(ctx context.Context) (string, AccountSource, error) {
	return SelectedAccount().RequestAccount(ctx)
}

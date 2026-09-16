// SPDX-License-Identifier: Apache-2.0

// manager_account_guard.go — the fail-closed backstop for a Manager asked to
// serve an account it was not built for.
//
// THE PRIMARY MECHANISM IS THE PER-REQUEST DESTINATION, not a restart. Every
// user call binds one before it dispatches, and the bound destination selects a
// CHILD manager over its own {storage, account} cache directory
// (Manager.ForDestination). A switch — a per-request account header, a session
// binding, or a machine-wide selection moved by `manage(account_use)` — is
// therefore followed with no restart at all: the next call binds a different
// destination and lands on a different child. The root Manager's
// boundAccountID, sampled once in NewManager, is never the serving identity of
// a user call.
//
// WHAT IS LEFT FOR THIS GUARD is the UNBOUND call: background work that carries
// no destination and follows the machine-wide selection, on a root Manager that
// sampled that selection at construction. There the two can diverge — the
// selection moved and this Manager's root did not — and serving the previous
// account's cached segments would be a correctness bug rather than a staleness
// annoyance.
//
// IT REFUSES RATHER THAN HOT-SWAPPING the manager's source, and that has not
// changed: the ratified decision in manager_factory.go — a mid-session identity
// change requires a new manager because a live hot-swap of an L2-authoritative
// manager's source is risky — stands. What changed is that a user never reaches
// it, because their call was routed to a child built for their account instead.
//
// THE REMEDY TEXT USED TO PROMISE A RESTART. It told the user the daemon
// restarts itself on a switch, which was true while `knowledge account use` was
// the only switch there was and the subcommand dispatcher restarted the daemon
// after it. manage(account_use) is a restart-less switch, so that sentence
// became false for it; the text below names what actually resolves the state.

package segmentdist

import (
	"context"
	"errors"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// ErrAccountChanged is returned by every serving entry point once the selected
// Fulminate account differs from the one this Manager was built under.
// Serving the previous account's cached segments would be a correctness bug,
// not a staleness annoyance.
var ErrAccountChanged = errors.New("segmentdist: the selected Fulminate account changed after this daemon started")

// accountSelectionID reports the live selected account. A package-level seam so
// tests can drive an in-session switch without touching a real config.
//
//nolint:gochecknoglobals // test seam, mirrors the other package-level seams here.
var accountSelectionID = func(ctx context.Context) string {
	return auth.SelectedAccount().ID(ctx)
}

// checkAccountBinding reports an error when the account this call is for has
// moved off the account this Manager was constructed under. Nil in every other
// case, including when neither is set.
//
// WHICH ACCOUNT THE CALL IS FOR HAS TWO ANSWERS, and the order matters. A
// request that names its own account (an inbound account header on /mcp, a
// qualified reference) carries a bound destination, and THAT is the account it
// must be served for: it reached this manager through ForDestination, which
// gives each account its own child over its own cache directory, so comparing
// the child with the PROCESS selection refuses a request that is already
// correctly partitioned. Only an unbound call — the daemon's own background
// work, which follows the selection — is judged against the live selection,
// which is the mid-session-switch case this guard was written for.
func (m *Manager) checkAccountBinding(ctx context.Context) error {
	if m.destination != nil && m.destination.Storage == "local" {
		return nil
	}
	if d, bound := graphclient.StorageDestination(ctx); bound {
		if d.Storage == "local" || d.AccountID == m.boundAccountID {
			return nil
		}
		return accountChangedError(m.boundAccountID, d.AccountID)
	}
	live := accountSelectionID(ctx)
	if live == m.boundAccountID {
		return nil
	}
	return accountChangedError(m.boundAccountID, live)
}

// accountChangedError renders the refusal with both accounts and the remedy.
//
// IT PROMISES NO RESTART. An account switch is followed per request now, so the
// path that reaches this error is background work on a root manager whose
// sampled account the selection has moved off — and the thing that resolves it
// is a call that binds a destination, or a restart the USER performs, never one
// the daemon performs for them.
func accountChangedError(bound, now string) error {
	return fmt.Errorf("%w (built for %q, now %q) — reads that name their account are unaffected and need nothing; this is background work on a manager built for the previous selection. It clears on the next daemon start: `knowledge stop` then `knowledge start`, or `brew services restart knowledge` on a brew-managed install",
		ErrAccountChanged, bound, now)
}

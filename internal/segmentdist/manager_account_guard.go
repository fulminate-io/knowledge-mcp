// SPDX-License-Identifier: Apache-2.0

// manager_account_guard.go — the fail-closed backstop for an in-session
// Fulminate account switch.
//
// The Manager samples its cache root and its per-graph sources ONCE at
// construction, so a mid-session `knowledge account use` does not move them.
// The primary mechanisms are elsewhere: the cache root is partitioned by
// account (bootstrap), and the subcommand dispatcher restarts a running daemon
// automatically when the selection moves, so in the ordinary case the user
// never meets this refusal.
//
// This guard exists for exactly three states: the brief window between the
// config write and the restarted daemon binding again, a restart that failed,
// and the brew-managed install where the repo defers to
// `brew services restart knowledge` rather than fighting the service manager.
//
// It REFUSES rather than hot-swapping the manager's source. The ratified
// decision in manager_factory.go — a mid-session identity change requires a
// daemon restart because a live hot-swap of an L2-authoritative manager's
// source is risky — stands; the automatic restart satisfies "no manual step" by
// restarting the PROCESS, not by swapping a live manager underneath itself.

package segmentdist

import (
	"context"
	"errors"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
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

// checkAccountBinding reports an error when the live selection has moved off
// the account this Manager was constructed under. Nil in every other case,
// including when neither is set.
func (m *Manager) checkAccountBinding(ctx context.Context) error {
	live := accountSelectionID(ctx)
	if live == m.boundAccountID {
		return nil
	}
	return fmt.Errorf("%w (built for %q, now %q) — the daemon restarts itself on a switch; if this persists, run `knowledge stop` then `knowledge start`, or `brew services restart knowledge` on a brew-managed install",
		ErrAccountChanged, m.boundAccountID, live)
}

// SPDX-License-Identifier: Apache-2.0

// account_daemon_entry.go — the two entry points the DAEMON's manage(account_*)
// arms call into this package for.
//
// THEY EXPORT, THEY DO NOT REIMPLEMENT. The membership decoder knows the frozen
// shape of GET /v1/me/accounts (accounts.go:48-51 says it is the one place that
// does), and `knowledge account use` owns the write flow that fetches
// memberships, matches id-first-then-slug, refuses an account with no active
// subscription and writes the UUID. A second copy of either in the daemon would
// be two bodies to keep in step, and the first thing to drift would be the
// refusal. Both bodies below are one-line forwards for exactly that reason.
//
// They live in a file of their own rather than beside the commands so the two
// callers of each body are visible in one place.

package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// AccountMembership is one membership of the caller's, for consumers outside
// this package. It carries the three fields a caller acts on; the decoder's own
// entry type stays unexported with the endpoint shape it mirrors.
type AccountMembership struct {
	ID                    string
	Slug                  string
	HasActiveSubscription bool
}

// LookupAccount resolves arg — an id or a slug, id first — against the caller's
// LIVE membership list, and reports the membership it names.
//
// IT CHECKS MEMBERSHIP AND NOTHING ELSE. An account with no active subscription
// is returned rather than refused: the gateway refuses every cloud call for such
// an account server-side, and a second client-side refusal here would make an
// account unbindable for a reason the client is not the authority on. The caller
// that must refuse one (`knowledge account use`, through runAccountUse) already
// does.
//
// An unreachable or unauthenticated endpoint FAILS rather than returning
// "not a member": absent-from-the-list must mean the list was read.
func LookupAccount(ctx context.Context, arg string) (AccountMembership, error) {
	accounts, err := fetchAccounts(ctx)
	if err != nil {
		return AccountMembership{}, fmt.Errorf("could not check your accounts, so nothing was changed: %w", err)
	}
	matched, ok := matchAccount(accounts, arg)
	if !ok {
		return AccountMembership{}, fmt.Errorf("you are not a member of any Fulminate account with id or slug %q — run `knowledge accounts` to see the accounts you can use, or ask an owner of that account to invite you", arg)
	}
	return AccountMembership{
		ID:                    matched.ID,
		Slug:                  matched.Slug,
		HasActiveSubscription: matched.HasActiveSubscription,
	}, nil
}

// UseAccount runs the `knowledge account use` flow for a caller that is not the
// command's own flag parsing — the daemon's manage(account_use) arm. It IS
// runAccountUse: same membership fetch, same id-first-then-slug match, same
// refusal of an account with no active subscription, same config write, same
// confirmation line onto out.
//
// It returns the account id now STORED, read back from the config file rather
// than from the process's cached selection: that cache is a TTL window old by
// design, so reading it here would report the previous account to a caller who
// just changed it.
func UseAccount(ctx context.Context, out io.Writer, arg string) (string, error) {
	if err := runAccountUse(ctx, out, arg); err != nil {
		return "", err
	}
	path, err := authConfigPath()
	if err != nil {
		return "", err
	}
	return config.ReadSelectedAccountID(path)
}

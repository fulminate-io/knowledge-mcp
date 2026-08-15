// SPDX-License-Identifier: Apache-2.0

// account_use.go — `knowledge account use <id|slug>`: pick the Fulminate
// account this machine's cloud calls are routed to.
//
// THE PRE-CHECKS HERE ARE BELT AND SUSPENDERS, NOT THE SECURITY CONTROL. The
// gateway owns enforcement; a client check can be stale and is never
// load-bearing for security. The check is one-directional: a membership list
// that says NO stops the write, but a list that says YES grants nothing —
// every subsequent cloud call is still gated by the gateway, per request.
//
// NO PROCESS SIDE EFFECTS LIVE IN THIS FILE. The automatic daemon restart is
// triggered by the subcommand dispatcher, which compares the stored selection
// before and after this command runs: bootstrap imports cli, so cli cannot
// reach bootstrap's lifecycle primitives.

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// accountUseUsage is printed by `knowledge account use --help`.
const accountUseUsage = `knowledge account use <id|slug> — route this machine's cloud calls to an account

Usage:
  knowledge account use <id|slug>

Checks that you are a member of the account and that it has an active
subscription (accounts without one have no cloud graph access at all), then
stores the account's id in ~/.knowledge/config.

The selection can be changed at any time by running this command again. It
cannot be cleared: there is no --clear or --unset.

List the accounts you can use with:
  knowledge accounts
`

// AccountUseCmd implements `knowledge account use <id|slug>`.
func AccountUseCmd(args []string) error {
	fs := flag.NewFlagSet("account use", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	fs.Usage = func() { fmt.Fprint(os.Stdout, accountUseUsage) }

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("knowledge account use: expected exactly one account id or slug — run `knowledge accounts` to see the accounts you can use")
	}
	return runAccountUse(context.Background(), os.Stdout, fs.Arg(0))
}

// runAccountUse holds the flow, factored from flag parsing so tests can drive
// it with a captured writer.
func runAccountUse(ctx context.Context, out io.Writer, arg string) error {
	// The list endpoint returns the caller's OWN memberships, so "absent from
	// this list" IS "not a member".
	accounts, err := fetchAccounts(ctx)
	if err != nil {
		// An unreachable endpoint fails rather than writing optimistically:
		// persisting a selection that was never checked is exactly the state
		// the pre-check exists to avoid creating.
		return fmt.Errorf("knowledge account use: could not check your accounts, so nothing was changed: %w", err)
	}

	matched, ok := matchAccount(accounts, arg)
	if !ok {
		return fmt.Errorf("you are not a member of any Fulminate account with id or slug %q — run `knowledge accounts` to see the accounts you can use, or ask an owner of that account to invite you", arg)
	}
	if !matched.HasActiveSubscription {
		return fmt.Errorf("account %q has no active subscription, so it has no cloud graph access at all — subscribe from the Fulminate billing settings for that account, or pick an account that already has one", matched.Slug)
	}

	path, err := config.DefaultPath()
	if err != nil {
		return fmt.Errorf("knowledge account use: %w", err)
	}
	// The UUID, never the slug: the gateway header contract is the internal
	// account id, and a slug can be renamed while an id cannot.
	if err := config.WriteSelectedAccountID(path, matched.ID); err != nil {
		return fmt.Errorf("knowledge account use: %w", err)
	}

	fmt.Fprintf(out, "Now using account %s (%s). Cloud calls from this machine are routed to it.\n", matched.Slug, matched.ID)
	return nil
}

// matchAccount resolves arg against the membership list, id first then slug.
// Id-first matters because the id is the unambiguous key.
func matchAccount(accounts []accountEntry, arg string) (accountEntry, bool) {
	for _, a := range accounts {
		if a.ID == arg {
			return a, true
		}
	}
	for _, a := range accounts {
		if a.Slug == arg {
			return a, true
		}
	}
	return accountEntry{}, false
}

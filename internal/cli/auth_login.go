// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// loginUsage is printed by `knowledge login --help`. Terse, factual,
// no flourish. Browser-only by design — no device-flow fallback. No
// `--fulminate-endpoint` flag: the cloud host is build-tag-pinned
// (memory: feedback_no_endpoint_override).
//
// No `--permissions` flag: WorkOS does not accept the application's
// permission slugs as OAuth scopes. The authorize request asks for the
// fixed standard scope set (see auth.oauthScopes); the granted
// permissions come from the user's assigned WorkOS Role and arrive in
// the access token's `permissions` claim.
const loginUsage = `knowledge login — authenticate via WorkOS browser PKCE

Usage:
  knowledge login

Prints an authorize URL and additionally opens a browser when one is
available, so headless environments (CI runners, remote SSH, containers)
are supported — open the printed URL from any browser that can reach the
callback address.
`

// LoginCmd implements `knowledge login`. Returns nil on success; a
// non-nil error is printed to stderr + exit 1 by the caller (main.go).
//
// User denial at the AuthKit hosted-login page comes back through the
// loopback handler as an OAuth `error` query parameter and ends up as
// a regular error here — there is no longer a separate "denied vs.
// failed" axis because the browser flow doesn't poll like the device
// flow did.
func LoginCmd(args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	fs.Usage = func() { fmt.Fprint(os.Stdout, loginUsage) }

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt)
	defer stop()

	return loginBrowserPKCE(ctx)
}

// runBrowserPKCEFlowFn is the package-private indirection for
// auth.RunBrowserPKCEFlow, matching the discoverFn seam in auth_logout.go.
// Tests override it to drive the persist half of login without a browser or
// a loopback listener; the production value is the real flow.
var runBrowserPKCEFlowFn = auth.RunBrowserPKCEFlow

// loginBrowserPKCE runs the WorkOS AuthKit browser PKCE flow:
// discovery (RFC 9728 + RFC 8414) → loopback listener → browser
// authorize → callback → token exchange → persist refresh token and
// publish the session. Prints a single-line "Logged in." on success.
func loginBrowserPKCE(ctx context.Context) error {
	store, err := openStore()
	if err != nil {
		if handleKeychainUnavailable(err) {
			return nil
		}
		return err
	}

	endpoints, err := discoverFn(ctx, CloudEndpoint, allowedAuthHosts)
	if err != nil {
		return fmt.Errorf("discover OAuth endpoints: %w", err)
	}

	fmt.Fprintln(os.Stdout, "Opening your browser to authenticate…")
	fmt.Fprintln(os.Stdout, "(Waiting for callback. Press Ctrl-C to cancel.)")

	clientID, tr, err := runBrowserPKCEFlowFn(ctx, endpoints)
	if err != nil {
		return err
	}

	// Persist the dynamically-registered client_id first: the refresh path
	// (auth.OAuthTokenSource) needs it alongside the refresh token, and a
	// refresh token without its client_id is unusable.
	if err := store.Set(ctx, auth.KeyClientID, clientID); err != nil {
		if handleKeychainUnavailable(err) {
			return nil
		}
		return fmt.Errorf("persist client id: %w", err)
	}

	if err := store.Set(
		ctx, auth.KeyRefreshToken, tr.RefreshToken,
	); err != nil {
		if handleKeychainUnavailable(err) {
			return nil
		}
		return fmt.Errorf("persist refresh token: %w", err)
	}

	// Publish the session so read-only consumers can use this login straight
	// away rather than waiting for the owning process's first refresh. Only
	// best-effort: a login that authenticated must not fail because the
	// readable copy did not land.
	if err := auth.PublishSessionToken(ctx, store, tr); err != nil {
		slog.Warn("login: could not publish the session access token — read-only consumers will see no session until the next refresh",
			"error", err)
	}

	fmt.Fprintln(os.Stdout, "Logged in.")
	establishOrRevalidateAccount(ctx, os.Stdout)
	return nil
}

// establishOrRevalidateAccount makes the account this machine routes cloud
// calls to EXPLICIT after a login, and revalidates one that is already stored.
//
// It follows the PublishSessionToken precedent directly above: a secondary
// step after a successful login that warns on failure and NEVER fails the
// login. It returns nothing, and every error path prints at most one line.
//
// ESTABLISH (nothing stored): a single membership is written; with several, the
// FIRST entry is written — the list is ordered created_at ASC, so that is the
// oldest membership, which is the account the gateway already resolves when no
// header is sent. Writing it changes nothing about where calls land; it only
// makes the resolution explicit and inspectable.
//
// REVALIDATE (something stored): the stored value is the USER'S, so login never
// overwrites it. It only warns when the membership or the subscription is gone.
//
// This call site can CREATE a selection but never blank one:
// config.WriteSelectedAccountID refuses an empty id. That is a statement about
// THIS call site — the client has two writers that rewrite an existing config
// (config.WriteSelectedAccountID and bootstrap.renderAndWriteConfig), and both
// preserve the entry.
//
// No process side effects live here: establishing a selection moves a running
// daemon's account identity, and the subcommand dispatcher is what restarts it,
// because bootstrap imports cli and not the other way round.
func establishOrRevalidateAccount(ctx context.Context, out io.Writer) {
	path, err := config.DefaultPath()
	if err != nil {
		return
	}
	selected, err := config.ReadSelectedAccountID(path)
	if err != nil {
		return
	}

	accounts, err := fetchAccounts(ctx)
	if err != nil {
		// A login that authenticated must not be reported as failed because a
		// secondary endpoint was down.
		return
	}

	if selected != "" {
		revalidateSelection(out, accounts, selected)
		return
	}
	establishSelection(out, path, accounts)
}

// establishSelection writes the account this machine is already using, when no
// selection is stored.
func establishSelection(out io.Writer, path string, accounts []accountEntry) {
	if len(accounts) == 0 {
		fmt.Fprintln(out, "No accounts — your login is not a member of any Fulminate account.")
		return
	}
	chosen := accounts[0]
	if err := config.WriteSelectedAccountID(path, chosen.ID); err != nil {
		fmt.Fprintf(out, "Could not record the selected account (%v) — run `knowledge account use <id|slug>` to set it.\n", err)
		return
	}
	fmt.Fprintf(out, "Account: %s (%s) — change it with `knowledge account use <id|slug>`.\n", chosen.Slug, chosen.ID)

	// Subscription state does NOT gate the establish, and the asymmetry with
	// `account use` is deliberate: `account use` refuses an unsubscribed
	// account because that is a user CHOOSING to route where every call is
	// guaranteed to fail, while login is only recording the account the gateway
	// would have resolved anyway. Declining to write it would leave the
	// selection unset and route to the identical account — buying nothing and
	// costing the visibility this exists for. So it is written, with a warning.
	if !chosen.HasActiveSubscription {
		fmt.Fprintln(out, "Warning: that account has no active subscription, so it has no cloud graph access — subscribe for that account, or run `knowledge account use <id|slug>` to pick another")
	}
}

// revalidateSelection warns when the stored selection has lost its membership
// or its subscription. It never writes.
func revalidateSelection(out io.Writer, accounts []accountEntry, selected string) {
	for _, a := range accounts {
		if a.ID != selected {
			continue
		}
		if !a.HasActiveSubscription {
			fmt.Fprintln(out, "Warning: the account selected in ~/.knowledge/config has no active subscription, so it has no cloud graph access — subscribe for that account, or run `knowledge account use <id|slug>` to pick another")
		}
		return
	}
	fmt.Fprintln(out, "Warning: the account selected in ~/.knowledge/config is no longer one of your accounts — run `knowledge accounts` then `knowledge account use <id|slug>`")
}

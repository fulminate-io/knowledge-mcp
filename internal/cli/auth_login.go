// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
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

Requires a working desktop browser. Headless environments (CI runners,
remote SSH without browser/X-forwarding, container-only) are explicitly
not supported.
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

// loginBrowserPKCE runs the WorkOS AuthKit browser PKCE flow:
// discovery (RFC 9728 + RFC 8414) → loopback listener → browser
// authorize → callback → token exchange → persist refresh token.
// Prints a single-line "Logged in." on success.
func loginBrowserPKCE(ctx context.Context) error {
	store, err := openStore()
	if err != nil {
		if handleKeychainUnavailable(err) {
			return nil
		}
		return err
	}

	endpoints, err := auth.Discover(ctx, CloudEndpoint, allowedAuthHosts)
	if err != nil {
		return fmt.Errorf("discover OAuth endpoints: %w", err)
	}

	fmt.Fprintln(os.Stdout, "Opening your browser to authenticate…")
	fmt.Fprintln(os.Stdout, "(Waiting for callback. Press Ctrl-C to cancel.)")

	clientID, tr, err := auth.RunBrowserPKCEFlow(ctx, endpoints)
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

	fmt.Fprintln(os.Stdout, "Logged in.")
	return nil
}

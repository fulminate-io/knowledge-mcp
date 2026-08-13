// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// logoutUsage is printed by `knowledge logout --help`. No
// `--fulminate-endpoint` flag — the cloud host is build-tag-pinned
// (memory: feedback_no_endpoint_override).
const logoutUsage = `knowledge logout — revoke + delete stored OAuth credentials

Usage:
  knowledge logout

Deletes the stored credentials — refresh token, client id, and the
published session token — from the credential store and best-effort
revokes the refresh token at the AuthKit revocation endpoint. Safe to
run when already logged out.
`

// LogoutCmd implements `knowledge logout`. Returns nil on success or
// when already logged out (a missing refresh token is not an error —
// the documented contract is idempotent cleanup).
//
// Server-side revocation is best-effort: discovery failures, AuthKit
// not exposing a revocation_endpoint, network errors, and non-200
// responses are all logged at WARN but never block local cleanup. The
// critical invariant is that neither the refresh token nor the published
// session token is left in the credential store when LogoutCmd returns —
// either one alone would keep some process authenticated.
func LogoutCmd(args []string) error {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	fs.Usage = func() { fmt.Fprint(os.Stdout, logoutUsage) }

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	ctx := context.Background()

	store, err := openStore()
	if err != nil {
		if handleKeychainUnavailable(err) {
			return nil
		}
		return err
	}

	revokeIfPresent(ctx, store)

	deleteIgnoringMissing(ctx, store, auth.KeyRefreshToken)
	deleteIgnoringMissing(ctx, store, auth.KeyClientID)
	// The published session must go too. It is independently usable by any
	// process that can read the store, so leaving it behind would let a
	// reader keep authenticating as the logged-out operator until the access
	// token lapsed on its own.
	deleteIgnoringMissing(ctx, store, auth.KeyAccessToken)
	deleteIgnoringMissing(ctx, store, auth.KeyAccessTokenExpiry)

	fmt.Fprintln(os.Stdout, "Logged out.")
	return nil
}

// discoverFn is the package-private indirection for auth.Discover.
// Tests override it to avoid mocking two .well-known endpoints; the
// production value is the real discovery function.
var discoverFn = auth.Discover

// revokeIfPresent loads the refresh token (if any), discovers the
// AuthKit revocation endpoint, and tells AuthKit to revoke it. Every
// step is best-effort — discovery, network, and validation errors are
// logged at WARN and swallowed so a transient failure doesn't block
// local cleanup. If the token isn't in the keychain at all, this is a
// no-op.
func revokeIfPresent(ctx context.Context, store auth.Store) {
	rt, err := store.Get(ctx, auth.KeyRefreshToken)
	switch {
	case err == nil && rt != "":
		eps, discErr := discoverFn(ctx, CloudEndpoint, allowedAuthHosts)
		if discErr != nil {
			slog.Warn("logout: OAuth discovery failed (continuing local cleanup)",
				"error", discErr)
			return
		}
		if revErr := auth.RevokeRefreshToken(
			ctx, eps.RevocationEndpoint, rt,
		); revErr != nil {
			slog.Warn("logout: AuthKit revoke failed (continuing)",
				"error", revErr)
		}
	case errors.Is(err, auth.ErrNotFound):
		// Nothing to revoke — already logged out.
	default:
		slog.Warn("logout: keychain read failed (continuing)",
			"error", err)
	}
}

// deleteIgnoringMissing deletes a keychain entry and swallows the
// ErrNotFound case so logout stays idempotent. Any other error is
// logged at WARN — local cleanup is best-effort by design, and a
// dbus/Keychain glitch shouldn't leave the user stuck with a non-zero
// exit they can't resolve.
func deleteIgnoringMissing(
	ctx context.Context, store auth.Store, key string,
) {
	err := store.Delete(ctx, key)
	switch {
	case err == nil, errors.Is(err, auth.ErrNotFound):
		return
	default:
		slog.Warn("logout: keychain delete failed",
			"key", key, "error", err)
	}
}

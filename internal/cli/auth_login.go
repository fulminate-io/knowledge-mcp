// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// loginFlags groups the parsed flag values for `knowledge login`.
type loginFlags struct {
	permissions string
}

// loginUsage is printed by `knowledge login --help`. Terse, factual,
// no flourish. Browser-only by design — no device-flow fallback. No
// `--fulminate-endpoint` flag: the cloud host is build-tag-pinned
// (memory: feedback_no_endpoint_override).
const loginUsage = `knowledge login — authenticate via WorkOS browser PKCE

Usage:
  knowledge login [flags]

Flags:
  --permissions LIST   Comma-separated WorkOS permission slugs to
                       request (default: mcp:knowledge:read,
                       mcp:knowledge:write)

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

	var lf loginFlags
	fs.StringVar(&lf.permissions, "permissions", "",
		"comma-separated requested permissions (empty = defaults)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt)
	defer stop()

	return loginBrowserPKCE(ctx, parsePermissions(lf.permissions))
}

// parsePermissions splits a comma-separated --permissions string into
// a whitespace-trimmed, de-empty-d slice. Empty input yields the
// default set (read + write on the knowledge MCP resource), which is
// what every CLI invocation needs to use sync.
func parsePermissions(s string) []string {
	if s == "" {
		return []string{auth.PermMCPKnowledgeRead, auth.PermMCPKnowledgeWrite}
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// loginBrowserPKCE runs the WorkOS AuthKit browser PKCE flow:
// discovery (RFC 9728 + RFC 8414) → loopback listener → browser
// authorize → callback → token exchange → persist refresh token.
// Prints a single-line "Logged in." on success.
func loginBrowserPKCE(
	ctx context.Context, permissions []string,
) error {
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

	tr, err := auth.RunBrowserPKCEFlow(ctx, endpoints, OAuthClientID, permissions)
	if err != nil {
		return err
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

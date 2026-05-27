// SPDX-License-Identifier: Apache-2.0

// Package cli implements the `knowledge` binary's user-facing
// authentication subcommands (login, logout). It sits above auth/ —
// wiring the WorkOS AuthKit browser-PKCE-loopback flow, the platform
// keychain, and the RFC 9728 + RFC 8414 discovery dance into a small
// set of terse, scriptable commands.
//
// This package lives client-internal (cmd/knowledge/internal/cli) and is
// imported by the client login/logout subcommands and the sync push
// transport builder. It must not import from the server, store, or tools
// packages — those are long-lived per-process concerns, while the CLI path
// is a one-shot invocation that completes and exits. Keeping the import
// surface minimal (auth, net/http, flag, os, fmt) prevents accidental
// coupling between "launch the MCP server" and "talk to AuthKit".
package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// OAuthClientID is the public OAuth client identifier for the knowledge
// binary. Duplicated from main.go's oauthClientID so the cli/ package
// is importable without pulling in the server-side wiring. The two
// must stay in sync with the server's registered client — see the A2
// wire contract.
const OAuthClientID = "knowledge-cli"

// The Fulminate API base URL is build-tag-pinned (see allowed_hosts.go
// and allowed_hosts_dev.go). There is NO runtime override — no
// `--fulminate-endpoint` flag, no `$FULMINATE_ENDPOINT` env var, no
// config-file knob (memory: feedback_no_endpoint_override). Callers
// reach the cloud via the exported `CloudEndpoint` constant.

// newStoreFn is the store constructor used by the auth subcommands.
// Tests override it to inject an in-memory store and avoid touching
// the real platform keychain. Production callers leave the default in
// place so auth.NewStore runs.
var newStoreFn = auth.NewStore

// openStore opens the platform keychain and returns a usage-specific
// error path when the OS has no implementation (currently Windows).
//
// Callers should handle the returned (nil, nil) case as "print the
// friendly Windows message, return nil from the subcommand" — not all
// subcommands want to treat a missing keychain as an error.
func openStore() (auth.Store, error) {
	store, err := newStoreFn()
	if err != nil {
		return nil, fmt.Errorf("open keychain: %w", err)
	}
	return store, nil
}

// handleKeychainUnavailable detects the ErrNotImplementedOS sentinel
// and prints the documented friendly fallback message. Returns true
// when the caller should treat the situation as "successful no-op"
// (return nil); false means the error was something else and the
// caller should surface it. The stdout text mirrors what the
// auth/storage.go sentinel documentation promises so Windows users get
// a consistent message across login/logout.
func handleKeychainUnavailable(err error) bool {
	if !errors.Is(err, auth.ErrNotImplementedOS) {
		return false
	}
	fmt.Fprintln(os.Stdout,
		"Windows keychain support is planned for a future release. "+
			"Paid features are unavailable until a Windows backend lands.")
	return true
}

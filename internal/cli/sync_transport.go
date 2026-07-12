// SPDX-License-Identifier: Apache-2.0

// sync_transport.go — the single sync-transport builder shared by graph sync
// (push/pull), the segment control plane (segmentdist manifest/read), and the
// standalone transcript-upload subcommand. It constructs the *auth.Transport the
// control-plane POSTs go over. A present KNOWLEDGE_AUTH_TOKEN machine bearer is
// honored FIRST (headless/automated path); otherwise it wires the interactive OAuth
// TokenSource off the local keychain store. The Fulminate cloud host is hardcoded
// per build tag (CloudEndpoint); there is no runtime override.

package cli

import (
	"fmt"
	"os"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// BuildSyncTransport constructs the sync Transport used by every control-plane POST
// (graph sync AND the segmentdist manifest/read path).
//
// A present KNOWLEDGE_AUTH_TOKEN machine bearer is honored FIRST: a headless daemon
// (e.g. the executor sandbox) has NO keychain/dbus, so the OAuth-refresh path fails
// with `secretservice ... "dbus-launch": executable file not found` even though the
// machine token is set — which is exactly what stranded the sandbox knowledge search
// at segmentdist manifest/read. This mirrors bootstrap.selectAuthSources: a present
// machine token bypasses the keychain entirely and presents the opaque bearer
// directly. It reads the SAME env var as the --auth-token flag default (config.go).
//
// With no machine token it falls back to the OAuth store for the interactive-login
// path, returning an actionable error when the keychain is unavailable so the caller
// can surface the "run knowledge login" guidance — a nil Transport with a nil error
// is never returned.
func BuildSyncTransport() (*auth.Transport, error) {
	if tok := os.Getenv("KNOWLEDGE_AUTH_TOKEN"); tok != "" {
		return auth.NewSyncTransport(CloudEndpoint, auth.StaticTokenSource{AccessToken: tok}), nil
	}
	store, err := auth.NewStore()
	if err != nil {
		return nil, fmt.Errorf("sync requires login — keychain unavailable: %w", err)
	}
	ts := auth.NewOAuthTokenSource(store, CloudEndpoint, AllowedAuthHosts())
	return auth.NewSyncTransport(CloudEndpoint, ts), nil
}

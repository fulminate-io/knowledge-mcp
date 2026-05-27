// SPDX-License-Identifier: Apache-2.0

// sync_transport_builder.go — client-side OAuth sync-transport builder. Lifts
// the retired server buildAuthStack body (cmd/knowledge-server/bootstrap/tools.go)
// into the client: it wires the OAuth TokenSource off the local keychain store
// and constructs the *auth.Transport the push orchestration POSTs the serialized
// graph bytes over. The Fulminate cloud host is hardcoded per build tag
// (cli.CloudEndpoint). Push needs only the Transport.

package tools

import (
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/cli"
)

// buildSyncTransport constructs the OAuth-backed sync Transport from the local
// keychain credential store. It returns an actionable error when the keychain is
// unavailable (no credential store on this platform) so the caller can surface
// the "run knowledge login" guidance — a nil Transport with a nil error is never
// returned.
func buildSyncTransport() (*auth.Transport, error) {
	store, err := auth.NewStore()
	if err != nil {
		return nil, fmt.Errorf("sync requires login — keychain unavailable: %w", err)
	}
	ts := auth.NewOAuthTokenSource(store, cli.CloudEndpoint, cli.OAuthClientID, cli.AllowedAuthHosts())
	return auth.NewSyncTransport(cli.CloudEndpoint, ts), nil
}

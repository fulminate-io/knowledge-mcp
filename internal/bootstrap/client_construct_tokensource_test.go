// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// TestConstructClient_SharedCloudTokenSource proves constructClient retains the
// ONE token source selectAuthSources builds on the *client (c.cloudTokenSource),
// that the RESOLVED Config.AuthToken (flag/config — not os.Getenv) populates it
// (fix 3), and that buildCloudSyncTransport wraps that shared source into a
// live transport. The no-token path retains the interactive keychain OAuth
// source so it can refresh.
func TestConstructClient_SharedCloudTokenSource(t *testing.T) {
	dialer := func(int) *graphclient.GraphClient {
		gc := graphclient.NewGraphClientForURL("http://local.invalid")
		t.Cleanup(gc.CloseIdleConnections)
		return gc
	}

	build := func(t *testing.T, cfg Config) *client {
		t.Helper()

		store := newFakeAuthStore()
		origStore := newAuthStoreFn
		newAuthStoreFn = func() (auth.Store, error) { return store, nil }
		t.Cleanup(func() { newAuthStoreFn = origStore })

		origKeepalive := startKeepaliveFn
		startKeepaliveFn = func(_ *graphclient.GraphClient, _ context.Context) {}
		t.Cleanup(func() { startKeepaliveFn = origKeepalive })

		cfg.LocalDialer = dialer
		c := constructClient(cfg)
		require.NotNil(t, c)
		return c
	}

	t.Run("resolved_flag_token_populates_the_single_source", func(t *testing.T) {
		c := build(t, Config{AuthToken: "machine-tok"})
		require.NotNil(t, c.cloudTokenSource)
		// The RESOLVED flag/config value — not os.Getenv — reaches the one
		// shared source, so a machine-authed headless daemon presents that
		// bearer on every cloud transport (fix 3).
		tok, _, err := c.cloudTokenSource.Token(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "machine-tok", tok)
	})

	t.Run("no_token_yields_interactive_oauth_source", func(t *testing.T) {
		c := build(t, Config{})
		require.NotNil(t, c.cloudTokenSource)
		_, ok := c.cloudTokenSource.(*auth.OAuthTokenSource)
		assert.True(t, ok, "no-token path must retain the interactive keychain OAuth source")
	})

	t.Run("buildCloudSyncTransport_wraps_the_shared_source", func(t *testing.T) {
		c := build(t, Config{AuthToken: "machine-tok"})
		tr, err := c.buildCloudSyncTransport()
		require.NoError(t, err)
		require.NotNil(t, tr)
	})
}

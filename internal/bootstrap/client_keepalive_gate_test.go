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

// TestConstructClient_KeepaliveGatedOnLogin proves the local-server keepalive
// is launched ONLY for a not-logged-in client. A logged-in daemon routes every
// op to cloud and runs with no local knowledge-server, so probing :15022 would
// emit escalating ERROR-log spam for a server intentionally not running. The
// gate (client.go: `if !c.router.LoggedIn(...)`) is asserted through the
// startKeepaliveFn seam without a real loopback dial.
//
// Seam-override idiom mirrors cli/auth_login_test.go withMemoryStore: override
// the package vars, restore via t.Cleanup.
func TestConstructClient_KeepaliveGatedOnLogin(t *testing.T) {
	// LocalDialer hands constructClient a throwaway *GraphClient pointed at an
	// unused URL — no RPC is issued in this test, so it never dials.
	dialer := func(int) *graphclient.GraphClient {
		gc := graphclient.NewGraphClientForURL("http://local.invalid")
		t.Cleanup(gc.Close)
		return gc
	}

	run := func(t *testing.T, loggedIn bool) (called bool) {
		t.Helper()

		store := newFakeAuthStore()
		if loggedIn {
			require.NoError(t, store.Set(context.Background(), auth.KeyRefreshToken, "frt-stub"))
		}

		origStore := newAuthStoreFn
		newAuthStoreFn = func() (auth.Store, error) { return store, nil }
		t.Cleanup(func() { newAuthStoreFn = origStore })

		origKeepalive := startKeepaliveFn
		startKeepaliveFn = func(_ *graphclient.GraphClient, _ context.Context) { called = true }
		t.Cleanup(func() { startKeepaliveFn = origKeepalive })

		c := constructClient(Config{LocalDialer: dialer})
		require.NotNil(t, c)
		return called
	}

	t.Run("logged_in_skips_keepalive", func(t *testing.T) {
		assert.False(t, run(t, true), "a logged-in daemon must NOT start the :15022 keepalive")
	})

	t.Run("logged_out_starts_keepalive", func(t *testing.T) {
		assert.True(t, run(t, false), "a not-logged-in client must start the keepalive for drop detection")
	})
}

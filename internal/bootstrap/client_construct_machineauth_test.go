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

// TestConstructClient_MachineAuthWiring proves constructClient threads the
// machine-token signal into the Router's cloud-selection decision. With a
// machine bearer token present, the Router reports LoggedIn==true over an EMPTY
// keychain — the headless client routes every op to cloud with no interactive
// login. Without a token, the keychain-driven interactive selection is retained
// unchanged (LoggedIn==false over the same empty store).
//
// Seam-override idiom mirrors client_keepalive_gate_test.go: override the
// package vars (newAuthStoreFn, startKeepaliveFn) and inject Config.LocalDialer
// so the constructor never touches the real keychain or dials a real server.
func TestConstructClient_MachineAuthWiring(t *testing.T) {
	// LocalDialer hands constructClient a throwaway *GraphClient pointed at an
	// unused URL — no RPC is issued in this test, so it never dials.
	dialer := func(int) *graphclient.GraphClient {
		return graphclient.NewGraphClientForURL("http://local.invalid")
	}

	build := func(t *testing.T, cfg Config) *client {
		t.Helper()

		// Empty store → never logged in via the keychain. Machine-auth, when
		// engaged, must select cloud independently of this.
		store := newFakeAuthStore()
		origStore := newAuthStoreFn
		newAuthStoreFn = func() (auth.Store, error) { return store, nil }
		t.Cleanup(func() { newAuthStoreFn = origStore })

		// Stub the keepalive so the gate decision does not launch a real
		// goroutine or dial loopback.
		origKeepalive := startKeepaliveFn
		startKeepaliveFn = func(_ *graphclient.GraphClient, _ context.Context) {}
		t.Cleanup(func() { startKeepaliveFn = origKeepalive })

		cfg.LocalDialer = dialer
		c := constructClient(cfg)
		require.NotNil(t, c)
		return c
	}

	t.Run("machine_token_selects_cloud_without_keychain", func(t *testing.T) {
		c := build(t, Config{AuthToken: "machine-tok"})
		assert.True(t, c.router.LoggedIn(context.Background()),
			"a machine-token client must report LoggedIn==true (cloud-only) over an empty keychain")
	})

	t.Run("no_token_retains_interactive_selection", func(t *testing.T) {
		c := build(t, Config{})
		assert.False(t, c.router.LoggedIn(context.Background()),
			"with no machine token and an empty keychain, the interactive selection is retained (LoggedIn==false)")
	})
}

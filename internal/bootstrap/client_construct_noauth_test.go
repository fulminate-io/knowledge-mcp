// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// TestConstructClient_NoAuth_ForcesLocalOnly is the fail-closed proof for the
// --no-auth flag. It exercises the REAL constructClient so the guarantee is
// asserted against the production wiring, not a hand-built *client.
//
// The chokepoint is Router.pick (router.go:173): cloud iff
// machineAuth || authState.IsLoggedIn. --no-auth must defeat BOTH triggers at
// once, even with FULL cloud credentials present. Each subtest therefore seeds
// the keychain with a live refresh token AND sets Config.AuthToken — the exact
// state that would route cloud without the flag — and proves the flag still
// pins the client to local.
//
// Seam-override idiom copied from client_construct_machineauth_test.go /
// router_e2e_test.go's TestConstructClient_Coexistence: override the package
// vars (newAuthStoreFn, startKeepaliveFn) and inject Config.LocalDialer so the
// constructor never touches the real keychain or dials loopback. Over-the-wire-
// free: the routing subtest points LocalDialer at an httptest counting engine.
func TestConstructClient_NoAuth_ForcesLocalOnly(t *testing.T) {
	ctx := context.Background()

	// withConstructClientSeams overrides the keychain store + keepalive package
	// vars so constructClient never touches the real keychain or dials loopback,
	// then builds a *client via the production constructor. localURL pins the
	// local *GraphClient at a caller-supplied URL (an httptest counting engine
	// in the routing subtest; an unused URL otherwise). Copied from
	// TestConstructClient_Coexistence's function-local closure (it is not
	// callable by reference).
	withConstructClientSeams := func(t *testing.T, cfg Config, store auth.Store, localURL string) *client {
		t.Helper()
		cfg.LocalDialer = func(int) *graphclient.GraphClient {
			return graphclient.NewGraphClientForURL(localURL)
		}
		origStore := newAuthStoreFn
		newAuthStoreFn = func() (auth.Store, error) { return store, nil }
		t.Cleanup(func() { newAuthStoreFn = origStore })
		origKeepalive := startKeepaliveFn
		startKeepaliveFn = func(_ *graphclient.GraphClient, _ context.Context) {}
		t.Cleanup(func() { startKeepaliveFn = origKeepalive })
		c := constructClient(cfg)
		require.NotNil(t, c)
		return c
	}

	t.Run("LoggedInFalse_DespiteSeededTokenAndAuthToken", func(t *testing.T) {
		// PRIMARY assertion: through the real constructClient, NoAuth=true forces
		// LoggedIn==false even though BOTH cloud triggers are armed — a live
		// refresh token sits in the keychain AND Config.AuthToken is non-empty.
		// This is the fail-closed guarantee: neither credential can re-enable
		// cloud routing under --no-auth.
		store := newFakeAuthStore()
		require.NoError(t, store.Set(ctx, auth.KeyRefreshToken, "frt-stub"))
		c := withConstructClientSeams(t,
			Config{NoAuth: true, AuthToken: "machine-tok"}, store, "http://local.invalid")
		assert.False(t, c.router.LoggedIn(ctx),
			"--no-auth must force LoggedIn==false even with a seeded keychain refresh token AND a machine --auth-token present")
	})

	t.Run("RoutingLandsLocal_NotCloud", func(t *testing.T) {
		// SECONDARY assertion: the picked backend is provably LOCAL. Drive a real
		// dispatch through the production constructClient wiring (NoAuth=true,
		// both credentials armed) and confirm the local counting engine serviced
		// the call while the cloud engine stayed at zero — no routed call reaches
		// cloud under --no-auth.
		localURL, localEng := startCountingEngine(t)
		_, cloudEng := startCountingEngine(t) // stood up only to prove it is NOT hit
		store := newFakeAuthStore()
		require.NoError(t, store.Set(ctx, auth.KeyRefreshToken, "frt-stub"))
		c := withConstructClientSeams(t,
			Config{NoAuth: true, AuthToken: "machine-tok"}, store, localURL)

		searchArgs := json.RawMessage(`{"query":"x","graph":"knowledge"}`)
		_, err := c.engineDispatch(ctx, "search", searchArgs)
		require.NoError(t, err)
		assert.Equal(t, int32(1), localEng.execute.Load(),
			"--no-auth must route the dispatch to the LOCAL engine")
		assert.Equal(t, int32(0), cloudEng.execute.Load(),
			"--no-auth must never route any op to cloud, even with a seeded token + machine auth-token")
	})

	t.Run("NegativeControl_NoAuthFalse_SeededTokenRoutesCloud", func(t *testing.T) {
		// NEGATIVE CONTROL: with NoAuth=false and the same seeded refresh token,
		// the default behavior is unchanged — LoggedIn==true and routing reaches
		// cloud. Proves the gate is the cause of the local pinning above, not an
		// artifact of the harness. Mirrors
		// TestRouterE2E ... Local_Auth_RoutesCloud / the machine-auth regression
		// guard.
		_, localEng := startCountingEngine(t)
		cloudURL, cloudEng := startCountingEngine(t)
		store := newFakeAuthStore()
		require.NoError(t, store.Set(ctx, auth.KeyRefreshToken, "frt-stub"))
		c := buildE2EClient(
			graphclient.NewGraphClientForURL("http://local.invalid"), cloudURL, store, time.Hour)
		assert.True(t, c.router.LoggedIn(ctx),
			"NoAuth=false with a seeded refresh token must follow the unchanged login selection (LoggedIn==true)")

		searchArgs := json.RawMessage(`{"query":"x","graph":"knowledge"}`)
		_, err := c.engineDispatch(ctx, "search", searchArgs)
		require.NoError(t, err)
		assert.Equal(t, int32(0), localEng.execute.Load(),
			"without --no-auth, a logged-in client must not route to local")
		assert.Equal(t, int32(1), cloudEng.execute.Load(),
			"without --no-auth, a seeded refresh token still routes cloud (default behavior unchanged)")
	})
}

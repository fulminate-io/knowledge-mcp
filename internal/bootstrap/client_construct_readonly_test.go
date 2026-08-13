// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// TestSelectAuthSources_ReadOnlyLeverSelectsTheReadOnlySource completes the
// read-only credential path. A process under the lever must serve the session
// its owner published or fail — it must never be handed a source that would
// attempt a network refresh, because that refresh rotates the refresh token
// and persisting the replacement is exactly what the lever forbids. The only
// way such a refresh could end is by stranding every sibling process on a
// consumed credential.
//
// Hermetic throughout: an in-memory store fake, no network, no real
// credential store.
func TestSelectAuthSources_ReadOnlyLeverSelectsTheReadOnlySource(t *testing.T) {
	withFakeStore := func(t *testing.T) {
		t.Helper()
		store := newFakeAuthStore()
		orig := newAuthStoreFn
		newAuthStoreFn = func() (auth.Store, error) { return store, nil }
		t.Cleanup(func() { newAuthStoreFn = orig })
	}

	t.Run("lever engaged selects a source that cannot refresh", func(t *testing.T) {
		withFakeStore(t)
		t.Setenv(auth.CredentialStoreReadOnlyEnv, "1")

		_, src, machineAuth := selectAuthSources(Config{})
		if _, ok := src.(*auth.ReadOnlyTokenSource); !ok {
			t.Fatalf("token source = %T, want *auth.ReadOnlyTokenSource", src)
		}
		// The structural half: even via the interface, nothing can force a
		// refresh through this source.
		if _, ok := src.(auth.RefreshingTokenSource); ok {
			t.Error("the source selected under the lever can still force a refresh")
		}
		if machineAuth {
			t.Error("machineAuth must stay false — the lever grants no cloud trigger of its own")
		}
	})

	// Known-positive control: the same call without the lever still builds the
	// refreshing source. Without this arm, a selectAuthSources that returned a
	// read-only source unconditionally would look identical above while
	// silently disabling refresh for every ordinary daemon.
	t.Run("control: without the lever the refreshing source is selected", func(t *testing.T) {
		withFakeStore(t)

		_, src, machineAuth := selectAuthSources(Config{})
		if _, ok := src.(*auth.OAuthTokenSource); !ok {
			t.Fatalf("token source = %T, want *auth.OAuthTokenSource", src)
		}
		if machineAuth {
			t.Error("machineAuth must be false on the interactive keychain path")
		}
	})

	// A caller who supplied their own credential is not reaching for the
	// operator's, so the machine token outranks the lever.
	t.Run("a supplied machine token outranks the lever", func(t *testing.T) {
		withFakeStore(t)
		t.Setenv(auth.CredentialStoreReadOnlyEnv, "1")

		_, src, machineAuth := selectAuthSources(Config{AuthToken: "machine-bearer"})
		if _, ok := src.(auth.StaticTokenSource); !ok {
			t.Fatalf("token source = %T, want auth.StaticTokenSource", src)
		}
		if !machineAuth {
			t.Error("machineAuth must be true for a supplied machine token")
		}
	})

	// --no-auth is the fail-closed floor and outranks everything, including
	// the lever: no source that could reach a cloud host at all.
	t.Run("no-auth outranks the lever", func(t *testing.T) {
		withFakeStore(t)
		t.Setenv(auth.CredentialStoreReadOnlyEnv, "1")

		store, src, machineAuth := selectAuthSources(Config{NoAuth: true})
		if _, ok := src.(auth.StaticTokenSource); !ok {
			t.Fatalf("token source = %T, want the zero-IO auth.StaticTokenSource", src)
		}
		if _, ok := store.(noopAuthStore); !ok {
			t.Errorf("store = %T, want noopAuthStore", store)
		}
		if machineAuth {
			t.Error("machineAuth must be false under --no-auth")
		}
	})
}

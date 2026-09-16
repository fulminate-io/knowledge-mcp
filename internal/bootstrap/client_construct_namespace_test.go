// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// withStoreError points the serve path's store seam at a constructor failing
// with a chosen error class, so each production class that used to be
// swallowed can be driven without a real store.
func withStoreError(t *testing.T, err error) {
	t.Helper()
	orig := newAuthStoreFn
	newAuthStoreFn = func() (auth.Store, error) { return nil, err }
	t.Cleanup(func() { newAuthStoreFn = orig })
}

// TestSelectAuthSources_StoreErrorsAreFatal pins the lift: NO store
// construction error reaches noopAuthStore any more. One row per production
// error class that can arrive here — an unresolvable home, an undirectory-able
// ~/.knowledge, and a malformed credential-namespace selector — because the
// swallow was lifted whole rather than for the selector alone.
//
// The error classes are the ones auth.OpenStore can actually return; they are
// injected through the seam because neither filesystem class can be provoked
// from inside a test binary, where the real store constructor refuses first.
func TestSelectAuthSources_StoreErrorsAreFatal(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{
			name: "an unresolvable home directory",
			err:  fmt.Errorf("auth: resolve home directory: %w", errors.New("$HOME is not defined")),
		},
		{
			name: "an undirectory-able ~/.knowledge",
			err:  fmt.Errorf("auth: create %q: %w", "/nope/.knowledge", os.ErrPermission),
		},
		{
			name: "a malformed credential namespace",
			err:  fmt.Errorf("%w: %s=%q", auth.ErrCredentialNamespaceInvalid, auth.CredentialNamespaceEnv, "Dev"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withStoreError(t, tc.err)

			store, src, machineAuth, err := selectAuthSources(Config{})
			if !errors.Is(err, tc.err) {
				t.Fatalf("selectAuthSources returned err=%v, want the store error to propagate", err)
			}
			if _, ok := store.(noopAuthStore); ok {
				t.Error("a store-construction error still degraded to noopAuthStore — the swallow is back")
			}
			if store != nil || src != nil || machineAuth {
				t.Errorf("selectAuthSources returned (%T, %T, %v) alongside an error, want the zero values", store, src, machineAuth)
			}
		})
	}

	// Known-positive control: a store that constructs is still served, so the
	// rows above are discriminating on the error and not refusing everything.
	t.Run("control-a-healthy-store-still-builds-the-sources", func(t *testing.T) {
		fake := newFakeAuthStore()
		orig := newAuthStoreFn
		newAuthStoreFn = func() (auth.Store, error) { return fake, nil }
		t.Cleanup(func() { newAuthStoreFn = orig })

		store, src, _, err := selectAuthSources(Config{})
		if err != nil {
			t.Fatalf("selectAuthSources with a healthy store = %v, want no error", err)
		}
		if store == nil || src == nil {
			t.Fatalf("selectAuthSources returned (%v, %v), want both sources", store, src)
		}
	})

	// --no-auth is the fail-closed floor and never consults the store at all,
	// so it cannot be made to fail by a store error.
	t.Run("no-auth-never-opens-the-store-and-so-never-fails", func(t *testing.T) {
		withStoreError(t, errors.New("must not be consulted"))

		store, _, _, err := selectAuthSources(Config{NoAuth: true})
		if err != nil {
			t.Fatalf("selectAuthSources under --no-auth = %v, want no error", err)
		}
		if _, ok := store.(noopAuthStore); !ok {
			t.Errorf("store = %T, want noopAuthStore under --no-auth", store)
		}
	})
}

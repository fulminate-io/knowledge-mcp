// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"testing"
)

// openStoreOverFake points the selection at an in-memory keychain and a temp
// HOME, so OpenStore can be exercised without the real credential store —
// which the testing.Testing() guard refuses to hand a test anyway.
func openStoreOverFake(t *testing.T) (Store, *writeRecorderStore) {
	t.Helper()
	credentialsPathInTempHome(t)

	rec := &writeRecorderStore{testStore: newTestStore()}
	newKeychainStoreFn = func() (Store, error) { return rec, nil }
	t.Cleanup(func() { newKeychainStoreFn = NewStore })

	store, err := OpenStore()
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	return store, rec
}

// TestOpenStore_ReadOnlyLever pins the lever's two halves: under it reads
// still work and writes refuse loudly. Both arms are required — a lever that
// broke reads would strand the very processes it exists to serve, since a
// process that cannot read has to authenticate, and authenticating writes.
func TestOpenStore_ReadOnlyLever(t *testing.T) {
	ctx := context.Background()

	t.Run("writes refuse and never reach the store", func(t *testing.T) {
		t.Setenv(CredentialStoreReadOnlyEnv, "1")
		store, rec := openStoreOverFake(t)

		if err := store.Set(ctx, KeyRefreshToken, "should-not-land"); !errors.Is(err, errCredentialStoreReadOnly) {
			t.Fatalf("Set under the lever = %v, want errCredentialStoreReadOnly", err)
		}
		if err := store.Delete(ctx, KeyRefreshToken); !errors.Is(err, errCredentialStoreReadOnly) {
			t.Fatalf("Delete under the lever = %v, want errCredentialStoreReadOnly", err)
		}
		if got := rec.recorded(); len(got) != 0 {
			t.Errorf("a refused write still reached the backing store: %v", got)
		}
	})

	t.Run("reads pass through", func(t *testing.T) {
		t.Setenv(CredentialStoreReadOnlyEnv, "1")
		store, rec := openStoreOverFake(t)
		// Seed through the BACKING store: the lever must not stop the
		// session owner's already-stored credential from being read.
		if err := rec.testStore.Set(ctx, KeyRefreshToken, "rt-existing"); err != nil {
			t.Fatalf("seed: %v", err)
		}

		got, err := store.Get(ctx, KeyRefreshToken)
		if err != nil {
			t.Fatalf("Get under the lever: %v", err)
		}
		if got != "rt-existing" {
			t.Errorf("Get under the lever = %q, want the stored value", got)
		}
	})

	// Known-positive control: without the lever the very same call lands.
	// Without this arm a wrapper that refused writes unconditionally — or a
	// fake that silently dropped them — would look identical above.
	t.Run("control: without the lever writes land", func(t *testing.T) {
		store, rec := openStoreOverFake(t)

		if err := store.Set(ctx, KeyRefreshToken, "rt-written"); err != nil {
			t.Fatalf("Set without the lever: %v", err)
		}
		if got := rec.recorded(); len(got) != 1 || got[0] != "set:"+KeyRefreshToken {
			t.Fatalf("write did not reach the backing store: %v", got)
		}
		if got, err := rec.testStore.Get(ctx, KeyRefreshToken); err != nil || got != "rt-written" {
			t.Errorf("backing store holds (%q, %v), want the written value", got, err)
		}
	})
}

// TestCredentialStoreIsReadOnly_ValueParsing pins the lever's fail-safe
// direction: an unrecognized value engages read-only mode rather than
// silently leaving the store writable.
func TestCredentialStoreIsReadOnly_ValueParsing(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"yes", true},
		{"anything-unrecognized", true},
		{" 1 ", true},
		{"", false},
		{"0", false},
		{"false", false},
		{"False", false},
	}

	for _, tc := range cases {
		t.Run("value_"+tc.value, func(t *testing.T) {
			t.Setenv(CredentialStoreReadOnlyEnv, tc.value)
			if got := CredentialStoreIsReadOnly(); got != tc.want {
				t.Errorf("CredentialStoreIsReadOnly() with %q = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

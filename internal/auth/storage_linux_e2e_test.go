// SPDX-License-Identifier: Apache-2.0

//go:build linux && linux_keyring_e2e

// Package auth linux SecretService end-to-end test. Runs ONLY when the
// `linux_keyring_e2e` build tag is set, because it requires a running dbus
// session bus and a SecretService-compatible daemon (gnome-keyring-daemon,
// KeePassXC, etc.) on the host.
//
// Run with: go test -tags=linux_keyring_e2e ./auth/
package auth

import (
	"context"
	"errors"
	"testing"
)

func TestLinuxStoreRoundtrip(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	const key = "e2e_roundtrip_token"
	const value = "e2e-value-linux-1234567890"

	// Clean slate — ignore ErrNotFound.
	if err := store.Delete(ctx, key); err != nil && !errors.Is(err, ErrNotFound) {
		t.Fatalf("pre-clean Delete: %v", err)
	}

	if err := store.Set(ctx, key, value); err != nil {
		t.Fatalf("Set: %v", err)
	}
	t.Cleanup(func() { _ = store.Delete(ctx, key) })

	got, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != value {
		t.Fatalf("Get: want %q, got %q", value, got)
	}

	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := store.Get(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete: want ErrNotFound, got %v", err)
	}
}

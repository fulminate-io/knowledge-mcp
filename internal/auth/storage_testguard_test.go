// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestNewStore_RefusesInsideTestBinary is the enforcement test for the rule
// that tests must use in-memory fakes; the real credential store is
// off-limits to test binaries. It is the one test that reaches for the real
// platform constructor, and it does so precisely to prove the constructor
// cannot be used — a store it handed back would be the developer's own
// keychain.
func TestNewStore_RefusesInsideTestBinary(t *testing.T) {
	store, err := NewStore()
	if !errors.Is(err, errRealStoreInTest) {
		t.Fatalf("NewStore() inside a test binary returned err=%v, want errRealStoreInTest", err)
	}
	if store != nil {
		t.Errorf("NewStore() handed back a usable %T alongside the refusal", store)
	}
}

// TestOpenStore_RefusesInsideTestBinary covers the seam a future test in any
// package is likeliest to reach for by accident. OpenStore selects through
// the real platform constructor, so the refusal must PROPAGATE: it must not
// be classified as "backend unavailable" and quietly downgraded to a
// file-backed store, which would write plaintext credentials to the
// developer's home directory instead.
func TestOpenStore_RefusesInsideTestBinary(t *testing.T) {
	// Pin the real constructor in place so this cannot pass against a fake
	// left behind by the selection tests.
	newKeychainStoreFn = NewStore
	t.Cleanup(func() { newKeychainStoreFn = NewStore })

	store, err := OpenStore()
	if !errors.Is(err, errRealStoreInTest) {
		t.Fatalf("OpenStore() inside a test binary returned err=%v, want the refusal to propagate", err)
	}
	if store != nil {
		t.Errorf("OpenStore() handed back a usable %T alongside the refusal", store)
	}
}

// TestNewFileStore_GuardIsScopedToTheRealHome pins that the file-store guard
// discriminates by target path rather than refusing everything inside a test
// binary. Both arms matter: without the refused arm the rule is unenforced,
// and without the allowed arm a guard that blanket-refused would look
// identical here while breaking every hermetic file-store test in the
// package.
func TestNewFileStore_GuardIsScopedToTheRealHome(t *testing.T) {
	t.Run("real home is refused", func(t *testing.T) {
		// HOME is deliberately NOT redirected. The guard runs before
		// newFileStore touches the filesystem, so reaching this line cannot
		// create or modify anything under the real home.
		store, err := newFileStore()
		if !errors.Is(err, errRealStoreInTest) {
			t.Fatalf("newFileStore() targeting the real home returned err=%v, want errRealStoreInTest", err)
		}
		if store != nil {
			t.Errorf("newFileStore() handed back a usable %T alongside the refusal", store)
		}
	})

	t.Run("redirected home is allowed", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		store, err := newFileStore()
		if err != nil {
			t.Fatalf("newFileStore() under a redirected HOME must be allowed, got %v", err)
		}
		fs, ok := store.(*fileStore)
		if !ok {
			t.Fatalf("newFileStore() returned %T, want *fileStore", store)
		}
		if want := filepath.Join(home, ".knowledge", credentialsFileName); fs.path != want {
			t.Errorf("file store path = %q, want %q", fs.path, want)
		}
	})
}

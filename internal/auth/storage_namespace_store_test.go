// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// serviceRecordingKeychain captures the service identifier the keychain
// constructor was handed. The service is what the namespace selector is FOR,
// so a fake that did not record it could not tell an honored selector from an
// ignored one.
type serviceRecordingKeychain struct {
	*testStore
	service string
}

// recordingKeychainFn points the keychain seam at a fake that records its
// service, and hands back the pointer the test asserts on. The seam is
// restored in t.Cleanup, as every other test in this package does.
func recordingKeychainFn(t *testing.T) **serviceRecordingKeychain {
	t.Helper()
	slot := new(*serviceRecordingKeychain)
	newKeychainStoreFn = func(service string) (Store, error) {
		*slot = &serviceRecordingKeychain{testStore: newTestStore(), service: service}
		return *slot, nil
	}
	t.Cleanup(func() { newKeychainStoreFn = NewStore })
	return slot
}

// TestOpenStore_KeychainServiceUnderNamespace is R1's unit half: under a
// namespace the keychain store is opened against the DERIVED service and never
// the default one. The expectation is a literal computed here, not a second
// call to the production deriver.
func TestOpenStore_KeychainServiceUnderNamespace(t *testing.T) {
	credentialsPathInTempHome(t)
	setNamespaceEnv(t, namespaceSet("dev"))
	slot := recordingKeychainFn(t)

	store, err := OpenStore()
	if err != nil {
		t.Fatalf("OpenStore under a namespace: %v", err)
	}
	if *slot == nil {
		t.Fatal("OpenStore did not construct the keychain store")
	}
	if got, want := (*slot).service, "io.fulminate.knowledge.dev"; got != want {
		t.Fatalf("keychain opened against service %q, want %q", got, want)
	}
	if (*slot).service == ServiceName {
		t.Fatal("keychain opened against the DEFAULT service under a namespace")
	}
	// The store handed back is the namespaced one, so every later Get/Set/Delete
	// this process performs reaches that service and no other.
	if err := store.Set(context.Background(), KeyRefreshToken, "rt"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if v, err := (*slot).testStore.Get(context.Background(), KeyRefreshToken); err != nil || v != "rt" {
		t.Fatalf("namespaced backend holds (%q, %v), want the written value", v, err)
	}
}

// TestOpenStore_KeychainServiceUnsetIsTheDefault is R2's service-name row: the
// name is asserted against the exported const, never against a re-typed
// literal, so a change to the const cannot pass unnoticed here.
func TestOpenStore_KeychainServiceUnsetIsTheDefault(t *testing.T) {
	credentialsPathInTempHome(t)
	setNamespaceEnv(t, namespaceUnset())
	slot := recordingKeychainFn(t)

	if _, err := OpenStore(); err != nil {
		t.Fatalf("OpenStore with the selector unset: %v", err)
	}
	if *slot == nil {
		t.Fatal("OpenStore did not construct the keychain store")
	}
	if got := (*slot).service; got != ServiceName {
		t.Fatalf("keychain opened against service %q, want the default %q byte for byte", got, ServiceName)
	}
}

// TestOpenStore_RefusesAMalformedNamespace pins that the refusal reaches every
// opener through OpenStore itself, and that no store is constructed for a
// refused value — the keychain is never touched at all.
func TestOpenStore_RefusesAMalformedNamespace(t *testing.T) {
	credentialsPathInTempHome(t)
	setNamespaceEnv(t, namespaceSet("Dev"))
	slot := recordingKeychainFn(t)

	store, err := OpenStore()
	if !errors.Is(err, ErrCredentialNamespaceInvalid) {
		t.Fatalf("OpenStore with a malformed selector = (%v, %v), want ErrCredentialNamespaceInvalid", store, err)
	}
	if store != nil {
		t.Errorf("OpenStore returned a store %T alongside a refusal", store)
	}
	if *slot != nil {
		t.Errorf("OpenStore constructed a keychain store for a refused selector (service %q)", (*slot).service)
	}
}

// TestOpenStore_RefusalIsTheSamePredicateTheCallersUse is the agreement leg:
// OpenStore must refuse exactly what CredentialNamespace refuses, because the
// six Desktop entry points call the predicate and OpenStore enforces it. Two
// implementations with equal regexps would pass a table run twice; running
// BOTH over the same values in one pass is what proves they do not disagree.
func TestOpenStore_RefusalIsTheSamePredicateTheCallersUse(t *testing.T) {
	for _, raw := range []string{"", " ", "Dev", "-dev", "dev_1", "dev.1", "dev\n", "io.fulminate.knowledge"} {
		t.Run("value_"+raw, func(t *testing.T) {
			credentialsPathInTempHome(t)
			setNamespaceEnv(t, namespaceSet(raw))
			recordingKeychainFn(t)

			_, predicateErr := CredentialNamespace()
			_, openErr := OpenStore()
			if !errors.Is(predicateErr, ErrCredentialNamespaceInvalid) {
				t.Fatalf("CredentialNamespace(%q) = %v, want a refusal", raw, predicateErr)
			}
			if !errors.Is(openErr, ErrCredentialNamespaceInvalid) {
				t.Fatalf("OpenStore with %q = %v, but the predicate refused it — the two disagree", raw, openErr)
			}
		})
	}
	t.Run("and-agree-on-an-accepted-value", func(t *testing.T) {
		credentialsPathInTempHome(t)
		setNamespaceEnv(t, namespaceSet("dev"))
		recordingKeychainFn(t)
		if _, err := CredentialNamespace(); err != nil {
			t.Fatalf("CredentialNamespace(\"dev\") = %v, want acceptance", err)
		}
		if _, err := OpenStore(); err != nil {
			t.Fatalf("OpenStore with \"dev\" = %v, want acceptance", err)
		}
	})
}

// unavailableKeychainFn forces the file-store fallback by handing back a
// keychain whose probe proves the backend unreachable.
func unavailableKeychainFn(t *testing.T) {
	t.Helper()
	newKeychainStoreFn = func(string) (Store, error) {
		return errGetStore{testStore: newTestStore(), getErr: wrappedExecNotFound(t)}, nil
	}
	t.Cleanup(func() { newKeychainStoreFn = NewStore })
}

// TestFileStore_NamespacedFile is R5: the file-backed fallback keys its file by
// the namespace, so two namespaces never share one credentials file and
// neither shares the default one.
func TestFileStore_NamespacedFile(t *testing.T) {
	ctx := context.Background()
	// The package's own temp-HOME helper, rather than a second HOME
	// redirection of this test's own: the file store resolves its directory
	// through os.UserHomeDir and has no injectable resolver, so one helper is
	// where that redirection lives.
	defaultPath := credentialsPathInTempHome(t)
	dir := filepath.Dir(defaultPath)
	pathA := filepath.Join(dir, "credentials.alpha")
	pathB := filepath.Join(dir, "credentials.beta")
	unavailableKeychainFn(t)

	openUnder := func(t *testing.T, ns namespaceEnv) Store {
		t.Helper()
		setNamespaceEnv(t, ns)
		store, err := OpenStore()
		if err != nil {
			t.Fatalf("OpenStore under %q: %v", ns.describe(), err)
		}
		return store
	}

	// A pre-existing default-namespace credential must survive untouched: this
	// is the operator's login, and leaving it alone is the point of the ticket.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create .knowledge: %v", err)
	}
	const seeded = `{"refresh_token":"rt-default"}`
	if err := os.WriteFile(defaultPath, []byte(seeded), 0o600); err != nil {
		t.Fatalf("seed default credentials: %v", err)
	}

	t.Run("write-under-alpha-lands-in-the-alpha-file", func(t *testing.T) {
		if err := openUnder(t, namespaceSet("alpha")).Set(ctx, KeyRefreshToken, "rt-alpha"); err != nil {
			t.Fatalf("Set under alpha: %v", err)
		}
		if _, err := os.Stat(pathA); err != nil {
			t.Fatalf("stat %s: %v", pathA, err)
		}
	})

	t.Run("read-under-alpha-returns-it", func(t *testing.T) {
		got, err := openUnder(t, namespaceSet("alpha")).Get(ctx, KeyRefreshToken)
		if err != nil || got != "rt-alpha" {
			t.Fatalf("Get under alpha = (%q, %v), want the written value", got, err)
		}
	})

	t.Run("read-under-beta-does-not-see-alpha", func(t *testing.T) {
		if _, err := openUnder(t, namespaceSet("beta")).Get(ctx, KeyRefreshToken); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get under beta = %v, want ErrNotFound", err)
		}
		if _, err := os.Stat(pathB); !os.IsNotExist(err) {
			t.Fatalf("stat %s = %v, want the beta file to be absent after a miss", pathB, err)
		}
	})

	t.Run("read-with-the-selector-unset-does-not-see-alpha", func(t *testing.T) {
		got, err := openUnder(t, namespaceUnset()).Get(ctx, KeyRefreshToken)
		if err != nil || got != "rt-default" {
			t.Fatalf("Get with the selector unset = (%q, %v), want the pre-existing default credential", got, err)
		}
	})

	t.Run("delete-under-alpha-leaves-the-default-file-byte-identical", func(t *testing.T) {
		if err := openUnder(t, namespaceSet("alpha")).Delete(ctx, KeyRefreshToken); err != nil {
			t.Fatalf("Delete under alpha: %v", err)
		}
		if _, err := openUnder(t, namespaceSet("alpha")).Get(ctx, KeyRefreshToken); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get under alpha after Delete = %v, want ErrNotFound", err)
		}
		data, err := os.ReadFile(defaultPath)
		if err != nil {
			t.Fatalf("re-read default credentials: %v", err)
		}
		if string(data) != seeded {
			t.Fatalf("default credentials changed to %q — a namespaced write reached the default file", string(data))
		}
	})
}

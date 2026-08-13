// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// errGetStore is a keychain fake whose Get fails with a chosen error, so the
// selection tests can drive each error class through OpenStore. Set and
// Delete delegate to the embedded testStore, so a store that is handed back
// to the caller still records writes.
type errGetStore struct {
	*testStore
	getErr error
}

func (s errGetStore) Get(_ context.Context, _ string) (string, error) {
	return "", s.getErr
}

// wrappedExecNotFound builds the error shape a container without dbus-launch
// produces: an *exec.Error carrying exec.ErrNotFound, wrapped the way the
// keychain backend wraps it.
func wrappedExecNotFound(t *testing.T) error {
	t.Helper()
	_, err := exec.LookPath("dbus-launch-definitely-absent-xyz")
	if err == nil {
		t.Fatal("expected the probe binary to be absent from PATH")
	}
	return fmt.Errorf("auth: secretservice get %q: %w", KeyClientID, err)
}

// wrappedExitError builds the error shape a denied keychain prompt produces:
// an *exec.ExitError from a real non-zero exit, wrapped the same way.
func wrappedExitError(t *testing.T) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit 1").Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected an *exec.ExitError, got %T (%v)", err, err)
	}
	return fmt.Errorf("auth: keychain get %q: %w", KeyClientID, err)
}

// credentialsPathInTempHome points HOME at a fresh temp dir and returns the
// path the file store would use there.
func credentialsPathInTempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return filepath.Join(home, ".knowledge", credentialsFileName)
}

func TestIsBackendUnavailable(t *testing.T) {
	if isBackendUnavailable(nil) {
		t.Fatal("nil classified as unavailable")
	}
	if isBackendUnavailable(fmt.Errorf("wrapped: %w", ErrNotFound)) {
		t.Fatal("ErrNotFound classified as unavailable — it proves the backend works")
	}
	if isBackendUnavailable(fmt.Errorf("wrapped: %w", ErrNotImplementedOS)) {
		t.Fatal("ErrNotImplementedOS classified as unavailable — Windows must keep today's behavior")
	}
	if !isBackendUnavailable(wrappedExecNotFound(t)) {
		t.Fatal("a wrapped exec.ErrNotFound was not classified as unavailable")
	}
	if isBackendUnavailable(wrappedExitError(t)) {
		t.Fatal("a wrapped *exec.ExitError classified as unavailable — a denial is not an absence")
	}
}

func TestOpenStore_PrefersKeychainWhenAvailable(t *testing.T) {
	path := credentialsPathInTempHome(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create credentials dir: %v", err)
	}
	const seeded = `{"refresh_token":"rt-from-file"}`
	if err := os.WriteFile(path, []byte(seeded), 0o600); err != nil {
		t.Fatalf("seed credentials file: %v", err)
	}

	keychain := newTestStore()
	newKeychainStoreFn = func() (Store, error) { return keychain, nil }
	t.Cleanup(func() { newKeychainStoreFn = NewStore })

	got, err := OpenStore()
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if got != Store(keychain) {
		t.Fatalf("OpenStore returned %T, want the keychain even though a credentials file exists", got)
	}
	if err := got.Set(context.Background(), KeyRefreshToken, "rt-from-keychain"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if v, err := keychain.Get(context.Background(), KeyRefreshToken); err != nil || v != "rt-from-keychain" {
		t.Fatalf("keychain holds (%q, %v), want the written value", v, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read credentials file: %v", err)
	}
	if string(data) != seeded {
		t.Fatalf("credentials file changed to %q — the keychain write leaked to disk", string(data))
	}
}

func TestOpenStore_FallsBackWhenBackendUnavailable(t *testing.T) {
	path := credentialsPathInTempHome(t)

	newKeychainStoreFn = func() (Store, error) {
		return errGetStore{testStore: newTestStore(), getErr: wrappedExecNotFound(t)}, nil
	}
	t.Cleanup(func() { newKeychainStoreFn = NewStore })

	got, err := OpenStore()
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if err := got.Set(context.Background(), KeyRefreshToken, "rt-on-disk"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("credentials file after fallback Set: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("credentials mode = %#o, want 0600", perm)
	}
	v, err := got.Get(context.Background(), KeyRefreshToken)
	if err != nil || v != "rt-on-disk" {
		t.Fatalf("fallback store holds (%q, %v), want the written value", v, err)
	}
}

func TestOpenStore_KeychainDenialIsNotShadowed(t *testing.T) {
	path := credentialsPathInTempHome(t)

	denied := errGetStore{testStore: newTestStore(), getErr: wrappedExitError(t)}
	newKeychainStoreFn = func() (Store, error) { return denied, nil }
	t.Cleanup(func() { newKeychainStoreFn = NewStore })

	got, err := OpenStore()
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if got != Store(denied) {
		t.Fatalf("OpenStore returned %T, want the keychain — a denial must not fall back", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stat credentials = %v, want the file to be absent", err)
	}
	// Known-positive control: the absence above is only meaningful if this
	// probe can see the file when one is written.
	fs, err := newFileStore()
	if err != nil {
		t.Fatalf("newFileStore: %v", err)
	}
	if err := fs.Set(context.Background(), KeyRefreshToken, "control"); err != nil {
		t.Fatalf("control Set: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("control probe cannot see a written credentials file: %v", err)
	}
}

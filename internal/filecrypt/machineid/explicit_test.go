// SPDX-License-Identifier: Apache-2.0
package machineid

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExplicitCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "machine-id")
	if err := os.WriteFile(path, []byte("0123456789abcdef\n"), 0600); err != nil {
		t.Fatal(err)
	}
	id, err := resolveExplicitCache(path)
	if err != nil || id != "0123456789abcdef" {
		t.Fatalf("read explicit identity: %q %v", id, err)
	}
	for _, bad := range []string{"invalid", "abcd\n", "0123456789abcdef0123456789abcdef\n", "", "\n"} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveExplicitCache(path); err == nil {
			t.Errorf("malformed identity %q accepted", bad)
		}
	}
	if _, err := resolveExplicitCache(dir); err == nil {
		t.Fatal("directory accepted")
	}
	if _, err := resolveExplicitCache("relative"); err == nil {
		t.Fatal("relative path accepted")
	}
	fresh := filepath.Join(dir, "new", "machine-id")
	first, err := resolveExplicitCache(fresh)
	if err != nil || len(first) != 16 {
		t.Fatalf("initialize: %q %v", first, err)
	}
	second, err := resolveExplicitCache(fresh)
	if err != nil || first != second {
		t.Fatalf("identity changed: %v", err)
	}
}

func TestDefaultIdentityMatchesExplicitCache(t *testing.T) {
	home := t.TempDir()
	previousHome := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHomeDir = previousHome })
	want := "0123456789abcdef"
	if err := writeCache(want); err != nil {
		t.Fatal(err)
	}
	if got := resolveAndCache(); got != want {
		t.Fatalf("default cache = %q", got)
	}
	explicit := filepath.Join(home, ".knowledge", "machine-id")
	userHomeDir = func() (string, error) { t.Fatal("explicit cache consulted default home"); return "", nil }
	if got, err := resolveExplicitCache(explicit); err != nil || got != want {
		t.Fatalf("explicit cache = %q, %v", got, err)
	}
	if got, err := resolveExplicitCache(filepath.Join(t.TempDir(), "machine-id")); err != nil || len(got) != 16 {
		t.Fatalf("new explicit cache = %q, %v", got, err)
	}
}

// The legacy no-argument API is exercised with its home resolver injected;
// tests never read or write the operator's identity cache.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "machineid-tests-")
	if err != nil {
		panic(err)
	}
	userHomeDir = func() (string, error) { return dir, nil }
	code := m.Run()
	if err := os.RemoveAll(dir); err != nil {
		panic(err)
	}
	os.Exit(code)
}

// SPDX-License-Identifier: Apache-2.0

package bootstrap

import "testing"

// TestParseFlagsRootDirSet pins the --root was-set bit on the REAL ParseFlags
// path (not the injected astTestDeps fake). ParseFlags and runServe share the
// single applyRootDirSet helper (one assignment site), so a broken helper goes
// RED here. A defaulted-VALUE "." still counts as explicitly SET — the was-set
// signal comes from fs.Visit, never a value-compare against ".".
func TestParseFlagsRootDirSet(t *testing.T) {
	t.Run("explicit --root path sets the bit", func(t *testing.T) {
		cfg, err := ParseFlags([]string{"--root", "/x"})
		if err != nil {
			t.Fatalf("ParseFlags: %v", err)
		}
		if !cfg.RootDirSet {
			t.Fatal("RootDirSet = false, want true for explicit --root /x")
		}
	})

	t.Run("explicit --root . still counts as set", func(t *testing.T) {
		cfg, err := ParseFlags([]string{"--root", "."})
		if err != nil {
			t.Fatalf("ParseFlags: %v", err)
		}
		if !cfg.RootDirSet {
			t.Fatal("RootDirSet = false, want true for explicit --root . (value-compare against \".\" is rejected)")
		}
	})

	t.Run("no --root leaves the bit false", func(t *testing.T) {
		cfg, err := ParseFlags(nil)
		if err != nil {
			t.Fatalf("ParseFlags: %v", err)
		}
		if cfg.RootDirSet {
			t.Fatal("RootDirSet = true, want false when --root is omitted")
		}
	})
}

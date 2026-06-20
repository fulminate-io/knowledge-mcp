// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriterID proves the stable per-machine writer-id helper:
//   - on a MISSING cache it generates + persists an id, and the next call over
//     the same cache file (a simulated restart) reads back the SAME value;
//   - the generated id is the server machine-id format — exactly 16 lowercase
//     hex chars — so it round-trips through the server's hex-validating cache
//     read (option (a)) without being rejected/clobbered;
//   - a pre-seeded valid cache value is returned verbatim (the client is a
//     faithful consumer of an id the server may have written first).
func TestWriterID(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, ".knowledge", "machine-id")

	// MISS: first call generates + persists.
	first := writerIDFor(cache)
	assertHex16(t, first)

	// The id was persisted to the cache file.
	raw, err := os.ReadFile(cache)
	if err != nil {
		t.Fatalf("cache file must be written on miss: %v", err)
	}
	if len(raw) == 0 {
		t.Fatalf("persisted cache must be non-empty")
	}

	// Simulated RESTART: a fresh call over the SAME cache file returns the SAME id.
	second := writerIDFor(cache)
	if second != first {
		t.Fatalf("writer id must be stable across a restart (same cache file): %q != %q", first, second)
	}

	// Two calls in the same process are also stable (no regeneration once cached).
	if third := writerIDFor(cache); third != first {
		t.Fatalf("writer id must be stable across calls: %q != %q", first, third)
	}

	// PRE-SEEDED valid cache (as the server would write it): returned verbatim.
	seedDir := t.TempDir()
	seedCache := filepath.Join(seedDir, ".knowledge", "machine-id")
	if err := os.MkdirAll(filepath.Dir(seedCache), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const seeded = "a1b2c3d4e5f60718"
	if err := os.WriteFile(seedCache, []byte(seeded+"\n"), 0o600); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	if got := writerIDFor(seedCache); got != seeded {
		t.Fatalf("a pre-seeded valid cache must be returned verbatim; want %q got %q", seeded, got)
	}

	// A MALFORMED cache value (non-16-hex, e.g. a dashed UUID) is treated as
	// absent and replaced with a fresh valid 16-hex id — this is exactly the
	// clobber-avoidance the format-compat choice guarantees against the server's
	// hex-validating read.
	badDir := t.TempDir()
	badCache := filepath.Join(badDir, ".knowledge", "machine-id")
	if err := os.MkdirAll(filepath.Dir(badCache), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(badCache, []byte("550e8400-e29b-41d4-a716-446655440000\n"), 0o600); err != nil {
		t.Fatalf("bad seed write: %v", err)
	}
	repaired := writerIDFor(badCache)
	assertHex16(t, repaired)
	if repaired == "550e8400-e29b-41" {
		t.Fatalf("a malformed cache must not be returned (even truncated)")
	}
}

// assertHex16 fails unless s is exactly 16 lowercase hex chars — the server
// machine-id format the shared cache file must hold.
func assertHex16(t *testing.T, s string) {
	t.Helper()
	if len(s) != 16 {
		t.Fatalf("writer id must be 16 chars (server machine-id format), got %d: %q", len(s), s)
	}
	if s != strings.ToLower(s) {
		t.Fatalf("writer id must be lowercase hex, got %q", s)
	}
	if _, err := hex.DecodeString(s); err != nil {
		t.Fatalf("writer id must be valid hex, got %q: %v", s, err)
	}
}

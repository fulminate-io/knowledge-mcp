// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// writerID is the stable per-machine identity threaded onto every segment RPC as
// writer_id. It establishes "one writer per machine per account": two
// machines on one account are two distinct writers; a daemon restart on one
// machine keeps the SAME writer_id, so its manifest UPDATES rather than orphaning
// (which would leave its blobs pinned forever). It is NOT an account / auth
// identity — that would collide two machines on one account into a single writer
// and break the multi-writer reclaim model.
//
// FORMAT COMPATIBILITY (chosen option (a)): the value is the SAME 16-lowercase-hex
// format the server's machine identity uses, and it is read from / written to the
// SAME shared cache file (~/.knowledge/machine-id). The server reads that file
// through a hex-validating cache read that REJECTS anything not exactly 16
// lowercase hex chars (treating it as absent and re-deriving). By producing the
// identical hex shape, a value either side writes round-trips through the other's
// validation, so the shared file stays the single convergence point with no
// separate file and no who-writes-first ordering dependency. This helper is
// client-only and CANNOT import the server's machine-identity package (the
// client/server separation invariant forbids cmd/knowledge importing
// cmd/knowledge-server); it re-derives the same cache shape independently.
//
// Behavior: read the cache; if it holds a valid 16-hex id, return it. Otherwise
// generate a fresh 16-hex id from crypto/rand, best-effort persist it (0o600 file
// under a 0o700 parent), and return it — so the client is fully self-sufficient
// and never depends on the server having written the file first.
func writerID() string {
	path, err := machineIDCachePath()
	if err != nil {
		// Home dir unresolvable (broken environment): derive an ephemeral but
		// process-stable id without persisting. An empty path would also make a
		// later StampWriterSeen a tolerated no-op, but a valid hex id is friendlier.
		return generateWriterID()
	}
	return writerIDFor(path)
}

// writerIDFor is the path-injectable core of writerID: it reads the cache at
// path, returning a stored 16-hex id when present, else generates + best-effort
// persists a fresh one. Separated so tests can drive a temp cache file (and
// simulate a restart by re-invoking with the same path) without touching the
// real ~/.knowledge/machine-id.
func writerIDFor(path string) string {
	if id := readMachineIDCache(path); id != "" {
		return id
	}
	id := generateWriterID()
	_ = writeMachineIDCache(path, id) // best-effort: a write failure just means we re-derive next start
	return id
}

// machineIDCachePath returns the absolute path to the shared machine-id cache
// file (~/.knowledge/machine-id). Mirrors the server's cache-path derivation
// (which this package cannot import) so both sides converge on one file.
func machineIDCachePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".knowledge", "machine-id"), nil
}

// readMachineIDCache returns the cached id when the file holds exactly 16
// lowercase hex chars (after whitespace trim), else "" — identical validation to
// the server's cache read, so the two never clobber each other's format.
func readMachineIDCache(path string) string {
	b, err := os.ReadFile(path) //nolint:gosec // path is the fixed ~/.knowledge/machine-id cache
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(b))
	if len(s) != 16 {
		return ""
	}
	if _, err := hex.DecodeString(s); err != nil {
		return ""
	}
	return strings.ToLower(s)
}

// writeMachineIDCache persists id to the cache file with 0o600 perms, creating
// the parent dir 0o700 if missing (the same best-effort write semantics as the
// server). The trailing newline matches the server's writer so a value written
// by either side reads back identically.
func writeMachineIDCache(path, id string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(id+"\n"), 0o600)
}

// generateWriterID returns a fresh 16-lowercase-hex id from crypto/rand (8 random
// bytes → 16 hex chars), matching the server's 16-hex shape so the shared cache
// file stays mutually valid.
func generateWriterID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand.Read effectively never fails on supported platforms; if it
		// somehow does, fall back to a fixed-but-valid hex value rather than panic
		// — an empty writer_id is a tolerated server-side no-op anyway.
		return "0000000000000000"
	}
	return hex.EncodeToString(buf[:])
}

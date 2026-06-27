// SPDX-License-Identifier: Apache-2.0

// repo_manifest.go — a MACHINE-LOCAL repo-name → on-disk-path registry, stored
// at ~/.knowledge/repos.json (alongside the daemon's pid + logs). Populated at
// code-collect time (repo name → the absolute path it was collected from on
// THIS machine) and read by the three name→dir consumers: ast's cross-repo walk
// root (resolveRepoDir), the code-search staleness footer (correct-dir +
// branch-aware), and the search/query/traverse branch auto-detect.
//
// LOAD-BEARING INVARIANT: this file is NEVER persisted to the cloud / shared
// graph / any synced store. The path is machine-specific — handing another
// teammate (a different machine, a different checkout layout) a path from this
// machine would be a wrong path, a real bug. Keeping it local makes name→dir
// resolution machine-correct: the daemon REMEMBERS where each repo was actually
// collected from here, rather than guessing a path from a name. See the
// decision "Repo→path registry is a machine-local ~/.knowledge manifest, never
// cloud".

package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// repoManifest is a mutex-guarded reader/writer over the JSON map
// {repoName: absPath} at `path`. It is missing-file tolerant: a Lookup against
// a non-existent file reports ok=false (no error), and Record creates the file
// (and the enclosing ~/.knowledge dir) on first write. Every read re-reads the
// file from disk so a concurrent collect in another process is observed —
// there is no in-memory cache to go stale.
type repoManifest struct {
	mu   sync.Mutex
	path string
}

// defaultRepoManifest is the process-wide manifest the four consumers reach.
// It points at ~/.knowledge/repos.json. nil only when the home dir can't be
// resolved at init (an effectively impossible environment) — the lookupRepoDir
// / recordRepoDir package helpers below tolerate a nil instance by degrading to
// "not found" / a no-op, so no consumer needs to nil-check. Tests swap this for
// an instance rooted at a t.TempDir() path.
var defaultRepoManifest = newDefaultRepoManifest()

// newDefaultRepoManifest builds the manifest rooted at ~/.knowledge/repos.json,
// following the same inline os.UserHomeDir + filepath.Join(home, ".knowledge",
// ...) pattern every other ~/.knowledge consumer in this binary uses (there is
// no central helper). Returns nil when the home dir is unresolvable.
func newDefaultRepoManifest() *repoManifest {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return &repoManifest{path: filepath.Join(home, ".knowledge", "repos.json")}
}

// Lookup returns the recorded absolute path for repoName, and ok=false when the
// name is absent, the manifest file does not exist, or the file is unreadable /
// malformed (degrade-to-unknown — a corrupt manifest must never error a search).
func (m *repoManifest) Lookup(repoName string) (string, bool) {
	if m == nil || repoName == "" {
		return "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entries, err := m.readLocked()
	if err != nil {
		return "", false
	}
	path, ok := entries[repoName]
	if !ok || path == "" {
		return "", false
	}
	return path, true
}

// Record upserts repoName → absPath and atomically rewrites the manifest. It
// creates the enclosing ~/.knowledge directory and the file on first write.
// A read failure on the existing file is treated as an empty map (the single
// upsert is preserved) rather than refusing the write — a malformed manifest
// self-heals on the next collect.
func (m *repoManifest) Record(repoName, absPath string) error {
	if m == nil {
		return fmt.Errorf("repo manifest: home dir unresolved; cannot record %q", repoName)
	}
	if repoName == "" || absPath == "" {
		return fmt.Errorf("repo manifest: record needs a non-empty name and path (got %q → %q)", repoName, absPath)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entries, err := m.readLocked()
	if err != nil {
		// Self-heal: a corrupt/unreadable existing manifest is replaced rather
		// than blocking the record. The just-collected mapping is what matters.
		entries = map[string]string{}
	}
	if entries[repoName] == absPath {
		return nil // already current — skip the rewrite.
	}
	entries[repoName] = absPath
	return m.writeAtomicLocked(entries)
}

// readLocked loads and decodes the manifest map. A missing file is NOT an error
// — it returns an empty map so first-write callers and absent-name lookups both
// see the empty case. Callers hold m.mu.
func (m *repoManifest) readLocked() (map[string]string, error) {
	data, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return map[string]string{}, nil
	}
	entries := map[string]string{}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// writeAtomicLocked serializes entries and writes them via temp-file +
// os.Rename in the manifest's own directory, so a reader never observes a
// half-written file. It ensures the ~/.knowledge dir exists first. Callers hold
// m.mu.
func (m *repoManifest) writeAtomicLocked(entries map[string]string) error {
	dir := filepath.Dir(m.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("repo manifest: mkdir %q: %w", dir, err)
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("repo manifest: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "repos-*.json.tmp")
	if err != nil {
		return fmt.Errorf("repo manifest: create temp: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename consumes the temp file.
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("repo manifest: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("repo manifest: close temp: %w", err)
	}
	if err := os.Rename(tmpName, m.path); err != nil {
		return fmt.Errorf("repo manifest: rename temp into place: %w", err)
	}
	return nil
}

// lookupRepoDir is the package-level convenience over the default manifest the
// consumers call. A nil default (home unresolved) degrades to "not found".
func lookupRepoDir(repoName string) (string, bool) {
	return defaultRepoManifest.Lookup(repoName)
}

// recordRepoDir is the package-level convenience over the default manifest the
// collect hook calls. A nil default (home unresolved) returns an error the
// best-effort caller logs and swallows.
func recordRepoDir(repoName, absPath string) error {
	return defaultRepoManifest.Record(repoName, absPath)
}

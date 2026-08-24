// SPDX-License-Identifier: Apache-2.0

// repo_manifest.go — a MACHINE-LOCAL repo-name → on-disk-path registry, stored
// at ~/.knowledge/repos.json (alongside the daemon's pid + logs). Populated at
// code-collect time (repo name → the absolute path it was collected from on
// THIS machine) and read by the four name→dir consumers: ast's cross-repo walk
// root (resolveRepoDir), the code-search staleness footer (correct-dir +
// branch-aware), the search/query/traverse branch auto-detect, and
// LocalCodeRepoPresent, which answers whether this machine holds the checkout at
// all so background work can be scoped to code graphs it can actually serve.
//
// The manifest SELF-HEALS on write: entries whose recorded directory has
// vanished are dropped when Record rewrites the file, so it never grows
// monotonically past the checkouts this machine still holds.
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
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
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

// defaultRepoManifest is the process-wide manifest the name→dir consumers and
// the collect-time writer reach.
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
//
// The rewrite also PRUNES entries whose recorded directory has vanished (see
// pruneVanishedLocked), which is why the already-current short-circuit below
// fires only when there is additionally nothing to drop.
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
	dropped := pruneVanishedLocked(entries, repoName)
	if entries[repoName] == absPath && len(dropped) == 0 {
		return nil // already current and nothing vanished — skip the rewrite.
	}
	entries[repoName] = absPath
	if err := m.writeAtomicLocked(entries); err != nil {
		return err
	}
	if len(dropped) > 0 {
		slog.Info("repo manifest: dropped entries whose recorded directory no longer exists",
			"repos", dropped, "count", len(dropped))
	}
	return nil
}

// pruneVanishedLocked deletes every entry except keep whose recorded directory
// is DEFINITIVELY gone, returning the dropped names sorted. Removing an entry is
// acceptable ONLY because a later collect re-records the repo — the manifest is a
// cache of where each repo was collected from, never the record of its existence.
// A stat error that is not fs.ErrNotExist (permission denied, an IO error, an
// unreadable parent) KEEPS the entry: the conservative direction on a deletion
// path is the opposite of LocalCodeRepoPresent's, which degrades to absent so
// background work does less.
//
// Callers hold m.mu, so these stats run under the lock every Lookup also takes.
// That is sound ONLY because the recorded paths are LOCAL CHECKOUTS on this
// machine — see the file header's machine-local invariant. A recorded path on a
// mounted-but-unresponsive network filesystem would block the whole manifest
// rather than one caller; an unmounted or deleted path returns ENOENT promptly
// and is exactly what this prunes.
func pruneVanishedLocked(entries map[string]string, keep string) []string {
	var dropped []string
	for name, dir := range entries {
		if name == keep {
			continue
		}
		if _, err := os.Stat(dir); err != nil && errors.Is(err, fs.ErrNotExist) {
			dropped = append(dropped, name)
		}
	}
	for _, name := range dropped {
		delete(entries, name)
	}
	slices.Sort(dropped)
	return dropped
}

// repoManifestLabel prefixes every error the manifest's read/write path emits. It is a
// constant rather than an inline literal because the shared writer below takes it as an
// argument, and the messages are user-visible: they must stay byte-identical to what they
// were when this file held its own writer.
const repoManifestLabel = "repo manifest"

// readLocked loads and decodes the manifest map. A missing file is NOT an error
// — it returns an empty map so first-write callers and absent-name lookups both
// see the empty case. Callers hold m.mu.
//
// The primitive lives in local_json_map.go, shared with the sync watermark store, so the
// two machine-local map files cannot drift in missing-file or decode behaviour.
func (m *repoManifest) readLocked() (map[string]string, error) {
	return readLocalJSONMap(m.path)
}

// writeAtomicLocked serializes entries and writes them via temp-file +
// os.Rename in the manifest's own directory, so a reader never observes a
// half-written file. It ensures the ~/.knowledge dir exists first. Callers hold
// m.mu.
//
// The primitive lives in local_json_map.go, shared with the sync watermark store; the
// temp pattern and the error label keep this caller's on-disk and user-visible behaviour
// exactly what it was before the lift.
func (m *repoManifest) writeAtomicLocked(entries map[string]string) error {
	return writeLocalJSONMapAtomic(m.path, "repos-*.json.tmp", repoManifestLabel, entries)
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

// LocalCodeRepoPresent reports whether this machine actually holds the checkout
// for a code graph named repo. Both halves are required: the manifest must name
// the repo AND the recorded directory must still exist, so a checkout deleted
// after it was collected reads as absent.
//
// It degrades to ABSENT on any stat error, matching Lookup's degrade-to-unknown
// posture. The conservative direction here is to do LESS background work, never
// more: a false negative costs some enrichment that a later collect restores,
// while a false positive is the failure this predicate exists to prevent.
func LocalCodeRepoPresent(repo string) bool {
	dir, ok := lookupRepoDir(repo)
	if !ok {
		return false
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return false
	}
	return fi.IsDir()
}

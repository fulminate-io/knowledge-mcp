// SPDX-License-Identifier: Apache-2.0

// sync_watermark.go — a MACHINE-LOCAL store of the last export watermark this machine
// received per synced graph, at ~/.knowledge/sync_watermarks.json (alongside the repo
// manifest, the daemon's pid and its logs). Written after a sync pull applies, and read
// on the next pull so the server can answer "unchanged" and skip serializing a graph that
// has not moved.
//
// LOAD-BEARING INVARIANT: this file is NEVER persisted to the cloud, a shared graph, or
// any synced store. A token is minted by ONE account's server and is meaningless to
// another; a shared token would be compared against a different server's counter and
// could produce a wrong "unchanged" — the silent-staleness failure the whole mechanism
// exists to prevent. It is machine-local for the same class of reason repo_manifest.go:
// 16-23 states for its own file: the value describes THIS machine's relationship to a
// remote, not a fact about the world.
//
// THE TOKEN IS OPAQUE HERE. This store never parses, compares or orders tokens; it only
// hands back the bytes it was given. The only comparer is the server that minted it.

package tools

import (
	"os"
	"path/filepath"
	"sync"
)

// syncWatermarkStore is a mutex-guarded reader/writer over the JSON map
// {"<graph_type>/<name>": token} at `path`. It is missing-file tolerant, and every read
// re-reads from disk so a concurrent pull in another process is observed — there is no
// in-memory cache to go stale. It shares its read/atomic-write primitives with
// repoManifest (local_json_map.go).
type syncWatermarkStore struct {
	mu   sync.Mutex
	path string
}

// defaultSyncWatermarkStore is the process-wide store the sync pull path reaches. nil
// only when the home dir cannot be resolved at init (an effectively impossible
// environment) — both methods tolerate a nil receiver by degrading to "no watermark" and
// a no-op, so no caller needs to nil-check. Tests swap this for an instance rooted at a
// t.TempDir() path.
var defaultSyncWatermarkStore = newDefaultSyncWatermarkStore()

// newDefaultSyncWatermarkStore builds the store rooted at ~/.knowledge/sync_watermarks.json,
// following the same inline os.UserHomeDir + filepath.Join(home, ".knowledge", ...)
// pattern every other ~/.knowledge consumer in this binary uses (there is no central
// helper — see newDefaultRepoManifest). Returns nil when the home dir is unresolvable.
func newDefaultSyncWatermarkStore() *syncWatermarkStore {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return &syncWatermarkStore{path: filepath.Join(home, ".knowledge", "sync_watermarks.json")}
}

// syncWatermarkKey is the fixed key format: "<graph_type>/<name>".
func syncWatermarkKey(graphType, name string) string { return graphType + "/" + name }

// Load returns the stored token for a graph, or "" when there is none.
//
// IT RETURNS "" FOR EVERY UNKNOWN CASE — a nil store, a missing file, an unreadable or
// malformed file, or an absent key — and that is NOT a fallback. "" means "send no
// watermark", which yields a full export: exactly the answer the system gives today and
// the only outcome reachable without a token. There is no degraded lane here because
// there is no second behaviour to degrade into.
func (s *syncWatermarkStore) Load(graphType, name string) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := readLocalJSONMap(s.path)
	if err != nil {
		return ""
	}
	return entries[syncWatermarkKey(graphType, name)]
}

// Save upserts a graph's token and atomically rewrites the file.
//
// AN EMPTY TOKEN DELETES THE KEY RATHER THAN STORING "". An empty token is the server's
// "I cannot answer" signal, and storing it would put a value in the file that every later
// comparison must then be careful never to send. Deleting removes the case instead of
// guarding it: the next pull finds no key, sends nothing, and receives a full export.
func (s *syncWatermarkStore) Save(graphType, name, token string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := readLocalJSONMap(s.path)
	if err != nil {
		// Self-heal: a corrupt/unreadable file is replaced rather than blocking the
		// save, the same disposition repoManifest.Record takes for its own file.
		entries = map[string]string{}
	}
	key := syncWatermarkKey(graphType, name)
	if token == "" {
		if _, present := entries[key]; !present {
			return nil // nothing stored and nothing to store — skip the rewrite.
		}
		delete(entries, key)
	} else {
		if entries[key] == token {
			return nil // already current — skip the rewrite.
		}
		entries[key] = token
	}
	return writeLocalJSONMapAtomic(s.path, "sync_watermarks-*.json.tmp", "sync watermarks", entries)
}

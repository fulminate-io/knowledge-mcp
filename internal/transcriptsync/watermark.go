// SPDX-License-Identifier: Apache-2.0

// watermark.go — the per-transcript incremental cursor: a MACHINE-LOCAL,
// daemon-independent store at ~/.knowledge/transcript-watermarks.json. It mirrors
// the repo_manifest.go durability idiom (mutex-guarded read-modify-write + atomic
// temp-file create→write→rename) so a crash mid-write never corrupts the cursor.
//
// LOAD-BEARING INVARIANT: this file is NEVER synced to the cloud / shared graph.
// The watermark records what THIS machine has already shipped (the size + mod time
// of the last-uploaded session file); it is local progress state, not shared
// knowledge. Advancing it follows the watermark-incremental pattern: the cursor
// moves ONLY after a session's terminal confirm succeeds, never in a defer, so a
// failed upload re-uploads the same session next run rather than dropping records.

package transcriptsync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Watermark is the per-transcript cursor persisted under the source:session key.
// It is the live os.Stat identity of the last-uploaded session file plus the rollup
// schema version that session was last shipped under:
//
//   - Size / Mtime: the live os.Stat values captured at the last advance. The next
//     run re-uploads the WHOLE session when either differs (a size change OR an
//     equal-size in-place rewrite caught by mtime) and ships nothing when both
//     match. Mtime is a live field — there is deliberately no prefix-hash.
//   - RollupSchemaVersion: the rollup contract version the session was last shipped
//     under. A stored value below the current rollupSchemaVersion forces a whole-session
//     re-derive + re-ship even when Size/Mtime are unchanged — this is the schema-bump
//     backfill (the initial 0→1 bump re-ships every locally-held session). Absent in
//     JSON (a pre-deploy watermark) decodes to 0, which is exactly that pre-bump value.
//
// The old byte-offset / chunk-seq / part-key-generation fields are gone: the client
// now converts each changed session to ONE parquet object and re-uploads it whole,
// so there is no per-record offset or per-chunk sequence to track.
type Watermark struct {
	Size                int64 `json:"size"`
	Mtime               int64 `json:"mtime"`
	RollupSchemaVersion int   `json:"rollup_schema_version"`
}

// WatermarkStore is a mutex-guarded reader/writer over the JSON map
// {source:session -> Watermark} at `path`. It is missing-file tolerant (a Lookup
// against a non-existent file reports ok=false, no error) and re-reads the file
// on every access so there is no in-memory cache to go stale. It mirrors
// repoManifest (cmd/knowledge/internal/tools/repo_manifest.go) — re-authored
// rather than reused because that helper is unexported, map[string]string-bound,
// and in package tools which this standalone engine must not import.
type WatermarkStore struct {
	mu   sync.Mutex
	path string
}

// NewDefaultWatermarkStore builds the store rooted at
// ~/.knowledge/transcript-watermarks.json, following the same inline
// os.UserHomeDir + filepath.Join(home, ".knowledge", ...) pattern every other
// ~/.knowledge consumer in this binary uses (mirrors newDefaultRepoManifest).
// Returns an error when the home dir is unresolvable so the cli wrapper can
// surface it rather than silently writing nowhere.
func NewDefaultWatermarkStore() (*WatermarkStore, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil, fmt.Errorf("transcriptsync: resolve home dir for watermark store: %w", err)
	}
	return &WatermarkStore{path: filepath.Join(home, ".knowledge", "transcript-watermarks.json")}, nil
}

// Lookup returns the persisted Watermark for key (source:session) and ok=false
// when the key is absent, the file does not exist, or the file is unreadable /
// malformed (degrade-to-unknown — a corrupt manifest must re-seed, never error).
func (s *WatermarkStore) Lookup(key string) (Watermark, bool) {
	if s == nil || key == "" {
		return Watermark{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.readLocked()
	if err != nil {
		return Watermark{}, false
	}
	w, ok := entries[key]
	return w, ok
}

// Advance upserts key -> w and atomically rewrites the manifest. It creates the
// enclosing ~/.knowledge directory and the file on first write. A read failure on
// the existing file is treated as an empty map (the single upsert is preserved)
// rather than refusing the write — a malformed manifest self-heals.
func (s *WatermarkStore) Advance(key string, w Watermark) error {
	if s == nil {
		return fmt.Errorf("transcriptsync: watermark store is nil; cannot advance %q", key)
	}
	if key == "" {
		return fmt.Errorf("transcriptsync: watermark advance needs a non-empty source:session key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.readLocked()
	if err != nil {
		// Self-heal: a corrupt/unreadable existing manifest is replaced rather than
		// blocking the advance. The just-shipped cursor is what matters.
		entries = map[string]Watermark{}
	}
	entries[key] = w
	return s.writeAtomicLocked(entries)
}

// readLocked loads and decodes the manifest map. A missing or empty file is NOT
// an error — it returns an empty map so first-write callers and absent-key
// lookups both see the empty case. Callers hold s.mu.
func (s *WatermarkStore) readLocked() (map[string]Watermark, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Watermark{}, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return map[string]Watermark{}, nil
	}
	entries := map[string]Watermark{}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// writeAtomicLocked serializes entries and writes them via temp-file + os.Rename
// in the manifest's own directory, so a reader never observes a half-written file
// and a crash mid-write leaves the prior cursor intact. It ensures the
// ~/.knowledge dir exists first. Mirrors repoManifest.writeAtomicLocked exactly.
// Callers hold s.mu.
func (s *WatermarkStore) writeAtomicLocked(entries map[string]Watermark) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("transcriptsync: mkdir %q: %w", dir, err)
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("transcriptsync: marshal watermarks: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "transcript-watermarks-*.json.tmp")
	if err != nil {
		return fmt.Errorf("transcriptsync: create temp: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename consumes the temp file.
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("transcriptsync: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("transcriptsync: close temp: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("transcriptsync: rename temp into place: %w", err)
	}
	return nil
}

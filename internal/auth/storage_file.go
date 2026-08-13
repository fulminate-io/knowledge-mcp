// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// credentialsFileName is the basename of the file-backed store inside
// ~/.knowledge. It sits beside the other non-graph siblings the binary keeps
// there (config, repos.json, server.pid) and is ignored by graph discovery,
// which only considers directories and .bin files.
const credentialsFileName = "credentials"

// fileStore persists secrets as a flat JSON object in a 0600 file. It is the
// fallback backend for environments with no reachable platform keychain — a
// container being the motivating case — and is never preferred over one.
//
// Durability is temp-file + rename within the same directory, so a reader
// never observes a half-written object.
//
// Concurrency: the mutex serializes this process's own read-modify-write
// cycles. It does NOT coordinate across processes, so two writers of one file
// (a login process and a running daemon rotating its refresh token) resolve
// last-rename-wins, and a rotation landing between login's two Sets can drop
// the client_id. The consequence is bounded to "re-login required", which is
// the residual risk already documented for this credential on the refresh
// path; a lock file would buy nothing more.
type fileStore struct {
	path string
	mu   sync.Mutex
}

// newFileStore returns a Store backed by ~/.knowledge/credentials, creating
// the directory at 0700 if it does not exist.
//
// Inside a test binary it refuses when it would target the real home, since
// tests must use in-memory fakes; the real credential store is off-limits to
// test binaries. A test that points HOME at a temp directory first is
// hermetic and still allowed. The refusal is checked before any filesystem
// call, so a refused construction leaves nothing behind.
func newFileStore() (Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("auth: resolve home directory: %w", err)
	}
	if err := refuseRealHomeInTest(home); err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".knowledge")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("auth: create %q: %w", dir, err)
	}
	return &fileStore{path: filepath.Join(dir, credentialsFileName)}, nil
}

// Get returns the value stored under key, or ErrNotFound if the key (or the
// whole file) is absent.
func (s *fileStore) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.readLocked()
	if err != nil {
		return "", err
	}
	v, ok := entries[key]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

// Set writes value under key, creating or overwriting the entry and leaving
// every other entry untouched.
func (s *fileStore) Set(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.readLocked()
	if err != nil {
		return err
	}
	entries[key] = value
	return s.writeLocked(entries)
}

// Delete removes the entry for key, returning ErrNotFound if it is absent —
// matching the keychain backends so callers' errors.Is comparisons hold
// across every platform.
func (s *fileStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.readLocked()
	if err != nil {
		return err
	}
	if _, ok := entries[key]; !ok {
		return ErrNotFound
	}
	delete(entries, key)
	// An emptied map is still written back as `{}` rather than unlinking the
	// file: keeping the inode avoids a remove/recreate race and preserves the
	// 0600 mode.
	return s.writeLocked(entries)
}

// readLocked loads the whole credential object. A missing file yields an
// empty map and no error — a cold install has no credentials yet, which is
// not a failure. A malformed file is an error rather than a silent reset, so
// a corrupted store is reported instead of quietly discarded. Callers hold
// s.mu.
func (s *fileStore) readLocked() (map[string]string, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("auth: read %q: %w", s.path, err)
	}
	entries := map[string]string{}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("auth: parse %q: %w", s.path, err)
	}
	return entries, nil
}

// writeLocked serializes entries into a sibling temp file and renames it over
// the target. The explicit Chmod is deliberate: os.CreateTemp's 0600 is
// umask-filtered, so the mode is asserted rather than assumed. The deferred
// remove is unconditional, so no exit path leaves a straggler behind.
// Callers hold s.mu.
func (s *fileStore) writeLocked(entries map[string]string) error {
	data, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("auth: marshal credentials: %w", err)
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, "credentials-*.tmp")
	if err != nil {
		return fmt.Errorf("auth: create temp credentials: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("auth: write temp credentials: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("auth: sync temp credentials: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("auth: close temp credentials: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("auth: chmod temp credentials: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("auth: rename credentials into place: %w", err)
	}
	return nil
}

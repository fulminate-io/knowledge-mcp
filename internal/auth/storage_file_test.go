// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newFileStoreInTempHome points HOME at a fresh temp dir and constructs the
// store there, returning the store and its ~/.knowledge directory.
func newFileStoreInTempHome(t *testing.T) (*fileStore, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	s, err := newFileStore()
	if err != nil {
		t.Fatalf("newFileStore: %v", err)
	}
	fs, ok := s.(*fileStore)
	if !ok {
		t.Fatalf("newFileStore returned %T, want *fileStore", s)
	}
	return fs, filepath.Join(home, ".knowledge")
}

// countTempFiles reports how many credentials-*.tmp entries exist in dir. It
// is the straggler probe; the failed-write subtest drives it non-zero with a
// planted file first so a zero elsewhere reads as "none there" rather than
// "the probe never looked".
func countTempFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %q: %v", dir, err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "credentials-") && strings.HasSuffix(e.Name(), ".tmp") {
			n++
		}
	}
	return n
}

func TestFileStore(t *testing.T) {
	ctx := context.Background()

	t.Run("round_trip", func(t *testing.T) {
		s, _ := newFileStoreInTempHome(t)
		if _, err := s.Get(ctx, KeyRefreshToken); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get on a cold store = %v, want ErrNotFound", err)
		}
		if err := s.Set(ctx, KeyRefreshToken, "rt-1"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		got, err := s.Get(ctx, KeyRefreshToken)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got != "rt-1" {
			t.Fatalf("Get = %q, want %q", got, "rt-1")
		}
	})

	t.Run("rotation_preserves_client_id", func(t *testing.T) {
		s, _ := newFileStoreInTempHome(t)
		if err := s.Set(ctx, KeyClientID, "cid-1"); err != nil {
			t.Fatalf("Set client id: %v", err)
		}
		if err := s.Set(ctx, KeyRefreshToken, "rt-1"); err != nil {
			t.Fatalf("Set refresh token: %v", err)
		}
		if err := s.Set(ctx, KeyRefreshToken, "rt-2"); err != nil {
			t.Fatalf("rotate refresh token: %v", err)
		}
		cid, err := s.Get(ctx, KeyClientID)
		if err != nil {
			t.Fatalf("Get client id after rotation: %v", err)
		}
		if cid != "cid-1" {
			t.Fatalf("client id after rotation = %q, want %q", cid, "cid-1")
		}
		rt, err := s.Get(ctx, KeyRefreshToken)
		if err != nil {
			t.Fatalf("Get refresh token after rotation: %v", err)
		}
		if rt != "rt-2" {
			t.Fatalf("refresh token after rotation = %q, want %q", rt, "rt-2")
		}
	})

	t.Run("file_mode_0600", func(t *testing.T) {
		s, _ := newFileStoreInTempHome(t)
		if err := s.Set(ctx, KeyRefreshToken, "rt-1"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		info, err := os.Stat(s.path)
		if err != nil {
			t.Fatalf("stat credentials: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("credentials mode = %#o, want 0600", perm)
		}
	})

	t.Run("dir_mode_0700", func(t *testing.T) {
		_, dir := newFileStoreInTempHome(t)
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat dir: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("%q mode = %#o, want 0700", dir, perm)
		}
	})

	t.Run("delete_missing_is_ErrNotFound", func(t *testing.T) {
		s, _ := newFileStoreInTempHome(t)
		if err := s.Delete(ctx, KeyRefreshToken); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Delete on a cold store = %v, want ErrNotFound", err)
		}
		if err := s.Set(ctx, KeyRefreshToken, "rt-1"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if err := s.Delete(ctx, KeyRefreshToken); err != nil {
			t.Fatalf("Delete of a present key: %v", err)
		}
		if err := s.Delete(ctx, KeyRefreshToken); !errors.Is(err, ErrNotFound) {
			t.Fatalf("second Delete = %v, want ErrNotFound", err)
		}
		// Emptying the map rewrites the object rather than unlinking the file.
		data, err := os.ReadFile(s.path)
		if err != nil {
			t.Fatalf("credentials file after emptying delete: %v", err)
		}
		if string(data) != "{}" {
			t.Fatalf("credentials after emptying delete = %q, want %q", string(data), "{}")
		}
	})

	t.Run("no_tmp_straggler_after_success", func(t *testing.T) {
		s, dir := newFileStoreInTempHome(t)
		if err := s.Set(ctx, KeyRefreshToken, "rt-1"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if err := s.Set(ctx, KeyClientID, "cid-1"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if n := countTempFiles(t, dir); n != 0 {
			t.Fatalf("temp stragglers after successful writes = %d, want 0", n)
		}
	})

	t.Run("no_tmp_straggler_after_failed_write", func(t *testing.T) {
		s, dir := newFileStoreInTempHome(t)

		// Known-positive control: the straggler probe must be able to see a
		// temp file when one is present, otherwise the zero below is
		// unreadable.
		planted := filepath.Join(dir, "credentials-planted.tmp")
		if err := os.WriteFile(planted, []byte("x"), 0o600); err != nil {
			t.Fatalf("plant control temp file: %v", err)
		}
		if n := countTempFiles(t, dir); n != 1 {
			t.Fatalf("straggler probe with a planted temp file = %d, want 1", n)
		}
		if err := os.Remove(planted); err != nil {
			t.Fatalf("remove control temp file: %v", err)
		}

		// Occupying the credentials path with a directory makes Set fail. Be
		// precise about WHICH failure this exercises: Set reads before it
		// writes, and os.ReadFile on a directory returns EISDIR, so the error
		// comes from the read leg and os.CreateTemp never runs. That is why no
		// temp file can exist here — the deferred remove in writeLocked is
		// verified structurally (it is unconditional and covers every exit
		// path) rather than by this case. What this case does prove is that a
		// failing Set surfaces its error and leaves the directory clean.
		if err := os.Mkdir(s.path, 0o700); err != nil {
			t.Fatalf("occupy credentials path: %v", err)
		}
		if err := s.Set(ctx, KeyRefreshToken, "rt-1"); err == nil {
			t.Fatal("Set against an occupied credentials path succeeded, want an error")
		}
		if n := countTempFiles(t, dir); n != 0 {
			t.Fatalf("temp stragglers after a failed write = %d, want 0", n)
		}
	})

	t.Run("malformed_file_is_an_error", func(t *testing.T) {
		s, _ := newFileStoreInTempHome(t)
		if err := os.WriteFile(s.path, []byte("{not json"), 0o600); err != nil {
			t.Fatalf("write malformed credentials: %v", err)
		}
		if _, err := s.Get(ctx, KeyRefreshToken); err == nil || errors.Is(err, ErrNotFound) {
			t.Fatalf("Get on a malformed store = %v, want a parse error", err)
		}
		// A malformed store is reported, never silently reset.
		data, err := os.ReadFile(s.path)
		if err != nil {
			t.Fatalf("re-read credentials: %v", err)
		}
		if string(data) != "{not json" {
			t.Fatalf("malformed credentials were rewritten to %q", string(data))
		}
	})
}

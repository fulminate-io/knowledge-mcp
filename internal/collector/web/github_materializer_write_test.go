// SPDX-License-Identifier: Apache-2.0

package web

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteTarFile_WritesTheBody is the in-bound case: an ordinary entry lands
// whole and the function reports no error.
func TestWriteTarFile_WritesTheBody(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "sub", "dir", "f.txt")
	body := strings.Repeat("payload\n", 1000)
	if err := writeTarFile(strings.NewReader(body), dst); err != nil {
		t.Fatalf("writeTarFile: %v", err)
	}
	got, err := os.ReadFile(dst) //nolint:gosec // dst is this test's own temp dir
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != body {
		t.Errorf("wrote %d bytes, want %d", len(got), len(body))
	}
}

// failingReader returns err after handing over its prefix, standing in for the
// size-capped reader the unpacker wraps the tar stream in.
type failingReader struct {
	prefix *bytes.Reader
	err    error
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.prefix.Len() > 0 {
		return r.prefix.Read(p)
	}
	return 0, r.err
}

// TestWriteTarFile_CopyErrorSurvivesTheClose is the guard on the joined close:
// the COPY error must still be what the caller sees, unwrapped, because
// errSizeExceeded travels on it and unpackTar matches it with errors.Is to
// decide between a mid-stream size warning and a per-entry loss.
//
// A close that succeeded must not overwrite it, and neither must a close that
// failed — the copy error is the earlier and more specific fact.
func TestWriteTarFile_CopyErrorSurvivesTheClose(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "capped.bin")
	r := &failingReader{prefix: bytes.NewReader([]byte("some bytes")), err: errSizeExceeded}

	err := writeTarFile(r, dst)
	if err == nil {
		t.Fatal("writeTarFile returned nil for a reader that failed")
	}
	if !errors.Is(err, errSizeExceeded) {
		t.Errorf("writeTarFile error = %v, want it to wrap errSizeExceeded — unpackTar's size-cap branch keys on this", err)
	}
	if strings.Contains(err.Error(), "close ") {
		t.Errorf("the close error displaced the copy error: %v", err)
	}
}

// TestJoinCloseErr covers all four combinations of the close fold, including
// the both-failed arm that a live writeTarFile cannot reach: a close on a temp
// file effectively never fails, so without this table the precedence rule would
// ship unobserved.
func TestJoinCloseErr(t *testing.T) {
	writeErr := errors.New("write blew up")
	closeErr := errors.New("close blew up")

	t.Run("neither failed", func(t *testing.T) {
		if got := joinCloseErr(nil, nil, "/tmp/x"); got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})
	t.Run("only the close failed", func(t *testing.T) {
		got := joinCloseErr(nil, closeErr, "/tmp/x")
		if !errors.Is(got, closeErr) {
			t.Fatalf("got %v, want it to wrap the close error", got)
		}
		if !strings.Contains(got.Error(), "/tmp/x") {
			t.Errorf("got %v, want the destination named", got)
		}
	})
	t.Run("only the write failed", func(t *testing.T) {
		if got := joinCloseErr(writeErr, nil, "/tmp/x"); !errors.Is(got, writeErr) {
			t.Errorf("got %v, want the write error", got)
		}
	})
	t.Run("both failed: the write error wins", func(t *testing.T) {
		got := joinCloseErr(writeErr, closeErr, "/tmp/x")
		if !errors.Is(got, writeErr) {
			t.Errorf("got %v, want the write error — it carries errSizeExceeded", got)
		}
		if errors.Is(got, closeErr) {
			t.Errorf("got %v, want the close error dropped, not folded in", got)
		}
	})
}

// TestWriteTarFile_OpenFailureIsReported keeps the open arm honest: a
// destination that cannot be opened is an error, not a silent skip.
func TestWriteTarFile_OpenFailureIsReported(t *testing.T) {
	dir := t.TempDir()
	// A path whose parent is an existing FILE — MkdirAll fails on it.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	err := writeTarFile(strings.NewReader("x"), filepath.Join(blocker, "child.txt"))
	if err == nil {
		t.Fatal("writeTarFile returned nil for a destination it cannot create")
	}
}

var _ io.Reader = (*failingReader)(nil)

//go:build !windows

// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesktopSettingsWriteFailurePreservesFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config")
	original := []byte("[default]\nprovider='openai'\nmodel='original'\nbase_url='http://127.0.0.1:1'\n")
	if err := os.WriteFile(p, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0500); err != nil { // #nosec G302 -- Owner-only test directory needs traversal while denying writes.
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0700); err != nil { // #nosec G302 -- Restore owner-only directory traversal and writes for cleanup.
			t.Error(err)
		}
	})
	r := runDesktopSettings(p, "save", strings.NewReader(`{"edits":{"default":{"model":"replacement"}}}`))
	if r.Code != "config_write_failed" || r.Saved {
		t.Fatal("write denial not reported")
	}
	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, original) {
		t.Fatal("write failure changed original")
	}
}

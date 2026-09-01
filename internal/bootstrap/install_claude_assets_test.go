// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// repoRootFromTest walks up from the test's working directory (the package
// dir under `go test`) until it finds the module root — the directory
// carrying go.mod AND the canonical .claude tree. Walking beats a hardcoded
// ../../../.. so the test survives a package move, and it fails loudly
// rather than silently comparing against nothing.
func repoRootFromTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, ".claude", "skills")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("repo root not found walking up from the test working directory")
		}
		dir = parent
	}
}

// TestRunInstallClaudeAssets_GovernanceFile drives a REAL claude install
// into a temp dest and asserts the flat governance file lands at
// <dest>/skills/GOVERNANCE.md byte-identical to the canonical
// .claude/skills/GOVERNANCE.md.
//
// Characterization guard: the claude installer writes the embed tree
// verbatim, so this routing already holds. What the test protects is the
// SHIPPING of the file — every agent def mandates reading it as its first
// action, so an install that omits it, or rewrites it on the way through,
// breaks every mandated read at once, silently.
//
// The byte comparison is against the canonical source rather than against
// the embed mirror: the mirror is a gitignored copy that scripts/sync-assets.sh
// regenerates, so comparing embed-to-embed would let the installer agree
// with itself.
func TestRunInstallClaudeAssets_GovernanceFile(t *testing.T) {
	root := repoRootFromTest(t)
	canonical, err := os.ReadFile(filepath.Join(root, ".claude", "skills", "GOVERNANCE.md"))
	if err != nil {
		t.Fatalf("read canonical governance file: %v", err)
	}

	dir := t.TempDir()
	dest := filepath.Join(dir, "clauderoot")
	if err := runInstallClaudeAssets([]string{
		"--no-mcp",
		"--dest", dest,
		"--claude-md-dest", filepath.Join(dir, "CLAUDE.md"),
		"--claude-settings-dest", filepath.Join(dir, "settings.json"),
	}); err != nil {
		t.Fatalf("runInstallClaudeAssets: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dest, "skills", "GOVERNANCE.md"))
	if err != nil {
		t.Fatalf("governance file not installed at <dest>/skills/GOVERNANCE.md: %v", err)
	}
	if !bytes.Equal(got, canonical) {
		t.Errorf("installed governance file differs from .claude/skills/GOVERNANCE.md (%d bytes installed, %d canonical)",
			len(got), len(canonical))
	}
}

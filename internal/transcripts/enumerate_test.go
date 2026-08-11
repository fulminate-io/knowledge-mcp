// SPDX-License-Identifier: Apache-2.0

package transcripts

import (
	"os"
	"path/filepath"
	"testing"
)

// withHomeDir overrides the package-local homeDir seam to point at dir for the
// duration of the test, restoring it on cleanup.
func withHomeDir(t *testing.T, dir string) {
	t.Helper()
	prev := homeDir
	homeDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { homeDir = prev })
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestEnumerate(t *testing.T) {
	home := t.TempDir()
	withHomeDir(t, home)

	// Claude: a top-level session transcript + a subagent transcript.
	claudeSession := filepath.Join(home, ".claude", "projects", "-Users-me-code-knowledge", "sess-1.jsonl")
	claudeSubagent := filepath.Join(home, ".claude", "projects", "-Users-me-code-knowledge", "subagents", "agent-abc.jsonl")
	mustWrite(t, claudeSession, "{\"type\":\"assistant\"}\n")
	mustWrite(t, claudeSubagent, "{\"type\":\"assistant\"}\n")

	// Codex: a rollout under a nested date dir + a NON-rollout file that must be excluded.
	codexRollout := filepath.Join(home, ".codex", "sessions", "2026", "06", "29", "rollout-2026-06-29T00-00-00-abc.jsonl")
	codexOther := filepath.Join(home, ".codex", "sessions", "2026", "06", "29", "history.jsonl")
	mustWrite(t, codexRollout, "{\"type\":\"session_meta\"}\n")
	mustWrite(t, codexOther, "{\"type\":\"whatever\"}\n")

	entries, err := Enumerate()
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}

	bySource := map[Source][]Entry{}
	for _, e := range entries {
		bySource[e.Source] = append(bySource[e.Source], e)
		if e.Size == 0 {
			t.Errorf("entry %s has zero Size", e.Path)
		}
	}

	if got := len(bySource[SourceClaude]); got != 2 {
		t.Errorf("claude entries = %d, want 2 (session + subagent); got %v", got, bySource[SourceClaude])
	}
	if got := len(bySource[SourceCodex]); got != 1 {
		t.Errorf("codex entries = %d, want 1 (rollout only, non-rollout excluded); got %v", got, bySource[SourceCodex])
	}

	// The non-rollout codex file must not appear.
	for _, e := range entries {
		if e.Path == codexOther {
			t.Errorf("non-rollout codex file %s was enumerated", codexOther)
		}
	}
}

// TestClaudeProjectSessions asserts the single-project readdir: only the session
// transcripts sitting DIRECTLY in the project dir are returned, with the nested
// subagent transcript excluded — the distinction Enumerate deliberately does not
// draw. The subagent file is written LAST so it is the newest in the tree, which
// is what a recency-picking caller would otherwise have bound.
func TestClaudeProjectSessions(t *testing.T) {
	home := t.TempDir()
	projDir := filepath.Join(home, ".claude", "projects", "-Users-me-code-knowledge")

	mustWrite(t, filepath.Join(projDir, "sess-1.jsonl"), "{\"type\":\"assistant\"}\n")
	mustWrite(t, filepath.Join(projDir, "sess-2.jsonl"), "{\"type\":\"assistant\"}\n")
	mustWrite(t, filepath.Join(projDir, "notes.txt"), "not a transcript\n")
	mustWrite(t, filepath.Join(projDir, "sess-1", "subagents", "agent-abc.jsonl"), "{\"type\":\"assistant\"}\n")

	entries, err := ClaudeProjectSessions(projDir)
	if err != nil {
		t.Fatalf("ClaudeProjectSessions: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2 top-level sessions; got %v", len(entries), entries)
	}
	for _, e := range entries {
		if e.Source != SourceClaude {
			t.Errorf("entry %s source = %q, want claude", e.Path, e.Source)
		}
		if e.Size == 0 {
			t.Errorf("entry %s has zero Size", e.Path)
		}
		if filepath.Dir(e.Path) != projDir {
			t.Errorf("entry %s is not directly in the project dir", e.Path)
		}
	}

	// A project dir that does not exist is "no sessions", not an error.
	missing, err := ClaudeProjectSessions(filepath.Join(home, ".claude", "projects", "-nope"))
	if err != nil {
		t.Fatalf("missing project dir: want nil error, got %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing project dir: want no entries, got %v", missing)
	}
}

// TestEnumerateMissingCodexRoot asserts that a user with only Claude installed
// (no ~/.codex root) still enumerates Claude entries with no error.
func TestEnumerateMissingCodexRoot(t *testing.T) {
	home := t.TempDir()
	withHomeDir(t, home)

	claudeSession := filepath.Join(home, ".claude", "projects", "-Users-me-x", "sess.jsonl")
	mustWrite(t, claudeSession, "{\"type\":\"assistant\"}\n")
	// Deliberately do NOT create ~/.codex/sessions.

	entries, err := Enumerate()
	if err != nil {
		t.Fatalf("Enumerate with missing codex root: %v", err)
	}
	if len(entries) != 1 || entries[0].Source != SourceClaude {
		t.Fatalf("want exactly 1 claude entry, got %v", entries)
	}
}

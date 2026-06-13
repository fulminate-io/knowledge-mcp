// SPDX-License-Identifier: Apache-2.0

package hivemonitor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// lsofFan builds an `lsof -Ffan` machine-format block for one open file:
// the `f<fd>`, `a<access>`, `n<name>` record triplet.
func lsofFan(fd, access, name string) string {
	return fmt.Sprintf("f%s\na%s\nn%s\n", fd, access, name)
}

func TestParseCodexWriteRollouts(t *testing.T) {
	const want = "/Users/j/.codex/sessions/2026/06/09/rollout-2026-06-09T11-00-00-aaaa.jsonl"
	readOnly := "/Users/j/.codex/sessions/2026/06/09/rollout-2026-06-09T10-00-00-bbbb.jsonl"
	out := "p42\n" +
		lsofFan("cwd", " ", "/Users/j/code/knowledge") + // cwd entry, not a file write
		lsofFan("3", "u", "/Users/j/.codex/state_5.sqlite") + // write, but not a rollout
		lsofFan("9", "r", readOnly) + // a rollout but read-only (resumed prior) — excluded
		lsofFan("46", "w", want) + // THE write rollout
		lsofFan("47", "w", want) // same path again — must dedup

	got := parseCodexWriteRollouts(out)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("parseCodexWriteRollouts = %v, want exactly [%q]", got, want)
	}
}

func TestCodexWriteRolloutForPID(t *testing.T) {
	const roll = "/Users/j/.codex/sessions/2026/06/09/rollout-2026-06-09T11-00-00-aaaa.jsonl"
	other := "/Users/j/.codex/sessions/2026/06/09/rollout-2026-06-09T12-00-00-cccc.jsonl"

	cases := []struct {
		name     string
		run      func(context.Context, string, ...string) ([]byte, error)
		pid      int
		wantPath string
		wantOK   bool
	}{
		{"single write rollout", func(context.Context, string, ...string) ([]byte, error) {
			return []byte(lsofFan("46", "w", roll)), nil
		}, 42, roll, true},
		{"two distinct write rollouts is ambiguous", func(context.Context, string, ...string) ([]byte, error) {
			return []byte(lsofFan("46", "w", roll) + lsofFan("48", "w", other)), nil
		}, 42, "", false},
		{"no rollout open (idle)", func(context.Context, string, ...string) ([]byte, error) {
			return []byte(lsofFan("3", "u", "/Users/j/.codex/state_5.sqlite")), nil
		}, 42, "", false},
		{"lsof error", func(context.Context, string, ...string) ([]byte, error) {
			return nil, fmt.Errorf("lsof: no such process")
		}, 42, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := execRunner
			t.Cleanup(func() { execRunner = orig })
			execRunner = tc.run
			path, ok := codexWriteRolloutForPID(context.Background(), tc.pid)
			if ok != tc.wantOK || path != tc.wantPath {
				t.Fatalf("codexWriteRolloutForPID = (%q, %v), want (%q, %v)", path, ok, tc.wantPath, tc.wantOK)
			}
		})
	}

	t.Run("non-positive pid skips lsof", func(t *testing.T) {
		orig := execRunner
		t.Cleanup(func() { execRunner = orig })
		called := false
		execRunner = func(context.Context, string, ...string) ([]byte, error) {
			called = true
			return nil, nil
		}
		if path, ok := codexWriteRolloutForPID(context.Background(), 0); ok || path != "" || called {
			t.Fatalf("pid 0: got (%q,%v) called=%v, want ('',false) without invoking lsof", path, ok, called)
		}
	})
}

// TestResolveTranscript_CodexPIDOpenRollout is the decisive test: when the agent
// holds a rollout open, the PID path returns THAT exact file even when a NEWER
// same-cwd rollout exists — i.e. the deterministic binding overrides the
// fallback's (wrong) newest-by-mtime guess for the same-directory collision.
func TestResolveTranscript_CodexPIDOpenRollout(t *testing.T) {
	home := t.TempDir()
	origHome := homeDir
	t.Cleanup(func() { homeDir = origHome })
	homeDir = func() (string, error) { return home, nil }

	const cwd = "/Users/jonathan/code/knowledge"
	now := time.Now()
	// The agent's OWN rollout (older mtime) and a competing same-cwd rollout
	// (newer) that would win the cwd-scan fallback.
	mine := writeCodexRolloutID(t, home, "2026/06/09/rollout-2026-06-09T11-00-00-mine.jsonl", "thread-mine", cwd, now.Add(-time.Hour))
	writeCodexRolloutID(t, home, "2026/06/09/rollout-2026-06-09T12-00-00-other.jsonl", "thread-other", cwd, now)

	// lsof reports the agent (pid 42) holding ITS rollout open for writing.
	origExec := execRunner
	t.Cleanup(func() { execRunner = origExec })
	execRunner = func(context.Context, string, ...string) ([]byte, error) {
		return []byte(lsofFan("46", "w", mine)), nil
	}

	h, err := ResolveTranscript(context.Background(), SessionSnapshot{ID: "cx", Cwd: cwd, PID: 42, Comm: "codex"})
	if err != nil {
		t.Fatalf("ResolveTranscript: %v", err)
	}
	if h.Path != mine {
		t.Fatalf("resolved %q, want the PID-held rollout %q (not the newer same-cwd one)", h.Path, mine)
	}
	if h.HarnessSessionID != "thread-mine" {
		t.Errorf("HarnessSessionID = %q, want thread-mine (from the held rollout's session_meta)", h.HarnessSessionID)
	}
	if h.Format != FormatCodex {
		t.Errorf("format = %q, want codex", h.Format)
	}
}

// writeCodexRolloutID writes a codex rollout whose first line is a session_meta
// declaring id+cwd, at relPath under <home>/.codex/sessions, with the given mtime.
func writeCodexRolloutID(t *testing.T, home, relPath, id, cwd string, mt time.Time) string {
	t.Helper()
	full := filepath.Join(home, ".codex", "sessions", relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"cwd":%q}}`+"\n", id, cwd)
	if err := os.WriteFile(full, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(full, mt, mt); err != nil {
		t.Fatal(err)
	}
	return full
}

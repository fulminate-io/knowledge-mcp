// SPDX-License-Identifier: Apache-2.0

package hivemonitor

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// injectProcessEnv wires the GOOS-appropriate env seam so ProcessEnvValue
// returns env for pid, host-independently: the darwin arm via a synthetic
// `ps eww` fixture, the linux arm via a temp procRoot/<pid>/environ file.
func injectProcessEnv(t *testing.T, pid int, env map[string]string) {
	t.Helper()
	switch runtime.GOOS {
	case "darwin":
		origExec := execRunner
		t.Cleanup(func() { execRunner = origExec })
		var b []byte
		b = append(b, []byte("  PID   TT  STAT      TIME COMMAND\n")...)
		b = append(b, []byte(strconv.Itoa(pid)+"   ??  Ss     0:01.00 /bin/claude")...)
		for k, v := range env {
			b = append(b, []byte(" "+k+"="+v)...)
		}
		b = append(b, '\n')
		execRunner = func(_ context.Context, _ string, _ ...string) ([]byte, error) { return b, nil }
	default: // linux + others read /proc
		root := t.TempDir()
		origRoot := procRoot
		t.Cleanup(func() { procRoot = origRoot })
		procRoot = root
		pidDir := filepath.Join(root, strconv.Itoa(pid))
		if err := os.MkdirAll(pidDir, 0o750); err != nil {
			t.Fatal(err)
		}
		var environ []byte
		for k, v := range env {
			environ = append(environ, []byte(k+"="+v+"\x00")...)
		}
		if err := os.WriteFile(filepath.Join(pidDir, "environ"), environ, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestResolveTranscript_ClaudeEnvToFile verifies the CLAUDE env→file resolution:
// given a temp HOME with ~/.claude/projects/<encoded-cwd>/<sid>.jsonl on disk
// and a fake process-env reader returning CLAUDE_CODE_SESSION_ID, the resolver
// returns the EXACT path. It fails when either the env var or the file is absent.
func TestResolveTranscript_ClaudeEnvToFile(t *testing.T) {
	home := t.TempDir()
	origHome := homeDir
	t.Cleanup(func() { homeDir = origHome })
	homeDir = func() (string, error) { return home, nil }

	const (
		cwd = "/Users/jonathan/code/knowledge"
		sid = "50fc2d24-aaaa-bbbb-cccc-ddddeeeeffff"
		pid = 31337
	)
	// On-disk transcript at the deterministic path.
	projDir := filepath.Join(home, ".claude", "projects", "-Users-jonathan-code-knowledge")
	if err := os.MkdirAll(projDir, 0o750); err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(projDir, sid+".jsonl")
	if err := os.WriteFile(wantPath, []byte(`{"type":"user"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	snap := SessionSnapshot{ID: "s1", Cwd: cwd, PID: pid, Comm: "claude"}

	// Happy path: env present + file present → exact path.
	injectProcessEnv(t, pid, map[string]string{"CLAUDE_CODE_SESSION_ID": sid})
	h, err := ResolveTranscript(context.Background(), snap)
	if err != nil {
		t.Fatalf("ResolveTranscript: %v", err)
	}
	if !h.Resolved() || h.Path != wantPath {
		t.Fatalf("resolved path = %q (resolved=%v), want %q", h.Path, h.Resolved(), wantPath)
	}
	if h.Format != FormatClaude {
		t.Errorf("format = %q, want claude", h.Format)
	}

	// FAILS when env absent: no CLAUDE_CODE_SESSION_ID → unresolved.
	injectProcessEnv(t, pid, map[string]string{"PATH": "/usr/bin"})
	if h, _ := ResolveTranscript(context.Background(), snap); h.Resolved() {
		t.Fatalf("env absent must yield unresolved handle, got %q", h.Path)
	}

	// FAILS when file absent: env present but transcript not on disk → unresolved.
	injectProcessEnv(t, pid, map[string]string{"CLAUDE_CODE_SESSION_ID": "no-such-session-id"})
	if h, _ := ResolveTranscript(context.Background(), snap); h.Resolved() {
		t.Fatalf("file absent must yield unresolved handle, got %q", h.Path)
	}
}

// writeCodexRollout writes a rollout-*.jsonl whose first line is a session_meta
// declaring cwd, under ~/.codex/sessions/<date>/, and stamps its mtime.
func writeCodexRollout(t *testing.T, home, dateDir, name, cwd string, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(home, ".codex", "sessions", dateDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	body := `{"type":"session_meta","payload":{"id":"` + name + `","cwd":"` + cwd + `"}}` + "\n" +
		`{"type":"response_item","payload":{"type":"message"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestResolveTranscript_CodexSessionMetaCwdMatch verifies the codex arm: given a
// synthetic ~/.codex/sessions tree with multiple rollouts whose first lines
// declare differing cwds, the resolver returns the one whose session_meta.cwd
// equals the snapshot Cwd. When two rollouts share the matching cwd, it returns
// the newest by mtime.
func TestResolveTranscript_CodexSessionMetaCwdMatch(t *testing.T) {
	home := t.TempDir()
	origHome := homeDir
	t.Cleanup(func() { homeDir = origHome })
	homeDir = func() (string, error) { return home, nil }

	// This test exercises the cwd-scan FALLBACK, so force the deterministic
	// PID→open-rollout probe to find nothing (empty lsof output).
	origExec := execRunner
	t.Cleanup(func() { execRunner = origExec })
	execRunner = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }

	const wantCwd = "/Users/jonathan/code/knowledge"
	now := time.Now()

	// A non-matching cwd rollout (must be ignored).
	writeCodexRollout(t, home, "2026/06/07", "rollout-2026-06-07T10-00-00-aaaa.jsonl",
		"/Users/jonathan/code/agent", now.Add(-2*time.Hour))
	// Two matching-cwd rollouts; the newer mtime must win.
	older := writeCodexRollout(t, home, "2026/06/08", "rollout-2026-06-08T09-00-00-bbbb.jsonl",
		wantCwd, now.Add(-1*time.Hour))
	newer := writeCodexRollout(t, home, "2026/06/08", "rollout-2026-06-08T11-00-00-cccc.jsonl",
		wantCwd, now)

	snap := SessionSnapshot{ID: "cx1", Cwd: wantCwd, PID: 7, Comm: "codex"}
	h, err := ResolveTranscript(context.Background(), snap)
	if err != nil {
		t.Fatalf("ResolveTranscript codex: %v", err)
	}
	if !h.Resolved() {
		t.Fatal("expected a resolved codex handle")
	}
	if h.Format != FormatCodex {
		t.Errorf("format = %q, want codex", h.Format)
	}
	if h.Path != newer {
		t.Fatalf("resolved %q, want newest-by-mtime %q (not older %q)", h.Path, newer, older)
	}

	// A snapshot whose cwd matches nothing resolves to unresolved.
	noMatch := SessionSnapshot{ID: "cx2", Cwd: "/nowhere", PID: 7, Comm: "codex"}
	if h, _ := ResolveTranscript(context.Background(), noMatch); h.Resolved() {
		t.Fatalf("unmatched cwd must be unresolved, got %q", h.Path)
	}
}

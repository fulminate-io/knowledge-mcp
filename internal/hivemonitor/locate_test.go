// SPDX-License-Identifier: Apache-2.0

package hivemonitor

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The cwd every fixture in this file binds against, and its project-dir
// encoding spelled out independently of the encoder under test.
const (
	testCwd            = "/Users/jonathan/code/knowledge"
	testEncodedProject = "-Users-jonathan-code-knowledge"
)

// stubHome points the resolver's home seam at a temp dir for the test's
// duration, returning it.
func stubHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	origHome := homeDir
	t.Cleanup(func() { homeDir = origHome })
	homeDir = func() (string, error) { return home, nil }
	return home
}

// stubNoOpenRollout makes the deterministic codex PID→open-rollout probe find
// nothing, so a test never shells out to the real lsof.
func stubNoOpenRollout(t *testing.T) {
	t.Helper()
	origExec := execRunner
	t.Cleanup(func() { execRunner = origExec })
	execRunner = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
}

// writeClaudeTranscript writes <home>/.claude/projects/<encoded-cwd>/<sid>.jsonl
// with one conversation line, stamps its mtime, and returns the path.
func writeClaudeTranscript(t *testing.T, home, sid string, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", testEncodedProject)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sid+".jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestResolveTranscript_ClaudeFileScanIgnoresComm pins the identity contract: a
// peer whose command name is a rewritten process title naming neither CLI still
// resolves, because the binding comes from the transcript store on disk and the
// harness session id is the file's stem. No process env is consulted.
func TestResolveTranscript_ClaudeFileScanIgnoresComm(t *testing.T) {
	home := stubHome(t)
	stubNoOpenRollout(t)

	const sid = "50fc2d24-aaaa-bbbb-cccc-ddddeeeeffff"
	wantPath := writeClaudeTranscript(t, home, sid, time.Now())

	// A rewritten process title: the command name names neither claude nor codex.
	snap := SessionSnapshot{ID: "s1", Cwd: testCwd, PID: 31337, Comm: "2.1.220"}

	h, err := ResolveTranscript(context.Background(), snap)
	if err != nil {
		t.Fatalf("ResolveTranscript: %v", err)
	}
	if !h.Resolved() || h.Path != wantPath {
		t.Fatalf("resolved path = %q (resolved=%v), want %q", h.Path, h.Resolved(), wantPath)
	}
	if h.HarnessSessionID != sid {
		t.Errorf("HarnessSessionID = %q, want the filename stem %q", h.HarnessSessionID, sid)
	}
	if h.Format != FormatClaude {
		t.Errorf("format = %q, want claude", h.Format)
	}

	// The literal command name resolves identically — comm is a hint, not a gate.
	named := SessionSnapshot{ID: "s2", Cwd: testCwd, PID: 31337, Comm: "claude"}
	if h, err := ResolveTranscript(context.Background(), named); err != nil || h.Path != wantPath {
		t.Fatalf("comm %q resolved %q (err %v), want %q", named.Comm, h.Path, err, wantPath)
	}
}

// TestResolveTranscript_ClaudeNoProjectDir verifies the unresolved contract: a
// missing project dir and an empty one both yield a zero handle and a NIL error,
// never a failure — the monitor treats unresolved as "skip this tick". The
// known-positive control is the final leg: the same cwd resolves once a
// transcript exists, so the zeros above measure absence rather than a dead scan.
func TestResolveTranscript_ClaudeNoProjectDir(t *testing.T) {
	home := stubHome(t)
	stubNoOpenRollout(t)

	snap := SessionSnapshot{ID: "s1", Cwd: testCwd, PID: 31337, Comm: "2.1.220"}

	// No project dir at all.
	h, err := ResolveTranscript(context.Background(), snap)
	if err != nil {
		t.Fatalf("missing project dir must be a nil error, got %v", err)
	}
	if h.Resolved() {
		t.Fatalf("missing project dir must be unresolved, got %q", h.Path)
	}

	// Project dir present but empty.
	dir := filepath.Join(home, ".claude", "projects", testEncodedProject)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	h, err = ResolveTranscript(context.Background(), snap)
	if err != nil {
		t.Fatalf("empty project dir must be a nil error, got %v", err)
	}
	if h.Resolved() {
		t.Fatalf("empty project dir must be unresolved, got %q", h.Path)
	}

	// KNOWN POSITIVE: the same snapshot resolves once a transcript lands, proving
	// the unresolved results above are absence and not a scan pointed at nothing.
	wantPath := writeClaudeTranscript(t, home, "now-there-is-one", time.Now())
	if h, err := ResolveTranscript(context.Background(), snap); err != nil || h.Path != wantPath {
		t.Fatalf("after writing a transcript: resolved %q (err %v), want %q", h.Path, err, wantPath)
	}
}

// captureWarnings redirects the default logger into a buffer for the test's
// duration so the resolver's diagnostic warn can be asserted.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := slog.Default()
	t.Cleanup(func() { slog.SetDefault(orig) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	return &buf
}

// TestResolveTranscript_ClaudeNewestWins verifies that among several session
// transcripts for one cwd — the normal state of a project dir, which accumulates
// every past session — the newest by mtime is bound, since the live session is
// the one appended each turn, and that the best-effort choice is warn-logged
// naming every candidate and the chosen one. A single candidate is an unambiguous
// binding and must NOT warn, which is what makes the warn assertion meaningful.
func TestResolveTranscript_ClaudeNewestWins(t *testing.T) {
	home := stubHome(t)
	stubNoOpenRollout(t)
	logs := captureWarnings(t)

	now := time.Now()
	oldest := writeClaudeTranscript(t, home, "sess-oldest", now.Add(-2*time.Hour))

	// One candidate: unambiguous, no warn.
	if h, err := ResolveTranscript(context.Background(), SessionSnapshot{ID: "s0", Cwd: testCwd, PID: 31337, Comm: "2.1.220"}); err != nil || h.Path != oldest {
		t.Fatalf("single candidate resolved %q (err %v), want %q", h.Path, err, oldest)
	}
	if logs.Len() != 0 {
		t.Fatalf("single candidate must not warn, logged: %s", logs.String())
	}

	older := writeClaudeTranscript(t, home, "sess-older", now.Add(-1*time.Hour))
	newest := writeClaudeTranscript(t, home, "sess-newest", now)

	snap := SessionSnapshot{ID: "s1", Cwd: testCwd, PID: 31337, Comm: "2.1.220"}
	h, err := ResolveTranscript(context.Background(), snap)
	if err != nil {
		t.Fatalf("ResolveTranscript: %v", err)
	}
	if h.Path != newest {
		t.Fatalf("resolved %q, want newest-by-mtime %q (not %q / %q)", h.Path, newest, older, oldest)
	}
	if h.HarnessSessionID != "sess-newest" {
		t.Errorf("HarnessSessionID = %q, want sess-newest", h.HarnessSessionID)
	}

	// The warn names every candidate and the chosen one.
	logged := logs.String()
	if !strings.Contains(logged, "multiple claude transcripts") {
		t.Fatalf("expected a multiple-candidate warn, logged: %s", logged)
	}
	for _, p := range []string{oldest, older, newest} {
		if !strings.Contains(logged, p) {
			t.Errorf("warn omits candidate %q; logged: %s", p, logged)
		}
	}
	if !strings.Contains(logged, "chosen="+newest) {
		t.Errorf("warn omits chosen=%q; logged: %s", newest, logged)
	}
}

// TestResolveTranscript_CodexCommStaysCodex verifies the routing hint still
// holds in the one direction it is trusted: a codex-naming comm resolves against
// the codex rollout store even when a claude transcript for the SAME cwd sits on
// disk and would otherwise have been the newer, winning candidate.
func TestResolveTranscript_CodexCommStaysCodex(t *testing.T) {
	home := stubHome(t)
	stubNoOpenRollout(t)

	now := time.Now()
	rollout := writeCodexRollout(t, home, "2026/06/08", "rollout-2026-06-08T09-00-00-bbbb.jsonl",
		testCwd, now.Add(-1*time.Hour))
	// A NEWER claude transcript for the same cwd — the codex peer must not bind it.
	claudePath := writeClaudeTranscript(t, home, "sess-claude", now)

	snap := SessionSnapshot{ID: "cx1", Cwd: testCwd, PID: 7, Comm: "codex"}
	h, err := ResolveTranscript(context.Background(), snap)
	if err != nil {
		t.Fatalf("ResolveTranscript codex: %v", err)
	}
	if h.Format != FormatCodex || h.Path != rollout {
		t.Fatalf("codex comm resolved %q (format %q), want the rollout %q — never the claude transcript %q",
			h.Path, h.Format, rollout, claudePath)
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

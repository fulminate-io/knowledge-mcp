// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeRecordingFake writes an executable shell script named client into
// dir that appends its argv (one space-joined line) to logPath and exits
// 0. Returns nothing — the caller puts dir on PATH via withPATH. Skips on
// Windows (the sh-script recording idiom is POSIX-only; the registration
// logic itself is OS-agnostic).
func writeRecordingFake(t *testing.T, dir, client, logPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("recording-fake shell script is POSIX-only")
	}
	script := "#!/bin/sh\necho \"$@\" >> " + shellQuote(logPath) + "\nexit 0\n"
	bin := filepath.Join(dir, client)
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatalf("write fake %s: %v", client, err)
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// recordedLines returns the captured argv lines from the fake's log, or
// nil when the log was never written (the fake was never invoked).
func recordedLines(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read log: %v", err)
	}
	var out []string
	for ln := range strings.SplitSeq(strings.TrimRight(string(data), "\n"), "\n") {
		if ln != "" {
			out = append(out, ln)
		}
	}
	return out
}

// TestRegisterKnowledgeMCP_ClaudeArgv: claude registration emits a
// preceding `mcp remove knowledge` then the add-json form
// `mcp add-json -s user knowledge '<json>'` — no stdio `-- <abs>`
// command and no `--transport http`. The JSON config must carry the
// per-server "timeout" (mcpToolTimeoutMs) so long ops are not cut off by
// the client default.
func TestRegisterKnowledgeMCP_ClaudeArgv(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "argv.log")
	writeRecordingFake(t, dir, "claude", log)
	withPATH(t, dir)

	if err := registerKnowledgeMCP("claude", []string{"-s", "user"}, false); err != nil {
		t.Fatalf("registerKnowledgeMCP: %v", err)
	}
	lines := recordedLines(t, log)
	if len(lines) != 2 {
		t.Fatalf("captured %d argv lines, want 2 (remove + add): %v", len(lines), lines)
	}
	if lines[0] != "mcp remove knowledge" {
		t.Errorf("first argv = %q, want %q", lines[0], "mcp remove knowledge")
	}
	// The fake records space-joined argv; the JSON is the last token. Assert
	// the verb/scope/name prefix and the timeout presence rather than exact
	// byte equality (JSON key ordering is from claudeServerJSON).
	add := lines[1]
	wantPrefix := "mcp add-json -s user knowledge "
	if !strings.HasPrefix(add, wantPrefix) {
		t.Errorf("second argv = %q, want prefix %q", add, wantPrefix)
	}
	if strings.Contains(add, "--transport") {
		t.Errorf("second argv still uses --transport: %q", add)
	}
	wantJSON, err := claudeServerJSON(daemonMCPURL())
	if err != nil {
		t.Fatalf("claudeServerJSON: %v", err)
	}
	if got := strings.TrimPrefix(add, wantPrefix); got != wantJSON {
		t.Errorf("add-json payload = %q, want %q", got, wantJSON)
	}
	if !strings.Contains(wantJSON, `"timeout":180000`) {
		t.Errorf("add-json payload missing timeout: %q", wantJSON)
	}
}

// TestRegisterKnowledgeMCP_CodexArgv: codex registration emits a
// preceding `mcp remove knowledge` then the codex url-form add
// `mcp add knowledge --url <daemon-url>` (codex names the streamable-HTTP
// target with --url, not claude's --transport http) and no scope flag.
func TestRegisterKnowledgeMCP_CodexArgv(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "argv.log")
	writeRecordingFake(t, dir, "codex", log)
	withPATH(t, dir)
	// Isolate HOME so the codex registration's config.toml patch writes to a
	// throwaway dir, never the developer's real ~/.codex/config.toml.
	withHOME(t, t.TempDir())

	if err := registerKnowledgeMCP("codex", nil, false); err != nil {
		t.Fatalf("registerKnowledgeMCP: %v", err)
	}
	lines := recordedLines(t, log)
	if len(lines) != 2 {
		t.Fatalf("captured %d argv lines, want 2: %v", len(lines), lines)
	}
	if lines[0] != "mcp remove knowledge" {
		t.Errorf("first argv = %q, want remove", lines[0])
	}
	want := "mcp add knowledge --url " + daemonMCPURL()
	if lines[1] != want {
		t.Errorf("second argv = %q, want %q", lines[1], want)
	}
}

// TestRegisterKnowledgeMCP_MissingCLI: with the client absent from PATH,
// registration returns nil (non-fatal) and records no argv.
func TestRegisterKnowledgeMCP_MissingCLI(t *testing.T) {
	withPATH(t, t.TempDir()) // empty dir → claude not found
	if err := registerKnowledgeMCP("claude", []string{"-s", "user"}, false); err != nil {
		t.Errorf("missing CLI should be non-fatal, got err: %v", err)
	}
}

// TestRegisterKnowledgeMCP_DryRun: dry-run prints argv and records
// nothing (the fake is never invoked).
func TestRegisterKnowledgeMCP_DryRun(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "argv.log")
	writeRecordingFake(t, dir, "claude", log)
	withPATH(t, dir)

	if err := registerKnowledgeMCP("claude", []string{"-s", "user"}, true); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if lines := recordedLines(t, log); len(lines) != 0 {
		t.Errorf("dry-run recorded argv %v, want none", lines)
	}
}

// TestRunInstallClaudeAssets_NoMCP: --no-mcp skips registration (no argv
// recorded); without it, registration runs.
func TestRunInstallClaudeAssets_NoMCP(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "argv.log")
	writeRecordingFake(t, dir, "claude", log)
	withPATH(t, dir)
	withStubExecutable(t, "/opt/knowledge/bin/knowledge")

	destRoot := t.TempDir()
	claudeMD := filepath.Join(destRoot, "CLAUDE.md")
	dotClaude := filepath.Join(destRoot, "dotclaude")

	// --no-mcp: no argv recorded.
	if err := runInstallClaudeAssets([]string{"--dest", dotClaude, "--claude-md-dest", claudeMD, "--no-mcp"}); err != nil {
		t.Fatalf("install --no-mcp: %v", err)
	}
	if lines := recordedLines(t, log); len(lines) != 0 {
		t.Errorf("--no-mcp recorded argv %v, want none", lines)
	}

	// Without --no-mcp: registration runs (remove + add).
	if err := runInstallClaudeAssets([]string{"--dest", dotClaude, "--claude-md-dest", claudeMD}); err != nil {
		t.Fatalf("install (mcp on): %v", err)
	}
	if lines := recordedLines(t, log); len(lines) != 2 {
		t.Errorf("default install recorded %d argv lines, want 2: %v", len(lines), lines)
	}
}

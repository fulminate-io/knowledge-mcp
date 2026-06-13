// SPDX-License-Identifier: Apache-2.0

package hivemonitor

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// capturedPSEnv is a captured `ps eww -p <pid>` output whose trailing env run
// includes CLAUDE_CODE_SESSION_ID. The header + process line + env tokens
// mirror the real wide-output shape.
const capturedPSEnv = `  PID   TT  STAT      TIME COMMAND
12345   ??  Ss     0:03.21 /Applications/Claude.app/Contents/MacOS/claude --resume SHLVL=1 PATH=/usr/bin:/bin CLAUDE_PROJECT_DIR=/Users/jonathan/code/knowledge CLAUDE_CODE_SESSION_ID=50fc2d24-1111-2222-3333-444455556666 TERM=xterm-256color
`

// TestProcessEnvDarwin_ParsesSessionID verifies the macOS parse arm: with the
// exec seam injected to emit a captured `ps eww` fixture whose trailing env run
// includes CLAUDE_CODE_SESSION_ID, processEnvDarwin returns it; a fixture
// without the key yields "".
func TestProcessEnvDarwin_ParsesSessionID(t *testing.T) {
	orig := execRunner
	t.Cleanup(func() { execRunner = orig })

	execRunner = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(capturedPSEnv), nil
	}
	env, err := processEnvDarwin(context.Background(), 12345)
	if err != nil {
		t.Fatalf("processEnvDarwin: %v", err)
	}
	const want = "50fc2d24-1111-2222-3333-444455556666"
	if got := env["CLAUDE_CODE_SESSION_ID"]; got != want {
		t.Fatalf("CLAUDE_CODE_SESSION_ID = %q, want %q", got, want)
	}
	if got := env["CLAUDE_PROJECT_DIR"]; got != "/Users/jonathan/code/knowledge" {
		t.Errorf("CLAUDE_PROJECT_DIR = %q, want the project dir", got)
	}

	// ProcessEnvValue is the focused single-key accessor used by locate.go.
	if got := parseEnvFromPS(capturedPSEnv)["CLAUDE_CODE_SESSION_ID"]; got != want {
		t.Errorf("parseEnvFromPS CLAUDE_CODE_SESSION_ID = %q, want %q", got, want)
	}

	// A fixture WITHOUT the key returns the empty string.
	execRunner = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte("  PID   TT  STAT      TIME COMMAND\n999   ??  Ss   0:01.00 /bin/claude PATH=/usr/bin TERM=xterm\n"), nil
	}
	noKey, err := processEnvDarwin(context.Background(), 999)
	if err != nil {
		t.Fatalf("processEnvDarwin (no key): %v", err)
	}
	if got := noKey["CLAUDE_CODE_SESSION_ID"]; got != "" {
		t.Errorf("expected empty session id when key absent, got %q", got)
	}
}

// TestProcessEnvLinux_ParsesEnviron verifies the Linux parse arm: given an
// overridable proc root pointed at a temp dir containing <pid>/environ with
// NUL-separated KEY=VALUE pairs (including CLAUDE_CODE_SESSION_ID), ProcessEnv
// returns the full map and the single-key accessor returns the session id.
func TestProcessEnvLinux_ParsesEnviron(t *testing.T) {
	root := t.TempDir()
	const pid = 4242
	const sid = "abcd1234-5678-90ab-cdef-1234567890ab"
	pidDir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(pidDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// NUL-separated KEY=VALUE pairs, with a trailing NUL (as real environ has).
	environ := "SHLVL=1\x00CLAUDE_CODE_SESSION_ID=" + sid + "\x00PATH=/usr/bin\x00"
	if err := os.WriteFile(filepath.Join(pidDir, "environ"), []byte(environ), 0o600); err != nil {
		t.Fatal(err)
	}

	origRoot := procRoot
	t.Cleanup(func() { procRoot = origRoot })
	procRoot = root

	env, err := processEnvLinux(pid)
	if err != nil {
		t.Fatalf("processEnvLinux: %v", err)
	}
	if got := env["CLAUDE_CODE_SESSION_ID"]; got != sid {
		t.Fatalf("CLAUDE_CODE_SESSION_ID = %q, want %q", got, sid)
	}
	if got := env["PATH"]; got != "/usr/bin" {
		t.Errorf("PATH = %q, want /usr/bin", got)
	}
	if len(env) != 3 {
		t.Errorf("expected 3 env pairs (trailing NUL skipped), got %d: %v", len(env), env)
	}
}

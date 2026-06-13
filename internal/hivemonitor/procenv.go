// SPDX-License-Identifier: Apache-2.0

package hivemonitor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// execRunner is the sibling exec seam through which the macOS env reader shells
// out to `ps`, so the OUTPUT PARSING is unit-tested against captured fixtures
// without a live process. It defaults to exec.CommandContext (the same idiom as
// graphclient/peer_cwd.go's peerCwdRunner — a sibling var of identical shape,
// not a cross-package reach, since hivemonitor is its own package) and is
// overridden in tests.
//
//nolint:gochecknoglobals // package-level seam for command injection in tests; mirrors the peer_cwd exec idiom.
var execRunner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// procRoot is the root the Linux arm reads /<pid>/environ under. It defaults to
// "/proc" and is overridden in tests to point at a temp dir, so the Linux parse
// arm is exercised without a real /proc.
//
//nolint:gochecknoglobals // overridable filesystem root for testability; mirrors the exec-seam idiom.
var procRoot = "/proc"

// ProcessEnv returns the target process's environment as a KEY→VALUE map,
// cross-platform:
//
//   - macOS (darwin): `ps eww -p <pid>` emits the process line followed by its
//     environment as a trailing run of KEY=VALUE tokens; parseEnvFromPS splits
//     that run.
//   - Linux: /proc/<pid>/environ is the NUL-separated KEY=VALUE environment;
//     parseEnvironFile splits on '\x00'.
//
// Other platforms return an error (the daemon only runs where claude/codex do —
// macOS and Linux). The reader is used by locate.go's claude branch to read
// CLAUDE_CODE_SESSION_ID from the peer agent process.
func ProcessEnv(ctx context.Context, pid int) (map[string]string, error) {
	switch runtime.GOOS {
	case "linux":
		return processEnvLinux(pid)
	case "darwin":
		return processEnvDarwin(ctx, pid)
	default:
		return nil, fmt.Errorf("hivemonitor: ProcessEnv unsupported on %s", runtime.GOOS)
	}
}

// ProcessEnvValue is the focused single-key lookup locate.go needs
// (CLAUDE_CODE_SESSION_ID). It returns the value for key, or "" when the key is
// absent or the environment could not be read.
func ProcessEnvValue(ctx context.Context, pid int, key string) string {
	env, err := ProcessEnv(ctx, pid)
	if err != nil {
		return ""
	}
	return env[key]
}

// processEnvDarwin shells `ps eww -p <pid>` and parses the trailing env run.
func processEnvDarwin(ctx context.Context, pid int) (map[string]string, error) {
	out, err := execRunner(ctx, "ps", "eww", "-p", strconv.Itoa(pid))
	if err != nil {
		return nil, fmt.Errorf("hivemonitor: ps eww -p %d: %w", pid, err)
	}
	return parseEnvFromPS(string(out)), nil
}

// processEnvLinux reads <procRoot>/<pid>/environ and splits the NUL-separated
// KEY=VALUE pairs.
func processEnvLinux(pid int) (map[string]string, error) {
	path := filepath.Join(procRoot, strconv.Itoa(pid), "environ")
	data, err := os.ReadFile(path) //nolint:gosec // pid is an int formatted into a fixed proc path, not user text.
	if err != nil {
		return nil, fmt.Errorf("hivemonitor: read %s: %w", path, err)
	}
	return parseEnvironFile(data), nil
}

// parseEnvironFile splits /proc/<pid>/environ contents (NUL-separated
// KEY=VALUE) into a map. A trailing NUL produces an empty final field that is
// skipped.
func parseEnvironFile(data []byte) map[string]string {
	env := make(map[string]string)
	for pair := range strings.SplitSeq(string(data), "\x00") {
		if pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		if !ok || k == "" {
			continue
		}
		env[k] = v
	}
	return env
}

// parseEnvFromPS extracts the environment KEY=VALUE pairs from `ps eww -p <pid>`
// output. The wide output is the header line, then the process line whose
// command is followed by the environment as space-separated KEY=VALUE tokens.
// We scan ALL whitespace-separated tokens across the data line(s) and keep
// every one that looks like KEY=VALUE with a shell-identifier KEY — the command
// arguments rarely match that shape, and the keys we care about
// (CLAUDE_CODE_SESSION_ID, CLAUDE_PROJECT_DIR) are unambiguous.
func parseEnvFromPS(out string) map[string]string {
	env := make(map[string]string)
	for line := range strings.SplitSeq(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Skip the header row ("PID TTY ... COMMAND" or "  PID ...").
		if strings.HasPrefix(trimmed, "PID") || strings.HasPrefix(trimmed, "USER") {
			continue
		}
		for tok := range strings.FieldsSeq(trimmed) {
			k, v, ok := strings.Cut(tok, "=")
			if !ok || !isEnvKey(k) {
				continue
			}
			env[k] = v
		}
	}
	return env
}

// isEnvKey reports whether s is a plausible environment variable name: a
// non-empty run of [A-Za-z_][A-Za-z0-9_]*. This filters out command-line
// fragments that happen to contain '=' (e.g. a flag value) from being mistaken
// for env pairs in the ps output.
func isEnvKey(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r == '_':
			// always valid
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

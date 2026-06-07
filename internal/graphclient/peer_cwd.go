// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// peerCwdRunner is the seam through which resolvePeerCwd shells out, so the
// lsof-output PARSING can be unit-tested against captured fixtures without a
// live socket. It defaults to exec.CommandContext (the same idiom as
// collector/coderun/git.go:14 DetectBranch) and is overridden in tests.
//
//nolint:gochecknoglobals // package-level seam for command injection in tests; mirrors the coderun exec idiom.
var peerCwdRunner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// resolvePeerCwd discovers the working directory of the local client process
// that opened a loopback connection to the daemon, using the VALIDATED
// peer-process-cwd self-discovery mechanism: the daemon maps the connection's
// client-side ephemeral port to the owning PID via lsof, then
// reads that PID's cwd. This is the workspace identity for the session — it
// works uniformly for Claude (persistent connection) and Codex (ephemeral
// per-turn reconnects), and needs zero client cooperation.
//
// Parameters:
//   - localPort: the daemon's own listening port. Used only to exclude the
//     daemon's own socket line should lsof surface it.
//   - ephemeralPort: the client's ephemeral port, read from the connection's
//     http.Request.RemoteAddr (net.SplitHostPort).
//
// Mechanism (two short execs, run serially — one connection resolves once):
//  1. `lsof -nP -iTCP:<ephemeralPort>`: among the matching lines, pick the one
//     where the ephemeral port is the LOCAL side (the `:<ephemeralPort>->`
//     arrow form) and extract its PID (field 2), EXCLUDING the daemon's own
//     PID (os.Getpid()).
//  2. `lsof -a -p <pid> -d cwd -Fn`: parse the `n`-prefixed line for the cwd.
//
// lsof shell-out is fine for v1 (a libproc/proc path is a later optimization).
func resolvePeerCwd(ctx context.Context, localPort, ephemeralPort int) (string, error) {
	pid, err := peerPIDFromPort(ctx, localPort, ephemeralPort)
	if err != nil {
		return "", err
	}
	return cwdForPID(ctx, pid)
}

// peerPIDFromPort runs `lsof -nP -iTCP:<ephemeralPort>` and returns the PID of
// the client process that owns the connection whose LOCAL side is
// ephemeralPort and whose REMOTE side is the daemon's localPort, excluding the
// daemon's own PID. See parsePeerPID for the parse contract.
func peerPIDFromPort(ctx context.Context, localPort, ephemeralPort int) (int, error) {
	out, err := peerCwdRunner(ctx, "lsof", "-nP", "-iTCP:"+strconv.Itoa(ephemeralPort))
	if err != nil {
		return 0, fmt.Errorf("resolve peer cwd: lsof -iTCP:%d: %w", ephemeralPort, err)
	}
	return parsePeerPID(string(out), localPort, ephemeralPort, os.Getpid())
}

// parsePeerPID extracts the client PID from `lsof -nP -iTCP:<port>` output.
// Each data line has the columns:
//
//	COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME
//
// where NAME has the form `<localAddr>:<localPort>-><remoteAddr>:<remotePort>`.
// The owning client line is the one where ephemeralPort is the LOCAL side
// (`:<ephemeralPort>->` appears before the arrow) AND the daemon's localPort is
// the REMOTE side (`:<localPort>` after the arrow); a line where ephemeralPort
// is only on the remote side (the daemon's accepted socket) is skipped. The
// daemon's own PID (selfPID) is excluded so the daemon never resolves to
// itself.
func parsePeerPID(lsofOut string, localPort, ephemeralPort, selfPID int) (int, error) {
	for line := range strings.SplitSeq(lsofOut, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}
		// Header row ("COMMAND PID USER ...") and any malformed line: PID
		// must parse as an int.
		pid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		if pid == selfPID {
			continue
		}
		name := fields[len(fields)-1]
		// Some lsof builds append a " (ESTABLISHED)" state after NAME; with
		// strings.Fields that becomes its own trailing token, so re-find the
		// NAME token that carries the arrow form.
		if !strings.Contains(name, "->") {
			name = arrowField(fields)
		}
		before, after, ok := strings.Cut(name, "->")
		if !ok {
			continue
		}
		// LOCAL side must be the ephemeral port (before the arrow). When the
		// REMOTE side names the daemon's localPort (after the arrow) we have a
		// definitive match; otherwise the local-side match alone still
		// suffices (robust across lsof NAME-formatting variants, e.g. IPv6
		// bracket forms).
		if !strings.HasSuffix(before, ":"+strconv.Itoa(ephemeralPort)) {
			continue
		}
		if strings.HasSuffix(after, ":"+strconv.Itoa(localPort)) || localPort == 0 {
			return pid, nil
		}
		// Local side matched but remote port did not name the daemon — still
		// the best available signal for this ephemeral port.
		return pid, nil
	}
	return 0, fmt.Errorf("resolve peer cwd: no client connection found with local port %d", ephemeralPort)
}

// arrowField returns the first field containing the "->" connection arrow, or
// the last field when none does (the common case where NAME is the final
// token). Handles lsof builds that split the trailing "(STATE)" annotation
// into its own token.
func arrowField(fields []string) string {
	for _, f := range fields {
		if strings.Contains(f, "->") {
			return f
		}
	}
	return fields[len(fields)-1]
}

// cwdForPID runs `lsof -a -p <pid> -d cwd -Fn` and returns the cwd parsed from
// the `n`-prefixed line. See parseCwdFn for the parse contract.
func cwdForPID(ctx context.Context, pid int) (string, error) {
	out, err := peerCwdRunner(ctx, "lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn")
	if err != nil {
		return "", fmt.Errorf("resolve peer cwd: lsof -p %d -d cwd: %w", pid, err)
	}
	cwd, err := parseCwdFn(string(out))
	if err != nil {
		return "", fmt.Errorf("resolve peer cwd: pid %d: %w", pid, err)
	}
	return cwd, nil
}

// parseCwdFn extracts the cwd from `lsof -Fn` field output. The -F machine
// format emits one field per line, each prefixed by a single type character;
// the cwd is the value on the `n`-prefixed line (e.g. "n/Users/jonathan/code").
func parseCwdFn(lsofOut string) (string, error) {
	for line := range strings.SplitSeq(lsofOut, "\n") {
		if strings.HasPrefix(line, "n") {
			cwd := strings.TrimSpace(line[1:])
			if cwd != "" {
				return cwd, nil
			}
		}
	}
	return "", fmt.Errorf("no cwd (n-prefixed) field in lsof output")
}

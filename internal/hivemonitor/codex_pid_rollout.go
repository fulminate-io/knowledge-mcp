// SPDX-License-Identifier: Apache-2.0

package hivemonitor

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// execRunner is the exec seam through which the PID→open-rollout probe shells
// out to `lsof`, so the OUTPUT PARSING is unit-tested against captured fixtures
// without a live process. It defaults to exec.CommandContext (the same idiom as
// graphclient/peer_cwd.go's peerCwdRunner — a sibling var of identical shape,
// not a cross-package reach, since hivemonitor is its own package) and is
// overridden in tests.
//
//nolint:gochecknoglobals // package-level seam for command injection in tests; mirrors the peer_cwd exec idiom.
var execRunner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// codexWriteRolloutForPID returns the rollout-*.jsonl file the codex process
// pid holds OPEN FOR WRITING, when there is exactly one. A live codex agent
// appends to its own rollout — the very file codexReader tails — so the process
// holds it open, and lsof on the PID names the EXACT transcript. This resolves
// the same-directory ambiguity a session_meta.cwd match cannot: two codex
// agents in one repo dir each hold their OWN rollout, distinguished by PID, not
// cwd. The PID is the same loopback-lsof-resolved peer PID (peer_cwd.go) that
// produced the snapshot's cwd, so no new identity signal is introduced.
//
// Returns ("", false) when the agent holds no write rollout open (idle /
// pre-first-turn — there is no active work to monitor yet) or holds more than
// one (ambiguous); the caller falls back to the cwd scan.
func codexWriteRolloutForPID(ctx context.Context, pid int) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	// -Ffan emits, per open file, an `f<fd>` record header, an `a<access>` field
	// (r/w/u), and an `n<name>` field. We keep names that are codex rollouts
	// opened for writing ('w' or read-write 'u') — a resumed session may also
	// hold the prior rollout open read-only, which the access filter excludes.
	out, err := execRunner(ctx, "lsof", "-p", strconv.Itoa(pid), "-Ffan")
	if err != nil {
		// lsof failed (process gone, no permission) — unresolved via PID; the
		// caller falls back to the cwd scan.
		return "", false
	}
	rollouts := parseCodexWriteRollouts(string(out))
	if len(rollouts) == 1 {
		return rollouts[0], true
	}
	return "", false
}

// parseCodexWriteRollouts walks `lsof -Ffan` machine output and returns the
// distinct codex rollout paths the process holds open for writing. Each open
// file is an `f<fd>`-delimited record carrying an `a<access>` mode then an
// `n<name>`; a rollout counts only when its access is 'w' or 'u' (read-write).
func parseCodexWriteRollouts(lsofOut string) []string {
	var out []string
	seen := make(map[string]bool)
	var access, name string
	flush := func() {
		if name != "" && isCodexRolloutPath(name) && (access == "w" || access == "u") && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
		access, name = "", ""
	}
	for line := range strings.SplitSeq(lsofOut, "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'f':
			flush() // close the previous file record before starting the next
		case 'a':
			access = line[1:]
		case 'n':
			name = line[1:]
		}
	}
	flush()
	return out
}

// isCodexRolloutPath reports whether p is a codex rollout transcript: a
// rollout-*.jsonl file under ~/.codex/sessions.
func isCodexRolloutPath(p string) bool {
	if !strings.Contains(p, "/.codex/sessions/") {
		return false
	}
	base := filepath.Base(p)
	return strings.HasPrefix(base, "rollout-") && strings.HasSuffix(base, ".jsonl")
}

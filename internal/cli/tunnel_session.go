// SPDX-License-Identifier: Apache-2.0

// tunnel_session.go — the dev-VM tmux SESSION-NAME model for `knowledge tunnel`.
// It owns the FULMINATE_SESSION name the CLI delivers to the VM: the
// allowlist it must satisfy, the connect-shape selectors parsed from the flags
// (tunnelOpts), and the persisted-per-env default resolution (reattach on re-run,
// --new to rotate, --reuse/--session to pin) backed by a `<env>.session` sidecar
// under ~/.knowledge/ssh. runTunnel (tunnel.go) orchestrates these; the on-disk
// identity + ssh-argv mechanics live in tunnel_ssh.go.

package cli

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// sessionNamePattern is the allowlist a FULMINATE_SESSION name must match. It is
// the SAME anchored allowlist the dev-VM cloud-config ForceCommand validates the
// value against BEFORE interpolating it into the tmux `-s` argument (leading char
// alphanumeric so it can never be read as a tmux flag; ≤64 chars; no spaces or
// shell metacharacters). The two live in separate repos (no shared package), so
// the literal is duplicated and guarded by tunnel_test.go on this side. This
// client-side check rejects a bad --session/--reuse value up front rather than
// letting the VM silently fall back to `s-$$`.
const sessionNamePattern = `^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`

// sessionNameRE is the compiled, package-level allowlist matcher for session names.
var sessionNameRE = regexp.MustCompile(sessionNamePattern)

// sessionOriginCLI is the origin prefix marking a session as the CLI's (the web
// uses "web"), so `--list-sessions` reads legibly. It leads every generated CLI
// session name.
const sessionOriginCLI = "cli"

// sessionWords is a small curated wordlist for HUMAN-READABLE session suffixes: a
// --new session reads as "cli-<host>-otter" instead of opaque hex. Order is
// irrelevant; every entry is lowercase [a-z] so any suffix satisfies the allowlist.
var sessionWords = []string{
	"otter", "falcon", "badger", "heron", "marmot", "lynx", "ibis", "koala",
	"panda", "tapir", "egret", "raven", "gecko", "cobra", "bison", "crane",
	"finch", "gull", "hare", "jay", "kite", "lark", "mink", "newt",
	"owl", "puma", "quail", "robin", "seal", "toad", "vole", "wren",
}

// hostLabel derives a short, allowlist-safe label from the machine hostname for
// human-readable session names ("cli-jonathans-macbook"): lowercased, first dotted
// component only, every non-[a-z0-9] run collapsed to a single '-', trimmed, capped
// at 24 chars. Returns "" when the hostname is unavailable or sanitizes to nothing,
// in which case the caller falls back to the bare origin.
func hostLabel() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return ""
	}
	h = strings.ToLower(h)
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	var b strings.Builder
	prevDash := false
	for _, r := range h {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		case !prevDash:
			b.WriteByte('-')
			prevDash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 24 {
		s = strings.Trim(s[:24], "-")
	}
	return s
}

// cliDefaultSessionName is the persisted per-env default: "cli-<host>" (or the bare
// "cli" when the host can't be derived) — human-readable and identifying the client,
// so `--list-sessions` distinguishes your CLI-on-this-machine session from the web's.
func cliDefaultSessionName() string {
	if h := hostLabel(); h != "" {
		return sessionOriginCLI + "-" + h
	}
	return sessionOriginCLI
}

// tunnelOpts carries the connect-shape selectors parsed from TunnelCmd's flags:
// whether to emit a ProxyCommand block instead of connecting, the session model
// (fresh/persisted-default/pinned/list/kill), and any one-shot remote command.
type tunnelOpts struct {
	printProxy    bool
	newSession    bool     // --new: rotate the persisted per-env default to a fresh name
	pinnedSession string   // --reuse/--session: attach this exact name, leave the default untouched
	listSessions  bool     // --list-sessions: one-shot `tmux ls`, no FULMINATE_SESSION threaded
	killSession   string   // --kill: one-shot `tmux kill-session -t <name>`, then exit
	renameSession string   // --rename: `<new>` renames the env default, `<old>=<new>` a specific session
	command       []string // --command / `-- <cmd...>`: run this non-interactively (no PTY), no session threaded
}

// resolveSessionName determines the tmux session name to deliver to the VM:
//   - a pinned name (--reuse/--session) is validated and returned WITHOUT persisting
//     (attaching a specific session must not change the env's default);
//   - --new generates a fresh name and persists it as the new per-env default;
//   - with no flag, the persisted per-env default is read back (reattach); if the
//     sidecar is absent or invalid, a fresh name is generated and persisted.
//
// persist reports whether the caller should write the name back to the sidecar.
func resolveSessionName(dir, env, pinned string, newSession bool) (name string, persist bool, err error) {
	if pinned != "" {
		if !sessionNameRE.MatchString(pinned) {
			return "", false, fmt.Errorf("invalid session name %q: must match %s (no leading hyphen, letters/digits/_/- only, ≤64 chars)", pinned, sessionNamePattern)
		}
		return pinned, false, nil
	}
	if newSession {
		n, err := generateSessionName()
		if err != nil {
			return "", false, err
		}
		return n, true, nil
	}
	if n, ok := readSessionSidecar(dir, env); ok {
		return n, false, nil
	}
	// First connect for this env: the human-recognizable client default (persisted),
	// not a random handle, so `--list-sessions` reads as `cli-<host>` not opaque hex.
	return cliDefaultSessionName(), true, nil
}

// generateSessionName mints a fresh HUMAN-READABLE CLI session name for --new: the
// client default plus a memorable word ("cli-<host>-otter"), so a rotated session is
// legible in `--list-sessions` rather than opaque hex. The word is chosen with
// crypto/rand; every component is allowlist-safe by construction.
func generateSessionName() (string, error) {
	var b [1]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate session name: %w", err)
	}
	word := sessionWords[int(b[0])%len(sessionWords)]
	return cliDefaultSessionName() + "-" + word, nil
}

// sessionSidecarPath is the per-env persisted-default session file, alongside the
// env's identity files (reusing connectionName so distinct envs never share one).
func sessionSidecarPath(dir, env string) string {
	return filepath.Join(dir, connectionName(env)+".session")
}

// readSessionSidecar reads the persisted per-env default session name, returning
// ok=false when the file is absent or its contents fail the allowlist (so a
// corrupt/hand-edited sidecar regenerates rather than delivering a bad name).
func readSessionSidecar(dir, env string) (string, bool) {
	data, err := os.ReadFile(sessionSidecarPath(dir, env))
	if err != nil {
		return "", false
	}
	name := strings.TrimSpace(string(data))
	if !sessionNameRE.MatchString(name) {
		return "", false
	}
	return name, true
}

// writeSessionSidecar persists the per-env default session name at 0600 (owner-only,
// like the identity files it sits beside).
func writeSessionSidecar(dir, env, name string) error {
	if err := os.WriteFile(sessionSidecarPath(dir, env), []byte(name+"\n"), 0o600); err != nil {
		return fmt.Errorf("write session sidecar: %w", err)
	}
	return nil
}

// parseRenameSpec resolves a --rename value into (old, newName). "<old>=<new>" renames
// a specific session; a bare "<new>" renames the env's persisted default (or the client
// default when no sidecar exists yet). Both names are allowlist-validated — old is a
// tmux `-t` target and new becomes the session's `-s` name, so a bad value must never
// reach the remote argv.
func parseRenameSpec(spec, dir, env string) (old, newName string, err error) {
	if before, after, found := strings.Cut(spec, "="); found {
		old, newName = before, after
	} else {
		newName = spec
		if n, ok := readSessionSidecar(dir, env); ok {
			old = n
		} else {
			old = cliDefaultSessionName()
		}
	}
	if !sessionNameRE.MatchString(old) {
		return "", "", fmt.Errorf("invalid current session name %q: must match %s", old, sessionNamePattern)
	}
	if !sessionNameRE.MatchString(newName) {
		return "", "", fmt.Errorf("invalid new session name %q: must match %s (no leading hyphen, letters/digits/_/- only, ≤64 chars)", newName, sessionNamePattern)
	}
	return old, newName, nil
}

// SPDX-License-Identifier: Apache-2.0

package hivemonitor

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/transcripts"
)

// TranscriptFormat names the on-disk transcript shape, selecting the tail
// reader for a handle.
type TranscriptFormat string

const (
	// FormatClaude is the ~/.claude/projects/<encoded-cwd>/<id>.jsonl shape.
	FormatClaude TranscriptFormat = "claude"
	// FormatCodex is the ~/.codex/sessions/**/rollout-*.jsonl shape.
	FormatCodex TranscriptFormat = "codex"
)

// SessionSnapshot is the per-session identity the resolver binds to a
// transcript: the resolved workspace cwd, the peer process PID, and the peer
// process command name (comm). Comm is a ROUTING HINT ONLY — a harness may
// rewrite its process title, so the command name can name anything. It is
// produced by the HTTPServer session-snapshot read seam (Phase 3) and consumed
// here.
type SessionSnapshot struct {
	ID   string
	Cwd  string
	PID  int
	Comm string
}

// TranscriptHandle is the resolved binding: the exact transcript file path, its
// format, and the HARNESS session-id (claude transcript filename stem / codex
// rollout session_meta.id) — the file-sourced, LLM-uncontrolled identity the ban
// keys on and the value passed as HiveRequest.MemberSession on renew. A zero
// TranscriptHandle (empty Path) means "not resolved" — the monitor skips that
// claim this tick rather than treating it as DEAD.
type TranscriptHandle struct {
	Path string
	// HarnessSessionID is the harness-sourced session identity (NOT the
	// reconnect-volatile Mcp-Session-Id): the claude transcript's filename stem
	// (the scheme names each session file <sessionId>.jsonl) or codex's rollout
	// session_meta.id. Stable for the CLI session's life and LLM-uncontrolled,
	// so it is the ban key and the renew MemberSession.
	HarnessSessionID string
	Format           TranscriptFormat
}

// Resolved reports whether the handle names an actual file.
func (h TranscriptHandle) Resolved() bool { return h.Path != "" }

// homeDir resolves the user's home directory. Overridable in tests so the
// claude/codex resolution runs against a temp HOME without mutating process
// env. Defaults to os.UserHomeDir (the same derivation as resolveClaudeDest in
// install_claude_assets.go).
//
//nolint:gochecknoglobals // overridable home seam for testability; mirrors the exec-seam idiom.
var homeDir = os.UserHomeDir

// ResolveTranscript deterministically binds a session to its EXACT transcript
// file. The peer's command name is a ROUTING HINT, not an identity source: a
// harness may rewrite its process title, so a comm that does not name codex is
// tried against the claude transcript store on disk FIRST — the same
// file-derived identity the transcript uploader already trusts — and falls back
// to the codex resolvers when that binds nothing.
//
//   - claude: list the session transcripts sitting directly in
//     ~/.claude/projects/<encoded-cwd>, where encoded-cwd is Cwd with every '/'
//     replaced by '-' (the documented claude project-dir scheme), and bind the
//     newest by mtime — the live session is the one appended every turn. The
//     scheme names each file <sessionId>.jsonl, so the filename stem IS the
//     harness session id; no process introspection is involved.
//   - codex: PRIMARY (deterministic) — the live agent holds its own
//     rollout-*.jsonl open for writing, so lsof on the peer PID names the EXACT
//     file, immune to the cwd collision two same-directory agents cause.
//     FALLBACK (no rollout held open yet) — scan ~/.codex/sessions, read ONLY
//     the first line of each rollout, decode session_meta, match payload.cwd ==
//     snapshot.Cwd, and among matches pick the newest by mtime.
//
// A zero (unresolved) handle is returned with a nil error when the binding
// cannot be made (no project dir, no transcript on disk, no cwd match) — the
// monitor treats unresolved as "skip this tick", never as DEAD.
func ResolveTranscript(ctx context.Context, snap SessionSnapshot) (TranscriptHandle, error) {
	if strings.Contains(strings.ToLower(snap.Comm), "codex") {
		return resolveCodex(ctx, snap)
	}
	// Any other comm — including a rewritten process title that names neither
	// CLI — is tried against the claude transcript store first, then codex.
	handle, err := resolveClaudeTranscript(snap)
	if err != nil {
		return TranscriptHandle{}, err
	}
	if handle.Resolved() {
		return handle, nil
	}
	return resolveCodex(ctx, snap)
}

// resolveCodex runs the codex resolution chain: the deterministic
// PID→open-write-rollout lookup, then the cwd-scan fallback.
func resolveCodex(ctx context.Context, snap SessionSnapshot) (TranscriptHandle, error) {
	// PRIMARY (deterministic): a live codex agent holds its own rollout-*.jsonl
	// open for writing, so the peer PID names the EXACT file via lsof — immune
	// to the cwd collision two same-directory agents cause.
	if path, ok := codexWriteRolloutForPID(ctx, snap.PID); ok {
		if meta, metaOK := transcripts.ReadCodexSessionMeta(path); metaOK {
			return TranscriptHandle{Path: path, HarnessSessionID: meta.Payload.ID, Format: FormatCodex}, nil
		}
	}
	// FALLBACK: no rollout held open yet (idle / pre-first-turn — nothing to
	// monitor) — scan by session_meta.cwd, newest-by-mtime.
	return resolveCodexTranscript(snap)
}

// resolveClaudeTranscript binds the session from the transcript store on disk:
// one readdir of ~/.claude/projects/<encoded-cwd>, newest session transcript by
// mtime, harness session id taken from the filename stem. Nothing about the peer
// PROCESS is consulted, so a rewritten process title cannot defeat it. An absent
// or empty project dir is unresolved (zero handle, nil error), never an error.
func resolveClaudeTranscript(snap SessionSnapshot) (TranscriptHandle, error) {
	home, err := homeDir()
	if err != nil {
		return TranscriptHandle{}, err
	}
	dir := filepath.Join(home, ".claude", "projects", transcripts.EncodeClaudeCwd(snap.Cwd))
	candidates, err := transcripts.ClaudeProjectSessions(dir)
	if err != nil {
		return TranscriptHandle{}, err
	}
	if len(candidates) == 0 {
		return TranscriptHandle{}, nil
	}

	// Newest by mtime wins: the live session is appended every turn. Path breaks
	// an mtime tie so the choice is deterministic.
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].ModTime.Equal(candidates[j].ModTime) {
			return candidates[i].ModTime.After(candidates[j].ModTime)
		}
		return candidates[i].Path < candidates[j].Path
	})
	chosen := candidates[0]

	if len(candidates) > 1 {
		// A project dir accumulates every past session for that cwd, so >1
		// candidate is the norm rather than an anomaly. Log all candidates and
		// the chosen one so a post-hoc diagnosis can see what recency picked.
		all := make([]string, 0, len(candidates))
		for _, c := range candidates {
			all = append(all, c.Path)
		}
		slog.Warn("hivemonitor: multiple claude transcripts under the project dir — choosing newest by mtime (the live session is appended each turn)",
			"cwd", snap.Cwd,
			"session", snap.ID,
			"candidates", all,
			"chosen", chosen.Path)
	}

	// The claude scheme names each session transcript <sessionId>.jsonl, so the
	// stem is the harness session id — the identity the uploader reads off these
	// same files.
	sid := strings.TrimSuffix(filepath.Base(chosen.Path), ".jsonl")
	return TranscriptHandle{Path: chosen.Path, HarnessSessionID: sid, Format: FormatClaude}, nil
}

// resolveCodexTranscript is the FALLBACK codex resolver (the primary is the
// deterministic PID→open-write-rollout lookup in ResolveTranscript). It scans
// ~/.codex/sessions for the rollout whose session_meta.cwd equals snap.Cwd,
// returning the newest match by mtime. It runs only when the agent holds no
// rollout open (idle / pre-first-turn), so >1 match is a tolerated best-effort
// guess, logged for diagnosis.
func resolveCodexTranscript(snap SessionSnapshot) (TranscriptHandle, error) {
	home, err := homeDir()
	if err != nil {
		return TranscriptHandle{}, err
	}
	root := filepath.Join(home, ".codex", "sessions")

	type candidate struct {
		path    string
		mtime   int64
		session string
	}
	var matches []candidate

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Skip unreadable subtrees rather than aborting the whole walk.
			//nolint:nilerr // a per-entry read error must not abort the scan.
			return nil
		}
		if d.IsDir() || !strings.HasPrefix(d.Name(), "rollout-") || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		meta, ok := transcripts.ReadCodexSessionMeta(path)
		if !ok || meta.Payload.Cwd != snap.Cwd {
			return nil
		}
		mt := int64(0)
		if info, statErr := d.Info(); statErr == nil {
			mt = info.ModTime().UnixNano()
		}
		matches = append(matches, candidate{path: path, mtime: mt, session: meta.Payload.ID})
		return nil
	})
	if walkErr != nil {
		// The sessions root not existing is "no codex transcripts" — unresolved,
		// not an error.
		if os.IsNotExist(walkErr) {
			return TranscriptHandle{}, nil
		}
		return TranscriptHandle{}, walkErr
	}
	if len(matches) == 0 {
		return TranscriptHandle{}, nil
	}
	// Newest by mtime wins (active session).
	sort.Slice(matches, func(i, j int) bool { return matches[i].mtime > matches[j].mtime })
	chosen := matches[0]

	if len(matches) > 1 {
		// Log BOTH candidate paths and the chosen one so a post-hoc diagnosis
		// has context. This fallback only runs when the agent holds no rollout
		// open; the deterministic path (PID → open write rollout) already
		// resolved the common active-agent case, so newest-by-mtime here is an
		// accepted best-effort guess for a not-yet-active session.
		all := make([]string, 0, len(matches))
		for _, m := range matches {
			all = append(all, m.path)
		}
		slog.Warn("hivemonitor: multiple codex rollouts match cwd — choosing newest (best-effort fallback; agent held no rollout open)",
			"cwd", snap.Cwd,
			"session", snap.ID,
			"candidates", all,
			"chosen", chosen.path)
	}
	return TranscriptHandle{Path: chosen.path, HarnessSessionID: chosen.session, Format: FormatCodex}, nil
}

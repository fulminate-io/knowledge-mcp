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
// process command name (comm — "claude" or "codex"). It is produced by the
// HTTPServer session-snapshot read seam (Phase 3) and consumed here.
type SessionSnapshot struct {
	ID   string
	Cwd  string
	PID  int
	Comm string
}

// TranscriptHandle is the resolved binding: the exact transcript file path, its
// format, and the HARNESS session-id (claude CLAUDE_CODE_SESSION_ID / codex
// rollout session_meta.id) — the OS/harness/file-sourced, LLM-uncontrolled
// identity the ban keys on and the value passed as HiveRequest.MemberSession on
// renew. A zero TranscriptHandle (empty Path) means "not resolved" — the monitor
// skips that claim this tick rather than treating it as DEAD.
type TranscriptHandle struct {
	Path string
	// HarnessSessionID is the harness-sourced session identity (NOT the
	// reconnect-volatile Mcp-Session-Id): claude's CLAUDE_CODE_SESSION_ID or
	// codex's rollout session_meta.id. Stable for the CLI session's life and
	// LLM-uncontrolled, so it is the ban key and the renew MemberSession.
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
// file. It branches on the peer process command:
//
//   - claude: read CLAUDE_CODE_SESSION_ID from the claiming process's
//     environment (set on EVERY claude launch, unlike --resume args) and join
//     ~/.claude/projects/<encoded-cwd>/<id>.jsonl where encoded-cwd is Cwd with
//     every '/' replaced by '-' (the documented claude project-dir scheme).
//   - codex: PRIMARY (deterministic) — the live agent holds its own
//     rollout-*.jsonl open for writing, so lsof on the peer PID names the EXACT
//     file, immune to the cwd collision two same-directory agents cause.
//     FALLBACK (no rollout held open yet) — scan ~/.codex/sessions, read ONLY
//     the first line of each rollout, decode session_meta, match payload.cwd ==
//     snapshot.Cwd, and among matches pick the newest by mtime.
//
// A zero (unresolved) handle is returned with a nil error when the binding
// cannot be made (env absent, file missing, no cwd match, unknown comm) — the
// monitor treats unresolved as "skip this tick", never as DEAD.
func ResolveTranscript(ctx context.Context, snap SessionSnapshot) (TranscriptHandle, error) {
	comm := strings.ToLower(snap.Comm)
	switch {
	case strings.Contains(comm, "claude"):
		return resolveClaudeTranscript(ctx, snap)
	case strings.Contains(comm, "codex"):
		// PRIMARY (deterministic): a live codex agent holds its own
		// rollout-*.jsonl open for writing, so the peer PID names the EXACT file
		// via lsof — immune to the cwd collision two same-directory agents cause.
		if path, ok := codexWriteRolloutForPID(ctx, snap.PID); ok {
			if meta, metaOK := transcripts.ReadCodexSessionMeta(path); metaOK {
				return TranscriptHandle{Path: path, HarnessSessionID: meta.Payload.ID, Format: FormatCodex}, nil
			}
		}
		// FALLBACK: no rollout held open yet (idle / pre-first-turn — nothing to
		// monitor) — scan by session_meta.cwd, newest-by-mtime.
		return resolveCodexTranscript(snap)
	default:
		return TranscriptHandle{}, nil
	}
}

// resolveClaudeTranscript reads CLAUDE_CODE_SESSION_ID from the peer process
// env and joins the deterministic project-dir path. Unresolved (zero handle) on
// absent env var or missing file.
func resolveClaudeTranscript(ctx context.Context, snap SessionSnapshot) (TranscriptHandle, error) {
	sid := ProcessEnvValue(ctx, snap.PID, "CLAUDE_CODE_SESSION_ID")
	if sid == "" {
		return TranscriptHandle{}, nil
	}
	home, err := homeDir()
	if err != nil {
		return TranscriptHandle{}, err
	}
	path := filepath.Join(home, ".claude", "projects", transcripts.EncodeClaudeCwd(snap.Cwd), sid+".jsonl")
	if _, statErr := os.Stat(path); statErr != nil {
		// File not present yet (or never) — unresolved, not an error.
		//nolint:nilerr // a missing transcript is "unresolved", not a read failure.
		return TranscriptHandle{}, nil
	}
	return TranscriptHandle{Path: path, HarnessSessionID: sid, Format: FormatClaude}, nil
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

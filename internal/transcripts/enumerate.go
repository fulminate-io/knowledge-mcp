// SPDX-License-Identifier: Apache-2.0

package transcripts

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Entry is one enumerated transcript file: its absolute path, the CLI that wrote
// it, and the size + mod time captured at walk. Size lets KN-2 diff a
// last-shipped byte offset against the current file length; this package does no
// offset persistence or upload itself (out of scope).
type Entry struct {
	Path    string
	Source  Source
	Size    int64
	ModTime time.Time
}

// Enumerate walks the WHOLE local transcript corpus across both CLI roots with
// NO cwd/PID filter, returning every transcript file it finds:
//
//   - ~/.claude/projects/**/*.jsonl  — every claude transcript, INCLUDING
//     <session>/subagents/agent-*.jsonl (a plain *.jsonl suffix match under the
//     tree captures subagents automatically).
//   - ~/.codex/sessions/**/rollout-*.jsonl — every codex rollout.
//
// cwd is NOT recovered from the encoded claude project-dir name (the '/'→'-'
// encoding is non-invertible); the parsers read the authoritative cwd from each
// record instead. A missing root (the user has only one CLI installed) yields no
// entries for that root and no error. Unreadable subtrees are skipped rather than
// aborting the walk. Traversal is I/O-bound, so a serial WalkDir per root is
// correct.
func Enumerate() ([]Entry, error) {
	home, err := homeDir()
	if err != nil {
		return nil, err
	}

	var entries []Entry

	claudeRoot := filepath.Join(home, ".claude", "projects")
	if err := walkRoot(claudeRoot, func(d fs.DirEntry) bool {
		return strings.HasSuffix(d.Name(), ".jsonl")
	}, SourceClaude, &entries); err != nil {
		return nil, err
	}

	codexRoot := filepath.Join(home, ".codex", "sessions")
	if err := walkRoot(codexRoot, func(d fs.DirEntry) bool {
		return strings.HasPrefix(d.Name(), "rollout-") && strings.HasSuffix(d.Name(), ".jsonl")
	}, SourceCodex, &entries); err != nil {
		return nil, err
	}

	return entries, nil
}

// ClaudeProjectSessions lists the SESSION transcripts sitting directly in one
// claude project directory (~/.claude/projects/<encoded-cwd>) — a single
// readdir, deliberately NOT recursive. The scheme names each session transcript
// <sessionId>.jsonl at the top level of the project dir, while per-session
// SUBDIRECTORIES hold subagent transcripts; a recursive walk (what Enumerate
// does, because the upload corpus wants subagents too) therefore surfaces files
// whose names are not session ids, and on a busy machine one of those is
// routinely the newest file in the tree. Callers binding a live session by
// recency need this narrower view.
//
// A missing directory yields no entries and no error, mirroring Enumerate's
// missing-root contract; a file that vanishes between dirent and stat is
// skipped rather than failing the listing.
func ClaudeProjectSessions(dir string) ([]Entry, error) {
	dirents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var entries []Entry
	for _, d := range dirents {
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			continue
		}
		info, statErr := d.Info()
		if statErr != nil {
			continue
		}
		entries = append(entries, Entry{
			Path:    filepath.Join(dir, d.Name()),
			Source:  SourceClaude,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}
	return entries, nil
}

// walkRoot walks one corpus root, appending an Entry for every non-dir file that
// satisfies match. A missing root is treated as "no transcripts" (nil error, no
// entries); per-entry read errors skip that subtree rather than aborting. The
// skeleton mirrors resolveCodexTranscript's proven walk shape.
func walkRoot(root string, match func(fs.DirEntry) bool, source Source, out *[]Entry) error {
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Skip unreadable subtrees rather than aborting the whole walk.
			//nolint:nilerr // a per-entry read error must not abort the scan.
			return nil
		}
		if d.IsDir() || !match(d) {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			// File vanished between dirent and stat — skip it.
			//nolint:nilerr // a transient stat failure must not abort the scan.
			return nil
		}
		*out = append(*out, Entry{
			Path:    path,
			Source:  source,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
		return nil
	})
	if walkErr != nil {
		// A missing root means the user has only the other CLI installed —
		// "no transcripts", not an error.
		if os.IsNotExist(walkErr) {
			return nil
		}
		return walkErr
	}
	return nil
}

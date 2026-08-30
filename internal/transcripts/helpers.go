// SPDX-License-Identifier: Apache-2.0

// Package transcripts enumerates the whole local CLI transcript corpus
// (~/.claude/projects and ~/.codex/sessions) and column-extracts both JSONL
// formats into a single normalized Row DTO carrying raw token counts and the
// model id. It is the read side of the transcript knowledge base; it computes
// no cost and applies no cwd filter.
package transcripts

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"time"
)

// homeDir resolves the user's home directory. It is a package-local, overridable
// test seam so Enumerate can run against a temp HOME without mutating process
// env. A per-package test seam is a trivial os.UserHomeDir alias, not a shared
// contract that could drift.
//
//nolint:gochecknoglobals // overridable home seam for testability; mirrors the exec-seam idiom.
var homeDir = os.UserHomeDir

// CodexSessionMeta is the codex rollout's first-line self-declaration:
// {"type":"session_meta","payload":{"id":...,"cwd":...}}. Only the fields the
// resolver matches on are decoded.
type CodexSessionMeta struct {
	Type    string `json:"type"`
	Payload struct {
		ID  string `json:"id"`
		Cwd string `json:"cwd"`
	} `json:"payload"`
}

// newJSONLScanner returns a bufio.Scanner with the 8MiB line-buffer cap used
// across the transcript readers, DRYing the tolerant-scan idiom. The default
// 64KiB cap would truncate a full assistant turn; 8MiB is the upper bound any
// single JSONL line is expected to reach.
func newJSONLScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	return sc
}

// parseRecordTS parses an RFC3339 transcript timestamp (both CLIs emit this
// shape), tolerating absence/garbage by returning the zero time.
func parseRecordTS(ts string) time.Time {
	if ts == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return time.Time{}
	}
	return t
}

// ReadCodexSessionMeta reads ONLY the first line of a codex rollout and decodes
// its session_meta self-declaration. Returns (meta, false) on any read/decode
// failure or when the first line is not a session_meta record.
func ReadCodexSessionMeta(path string) (CodexSessionMeta, bool) {
	f, err := os.Open(path) //nolint:gosec // path comes from a WalkDir under ~/.codex/sessions, not user text.
	if err != nil {
		return CodexSessionMeta{}, false
	}
	defer f.Close()

	sc := newJSONLScanner(f)
	if !sc.Scan() {
		return CodexSessionMeta{}, false
	}
	var meta CodexSessionMeta
	if err := json.Unmarshal(sc.Bytes(), &meta); err != nil {
		return CodexSessionMeta{}, false
	}
	if meta.Type != "session_meta" {
		return CodexSessionMeta{}, false
	}
	return meta, true
}

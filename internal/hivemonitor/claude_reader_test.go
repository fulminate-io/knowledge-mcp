// SPDX-License-Identifier: Apache-2.0

package hivemonitor

import (
	"os"
	"path/filepath"
	"testing"
)

// claude transcript record builders — minimal valid lines matching the real
// on-disk shape (verified against ~/.claude/projects/*.jsonl).
func claudeAssistantToolUse(id, name string) string {
	return `{"type":"assistant","message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"` + id + `","name":"` + name + `"}]}}`
}

func claudeUserToolResult(id string) string {
	return `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `"}]}}`
}

func claudeAssistantText() string {
	return `{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"done"}]}}`
}

// non-conversation records that interleave in real transcripts (T3-2).
func claudeFileHistorySnapshot() string {
	return `{"type":"file-history-snapshot","snapshot":{"messageId":"x"}}`
}

func claudeAttachment() string {
	return `{"type":"attachment","attachment":{"type":"file"}}`
}

func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	var body string
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestClaudeClassify_BlockedOnToolLinchpin is THE LINCHPIN: a synthetic claude
// transcript whose final CONVERSATION line is an assistant tool_use with NO
// subsequent matching tool_result classifies as BLOCKED_ON_TOOL (working), NOT
// IDLE — including the T3-2 case where a trailing NON-conversation record
// (file-history-snapshot / attachment) sits AFTER the unmatched tool_use and
// must NOT reset the in-flight-tool state to IDLE.
func TestClaudeClassify_BlockedOnToolLinchpin(t *testing.T) {
	r := NewClaudeReader()

	// Bare case: tail is the unmatched tool_use.
	path := writeTranscript(t,
		claudeUserToolResult("toolu_prev"), // a prior, fully-resolved turn
		claudeAssistantText(),
		claudeAssistantToolUse("toolu_inflight", "Bash"),
	)
	state, _, err := r.Classify(path, 0)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if state != StateBlockedOnTool {
		t.Fatalf("bare unmatched tool_use tail classified %s, want BLOCKED_ON_TOOL", state)
	}

	// T3-2 STRENGTHENED: a trailing NON-conversation record after the unmatched
	// tool_use must STILL classify BLOCKED_ON_TOOL (the literal last line is a
	// file-history-snapshot, which must be skipped, not treated as the tail).
	path2 := writeTranscript(t,
		claudeAssistantToolUse("toolu_inflight", "Bash"),
		claudeFileHistorySnapshot(),
		claudeAttachment(),
	)
	state2, _, err := r.Classify(path2, 0)
	if err != nil {
		t.Fatalf("Classify (trailing non-conv): %v", err)
	}
	if state2 != StateBlockedOnTool {
		t.Fatalf("unmatched tool_use + trailing non-conversation record classified %s, want BLOCKED_ON_TOOL (must NOT reset to IDLE)", state2)
	}
}

// TestClaudeClassify_IdleExecutingDead covers the remaining states: a tail of
// tool_use → matching tool_result → assistant end_turn classifies IDLE; fresh
// appended non-tool lines classify EXECUTING; a missing file classifies DEAD.
func TestClaudeClassify_IdleExecutingDead(t *testing.T) {
	r := NewClaudeReader()

	// IDLE: tool_use, its matching tool_result, then a pure-text end_turn.
	idlePath := writeTranscript(t,
		claudeAssistantToolUse("toolu_1", "Bash"),
		claudeUserToolResult("toolu_1"),
		claudeAssistantText(),
	)
	// Classify with prevOffset == file size so there are no "new" bytes — a
	// settled, completed turn.
	size := fileSize(t, idlePath)
	if state, _, err := r.Classify(idlePath, size); err != nil || state != StateIdle {
		t.Fatalf("settled completed turn classified %s (err=%v), want IDLE", state, err)
	}

	// EXECUTING: fresh appended non-tool conversation lines since prevOffset,
	// tail not an unmatched tool_use.
	if state, _, err := r.Classify(idlePath, 0); err != nil || state != StateExecuting {
		t.Fatalf("fresh activity with resolved tail classified %s (err=%v), want EXECUTING", state, err)
	}

	// DEAD: missing file.
	if state, _, err := r.Classify(filepath.Join(t.TempDir(), "gone.jsonl"), 0); err != nil || state != StateDead {
		t.Fatalf("missing file classified %s (err=%v), want DEAD", state, err)
	}
}

// TestClaudeClassify_OffsetFollow asserts the returned newOffset is the file
// size so the next tick reads only appended bytes, and that a follow-up classify
// at that offset with no new bytes is IDLE.
func TestClaudeClassify_OffsetFollow(t *testing.T) {
	r := NewClaudeReader()
	path := writeTranscript(t,
		claudeAssistantToolUse("toolu_1", "Bash"),
		claudeUserToolResult("toolu_1"),
		claudeAssistantText(),
	)
	_, off, err := r.Classify(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if off != fileSize(t, path) {
		t.Fatalf("newOffset = %d, want file size %d", off, fileSize(t, path))
	}
	if state, _, _ := r.Classify(path, off); state != StateIdle {
		t.Fatalf("no new bytes since offset classified %s, want IDLE", state)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

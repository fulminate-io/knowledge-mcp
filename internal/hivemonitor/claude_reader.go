// SPDX-License-Identifier: Apache-2.0

package hivemonitor

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
)

// claudeReader classifies a claude transcript (~/.claude/projects/<encoded-cwd>/
// <id>.jsonl) by following it from an offset. It satisfies TranscriptReader.
type claudeReader struct{}

// NewClaudeReader returns a claude transcript reader.
func NewClaudeReader() TranscriptReader { return claudeReader{} }

// claudeRecord is the top-level claude transcript line. Only the fields the
// classifier reads are decoded. The conversational records are type ∈
// {user, assistant, system}; real transcripts ALSO interleave non-conversation
// records (file-history-snapshot, attachment, last-prompt, mode, ai-title,
// queue-operation, ...) which the classifier SKIPS when finding the tail
// conversation record — the literal last line is frequently one of those and
// must NOT reset the in-flight-tool state.
type claudeRecord struct {
	Type    string         `json:"type"`
	Message *claudeMessage `json:"message"`
}

// claudeMessage is the nested message object on a conversational record. Only
// Content is read — the tail's liveness is derived structurally from the
// tool_use/tool_result blocks, not from stop_reason (a tool_use turn with no
// matching tool_result is the BLOCKED signal regardless of the field).
type claudeMessage struct {
	Content []claudeBlock `json:"content"`
}

// claudeBlock is one content block. tool_use carries an id; tool_result carries
// the tool_use_id it answers.
//
// Text and Name are Classify-INERT decode-only fields: Classify reasons purely
// over Type/ID/ToolUseID (see applyClaudeRecord) and never reads them. Text
// carries an assistant/user text block's content; Name carries a tool_use's
// tool name. FormatTranscript (transcript_text.go) renders them into the LLM
// supervisor's transcript view.
type claudeBlock struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	ToolUseID string `json:"tool_use_id"`
	Text      string `json:"text"`
	Name      string `json:"name"`
}

// isConversation reports whether a record type is one of the three
// conversational kinds the classifier reasons over.
func isConversation(t string) bool {
	return t == "user" || t == "assistant" || t == "system"
}

// Classify reads the claude transcript from prevOffset and returns the liveness
// state plus the new end-of-file offset.
//
//   - File missing → DEAD (the process is gone; not a read error).
//   - Tail conversation record is an assistant turn whose tool_use has no later
//     matching tool_result → BLOCKED_ON_TOOL (still working). Non-conversation
//     records AFTER the tool_use (file-history-snapshot/attachment/...) do NOT
//     reset this — they are skipped.
//   - New bytes appended since prevOffset and the tail is not an unmatched
//     tool_use → EXECUTING.
//   - No new bytes since prevOffset, or the tail is a completed turn with no
//     in-flight tool → IDLE.
func (claudeReader) Classify(path string, prevOffset int64) (LivenessState, int64, error) {
	f, err := os.Open(path) //nolint:gosec // path is a resolved transcript path, not user text.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return StateDead, 0, nil
		}
		return StateUnknown, prevOffset, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return StateUnknown, prevOffset, err
	}
	size := info.Size()
	// A truncated/rotated file (smaller than prevOffset) resets the follow.
	if size < prevOffset {
		prevOffset = 0
	}
	hadNew := size > prevOffset

	// Scan the WHOLE file from the start: the tool_use the tail is blocked on
	// may precede prevOffset, so a warm-tick appended slice alone can't decide
	// blocked-on-tool. prevOffset drives only hadNew (was there fresh activity
	// since the last tick); `size` is the new offset returned. delivered
	// collects every tool_result's tool_use_id; lastToolUseIDs holds the
	// tool_use ids issued by the LAST assistant turn that issued any (the tail's
	// in-flight tools). The tail is blocked iff any of lastToolUseIDs was never
	// delivered.
	delivered := map[string]bool{}
	var (
		sawConversation bool
		lastToolUseIDs  []string
	)

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 || line[0] != '{' {
			continue
		}
		var rec claudeRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		// Skip non-conversation records entirely — they neither advance nor
		// reset the in-flight-tool reasoning (T3-2 linchpin: a trailing
		// file-history-snapshot/attachment must NOT reset a blocked tail).
		if !isConversation(rec.Type) {
			continue
		}
		sawConversation = true
		applyClaudeRecord(&rec, delivered, &lastToolUseIDs)
	}
	if err := sc.Err(); err != nil {
		return StateUnknown, prevOffset, err
	}

	switch {
	case tailIsBlocked(lastToolUseIDs, delivered):
		// The last assistant turn issued tool_use(s), at least one of which has
		// no matching tool_result — the worker is waiting on a tool. STILL
		// WORKING, not IDLE.
		return StateBlockedOnTool, size, nil
	case hadNew && sawConversation:
		// Fresh conversation activity since the last tick, tail not blocked.
		return StateExecuting, size, nil
	default:
		// Completed turn / no new activity.
		return StateIdle, size, nil
	}
}

// applyClaudeRecord folds one conversational record into the running state: a
// user tool_result records its tool_use_id as delivered; an assistant turn that
// issues tool_use(s) REPLACES lastToolUseIDs with its ids (the new tail's
// in-flight tools), while an assistant turn that issues NONE (a pure-text
// end_turn) CLEARS lastToolUseIDs — the tail is no longer an unmatched tool_use.
func applyClaudeRecord(rec *claudeRecord, delivered map[string]bool, lastToolUseIDs *[]string) {
	if rec.Message == nil {
		return
	}
	switch rec.Type {
	case "assistant":
		var ids []string
		for _, b := range rec.Message.Content {
			if b.Type == "tool_use" && b.ID != "" {
				ids = append(ids, b.ID)
			}
		}
		// A tool_use-issuing assistant turn sets the tail's in-flight ids; a
		// pure-text assistant turn clears them (ids is nil).
		*lastToolUseIDs = ids
	case "user":
		for _, b := range rec.Message.Content {
			if b.Type == "tool_result" && b.ToolUseID != "" {
				delivered[b.ToolUseID] = true
			}
		}
	case "system":
		// A system record alone is not an in-flight tool, but it also does not
		// answer one — leave lastToolUseIDs unchanged so a system note between a
		// tool_use and its result does not hide the block.
	}
}

// tailIsBlocked reports whether the tail's last tool-issuing assistant turn has
// any tool_use id with no matching tool_result delivered.
func tailIsBlocked(lastToolUseIDs []string, delivered map[string]bool) bool {
	for _, id := range lastToolUseIDs {
		if !delivered[id] {
			return true
		}
	}
	return false
}

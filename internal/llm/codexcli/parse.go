// SPDX-License-Identifier: Apache-2.0

package codexcli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// codexEvent is the discriminated-union JSON event codex emits one per
// stdout line under --json. Only the fields the parser cares about are
// populated; the union is left intentionally loose because codex evolves
// the surface frequently and we don't want a strict schema to break the
// parser when codex adds a new event type.
type codexEvent struct {
	Type    string          `json:"type"`
	Message string          `json:"message,omitempty"`
	Item    *codexItem      `json:"item,omitempty"`
	Usage   *codexUsage     `json:"usage,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

// codexItem is the inner payload for item.* events. ItemType discriminates
// which kind of item it is (agent_message, reasoning, tool_call, etc.) and
// Text holds the rendered content for message-shaped items.
//
// Codex versions vary on whether the assistant text lives under
// "agent_message" or "assistant_message"; we accept either to insulate the
// parser from minor wire-format drift.
type codexItem struct {
	ItemType string `json:"item_type,omitempty"`
	Type     string `json:"type,omitempty"`
	Text     string `json:"text,omitempty"`
	Content  string `json:"content,omitempty"`
}

// codexUsage mirrors the usage block on turn.completed events. Codex emits
// a JSON object with input/output token counts; cached/reasoning sub-counts
// (when present) fold into the substrate's TokenUsage InputTokens to match
// what every other knowledge consumer expects.
type codexUsage struct {
	InputTokens           int `json:"input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens,omitempty"`
	ReasoningOutputTokens int `json:"reasoning_output_tokens,omitempty"`
}

// codexTurnFailedError mirrors the inner error block on turn.failed events.
// Used by parseTurnFailedError to lift codex's diagnostic message into the
// LLMError surface without losing it to a generic "turn failed" string.
type codexTurnFailedError struct {
	Message string `json:"message"`
}

// parseResponse parses codex's JSONL event stream. Returns a populated
// *llm.Response on success, or an *llm.LLMError when codex reported a
// failed turn or the stream is malformed.
//
// Surfaced fields:
//   - Content      — concatenated text from agent_message / assistant_message item.completed events
//   - Usage        — from the turn.completed event's usage block
//   - FinishReason — FinishReasonEndTurn on turn.completed, FinishReasonOther
//     when the stream ends without one (codex error path)
//   - Model        — passed through from the request
//   - Provider     — ProviderCodexCLI
//   - Raw          — entire stdout, verbatim
//
// Fields NOT surfaced:
//   - ToolCalls — codex doesn't emit per-call tool dispatches in a shape
//     the substrate's eino schema.ToolCall fits; tool use is internal to
//     codex's MCP integration. Documented in package doc.
//   - ThinkingContent — codex doesn't expose Anthropic-style thinking
//     blocks. Documented as not surfaced.
//   - ReasoningContent — codex's --json stream emits "reasoning" items
//     with summaries internally, but the wire format and presence vary by
//     model and version. We deliberately don't pluck them in v1; revisit
//     when codex stabilizes the shape. Documented as not surfaced.
func parseResponse(stdout []byte, model llm.Model) (*llm.Response, error) {
	if len(stdout) == 0 {
		return nil, &llm.LLMError{
			Transient: false,
			Reason:    "parse_response",
			Cause:     fmt.Errorf("codex emitted empty stdout"),
		}
	}

	state, err := scanCodexEvents(stdout)
	if err != nil {
		return nil, err
	}

	if state.turnFailed != "" {
		return nil, &llm.LLMError{
			Transient: false,
			Reason:    "turn_failed",
			Cause:     fmt.Errorf("codex turn failed: %s", state.turnFailed),
		}
	}
	if !state.sawComplete && state.content.Len() == 0 {
		return nil, &llm.LLMError{
			Transient: false,
			Reason:    "parse_response",
			Cause:     fmt.Errorf("codex stdout had no turn.completed and no agent message"),
		}
	}

	return &llm.Response{
		Content:      state.content.String(),
		FinishReason: state.finish,
		Usage:        state.usage,
		Model:        model,
		Provider:     llm.ProviderCodexCLI,
		Raw:          json.RawMessage(stdout),
	}, nil
}

// parseState collects the running result of scanCodexEvents. Pulled out so
// parseResponse can stay under the funlen budget without losing the
// per-event accumulator.
type parseState struct {
	content     strings.Builder
	usage       llm.TokenUsage
	finish      llm.FinishReason
	turnFailed  string
	sawComplete bool
}

// scanCodexEvents walks stdout line-by-line, decoding each JSON event and
// accumulating into a parseState. Bufio's scanner buffer is bumped so
// long item.completed lines (full assistant messages) aren't truncated.
//
// Default cap is 64KiB; 8 MiB matches the upper bound we expect for any
// single assistant turn (well above codex's documented per-turn token
// budget × ~5 bytes/tok). See feedback_no_truncation_for_llm.
func scanCodexEvents(stdout []byte) (*parseState, error) {
	state := &parseState{finish: llm.FinishReasonOther}

	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 || line[0] != '{' {
			// Skip blank lines and non-JSON banner lines (codex prepends
			// "Reading prompt from stdin..." to stdout when invoked with
			// `-`). Banners are informational, not events.
			continue
		}
		var ev codexEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			// Malformed JSON is unusual; skip it and keep parsing. The
			// terminating turn.completed / turn.failed event is what
			// determines the final outcome.
			continue
		}
		applyEvent(state, &ev)
	}
	if err := scanner.Err(); err != nil {
		return nil, &llm.LLMError{
			Transient: false,
			Reason:    "parse_response",
			Cause:     fmt.Errorf("scan codex stdout: %w", err),
		}
	}
	return state, nil
}

// applyEvent folds a single decoded codex event into the parseState. Kept
// separate from scanCodexEvents so the dispatch table reads cleanly and
// new event types are easy to add without growing the scanner loop.
func applyEvent(state *parseState, ev *codexEvent) {
	switch ev.Type {
	case "item.completed":
		if ev.Item == nil || !isAgentMessage(ev.Item) {
			return
		}
		text := agentMessageText(ev.Item)
		if text == "" {
			return
		}
		if state.content.Len() > 0 {
			state.content.WriteString("\n")
		}
		state.content.WriteString(text)
	case "turn.completed":
		state.sawComplete = true
		state.finish = llm.FinishReasonEndTurn
		if ev.Usage != nil {
			state.usage = llm.TokenUsage{
				InputTokens:  ev.Usage.InputTokens + ev.Usage.CachedInputTokens,
				OutputTokens: ev.Usage.OutputTokens + ev.Usage.ReasoningOutputTokens,
			}
		}
	case "turn.failed":
		state.turnFailed = parseTurnFailedError(ev.Error)
	}
}

// isAgentMessage reports whether the item event carries an assistant
// message body. Codex versions split between "agent_message" and
// "assistant_message" so the parser checks both.
func isAgentMessage(item *codexItem) bool {
	switch item.ItemType {
	case "agent_message", "assistant_message":
		return true
	}
	switch item.Type {
	case "agent_message", "assistant_message":
		return true
	}
	return false
}

// agentMessageText returns the rendered text from a message item. Codex
// uses Text on most events; we also accept Content as a fallback.
func agentMessageText(item *codexItem) string {
	if item.Text != "" {
		return item.Text
	}
	return item.Content
}

// parseTurnFailedError lifts codex's nested error.message field. Returns a
// human-readable message; falls back to the raw JSON when the inner shape
// doesn't match the expected envelope so operators still see something.
func parseTurnFailedError(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "(no error detail)"
	}
	var e codexTurnFailedError
	if err := json.Unmarshal(raw, &e); err == nil && e.Message != "" {
		return e.Message
	}
	return string(raw)
}

// extractCodexError mines codex's --json STDOUT for the diagnostic it emits on
// a failed turn. Codex writes the real cause (rate/usage limit, model
// unavailable, auth) as a `type:"error"` or `turn.failed` event on STDOUT and
// exits NON-ZERO without writing anything to stderr — so runCLI's non-zero-exit
// path (which short-circuits before parseResponse ever sees stdout) must call
// this to surface the cause instead of a bare "codex exited 1:". Returns the
// last error message found (single-lined), or "" when stdout carried none.
func extractCodexError(stdout []byte) string {
	if len(stdout) == 0 {
		return ""
	}
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var msg string
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 || line[0] != '{' {
			continue
		}
		var ev codexEvent
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		switch ev.Type {
		case "error":
			if ev.Message != "" {
				msg = ev.Message
			}
		case "turn.failed":
			if m := parseTurnFailedError(ev.Error); m != "" && m != "(no error detail)" {
				msg = m
			}
		}
	}
	return trimToLine(msg)
}

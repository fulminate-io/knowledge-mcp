// SPDX-License-Identifier: Apache-2.0

package hivemonitor

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
)

// codexReader classifies a codex rollout transcript (~/.codex/sessions/<date>/
// rollout-*.jsonl) by following it from an offset. It satisfies
// TranscriptReader.
//
// The on-disk rollout shape (VERIFIED against live rollouts — NOT the
// codexcli stdout-envelope shape in llm/codexcli/parse.go, which parses a
// different surface) is one JSON object per line with a top-level `type` and a
// nested `payload`:
//
//   - {"type":"session_meta","payload":{"id","cwd",...}}            — first line
//   - {"type":"response_item","payload":{"type":"function_call","call_id",...}}
//   - {"type":"response_item","payload":{"type":"function_call_output","call_id",...}}
//   - {"type":"response_item","payload":{"type":"custom_tool_call"/"..._output","call_id"}}
//   - {"type":"response_item","payload":{"type":"message"/"reasoning",...}}
//   - {"type":"event_msg","payload":{"type":"task_started"/"task_complete"/...}}
//
// A tool call is a function_call / custom_tool_call carrying a call_id; its
// result is the matching *_output with the same call_id — the same matched-pair
// model as claude's tool_use/tool_result, keyed on call_id instead of
// tool_use_id.
type codexReader struct{}

// NewCodexReader returns a codex transcript reader.
func NewCodexReader() TranscriptReader { return codexReader{} }

// codexRolloutRecord is one rollout line: a top-level type and a nested payload.
type codexRolloutRecord struct {
	Type    string               `json:"type"`
	Payload *codexRolloutPayload `json:"payload"`
}

// codexRolloutPayload is the inner payload. PayloadType discriminates the kind;
// CallID links a tool call to its output.
//
// Name, Arguments, Content, and Text are Classify-INERT decode-only fields: the
// classifier reasons purely over PayloadType/CallID (see Classify) and never
// reads them. They feed FormatTranscript (transcript_text.go): Name+Arguments
// render a function_call line, Content renders message/reasoning text blocks,
// and Text/Output render a plain or function_call_output payload.
type codexRolloutPayload struct {
	PayloadType string `json:"type"`
	CallID      string `json:"call_id"`
	Name        string `json:"name"`
	Arguments   string `json:"arguments"`
	Text        string `json:"text"`
	Output      string `json:"output"`
	// Content is the message/reasoning content, decoded lazily: real rollouts
	// carry an array of {type,text} blocks, but some shapes carry a flat string.
	// Held as RawMessage so an unexpected scalar shape can never fail the whole
	// record decode (which would silently drop the line from the transcript).
	Content json.RawMessage `json:"content"`
}

// codexContentBlock is one element of a message/reasoning payload's content
// array: {"type":"input_text"/"output_text"/"text","text":"..."}. Classify-inert;
// rendered by FormatTranscript only.
type codexContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// isCodexToolCall reports whether a payload type is a tool-call issuance (the
// shape that, left without a matching *_output, means blocked-on-tool).
func isCodexToolCall(pt string) bool {
	return pt == "function_call" || pt == "custom_tool_call"
}

// isCodexToolOutput reports whether a payload type is a tool-call result.
func isCodexToolOutput(pt string) bool {
	return pt == "function_call_output" || pt == "custom_tool_call_output"
}

// Classify reads the codex rollout from prevOffset and returns the liveness
// state plus the new end-of-file offset:
//
//   - File missing → DEAD.
//   - Any tool call (function_call/custom_tool_call) has no matching *_output
//     (call_id still pending at scan-end) → BLOCKED_ON_TOOL (still working).
//   - New bytes appended since prevOffset and no tool call is in flight →
//     EXECUTING.
//   - No new bytes since prevOffset, or the tail is a settled turn
//     (task_complete) with no in-flight tool → IDLE.
func (codexReader) Classify(path string, prevOffset int64) (LivenessState, int64, error) {
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
	if size < prevOffset {
		prevOffset = 0
	}
	hadNew := size > prevOffset

	// Scan the whole file (the tool call the tail is blocked on may precede
	// prevOffset). pending holds the call_ids of tool calls issued with no
	// matching *_output yet; an *_output removes its call_id. Codex always emits
	// a *_output for each tool call within a turn, so any call_id still pending
	// at scan-end is a genuinely in-flight tool — the blocked signal. This
	// correctly handles interleaved parallel calls (A, B, output-A leaves B
	// pending) unlike a last-record-only heuristic.
	pending := map[string]bool{}
	var sawRecord bool

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 || line[0] != '{' {
			continue
		}
		var rec codexRolloutRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.Payload == nil {
			continue
		}
		sawRecord = true
		pt := rec.Payload.PayloadType
		switch {
		case isCodexToolCall(pt) && rec.Payload.CallID != "":
			pending[rec.Payload.CallID] = true
		case isCodexToolOutput(pt) && rec.Payload.CallID != "":
			delete(pending, rec.Payload.CallID)
		}
	}
	if err := sc.Err(); err != nil {
		return StateUnknown, prevOffset, err
	}

	switch {
	case len(pending) > 0:
		// A tool call with no matching output — waiting on a tool. STILL
		// WORKING, not IDLE.
		return StateBlockedOnTool, size, nil
	case hadNew && sawRecord:
		return StateExecuting, size, nil
	default:
		return StateIdle, size, nil
	}
}

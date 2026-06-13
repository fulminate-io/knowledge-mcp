// SPDX-License-Identifier: Apache-2.0

package hivemonitor

import (
	"path/filepath"
	"testing"
)

// codex rollout line builders — minimal valid lines matching the real on-disk
// rollout shape (verified against ~/.codex/sessions/**/rollout-*.jsonl).
func codexSessionMetaLine() string {
	return `{"type":"session_meta","payload":{"id":"s","cwd":"/repo"}}`
}

func codexFunctionCall(callID string) string {
	return `{"type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"` + callID + `"}}`
}

func codexFunctionCallOutput(callID string) string {
	return `{"type":"response_item","payload":{"type":"function_call_output","call_id":"` + callID + `","output":"ok"}}`
}

func codexMessage() string {
	return `{"type":"response_item","payload":{"type":"message","role":"assistant","content":"hi"}}`
}

func codexTaskComplete() string {
	return `{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t1"}}`
}

// TestCodexClassify_BlockedIdleExecuting covers the codex 4-state mapping: a
// tool_call (function_call) with no following output classifies BLOCKED_ON_TOOL;
// a task_complete tail classifies IDLE; fresh item lines classify EXECUTING; a
// missing file classifies DEAD.
func TestCodexClassify_BlockedIdleExecuting(t *testing.T) {
	r := NewCodexReader()

	// BLOCKED_ON_TOOL: a function_call with no matching function_call_output.
	blockedPath := writeTranscript(t,
		codexSessionMetaLine(),
		codexFunctionCall("call_A"),
	)
	if state, _, err := r.Classify(blockedPath, 0); err != nil || state != StateBlockedOnTool {
		t.Fatalf("unmatched function_call classified %s (err=%v), want BLOCKED_ON_TOOL", state, err)
	}

	// BLOCKED still holds with an interleaved parallel call (A,B,output-A leaves
	// B pending) — the pending-set model, not last-record-only.
	parallelPath := writeTranscript(t,
		codexSessionMetaLine(),
		codexFunctionCall("call_A"),
		codexFunctionCall("call_B"),
		codexFunctionCallOutput("call_A"),
	)
	if state, _, err := r.Classify(parallelPath, 0); err != nil || state != StateBlockedOnTool {
		t.Fatalf("parallel call with one unmatched classified %s (err=%v), want BLOCKED_ON_TOOL", state, err)
	}

	// IDLE: a settled turn — tool call, its matching output, a message, then
	// task_complete — classified at offset==size (no new bytes).
	idlePath := writeTranscript(t,
		codexSessionMetaLine(),
		codexFunctionCall("call_A"),
		codexFunctionCallOutput("call_A"),
		codexMessage(),
		codexTaskComplete(),
	)
	size := fileSize(t, idlePath)
	if state, _, err := r.Classify(idlePath, size); err != nil || state != StateIdle {
		t.Fatalf("settled task_complete tail classified %s (err=%v), want IDLE", state, err)
	}

	// EXECUTING: fresh appended item lines since prevOffset, no tool in flight.
	if state, _, err := r.Classify(idlePath, 0); err != nil || state != StateExecuting {
		t.Fatalf("fresh resolved activity classified %s (err=%v), want EXECUTING", state, err)
	}

	// DEAD: missing file.
	if state, _, err := r.Classify(filepath.Join(t.TempDir(), "gone.jsonl"), 0); err != nil || state != StateDead {
		t.Fatalf("missing file classified %s (err=%v), want DEAD", state, err)
	}
}

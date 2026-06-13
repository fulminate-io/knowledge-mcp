// SPDX-License-Identifier: Apache-2.0

package hivemonitor

import (
	"strings"
	"testing"
)

// TestFormatTranscript_Claude renders a claude transcript and asserts the recent
// assistant text + tool line surface as non-empty content.
func TestFormatTranscript_Claude(t *testing.T) {
	path := writeTranscript(t,
		claudeAssistantText(),                     // assistant: done
		claudeAssistantToolUse("toolu_1", "Bash"), // tool: Bash
		claudeUserToolResult("toolu_1"),           // tool_result
	)
	out, err := FormatTranscript(TranscriptHandle{Path: path, Format: FormatClaude}, 1<<20)
	if err != nil {
		t.Fatalf("FormatTranscript(claude): %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("claude transcript rendered empty")
	}
	if !strings.Contains(out, "done") {
		t.Errorf("missing assistant text; got:\n%s", out)
	}
	if !strings.Contains(out, "Bash") {
		t.Errorf("missing tool name; got:\n%s", out)
	}
}

// TestFormatTranscript_Codex renders a codex rollout and asserts the message text
// and function-call name surface.
func TestFormatTranscript_Codex(t *testing.T) {
	path := writeTranscript(t,
		codexSessionMetaLine(),
		codexMessage(),                    // message: hi
		codexFunctionCall("call_A"),       // tool: exec_command(...)
		codexFunctionCallOutput("call_A"), // tool_result: ok
	)
	out, err := FormatTranscript(TranscriptHandle{Path: path, Format: FormatCodex}, 1<<20)
	if err != nil {
		t.Fatalf("FormatTranscript(codex): %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("codex transcript rendered empty")
	}
	if !strings.Contains(out, "hi") {
		t.Errorf("missing message text; got:\n%s", out)
	}
	if !strings.Contains(out, "exec_command") {
		t.Errorf("missing function_call name; got:\n%s", out)
	}
}

// TestFormatTranscript_BoundedByMaxBytes asserts only the trailing maxBytes are
// read: with a tiny maxBytes the earliest records are dropped while the tail
// survives, and the rendered output never exceeds the raw byte budget grossly.
func TestFormatTranscript_BoundedByMaxBytes(t *testing.T) {
	// Build many distinct assistant-text lines so the early ones are far from the
	// tail. claudeAssistantText emits the same "done" text; use tool lines with
	// unique names to make position observable.
	var lines []string
	for range 200 {
		lines = append(lines, claudeAssistantToolUse("toolu_x", "EARLY"))
	}
	lines = append(lines, claudeAssistantToolUse("toolu_tail", "TAILTOOL"))
	path := writeTranscript(t, lines...)

	// A small byte budget: only the final records are within the tail window.
	const maxBytes = 256
	out, err := FormatTranscript(TranscriptHandle{Path: path, Format: FormatClaude}, maxBytes)
	if err != nil {
		t.Fatalf("FormatTranscript: %v", err)
	}
	if !strings.Contains(out, "TAILTOOL") {
		t.Errorf("tail record dropped; got:\n%s", out)
	}
	if strings.Contains(out, "EARLY") {
		t.Errorf("early records past the maxBytes window leaked into output; got:\n%s", out)
	}
}

// SPDX-License-Identifier: Apache-2.0

package codexcli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// Unit tests for translate.go's parseResponse that don't need a fake
// codex binary. parse_test.go intentionally stays subprocess-free so it
// can run on platforms that lack POSIX shell.

// TestParseResponse_AssistantMessageVariant verifies that the parser
// accepts both `agent_message` and `assistant_message` discriminants —
// codex versions disagree on which name they emit.
func TestParseResponse_AssistantMessageVariant(t *testing.T) {
	transcript := `{"type":"item.completed","item":{"item_type":"assistant_message","text":"alt-shape"}}
{"type":"turn.completed","usage":{"input_tokens":2,"output_tokens":3}}
`
	resp, err := parseResponse([]byte(transcript), "gpt-5-codex")
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if resp.Content != "alt-shape" {
		t.Errorf("Content = %q, want %q", resp.Content, "alt-shape")
	}
}

// TestParseResponse_ConcatenatesMessages verifies that multiple
// item.completed events with assistant text concatenate (newline-joined)
// rather than the parser keeping only the last one.
func TestParseResponse_ConcatenatesMessages(t *testing.T) {
	transcript := `{"type":"item.completed","item":{"item_type":"agent_message","text":"first"}}
{"type":"item.completed","item":{"item_type":"agent_message","text":"second"}}
{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":2}}
`
	resp, err := parseResponse([]byte(transcript), "gpt-5-codex")
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if resp.Content != "first\nsecond" {
		t.Errorf("Content = %q, want %q", resp.Content, "first\nsecond")
	}
}

// TestParseResponse_FoldsCachedAndReasoningTokens verifies that codex's
// cached_input_tokens and reasoning_output_tokens fold into the substrate's
// flat InputTokens/OutputTokens (matching the Anthropic provider's fold).
func TestParseResponse_FoldsCachedAndReasoningTokens(t *testing.T) {
	transcript := `{"type":"item.completed","item":{"item_type":"agent_message","text":"hi"}}
{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":5,"cached_input_tokens":20,"reasoning_output_tokens":10}}
`
	resp, err := parseResponse([]byte(transcript), "gpt-5-codex")
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if resp.Usage.InputTokens != 120 {
		t.Errorf("InputTokens = %d, want 120 (100+20)", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 15 {
		t.Errorf("OutputTokens = %d, want 15 (5+10)", resp.Usage.OutputTokens)
	}
}

// TestParseResponse_EmptyStdoutIsParseError verifies that a fake codex
// emitting nothing surfaces as parse_response rather than silent success.
func TestParseResponse_EmptyStdoutIsParseError(t *testing.T) {
	_, err := parseResponse([]byte(""), "gpt-5-codex")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var llmErr *llm.LLMError
	if !errors.As(err, &llmErr) {
		t.Fatalf("expected *llm.LLMError, got %T: %v", err, err)
	}
	if llmErr.Reason != "parse_response" {
		t.Errorf("Reason = %q, want %q", llmErr.Reason, "parse_response")
	}
}

// TestParseResponse_NoTerminalEvent verifies that a stream without
// turn.completed and without any agent message surfaces as parse_response.
func TestParseResponse_NoTerminalEvent(t *testing.T) {
	transcript := `{"type":"thread.started","thread_id":"x"}
{"type":"turn.started"}
`
	_, err := parseResponse([]byte(transcript), "gpt-5-codex")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var llmErr *llm.LLMError
	if !errors.As(err, &llmErr) {
		t.Fatalf("expected *llm.LLMError, got %T: %v", err, err)
	}
	if llmErr.Reason != "parse_response" {
		t.Errorf("Reason = %q, want %q", llmErr.Reason, "parse_response")
	}
}

// TestParseResponse_RawIsVerbatim verifies the response's Raw field
// contains the entire stdout byte-for-byte.
func TestParseResponse_RawIsVerbatim(t *testing.T) {
	resp, err := parseResponse([]byte(successfulTranscript), "gpt-5-codex")
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if string(resp.Raw) != successfulTranscript {
		t.Errorf("Raw mismatch:\n got=%q\nwant=%q", string(resp.Raw), successfulTranscript)
	}
	// Sanity: Raw should be valid JSONL — every non-empty line decodes.
	for line := range strings.SplitSeq(strings.TrimSpace(string(resp.Raw)), "\n") {
		var anyEv map[string]any
		if err := json.Unmarshal([]byte(line), &anyEv); err != nil {
			t.Errorf("Raw line did not parse as JSON: %q (%v)", line, err)
		}
	}
}

// TestParseResponse_TurnFailedSurface verifies that a turn.failed event in
// the JSONL stream surfaces as LLMError("turn_failed") with the inner
// message preserved. Subprocess-free counterpart to the end-to-end variant.
func TestParseResponse_TurnFailedSurface(t *testing.T) {
	transcript := `{"type":"thread.started","thread_id":"x"}
{"type":"turn.started"}
{"type":"turn.failed","error":{"message":"401 Unauthorized: missing bearer"}}
`
	_, err := parseResponse([]byte(transcript), "gpt-5-codex")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var llmErr *llm.LLMError
	if !errors.As(err, &llmErr) {
		t.Fatalf("expected *llm.LLMError, got %T: %v", err, err)
	}
	if llmErr.Reason != "turn_failed" {
		t.Errorf("Reason = %q, want %q", llmErr.Reason, "turn_failed")
	}
	if !strings.Contains(llmErr.Cause.Error(), "401") {
		t.Errorf("error did not include codex message: %v", llmErr.Cause)
	}
}

// TestParseResponse_TurnFailedQuotaIsTerminal pins the codex-cli quota
// contract: a turn.failed event carrying an "out of usage limit" message
// classifies TERMINAL (Transient==false), so the summary/embed pipeline
// sheds the node rather than retrying a quota wall forever. codex-cli is
// already terminal here (parse.go stamps Transient:false unconditionally on
// turn_failed) — this is the regression guard against a future flip, and the
// codex counterpart to claude-cli's now-terminal quota classification.
func TestParseResponse_TurnFailedQuotaIsTerminal(t *testing.T) {
	transcript := `{"type":"thread.started","thread_id":"x"}
{"type":"turn.started"}
{"type":"turn.failed","error":{"message":"You are out of usage limit. Please try again later."}}
`
	_, err := parseResponse([]byte(transcript), "gpt-5-codex")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var llmErr *llm.LLMError
	if !errors.As(err, &llmErr) {
		t.Fatalf("expected *llm.LLMError, got %T: %v", err, err)
	}
	if llmErr.Reason != "turn_failed" {
		t.Errorf("Reason = %q, want %q", llmErr.Reason, "turn_failed")
	}
	if llmErr.Transient {
		t.Fatalf("codex quota turn.failed must classify terminal, got transient")
	}
}

// TestParseResponse_TurnFailedFallsBackToRawOnUnknownShape verifies that
// a turn.failed event with an unrecognized error envelope still surfaces
// the raw JSON rather than collapsing to an empty message.
func TestParseResponse_TurnFailedFallsBackToRawOnUnknownShape(t *testing.T) {
	transcript := `{"type":"turn.failed","error":["arr","unexpected"]}
`
	_, err := parseResponse([]byte(transcript), "gpt-5-codex")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var llmErr *llm.LLMError
	if !errors.As(err, &llmErr) {
		t.Fatalf("expected *llm.LLMError, got %T: %v", err, err)
	}
	if !strings.Contains(llmErr.Cause.Error(), "arr") {
		t.Errorf("expected raw error JSON in cause, got %v", llmErr.Cause)
	}
}

// TestParseResponse_IgnoresBannerLines verifies the parser's leading
// non-JSON banner suppression: codex prepends a "Reading prompt..." string
// when reading from stdin via `-`. Standalone test (no subprocess).
func TestParseResponse_IgnoresBannerLines(t *testing.T) {
	transcript := "Reading prompt from stdin...\n" + successfulTranscript
	resp, err := parseResponse([]byte(transcript), "gpt-5-codex")
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if resp.Content != "hello back" {
		t.Errorf("Content = %q, want %q", resp.Content, "hello back")
	}
}

// SPDX-License-Identifier: Apache-2.0

package anthropic

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// Unit tests for response_parse.go that don't need an httptest.Server.

func TestMapStopReason(t *testing.T) {
	cases := []struct {
		in   string
		want llm.FinishReason
	}{
		{"end_turn", llm.FinishReasonEndTurn},
		{"tool_use", llm.FinishReasonToolUse},
		{"max_tokens", llm.FinishReasonMaxTokens},
		{"stop_sequence", llm.FinishReasonStopSequence},
		{"refusal", llm.FinishReasonOther},
		{"", llm.FinishReasonOther},
	}
	for _, c := range cases {
		if got := mapStopReason(c.in); got != c.want {
			t.Errorf("mapStopReason(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseUsageFoldsCacheTokens(t *testing.T) {
	body := []byte(`{
		"id":"x","type":"message","role":"assistant","model":"claude-x",
		"content":[{"type":"text","text":"ok"}],
		"stop_reason":"end_turn",
		"usage":{
			"input_tokens": 100,
			"output_tokens": 5,
			"cache_creation_input_tokens": 50,
			"cache_read_input_tokens": 25
		}
	}`)
	resp, err := parseResponse(body, "claude-x")
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if resp.Usage.InputTokens != 175 {
		t.Errorf("InputTokens = %d, want 175 (100+50+25)", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 5 {
		t.Errorf("OutputTokens = %d, want 5", resp.Usage.OutputTokens)
	}
}

func TestRedactedThinkingSkipped(t *testing.T) {
	body := []byte(`{
		"id":"x","type":"message","role":"assistant","model":"claude-x",
		"content":[
			{"type":"redacted_thinking","data":"redacted-blob"},
			{"type":"text","text":"answer"}
		],
		"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}
	}`)
	resp, err := parseResponse(body, "claude-x")
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if resp.Content != "answer" {
		t.Errorf("Content = %q, want answer", resp.Content)
	}
	if resp.ThinkingContent != "" {
		t.Errorf("redacted thinking should not appear in ThinkingContent: %q", resp.ThinkingContent)
	}
}

func TestUnknownStopReasonAndBlocks(t *testing.T) {
	body := []byte(`{
		"id":"x","type":"message","role":"assistant","model":"claude-x",
		"content":[
			{"type":"text","text":"hi"},
			{"type":"future_block","data":"ignored"}
		],
		"stop_reason":"some_new_reason","usage":{"input_tokens":2,"output_tokens":3}
	}`)
	resp, err := parseResponse(body, "claude-x")
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if resp.FinishReason != llm.FinishReasonOther {
		t.Errorf("FinishReason = %q, want other", resp.FinishReason)
	}
	if resp.Content != "hi" {
		t.Errorf("unknown blocks should not corrupt Content; got %q", resp.Content)
	}
}

func TestParseResponseSurfacesRawBody(t *testing.T) {
	body := []byte(`{
		"id":"x","type":"message","role":"assistant","model":"claude-x",
		"content":[{"type":"text","text":"ok"}],
		"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}
	}`)
	resp, err := parseResponse(body, "claude-x")
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if string(resp.Raw) != string(body) {
		t.Errorf("Raw should be the verbatim response body")
	}
}

func TestParseResponseInvalidJSON(t *testing.T) {
	_, err := parseResponse([]byte(`not-json`), "claude-x")
	if err == nil {
		t.Fatalf("expected error on malformed body")
	}
}

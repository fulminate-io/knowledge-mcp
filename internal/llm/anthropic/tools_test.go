// SPDX-License-Identifier: Apache-2.0

package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// Tests for tool-call translation and round-trip. Helpers (newFakeServer,
// withClient, captured) live in anthropic_test.go in the same package.

func TestGenerateToolCallRoundTrip(t *testing.T) {
	srv, cap := newFakeServer(t, http.StatusOK, `{
		"id":"msg_05","type":"message","role":"assistant","model":"claude-x",
		"content":[{
			"type":"tool_use",
			"id":"toolu_01",
			"name":"lookup",
			"input":{"key":"abc"}
		}],
		"stop_reason":"tool_use",
		"usage":{"input_tokens":3,"output_tokens":4}
	}`)
	svc := withClient(t, srv, "claude-x")

	tools := []*schema.ToolInfo{
		{
			Name: "lookup",
			Desc: "look something up",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"key": {Type: schema.String, Required: true, Desc: "the key"},
			}),
		},
	}

	// Round-trip: assistant turn carrying a tool_use, then a tool message
	// answering it. The provider must serialize both shapes correctly.
	resp, err := svc.Generate(context.Background(),
		[]*schema.Message{
			{Role: schema.User, Content: "look up abc"},
			{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID:       "toolu_prev",
					Type:     "function",
					Function: schema.FunctionCall{Name: "lookup", Arguments: `{"key":"abc"}`},
				}},
			},
			{Role: schema.Tool, ToolCallID: "toolu_prev", Content: "result-text"},
		},
		llm.WithTools(tools),
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.FinishReason != llm.FinishReasonToolUse {
		t.Errorf("FinishReason = %q, want tool_use", resp.FinishReason)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "toolu_01" || tc.Function.Name != "lookup" {
		t.Errorf("tool call mismatch: %+v", tc)
	}
	if !strings.Contains(tc.Function.Arguments, `"key"`) {
		t.Errorf("tool arguments lost: %q", tc.Function.Arguments)
	}

	var sent anthropicRequest
	if err := json.Unmarshal(cap.body, &sent); err != nil {
		t.Fatalf("unmarshal sent body: %v", err)
	}

	// Tools translated.
	if len(sent.Tools) != 1 || sent.Tools[0].Name != "lookup" {
		t.Errorf("tools missing or wrong: %+v", sent.Tools)
	}
	// Schema must be a JSON object.
	if !json.Valid(sent.Tools[0].InputSchema) {
		t.Errorf("input_schema is not valid JSON: %s", string(sent.Tools[0].InputSchema))
	}

	// Three messages: user / assistant(tool_use) / user(tool_result).
	if len(sent.Messages) != 3 {
		t.Fatalf("messages len = %d, want 3", len(sent.Messages))
	}
	if sent.Messages[1].Role != "assistant" || sent.Messages[1].Content[0].Type != "tool_use" {
		t.Errorf("assistant tool_use turn malformed: %+v", sent.Messages[1])
	}
	if sent.Messages[2].Role != "user" || sent.Messages[2].Content[0].Type != "tool_result" {
		t.Errorf("tool_result turn malformed: %+v", sent.Messages[2])
	}
	if sent.Messages[2].Content[0].ToolUseID != "toolu_prev" {
		t.Errorf("tool_use_id missing: %+v", sent.Messages[2].Content[0])
	}
	if sent.Messages[2].Content[0].Content != "result-text" {
		t.Errorf("tool_result content not preserved: %+v", sent.Messages[2].Content[0])
	}
}

func TestTranslateToolsEmptyParams(t *testing.T) {
	tools := []*schema.ToolInfo{
		{Name: "noparams", Desc: "takes nothing"},
	}
	out, err := translateTools(tools)
	if err != nil {
		t.Fatalf("translateTools: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	if !json.Valid(out[0].InputSchema) {
		t.Errorf("input_schema must be valid JSON when ParamsOneOf is nil; got %s", out[0].InputSchema)
	}
	// Schema must be a JSON object body.
	var probe map[string]any
	if err := json.Unmarshal(out[0].InputSchema, &probe); err != nil {
		t.Errorf("input_schema is not an object: %v", err)
	}
	if probe["type"] != "object" {
		t.Errorf("schema type = %v, want object", probe["type"])
	}
}

func TestTranslateToolsNilEntryRejected(t *testing.T) {
	_, err := translateTools([]*schema.ToolInfo{nil})
	if err == nil {
		t.Fatalf("expected error on nil tool")
	}
}

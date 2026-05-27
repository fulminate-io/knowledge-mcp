// SPDX-License-Identifier: Apache-2.0

package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// anthropicResponse mirrors the /v1/messages response body. Fields we
// don't read (id, role, model echo) are still declared so json.Unmarshal
// validates the shape rather than silently swallowing typos.
type anthropicResponse struct {
	ID         string           `json:"id"`
	Type       string           `json:"type"`
	Role       string           `json:"role"`
	Model      string           `json:"model"`
	Content    []anthropicBlock `json:"content"`
	StopReason string           `json:"stop_reason"`
	Usage      anthropicUsage   `json:"usage"`
}

// anthropicUsage carries per-response token counts. cache_creation_input
// and cache_read_input are surfaced for diagnostics but are NOT folded
// into TokenUsage — TokenUsage is the substrate's portable shape and
// keeps the same {input,output} pair across all providers.
type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// parseResponse decodes the raw HTTP body and projects it onto
// llm.Response. The decoded body lands in Response.Raw verbatim so
// callers can drill into provider-native detail when debugging without
// re-issuing the request.
func parseResponse(body []byte, model llm.Model) (*llm.Response, error) {
	var raw anthropicResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	content, thinking, toolCalls := splitContentBlocks(raw.Content)

	return &llm.Response{
		Content:         content,
		ToolCalls:       toolCalls,
		ThinkingContent: thinking,
		FinishReason:    mapStopReason(raw.StopReason),
		Usage: llm.TokenUsage{
			InputTokens:  raw.InputTokens(),
			OutputTokens: raw.Usage.OutputTokens,
		},
		Model:    model,
		Provider: llm.ProviderAnthropic,
		Raw:      json.RawMessage(body),
	}, nil
}

// InputTokens sums the regular and cache token counts so callers see the
// total input cost rather than only fresh tokens. Cache counts are
// strictly additive on the Anthropic billing model.
func (r anthropicResponse) InputTokens() int {
	return r.Usage.InputTokens + r.Usage.CacheCreationInputTokens + r.Usage.CacheReadInputTokens
}

// splitContentBlocks walks the response content array once, peeling off
// text → Content (concatenated), thinking → ThinkingContent
// (concatenated), and tool_use → ToolCalls (in order).
//
// Unknown block types (including redacted_thinking) are skipped silently
// — Anthropic occasionally introduces new block kinds, and the substrate
// treats forward-compatible shapes as graceful degradation. The Raw
// field on the response still holds the full body for debugging.
func splitContentBlocks(blocks []anthropicBlock) (string, string, []schema.ToolCall) {
	var contentB, thinkingB strings.Builder
	var toolCalls []schema.ToolCall
	for _, b := range blocks {
		switch b.Type {
		case "text":
			contentB.WriteString(b.Text)
		case "thinking":
			thinkingB.WriteString(b.Thinking)
		case "tool_use":
			args := string(b.Input)
			if args == "" {
				args = "{}"
			}
			toolCalls = append(toolCalls, schema.ToolCall{
				ID:   b.ID,
				Type: "function",
				Function: schema.FunctionCall{
					Name:      b.Name,
					Arguments: args,
				},
			})
		}
	}
	return contentB.String(), thinkingB.String(), toolCalls
}

// mapStopReason normalizes Anthropic's stop_reason field to the
// substrate's FinishReason. Anything outside the documented set
// (refusal, content_filter, future additions) maps to Other so callers
// have a deterministic non-empty enum.
func mapStopReason(reason string) llm.FinishReason {
	switch reason {
	case "end_turn":
		return llm.FinishReasonEndTurn
	case "tool_use":
		return llm.FinishReasonToolUse
	case "max_tokens":
		return llm.FinishReasonMaxTokens
	case "stop_sequence":
		return llm.FinishReasonStopSequence
	default:
		return llm.FinishReasonOther
	}
}

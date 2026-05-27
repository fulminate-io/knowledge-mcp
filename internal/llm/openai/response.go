// SPDX-License-Identifier: Apache-2.0

package openai

import (
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// parseResponse converts the raw HTTP body into an llm.Response. The full
// body bytes are stashed in Response.Raw so callers debugging unexpected
// behavior can inspect the wire payload without re-parsing.
func parseResponse(body []byte, requestModel llm.Model) (*llm.Response, error) {
	var apiResp chatResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "parse_response", Cause: fmt.Errorf("openai: %w", err)}
	}
	if apiResp.Error != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "openai_api_error", Cause: fmt.Errorf("openai: %s", apiResp.Error.Message)}
	}
	if len(apiResp.Choices) == 0 {
		return nil, &llm.LLMError{Transient: false, Reason: "no_choices", Cause: fmt.Errorf("openai: no choices in response")}
	}

	choice := apiResp.Choices[0]
	resp := &llm.Response{
		Content:      choice.Message.Content,
		FinishReason: mapFinishReason(choice.FinishReason),
		Usage: llm.TokenUsage{
			InputTokens:  apiResp.Usage.PromptTokens,
			OutputTokens: apiResp.Usage.CompletionTokens,
		},
		Provider: llm.ProviderOpenAI,
		Raw:      json.RawMessage(body),
	}

	switch {
	case choice.Message.ReasoningContent != "":
		resp.ReasoningContent = choice.Message.ReasoningContent
	case choice.Message.Reasoning != "":
		resp.ReasoningContent = choice.Message.Reasoning
	}

	if model := apiResp.Model; model != "" {
		resp.Model = llm.Model(model)
	} else {
		resp.Model = requestModel
	}

	if len(choice.Message.ToolCalls) > 0 {
		resp.ToolCalls = make([]schema.ToolCall, len(choice.Message.ToolCalls))
		for i, tc := range choice.Message.ToolCalls {
			resp.ToolCalls[i] = schema.ToolCall{
				ID:   tc.ID,
				Type: tc.Type,
				Function: schema.FunctionCall{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			}
		}
	}

	return resp, nil
}

// mapFinishReason normalizes OpenAI's stop reason to llm.FinishReason. The
// OpenAI API documents stop / length / tool_calls / content_filter /
// function_call. Anything else lands in FinishReasonOther.
func mapFinishReason(raw string) llm.FinishReason {
	switch raw {
	case "stop":
		return llm.FinishReasonEndTurn
	case "length":
		return llm.FinishReasonMaxTokens
	case "tool_calls", "function_call":
		return llm.FinishReasonToolUse
	case "":
		return llm.FinishReasonOther
	default:
		return llm.FinishReasonOther
	}
}

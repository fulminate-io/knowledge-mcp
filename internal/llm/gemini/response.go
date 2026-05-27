package gemini

import (
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// parseResponse decodes the raw HTTP body into an *llm.Response.
//
// Pure function: no HTTP, no I/O, no Service dependency. Tests can call it
// directly with crafted bodies.
func parseResponse(body []byte, model llm.Model) (*llm.Response, error) {
	var raw geminiResponseBody
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, &llm.LLMError{
			Transient: false,
			Reason:    "parse_response",
			Cause:     fmt.Errorf("decode response: %w", err),
		}
	}

	if raw.PromptFeedback != nil && raw.PromptFeedback.BlockReason != "" {
		return nil, &llm.LLMError{
			Transient: false,
			Reason:    "prompt_blocked",
			Cause:     fmt.Errorf("gemini blocked prompt: %s", raw.PromptFeedback.BlockReason),
		}
	}

	if len(raw.Candidates) == 0 {
		return nil, &llm.LLMError{
			Transient: false,
			Reason:    "no_candidates",
			Cause:     fmt.Errorf("gemini response had no candidates"),
		}
	}

	cand := raw.Candidates[0]
	resp := &llm.Response{
		Model:    model,
		Provider: llm.ProviderGemini,
		Raw:      append(json.RawMessage(nil), body...),
	}

	if cand.Content != nil {
		content, reasoning, calls, err := splitParts(cand.Content.Parts)
		if err != nil {
			return nil, &llm.LLMError{
				Transient: false,
				Reason:    "parse_response",
				Cause:     err,
			}
		}
		resp.Content = content
		resp.ReasoningContent = reasoning
		resp.ToolCalls = calls
	}

	resp.FinishReason = mapFinishReason(cand.FinishReason, len(resp.ToolCalls) > 0)

	if raw.UsageMetadata != nil {
		resp.Usage = llm.TokenUsage{
			InputTokens:  raw.UsageMetadata.PromptTokenCount,
			OutputTokens: raw.UsageMetadata.CandidatesTokenCount,
		}
	}

	return resp, nil
}

// splitParts walks the parts array, concatenating text into Content,
// concatenating thought-text into ReasoningContent, and decoding
// functionCall parts into eino schema.ToolCall values.
func splitParts(parts []geminiPart) (content string, reasoning string, calls []schema.ToolCall, err error) {
	for i, p := range parts {
		switch {
		case p.FunctionCall != nil:
			argBytes, mErr := json.Marshal(p.FunctionCall.Args)
			if mErr != nil {
				return "", "", nil, fmt.Errorf("part %d: marshal function args: %w", i, mErr)
			}
			idx := i
			calls = append(calls, schema.ToolCall{
				Index: &idx,
				// Gemini doesn't return a stable tool-call id; synthesize
				// one from the function name + index so callers that
				// thread IDs through their pipeline still see uniqueness
				// within the turn.
				ID:   fmt.Sprintf("%s-%d", p.FunctionCall.Name, i),
				Type: "function",
				Function: schema.FunctionCall{
					Name:      p.FunctionCall.Name,
					Arguments: string(argBytes),
				},
			})
		case p.Thought:
			reasoning += p.Text
		case p.Text != "":
			content += p.Text
		}
	}
	return content, reasoning, calls, nil
}

// mapFinishReason translates Gemini's finishReason enum into the
// substrate's normalized FinishReason. When tool calls are present we
// prefer FinishReasonToolUse since callers loop on tool results; Gemini
// itself reports "STOP" alongside functionCall parts.
func mapFinishReason(raw string, hasToolCalls bool) llm.FinishReason {
	if hasToolCalls {
		return llm.FinishReasonToolUse
	}
	switch raw {
	case "STOP":
		return llm.FinishReasonEndTurn
	case "MAX_TOKENS":
		return llm.FinishReasonMaxTokens
	case "STOP_SEQUENCE":
		return llm.FinishReasonStopSequence
	default:
		// SAFETY, RECITATION, BLOCKLIST, PROHIBITED_CONTENT, OTHER, ""
		return llm.FinishReasonOther
	}
}

package llm

import (
	"encoding/json"

	"github.com/cloudwego/eino/schema"
)

// Response is the populated result of a single Generate call.
//
// Field shapes match what the provider implementations in Phase 2/3 actually
// produce. Content holds the assistant's text reply; ToolCalls holds any
// tool-use intents the model emitted on this turn (eino's schema.ToolCall
// shape); FinishReason normalizes the provider's stop reason; Usage records
// input/output token counts.
//
// Model and Provider duplicate the request configuration for caller
// convenience — callers fanning responses through downstream code shouldn't
// have to thread the provider/model context through separately.
//
// Raw is an optional provider-native body for diagnostics. Implementations
// MAY stash their decoded HTTP response (or equivalent CLI output) here so
// callers debugging an unexpected response can examine it without
// re-parsing. json.RawMessage rather than `any` so the contract is
// explicit: a JSON-shaped blob, not arbitrary Go values.
type Response struct {
	Content   string
	ToolCalls []schema.ToolCall
	// ThinkingContent is the Anthropic extended-thinking text emitted on this
	// turn (Claude 4.x extended thinking blocks). Empty when extended thinking
	// is off or unsupported by the provider.
	ThinkingContent string
	// ReasoningContent is the OpenAI o-series / Gemini reasoning text emitted
	// on this turn. Empty when reasoning is off or unsupported.
	ReasoningContent string
	FinishReason     FinishReason
	Usage            TokenUsage
	Model            Model
	Provider         Provider
	Raw              json.RawMessage
}

package llm

import "github.com/fulminate-io/knowledge-mcp/internal/config"

// Provider identifies an LLM provider that backs a Client.
//
// The canonical type + constants live in internal/config since "which LLM
// does the user want?" is fundamentally a config-validation concern. This
// alias re-exports the type so existing llm.Provider / llm.ProviderXxx
// call sites continue to compile.
type Provider = config.Provider

const (
	ProviderOpenAI    = config.ProviderOpenAI
	ProviderAnthropic = config.ProviderAnthropic
	ProviderGemini    = config.ProviderGemini
	ProviderClaudeCLI = config.ProviderClaudeCLI
	ProviderCodexCLI  = config.ProviderCodexCLI
)

// Model is a free-form model identifier (e.g. "gpt-5-mini", "claude-opus-4-7").
//
// Concrete model names live with the provider implementations; the substrate
// treats Model as opaque.
type Model string

// String returns m as a plain string.
func (m Model) String() string { return string(m) }

// FinishReason describes why a model stopped generating.
//
// The set normalizes the most common values across providers. Anything a
// provider returns that doesn't fit one of the named constants maps to
// FinishReasonOther.
type FinishReason string

const (
	// FinishReasonEndTurn is a normal end-of-turn stop.
	FinishReasonEndTurn FinishReason = "end_turn"
	// FinishReasonToolUse means the model emitted a tool call and is waiting on a result.
	FinishReasonToolUse FinishReason = "tool_use"
	// FinishReasonMaxTokens means the model hit its output token cap.
	FinishReasonMaxTokens FinishReason = "max_tokens"
	// FinishReasonStopSequence means the model hit a configured stop sequence.
	FinishReasonStopSequence FinishReason = "stop_sequence"
	// FinishReasonOther covers everything else (errors, content filters, unknown).
	FinishReasonOther FinishReason = "other"
)

// String returns r as a plain string.
func (r FinishReason) String() string { return string(r) }

// TokenUsage records input and output token counts for a single Generate call.
//
// Knowledge intentionally does not track cached/thinking/reasoning sub-counts
// at this layer — they're provider-specific and most callers only need the
// two top-line numbers for budgeting.
type TokenUsage struct {
	InputTokens  int
	OutputTokens int
}

// Total returns InputTokens + OutputTokens.
func (t TokenUsage) Total() int { return t.InputTokens + t.OutputTokens }

// Add adds src into t in place.
func (t *TokenUsage) Add(src TokenUsage) {
	t.InputTokens += src.InputTokens
	t.OutputTokens += src.OutputTokens
}

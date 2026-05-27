// Package llm is knowledge's LLM-call substrate.
//
// One Client interface (Generate-shaped, non-streaming) backed by per-provider
// implementations that self-register via init(). Callers obtain a Client through
// NewClient and dispatch with functional options:
//
//	import (
//	    "github.com/fulminate-io/knowledge-mcp/internal/llm"
//	    _ "github.com/fulminate-io/knowledge-mcp/internal/llm/anthropic"
//	)
//
//	cfg := &llm.Config{
//	    Provider: llm.ProviderAnthropic,
//	    APIKey:   os.Getenv("ANTHROPIC_API_KEY"),
//	    Model:    "claude-haiku-4-5-20251001",
//	}
//	client, err := llm.NewClient(ctx, cfg)
//	if err != nil { return err }
//
//	resp, err := client.Generate(ctx,
//	    []*schema.Message{{Role: schema.User, Content: "Say hi."}},
//	    llm.WithMaxTokens(64),
//	    llm.WithTemperature(0.2),
//	)
//
// # Providers
//
//   - [github.com/fulminate-io/knowledge-mcp/internal/llm/openai] — OpenAI direct API
//   - [github.com/fulminate-io/knowledge-mcp/internal/llm/anthropic] — Anthropic direct API
//   - [github.com/fulminate-io/knowledge-mcp/internal/llm/gemini] — Google Gemini direct API
//   - [github.com/fulminate-io/knowledge-mcp/internal/llm/claudecli] — Anthropic claude CLI subprocess
//   - [github.com/fulminate-io/knowledge-mcp/internal/llm/codexcli] — OpenAI codex CLI subprocess
//
// Blank-import each provider sub-package you want available. The sub-package's
// init() registers a factory under its [Provider] constant; [NewClient] then
// dispatches by [Config.Provider].
//
// # Substrate fields
//
// [RequestOptions] exposes Model, SystemPrompt, Tools, ResponseFormat,
// Temperature, TopP, TopK, MaxTokens, StopSequences, ExtendedThinking,
// DisableExtendedThinking, ThinkingBudget, ReasoningEffort, BaseURL, APIKey.
// Provider implementations honor every field they can and document any
// they cannot — the substrate never silently drops a knob.
//
// [Response] returns Content, ToolCalls, ThinkingContent, ReasoningContent,
// FinishReason, Usage, Model, Provider, and a verbatim Raw body for
// diagnostics.
//
// # Tools
//
// Tools follow the eino [schema.ToolInfo] / [schema.ToolCall] shape.
// A typical tool-use loop reads:
//
//	resp, err := client.Generate(ctx, msgs, llm.WithTools(tools))
//	if resp.FinishReason == llm.FinishReasonToolUse {
//	    for _, call := range resp.ToolCalls { ... }
//	    msgs = append(msgs, toolResultMessages...)
//	    resp, err = client.Generate(ctx, msgs, llm.WithTools(tools))
//	}
//
// The substrate is intentionally non-iterative: callers own the loop.
//
// # Errors
//
// Every implementation returns [*LLMError] for failed calls. Use
// [errors.As] to access Transient + Reason for retry decisions:
//
//	var llmErr *llm.LLMError
//	if errors.As(err, &llmErr) && llmErr.Transient {
//	    // retry on next tick
//	}
//
// [IsTransient] is the convenience wrapper.
//
// # Testing
//
// [FakeClient] is the cross-package test double. Queue responses, run the
// code under test, then assert against the recorded [FakeCall] history.
// Tests that need NewClient-shaped wiring can register a factory returning
// a FakeClient under a one-off [Provider] id; see integration_test.go.
//
// # Constraints
//
// This package MUST NOT import core/ or cmd/ — it is a leaf the rest of the
// codebase depends on. Implementations MUST NOT truncate message content
// before sending; if the caller's input exceeds a model's context, the
// implementation returns an error rather than silently dropping content.
package llm

// SPDX-License-Identifier: Apache-2.0

package claudecli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// buildArgs translates messages + RequestOptions into argv + stdin for the
// claude CLI's `-p` (prompt) mode.
//
// Validation:
//   - len(messages) must be exactly 1 with Role == User. The CLI's prompt
//     mode accepts a single user prompt on stdin and has no native
//     multi-turn surface. Multi-turn slices return an LLMError (callers
//     that need multi-turn must use an API provider).
//
// Tool-use:
//   - When opts.Tools is non-empty, buildArgs emits --strict-mcp-config +
//     --mcp-config pointing the CLI at the shared knowledge daemon's loopback
//     HTTP MCP endpoint (so it speaks MCP to the same knowledge graph the
//     daemon fronts — and ONLY that, not the user's globally-configured MCP
//     servers) plus --allowedTools to pre-authorize each tool by its
//     mcp__knowledge__<name> qualified form.
//     The CLI runs its OWN MCP/ReAct loop and returns a single final
//     text response in the JSON envelope; intermediate tool_use blocks
//     do not round-trip to the substrate. eino-side react.NewAgent sees
//     one text-only response and terminates immediately, treating the
//     CLI's final answer as the loop output.
//
// Honored RequestOptions:
//   - opts.Model → --model
//   - opts.SystemPrompt → --system-prompt
//   - opts.ResponseFormat (Type "json_schema") → --json-schema
//   - opts.Tools → --strict-mcp-config --mcp-config + --allowedTools (when
//     non-empty); when empty (e.g. the summarizer) → --strict-mcp-config
//     --mcp-config '{}'. Either way --strict-mcp-config makes the CLI load ONLY
//     the config we pass, never the user's globally-configured MCP servers.
//
// Ignored RequestOptions (no flag in -p mode): Temperature, TopP, TopK,
// MaxTokens, StopSequences, ExtendedThinking, ThinkingBudget,
// DisableExtendedThinking, ReasoningEffort, BaseURL, APIKey. The CLI
// authenticates via the user's local `claude login` (OAuth tokens in
// keychain); injecting APIKey per-call is out of scope.
//
// Static flags:
//   - -p (prompt mode, non-interactive)
//   - --output-format json (single-result JSON we parse below)
//   - --no-session-persistence (throwaway sessions; mirrors store/summarize_cli.go)
//   - --disable-slash-commands (don't load skills/commands; we want a clean turn)
func buildArgs(model llm.Model, messages []*schema.Message, opts *llm.RequestOptions) ([]string, string, error) {
	systemFromMessages, prompt, err := extractPrompt(messages)
	if err != nil {
		return nil, "", err
	}

	args := []string{
		"-p",
		"--output-format", "json",
		"--no-session-persistence",
		"--disable-slash-commands",
	}
	if model != "" {
		args = append(args, "--model", string(model))
	}
	// opts.SystemPrompt wins over a system-role message in the slice when
	// both are set — it's the explicit per-call override surface.
	systemPrompt := systemFromMessages
	if opts != nil && opts.SystemPrompt != "" {
		systemPrompt = opts.SystemPrompt
	}
	if systemPrompt != "" {
		args = append(args, "--system-prompt", systemPrompt)
	}
	if opts != nil && opts.ResponseFormat != nil && opts.ResponseFormat.Type == "json_schema" {
		schemaBytes, err := marshalJSONSchema(opts.ResponseFormat.Schema)
		if err != nil {
			return nil, "", err
		}
		args = append(args, "--json-schema", string(schemaBytes))
	}
	if opts != nil && len(opts.Tools) > 0 {
		mcpConfig, allowed, err := buildMCPConfig(opts.Tools)
		if err != nil {
			return nil, "", err
		}
		args = append(args, "--strict-mcp-config", "--mcp-config", mcpConfig, "--allowedTools", allowed)
	} else {
		// No tool-use (e.g. the summarizer): pin an empty, strict MCP config so
		// the CLI loads NONE of the user's configured MCP servers. We never use
		// them on this path, and spawning each per call wastes startup time.
		// claude CLI validates the config shape and rejects a bare "{}" with
		// `mcpServers: Invalid input: expected record, received undefined`, so
		// the empty config must still carry the mcpServers key with an empty
		// record value.
		args = append(args, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`)
	}

	return args, prompt, nil
}

// buildMCPConfig generates the --mcp-config + --allowedTools arg pair for a
// CLI invocation that needs tool-use. The MCP config points the CLI at the
// shared `knowledge serve` daemon's loopback streamable-HTTP endpoint
// (http transport, daemon /mcp url) under server name "knowledge" — NOT a
// per-call stdio child of this process. The worker connects to the one
// running daemon over HTTP exactly as editors do, so there is no spawned
// child to break recursion on (the daemon runs a single shared runtime).
// Tool names go from bare "search" / "thoughts" to the CLI's qualified form
// "mcp__knowledge__search" / "mcp__knowledge__thoughts" for --allowedTools so
// the CLI doesn't prompt for permission per call.
//
// The returned mcp-config is inline JSON suitable for `--mcp-config <json>`;
// the CLI accepts both a file path and an inline JSON string for that flag.
// The HTTP entry shape ({"type":"http","url":…}) matches what `claude mcp
// add --transport http` writes into .claude.json.
func buildMCPConfig(tools []*schema.ToolInfo) (string, string, error) {
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"knowledge": map[string]any{
				"type": "http",
				"url":  fmt.Sprintf("http://127.0.0.1:%d/mcp", graphclient.DefaultMCPHTTPPort),
			},
		},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return "", "", &llm.LLMError{Transient: false, Reason: "config", Cause: fmt.Errorf("claude-cli: marshal mcp config: %w", err)}
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		if t == nil || t.Name == "" {
			continue
		}
		names = append(names, "mcp__knowledge__"+t.Name)
	}
	return string(raw), strings.Join(names, ","), nil
}

// extractPrompt walks messages and pulls out (at most) one system + exactly
// one user message. The CLI's `-p` mode is single-turn — assistant messages
// or repeat user/system entries imply multi-turn and require an API
// provider. Eino's react.NewAgent typically passes messages as
// [systemMsg, userMsg] on the first call; with --mcp-config the CLI runs
// its own ReAct loop internally and we never see a follow-up call, so
// 1- or 2-message slices are the only legal inputs.
//
// The returned systemFromMessages is empty when no system-role message
// appears; buildArgs prefers opts.SystemPrompt when set so an explicit
// per-call system override wins over a system-role message in the slice.
func extractPrompt(messages []*schema.Message) (systemFromMessages, userPrompt string, err error) {
	if len(messages) == 0 {
		return "", "", &llm.LLMError{Transient: false, Reason: "config", Cause: fmt.Errorf("claude-cli: no messages supplied")}
	}
	var sysMsg, usrMsg *schema.Message
	for i, msg := range messages {
		if msg == nil {
			return "", "", &llm.LLMError{Transient: false, Reason: "config", Cause: fmt.Errorf("claude-cli: nil message at index %d", i)}
		}
		switch msg.Role {
		case schema.System:
			if sysMsg != nil {
				return "", "", &llm.LLMError{Transient: false, Reason: "config", Cause: fmt.Errorf("claude-cli: multiple system messages (multi-turn requires API provider)")}
			}
			sysMsg = msg
		case schema.User:
			if usrMsg != nil {
				return "", "", &llm.LLMError{Transient: false, Reason: "config", Cause: fmt.Errorf("claude-cli: multiple user messages (multi-turn requires API provider)")}
			}
			usrMsg = msg
		default:
			return "", "", &llm.LLMError{Transient: false, Reason: "config", Cause: fmt.Errorf("claude-cli: -p mode requires user/system roles, got %q at index %d (multi-turn requires API provider)", msg.Role, i)}
		}
	}
	if usrMsg == nil {
		return "", "", &llm.LLMError{Transient: false, Reason: "config", Cause: fmt.Errorf("claude-cli: no user message")}
	}
	if sysMsg != nil {
		systemFromMessages = sysMsg.Content
	}
	return systemFromMessages, usrMsg.Content, nil
}

// marshalJSONSchema accepts the shapes a caller might hand us
// (json.RawMessage, []byte, string, struct, map) and returns canonical
// JSON bytes suitable for --json-schema. Mirrors the substrate behavior
// in internal/llm/openai/translate.go:marshalSchema.
func marshalJSONSchema(s any) ([]byte, error) {
	if s == nil {
		return []byte(`{}`), nil
	}
	switch v := s.(type) {
	case json.RawMessage:
		return []byte(v), nil
	case []byte:
		return v, nil
	case string:
		return []byte(v), nil
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "config", Cause: fmt.Errorf("claude-cli: marshal json schema: %w", err)}
	}
	return raw, nil
}

// cliResultEnvelope is the `--output-format json` reply.
//
// Fields populated by the CLI today (verified by inspecting `claude -p
// --output-format json` output, May 2026):
//
//   - type: always "result" for non-streaming.
//   - subtype: "success" when the call completed; the CLI may surface
//     "error_*" subtypes on failure paths.
//   - is_error: true on auth/transport/upstream failures even when the
//     process exits 0.
//   - result: the assistant's text reply.
//   - stop_reason: "end_turn" / "tool_use" / "max_tokens" / "stop_sequence".
//   - usage.input_tokens / usage.output_tokens: token counts when the
//     call reached the upstream API.
//   - structured_output: present when --json-schema was supplied; holds
//     the schema-validated payload alongside `result`.
//
// We capture the verbatim bytes in Response.Raw so callers debugging an
// unexpected response can re-parse without re-invoking the CLI.
type cliResultEnvelope struct {
	Type             string          `json:"type"`
	Subtype          string          `json:"subtype"`
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	StopReason       string          `json:"stop_reason"`
	StructuredOutput json.RawMessage `json:"structured_output,omitempty"`
	Usage            cliUsage        `json:"usage"`
}

type cliUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// parseResponse turns the CLI's JSON reply into an llm.Response.
//
// The substrate Response surface the claude CLI does NOT populate:
//   - ToolCalls: claude-cli `-p` mode does not surface tool_use blocks in
//     JSON output, and buildArgs rejects requests with Tools set, so this
//     field is always empty.
//   - ThinkingContent: not surfaced in `-p` JSON output. Knowledge sets
//     MAX_THINKING_TOKENS=0 in runCLI to keep thinking off; even if the
//     CLI's thinking blocks were surfaced, the substrate caller wouldn't
//     see them through this provider.
//   - ReasoningContent: same — not surfaced in `-p` JSON output.
//
// Populated:
//   - Content: result string from the envelope.
//   - FinishReason: stop_reason mapped to llm.FinishReason.
//   - Usage: input/output token counts from envelope.usage.
//   - Model / Provider: copied from the request.
//   - Raw: verbatim CLI output bytes.
func parseResponse(body []byte, requestModel llm.Model) (*llm.Response, error) {
	var env cliResultEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		// Include the verbatim body bytes so operators triaging an
		// unexpected CLI output (auth dialog leaked to stdout, schema
		// drift) can see what came back. No truncation — full body.
		return nil, &llm.LLMError{
			Transient: false,
			Reason:    "parse_cli_response",
			Cause:     fmt.Errorf("claude-cli: %w (body: %s)", err, string(body)),
		}
	}
	if env.IsError {
		return nil, &llm.LLMError{
			Transient: cliErrorSignalTransient(env.Result + " " + env.Subtype),
			Reason:    "cli_response_error",
			Cause:     fmt.Errorf("claude-cli: %s", env.Result),
		}
	}

	// When --json-schema was supplied, the CLI populates structured_output
	// with the schema-validated JSON payload AND result with a free-form
	// commentary string. Callers asking for json_schema want the validated
	// JSON; returning the commentary breaks downstream JSON parsers. Prefer
	// structured_output when present.
	content := env.Result
	if len(env.StructuredOutput) > 0 && string(env.StructuredOutput) != "null" {
		content = string(env.StructuredOutput)
	}

	resp := &llm.Response{
		Content:      content,
		FinishReason: mapFinishReason(env.StopReason),
		Usage: llm.TokenUsage{
			InputTokens:  env.Usage.InputTokens,
			OutputTokens: env.Usage.OutputTokens,
		},
		Model:    requestModel,
		Provider: llm.ProviderClaudeCLI,
		Raw:      json.RawMessage(body),
	}
	return resp, nil
}

// mapFinishReason normalizes the CLI's stop_reason to llm.FinishReason.
// The CLI's vocabulary already matches Anthropic's Messages API stop
// reasons, so the mapping is largely identity. Empty/unknown lands in
// FinishReasonOther.
func mapFinishReason(raw string) llm.FinishReason {
	switch raw {
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

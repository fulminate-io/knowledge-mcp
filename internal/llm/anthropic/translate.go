// SPDX-License-Identifier: Apache-2.0

package anthropic

import (
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// anthropicRequest mirrors the POST body for /v1/messages. Fields
// intentionally use json tags with omitempty so an unset knob never lands
// in the wire request — Anthropic rejects unknown / null fields strictly.
type anthropicRequest struct {
	Model         string             `json:"model"`
	System        string             `json:"system,omitempty"`
	Messages      []anthropicMsg     `json:"messages"`
	Tools         []anthropicTool    `json:"tools,omitempty"`
	MaxTokens     int                `json:"max_tokens"`
	Temperature   *float32           `json:"temperature,omitempty"`
	TopP          *float32           `json:"top_p,omitempty"`
	TopK          *int32             `json:"top_k,omitempty"`
	StopSequences []string           `json:"stop_sequences,omitempty"`
	Thinking      *anthropicThinking `json:"thinking,omitempty"`
}

// anthropicMsg is one turn in the Messages thread.
type anthropicMsg struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

// anthropicBlock is a typed content block. The Anthropic Messages API
// uses a discriminated-union pattern keyed on Type; json.Marshal with
// omitempty drops fields that don't apply to the active type.
type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	// Thinking carries Anthropic's extended-thinking text on response
	// blocks of type "thinking". Only used on read.
	Thinking string `json:"thinking,omitempty"`
}

// anthropicTool mirrors Anthropic's tool definition block. InputSchema is
// always a JSON object body (Anthropic rejects arrays / scalars), so we
// pass json.RawMessage and let translateTools build the canonical shape.
type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// anthropicThinking enables Anthropic extended thinking. The 2023-06-01
// Messages-API shape names the type "enabled" and the budget cap
// "budget_tokens" (integer).
type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

// buildRequest translates RequestOptions + eino messages into an
// anthropicRequest. Returns the JSON-encoded body ready for HTTP.
//
// Knobs intentionally NOT translated and the reason for each:
//
//   - ReasoningEffort: Anthropic does not have an "effort" knob — extended
//     thinking is the equivalent surface, and it takes a token budget
//     instead of a low/medium/high enum. ReasoningEffort is silently
//     ignored here; callers that want thinking on Anthropic should use
//     WithExtendedThinking.
//   - ResponseFormat: the Messages API has no JSON-schema-mode field. The
//     Anthropic-recommended pattern for structured output is a tool_use
//     contract, which the caller already controls via WithTools. We pass
//     ResponseFormat through silently rather than synthesizing a tool
//     behind the caller's back.
func buildRequest(model llm.Model, messages []*schema.Message, options *llm.RequestOptions) ([]byte, error) {
	system, msgs, err := translateMessages(options.SystemPrompt, messages)
	if err != nil {
		return nil, err
	}

	tools, err := translateTools(options.Tools)
	if err != nil {
		return nil, err
	}

	maxTokens := options.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}

	req := anthropicRequest{
		Model:         string(model),
		System:        system,
		Messages:      msgs,
		Tools:         tools,
		MaxTokens:     maxTokens,
		Temperature:   options.Temperature,
		TopP:          options.TopP,
		TopK:          options.TopK,
		StopSequences: options.StopSequences,
	}

	applyThinking(&req, options)

	return json.Marshal(req)
}

// applyThinking attaches the thinking block when the caller asked for
// extended thinking. DisableExtendedThinking always wins — even if a
// future Claude default would have it on, callers can force it off.
//
// When extended thinking is active Anthropic requires:
//
//   - Temperature == 1 (or unset). If the caller pinned a different
//     temperature we drop it back to provider-default rather than send a
//     request the API will 400 on. The caller asked for thinking; thinking
//     wins.
//   - max_tokens > budget_tokens. We bump max_tokens to budget+1024 if
//     necessary so the model has room to actually answer after thinking.
func applyThinking(req *anthropicRequest, options *llm.RequestOptions) {
	if options.DisableExtendedThinking || !options.ExtendedThinking {
		return
	}
	budget := options.ThinkingBudget
	if budget <= 0 {
		// Anthropic doesn't have a documented sentinel for "use default
		// budget" inside the thinking block — the field is required.
		// 1024 is the smallest documented budget per the public docs and
		// gives us a deterministic floor across models.
		budget = 1024
	}
	req.Thinking = &anthropicThinking{Type: "enabled", BudgetTokens: budget}
	if req.MaxTokens <= budget {
		req.MaxTokens = budget + 1024
	}
	// Anthropic requires temperature=1 when thinking is enabled. Drop any
	// caller-supplied temperature override.
	req.Temperature = nil
}

// translateMessages converts eino schema messages into Anthropic's
// system+messages split. SystemPrompt from RequestOptions concatenates
// in front of any schema.System messages so callers can layer.
//
// Tool messages (schema.Tool) become user-role tool_result blocks per the
// Anthropic shape — the API has no dedicated "tool" role.
//
// Adjacent same-role messages are NOT merged. The Messages API does
// require alternating user/assistant turns when no tool_use is in flight,
// but eino callers are responsible for assembling well-formed transcripts.
// We pass the structure through verbatim so a malformed sequence surfaces
// as a 400 from the API rather than silent corrupted state on our side.
func translateMessages(systemPrompt string, messages []*schema.Message) (string, []anthropicMsg, error) {
	system := systemPrompt
	out := make([]anthropicMsg, 0, len(messages))

	for i, msg := range messages {
		if msg == nil {
			return "", nil, fmt.Errorf("messages[%d] is nil", i)
		}
		switch msg.Role {
		case schema.System:
			if system == "" {
				system = msg.Content
			} else {
				system = system + "\n\n" + msg.Content
			}
		case schema.User:
			out = append(out, anthropicMsg{
				Role:    "user",
				Content: []anthropicBlock{{Type: "text", Text: msg.Content}},
			})
		case schema.Assistant:
			blocks := assistantBlocks(msg)
			if len(blocks) == 0 {
				// Skip empty assistant turns rather than send an empty
				// content array (which Anthropic 400s on).
				continue
			}
			out = append(out, anthropicMsg{Role: "assistant", Content: blocks})
		case schema.Tool:
			content := msg.Content
			if msg.ToolCallID == "" {
				return "", nil, fmt.Errorf("messages[%d]: tool message missing ToolCallID", i)
			}
			out = append(out, anthropicMsg{
				Role: "user",
				Content: []anthropicBlock{{
					Type:      "tool_result",
					ToolUseID: msg.ToolCallID,
					Content:   content,
				}},
			})
		default:
			return "", nil, fmt.Errorf("messages[%d]: unsupported role %q", i, msg.Role)
		}
	}
	return system, out, nil
}

// assistantBlocks builds the content array for one assistant turn. Text
// content (if any) leads, then every tool_call becomes a tool_use block.
//
// Tool call arguments are passed through verbatim as a JSON RawMessage —
// no validation, no truncation. If the arguments string is empty we
// substitute "{}" so json.RawMessage marshals to a valid JSON object.
func assistantBlocks(msg *schema.Message) []anthropicBlock {
	var blocks []anthropicBlock
	if msg.Content != "" {
		blocks = append(blocks, anthropicBlock{Type: "text", Text: msg.Content})
	}
	for _, tc := range msg.ToolCalls {
		input := json.RawMessage(tc.Function.Arguments)
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		blocks = append(blocks, anthropicBlock{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: input,
		})
	}
	return blocks
}

// translateTools converts eino tool definitions into Anthropic's tool
// shape. Each tool's ParamsOneOf is rendered to a JSON Schema object;
// tools with nil ParamsOneOf get a stub `{"type":"object","properties":{}}`
// schema (Anthropic requires input_schema to be a JSON object).
func translateTools(tools []*schema.ToolInfo) ([]anthropicTool, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	out := make([]anthropicTool, len(tools))
	for i, t := range tools {
		if t == nil {
			return nil, fmt.Errorf("tools[%d] is nil", i)
		}
		schemaJSON, err := toolInputSchema(t)
		if err != nil {
			return nil, fmt.Errorf("tools[%d] (%s): %w", i, t.Name, err)
		}
		out[i] = anthropicTool{
			Name:        t.Name,
			Description: t.Desc,
			InputSchema: schemaJSON,
		}
	}
	return out, nil
}

// toolInputSchema renders a single tool's parameter schema. Returns the
// stub object schema when ParamsOneOf is nil. Kept separate so
// translateTools doesn't trip the nestif lint.
func toolInputSchema(t *schema.ToolInfo) (json.RawMessage, error) {
	stub := json.RawMessage(`{"type":"object","properties":{}}`)
	if t.ParamsOneOf == nil {
		return stub, nil
	}
	js, err := t.ToJSONSchema()
	if err != nil {
		return nil, fmt.Errorf("json schema: %w", err)
	}
	if js == nil {
		return stub, nil
	}
	raw, err := json.Marshal(js)
	if err != nil {
		return nil, fmt.Errorf("marshal schema: %w", err)
	}
	return raw, nil
}

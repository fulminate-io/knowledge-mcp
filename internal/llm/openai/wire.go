// SPDX-License-Identifier: Apache-2.0

package openai

import "encoding/json"

// chatRequest is the /v1/chat/completions request body. Field tags use
// `omitempty` so optional knobs only appear when the caller set them.
type chatRequest struct {
	Model           string          `json:"model"`
	Messages        []chatMessage   `json:"messages"`
	Temperature     *float32        `json:"temperature,omitempty"`
	TopP            *float32        `json:"top_p,omitempty"`
	MaxTokens       int             `json:"max_completion_tokens,omitempty"`
	Stop            []string        `json:"stop,omitempty"`
	Tools           []chatTool      `json:"tools,omitempty"`
	ResponseFormat  *responseFormat `json:"response_format,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
}

// chatMessage is one element of the messages array. content is a string for
// text turns and the tool result; tool_calls is populated on assistant turns
// emitting tool intents.
type chatMessage struct {
	Role       string            `json:"role"`
	Content    string            `json:"content,omitempty"`
	Name       string            `json:"name,omitempty"`
	ToolCalls  []chatMessageTool `json:"tool_calls,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
}

type chatMessageTool struct {
	ID       string                  `json:"id"`
	Type     string                  `json:"type"`
	Function chatMessageToolFunction `json:"function"`
}

type chatMessageToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatTool struct {
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

// responseFormat constrains the assistant output.
type responseFormat struct {
	Type       string          `json:"type"`
	JSONSchema *jsonSchemaSpec `json:"json_schema,omitempty"`
}

type jsonSchemaSpec struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict"`
}

// chatResponse is the /v1/chat/completions response body. Reasoning content
// is captured under both `reasoning_content` (DeepSeek/o-series Chat
// Completions convention) and `reasoning` (some gateways) so we don't drop
// it on either spelling.
type chatResponse struct {
	ID      string       `json:"id,omitempty"`
	Model   string       `json:"model,omitempty"`
	Choices []chatChoice `json:"choices"`
	Usage   chatUsage    `json:"usage"`
	Error   *chatError   `json:"error,omitempty"`
}

type chatChoice struct {
	Index        int           `json:"index"`
	Message      chatChoiceMsg `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type chatChoiceMsg struct {
	Role             string            `json:"role"`
	Content          string            `json:"content"`
	ReasoningContent string            `json:"reasoning_content,omitempty"`
	Reasoning        string            `json:"reasoning,omitempty"`
	ToolCalls        []chatMessageTool `json:"tool_calls,omitempty"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type chatError struct {
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
	Code    string `json:"code,omitempty"`
}

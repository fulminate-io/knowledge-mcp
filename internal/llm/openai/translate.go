// SPDX-License-Identifier: Apache-2.0

package openai

import (
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// buildRequest translates messages + RequestOptions into the wire body.
//
// Fields the OpenAI Chat Completions API does NOT honor are noted inline:
//   - opts.TopK: OpenAI Chat Completions has no top_k knob; ignored. Some
//     OpenAI-compatible gateways (vLLM, Ollama) do accept it but the
//     baseline API does not, and we don't want to send a field the official
//     API will reject.
//   - opts.ExtendedThinking / opts.ThinkingBudget /
//     opts.DisableExtendedThinking: OpenAI does not expose Anthropic-style
//     extended thinking. Reasoning behavior on o-series models is
//     controlled implicitly via reasoning_effort instead. These fields are
//     intentionally not translated.
func buildRequest(model llm.Model, messages []*schema.Message, opts *llm.RequestOptions) (*chatRequest, error) {
	req := &chatRequest{
		Model: string(model),
	}

	wireMessages := make([]chatMessage, 0, len(messages)+1)
	if opts != nil && opts.SystemPrompt != "" {
		wireMessages = append(wireMessages, chatMessage{Role: "system", Content: opts.SystemPrompt})
	}
	for _, msg := range messages {
		converted, err := convertMessage(msg)
		if err != nil {
			return nil, err
		}
		wireMessages = append(wireMessages, converted)
	}
	req.Messages = wireMessages

	if opts == nil {
		return req, nil
	}

	if opts.Temperature != nil {
		req.Temperature = opts.Temperature
	}
	if opts.TopP != nil {
		req.TopP = opts.TopP
	}
	if opts.MaxTokens > 0 {
		req.MaxTokens = opts.MaxTokens
	}
	if len(opts.StopSequences) > 0 {
		req.Stop = opts.StopSequences
	}
	if opts.ReasoningEffort != "" {
		req.ReasoningEffort = opts.ReasoningEffort
	}

	if len(opts.Tools) > 0 {
		tools, err := convertTools(opts.Tools)
		if err != nil {
			return nil, err
		}
		req.Tools = tools
	}

	if opts.ResponseFormat != nil {
		rf, err := convertResponseFormat(opts.ResponseFormat)
		if err != nil {
			return nil, err
		}
		req.ResponseFormat = rf
	}

	return req, nil
}

// convertMessage translates one eino schema.Message to the OpenAI wire shape.
// Roles map directly: user/assistant/system/tool. Tool messages forward the
// tool_call_id and content; assistant messages forward any tool_calls verbatim.
func convertMessage(msg *schema.Message) (chatMessage, error) {
	if msg == nil {
		return chatMessage{}, &llm.LLMError{Transient: false, Reason: "config", Cause: fmt.Errorf("openai: nil message in conversation")}
	}

	out := chatMessage{
		Role:    string(msg.Role),
		Content: msg.Content,
		Name:    msg.Name,
	}

	switch msg.Role {
	case schema.User, schema.System:
		// content is the only meaningful payload.
	case schema.Assistant:
		if len(msg.ToolCalls) > 0 {
			out.ToolCalls = make([]chatMessageTool, len(msg.ToolCalls))
			for i, tc := range msg.ToolCalls {
				out.ToolCalls[i] = chatMessageTool{
					ID:   tc.ID,
					Type: "function",
					Function: chatMessageToolFunction{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				}
			}
		}
	case schema.Tool:
		out.ToolCallID = msg.ToolCallID
	default:
		return chatMessage{}, &llm.LLMError{Transient: false, Reason: "config", Cause: fmt.Errorf("openai: unsupported message role %q", msg.Role)}
	}

	return out, nil
}

// convertTools translates eino schema.ToolInfo into OpenAI's function-tool shape.
//
// OpenAI requires every function tool to expose a JSON-schema parameters
// object even when the tool takes no arguments — we always emit
// {"type":"object","properties":{}} as the floor.
func convertTools(tools []*schema.ToolInfo) ([]chatTool, error) {
	out := make([]chatTool, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		params, err := convertToolParams(tool)
		if err != nil {
			return nil, err
		}
		out = append(out, chatTool{
			Type: "function",
			Function: chatToolFunction{
				Name:        tool.Name,
				Description: tool.Desc,
				Parameters:  params,
			},
		})
	}
	return out, nil
}

// convertToolParams renders the tool's ParamsOneOf as a JSON-schema map.
// Round-trips ToJSONSchema through encoding/json so we get a canonical
// map[string]any representation without re-implementing the ordered-map
// walk locally. Empty schemas degrade to {type: object, properties: {}}.
func convertToolParams(tool *schema.ToolInfo) (map[string]any, error) {
	empty := map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
	if tool.ParamsOneOf == nil {
		return empty, nil
	}
	js, err := tool.ToJSONSchema()
	if err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "config", Cause: fmt.Errorf("openai: tool %q: %w", tool.Name, err)}
	}
	if js == nil {
		return empty, nil
	}
	raw, err := json.Marshal(js)
	if err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "config", Cause: fmt.Errorf("openai: marshal tool %q schema: %w", tool.Name, err)}
	}
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "config", Cause: fmt.Errorf("openai: parse tool %q schema: %w", tool.Name, err)}
	}
	if asMap == nil {
		return empty, nil
	}
	if _, ok := asMap["type"]; !ok {
		asMap["type"] = "object"
	}
	if _, ok := asMap["properties"]; !ok {
		asMap["properties"] = map[string]any{}
	}
	return asMap, nil
}

// convertResponseFormat translates llm.ResponseFormat to the OpenAI
// response_format wire shape. Two shapes are supported:
//
//   - "json_object" — request a free-form JSON object.
//   - "json_schema" — request strict JSON conforming to a schema; Schema is
//     marshaled to a json.RawMessage so any provider-side schema document
//     (map[string]any, *jsonschema.Schema, struct, raw bytes) round-trips.
//
// Other Type values are passed through verbatim so OpenAI-compatible
// gateways with extra format types are not blocked at the substrate.
func convertResponseFormat(rf *llm.ResponseFormat) (*responseFormat, error) {
	if rf == nil {
		return nil, nil
	}
	out := &responseFormat{Type: rf.Type}
	if rf.Type == "json_schema" {
		schemaBytes, err := marshalSchema(rf.Schema)
		if err != nil {
			return nil, err
		}
		out.JSONSchema = &jsonSchemaSpec{
			Name:   "response",
			Schema: schemaBytes,
			Strict: true,
		}
	}
	return out, nil
}

// marshalSchema accepts any of the shapes a caller might hand us
// (json.RawMessage, []byte, string, struct, map) and returns canonical JSON.
func marshalSchema(schema any) (json.RawMessage, error) {
	if schema == nil {
		return json.RawMessage(`{}`), nil
	}
	switch v := schema.(type) {
	case json.RawMessage:
		return v, nil
	case []byte:
		return json.RawMessage(v), nil
	case string:
		return json.RawMessage(v), nil
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "config", Cause: fmt.Errorf("openai: marshal response_format schema: %w", err)}
	}
	return raw, nil
}

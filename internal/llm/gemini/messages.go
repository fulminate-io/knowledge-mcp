package gemini

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

// translateMessages converts the eino message stream to Gemini contents.
// System-role messages are pulled out of the stream and joined into a
// single instruction string returned as the second value (newline-joined
// when there are multiple system messages — Gemini supports only one
// systemInstruction per request).
func translateMessages(messages []*schema.Message) ([]geminiContent, string, error) {
	out := make([]geminiContent, 0, len(messages))
	var systemTexts []string

	for i, m := range messages {
		if m == nil {
			return nil, "", fmt.Errorf("messages[%d] is nil", i)
		}
		switch m.Role {
		case schema.System:
			if m.Content != "" {
				systemTexts = append(systemTexts, m.Content)
			}
			continue
		case schema.User:
			parts, err := userParts(m)
			if err != nil {
				return nil, "", fmt.Errorf("messages[%d]: %w", i, err)
			}
			out = append(out, geminiContent{Role: "user", Parts: parts})
		case schema.Assistant:
			parts, err := assistantParts(m)
			if err != nil {
				return nil, "", fmt.Errorf("messages[%d]: %w", i, err)
			}
			out = append(out, geminiContent{Role: "model", Parts: parts})
		case schema.Tool:
			parts, err := toolParts(m)
			if err != nil {
				return nil, "", fmt.Errorf("messages[%d]: %w", i, err)
			}
			// Gemini expects tool/function-response turns under role "user".
			out = append(out, geminiContent{Role: "user", Parts: parts})
		default:
			return nil, "", fmt.Errorf("messages[%d]: unsupported role %q", i, m.Role)
		}
	}

	sys := ""
	switch len(systemTexts) {
	case 0:
		// no-op
	case 1:
		sys = systemTexts[0]
	default:
		// Multi-system: newline-join. Callers passing multiple system
		// messages get a deterministic concatenation rather than a
		// silent drop.
		var joined strings.Builder
		joined.WriteString(systemTexts[0])
		for _, s := range systemTexts[1:] {
			joined.WriteByte('\n')
			joined.WriteString(s)
		}
		sys = joined.String()
	}

	return out, sys, nil
}

func userParts(m *schema.Message) ([]geminiPart, error) {
	if m.Content == "" {
		return nil, fmt.Errorf("user message has empty content")
	}
	return []geminiPart{{Text: m.Content}}, nil
}

func assistantParts(m *schema.Message) ([]geminiPart, error) {
	parts := make([]geminiPart, 0, len(m.ToolCalls)+1)
	if m.Content != "" {
		parts = append(parts, geminiPart{Text: m.Content})
	}
	for _, tc := range m.ToolCalls {
		args := map[string]any{}
		if tc.Function.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				return nil, fmt.Errorf("tool call %q: parse arguments: %w", tc.Function.Name, err)
			}
		}
		parts = append(parts, geminiPart{
			FunctionCall: &geminiFunctionCall{
				Name: tc.Function.Name,
				Args: args,
			},
		})
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("assistant message has neither content nor tool calls")
	}
	return parts, nil
}

func toolParts(m *schema.Message) ([]geminiPart, error) {
	// eino tool messages carry the tool's name in m.Name (the tool's
	// declared name). Gemini's functionResponse requires the tool name
	// in `name`; we wrap the textual content into {response: {content: ...}}
	// when a JSON object isn't already provided.
	name := m.Name
	if name == "" {
		// Fallback: some callers populate ToolCallID with the tool name.
		// Gemini doesn't carry tool_call_id semantics, so we use the
		// best identifier we have.
		name = m.ToolCallID
	}
	if name == "" {
		return nil, fmt.Errorf("tool message missing name and tool_call_id")
	}

	// Try to parse the content as a JSON object first; if it parses,
	// pass the structured response through. Otherwise wrap as
	// {content: <raw text>} so Gemini still receives a valid object.
	resp := map[string]any{}
	if m.Content != "" {
		if err := json.Unmarshal([]byte(m.Content), &resp); err != nil {
			resp = map[string]any{"content": m.Content}
		}
	}

	return []geminiPart{{
		FunctionResponse: &geminiFunctionResponse{
			Name:     name,
			Response: resp,
		},
	}}, nil
}

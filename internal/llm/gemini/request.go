package gemini

import (
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// buildRequest translates the eino-shaped messages plus llm.RequestOptions
// into the Gemini wire body.
//
// Notes on knob coverage (mirrors RequestOptions field-by-field):
//   - Model: handled by the URL builder, not the body — see service.go.
//   - SystemPrompt: top-level systemInstruction.
//   - Tools: tools[].functionDeclarations[]; each ToolInfo's ParamsOneOf
//     is converted via ToJSONSchema and embedded as the parameters schema.
//   - ResponseFormat: JSON-mode (Type containing "json") sets
//     responseMimeType=application/json. If Schema is non-nil it is
//     marshaled as-is into responseSchema (callers pass either a
//     map[string]any, a struct, or a *jsonschema.Schema — anything that
//     json.Marshal can handle).
//   - Temperature/TopP/TopK/MaxTokens/StopSequences: generationConfig.
//   - ExtendedThinking: thinkingConfig.includeThoughts=true; budget
//     populated when ThinkingBudget>0.
//   - DisableExtendedThinking: thinkingConfig.thinkingBudget=0 (force off).
//   - ReasoningEffort: NOT SUPPORTED. Gemini's reasoning knob is a token
//     budget (thinkingBudget), not the OpenAI low/medium/high effort
//     bucket. Callers wanting reasoning on Gemini set
//     WithExtendedThinking(budget). Setting ReasoningEffort on a Gemini
//     request is silently ignored here; documenting the omission per the
//     llm.RequestOptions contract.
//   - BaseURL/APIKey: handled by the HTTP layer in service.go.
func buildRequest(messages []*schema.Message, opts *llm.RequestOptions) (*geminiRequest, error) {
	req := &geminiRequest{}

	contents, sysFromMsgs, err := translateMessages(messages)
	if err != nil {
		return nil, err
	}
	req.Contents = contents

	// Caller's WithSystemPrompt wins over a system role in the messages
	// slice. If both are set, the explicit option is the canonical
	// instruction; the in-message system text would otherwise be
	// duplicated. If only messages-system is set we use that.
	switch {
	case opts.SystemPrompt != "":
		req.SystemInstruction = &geminiContent{
			Parts: []geminiPart{{Text: opts.SystemPrompt}},
		}
	case sysFromMsgs != "":
		req.SystemInstruction = &geminiContent{
			Parts: []geminiPart{{Text: sysFromMsgs}},
		}
	}

	if len(opts.Tools) > 0 {
		decls, err := translateTools(opts.Tools)
		if err != nil {
			return nil, err
		}
		req.Tools = []geminiTool{{FunctionDeclarations: decls}}
	}

	cfg := buildGenerationConfig(opts)
	if cfg != nil {
		req.GenerationConfig = cfg
	}

	return req, nil
}

func translateTools(tools []*schema.ToolInfo) ([]geminiFunctionDeclaration, error) {
	out := make([]geminiFunctionDeclaration, 0, len(tools))
	for _, t := range tools {
		if t == nil {
			continue
		}
		decl := geminiFunctionDeclaration{
			Name:        t.Name,
			Description: t.Desc,
		}
		params, err := toolParameters(t)
		if err != nil {
			return nil, err
		}
		decl.Parameters = params
		out = append(out, decl)
	}
	return out, nil
}

// toolParameters renders an eino ToolInfo's parameters as a JSON-marshaled
// schema. Returns nil bytes when the tool exposes no parameter schema.
func toolParameters(t *schema.ToolInfo) (json.RawMessage, error) {
	if t.ParamsOneOf == nil {
		return nil, nil
	}
	sc, err := t.ToJSONSchema()
	if err != nil {
		return nil, fmt.Errorf("tool %q: convert params: %w", t.Name, err)
	}
	if sc == nil {
		return nil, nil
	}
	raw, err := json.Marshal(sc)
	if err != nil {
		return nil, fmt.Errorf("tool %q: marshal params: %w", t.Name, err)
	}
	return raw, nil
}

func buildGenerationConfig(opts *llm.RequestOptions) *geminiGenerationCfg {
	cfg := &geminiGenerationCfg{}
	dirty := false

	if opts.Temperature != nil {
		cfg.Temperature = opts.Temperature
		dirty = true
	}
	if opts.TopP != nil {
		cfg.TopP = opts.TopP
		dirty = true
	}
	if opts.TopK != nil {
		cfg.TopK = opts.TopK
		dirty = true
	}
	if opts.MaxTokens > 0 {
		cfg.MaxOutputTokens = opts.MaxTokens
		dirty = true
	}
	if len(opts.StopSequences) > 0 {
		cfg.StopSequences = opts.StopSequences
		dirty = true
	}

	if opts.ResponseFormat != nil {
		// JSON-mode response: set the MIME type. Schema, when supplied,
		// is marshaled directly. Gemini accepts JSON Schema documents
		// (subset of OpenAPI 3.0 schema) for responseSchema.
		cfg.ResponseMimeType = "application/json"
		if opts.ResponseFormat.Schema != nil {
			raw, err := json.Marshal(opts.ResponseFormat.Schema)
			if err == nil && len(raw) > 0 && string(raw) != "null" {
				cfg.ResponseSchema = raw
			}
		}
		dirty = true
	}

	if opts.DisableExtendedThinking {
		zero := 0
		cfg.ThinkingConfig = &geminiThinkingCfg{
			ThinkingBudget: &zero,
		}
		dirty = true
	} else if opts.ExtendedThinking {
		yes := true
		tc := &geminiThinkingCfg{IncludeThoughts: &yes}
		if opts.ThinkingBudget > 0 {
			b := opts.ThinkingBudget
			tc.ThinkingBudget = &b
		}
		cfg.ThinkingConfig = tc
		dirty = true
	}

	// ReasoningEffort: Gemini has no equivalent knob (it uses a numeric
	// thinking budget, not low/medium/high effort buckets). We
	// intentionally drop the value — see buildRequest godoc. No dirty
	// toggle here so the absence shows up correctly when only
	// ReasoningEffort is set.

	if !dirty {
		return nil
	}
	return cfg
}

package gemini

import "encoding/json"

// Wire shapes for the Gemini generateContent REST endpoint
// (generativelanguage.googleapis.com/v1beta/models/<model>:generateContent).
//
// We model the subset we actually populate. Fields we don't write are kept
// out of the struct to avoid emitting empty JSON keys that confuse the API.
// All optional fields use omitempty so a zero RequestOptions translation
// produces a minimal request body.
//
// Both request and response shapes live here; request-side and response-side
// translation logic live in request.go and response.go respectively.

type geminiRequest struct {
	Contents          []geminiContent      `json:"contents"`
	SystemInstruction *geminiContent       `json:"systemInstruction,omitempty"`
	Tools             []geminiTool         `json:"tools,omitempty"`
	GenerationConfig  *geminiGenerationCfg `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

// geminiPart is a one-of: exactly one of the field groups is populated per
// part. Gemini accepts a parts array where each entry carries text, a
// functionCall (model -> tool), or a functionResponse (tool -> model).
//
// Thought is response-only; Gemini sets it on reasoning parts when
// generationConfig.thinkingConfig.includeThoughts=true. It's always
// omitted on request bodies because callers don't author thought parts.
type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
}

type geminiFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type geminiFunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response,omitempty"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDeclaration `json:"functionDeclarations,omitempty"`
}

type geminiFunctionDeclaration struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Parameters is a JSON Schema document. We marshal whatever eino's
	// ToolInfo.ToJSONSchema produces; Gemini accepts standard JSON Schema
	// for function parameters.
	Parameters json.RawMessage `json:"parameters,omitempty"`
}

type geminiGenerationCfg struct {
	Temperature      *float32           `json:"temperature,omitempty"`
	TopP             *float32           `json:"topP,omitempty"`
	TopK             *int32             `json:"topK,omitempty"`
	MaxOutputTokens  int                `json:"maxOutputTokens,omitempty"`
	StopSequences    []string           `json:"stopSequences,omitempty"`
	ResponseMimeType string             `json:"responseMimeType,omitempty"`
	ResponseSchema   json.RawMessage    `json:"responseSchema,omitempty"`
	ThinkingConfig   *geminiThinkingCfg `json:"thinkingConfig,omitempty"`
}

type geminiThinkingCfg struct {
	// IncludeThoughts asks Gemini to emit reasoning parts (parts with
	// thought=true). Pointer so we can distinguish "not set" from "false".
	IncludeThoughts *bool `json:"includeThoughts,omitempty"`
	// ThinkingBudget caps the thinking-token budget. 0 forces thinking
	// off when supplied alongside DisableExtendedThinking. Pointer so
	// we can emit 0 explicitly without colliding with omitempty.
	ThinkingBudget *int `json:"thinkingBudget,omitempty"`
}

// Response shapes follow.

type geminiResponseBody struct {
	Candidates    []geminiCandidate    `json:"candidates"`
	UsageMetadata *geminiUsageMetadata `json:"usageMetadata,omitempty"`
	// PromptFeedback is set when the prompt itself was blocked; we
	// surface it as a parse error so callers see the failure mode.
	PromptFeedback *geminiPromptFeedback `json:"promptFeedback,omitempty"`
	ModelVersion   string                `json:"modelVersion,omitempty"`
}

type geminiCandidate struct {
	Content      *geminiContent `json:"content,omitempty"`
	FinishReason string         `json:"finishReason,omitempty"`
	Index        int            `json:"index,omitempty"`
}

type geminiUsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount,omitempty"`
	CandidatesTokenCount int `json:"candidatesTokenCount,omitempty"`
	// Other fields (totalTokenCount, thoughtsTokenCount, cachedContentTokenCount)
	// exist in the wire but we don't surface them on TokenUsage; the
	// substrate keeps usage to input/output only.
}

type geminiPromptFeedback struct {
	BlockReason string `json:"blockReason,omitempty"`
}

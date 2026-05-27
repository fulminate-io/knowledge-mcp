package gemini

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// Tests in this file pin the *request body* shape: how llm.RequestOptions
// + eino messages translate into the Gemini wire format. They use the
// roundTripFunc helper from gemini_test.go to capture the body.

// --- System prompt + generation knobs ---------------------------------

func TestGenerate_SystemPromptAndKnobs(t *testing.T) {
	var capturedReq geminiRequest

	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		_ = json.Unmarshal(mustReadBody(t, r), &capturedReq)
		return canned(http.StatusOK, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`), nil
	})

	svc := newServiceWithRT(t, rt)
	temp := float32(0.4)
	topP := float32(0.9)
	topK := int32(40)
	_, err := svc.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "do the thing"},
	},
		llm.WithSystemPrompt("You are terse."),
		llm.WithTemperature(temp),
		llm.WithTopP(topP),
		llm.WithTopK(topK),
		llm.WithMaxTokens(256),
		llm.WithStopSequences("END", "STOP_NOW"),
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if capturedReq.SystemInstruction == nil || capturedReq.SystemInstruction.Parts[0].Text != "You are terse." {
		t.Errorf("systemInstruction missing or wrong: %+v", capturedReq.SystemInstruction)
	}
	cfg := capturedReq.GenerationConfig
	if cfg == nil {
		t.Fatalf("generationConfig nil")
	}
	if cfg.Temperature == nil || *cfg.Temperature != temp {
		t.Errorf("temperature: %+v", cfg.Temperature)
	}
	if cfg.TopP == nil || *cfg.TopP != topP {
		t.Errorf("topP: %+v", cfg.TopP)
	}
	if cfg.TopK == nil || *cfg.TopK != topK {
		t.Errorf("topK: %+v", cfg.TopK)
	}
	if cfg.MaxOutputTokens != 256 {
		t.Errorf("maxOutputTokens = %d", cfg.MaxOutputTokens)
	}
	if len(cfg.StopSequences) != 2 || cfg.StopSequences[0] != "END" {
		t.Errorf("stopSequences = %v", cfg.StopSequences)
	}
}

// --- Tools -------------------------------------------------------------

func TestGenerate_ToolsRoundTrip(t *testing.T) {
	var capturedReq geminiRequest

	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		_ = json.Unmarshal(mustReadBody(t, r), &capturedReq)
		// Model emits a functionCall as the response.
		return canned(http.StatusOK, `{
			"candidates":[{"content":{"role":"model","parts":[
				{"functionCall":{"name":"get_weather","args":{"city":"sf","units":"celsius"}}}
			]},"finishReason":"STOP"}],
			"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}
		}`), nil
	})

	svc := newServiceWithRT(t, rt)

	params := map[string]*schema.ParameterInfo{
		"city":  {Type: schema.String, Required: true, Desc: "city name"},
		"units": {Type: schema.String, Required: false, Desc: "celsius|fahrenheit"},
	}
	tool := &schema.ToolInfo{
		Name:        "get_weather",
		Desc:        "Look up the weather",
		ParamsOneOf: schema.NewParamsOneOfByParams(params),
	}

	resp, err := svc.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "weather in sf?"},
	}, llm.WithTools([]*schema.ToolInfo{tool}))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if len(capturedReq.Tools) != 1 || len(capturedReq.Tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("functionDeclarations not emitted: %+v", capturedReq.Tools)
	}
	decl := capturedReq.Tools[0].FunctionDeclarations[0]
	if decl.Name != "get_weather" || decl.Description != "Look up the weather" {
		t.Errorf("function decl = %+v", decl)
	}
	if len(decl.Parameters) == 0 {
		t.Errorf("parameters JSON should be populated")
	} else {
		// Ensure it parses as JSON (don't pin exact shape — eino's
		// schema converter owns the structure).
		var anyMap map[string]any
		if err := json.Unmarshal(decl.Parameters, &anyMap); err != nil {
			t.Errorf("parameters not valid json: %v", err)
		}
	}

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.Function.Name != "get_weather" {
		t.Errorf("tool call name = %q", tc.Function.Name)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		t.Fatalf("tool call args not json: %v", err)
	}
	if args["city"] != "sf" || args["units"] != "celsius" {
		t.Errorf("args = %v", args)
	}
	if resp.FinishReason != llm.FinishReasonToolUse {
		t.Errorf("FinishReason should be tool_use when functionCall present, got %q", resp.FinishReason)
	}
}

func TestGenerate_ToolResultMessage(t *testing.T) {
	var capturedReq geminiRequest

	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		_ = json.Unmarshal(mustReadBody(t, r), &capturedReq)
		return canned(http.StatusOK, `{"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]},"finishReason":"STOP"}]}`), nil
	})

	svc := newServiceWithRT(t, rt)
	_, err := svc.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "weather?"},
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:       "call-1",
				Type:     "function",
				Function: schema.FunctionCall{Name: "get_weather", Arguments: `{"city":"sf"}`},
			}},
		},
		{
			Role:       schema.Tool,
			Name:       "get_weather",
			ToolCallID: "call-1",
			Content:    `{"temperature":21}`,
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if len(capturedReq.Contents) != 3 {
		t.Fatalf("expected 3 contents (user/model/tool-as-user), got %d", len(capturedReq.Contents))
	}
	model := capturedReq.Contents[1]
	if model.Role != "model" || model.Parts[0].FunctionCall == nil || model.Parts[0].FunctionCall.Name != "get_weather" {
		t.Errorf("assistant tool call not translated: %+v", model)
	}
	if model.Parts[0].FunctionCall.Args["city"] != "sf" {
		t.Errorf("assistant tool args = %v", model.Parts[0].FunctionCall.Args)
	}
	toolTurn := capturedReq.Contents[2]
	if toolTurn.Role != "user" || toolTurn.Parts[0].FunctionResponse == nil {
		t.Errorf("tool turn missing functionResponse: %+v", toolTurn)
	}
	if toolTurn.Parts[0].FunctionResponse.Name != "get_weather" {
		t.Errorf("functionResponse name = %q", toolTurn.Parts[0].FunctionResponse.Name)
	}
	if got := toolTurn.Parts[0].FunctionResponse.Response["temperature"]; got != float64(21) {
		t.Errorf("functionResponse content = %v", toolTurn.Parts[0].FunctionResponse.Response)
	}
}

// --- Extended thinking -------------------------------------------------

func TestGenerate_ExtendedThinking(t *testing.T) {
	var capturedReq geminiRequest

	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		_ = json.Unmarshal(mustReadBody(t, r), &capturedReq)
		return canned(http.StatusOK, `{
			"candidates":[{"content":{"role":"model","parts":[
				{"thought":true,"text":"thinking step"},
				{"text":"final answer"}
			]},"finishReason":"STOP"}]
		}`), nil
	})

	svc := newServiceWithRT(t, rt)
	resp, err := svc.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "think hard"},
	}, llm.WithExtendedThinking(2048))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	cfg := capturedReq.GenerationConfig
	if cfg == nil || cfg.ThinkingConfig == nil {
		t.Fatalf("thinkingConfig missing: %+v", capturedReq.GenerationConfig)
	}
	if cfg.ThinkingConfig.IncludeThoughts == nil || !*cfg.ThinkingConfig.IncludeThoughts {
		t.Errorf("includeThoughts != true: %+v", cfg.ThinkingConfig.IncludeThoughts)
	}
	if cfg.ThinkingConfig.ThinkingBudget == nil || *cfg.ThinkingConfig.ThinkingBudget != 2048 {
		t.Errorf("thinkingBudget != 2048: %+v", cfg.ThinkingConfig.ThinkingBudget)
	}

	if resp.ReasoningContent != "thinking step" {
		t.Errorf("ReasoningContent = %q", resp.ReasoningContent)
	}
	if resp.Content != "final answer" {
		t.Errorf("Content = %q", resp.Content)
	}
}

func TestGenerate_DisableExtendedThinking(t *testing.T) {
	var capturedReq geminiRequest

	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		_ = json.Unmarshal(mustReadBody(t, r), &capturedReq)
		return canned(http.StatusOK, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`), nil
	})

	svc := newServiceWithRT(t, rt)
	_, err := svc.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "go"},
	}, llm.WithDisableExtendedThinking())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	cfg := capturedReq.GenerationConfig
	if cfg == nil || cfg.ThinkingConfig == nil {
		t.Fatalf("thinkingConfig missing: %+v", capturedReq.GenerationConfig)
	}
	if cfg.ThinkingConfig.ThinkingBudget == nil || *cfg.ThinkingConfig.ThinkingBudget != 0 {
		t.Errorf("disable should set thinkingBudget=0, got %+v", cfg.ThinkingConfig.ThinkingBudget)
	}
	if cfg.ThinkingConfig.IncludeThoughts != nil {
		t.Errorf("disable should NOT set includeThoughts: %+v", cfg.ThinkingConfig.IncludeThoughts)
	}
}

// --- Response format / JSON mode ---------------------------------------

func TestGenerate_ResponseFormatJSON(t *testing.T) {
	var capturedReq geminiRequest

	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		_ = json.Unmarshal(mustReadBody(t, r), &capturedReq)
		return canned(http.StatusOK, `{"candidates":[{"content":{"role":"model","parts":[{"text":"{\"x\":1}"}]},"finishReason":"STOP"}]}`), nil
	})

	svc := newServiceWithRT(t, rt)
	schemaDoc := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"x": map[string]any{"type": "integer"},
		},
		"required": []string{"x"},
	}
	_, err := svc.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "give me x"},
	}, llm.WithResponseFormat(&llm.ResponseFormat{
		Type:   "json_schema",
		Schema: schemaDoc,
	}))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	cfg := capturedReq.GenerationConfig
	if cfg == nil || cfg.ResponseMimeType != "application/json" {
		t.Errorf("responseMimeType: %+v", cfg)
	}
	if len(cfg.ResponseSchema) == 0 {
		t.Fatalf("responseSchema not populated")
	}
	var roundTrip map[string]any
	if err := json.Unmarshal(cfg.ResponseSchema, &roundTrip); err != nil {
		t.Fatalf("responseSchema not valid json: %v", err)
	}
	if roundTrip["type"] != "object" {
		t.Errorf("schema type = %v", roundTrip["type"])
	}
}

// --- ReasoningEffort silently ignored (Gemini has no equivalent) ------

func TestGenerate_ReasoningEffortIgnored(t *testing.T) {
	var capturedReq geminiRequest

	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		_ = json.Unmarshal(mustReadBody(t, r), &capturedReq)
		return canned(http.StatusOK, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`), nil
	})

	svc := newServiceWithRT(t, rt)
	_, err := svc.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "x"},
	}, llm.WithReasoningEffort("high"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// Setting only ReasoningEffort should not produce a generationConfig
	// (no other knobs are set). This documents the omission behavior.
	if capturedReq.GenerationConfig != nil {
		t.Errorf("generationConfig should be nil when only ReasoningEffort is set, got %+v", capturedReq.GenerationConfig)
	}
}

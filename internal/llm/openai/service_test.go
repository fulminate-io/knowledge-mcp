// SPDX-License-Identifier: Apache-2.0

package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// newTestService builds a Service whose HTTP client is targeted at srv. We
// don't use NewService here because we want to inject the test transport.
// init()-side registration is exercised in TestRegistration_RegistersFactory.
func newTestService(t *testing.T, srv *httptest.Server, model string) *Service {
	t.Helper()
	cfg := &llm.Config{
		Provider: llm.ProviderOpenAI,
		APIKey:   "sk-test",
		BaseURL:  srv.URL,
		Model:    llm.Model(model),
	}
	svc, err := NewService(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	openaiSvc, ok := svc.(*Service)
	if !ok {
		t.Fatalf("NewService returned %T, want *Service", svc)
	}
	openaiSvc.client = srv.Client()
	return openaiSvc
}

// readJSONRequest decodes the JSON body of an httptest request into target
// while leaving room for assertions on header/method.
func readJSONRequest(t *testing.T, r *http.Request, target any) {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatalf("decode request body: %v\nbody: %s", err, string(body))
	}
}

func TestRegistration_RegistersFactory(t *testing.T) {
	if !llm.HasProvider(llm.ProviderOpenAI) {
		t.Fatal("init() did not register openai provider")
	}
	cfg := &llm.Config{
		Provider: llm.ProviderOpenAI,
		APIKey:   "sk-test",
		Model:    "gpt-5-mini",
	}
	client, err := llm.NewClient(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, ok := client.(*Service); !ok {
		t.Fatalf("NewClient returned %T, want *openai.Service", client)
	}
}

func TestGenerate_HappyPath_TextResponse(t *testing.T) {
	var captured chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != chatCompletionsPath {
			t.Errorf("path = %s, want %s", r.URL.Path, chatCompletionsPath)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer sk-test")
		}
		readJSONRequest(t, r, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id": "chatcmpl-1",
			"model": "gpt-5-mini",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "hello, world"},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 12, "completion_tokens": 4, "total_tokens": 16}
		}`)
	}))
	defer srv.Close()

	svc := newTestService(t, srv, "gpt-5-mini")
	temp := float32(0.2)
	topP := float32(0.95)
	resp, err := svc.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hi"}},
		llm.WithSystemPrompt("be brief"),
		llm.WithTemperature(temp),
		llm.WithTopP(topP),
		llm.WithMaxTokens(100),
		llm.WithStopSequences("END"),
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if resp.Content != "hello, world" {
		t.Errorf("Content = %q, want %q", resp.Content, "hello, world")
	}
	if resp.FinishReason != llm.FinishReasonEndTurn {
		t.Errorf("FinishReason = %q, want end_turn", resp.FinishReason)
	}
	if resp.Provider != llm.ProviderOpenAI {
		t.Errorf("Provider = %q, want openai", resp.Provider)
	}
	if resp.Model != "gpt-5-mini" {
		t.Errorf("Model = %q, want gpt-5-mini", resp.Model)
	}
	if resp.Usage.InputTokens != 12 || resp.Usage.OutputTokens != 4 {
		t.Errorf("Usage = %+v, want {12, 4}", resp.Usage)
	}
	if len(resp.Raw) == 0 {
		t.Error("Raw is empty; expected decoded HTTP body")
	}

	if captured.Model != "gpt-5-mini" {
		t.Errorf("request Model = %q, want gpt-5-mini", captured.Model)
	}
	if len(captured.Messages) != 2 || captured.Messages[0].Role != "system" || captured.Messages[1].Role != "user" {
		t.Errorf("messages roles = %+v, want [system user]", captured.Messages)
	}
	if captured.Temperature == nil || *captured.Temperature != 0.2 {
		t.Errorf("Temperature = %v, want 0.2", captured.Temperature)
	}
	if captured.TopP == nil || *captured.TopP != 0.95 {
		t.Errorf("TopP = %v, want 0.95", captured.TopP)
	}
	if captured.MaxTokens != 100 {
		t.Errorf("MaxTokens = %d, want 100", captured.MaxTokens)
	}
	if len(captured.Stop) != 1 || captured.Stop[0] != "END" {
		t.Errorf("Stop = %v, want [END]", captured.Stop)
	}

	// Aggregate usage should accumulate across calls.
	if got := svc.GetUsage(); got.InputTokens != 12 || got.OutputTokens != 4 {
		t.Errorf("BaseService usage = %+v, want {12, 4}", got)
	}
}

func TestGenerate_ToolCallResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		readJSONRequest(t, r, &req)
		if len(req.Tools) != 1 || req.Tools[0].Function.Name != "lookup" {
			t.Errorf("tools = %+v, want one lookup tool", req.Tools)
		}
		params := req.Tools[0].Function.Parameters
		if params["type"] != "object" {
			t.Errorf("tool params type = %v, want object", params["type"])
		}
		if _, ok := params["properties"]; !ok {
			t.Error("tool params missing properties")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id": "chatcmpl-2",
			"model": "gpt-5-mini",
			"choices": [{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": "",
					"tool_calls": [{
						"id": "call_1",
						"type": "function",
						"function": {"name": "lookup", "arguments": "{\"q\":\"foo\"}"}
					}]
				},
				"finish_reason": "tool_calls"
			}],
			"usage": {"prompt_tokens": 8, "completion_tokens": 2, "total_tokens": 10}
		}`)
	}))
	defer srv.Close()

	svc := newTestService(t, srv, "gpt-5-mini")
	tools := []*schema.ToolInfo{{
		Name: "lookup",
		Desc: "look up a value",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"q": {Type: schema.String, Desc: "query", Required: true},
		}),
	}}
	resp, err := svc.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "find foo"}},
		llm.WithTools(tools),
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.FinishReason != llm.FinishReasonToolUse {
		t.Errorf("FinishReason = %q, want tool_use", resp.FinishReason)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_1" || tc.Function.Name != "lookup" || tc.Function.Arguments != `{"q":"foo"}` {
		t.Errorf("ToolCall = %+v, want call_1/lookup/{q:foo}", tc)
	}
}

func TestGenerate_AssistantMessageWithToolCalls_RoundTrips(t *testing.T) {
	var captured chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readJSONRequest(t, r, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id": "x", "model": "m",
			"choices": [{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage": {"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`)
	}))
	defer srv.Close()

	svc := newTestService(t, srv, "gpt-5-mini")
	prior := []*schema.Message{
		{Role: schema.User, Content: "find foo"},
		{Role: schema.Assistant, Content: "", ToolCalls: []schema.ToolCall{{
			ID: "call_1", Type: "function", Function: schema.FunctionCall{Name: "lookup", Arguments: `{"q":"foo"}`},
		}}},
		{Role: schema.Tool, Content: "result-payload", ToolCallID: "call_1"},
	}
	if _, err := svc.Generate(context.Background(), prior); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(captured.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(captured.Messages))
	}
	asst := captured.Messages[1]
	if asst.Role != "assistant" || len(asst.ToolCalls) != 1 || asst.ToolCalls[0].ID != "call_1" {
		t.Errorf("assistant turn = %+v, want one tool_call call_1", asst)
	}
	tool := captured.Messages[2]
	if tool.Role != "tool" || tool.ToolCallID != "call_1" || tool.Content != "result-payload" {
		t.Errorf("tool turn = %+v, want tool/call_1/result-payload", tool)
	}
}

func TestGenerate_ReasoningEffort_PropagatesAndCapturesReasoning(t *testing.T) {
	var captured chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readJSONRequest(t, r, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"r","model":"o3",
			"choices":[{"index":0,"message":{"role":"assistant","content":"answer","reasoning_content":"step1\nstep2"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}
		}`)
	}))
	defer srv.Close()

	svc := newTestService(t, srv, "o3")
	resp, err := svc.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "think"}},
		llm.WithReasoningEffort("high"),
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if captured.ReasoningEffort != "high" {
		t.Errorf("reasoning_effort = %q, want high", captured.ReasoningEffort)
	}
	if resp.ReasoningContent != "step1\nstep2" {
		t.Errorf("ReasoningContent = %q, want step1\\nstep2", resp.ReasoningContent)
	}
}

func TestGenerate_ResponseFormat_JSONSchema(t *testing.T) {
	var captured chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readJSONRequest(t, r, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"j","model":"gpt-5-mini",
			"choices":[{"index":0,"message":{"role":"assistant","content":"{\"v\":1}"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`)
	}))
	defer srv.Close()

	svc := newTestService(t, srv, "gpt-5-mini")
	schemaDoc := map[string]any{
		"type":       "object",
		"properties": map[string]any{"v": map[string]any{"type": "integer"}},
	}
	if _, err := svc.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "produce"}},
		llm.WithResponseFormat(&llm.ResponseFormat{Type: "json_schema", Schema: schemaDoc}),
	); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if captured.ResponseFormat == nil || captured.ResponseFormat.Type != "json_schema" {
		t.Fatalf("response_format = %+v, want json_schema", captured.ResponseFormat)
	}
	if captured.ResponseFormat.JSONSchema == nil || captured.ResponseFormat.JSONSchema.Strict != true {
		t.Errorf("json_schema spec = %+v, want strict=true", captured.ResponseFormat.JSONSchema)
	}
	if !strings.Contains(string(captured.ResponseFormat.JSONSchema.Schema), `"v"`) {
		t.Errorf("schema bytes = %s, want contains 'v' field", string(captured.ResponseFormat.JSONSchema.Schema))
	}
}

func TestGenerate_HTTP429_IsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
	}))
	defer srv.Close()

	svc := newTestService(t, srv, "gpt-5-mini")
	_, err := svc.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hi"}})
	if err == nil {
		t.Fatal("expected error from 429")
	}
	var le *llm.LLMError
	if !errors.As(err, &le) {
		t.Fatalf("err type = %T, want *llm.LLMError", err)
	}
	if !le.Transient {
		t.Errorf("Transient = false, want true (HTTP 429)")
	}
	if le.Reason != "http_429" {
		t.Errorf("Reason = %q, want http_429", le.Reason)
	}
	if !llm.IsTransient(err) {
		t.Error("llm.IsTransient(err) = false, want true")
	}
}

func TestGenerate_HTTP400_IsTerminal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"bad request"}}`)
	}))
	defer srv.Close()

	svc := newTestService(t, srv, "gpt-5-mini")
	_, err := svc.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hi"}})
	if err == nil {
		t.Fatal("expected error from 400")
	}
	if llm.IsTransient(err) {
		t.Errorf("IsTransient = true, want false (HTTP 400 is terminal)")
	}
}

func TestGenerate_APIErrorField_IsTerminal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"error":{"message":"model not found","code":"model_not_found"}}`)
	}))
	defer srv.Close()

	svc := newTestService(t, srv, "gpt-5-mini")
	_, err := svc.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hi"}})
	if err == nil {
		t.Fatal("expected error from API error field")
	}
	var le *llm.LLMError
	if !errors.As(err, &le) {
		t.Fatalf("err type = %T, want *llm.LLMError", err)
	}
	if le.Reason != "openai_api_error" {
		t.Errorf("Reason = %q, want openai_api_error", le.Reason)
	}
	if le.Transient {
		t.Errorf("Transient = true, want false (API error is terminal)")
	}
}

func TestGenerate_NoChoices_IsTerminal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`)
	}))
	defer srv.Close()

	svc := newTestService(t, srv, "gpt-5-mini")
	_, err := svc.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hi"}})
	if err == nil {
		t.Fatal("expected error from empty choices")
	}
	var le *llm.LLMError
	if !errors.As(err, &le) || le.Reason != "no_choices" {
		t.Errorf("err = %v, want no_choices", err)
	}
}

func TestGenerate_MissingModel_IsTerminal(t *testing.T) {
	cfg := &llm.Config{Provider: llm.ProviderOpenAI, APIKey: "sk-test"}
	svc, err := NewService(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	_, err = svc.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hi"}})
	if err == nil {
		t.Fatal("expected error when no model is configured")
	}
	var le *llm.LLMError
	if !errors.As(err, &le) || le.Reason != "config" {
		t.Errorf("err = %v, want config error", err)
	}
}

func TestGenerate_PerCallOverrides_BaseURLAndAPIKey(t *testing.T) {
	gotKey := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"o","model":"gpt-5-mini",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`)
	}))
	defer srv.Close()

	// cfg has a different base + key; per-call options should override.
	cfg := &llm.Config{Provider: llm.ProviderOpenAI, APIKey: "sk-cfg", BaseURL: "http://invalid.invalid", Model: "gpt-5-mini"}
	svc, err := NewService(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	openaiSvc := svc.(*Service)
	openaiSvc.client = srv.Client()

	if _, err := svc.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hi"}},
		llm.WithBaseURL(srv.URL),
		llm.WithAPIKey("sk-percall"),
	); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if gotKey != "Bearer sk-percall" {
		t.Errorf("Authorization = %q, want Bearer sk-percall", gotKey)
	}
}

func TestMapFinishReason(t *testing.T) {
	cases := map[string]llm.FinishReason{
		"stop":           llm.FinishReasonEndTurn,
		"length":         llm.FinishReasonMaxTokens,
		"tool_calls":     llm.FinishReasonToolUse,
		"function_call":  llm.FinishReasonToolUse,
		"content_filter": llm.FinishReasonOther,
		"":               llm.FinishReasonOther,
		"weird":          llm.FinishReasonOther,
	}
	for raw, want := range cases {
		if got := mapFinishReason(raw); got != want {
			t.Errorf("mapFinishReason(%q) = %q, want %q", raw, got, want)
		}
	}
}

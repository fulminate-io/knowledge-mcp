// SPDX-License-Identifier: Apache-2.0

package anthropic

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

// captured holds the request the fake Anthropic server saw. Tests assert
// against this to verify translation correctness.
type captured struct {
	method  string
	path    string
	headers http.Header
	body    json.RawMessage
}

// newFakeServer stands up an httptest.Server that records the incoming
// request and replies with respBody under respStatus. Returns the server,
// a *captured pointer the test can read after Generate returns, and a
// cleanup func.
func newFakeServer(t *testing.T, respStatus int, respBody string) (*httptest.Server, *captured) {
	t.Helper()
	cap := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.headers = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		cap.body = json.RawMessage(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(respStatus)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

// withClient builds a Service backed by an httptest.Server. Tests pass
// the server URL as the BaseURL so requests hit the fake.
func withClient(t *testing.T, srv *httptest.Server, defaultModel llm.Model) *Service {
	t.Helper()
	return New("test-key", srv.URL, defaultModel, srv.Client())
}

func TestRegistered(t *testing.T) {
	if !llm.HasProvider(llm.ProviderAnthropic) {
		t.Fatalf("anthropic provider not registered after import")
	}
}

func TestNewClientFromConfig(t *testing.T) {
	cfg := &llm.Config{
		Provider: llm.ProviderAnthropic,
		APIKey:   "sk-ant-test",
		Model:    "claude-3-haiku-20240307",
	}
	c, err := llm.NewClient(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	svc, ok := c.(*Service)
	if !ok {
		t.Fatalf("expected *Service, got %T", c)
	}
	if svc.Provider() != llm.ProviderAnthropic {
		t.Fatalf("Provider() = %q, want %q", svc.Provider(), llm.ProviderAnthropic)
	}
	if svc.defaultModel != llm.Model("claude-3-haiku-20240307") {
		t.Fatalf("defaultModel = %q, want claude-3-haiku-20240307", svc.defaultModel)
	}
}

func TestClientInterfaceImpl(t *testing.T) {
	// Compile-time check is in anthropic.go via `var _ llm.Client = (*Service)(nil)`.
	// A runtime check guards against any future indirection that erases the
	// interface conformance.
	var _ llm.Client = New("k", "", "", nil)
}

func TestGenerateBasic(t *testing.T) {
	srv, cap := newFakeServer(t, http.StatusOK, `{
		"id": "msg_01",
		"type": "message",
		"role": "assistant",
		"model": "claude-3-haiku-20240307",
		"content": [{"type":"text","text":"hello back"}],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 12, "output_tokens": 5}
	}`)
	svc := withClient(t, srv, "claude-3-haiku-20240307")

	resp, err := svc.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hello"}},
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Content != "hello back" {
		t.Errorf("Content = %q, want %q", resp.Content, "hello back")
	}
	if resp.FinishReason != llm.FinishReasonEndTurn {
		t.Errorf("FinishReason = %q, want end_turn", resp.FinishReason)
	}
	if resp.Usage.InputTokens != 12 || resp.Usage.OutputTokens != 5 {
		t.Errorf("Usage = %+v, want {12,5}", resp.Usage)
	}
	if resp.Provider != llm.ProviderAnthropic {
		t.Errorf("Provider = %q, want anthropic", resp.Provider)
	}
	if len(resp.Raw) == 0 {
		t.Errorf("Raw body should be populated for diagnostics")
	}
	if cap.method != http.MethodPost {
		t.Errorf("method = %q, want POST", cap.method)
	}
	if cap.path != "/v1/messages" {
		t.Errorf("path = %q, want /v1/messages", cap.path)
	}
	if cap.headers.Get("x-api-key") != "test-key" {
		t.Errorf("x-api-key header = %q, want test-key", cap.headers.Get("x-api-key"))
	}
	if cap.headers.Get("anthropic-version") == "" {
		t.Errorf("anthropic-version header missing")
	}

	// Verify usage was tracked on the BaseService.
	tot := svc.GetUsage()
	if tot.InputTokens != 12 || tot.OutputTokens != 5 {
		t.Errorf("BaseService usage = %+v, want {12,5}", tot)
	}
}

func TestGenerateRequiresModel(t *testing.T) {
	srv, _ := newFakeServer(t, http.StatusOK, `{}`)
	svc := withClient(t, srv, "")
	_, err := svc.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hi"}},
	)
	if err == nil {
		t.Fatalf("expected error when no default model and no WithModel")
	}
	if !errors.Is(err, llm.ErrInvalidConfig) {
		t.Errorf("err = %v, want ErrInvalidConfig wrap", err)
	}
}

func TestGenerateSystemAndOptions(t *testing.T) {
	srv, cap := newFakeServer(t, http.StatusOK, `{
		"id":"msg_02","type":"message","role":"assistant","model":"claude-x",
		"content":[{"type":"text","text":"ok"}],
		"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}
	}`)
	svc := withClient(t, srv, "claude-x")

	temp := float32(0.3)
	topP := float32(0.9)
	topK := int32(40)
	_, err := svc.Generate(context.Background(),
		[]*schema.Message{
			{Role: schema.System, Content: "you are concise"},
			{Role: schema.User, Content: "hi"},
		},
		llm.WithSystemPrompt("be helpful"),
		llm.WithTemperature(temp),
		llm.WithTopP(topP),
		llm.WithTopK(topK),
		llm.WithMaxTokens(256),
		llm.WithStopSequences("STOP"),
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	var sent anthropicRequest
	if err := json.Unmarshal(cap.body, &sent); err != nil {
		t.Fatalf("unmarshal sent body: %v", err)
	}
	// SystemPrompt option layers in front of the schema.System message.
	if !strings.Contains(sent.System, "be helpful") || !strings.Contains(sent.System, "you are concise") {
		t.Errorf("system = %q, missing layered prompts", sent.System)
	}
	if sent.Temperature == nil || *sent.Temperature != temp {
		t.Errorf("Temperature = %v, want %v", sent.Temperature, temp)
	}
	if sent.TopP == nil || *sent.TopP != topP {
		t.Errorf("TopP = %v, want %v", sent.TopP, topP)
	}
	if sent.TopK == nil || *sent.TopK != topK {
		t.Errorf("TopK = %v, want %v", sent.TopK, topK)
	}
	if sent.MaxTokens != 256 {
		t.Errorf("MaxTokens = %d, want 256", sent.MaxTokens)
	}
	if len(sent.StopSequences) != 1 || sent.StopSequences[0] != "STOP" {
		t.Errorf("StopSequences = %v, want [STOP]", sent.StopSequences)
	}
	if sent.Thinking != nil {
		t.Errorf("Thinking should be nil when not enabled")
	}
}

func TestGenerateExtendedThinking(t *testing.T) {
	srv, cap := newFakeServer(t, http.StatusOK, `{
		"id":"msg_03","type":"message","role":"assistant","model":"claude-x",
		"content":[
			{"type":"thinking","thinking":"let me think"},
			{"type":"text","text":"answer"}
		],
		"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}
	}`)
	svc := withClient(t, srv, "claude-x")

	temp := float32(0.5) // should be dropped because thinking forces temp=1 default
	resp, err := svc.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "deep q"}},
		llm.WithTemperature(temp),
		llm.WithExtendedThinking(2048),
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.ThinkingContent != "let me think" {
		t.Errorf("ThinkingContent = %q, want %q", resp.ThinkingContent, "let me think")
	}
	if resp.Content != "answer" {
		t.Errorf("Content = %q, want %q", resp.Content, "answer")
	}

	var sent anthropicRequest
	_ = json.Unmarshal(cap.body, &sent)
	if sent.Thinking == nil || sent.Thinking.Type != "enabled" || sent.Thinking.BudgetTokens != 2048 {
		t.Errorf("Thinking = %+v, want {enabled, 2048}", sent.Thinking)
	}
	if sent.MaxTokens <= 2048 {
		t.Errorf("MaxTokens = %d, must exceed thinking budget 2048", sent.MaxTokens)
	}
	if sent.Temperature != nil {
		t.Errorf("Temperature should be dropped when thinking is on, got %v", *sent.Temperature)
	}
}

func TestGenerateDisableThinking(t *testing.T) {
	srv, cap := newFakeServer(t, http.StatusOK, `{
		"id":"msg_04","type":"message","role":"assistant","model":"claude-x",
		"content":[{"type":"text","text":"ok"}],
		"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}
	}`)
	svc := withClient(t, srv, "claude-x")
	_, err := svc.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "q"}},
		llm.WithExtendedThinking(2048),
		llm.WithDisableExtendedThinking(),
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var sent anthropicRequest
	_ = json.Unmarshal(cap.body, &sent)
	if sent.Thinking != nil {
		t.Errorf("Thinking should be nil when DisableExtendedThinking is set, got %+v", sent.Thinking)
	}
}

func TestGenerateOverrideAPIKeyAndBaseURL(t *testing.T) {
	srv, cap := newFakeServer(t, http.StatusOK, `{
		"id":"msg_06","type":"message","role":"assistant","model":"claude-x",
		"content":[{"type":"text","text":"ok"}],
		"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}
	}`)
	// Default service points at a wrong URL with a different key — the
	// option overrides should take precedence.
	svc := New("default-key", "http://wrong.invalid", "claude-x", srv.Client())

	_, err := svc.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hi"}},
		llm.WithAPIKey("override-key"),
		llm.WithBaseURL(srv.URL),
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if cap.headers.Get("x-api-key") != "override-key" {
		t.Errorf("x-api-key header = %q, want override-key", cap.headers.Get("x-api-key"))
	}
	if cap.path != "/v1/messages" {
		t.Errorf("path = %q, want /v1/messages", cap.path)
	}
}

func TestGenerateModelOption(t *testing.T) {
	srv, cap := newFakeServer(t, http.StatusOK, `{
		"id":"x","type":"message","role":"assistant","model":"override",
		"content":[{"type":"text","text":"ok"}],
		"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}
	}`)
	// No default model on the service — WithModel must satisfy the requirement.
	svc := withClient(t, srv, "")
	_, err := svc.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hi"}},
		llm.WithModel("override"),
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var sent anthropicRequest
	_ = json.Unmarshal(cap.body, &sent)
	if sent.Model != "override" {
		t.Errorf("model = %q, want override", sent.Model)
	}
}

func TestGenerateHTTPErrorTransient(t *testing.T) {
	srv, _ := newFakeServer(t, http.StatusTooManyRequests, `{"error":"rate limit"}`)
	svc := withClient(t, srv, "claude-x")
	_, err := svc.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hi"}},
	)
	if err == nil {
		t.Fatalf("expected error on 429")
	}
	if !llm.IsTransient(err) {
		t.Errorf("429 should be transient, got %v", err)
	}
	var lerr *llm.LLMError
	if !errors.As(err, &lerr) {
		t.Fatalf("error is not *llm.LLMError: %T", err)
	}
	if lerr.Reason != "http_429" {
		t.Errorf("Reason = %q, want http_429", lerr.Reason)
	}
}

func TestGenerateHTTPErrorTerminal(t *testing.T) {
	srv, _ := newFakeServer(t, http.StatusBadRequest, `{"error":"bad"}`)
	svc := withClient(t, srv, "claude-x")
	_, err := svc.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hi"}},
	)
	if err == nil {
		t.Fatalf("expected error on 400")
	}
	if llm.IsTransient(err) {
		t.Errorf("400 should be terminal, got transient")
	}
}

func TestGenerateHTTPError5xxTransient(t *testing.T) {
	srv, _ := newFakeServer(t, http.StatusInternalServerError, `{"error":"oops"}`)
	svc := withClient(t, srv, "claude-x")
	_, err := svc.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hi"}},
	)
	if err == nil {
		t.Fatalf("expected error on 500")
	}
	if !llm.IsTransient(err) {
		t.Errorf("500 should be transient")
	}
}

func TestGenerateNilMessageRejected(t *testing.T) {
	srv, _ := newFakeServer(t, http.StatusOK, `{}`)
	svc := withClient(t, srv, "claude-x")
	_, err := svc.Generate(context.Background(),
		[]*schema.Message{nil},
	)
	if err == nil {
		t.Fatalf("expected error on nil message")
	}
}

func TestGenerateToolMessageRequiresID(t *testing.T) {
	srv, _ := newFakeServer(t, http.StatusOK, `{}`)
	svc := withClient(t, srv, "claude-x")
	_, err := svc.Generate(context.Background(),
		[]*schema.Message{{Role: schema.Tool, Content: "x"}},
	)
	if err == nil {
		t.Fatalf("expected error on tool message missing ToolCallID")
	}
}

func TestEmptyAssistantTurnSkipped(t *testing.T) {
	srv, cap := newFakeServer(t, http.StatusOK, `{
		"id":"x","type":"message","role":"assistant","model":"claude-x",
		"content":[{"type":"text","text":"ok"}],
		"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}
	}`)
	svc := withClient(t, srv, "claude-x")
	_, err := svc.Generate(context.Background(),
		[]*schema.Message{
			{Role: schema.User, Content: "hi"},
			{Role: schema.Assistant, Content: ""}, // empty: must be skipped
			{Role: schema.User, Content: "again"},
		},
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var sent anthropicRequest
	_ = json.Unmarshal(cap.body, &sent)
	if len(sent.Messages) != 2 {
		t.Errorf("messages len = %d, want 2 (empty assistant skipped)", len(sent.Messages))
	}
}

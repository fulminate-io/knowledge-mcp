package gemini

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// Tests in this file pin response decoding and error mapping. They share
// the roundTripFunc helper from gemini_test.go.

// --- HTTP error mapping -----------------------------------------------

func TestGenerate_HTTP429Transient(t *testing.T) {
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return canned(http.StatusTooManyRequests, `{"error":{"message":"rate limited"}}`), nil
	})
	svc := newServiceWithRT(t, rt)
	_, err := svc.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "x"},
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	var le *llm.LLMError
	if !errors.As(err, &le) {
		t.Fatalf("expected LLMError, got %v", err)
	}
	if !le.Transient {
		t.Errorf("429 should be transient")
	}
	if le.Reason != "http_429" {
		t.Errorf("reason = %q", le.Reason)
	}
	if !llm.IsTransient(err) {
		t.Errorf("IsTransient should be true")
	}
}

func TestGenerate_HTTP400Terminal(t *testing.T) {
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return canned(http.StatusBadRequest, `{"error":{"message":"bad input"}}`), nil
	})
	svc := newServiceWithRT(t, rt)
	_, err := svc.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "x"},
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	var le *llm.LLMError
	if !errors.As(err, &le) {
		t.Fatalf("expected LLMError, got %v", err)
	}
	if le.Transient {
		t.Errorf("400 should be terminal")
	}
	if le.Reason != "http_400" {
		t.Errorf("reason = %q", le.Reason)
	}
}

func TestGenerate_HTTP503Transient(t *testing.T) {
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return canned(http.StatusServiceUnavailable, `{}`), nil
	})
	svc := newServiceWithRT(t, rt)
	_, err := svc.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "x"},
	})
	var le *llm.LLMError
	if !errors.As(err, &le) || !le.Transient {
		t.Fatalf("expected transient LLMError, got %v", err)
	}
}

func TestGenerate_NetworkErrorTransient(t *testing.T) {
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return nil, errors.New("dial timeout")
	})
	svc := newServiceWithRT(t, rt)
	_, err := svc.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "x"},
	})
	var le *llm.LLMError
	if !errors.As(err, &le) {
		t.Fatalf("expected LLMError, got %v", err)
	}
	if !le.Transient || le.Reason != "network" {
		t.Errorf("expected transient network error, got %+v", le)
	}
}

func TestGenerate_PromptBlocked(t *testing.T) {
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return canned(http.StatusOK, `{"promptFeedback":{"blockReason":"SAFETY"}}`), nil
	})
	svc := newServiceWithRT(t, rt)
	_, err := svc.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "x"},
	})
	var le *llm.LLMError
	if !errors.As(err, &le) {
		t.Fatalf("expected LLMError, got %v", err)
	}
	if le.Transient {
		t.Errorf("prompt block should be terminal")
	}
	if le.Reason != "prompt_blocked" {
		t.Errorf("reason = %q", le.Reason)
	}
}

// --- parseResponse / mapFinishReason units -----------------------------

func TestParseResponse_FinishReasons(t *testing.T) {
	cases := []struct {
		raw  string
		want llm.FinishReason
	}{
		{"STOP", llm.FinishReasonEndTurn},
		{"MAX_TOKENS", llm.FinishReasonMaxTokens},
		{"STOP_SEQUENCE", llm.FinishReasonStopSequence},
		{"SAFETY", llm.FinishReasonOther},
		{"", llm.FinishReasonOther},
	}
	for _, tc := range cases {
		got := mapFinishReason(tc.raw, false)
		if got != tc.want {
			t.Errorf("mapFinishReason(%q, false) = %q, want %q", tc.raw, got, tc.want)
		}
	}
	if got := mapFinishReason("STOP", true); got != llm.FinishReasonToolUse {
		t.Errorf("hasToolCalls should override to tool_use, got %q", got)
	}
}

func TestParseResponse_NoCandidates(t *testing.T) {
	_, err := parseResponse([]byte(`{}`), llm.Model("g"))
	if err == nil {
		t.Fatalf("expected error")
	}
	var le *llm.LLMError
	if !errors.As(err, &le) || le.Reason != "no_candidates" {
		t.Errorf("expected no_candidates LLMError, got %v", err)
	}
}

func TestParseResponse_RawPreserved(t *testing.T) {
	body := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}]}`)
	resp, err := parseResponse(body, llm.Model("g"))
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if string(resp.Raw) != string(body) {
		t.Errorf("Raw not preserved verbatim")
	}
}

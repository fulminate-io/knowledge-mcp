package gemini

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// roundTripFunc lets tests stub http.Client.Do without spinning up a real
// listener for cases where we want to assert on the *http.Request before
// returning a canned body.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newServiceWithRT(t *testing.T, rt roundTripFunc) *Service {
	t.Helper()
	svc, err := NewWithHTTPClient(&llm.Config{
		Provider: llm.ProviderGemini,
		APIKey:   "test-key",
		Model:    llm.Model("gemini-2.5-pro"),
	}, &http.Client{Transport: rt})
	if err != nil {
		t.Fatalf("NewWithHTTPClient: %v", err)
	}
	return svc
}

func mustReadBody(t *testing.T, r *http.Request) []byte {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	return b
}

func canned(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

// --- Registration / interface satisfaction -----------------------------

func TestRegistration(t *testing.T) {
	if !llm.HasProvider(llm.ProviderGemini) {
		t.Fatalf("gemini provider not registered after import")
	}
	providers := llm.ListProviders()
	found := slices.Contains(providers, llm.ProviderGemini)
	if !found {
		t.Fatalf("ListProviders missing gemini: %v", providers)
	}
}

func TestNewClient_ValidatesAPIKey(t *testing.T) {
	_, err := New(context.Background(), &llm.Config{
		Provider: llm.ProviderGemini,
	})
	if err == nil {
		t.Fatalf("expected error for missing APIKey")
	}
	if !errors.Is(err, llm.ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
}

func TestNewClient_DefaultsBaseURL(t *testing.T) {
	c, err := New(context.Background(), &llm.Config{
		Provider: llm.ProviderGemini,
		APIKey:   "k",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	svc := c.(*Service)
	if svc.baseURL != DefaultBaseURL {
		t.Fatalf("baseURL = %q want %q", svc.baseURL, DefaultBaseURL)
	}
	if svc.Provider() != llm.ProviderGemini {
		t.Fatalf("Provider() = %q want %q", svc.Provider(), llm.ProviderGemini)
	}
}

// TestGenerate_TextSuccess covers the end-to-end happy path: URL
// construction, header auth, body shape, response decoding, usage
// recording. Provider-specific error/translation cases live in
// request_test.go and response_test.go.
func TestGenerate_TextSuccess(t *testing.T) {
	var capturedURL string
	var capturedAPIKey string

	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		capturedURL = r.URL.String()
		capturedAPIKey = r.Header.Get("x-goog-api-key")
		_ = mustReadBody(t, r)
		return canned(http.StatusOK, `{
			"candidates":[{"content":{"role":"model","parts":[{"text":"hi there"}]},"finishReason":"STOP"}],
			"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":3}
		}`), nil
	})

	svc := newServiceWithRT(t, rt)
	resp, err := svc.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "hello"},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if resp.Content != "hi there" {
		t.Errorf("Content = %q", resp.Content)
	}
	if resp.FinishReason != llm.FinishReasonEndTurn {
		t.Errorf("FinishReason = %q", resp.FinishReason)
	}
	if resp.Usage.InputTokens != 12 || resp.Usage.OutputTokens != 3 {
		t.Errorf("Usage = %+v", resp.Usage)
	}
	if resp.Provider != llm.ProviderGemini {
		t.Errorf("Provider = %q", resp.Provider)
	}
	if resp.Model != llm.Model("gemini-2.5-pro") {
		t.Errorf("Model = %q", resp.Model)
	}
	if len(resp.Raw) == 0 {
		t.Errorf("Raw should be populated")
	}

	if !strings.Contains(capturedURL, "/v1beta/models/gemini-2.5-pro:generateContent") {
		t.Errorf("unexpected URL: %s", capturedURL)
	}
	if capturedAPIKey != "test-key" {
		t.Errorf("x-goog-api-key header = %q", capturedAPIKey)
	}
	// Key must NOT appear in the URL — that's the whole point of using the header.
	if strings.Contains(capturedURL, "test-key") {
		t.Errorf("API key leaked into URL: %s", capturedURL)
	}

	// Aggregate usage tracked.
	if got := svc.GetUsage(); got.InputTokens != 12 || got.OutputTokens != 3 {
		t.Errorf("GetUsage() = %+v", got)
	}
}

// --- Model resolution + URL construction -------------------------------

func TestGenerate_PerCallModelOverride(t *testing.T) {
	var capturedURL string

	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		capturedURL = r.URL.String()
		return canned(http.StatusOK, `{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}]}`), nil
	})

	svc := newServiceWithRT(t, rt)
	_, err := svc.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "hi"},
	}, llm.WithModel(llm.Model("gemini-2.0-flash")))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(capturedURL, "/models/gemini-2.0-flash:generateContent") {
		t.Errorf("per-call model not honored: %s", capturedURL)
	}
}

func TestGenerate_PerCallBaseURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`))
	}))
	defer srv.Close()

	svc := newServiceWithRT(t, roundTripFunc(http.DefaultTransport.RoundTrip))
	_, err := svc.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "x"},
	}, llm.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
}

func TestGenerate_NoModel(t *testing.T) {
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		t.Fatalf("HTTP should not have been called")
		return nil, nil
	})
	svc, err := NewWithHTTPClient(&llm.Config{
		Provider: llm.ProviderGemini,
		APIKey:   "k",
	}, &http.Client{Transport: rt})
	if err != nil {
		t.Fatalf("NewWithHTTPClient: %v", err)
	}
	_, err = svc.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "x"},
	})
	var le *llm.LLMError
	if !errors.As(err, &le) || le.Reason != "config" {
		t.Errorf("expected config error, got %v", err)
	}
}

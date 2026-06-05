// SPDX-License-Identifier: Apache-2.0

// Package openai provides an llm.Client implementation that talks to the
// OpenAI /v1/chat/completions API (and any OpenAI-compatible gateway: vLLM,
// Ollama, LiteLLM, OpenRouter, Azure OpenAI, etc.).
//
// Knowledge intentionally does NOT depend on the openai-go SDK. The existing
// summarize_openai.go pipeline (domains/store) uses hand-rolled HTTP calls
// against /v1/chat/completions; this provider follows the same pattern so we
// keep one wire-format target across the codebase.
//
// Translation map (RequestOptions → wire fields) lives in translate.go. Fields
// the OpenAI Chat Completions API does not honor are documented as comments
// on the translation function rather than silently dropped.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// DefaultBaseURL is the OpenAI public API endpoint. Callers can override via
// llm.Config.BaseURL or llm.WithBaseURL for gateways and regional overrides.
const DefaultBaseURL = "https://api.openai.com"

// chatCompletionsPath is the relative path appended to BaseURL for non-streaming chat.
const chatCompletionsPath = "/v1/chat/completions"

// Service is the OpenAI llm.Client implementation. Embeds *llm.BaseService for
// shared Provider() identity and aggregate token-usage tracking.
type Service struct {
	*llm.BaseService

	cfg    *llm.Config
	client *http.Client
}

// Compile-time guarantee that Service satisfies llm.Client. Lives in this
// file (not translate.go) so removing the constructor surfaces the breakage
// at the same site.
var _ llm.Client = (*Service)(nil)

func init() {
	llm.RegisterProvider(llm.ProviderOpenAI, NewService)
}

// NewService is the registered factory. Caller-facing entry point is
// llm.NewClient(ctx, &llm.Config{Provider: llm.ProviderOpenAI, ...}); the
// substrate validates cfg before dispatching here.
func NewService(_ context.Context, cfg *llm.Config) (llm.Client, error) {
	if cfg == nil {
		return nil, fmt.Errorf("%w: nil config", llm.ErrInvalidConfig)
	}
	return &Service{
		BaseService: llm.NewBaseService(llm.ProviderOpenAI),
		cfg:         cfg,
		client:      llm.DefaultHTTPClient(),
	}, nil
}

// Generate executes one /v1/chat/completions call against the OpenAI API.
//
// The wire body is built from messages + applied options via translate.go.
// Errors are returned as *llm.LLMError so callers can distinguish transient
// (HTTP 429 / 5xx, network) from terminal (4xx-other, malformed response,
// configuration) failures via llm.IsTransient.
func (s *Service) Generate(ctx context.Context, messages []*schema.Message, opts ...llm.Option) (*llm.Response, error) {
	options := llm.ApplyOptions(opts...)

	model := options.Model
	if model == "" {
		model = s.cfg.Model
	}
	if model == "" {
		return nil, &llm.LLMError{Transient: false, Reason: "config", Cause: fmt.Errorf("openai: model is required (set llm.WithModel or Config.Model)")}
	}

	apiKey := options.APIKey
	if apiKey == "" {
		apiKey = s.cfg.APIKey
	}

	baseURL := options.BaseURL
	if baseURL == "" {
		baseURL = s.cfg.BaseURL
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")

	reqBody, err := buildRequest(model, messages, options)
	if err != nil {
		return nil, err
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "marshal_request", Cause: err}
	}

	url := baseURL + chatCompletionsPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "create_request", Cause: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	httpResp, err := s.client.Do(httpReq)
	if err != nil {
		return nil, &llm.LLMError{Transient: true, Reason: "network", Cause: fmt.Errorf("openai: %w", err)}
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, &llm.LLMError{Transient: true, Reason: "read_response", Cause: err}
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, &llm.LLMError{
			Transient:  llm.HTTPStatusToTransient(httpResp.StatusCode),
			Reason:     fmt.Sprintf("http_%d", httpResp.StatusCode),
			RetryAfter: llm.ParseRetryAfter(httpResp.Header),
			Cause:      fmt.Errorf("openai: status %d: %s", httpResp.StatusCode, string(respBody)),
		}
	}

	resp, err := parseResponse(respBody, model)
	if err != nil {
		return nil, err
	}

	s.RecordUsage(resp.Usage)
	return resp, nil
}

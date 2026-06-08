// Package gemini implements an llm.Client backed by Google's Gemini REST
// API (generativelanguage.googleapis.com).
//
// The package self-registers with the llm registry from init() so callers
// reach a Gemini client via:
//
//	import _ "github.com/fulminate-io/knowledge-mcp/internal/llm/gemini"
//	cli, err := llm.NewClient(ctx, &llm.Config{
//	    Provider: llm.ProviderGemini,
//	    APIKey:   key,
//	    Model:    llm.Model("gemini-2.5-pro"),
//	})
//
// Authentication uses the `x-goog-api-key` request header (preferred over
// the `?key=` query parameter — keeps credentials out of access logs and
// HTTP error messages).
//
// Translation rules between llm.RequestOptions / eino schema.Message and
// the Gemini wire shapes are documented on buildRequest in request.go and
// parseResponse in response.go.
package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// DefaultBaseURL is the public Gemini REST endpoint. Callers that need a
// regional override or a proxy set Config.BaseURL.
const DefaultBaseURL = "https://generativelanguage.googleapis.com"

// apiVersion is the path segment we target. Gemini's stable v1beta has the
// generationConfig knobs (thinkingConfig, responseSchema) we surface.
const apiVersion = "v1beta"

// Service is the Gemini llm.Client implementation.
//
// It embeds *llm.BaseService for Provider() identity and aggregate token
// tracking, and uses an injected *http.Client so tests can swap the
// transport.
type Service struct {
	*llm.BaseService

	apiKey  string
	baseURL string
	model   llm.Model
	client  *http.Client
}

// Compile-time interface check. Keeps the substrate contract honest as
// llm.Client evolves.
var _ llm.Client = (*Service)(nil)

// New constructs a Service from an llm.Config. The provider registry calls
// this from llm.NewClient; tests can also call it directly to inject a
// custom *http.Client via the returned Service's exported fields. The
// non-exported HTTP client is intentional — callers who want a custom
// transport should use NewWithHTTPClient.
func New(_ context.Context, cfg *llm.Config) (llm.Client, error) {
	if cfg == nil {
		return nil, fmt.Errorf("%w: nil config", llm.ErrInvalidConfig)
	}
	// Defense-in-depth gate (gemini.New is also called directly from tests,
	// bypassing llm.Config.Validate): a keyless config is valid only when it
	// supplies a BaseURL — a local/compatible endpoint that handles auth
	// out-of-band. Retained, not deleted.
	if cfg.APIKey == "" && cfg.BaseURL == "" {
		return nil, fmt.Errorf("%w: gemini requires APIKey or BaseURL", llm.ErrInvalidConfig)
	}
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	return &Service{
		BaseService: llm.NewBaseService(llm.ProviderGemini),
		apiKey:      cfg.APIKey,
		baseURL:     strings.TrimRight(base, "/"),
		model:       cfg.Model,
		client:      llm.DefaultHTTPClient(),
	}, nil
}

// NewWithHTTPClient is the test-injection door. Production code reaches
// the Service through llm.NewClient; tests that want a custom round
// tripper construct the Service directly.
func NewWithHTTPClient(cfg *llm.Config, hc *http.Client) (*Service, error) {
	c, err := New(context.Background(), cfg)
	if err != nil {
		return nil, err
	}
	svc, ok := c.(*Service)
	if !ok {
		return nil, fmt.Errorf("gemini: factory returned unexpected client type %T", c)
	}
	if hc != nil {
		svc.client = hc
	}
	return svc, nil
}

// Generate executes one non-streaming Gemini call.
//
// Translation steps:
//  1. Apply the variadic Options into a populated RequestOptions.
//  2. Resolve the effective model (per-call WithModel beats Config.Model).
//  3. Translate messages + options into the Gemini wire body.
//  4. Build the URL ("{base}/v1beta/models/{model}:generateContent").
//  5. POST with the x-goog-api-key header.
//  6. Decode the response body into *llm.Response and record usage.
func (s *Service) Generate(ctx context.Context, messages []*schema.Message, opts ...llm.Option) (*llm.Response, error) {
	applied := llm.ApplyOptions(opts...)

	model := applied.Model
	if model == "" {
		model = s.model
	}
	if model == "" {
		return nil, &llm.LLMError{
			Transient: false,
			Reason:    "config",
			Cause:     fmt.Errorf("gemini: no model set on call or config"),
		}
	}

	body, err := buildRequest(messages, applied)
	if err != nil {
		return nil, &llm.LLMError{
			Transient: false,
			Reason:    "build_request",
			Cause:     err,
		}
	}

	respBytes, err := s.do(ctx, body, applied, model)
	if err != nil {
		return nil, err
	}

	parsed, err := parseResponse(respBytes, model)
	if err != nil {
		return nil, err
	}
	s.RecordUsage(parsed.Usage)
	return parsed, nil
}

// do marshals the request body, issues the POST, and returns the response body
// bytes. It folds every transport / status-code failure into a typed *llm.LLMError
// so Generate stays focused on the translation pipeline.
func (s *Service) do(ctx context.Context, body any, applied *llm.RequestOptions, model llm.Model) ([]byte, error) {
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "marshal_request", Cause: err}
	}

	endpoint := s.endpointFor(applied.BaseURL, model)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "create_request", Cause: err}
	}
	req.Header.Set("Content-Type", "application/json")
	apiKey := applied.APIKey
	if apiKey == "" {
		apiKey = s.apiKey
	}
	// Send the auth header only when a key is set: a keyless local
	// endpoint (base_url override) handles auth out-of-band. Mirrors the
	// openai service guard.
	if apiKey != "" {
		req.Header.Set("x-goog-api-key", apiKey)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, &llm.LLMError{Transient: true, Reason: "network", Cause: fmt.Errorf("gemini API: %w", err)}
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &llm.LLMError{Transient: true, Reason: "read_response", Cause: err}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &llm.LLMError{
			Transient:  llm.HTTPStatusToTransient(resp.StatusCode),
			Reason:     fmt.Sprintf("http_%d", resp.StatusCode),
			RetryAfter: llm.ParseRetryAfter(resp.Header),
			Cause:      fmt.Errorf("gemini API status %d: %s", resp.StatusCode, string(respBytes)),
		}
	}
	return respBytes, nil
}

// endpointFor builds the generateContent URL for the resolved base + model.
// per-call BaseURL override wins over the Service-configured base.
func (s *Service) endpointFor(perCallBase string, model llm.Model) string {
	base := s.baseURL
	if perCallBase != "" {
		base = strings.TrimRight(perCallBase, "/")
	}
	// url.PathEscape on the model segment so a model name containing a
	// slash (Gemini sometimes uses paths like "models/gemini-1.5/preview")
	// stays a single path component.
	model = llm.Model(url.PathEscape(string(model)))
	return fmt.Sprintf("%s/%s/models/%s:generateContent", base, apiVersion, model)
}

func init() {
	llm.RegisterProvider(llm.ProviderGemini, New)
}

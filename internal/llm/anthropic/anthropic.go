// SPDX-License-Identifier: Apache-2.0

// Package anthropic implements the [llm.Client] substrate for the Anthropic
// Messages API.
//
// The provider self-registers under [llm.ProviderAnthropic] from init() so
// callers receive a working client by importing the package once and then
// calling [llm.NewClient] with a Config whose Provider is ProviderAnthropic.
//
// One Generate call maps to one POST against /v1/messages — there is no
// outer tool-use loop here. Higher-level loops (like the dream-worker tool
// agent in domains/store) drive the loop themselves by issuing successive
// Generate calls and wiring tool_result back in as user-role messages.
package anthropic

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// defaultBaseURL is the public Anthropic API endpoint. Callers point at a
// proxy or regional override via Config.BaseURL.
const defaultBaseURL = "https://api.anthropic.com"

// messagesPath is appended to BaseURL for every request. Keeping the /v1
// prefix here (rather than baking it into the BaseURL default) lets a
// caller's BaseURL stay a bare host the same way other knowledge config
// strings do.
const messagesPath = "/v1/messages"

// anthropicVersion is the Messages-API version pinned by all requests. The
// 2023-06-01 wire format is what every current Claude model targets.
const anthropicVersion = "2023-06-01"

// defaultMaxTokens is used when the caller does not set MaxTokens. The
// Messages API requires a positive max_tokens on every request, so we pick
// a reasonable cap rather than rejecting the call. 4096 matches the value
// store/summarize_openai_agent_anthropic.go has been running in production.
const defaultMaxTokens = 4096

// resolveMaxTokens applies the default cap when the caller left MaxTokens
// unset (<= 0). Shared by buildRequest (to set the wire field) and Generate's
// truncation report (so the TruncatedOutputError names the cap actually sent).
// This is the pre-thinking cap; applyThinking may bump it higher when extended
// thinking is on, but a truncated structured response never has thinking
// active on the same call.
func resolveMaxTokens(requested int) int {
	if requested <= 0 {
		return defaultMaxTokens
	}
	return requested
}

// init registers Anthropic's factory at package import time. Test code can
// rely on a side-effect import (`_ "github.com/.../domains/llm/anthropic"`)
// to make the provider visible to llm.NewClient.
func init() {
	llm.RegisterProvider(llm.ProviderAnthropic, newClientFromConfig)
}

// Service is the Anthropic implementation of [llm.Client]. It embeds
// [llm.BaseService] for shared Provider/usage bookkeeping.
//
// httpClient is exposed so tests can swap in an httptest.Server-backed
// http.Client. defaultModel is consulted only when the caller does not
// supply WithModel for a given Generate call.
type Service struct {
	*llm.BaseService

	apiKey       string
	baseURL      string
	defaultModel llm.Model
	httpClient   *http.Client
}

// Compile-time guard: Service must satisfy llm.Client. If the interface
// shape changes, the build fails here rather than at the registry call site.
var _ llm.Client = (*Service)(nil)

// newClientFromConfig is the [llm.ProviderFactory] entry point. The
// substrate's Validate has already been called by the registry, so APIKey is
// guaranteed non-empty.
func newClientFromConfig(_ context.Context, cfg *llm.Config) (llm.Client, error) {
	return New(cfg.APIKey, cfg.BaseURL, cfg.Model, nil), nil
}

// New constructs a Service bound to the given credentials. httpClient may
// be nil to use [llm.DefaultHTTPClient]; tests pass an httptest-backed
// client to intercept requests.
func New(apiKey, baseURL string, defaultModel llm.Model, httpClient *http.Client) *Service {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if httpClient == nil {
		httpClient = llm.DefaultHTTPClient()
	}
	return &Service{
		BaseService:  llm.NewBaseService(llm.ProviderAnthropic),
		apiKey:       apiKey,
		baseURL:      baseURL,
		defaultModel: defaultModel,
		httpClient:   httpClient,
	}
}

// Generate executes one non-streaming /v1/messages call. Per the
// [llm.Client] contract this is a single turn — there is no caller-hidden
// tool-use loop. ToolCalls in the response signal that the caller should
// dispatch the tools and re-invoke Generate with a tool-result message.
func (s *Service) Generate(ctx context.Context, messages []*schema.Message, opts ...llm.Option) (*llm.Response, error) {
	options := llm.ApplyOptions(opts...)

	model := s.pickModel(options)
	if model == "" {
		return nil, fmt.Errorf("%w: anthropic requires a model (set Config.Model or pass WithModel)", llm.ErrInvalidConfig)
	}

	apiKey := s.apiKey
	if options.APIKey != "" {
		apiKey = options.APIKey
	}
	baseURL := s.baseURL
	if options.BaseURL != "" {
		baseURL = options.BaseURL
	}

	reqBody, err := buildRequest(model, messages, options)
	if err != nil {
		return nil, &llm.LLMError{Reason: "translate_request", Cause: err}
	}

	body, status, retryAfter, err := s.doPost(ctx, baseURL+messagesPath, apiKey, reqBody)
	if err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "network", Cause: err}
	}
	if status < 200 || status >= 300 {
		return nil, &llm.LLMError{
			Transient:  llm.HTTPStatusToTransient(status),
			Reason:     fmt.Sprintf("http_%d", status),
			RetryAfter: retryAfter,
			Cause:      fmt.Errorf("anthropic: %s", string(body)),
		}
	}

	resp, err := parseResponse(body, model)
	if err != nil {
		return nil, &llm.LLMError{Reason: "parse_response", Cause: err}
	}
	s.RecordUsage(resp.Usage)

	if err := applyStructuredResponse(resp, options); err != nil {
		return nil, err
	}

	return resp, nil
}

// applyStructuredResponse runs the structured-output response handling for a
// request that asked for ResponseFormat, in order:
//
//  1. Truncation FIRST. A structured request that stopped on max_tokens carries
//     a partial, unparseable JSON body — return the distinct, wrappable
//     truncation error rather than letting the consumer choke on half a
//     document. A NON-structured max_tokens response is a normal truncated
//     completion the caller handles via FinishReason, so this is gated on
//     ResponseFormat (a no-op when ResponseFormat is nil).
//  2. The fallback content bridge. On the forced-tool path the structured
//     output arrives as a structured_output tool_use block, which
//     splitContentBlocks routed into resp.ToolCalls, leaving resp.Content
//     empty. Both ResponseFormat consumers read resp.Content only, so copy the
//     synthesized tool's arguments (already a raw JSON object string — no
//     re-marshal) into resp.Content. Match by outputToolName so a genuine
//     WithTools tool_use is never touched; gate on empty Content so a real text
//     block is never clobbered; ToolCalls is left intact.
func applyStructuredResponse(resp *llm.Response, options *llm.RequestOptions) error {
	if options.ResponseFormat == nil {
		return nil
	}
	if resp.FinishReason == llm.FinishReasonMaxTokens {
		return &llm.LLMError{
			Transient: false,
			Reason:    "response_truncated",
			Cause:     &TruncatedOutputError{StopReason: string(resp.FinishReason), MaxTokens: resolveMaxTokens(options.MaxTokens)},
		}
	}
	if resp.Content != "" {
		return nil
	}
	for i := range resp.ToolCalls {
		if resp.ToolCalls[i].Function.Name == outputToolName {
			resp.Content = resp.ToolCalls[i].Function.Arguments
			break
		}
	}
	return nil
}

// pickModel returns options.Model if set, otherwise the Service-level
// default. Empty result means the caller never specified a model.
func (s *Service) pickModel(options *llm.RequestOptions) llm.Model {
	if options != nil && options.Model != "" {
		return options.Model
	}
	return s.defaultModel
}

// doPost issues the POST and returns the raw body and status. The caller
// handles JSON parsing and status classification.
//
// Body is read in full with no truncation regardless of status — the
// upstream error message is often the only signal the operator has
// (mirrors store/summarize_openai_agent.go:doHTTPPost). See
// feedback_no_truncation_for_llm.
func (s *Service) doPost(ctx context.Context, url, apiKey string, payload []byte) ([]byte, int, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Send the auth header only when a key is set: a keyless local
	// endpoint (base_url override) handles auth out-of-band. Mirrors the
	// openai service guard.
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	// Retry-After (parsed unconditionally; only non-2xx callers consult it)
	// lets a 429/503 caller honor the server's stated delay.
	retryAfter := llm.ParseRetryAfter(resp.Header)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, retryAfter, fmt.Errorf("read response: %w", err)
	}
	return body, resp.StatusCode, retryAfter, nil
}

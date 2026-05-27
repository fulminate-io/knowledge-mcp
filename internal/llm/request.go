package llm

import "github.com/cloudwego/eino/schema"

// RequestOptions is the populated form of all Option values applied to a
// single Generate call. Implementations read RequestOptions to translate
// caller intent into provider-specific request shapes.
//
// Provider implementations translate these fields into their wire formats and
// silently ignore (or document the omission of) any field they cannot honor.
// They MUST NOT pretend a knob isn't supported by dropping it from the
// substrate — knobs live here so callers can target them uniformly.
type RequestOptions struct {
	// Model selects which provider model to call (e.g. "gpt-5-mini").
	Model Model
	// SystemPrompt is prepended to the conversation as the system role.
	SystemPrompt string
	// Tools lists tools the model may call (eino schema).
	Tools []*schema.ToolInfo
	// ResponseFormat constrains the output shape (e.g. JSON schema mode).
	ResponseFormat *ResponseFormat
	// Temperature is the sampling temperature; nil leaves the provider default.
	Temperature *float32
	// TopP is the nucleus-sampling probability mass; nil leaves the provider default.
	TopP *float32
	// TopK caps the candidate token count per step; nil leaves the provider default.
	TopK *int32
	// MaxTokens caps the model's output token count; 0 means provider default.
	MaxTokens int
	// StopSequences halts generation when any of these appear in the output.
	StopSequences []string
	// ExtendedThinking enables an Anthropic extended-thinking turn (Claude 4.x
	// extended thinking, Gemini thinkingConfig). Providers that don't support
	// it ignore the field.
	ExtendedThinking bool
	// DisableExtendedThinking forces extended thinking off even when a model
	// would default to it. Used by callers that want a deterministic non-thinking
	// turn from a thinking-capable model.
	DisableExtendedThinking bool
	// ThinkingBudget caps the thinking-token budget when ExtendedThinking is
	// true. Zero means provider default.
	ThinkingBudget int
	// ReasoningEffort selects an OpenAI o-series reasoning effort: "low",
	// "medium", or "high". Empty means provider default.
	ReasoningEffort string
	// BaseURL overrides the provider's default API endpoint for this request.
	// Most callers leave this empty and configure it once via Config.
	BaseURL string
	// APIKey overrides the provider's default credential for this request.
	// Most callers leave this empty and configure it once via Config.
	APIKey string
	// InheritWorkdir is honored ONLY by CLI providers (claude-cli,
	// codex-cli). When true, the spawned subprocess runs in the
	// parent's cwd — claude-cli auto-detects the project's
	// `.mcp.json` and any other project-local config in that
	// directory. When false (the default), the subprocess runs in
	// os.TempDir() so no project context bleeds in. Set true for
	// the dream worker, where the LLM's job IS to operate in the
	// caller's project context. Leave false for the summarizer,
	// startup precheck, and any other "single-shot non-agentic"
	// request — those should NOT spawn child MCP servers from the
	// project's `.mcp.json` (which would recursively dial the
	// running knowledge-server, adding ~30s of latency per call
	// and risking deadlocks). API providers ignore this field.
	InheritWorkdir bool
}

// ResponseFormat describes a constrained output shape.
//
// Type is the format identifier (e.g. "json_schema", "json_object"). Schema
// holds a provider-specific schema document; implementations marshal it as
// they need.
type ResponseFormat struct {
	Type   string
	Schema any
}

// ApplyOptions builds a RequestOptions by applying each opt in order.
//
// Implementations call this once at the top of Generate to materialize the
// caller's intent into a single struct.
func ApplyOptions(opts ...Option) *RequestOptions {
	o := &RequestOptions{}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// WithModel sets the model to use for this request.
func WithModel(model Model) Option {
	return func(o *RequestOptions) { o.Model = model }
}

// WithSystemPrompt sets the system prompt prepended to the conversation.
func WithSystemPrompt(prompt string) Option {
	return func(o *RequestOptions) { o.SystemPrompt = prompt }
}

// WithInheritWorkdir requests the CLI provider's subprocess inherit the
// parent's cwd instead of running in os.TempDir(). See RequestOptions
// docstring — this is the dream-worker escape hatch for cases where
// the LLM is expected to operate inside the project context. API
// providers ignore the option.
func WithInheritWorkdir() Option {
	return func(o *RequestOptions) { o.InheritWorkdir = true }
}

// WithTools sets the list of tools the model may call.
func WithTools(tools []*schema.ToolInfo) Option {
	return func(o *RequestOptions) { o.Tools = tools }
}

// WithResponseFormat constrains the output shape.
func WithResponseFormat(format *ResponseFormat) Option {
	return func(o *RequestOptions) { o.ResponseFormat = format }
}

// WithTemperature sets the sampling temperature.
func WithTemperature(temp float32) Option {
	return func(o *RequestOptions) { o.Temperature = &temp }
}

// WithTopP sets the nucleus-sampling probability mass.
func WithTopP(topP float32) Option {
	return func(o *RequestOptions) { o.TopP = &topP }
}

// WithTopK caps the candidate token count per step.
func WithTopK(topK int32) Option {
	return func(o *RequestOptions) { o.TopK = &topK }
}

// WithMaxTokens caps the output token count.
func WithMaxTokens(maxTokens int) Option {
	return func(o *RequestOptions) { o.MaxTokens = maxTokens }
}

// WithStopSequences halts generation when any sequence appears in the output.
func WithStopSequences(sequences ...string) Option {
	return func(o *RequestOptions) { o.StopSequences = sequences }
}

// WithExtendedThinking enables extended-thinking turns and sets the
// thinking-token budget. budget=0 means provider default.
func WithExtendedThinking(budget int) Option {
	return func(o *RequestOptions) {
		o.ExtendedThinking = true
		o.ThinkingBudget = budget
	}
}

// WithDisableExtendedThinking forces extended thinking off even on a model
// that would default to it.
func WithDisableExtendedThinking() Option {
	return func(o *RequestOptions) { o.DisableExtendedThinking = true }
}

// WithReasoningEffort selects an OpenAI o-series reasoning effort: "low",
// "medium", or "high".
func WithReasoningEffort(effort string) Option {
	return func(o *RequestOptions) { o.ReasoningEffort = effort }
}

// WithBaseURL overrides the provider endpoint for this request only. Prefer
// configuring BaseURL once on the provider Config; per-call overrides are for
// niche cases (proxy, regional override, test injection).
func WithBaseURL(url string) Option {
	return func(o *RequestOptions) { o.BaseURL = url }
}

// WithAPIKey overrides the provider credential for this request only. Prefer
// configuring APIKey once on the provider Config; per-call overrides are for
// per-tenant key rotation or tests.
func WithAPIKey(key string) Option {
	return func(o *RequestOptions) { o.APIKey = key }
}

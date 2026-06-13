// SPDX-License-Identifier: Apache-2.0

package llmproviders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// llmSummarizer is the substrate-routed summarizer adapter. It
// satisfies the package-private summarizer interface (for the LLM
// pipeline's SummarizeBatch path) on a single underlying llm.Client.
//
// All wire-shape concerns (provider selection, request marshaling,
// retry classification, transient/terminal error mapping) live in the
// per-provider sub-packages of domains/llm. This adapter only owns
// the summarize-specific JSON-schema prompt shape.
//
// systemPrompt selects the system prompt sent on every SummarizeBatch.
// An empty systemPrompt falls back to defaultCodeSummarizePrompt (see
// promptOrDefault), so the code-chunk pipeline path is unchanged while
// the topics path can supply defaultTopicSummarizePrompt.
type llmSummarizer struct {
	client       llm.Client
	model        llm.Model
	provider     llm.Provider
	systemPrompt string
}

// Compile-time interface satisfaction.
var _ summarizer = (*llmSummarizer)(nil)

// NewLLMSummarizer constructs a substrate-routed summarizer that
// satisfies the package-private summarizer interface. Production callers
// build this via BuildSummarizer; the constructor stays exported so
// integration test harnesses can supply their own llm.Client (typically
// llm.NewFakeClient) without reaching into unexported types.
//
// This is the code-chunk path: it delegates to NewLLMSummarizerWithPrompt
// with defaultCodeSummarizePrompt so the pipeline reindex path is unchanged.
func NewLLMSummarizer(client llm.Client, provider llm.Provider, model llm.Model) Summarizer {
	return NewLLMSummarizerWithPrompt(client, provider, model, defaultCodeSummarizePrompt)
}

// NewLLMSummarizerWithPrompt constructs a substrate-routed summarizer with an
// explicit system prompt. The topics path uses this to supply
// defaultTopicSummarizePrompt; an empty systemPrompt falls back to
// defaultCodeSummarizePrompt at send time (see promptOrDefault).
func NewLLMSummarizerWithPrompt(client llm.Client, provider llm.Provider, model llm.Model, systemPrompt string) Summarizer {
	return &llmSummarizer{client: client, model: model, provider: provider, systemPrompt: systemPrompt}
}

// promptOrDefault returns the configured systemPrompt, or
// defaultCodeSummarizePrompt when it is empty. The empty-string fallback keeps
// zero-value llmSummarizer struct literals (e.g. in tests) sending the code
// prompt.
func (s *llmSummarizer) promptOrDefault() string {
	if s.systemPrompt == "" {
		return defaultCodeSummarizePrompt
	}
	return s.systemPrompt
}

// SummarizeBatch sends numbered chunks to the configured llm.Client and
// returns positional summaries keyed by chunk ID.
//
// If the response fails to parse even after the tolerant fallback in
// parseSummariesContent, SummarizeBatch issues at most ONE additional billed
// repair Generate (see repairParse) before failing the batch — so a batch costs
// one Generate call on the common path and two at most.
//
// Errors are returned as *llm.LLMError so the pipeline worker can
// distinguish transient (retry next tick) from terminal (write failure
// marker) failures. The substrate layer surfaces *llm.LLMError directly;
// translateLLMError only buckets plain (non-LLMError) errors as terminal.
func (s *llmSummarizer) SummarizeBatch(ctx context.Context, chunks []BatchChunk) (map[string]SummarizeResult, error) {
	if len(chunks) == 0 {
		return nil, nil
	}

	// Format chunks as numbered text — same as the prior summarizer paths.
	var sb strings.Builder
	for i, c := range chunks {
		fmt.Fprintf(&sb, "--- Chunk %d ---\n%s\n\n", i+1, c.Content)
	}

	// JSON schema with exact minItems/maxItems pinning the model to one
	// summary per chunk in order. Any drift in count is a hard parse
	// failure rather than a silent under/over-fill.
	//
	// additionalProperties:false on every object is REQUIRED — the OpenAI
	// Responses API (which the codex-cli provider's --output-schema flag
	// drives) rejects strict response-format schemas that omit it with
	// "'additionalProperties' is required to be supplied and to be false."
	// Other providers (claude-cli json-schema, openai json_schema mode)
	// accept the extra constraint as a no-op tightening.
	schemaJSON := json.RawMessage(fmt.Sprintf(
		`{"type":"object","properties":{"items":{"type":"array","items":{"type":"object","properties":{"summary":{"type":"string","maxLength":200},"keywords":{"type":"array","items":{"type":"string"},"minItems":3,"maxItems":15}},"required":["summary","keywords"],"additionalProperties":false},"minItems":%d,"maxItems":%d}},"required":["items"],"additionalProperties":false}`,
		len(chunks), len(chunks),
	))

	resp, err := s.client.Generate(ctx, []*schema.Message{
		{Role: schema.User, Content: sb.String()},
	},
		llm.WithModel(s.model),
		llm.WithSystemPrompt(s.promptOrDefault()),
		// 4096 fits the batch worst case with headroom: batch size is 20
		// (pipeline.Config.SummaryBatchSizeOrDefault), and 20 items ×
		// {summary <=200 chars ~50-70 tokens + 3-15 keywords ~10-45 tokens}
		// plus the JSON envelope is ~2300-2800 output tokens, leaving
		// ~1300-1800 headroom. The anthropic provider's max_tokens truncation
		// sentinel is the safety net if a pathological batch ever truncates.
		llm.WithMaxTokens(4096),
		llm.WithResponseFormat(&llm.ResponseFormat{
			Type:   "json_schema",
			Schema: schemaJSON,
		}),
	)
	if err != nil {
		return nil, translateLLMError(err, "summarize_generate")
	}

	parsed, parseErr := parseSummariesContent(resp.Content)
	if parseErr != nil {
		// One billed repair retry: ask the model to return ONLY the JSON, with
		// the same json_schema ResponseFormat so structured providers stay
		// structured. On residual failure repairParse returns the unchanged
		// terminal parse_summaries_json error; a transient Generate error on the
		// repair surfaces with its transient flag intact.
		parsed, parseErr = s.repairParse(ctx, schemaJSON, resp.Content, parseErr)
		if parseErr != nil {
			return nil, parseErr
		}
	}

	results := make(map[string]SummarizeResult, len(chunks))
	for i, chunk := range chunks {
		if i >= len(parsed.Items) {
			break
		}
		item := parsed.Items[i]
		summary := strings.TrimSpace(item.Summary)
		if summary == "" {
			continue
		}
		results[chunk.ID] = SummarizeResult{
			Summary:  summary,
			Keywords: strings.Join(item.Keywords, " "),
		}
	}

	if len(results) == 0 {
		return nil, &llm.LLMError{
			Transient: false,
			Reason:    "empty_structured_output",
			Cause:     fmt.Errorf("no summaries decoded (content: %s)", resp.Content),
		}
	}
	return results, nil
}

// repairParse issues exactly ONE billed repair Generate when the primary
// response failed to parse even after the tolerant fallback. It re-sends the
// original raw content plus the parse error, asking the model to return ONLY
// the JSON object, with the SAME json_schema ResponseFormat (reusing schemaJSON)
// so structured providers stay structured on the retry. A Generate error on the
// repair call surfaces via translateLLMError with its transient flag intact (a
// repair-side http_429 must stay transient, not be masked into a terminal parse
// error). On residual parse failure it returns the unchanged terminal
// *llm.LLMError (Transient=false, Reason="parse_summaries_json") so the pipeline
// classifier and breaker-reason consumers see the same terminal reason.
func (s *llmSummarizer) repairParse(ctx context.Context, schemaJSON json.RawMessage, origContent string, parseErr error) (summariesPayload, error) {
	slog.Warn("llmproviders: summary parse repair attempted",
		"provider", s.provider, "model", s.model, "parse_error", parseErr)

	repairPrompt := fmt.Sprintf(
		"Your previous reply could not be parsed as JSON (error: %v). "+
			"Return ONLY the JSON object matching the required schema — no prose, "+
			"no Markdown code fences, no commentary. Previous reply:\n%s",
		parseErr, origContent,
	)

	resp, err := s.client.Generate(ctx, []*schema.Message{
		{Role: schema.User, Content: repairPrompt},
	},
		llm.WithModel(s.model),
		llm.WithSystemPrompt(s.promptOrDefault()),
		llm.WithMaxTokens(4096),
		llm.WithResponseFormat(&llm.ResponseFormat{
			Type:   "json_schema",
			Schema: schemaJSON,
		}),
	)
	if err != nil {
		return summariesPayload{}, translateLLMError(err, "summarize_generate")
	}

	parsed, repairErr := parseSummariesContent(resp.Content)
	if repairErr != nil {
		return summariesPayload{}, &llm.LLMError{
			Transient: false,
			Reason:    "parse_summaries_json",
			Cause:     fmt.Errorf("parse summaries JSON: %w (content: %s)", repairErr, resp.Content),
		}
	}
	return parsed, nil
}

// summariesPayload is the expected JSON structure from the LLM. Mirrors
// the shape the prior OpenAI summarizer used; identical decoding semantics.
type summariesPayload struct {
	Items []struct {
		Summary  string   `json:"summary"`
		Keywords []string `json:"keywords"`
	} `json:"items"`
}

// parseSummariesContent decodes the JSON-schema-constrained content string
// into the positional Items slice. It FIRST attempts a bare json.Unmarshal on
// the raw content (the zero-cost fast path: well-formed JSON does no
// fence/extraction work). ONLY on failure does it run the tolerant fallback —
// strip a surrounding Markdown code fence, then extract the first balanced JSON
// value substring (so a prose preamble/postscript is tolerated) — and retry the
// decode on the cleaned text. If nothing parses it returns the ORIGINAL
// fast-path error verbatim, so the caller's error message and downstream
// wrapping are unchanged when the content is genuinely un-decodable.
func parseSummariesContent(content string) (summariesPayload, error) {
	var parsed summariesPayload
	origErr := json.Unmarshal([]byte(content), &parsed)
	if origErr == nil {
		return parsed, nil
	}

	cleaned := stripCodeFences(content)
	if extracted, ok := extractFirstJSONValue(cleaned); ok {
		var retry summariesPayload
		if err := json.Unmarshal([]byte(extracted), &retry); err == nil {
			return retry, nil
		}
	}
	return summariesPayload{}, origErr
}

// stripCodeFences trims surrounding whitespace and, when the content opens with
// a Markdown code fence (```json, ```JSON, or a bare ```), removes the opening
// fence line and a matching trailing closing ``` fence. When no opening fence is
// present the input is returned unchanged (after the leading/trailing
// whitespace trim). Allocation-light: a handful of strings.* calls, no regexp —
// this runs on every fallback parse and a per-call regexp would be a hot-path
// allocation, matching the package's hoist-regex-or-avoid-it perf discipline.
func stripCodeFences(content string) string {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}

	// Drop the opening fence line (```, ```json, ```JSON, ... up to the newline).
	rest := trimmed[len("```"):]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[nl+1:]
	} else {
		rest = ""
	}

	// Drop a trailing closing fence if present.
	rest = strings.TrimSpace(rest)
	rest = strings.TrimSuffix(rest, "```")
	return strings.TrimSpace(rest)
}

// extractFirstJSONValue scans content for the first '{' or '[' and walks
// forward tracking brace/bracket nesting depth to return the first balanced JSON
// value substring (so a prose preamble before, or trailing text after, the JSON
// is tolerated). The scanner is JSON-string-literal aware: it toggles an
// in-string flag on each unescaped '"' and skips the character after a
// backslash, so a '}' or ']' inside a string value does not prematurely close
// the balanced span. Returns the balanced substring and true, or ("", false)
// when no opening delimiter or no balanced close is found. Single pass, no
// allocation beyond the returned slice.
func extractFirstJSONValue(content string) (string, bool) {
	start := strings.IndexAny(content, "{[")
	if start < 0 {
		return "", false
	}

	openCh := content[start]
	var closeCh byte = '}'
	if openCh == '[' {
		closeCh = ']'
	}

	depth := 0
	inStr := false
	escaped := false
	for i := start; i < len(content); i++ {
		ch := content[i]
		if inStr {
			switch {
			case escaped:
				escaped = false
			case ch == '\\':
				escaped = true
			case ch == '"':
				inStr = false
			}
			continue
		}
		switch ch {
		case '"':
			inStr = true
		case openCh:
			depth++
		case closeCh:
			depth--
			if depth == 0 {
				return content[start : i+1], true
			}
		}
	}
	return "", false
}

// translateLLMError surfaces the substrate error as an *llm.LLMError so the
// pipeline's transient/terminal classifier works without change. A substrate
// *llm.LLMError already carries the transient flag + reason and is returned
// as-is (filling in reasonPrefix only when the substrate left Reason empty).
// Plain (non-LLMError) errors are bucketed as terminal — a malformed substrate
// response is not something a retry will fix.
func translateLLMError(err error, reasonPrefix string) error {
	if err == nil {
		return nil
	}
	if le, ok := errors.AsType[*llm.LLMError](err); ok {
		if le.Reason == "" {
			le.Reason = reasonPrefix
		}
		return le
	}
	return &llm.LLMError{
		Transient: false,
		Reason:    reasonPrefix,
		Cause:     err,
	}
}

// SPDX-License-Identifier: Apache-2.0

package llmproviders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
type llmSummarizer struct {
	client   llm.Client
	model    llm.Model
	provider llm.Provider
}

// Compile-time interface satisfaction.
var _ summarizer = (*llmSummarizer)(nil)

// NewLLMSummarizer constructs a substrate-routed summarizer that
// satisfies the package-private summarizer interface. Production callers
// build this via BuildSummarizer; the constructor stays exported so
// integration test harnesses can supply their own llm.Client (typically
// llm.NewFakeClient) without reaching into unexported types.
func NewLLMSummarizer(client llm.Client, provider llm.Provider, model llm.Model) Summarizer {
	return &llmSummarizer{client: client, model: model, provider: provider}
}

// SummarizeBatch sends numbered chunks to the configured llm.Client and
// returns positional summaries keyed by chunk ID.
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
		llm.WithSystemPrompt(defaultCodeSummarizePrompt),
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
		return nil, &llm.LLMError{
			Transient: false,
			Reason:    "parse_summaries_json",
			Cause:     fmt.Errorf("parse summaries JSON: %w (content: %s)", parseErr, resp.Content),
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

// summariesPayload is the expected JSON structure from the LLM. Mirrors
// the shape the prior OpenAI summarizer used; identical decoding semantics.
type summariesPayload struct {
	Items []struct {
		Summary  string   `json:"summary"`
		Keywords []string `json:"keywords"`
	} `json:"items"`
}

// parseSummariesContent decodes the JSON-schema-constrained content
// string into the positional Items slice. Returns the decoded payload
// or a parse error verbatim — caller wraps as *llm.LLMError.
func parseSummariesContent(content string) (summariesPayload, error) {
	var parsed summariesPayload
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return summariesPayload{}, err
	}
	return parsed, nil
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

// SPDX-License-Identifier: Apache-2.0

package llmproviders

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// TestLLMSummarizer_SummarizeBatch_Happy verifies the JSON-schema
// prompt → positional decode round-trip. The FakeClient returns a
// canned items array and SummarizeBatch must align summaries to chunk
// IDs by position.
func TestLLMSummarizer_SummarizeBatch_Happy(t *testing.T) {
	canned := `{"items":[` +
		`{"summary":"first chunk","keywords":["alpha","beta","gamma"]},` +
		`{"summary":"second chunk","keywords":["delta","epsilon","zeta"]}` +
		`]}`
	fake := llm.NewFakeClient(&llm.Response{
		Content:      canned,
		FinishReason: llm.FinishReasonEndTurn,
		Usage:        llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
	})

	s := &llmSummarizer{client: fake, model: "fake-model", provider: llm.ProviderOpenAI}
	chunks := []BatchChunk{
		{ID: "a", Content: "first source"},
		{ID: "b", Content: "second source"},
	}
	out, err := s.SummarizeBatch(context.Background(), chunks)
	require.NoError(t, err)
	require.Len(t, out, 2)
	assert.Equal(t, "first chunk", out["a"].Summary)
	assert.Equal(t, "alpha beta gamma", out["a"].Keywords)
	assert.Equal(t, "second chunk", out["b"].Summary)
	assert.Equal(t, "delta epsilon zeta", out["b"].Keywords)

	// One Generate call, with the json_schema response format wired and
	// the system prompt set verbatim. Exactly one call means well-formed JSON
	// took the zero-cost fast path and the repair retry was NOT taken.
	calls := fake.Calls()
	require.Len(t, calls, 1)
	assert.NotNil(t, calls[0].Options.ResponseFormat)
	assert.Equal(t, "json_schema", calls[0].Options.ResponseFormat.Type)
	assert.Equal(t, defaultCodeSummarizePrompt, calls[0].Options.SystemPrompt)
}

// TestLLMSummarizer_SummarizeBatch_FencedJSON verifies that a reply wrapped in
// a Markdown ```json fence still parses — the tolerant fence-strip fallback
// recovers the JSON and NO repair retry fires (exactly one Generate call).
// Fails-when-absent: today the bare json.Unmarshal chokes on the leading fence.
func TestLLMSummarizer_SummarizeBatch_FencedJSON(t *testing.T) {
	canned := "```json\n" +
		`{"items":[` +
		`{"summary":"first chunk","keywords":["alpha","beta","gamma"]},` +
		`{"summary":"second chunk","keywords":["delta","epsilon","zeta"]}` +
		`]}` +
		"\n```"
	fake := llm.NewFakeClient(&llm.Response{
		Content:      canned,
		FinishReason: llm.FinishReasonEndTurn,
	})

	s := &llmSummarizer{client: fake, model: "fake-model", provider: llm.ProviderOpenAI}
	chunks := []BatchChunk{
		{ID: "a", Content: "first source"},
		{ID: "b", Content: "second source"},
	}
	out, err := s.SummarizeBatch(context.Background(), chunks)
	require.NoError(t, err)
	require.Len(t, out, 2)
	assert.Equal(t, "first chunk", out["a"].Summary)
	assert.Equal(t, "second chunk", out["b"].Summary)

	// Fence-strip succeeded on the fast-path fallback — no repair needed.
	require.Len(t, fake.Calls(), 1)
}

// TestLLMSummarizer_SummarizeBatch_ProsePreamble verifies that a reply with a
// prose preamble (and trailing sentence) around the JSON still parses via the
// balanced-JSON-substring extraction — and NO repair retry fires. One summary
// contains a literal '}' to exercise the string-literal-aware brace scanner:
// the embedded brace must not prematurely close the extracted span.
// Fails-when-absent: today the preamble fails the unmarshal.
func TestLLMSummarizer_SummarizeBatch_ProsePreamble(t *testing.T) {
	canned := "Here are the summaries you asked for:\n" +
		`{"items":[` +
		`{"summary":"handles the } brace case","keywords":["alpha","beta","gamma"]},` +
		`{"summary":"second chunk","keywords":["delta","epsilon","zeta"]}` +
		`]}` +
		"\nLet me know if you need anything else."
	fake := llm.NewFakeClient(&llm.Response{
		Content:      canned,
		FinishReason: llm.FinishReasonEndTurn,
	})

	s := &llmSummarizer{client: fake, model: "fake-model", provider: llm.ProviderOpenAI}
	chunks := []BatchChunk{
		{ID: "a", Content: "first source"},
		{ID: "b", Content: "second source"},
	}
	out, err := s.SummarizeBatch(context.Background(), chunks)
	require.NoError(t, err)
	require.Len(t, out, 2)
	assert.Equal(t, "handles the } brace case", out["a"].Summary)
	assert.Equal(t, "second chunk", out["b"].Summary)

	// Extraction succeeded on the fallback — no repair needed.
	require.Len(t, fake.Calls(), 1)
}

// TestLLMSummarizer_SummarizeBatch_RepairGenerateErrorTransient verifies that a
// transient error returned by the REPAIR Generate call surfaces with its
// transient flag intact — it is NOT masked into a terminal parse error. The
// primary call returns malformed content (forcing the repair), and the repair
// call (call #2) errors with a transient http_429 via SetErrorOnCall.
func TestLLMSummarizer_SummarizeBatch_RepairGenerateErrorTransient(t *testing.T) {
	fake := llm.NewFakeClient(&llm.Response{
		Content:      "not json at all",
		FinishReason: llm.FinishReasonEndTurn,
	})
	fake.SetErrorOnCall(2, &llm.LLMError{
		Transient: true,
		Reason:    "http_429",
		Cause:     errors.New("rate limited"),
	})

	s := &llmSummarizer{client: fake, model: "fake-model", provider: llm.ProviderOpenAI}
	_, err := s.SummarizeBatch(context.Background(), []BatchChunk{{ID: "a", Content: "x"}})
	require.Error(t, err)

	var le *llm.LLMError
	require.ErrorAs(t, err, &le, "repair Generate error must surface as *llm.LLMError")
	assert.True(t, le.Transient, "transient flag must survive the repair path")
	assert.Equal(t, "http_429", le.Reason)
	require.Len(t, fake.Calls(), 2, "primary + one repair attempt")
}

// TestLLMSummarizer_SummarizeBatch_TransientError verifies that an
// llm.LLMError marked Transient surfaces as *llm.LLMError with the
// transient flag preserved. The pipeline classifier relies on this
// distinction to retry on next tick rather than write a failure marker.
func TestLLMSummarizer_SummarizeBatch_TransientError(t *testing.T) {
	fake := llm.NewFakeClient()
	fake.SetError(&llm.LLMError{
		Transient: true,
		Reason:    "http_429",
		Cause:     errors.New("rate limited"),
	})

	s := &llmSummarizer{client: fake, model: "fake-model", provider: llm.ProviderOpenAI}
	_, err := s.SummarizeBatch(context.Background(), []BatchChunk{{ID: "a", Content: "x"}})
	require.Error(t, err)

	var le *llm.LLMError
	require.ErrorAs(t, err, &le, "must surface as *llm.LLMError")
	assert.True(t, le.Transient, "transient flag must round-trip")
	assert.Equal(t, "http_429", le.Reason)
}

// TestLLMSummarizer_SummarizeBatch_TerminalParseError verifies the canonical
// "two-malformed → terminal" path: the primary Generate returns un-parseable
// content (pure Markdown, no JSON), the repair retry fires exactly ONCE, that
// reply is ALSO un-parseable, and SummarizeBatch fails with a terminal LLMError
// (Transient=false, Reason="parse_summaries_json"). It must queue TWO responses
// — with one response the repair call would hit ErrFakeExhausted and change the
// error path. The fake.Calls() length of exactly 2 pins the "maximum one repair
// retry" billed-call guarantee. Pipeline classifier treats this terminal — a
// retry won't fix bad JSON.
func TestLLMSummarizer_SummarizeBatch_TerminalParseError(t *testing.T) {
	fake := llm.NewFakeClient(
		&llm.Response{Content: "## Heading\nsome prose", FinishReason: llm.FinishReasonEndTurn},
		&llm.Response{Content: "still not json", FinishReason: llm.FinishReasonEndTurn},
	)
	s := &llmSummarizer{client: fake, model: "fake-model", provider: llm.ProviderOpenAI}
	_, err := s.SummarizeBatch(context.Background(), []BatchChunk{{ID: "a", Content: "x"}})
	require.Error(t, err)

	var le *llm.LLMError
	require.ErrorAs(t, err, &le)
	assert.False(t, le.Transient)
	assert.Equal(t, "parse_summaries_json", le.Reason)

	// One primary + exactly one repair: the retry happened once and no more.
	require.Len(t, fake.Calls(), 2)
}

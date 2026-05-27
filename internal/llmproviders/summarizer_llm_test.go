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
	// the system prompt set verbatim.
	calls := fake.Calls()
	require.Len(t, calls, 1)
	assert.NotNil(t, calls[0].Options.ResponseFormat)
	assert.Equal(t, "json_schema", calls[0].Options.ResponseFormat.Type)
	assert.Equal(t, defaultCodeSummarizePrompt, calls[0].Options.SystemPrompt)
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

// TestLLMSummarizer_SummarizeBatch_TerminalParseError verifies that a
// successful Generate with malformed content returns a terminal LLMError
// (Transient=false, Reason="parse_summaries_json"). Pipeline classifier
// treats this as a terminal failure marker — a retry won't fix bad JSON.
func TestLLMSummarizer_SummarizeBatch_TerminalParseError(t *testing.T) {
	fake := llm.NewFakeClient(&llm.Response{
		Content:      "not json at all",
		FinishReason: llm.FinishReasonEndTurn,
	})
	s := &llmSummarizer{client: fake, model: "fake-model", provider: llm.ProviderOpenAI}
	_, err := s.SummarizeBatch(context.Background(), []BatchChunk{{ID: "a", Content: "x"}})
	require.Error(t, err)

	var le *llm.LLMError
	require.ErrorAs(t, err, &le)
	assert.False(t, le.Transient)
	assert.Equal(t, "parse_summaries_json", le.Reason)
}

// SPDX-License-Identifier: Apache-2.0

package llmproviders

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// cannedSummaryResponse is a minimal well-formed single-item response so
// SummarizeBatch reaches the Generate call and records the system prompt.
func cannedSummaryResponse() *llm.Response {
	return &llm.Response{
		Content:      `{"items":[{"summary":"x","keywords":["a","b","c"]}]}`,
		FinishReason: llm.FinishReasonEndTurn,
	}
}

// TestSummarizer_PromptSelection_TopicsPath asserts the prompt-carrying
// constructor sends defaultTopicSummarizePrompt. Fails if the topics path
// silently regresses to the code prompt.
func TestSummarizer_PromptSelection_TopicsPath(t *testing.T) {
	fake := llm.NewFakeClient(cannedSummaryResponse())
	s := NewLLMSummarizerWithPrompt(fake, llm.ProviderOpenAI, "fake-model", defaultTopicSummarizePrompt)

	_, err := s.SummarizeBatch(context.Background(), []BatchChunk{{ID: "a", Content: "x"}})
	require.NoError(t, err)

	calls := fake.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, defaultTopicSummarizePrompt, calls[0].Options.SystemPrompt,
		"topics path must send the topic-purpose prompt")
}

// TestSummarizer_PromptSelection_CodePath asserts the 3-arg code-path
// constructor sends defaultCodeSummarizePrompt. Fails if the code path picks
// up the topic prompt.
func TestSummarizer_PromptSelection_CodePath(t *testing.T) {
	fake := llm.NewFakeClient(cannedSummaryResponse())
	s := NewLLMSummarizer(fake, llm.ProviderOpenAI, "fake-model")

	_, err := s.SummarizeBatch(context.Background(), []BatchChunk{{ID: "a", Content: "x"}})
	require.NoError(t, err)

	calls := fake.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, defaultCodeSummarizePrompt, calls[0].Options.SystemPrompt,
		"code path must send the code-chunk prompt")
}

// TestPromptForConsumer pins the consumer→prompt mapping BuildSummarizerFor
// relies on: ConsumerTopics → topic prompt, every other consumer → code prompt.
func TestPromptForConsumer(t *testing.T) {
	assert.Equal(t, defaultTopicSummarizePrompt, promptForConsumer(config.ConsumerTopics),
		"ConsumerTopics must select the topic prompt")
	assert.Equal(t, defaultCodeSummarizePrompt, promptForConsumer(config.ConsumerSummarizer),
		"ConsumerSummarizer must select the code prompt")
}

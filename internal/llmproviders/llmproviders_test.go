// SPDX-License-Identifier: Apache-2.0

package llmproviders

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	// Blank import registers the OpenAI provider factory so BuildSummarizer
	// can construct a real client from the config read (production wires
	// these registrations from cmd/knowledge/main.go).
	_ "github.com/fulminate-io/knowledge-mcp/internal/llm/openai"
)

// TestBuildSummarizer_ThreadsBaseURL drives BuildSummarizer through an
// actual config read (the path TestLLMSummarizer_SummarizeBatch_Happy
// bypasses by injecting a FakeClient) with base_url set and NO API key on
// the resolved section. It proves the config-read → llm.Config construction
// path accepts a KEYLESS base_url config (the gates relaxed in Phase 4) and
// builds a client end-to-end.
func TestBuildSummarizer_ThreadsBaseURL(t *testing.T) {
	cfg := &config.Config{
		Default: config.Section{
			Provider: config.ProviderOpenAI,
			Model:    "gpt-5-mini",
			BaseURL:  "http://127.0.0.1:9/v1",
		},
	}
	t.Setenv("OPENAI_API_KEY", "")
	t.Cleanup(config.SetForTest(cfg))

	summ, err := BuildSummarizer(context.Background())
	require.NoError(t, err)
	require.NotNil(t, summ, "BuildSummarizer should construct a Summarizer from a keyless base_url config")
}

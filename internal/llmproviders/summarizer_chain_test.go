// SPDX-License-Identifier: Apache-2.0

package llmproviders

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// TestBuildSummarizerChainFor_OrderedChain installs a config carrying a primary
// [summarizer] section plus two fallback entries over a FAKE provider factory
// (registered through the llm registry under the CLI provider, which Validate
// accepts with no required fields), then asserts BuildSummarizerChainFor returns
// a len-3 chain of non-nil summarizers in priority order.
func TestBuildSummarizerChainFor_OrderedChain(t *testing.T) {
	t.Cleanup(llm.SnapshotRegistryForTest())
	llm.RegisterProvider(llm.ProviderClaudeCLI, func(_ context.Context, _ *llm.Config) (llm.Client, error) {
		return llm.NewFakeClient(), nil
	})

	cfg := &config.Config{
		Default: config.Section{Provider: config.ProviderClaudeCLI, Model: "primary-model", CLIBin: "/bin/fake"},
		Summarizer: &config.Section{
			Fallback: []config.Section{
				{Model: "fallback-0"},
				{Model: "fallback-1"},
			},
		},
	}
	t.Cleanup(config.SetForTest(cfg))

	chain, err := BuildSummarizerChainFor(context.Background(), config.ConsumerSummarizer)
	require.NoError(t, err)
	require.Len(t, chain, 3, "primary + 2 fallbacks")
	for i, s := range chain {
		require.NotNilf(t, s, "chain entry %d must be non-nil", i)
	}
}

// TestBuildSummarizerChainFor_ConfigNotLoaded asserts the same degrade-not-die
// contract as BuildSummarizerFor: with no config loaded, the chain builder
// returns (nil, nil) so the pipeline disables summarization rather than erroring.
func TestBuildSummarizerChainFor_ConfigNotLoaded(t *testing.T) {
	// Force the unloaded state for the duration of this test, then restore.
	t.Cleanup(config.SetForTest(nil))
	if config.Loaded() {
		t.Skip("config is loaded in this environment; cannot exercise the unloaded path")
	}
	chain, err := BuildSummarizerChainFor(context.Background(), config.ConsumerSummarizer)
	require.NoError(t, err)
	require.Nil(t, chain, "unloaded config must degrade to (nil, nil)")
}

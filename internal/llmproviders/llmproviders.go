// SPDX-License-Identifier: Apache-2.0

// Package llmproviders constructs the client-side LLM provider stack used
// by the client pipeline (summarize + embed). Mirrors the pre-split
// server-side buildSummarizer / buildEmbedder logic in
// domains/store/singleton.go, but runs in the stdio client process so
// the server stays LLM-key-free.
//
// Resolution order matches the server-side path:
//
//   - Summarizer: config.Active().Resolve(config.ConsumerSummarizer) → llm.NewClient.
//     Empty config returns (nil, nil) and the caller leaves the pipeline
//     unsummarized — same degrade-not-die semantics the server used.
//   - Embedder: config.VoyageAPIKey() → embed.NewVoyageBinaryEmbedder.
//     Empty key returns nil with a WARN log; vector search falls back to
//     BM25 only.
package llmproviders

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// BuildSummarizer constructs the client-side pipeline summarizer from
// config.Active() (the [summarizer] consumer). Returns (nil, nil) when config
// is unloaded — caller treats this as "summarization disabled" and the
// pipeline degrades gracefully.
//
// Production callers: cmd/knowledge runtime that wires the pipeline.
func BuildSummarizer(ctx context.Context) (Summarizer, error) {
	return BuildSummarizerFor(ctx, config.ConsumerSummarizer)
}

// BuildSummarizerFor constructs a summarizer for an arbitrary config consumer
// section — e.g. config.ConsumerTopics for the similarity lever's topic
// summaries, which can run a stronger model than the high-volume pipeline
// [summarizer]. Same degrade-not-die semantics as BuildSummarizer: unloaded
// config returns (nil, nil).
func BuildSummarizerFor(ctx context.Context, consumer config.Consumer) (Summarizer, error) {
	if !config.Loaded() {
		slog.Warn("llmproviders: config not loaded; summarization disabled", "consumer", consumer)
		return nil, nil
	}
	sec, err := config.Active().Resolve(consumer)
	if err != nil {
		return nil, fmt.Errorf("resolve %s config: %w", consumer, err)
	}
	provider := sec.Provider
	model := llm.Model(sec.Model)
	client, err := llm.NewClient(ctx, &llm.Config{
		Provider: provider,
		Model:    model,
		APIKey:   config.APIKeyForProvider(sec.Provider),
		BaseURL:  sec.BaseURL,
		CLIBin:   sec.CLIBin,
	})
	if err != nil {
		return nil, fmt.Errorf("build %s client: %w", consumer, err)
	}
	slog.Info("llmproviders: summarizer ready", "consumer", consumer, "provider", sec.Provider, "model", sec.Model)
	return NewLLMSummarizerWithPrompt(client, provider, model, promptForConsumer(consumer)), nil
}

// promptForConsumer selects the system prompt for a consumer section: the
// topics consumer (the similarity lever's thought-cluster summaries) gets the
// topic-purpose prompt; every other consumer (the high-volume code-chunk
// pipeline) gets the code prompt, keeping that path byte-for-byte unchanged.
func promptForConsumer(consumer config.Consumer) string {
	if consumer == config.ConsumerTopics {
		return defaultTopicSummarizePrompt
	}
	return defaultCodeSummarizePrompt
}

// BuildEmbedder constructs the client-side binary embedder from the
// configured Voyage key. Returns nil when no key is configured — vector
// search degrades to BM25-only, matching the prior server-side behavior.
func BuildEmbedder() embed.BinaryEmbedder {
	key := config.VoyageAPIKey()
	if key == "" {
		slog.Warn("llmproviders: no voyage_api_key — vector search disabled, BM25 only")
		return nil
	}
	slog.Info("llmproviders: voyage binary embedder ready")
	return embed.NewVoyageBinaryEmbedder(key)
}

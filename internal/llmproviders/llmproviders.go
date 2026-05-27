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

// BuildSummarizer constructs the client-side summarizer from
// config.Active(). Returns (nil, nil) when config is unloaded — caller
// treats this as "summarization disabled" and the pipeline degrades
// gracefully.
//
// Production callers: cmd/knowledge runtime that wires the pipeline.
func BuildSummarizer(ctx context.Context) (Summarizer, error) {
	if !config.Loaded() {
		slog.Warn("llmproviders: config not loaded; summarization disabled")
		return nil, nil
	}
	sec, err := config.Active().Resolve(config.ConsumerSummarizer)
	if err != nil {
		return nil, fmt.Errorf("resolve summarizer config: %w", err)
	}
	provider := sec.Provider
	model := llm.Model(sec.Model)
	client, err := llm.NewClient(ctx, &llm.Config{
		Provider: provider,
		Model:    model,
		APIKey:   config.APIKeyForProvider(sec.Provider),
		CLIBin:   sec.CLIBin,
	})
	if err != nil {
		return nil, fmt.Errorf("build summarizer client: %w", err)
	}
	slog.Info("llmproviders: summarizer ready", "provider", sec.Provider, "model", sec.Model)
	return NewLLMSummarizer(client, provider, model), nil
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

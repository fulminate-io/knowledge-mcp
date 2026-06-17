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
	return buildOneSummarizer(ctx, sec, consumer)
}

// BuildSummarizerChainFor constructs the ordered summarizer chain (primary
// followed by each configured fallback) for consumer from config.Active()'s
// ResolveChain. Each entry is built through the SAME buildOneSummarizer path as
// the single-section BuildSummarizerFor, so the two cannot drift; every entry
// gets the one shared prompt for the consumer (no per-entry override).
//
// Same degrade-not-die contract as BuildSummarizerFor: an unloaded config
// returns (nil, nil) so the pipeline disables summarization. A build error on
// ANY entry fails the whole wire (returns the error) — the wiring layer decides
// how to degrade, exactly as it does for the single-section build error.
func BuildSummarizerChainFor(ctx context.Context, consumer config.Consumer) ([]Summarizer, error) {
	entries, err := buildChainEntries(ctx, consumer)
	if err != nil {
		return nil, err
	}
	if entries == nil {
		return nil, nil
	}
	out := make([]Summarizer, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.summarizer)
	}
	return out, nil
}

// buildChainEntries resolves consumer's ordered chain (ResolveChain) and builds
// one fully-constructed entry (summarizer + client + labels) per section through
// the SHARED buildOneEntry path. It is the SINGLE resolve-and-build loop behind
// both BuildSummarizerChainFor (the []Summarizer view) and
// BuildSummarizerWithFallback (the facade that also needs clients + labels), so
// the two cannot drift. Degrade-not-die: an unloaded config returns (nil, nil).
func buildChainEntries(ctx context.Context, consumer config.Consumer) ([]builtEntry, error) {
	if !config.Loaded() {
		slog.Warn("llmproviders: config not loaded; summarization disabled", "consumer", consumer)
		return nil, nil
	}
	chain, err := config.Active().ResolveChain(consumer)
	if err != nil {
		return nil, fmt.Errorf("resolve %s chain: %w", consumer, err)
	}
	out := make([]builtEntry, 0, len(chain))
	for i, sec := range chain {
		e, err := buildOneEntry(ctx, sec, consumer)
		if err != nil {
			return nil, fmt.Errorf("build %s chain entry %d: %w", consumer, i, err)
		}
		out = append(out, e)
	}
	return out, nil
}

// buildOneSummarizer builds ONE summarizer from an already-resolved Section: the
// IDENTICAL llm.NewClient → NewLLMSummarizerWithPrompt sequence shared by the
// single-section (BuildSummarizerFor) and chain (BuildSummarizerChainFor) paths,
// so neither path can drift from the other. The prompt is the one shared prompt
// for the consumer; sec must already carry its inherited Provider/Model (the
// caller resolves via Resolve / ResolveChain).
func buildOneSummarizer(ctx context.Context, sec config.Section, consumer config.Consumer) (Summarizer, error) {
	e, err := buildOneEntry(ctx, sec, consumer)
	if err != nil {
		return nil, err
	}
	return e.summarizer, nil
}

// builtEntry is one fully-constructed chain entry: its summarizer plus the
// underlying client + provider/model labels. The client and labels are what the
// fallback facade needs to build a per-entry recovery probe and report the live
// active entry in status — beyond the bare Summarizer the []Summarizer chain
// builder returns.
type builtEntry struct {
	summarizer Summarizer
	client     llm.Client
	provider   config.Provider
	model      llm.Model
}

// buildOneEntry is the shared single-entry construction used by buildOneSummarizer
// (which discards the extra fields) and the fallback facade (which keeps the
// client + labels). Same llm.NewClient → NewLLMSummarizerWithPrompt sequence.
func buildOneEntry(ctx context.Context, sec config.Section, consumer config.Consumer) (builtEntry, error) {
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
		return builtEntry{}, fmt.Errorf("build %s client: %w", consumer, err)
	}
	slog.Info("llmproviders: summarizer ready", "consumer", consumer, "provider", sec.Provider, "model", sec.Model)
	return builtEntry{
		summarizer: NewLLMSummarizerWithPrompt(client, provider, model, promptForConsumer(consumer)),
		client:     client,
		provider:   provider,
		model:      model,
	}, nil
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

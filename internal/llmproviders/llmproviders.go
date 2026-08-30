// SPDX-License-Identifier: Apache-2.0

// Package llmproviders constructs the client-side LLM provider stack used
// by the client pipeline (summarize + embed). Mirrors the pre-split
// server-side buildSummarizer / buildEmbedder logic in
// cmd/knowledge-server/internal/store/singleton.go, but runs in the client process (cmd/knowledge) so
// the server stays LLM-key-free.
//
// Resolution order matches the server-side path:
//
//   - Summarizer: config.Active().Resolve(config.ConsumerSummarizer) → llm.NewClient.
//     Empty config returns (nil, nil) and the caller leaves the pipeline
//     unsummarized — same degrade-not-die semantics the server used.
//   - Embedder: config.Active().ResolveEmbedder() → embed.NewEmbedder,
//     with the axis's key resolved from its own provider via
//     config.APIKeyForEmbedProvider. A missing credential on an API
//     provider returns (nil, nil) with a WARN log and vector search falls
//     back to BM25 only; a malformed section or an unregistered arm
//     returns an error.
package llmproviders

import (
	"context"
	"fmt"
	"log/slog"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
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

// BuildEmbedder constructs the client-side binary embedder for one role by
// resolving the [embedder] section and dispatching through the embed
// registry.
//
// role is a CONSTRUCTION parameter, not a per-call one: the index pipeline
// embeds corpus text and the search-time embedder embeds query text, and
// each is a separate construction site, so the role never varies within
// one instance.
//
// THE ONE DEGRADE, AND WHY IT IS NOT A NEW FALLBACK: when the resolved
// provider is an API provider and BOTH its key and its base_url are empty,
// this returns (nil, nil) with a WARN. That is the PRE-EXISTING documented
// contract — "Empty means BM25-only search (no binary-vector embeddings,
// no cross-encoder rerank) — a documented, non-error degrade" (see
// config/keys.go). It is not introduced here and its scope does not
// widen. Every OTHER failure — a malformed [embedder] section, an unknown
// provider, a refused dtype or dimension, an unregistered arm — now
// returns an ERROR, where before this change there was nothing to be
// wrong about.
func BuildEmbedder(ctx context.Context, role embed.InputRole) (embed.BinaryEmbedder, error) {
	cfg, sec, err := embedConfigFor(role)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, nil // the documented BM25-only degrade; embedConfigFor logged why.
	}
	e, err := embed.NewEmbedder(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("build embedder: %w", err)
	}
	warnNonDefaultEmbedModel(sec)
	slog.Info("llmproviders: binary embedder ready", "provider", sec.Provider, "role", role)
	return e, nil
}

// embedConfigFor builds the embed.Config for one role — the SINGLE construction
// of that config, so everything derived from "what this client embeds with" is
// derived from the same object the embedder is built from.
//
// THAT IS THE WHOLE REASON IT EXISTS AS A FUNCTION. ResolvedEmbedIdentity below
// states, on the wire, the identity a graph will RECORD at its first embed. Had
// it re-derived its own embed.Config beside this one, the two derivations could
// disagree and the graph would be permanently recorded under an identity nothing
// actually embedded with. One construction, two readers.
//
// A NIL CONFIG WITH A NIL ERROR IS THE PRE-EXISTING BM25-ONLY DEGRADE, not a new
// one: config unloaded, or an API provider with neither a key nor a base_url.
// See BuildEmbedder's contract note above — its scope is unchanged, it is only
// evaluated here now.
func embedConfigFor(role embed.InputRole) (*embed.Config, config.EmbedSection, error) {
	if !config.Loaded() {
		slog.Warn("llmproviders: config not loaded; vector search disabled, BM25 only", "role", role)
		return nil, config.EmbedSection{}, nil
	}
	sec, err := config.Active().ResolveEmbedder()
	if err != nil {
		return nil, config.EmbedSection{}, fmt.Errorf("resolve embedder config: %w", err)
	}
	key := sec.ResolveEmbedKey()
	if sec.Provider.IsAPI() && key == "" && sec.BaseURL == "" {
		slog.Warn("llmproviders: no embed credential — vector search disabled, BM25 only", "provider", sec.Provider, "role", role)
		return nil, sec, nil
	}
	return &embed.Config{
		Provider:  sec.Provider,
		Model:     sec.Model,
		APIKey:    key,
		BaseURL:   sec.BaseURL,
		Dimension: sec.Dimension,
		Dtype:     sec.Dtype,
		InputRole: role,
	}, sec, nil
}

// ResolvedEmbedIdentity returns the wire identity the embedder this client
// builds for role will produce vectors under, or nil when no embedder can be
// built (the BM25-only degrade — nothing is embedded, so nothing is claimed).
//
// IT IS RESOLVED FROM THE CONSTRUCTED CONFIG, NOT FROM THE SECTION. The section
// leaves Model empty in the ordinary no-config case and the ARM fills its own
// default, so a section-derived identity would state an empty model for vectors
// produced by voyage-code-3 — and a graph records the first identity offered to
// it and is authoritative afterwards, so that wrong answer would be permanent
// short of an explicit migration. embed.ResolveIdentity fills the model through
// the same function every arm's factory fills from.
//
// THE CREDENTIAL DOES NOT TRAVEL. An identity names provider, model, dimension
// and dtype — what the vectors ARE — and deliberately carries neither the key
// nor the base_url, which are facts about how THIS machine reaches the provider.
func ResolvedEmbedIdentity(role embed.InputRole) (*knowledgev1.EmbedIdentity, error) {
	cfg, _, err := embedConfigFor(role)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, nil
	}
	id, err := embed.ResolveIdentity(cfg)
	if err != nil {
		return nil, fmt.Errorf("resolve embed identity: %w", err)
	}
	return &knowledgev1.EmbedIdentity{
		Provider: id.Provider.String(),
		Model:    id.Model,
		//nolint:gosec // a width from config.AcceptedEmbedDimensions, max 2048
		Dimension: int32(id.Dimension),
		Dtype:     id.Dtype,
	}, nil
}

// warnNonDefaultEmbedModel warns when the resolved embed model is not the
// arm's own default, because changing the model on a graph that already
// has vectors does NOT re-embed it: unchanged content hits the embed cache
// and the gap is suppressed, so the corpus keeps the OLD model's vectors
// while every new query vector comes from the new one, and the two are
// compared by hamming distance with no length guard to notice.
//
// WHAT THIS CANNOT CHECK, stated so the message does not imply otherwise:
// this warning fires at BUILD time, from config alone, with no graph in
// hand — so it claims only "this is not the arm's default model" and does
// NOT compare against any corpus. An empty model is the ordinary no-config
// case and is not warned about.
//
// THE UNDERLYING GAP IT WAS WRITTEN FOR IS CLOSED, and the wording above
// used to say so in stronger terms — that no stored record of a graph's
// embedder existed at all. One does now: a graph records its embed
// identity at first embed, the write path REFUSES a vector offered under a
// different one, and the query path resolves its embedder FROM that record
// (BuildEmbedderForIdentity). So the silent mixed-corpus this warning was a
// partial substitute for is prevented rather than warned about. The warning
// stays because it still catches the config-time mistake earlier than the
// refusal does.
//
// Only the voyage arm exposes a default model constant today; other arms
// carry their own unexported ones, so the check is scoped to the provider
// whose default is visible from here rather than guessing for the rest.
func warnNonDefaultEmbedModel(sec config.EmbedSection) {
	if sec.Model == "" || sec.Provider != config.EmbedProviderVoyage || sec.Model == embed.DefaultModel {
		return
	}
	slog.Warn("llmproviders: [embedder].model is not this arm's default — changing the model does NOT re-embed an existing graph; the stored vectors keep whatever model produced them and search quality degrades until the corpus is rebuilt. This build cannot tell which model produced the existing vectors, so this is not a comparison against your corpus, only a note that the configured model differs from the default",
		"configured_model", sec.Model, "arm_default_model", embed.DefaultModel, "provider", sec.Provider)
}

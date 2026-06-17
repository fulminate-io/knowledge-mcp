// SPDX-License-Identifier: Apache-2.0

package llmproviders

import (
	"context"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// fallbackSummarizer is the composite selection summarizer: it walks an ordered
// summarizer chain, calling the highest-priority HEALTHY entry and, on a
// failure the injected advance predicate says is non-deterministic-terminal,
// marking that entry limited and advancing to the next. It satisfies the
// package summarizer interface so it drops into the pipeline's summarizer seam
// transparently.
//
// SELECTION is STICKY: SummarizeBatch starts at the current ActiveIndex (the
// highest-priority healthy entry) — it does NOT re-probe a known-limited entry
// on every batch. A separate background prober (fallback_prober.go) is what
// re-checks limited entries and shifts traffic back once one recovers.
//
// IMPORT DIRECTION: llmproviders must NOT import pipeline (pipeline imports
// llmproviders — that would be a cycle), so the advance decision is INJECTED as
// `advance func(error) bool`, wired from bootstrap over
// pipeline.ShouldAdvanceFallback (which is !IsDeterministicTerminal(classify)).
// This keeps the pipeline classifier the single source of truth for what
// advances vs fails directly — no classification is duplicated here.
type fallbackSummarizer struct {
	entries []Summarizer
	health  *chainHealth
	advance func(error) bool
}

// Compile-time interface satisfaction.
var _ summarizer = (*fallbackSummarizer)(nil)

// newFallbackSummarizer constructs a fallbackSummarizer over the ordered
// entries, the shared health state, and the injected advance predicate. The
// chainHealth must have len == len(entries).
func newFallbackSummarizer(entries []Summarizer, health *chainHealth, advance func(error) bool) *fallbackSummarizer {
	return &fallbackSummarizer{entries: entries, health: health, advance: advance}
}

// SummarizeBatch summarizes chunks through the chain, advancing on a
// non-deterministic-terminal failure.
//
// Walk: start at the active (highest-priority healthy) index and try each
// subsequent entry in priority order:
//   - SUCCESS → return its results immediately (sticky; lower indices are left
//     untouched — the prober, not this path, restores them).
//   - advance(err) == true → mark this entry limited and continue to the next
//     entry (quota / rate-limit / overload / timeout class).
//   - advance(err) == false → return the error immediately WITHOUT marking the
//     entry limited (a deterministic-terminal class — a retry on another entry
//     would fail identically; the unchanged handleSummarizerError marks the
//     node failed directly).
//
// CHAIN EXHAUSTED: when every entry from the active index onward returned an
// advance error, the last error is returned so the EXISTING worker path
// (handleSummarizerError, worker.go) marks the node failed. Note this returns a
// TRANSIENT error only if the LAST entry's error was transient — an
// all-transient exhaustion therefore retries next tick (no durable mark), while
// a non-transient last error (e.g. the codex-quota subprocess_failed case,
// Transient:false) DOES durably mark after exhaustion. Both are intended.
func (s *fallbackSummarizer) SummarizeBatch(ctx context.Context, chunks []BatchChunk) (map[string]SummarizeResult, error) {
	start := s.health.ActiveIndex()
	if start < 0 {
		// Every entry is already limited (the prober has not yet restored any).
		// Return a terminal-shaped error so the node is failure-marked rather
		// than spun; the prober will re-open the chain on recovery.
		return nil, fmt.Errorf("summarizer chain exhausted: all %d entries limited", s.health.Len())
	}

	var lastErr error
	for i := start; i < len(s.entries); i++ {
		results, err := s.entries[i].SummarizeBatch(ctx, chunks)
		if err == nil {
			return results, nil
		}
		lastErr = err
		if !s.advance(err) {
			// Deterministic-terminal: do not advance, do not limit — fail directly.
			return nil, err
		}
		s.health.MarkLimited(i)
	}
	// Chain exhausted: every entry from start onward advanced. Return the last
	// error for the existing worker path to mark the node.
	return nil, lastErr
}

// FallbackChain is the exported assembly the pipeline wiring uses for a
// multi-entry summarizer chain: it bundles the composite selection Summarizer,
// the shared health state, the background prober, and a live active-entry
// accessor for status. It keeps chainHealth / fallbackSummarizer / healthProber
// unexported (the wiring layer reaches them only through this one seam).
type FallbackChain struct {
	summarizer *fallbackSummarizer
	health     *chainHealth
	prober     *healthProber
	labels     []string // per-entry "provider/model" for status
}

// BuildSummarizerWithFallback resolves the consumer's chain, builds one client +
// summarizer per entry, and assembles the composite selection summarizer + the
// background health-prober over a shared health state. The advance predicate
// (whether a given error should advance to the next entry) is injected by the
// caller (bootstrap wires pipeline.ShouldAdvanceFallback) so this package never
// imports pipeline.
//
// Degrade-not-die: an unloaded config returns (nil, nil) — the caller disables
// summarization. The probe interval is read from config.HealthProbeInterval
// (the prober defaults it to 10m when zero). A build error on any entry fails
// the whole wire.
func BuildSummarizerWithFallback(ctx context.Context, consumer config.Consumer, advance func(error) bool) (*FallbackChain, error) {
	built, err := buildChainEntries(ctx, consumer)
	if err != nil {
		return nil, err
	}
	if len(built) == 0 {
		return nil, nil
	}

	entries := make([]Summarizer, 0, len(built))
	probes := make([]func(context.Context) error, 0, len(built))
	labels := make([]string, 0, len(built))
	for _, e := range built {
		entries = append(entries, e.summarizer)
		probes = append(probes, newGeneratePingProbe(e.client, e.model))
		labels = append(labels, fmt.Sprintf("%s/%s", e.provider, e.model))
	}

	health := NewChainHealth(len(entries))
	return &FallbackChain{
		summarizer: newFallbackSummarizer(entries, health, advance),
		health:     health,
		prober:     newHealthProber(probes, health, config.Active().HealthProbeInterval),
		labels:     labels,
	}, nil
}

// Summarizer returns the composite selection summarizer to wire into the
// pipeline's summarizer seam.
func (c *FallbackChain) Summarizer() Summarizer { return c.summarizer }

// FirstSummarizer returns the highest-priority entry's bare summarizer. The
// wiring layer uses it for the len==1 (no-fallback) case so a single-entry
// config keeps the exact pre-fallback behavior — direct summarizer, no
// selection wrapper, no prober.
func (c *FallbackChain) FirstSummarizer() Summarizer { return c.summarizer.entries[0] }

// Len reports the number of entries in the chain.
func (c *FallbackChain) Len() int { return len(c.labels) }

// RunHealthProbeLoop runs the background prober until ctx is cancelled. Spawn it
// in a goroutine alongside the pipeline's other background loops so it shares the
// runtime lifecycle and leaks nothing on shutdown.
func (c *FallbackChain) RunHealthProbeLoop(ctx context.Context) {
	c.prober.RunHealthProbeLoop(ctx)
}

// ActiveEntry returns the "provider/model" label of the live active (highest-
// priority healthy) entry, or "" when the chain is fully exhausted. Status
// rendering reads this so operators see the CURRENT summarizer, not the static
// configured primary.
func (c *FallbackChain) ActiveEntry() string {
	i := c.health.ActiveIndex()
	if i < 0 || i >= len(c.labels) {
		return ""
	}
	return c.labels[i]
}

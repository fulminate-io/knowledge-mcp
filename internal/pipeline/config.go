// SPDX-License-Identifier: Apache-2.0

// Package pipeline implements the LLM pipeline v2: long-lived per-graph
// collectors discover nodes needing summarization or embedding, push work
// onto global channels, and worker pools call the LLM/embedder backends.
//
// Architecture (3-layer producer/consumer):
//   - Per-graph collectors: 2 goroutines per loaded graph (summary + embed),
//     ticking every Tick interval. Read-only discovery via the existing
//     NodeIDsBySummaryGap / NodeIDsByEmbedGap shims.
//   - Global channels: SummaryWork / EmbedWork buffered to ChannelSize.
//     Collectors block on full channels — natural backpressure.
//   - Worker pools: SummaryWorkers / EmbedWorkers goroutines per system,
//     pulling batches from the dispatcher's sub-channel and writing back
//     Summary / vector via the existing setter paths.
//
// Cache-blindness — why the fresh-summary path needs its own synthetic gate:
// the gap-scan a collector runs first consults a content-hash summary cache
// (the implementation is summaryCacheHit in the server store's
// composite_db_pipeline_gaps_cache.go; an equivalent hit path exists for the
// hosted backend, described here backend-neutrally). A byte-identical
// node — re-collected with the same content, or merged from a branch that
// already earned a summary — is served from that cache and SKIPPED: it never
// reaches the summary worker, never reaches any LLM provider. This is CORRECT,
// and on any established graph the overwhelming majority of nodes take it. The
// consequence is that the FRESH path — the worker actually calling a provider,
// threading the structured-output schema onto the wire, and parsing the reply —
// is exercised almost never in normal operation. A provider whose translate or
// parse layer regressed could ship and stay invisible for a long time, because
// cache-replay keeps the regressed fresh path from ever running on a mature
// graph (this is the mechanism behind the recorded Markdown-instead-of-JSON
// regression: the cache hid it). The per-provider conformance suite in
// cmd/knowledge/internal/llmproviders (conformance_test.go and its siblings) is
// the SYNTHETIC fresh-path coverage that closes this gap — it drives every
// registered provider's real fresh-summary path (wire shape, success parse, and
// loud failure on a Markdown reply) against fake endpoints, so a fresh-path
// regression is caught by that suite rather than by users on a cache miss.
//
// See the design doc, Section C, for the full design.
package pipeline

import "time"

// DefaultCircuitBreakerThreshold is the default PER-AXIS number of consecutive
// errored LLM calls (with zero intervening success on THAT axis) that latches
// that axis's workers paused. Each axis (summary, embed) owns its own breaker and
// its own counter. 20 ≈ half the default in-flight worker set per axis (25
// summary / 20 embed), so it reads as a clear majority-failing signal for that
// axis while still being unreachable from an isolated failure: any single success
// on the axis zeroes that axis's counter, so only a genuine zero-success storm on
// the axis climbs to 20.
const DefaultCircuitBreakerThreshold = 20

// DefaultDeterministicFastTripThreshold is the number of CONSECUTIVE, SAME-CLASS,
// deterministic-terminal batch failures (a class IsDeterministicTerminal reports
// true for: ClassParse / ClassInvalidRequest / ClassTruncation) that trips the
// breaker immediately — well before the class-agnostic zero-success window
// (DefaultCircuitBreakerThreshold, 20) would. A deterministic-terminal failure
// reproduces identically for the same batch + config, so the SECOND identical
// failure proves the third would fail too: there is no value in burning the full
// 20-call window, where every call is a full-billed discarded API round. 2 is the
// minimum that still demands a confirming repeat — a single isolated parse failure
// must NOT fast-trip, only a same-class streak of 2.
const DefaultDeterministicFastTripThreshold = 2

// Config controls the pipeline's worker counts, batch sizes, channel
// capacities, and tick cadence. Zero values fall back to defaults via the
// *OrDefault accessors. Defaults match ticket Section C — channel=10000,
// batch summary=20 / embed=100, workers summary=20 / embed=20, tick=250ms.
type Config struct {
	// SummaryChannelSize is the SummaryWork channel buffer. Default 10000.
	SummaryChannelSize int

	// SummaryBatchSize is the number of SummaryWork items each summary
	// worker processes per LLM call. Default 20 — matches the existing
	// summarize_pipeline batch shape.
	SummaryBatchSize int

	// SummaryWorkers is the count of summary worker goroutines. Default 25.
	SummaryWorkers int

	// EmbedChannelSize is the EmbedWork channel buffer. Default 10000.
	EmbedChannelSize int

	// EmbedBatchSize is the number of EmbedWork items each embed worker
	// processes per embedder call. Default 100 — under voyageEmbedder's
	// internal 128 chunk-size to avoid double-batching.
	EmbedBatchSize int

	// EmbedWorkers is the count of embed worker goroutines. Default 20.
	EmbedWorkers int

	// EmbedRPM is the max embed API requests per minute across ALL embed
	// workers — the PROACTIVE companion to the reactive ErrBackoff gate. It
	// paces dispatch so the opening 20×100 worker burst respects a low-tier
	// Voyage account's rate BEFORE the first 429 lands. 0 = unlimited =
	// current behavior (the gate is disabled and adds zero hot-path overhead).
	EmbedRPM int

	// Tick is the per-graph collector poll interval. Default 250ms. If a
	// tick takes longer than this to push (channel full), the next tick
	// is delayed by the channel send block — backpressure is implicit.
	Tick time.Duration

	// CloudTick is the per-graph collector poll interval used when the
	// collector is bound to a REMOTE (logged-in) backend. Default 5s. A
	// local loopback scan is cheap, but a remote scan is a network round
	// trip that may be subject to a remote per-IP rate limit — at the 250ms
	// local cadence, N graphs × 2 axes can saturate such a limit. The
	// collector picks Tick vs CloudTick at registration based on the
	// resolved backend's login state; a login flip re-registers, so the
	// cadence stays correct across flips. This is the BASE (busy) cadence;
	// an idle graph backs off from here toward IdleTickMax.
	CloudTick time.Duration

	// IdleTickMax caps the idle-backoff interval for a REMOTE-bound collector.
	// Default 1h. When a scan returns no work and a stable dirty-gen, the
	// collector grows its poll interval geometrically from the base (CloudTick)
	// up to this ceiling, so a fully-drained graph costs ~one scan/hour instead
	// of one every base tick. Any discovered work snaps the interval straight
	// back to the base — and a collect explicitly WAKES every collector
	// (Pipeline.WakeAll) so a freshly-collected graph re-scans within one base
	// tick instead of waiting out its (now hour-long) idle interval. Local-bound
	// collectors do NOT idle-back-off (loopback scans are cheap and
	// latency-to-first-summary should stay low), so this knob is remote-only.
	IdleTickMax time.Duration

	// ErrBackoffBase is the first-failure delay of the shared LLM-error
	// backoff gate. Default 500ms. Each consecutive transient failure
	// doubles the window up to ErrBackoffMax.
	ErrBackoffBase time.Duration

	// ErrBackoffMax caps the exponential backoff window. Default 60s.
	ErrBackoffMax time.Duration

	// CircuitBreakerThreshold is the PER-AXIS number of consecutive errored LLM
	// calls (with zero intervening success on that axis) that latches the TRIPPING
	// axis's workers PAUSED — the summary and embed axes each have an independent
	// breaker at this threshold, so a storm on one axis pauses only that axis.
	// Default 20. The breaker is the latched companion to the self-clearing
	// ErrBackoff gate: once tripped it stays paused until a human runs
	// resume_pipeline — there is no self-heal.
	CircuitBreakerThreshold int

	// SummaryProvider and EmbedProvider carry each axis's LLM provider identity
	// (e.g. "anthropic" for summaries, "voyage" for embeddings). They are used
	// ONLY by the shared-cause escalation (escalation.go) to decide whether a
	// same-provider cross-trip applies: an auth/quota auto-trip on one axis
	// propagates to the other ONLY when both providers are equal and non-empty.
	// Empty = unknown = that axis never participates in escalation (the safe
	// default, and the test default). Provider-distinct axes never cross-trip.
	SummaryProvider string
	EmbedProvider   string
}

// SummaryChannelSizeOrDefault returns cfg.SummaryChannelSize or 10000.
func (c Config) SummaryChannelSizeOrDefault() int {
	if c.SummaryChannelSize > 0 {
		return c.SummaryChannelSize
	}
	return 10000
}

// SummaryBatchSizeOrDefault returns cfg.SummaryBatchSize or 20.
func (c Config) SummaryBatchSizeOrDefault() int {
	if c.SummaryBatchSize > 0 {
		return c.SummaryBatchSize
	}
	return 20
}

// SummaryWorkersOrDefault returns cfg.SummaryWorkers or 25.
func (c Config) SummaryWorkersOrDefault() int {
	if c.SummaryWorkers > 0 {
		return c.SummaryWorkers
	}
	return 25
}

// EmbedChannelSizeOrDefault returns cfg.EmbedChannelSize or 10000.
func (c Config) EmbedChannelSizeOrDefault() int {
	if c.EmbedChannelSize > 0 {
		return c.EmbedChannelSize
	}
	return 10000
}

// EmbedBatchSizeOrDefault returns cfg.EmbedBatchSize or 100.
func (c Config) EmbedBatchSizeOrDefault() int {
	if c.EmbedBatchSize > 0 {
		return c.EmbedBatchSize
	}
	return 100
}

// EmbedWorkersOrDefault returns cfg.EmbedWorkers or 20.
func (c Config) EmbedWorkersOrDefault() int {
	if c.EmbedWorkers > 0 {
		return c.EmbedWorkers
	}
	return 20
}

// EmbedRPMOrDefault returns cfg.EmbedRPM verbatim — 0 is a MEANINGFUL value
// (unlimited = gate disabled), so unlike the >0-floor accessors there is no
// default substitution. The companion gate treats rpm <= 0 as disabled.
func (c Config) EmbedRPMOrDefault() int {
	return c.EmbedRPM
}

// TickOrDefault returns cfg.Tick or 250ms.
func (c Config) TickOrDefault() time.Duration {
	if c.Tick > 0 {
		return c.Tick
	}
	return 250 * time.Millisecond
}

// CloudTickOrDefault returns cfg.CloudTick or 5s — the base (busy) poll cadence
// for collectors bound to a remote backend (keeps remote scan volume far under
// any remote per-IP rate limit).
func (c Config) CloudTickOrDefault() time.Duration {
	if c.CloudTick > 0 {
		return c.CloudTick
	}
	return 5 * time.Second
}

// IdleTickMaxOrDefault returns cfg.IdleTickMax or 1h — the idle-backoff ceiling
// for a remote-bound collector (a fully-drained graph costs ~one scan/hour; a
// collect wakes it back to the base cadence via Pipeline.WakeAll).
func (c Config) IdleTickMaxOrDefault() time.Duration {
	if c.IdleTickMax > 0 {
		return c.IdleTickMax
	}
	return time.Hour
}

// ErrBackoffBaseOrDefault returns cfg.ErrBackoffBase or 500ms.
func (c Config) ErrBackoffBaseOrDefault() time.Duration {
	if c.ErrBackoffBase > 0 {
		return c.ErrBackoffBase
	}
	return 500 * time.Millisecond
}

// ErrBackoffMaxOrDefault returns cfg.ErrBackoffMax or 60s.
func (c Config) ErrBackoffMaxOrDefault() time.Duration {
	if c.ErrBackoffMax > 0 {
		return c.ErrBackoffMax
	}
	return 60 * time.Second
}

// CircuitBreakerThresholdOrDefault returns cfg.CircuitBreakerThreshold or
// DefaultCircuitBreakerThreshold (20).
func (c Config) CircuitBreakerThresholdOrDefault() int {
	if c.CircuitBreakerThreshold > 0 {
		return c.CircuitBreakerThreshold
	}
	return DefaultCircuitBreakerThreshold
}

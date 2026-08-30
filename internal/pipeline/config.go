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

import (
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

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

// LeaseProviderCalls is the CEILING term of the lease derivation: at most this
// many provider calls' worth of items are taken by one worker in one lease, so
// the lease can never grow into an unbounded in-flight content buffer however
// large a scan page the server serves or however few workers are configured.
const LeaseProviderCalls = 10

// maxClientScanPage is a client-side MIRROR of the server's per-request
// pipeline-scan ceiling. THE AUTHORITY IS SERVER-SIDE — maxPipelineScanItems in
// cmd/knowledge-server/internal/bootstrap/engine_limits.go — and it is
// unexported there, so this constant exists ONLY to make the lease derivation
// legible. It is deliberately NOT used as a request clamp: the server clamps
// every scan regardless and reports the truncation, so a client that over-asks
// is corrected by the server rather than by this number. Keeping it out of the
// request path is what stops it becoming a second, silently-disagreeing
// authority on the same bound.
const maxClientScanPage = 10000

// Config controls the pipeline's worker counts, batch sizes, channel
// capacities, and tick cadence. Zero values fall back to defaults via the
// *OrDefault accessors. Defaults match ticket Section C — channel=10000,
// batch summary=20 / embed=100, workers summary=25 / embed=20, tick=250ms.
//
// BATCH SIZE AND LEASE SIZE ARE TWO DIFFERENT UNITS and both are configured
// here. The *BatchSize knobs are the PROVIDER-CALL cap — how many items go into
// one embedder or summarizer call. The *LeaseSize knobs are the WRITEBACK unit —
// how many items one worker takes, processes as N provider calls, and writes
// back in ONE transaction. Lease size defaults to a DERIVATION over the batch
// size and the worker count rather than a literal; see the two accessors.
type Config struct {
	// SummaryChannelSize is the SummaryWork channel buffer. Default 10000.
	SummaryChannelSize int

	// SummaryBatchSize is the PROVIDER-CALL cap on the summary axis: the number
	// of SummaryWork items that go into ONE summarizer call. Default 20 —
	// matches the existing summarize_pipeline batch shape. On this axis the cap
	// is a PROMPT bound, not merely a request-size one: the summarizer puts
	// every chunk of a call into one prompt.
	SummaryBatchSize int

	// SummaryLeaseSize is the number of SummaryWork items one summary worker
	// takes per lease — the WRITEBACK unit, processed as ceil(lease/batch)
	// summarizer calls and written back in ONE transaction. Zero means DERIVE;
	// see SummaryLeaseSizeOrDefault.
	SummaryLeaseSize int

	// SummaryWorkers is the count of summary worker goroutines. Default 25.
	SummaryWorkers int

	// EmbedChannelSize is the EmbedWork channel buffer. Default 10000.
	EmbedChannelSize int

	// EmbedBatchSize is the PROVIDER-CALL cap on the embed axis: the number of
	// EmbedWork items that go into ONE embedder call. Default 100 — under
	// voyageEmbedder's internal 128 chunk-size to avoid double-batching.
	EmbedBatchSize int

	// EmbedLeaseSize is the number of EmbedWork items one embed worker takes per
	// lease — the WRITEBACK unit, processed as ceil(lease/batch) embedder calls
	// and written back in ONE transaction. Zero means DERIVE; see
	// EmbedLeaseSizeOrDefault.
	EmbedLeaseSize int

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

	// EmbedDtype is the RESOLVED [embedder] representation this client's vectors
	// are produced in — searchengine.DtypeUbinary or DtypeFloat32. It rides with
	// every HNSW document the ship path builds, because a vector format derives a
	// segment's dtype from its documents and therefore decides which metric ranks
	// them.
	//
	// IT IS THE CONFIG'S ANSWER, NOT THE GRAPH'S, and the distinction is worth
	// stating: this pipeline embeds under one resolved [embedder] section, so the
	// bytes it ships ARE that section's representation. A graph whose recorded
	// identity disagrees with that section is a mismatch to be reported by the
	// identity machinery, not something for this field to second-guess.
	//
	// Empty is read as ubinary by the format, matching the on-disk tag-0
	// convention, so a Config that never sets it behaves exactly as before.
	EmbedDtype string

	// EmbedIdentity is the RESOLVED identity this client's vectors are produced
	// under — provider, model, dimension and dtype — stated on every
	// vector-bearing writeback item and on every embed-axis scan. nil when no
	// embedder is wired, which is also the only state in which no vector is
	// produced, so a nil here never leaves a vector unlabeled.
	//
	// IT IS RESOLVED FROM THE CONSTRUCTED EMBEDDER CONFIG, not assembled here:
	// llmproviders.ResolvedEmbedIdentity reads the same embed.Config the embedder
	// is built from, and the model is filled by the same function each arm fills
	// its own default from. A parallel constant would state one model while the
	// arm embedded under another, and because a graph RECORDS the first identity
	// offered to it, that mistake is permanent short of an explicit migration.
	//
	// STATING IT IS WHAT LETS A GRAPH BOOTSTRAP AT ALL. The server records a
	// first-embed identity only when the batch OFFERS one; a vector writeback that
	// states nothing takes the server's identity-less path, so a graph with no
	// record can never acquire one and every vector-bearing write into it is
	// refused for having no recorded shape.
	EmbedIdentity *knowledgev1.EmbedIdentity
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

// SummaryLeaseSizeOrDefault returns cfg.SummaryLeaseSize, or the DERIVED lease
// size when it is zero. The derivation reads, in the order the expression
// evaluates it:
//
//   - FLOOR — one provider call. A lease smaller than one summarizer call would
//     make the lease the binding constraint on prompt size, which is the batch
//     size's job.
//   - OPERATIVE — what one server scan page can give EVERY worker:
//     maxClientScanPage / SummaryWorkersOrDefault(). Asking for more than this
//     per worker cannot fill every worker from one page, so it would trade
//     provider concurrency for batching rather than adding batching.
//   - CEILING — LeaseProviderCalls provider calls' worth of items.
//
// At the shipped defaults (workers=25, batch=20) this is 200: a 10x reduction in
// writeback transactions, and therefore in acquisitions of the server's
// per-graph advisory write mutex, with provider concurrency unchanged.
//
// PER-WORKER MEMORY BOUND: SummaryLeaseSizeOrDefault() * maxItemBytes * SummaryWorkersOrDefault()
//
// maxItemBytes is the largest server-composed SummarizeText this axis carries.
// No byte figure is stated because no measurement of it exists in this tree, and
// inventing one would be the magic number this derivation exists to avoid. Note
// this bound covers the WORKERS' in-flight content only: the SummaryWork channel
// buffer (SummaryChannelSize, 10000 items) is a separate and larger term.
func (c Config) SummaryLeaseSizeOrDefault() int {
	if c.SummaryLeaseSize > 0 {
		return c.SummaryLeaseSize
	}
	batch := c.SummaryBatchSizeOrDefault()
	return min(max(maxClientScanPage/c.SummaryWorkersOrDefault(), batch), LeaseProviderCalls*batch)
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

// EmbedLeaseSizeOrDefault returns cfg.EmbedLeaseSize, or the DERIVED lease size
// when it is zero. Same three terms, in the order the expression evaluates them:
//
//   - FLOOR — one provider call (EmbedBatchSizeOrDefault items).
//   - OPERATIVE — what one server scan page can give EVERY worker:
//     maxClientScanPage / EmbedWorkersOrDefault().
//   - CEILING — LeaseProviderCalls provider calls' worth of items.
//
// At the shipped defaults (workers=20, batch=100) this is 500: a 5x reduction in
// writeback transactions with provider concurrency unchanged at 20 in-flight
// embedder calls. A FIXED 1000 — the figure the originating request named —
// would need 20,000 items from a scan page the server clamps at 10,000, so half
// the workers would get no lease and concurrent provider calls would halve. It
// would also sit exactly ON the server's copy-from switchover threshold, one
// item away from a write path the batched writeback does not cover. Raising the
// server's page ceiling to restore the full 10x is a decision for whoever owns
// that constant, not something this derivation takes on its own.
//
// PER-WORKER MEMORY BOUND: EmbedLeaseSizeOrDefault() * maxItemBytes * EmbedWorkersOrDefault()
//
// maxItemBytes is the largest server-composed EmbedText this axis carries; no
// byte figure is stated for the reason given on the summary accessor. The
// EmbedWork channel buffer (EmbedChannelSize, 10000 items) is a separate and
// larger term this bound does not cover. The lease also raises the collector's
// transient scan-response allocation, from a fixed 2,000 items to at most
// maxClientScanPage.
func (c Config) EmbedLeaseSizeOrDefault() int {
	if c.EmbedLeaseSize > 0 {
		return c.EmbedLeaseSize
	}
	batch := c.EmbedBatchSizeOrDefault()
	return min(max(maxClientScanPage/c.EmbedWorkersOrDefault(), batch), LeaseProviderCalls*batch)
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

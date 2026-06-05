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
// See the design doc, Section C, for the full design.
package pipeline

import "time"

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

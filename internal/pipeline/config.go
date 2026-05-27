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
// See ticket 204548103831bafc26cc29ff50262a16 Section C for the full design.
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

	// Tick is the per-graph collector poll interval. Default 250ms. If a
	// tick takes longer than this to push (channel full), the next tick
	// is delayed by the channel send block — backpressure is implicit.
	Tick time.Duration

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

// TickOrDefault returns cfg.Tick or 250ms.
func (c Config) TickOrDefault() time.Duration {
	if c.Tick > 0 {
		return c.Tick
	}
	return 250 * time.Millisecond
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

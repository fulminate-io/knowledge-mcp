// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"log/slog"
	"time"
)

// partialFlushInterval is the maximum age of a partial batch before the
// dispatcher emits it without waiting to fill. Without this timeout the
// dispatcher would stall indefinitely when work trickles in below
// batchSize between bursts (observed against a live server: embed work
// dispatched 99 items per burst, batchSize=100, dispatcher held all 99
// in local memory waiting for the 100th that never came). 250ms matches
// the collector's default tick so a flush always lands within one tick.
const partialFlushInterval = 250 * time.Millisecond

// runSummaryDispatcher batches incoming SummaryWork items into slices of
// batchSize and forwards them to batchOut. One dispatcher per channel,
// spawned by Pipeline.Start (Phase 6 wires the goroutine launch). Exits
// when in is closed; emits any partial-batch in-flight before closing
// batchOut.
//
// Backpressure: send to batchOut blocks if no worker is reading. The
// dispatcher does not buffer beyond batchSize — that's the worker pool's
// shared sub-channel's job.
//
// Partial-batch flush: a partial batch ages out after partialFlushInterval
// even if no new items arrive. Without this, sub-batchSize bursts stall
// indefinitely (no flow-control feedback to the collector loop).
//
// Pattern intentionally matches runEmbedDispatcher below — the only
// difference is the work-item type (SummaryWork vs EmbedWork). They are
// kept as two functions rather than one generic to keep the per-batch
// emission path one-line-readable in profiles.
func runSummaryDispatcher(ctx context.Context, in <-chan SummaryWork, batchOut chan<- []SummaryWork, batchSize int) {
	defer close(batchOut)
	if batchSize < 1 {
		batchSize = 1
	}
	batch := make([]SummaryWork, 0, batchSize)
	flushTimer := time.NewTimer(partialFlushInterval)
	defer flushTimer.Stop()
	// emitNoSelect performs an unconditional send; used during shutdown
	// drain so a canceled context does not skip the final partial batch.
	emitNoSelect := func() {
		if len(batch) == 0 {
			return
		}
		batchOut <- batch
		batch = make([]SummaryWork, 0, batchSize)
	}
	emit := func() {
		if len(batch) == 0 {
			return
		}
		size := len(batch)
		select {
		case <-ctx.Done():
			slog.Debug("pipeline.summary dispatcher: ctx done, dropping partial batch", "size", size)
		case batchOut <- batch:
			slog.Debug("pipeline.summary dispatcher: emitted batch", "size", size)
		}
		batch = make([]SummaryWork, 0, batchSize)
	}
	for {
		select {
		case <-ctx.Done():
			emitNoSelect()
			return
		case <-flushTimer.C:
			emit()
			flushTimer.Reset(partialFlushInterval)
		case w, ok := <-in:
			if !ok {
				emitNoSelect()
				return
			}
			batch = append(batch, w)
			if len(batch) >= batchSize {
				emit()
				if !flushTimer.Stop() {
					select {
					case <-flushTimer.C:
					default:
					}
				}
				flushTimer.Reset(partialFlushInterval)
			}
		}
	}
}

// runEmbedDispatcher is the embed-side mirror of runSummaryDispatcher.
func runEmbedDispatcher(ctx context.Context, in <-chan EmbedWork, batchOut chan<- []EmbedWork, batchSize int) {
	defer close(batchOut)
	if batchSize < 1 {
		batchSize = 1
	}
	batch := make([]EmbedWork, 0, batchSize)
	flushTimer := time.NewTimer(partialFlushInterval)
	defer flushTimer.Stop()
	emitNoSelect := func() {
		if len(batch) == 0 {
			return
		}
		batchOut <- batch
		batch = make([]EmbedWork, 0, batchSize)
	}
	emit := func() {
		if len(batch) == 0 {
			return
		}
		select {
		case <-ctx.Done():
		case batchOut <- batch:
		}
		batch = make([]EmbedWork, 0, batchSize)
	}
	for {
		select {
		case <-ctx.Done():
			emitNoSelect()
			return
		case <-flushTimer.C:
			emit()
			flushTimer.Reset(partialFlushInterval)
		case w, ok := <-in:
			if !ok {
				emitNoSelect()
				return
			}
			batch = append(batch, w)
			if len(batch) >= batchSize {
				emit()
				if !flushTimer.Stop() {
					select {
					case <-flushTimer.C:
					default:
					}
				}
				flushTimer.Reset(partialFlushInterval)
			}
		}
	}
}

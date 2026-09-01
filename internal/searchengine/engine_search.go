// SPDX-License-Identifier: Apache-2.0

package searchengine

// engine_search.go — the lock-free read path, split out of engine.go so the
// per-segment corruption boundary and the fan-out it guards read together.

import (
	"runtime"
	"sync"
)

// Search runs a lock-free, parallel cross-segment query. It loads the immutable
// set with a SINGLE atomic load (NO mutex, NO RLock — activeMu is never touched
// here), fans out one goroutine per segment bounded by NumCPU, each writing a
// preallocated result slot (no shared-slice contention), then merges the global
// top-k. The liveDocs accept filter excludes deleted ids. The only
// synchronization is the atomic load + the fan-out WaitGroup/semaphore.
func (e *SegmentedIndex[Q, S]) Search(q Q, k int) []Hit {
	set := e.set.Load()
	if len(set.entries) == 0 || k <= 0 {
		return nil
	}

	results := make([][]Hit, len(set.entries))
	sem := make(chan struct{}, runtime.NumCPU())
	var wg sync.WaitGroup
	for i, entry := range set.entries {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, entry *segmentEntry[Q, S]) {
			defer wg.Done()
			defer func() { <-sem }()

			// THE PER-SEGMENT CORRUPTION BOUNDARY. A format raises
			// CorruptSegmentError from deep in its read path when the stored
			// bytes violate an invariant it guarantees. Before this boundary
			// existed that panic crossed this goroutine and killed the process
			// — and since the daemon is restarted automatically, one bad file
			// in one graph crashed every retry and made the WHOLE corpus
			// unserviceable until a human quarantined it.
			//
			// Contained here it costs exactly this segment: results[i] stays
			// nil, every other segment's goroutine is unaffected, and the owner
			// is told which id to quarantine and re-fetch.
			//
			// THE TWO DEFERS ARE ORDERED, AND THE ORDER IS LOAD-BEARING. defers
			// run last-registered-first, so the reporting closure is registered
			// FIRST and therefore runs SECOND — after catchCorrupt has recovered
			// and populated corrupt. catchCorrupt is deferred DIRECTLY rather
			// than wrapped in a closure because recover() only stops a panic
			// when the deferred function calls it itself; one more frame and it
			// returns nil and the process still dies.
			// containCorrupt IS THE IDIOM, and this call site is why it exists as
			// one. The two defers it encapsulates were written out by hand here,
			// and the pattern has already shipped a silent disarm once — a
			// delegation that moved recover() one frame further down, where it
			// returns nil and the panic keeps unwinding. Every path that owns one
			// segment's read reaches for the helper so that mistake is available
			// in exactly one place.
			accept := func(id ExternalID) bool {
				ord, ok := entry.members[id]
				return ok && entry.live.Live(ord)
			}
			_ = e.containCorrupt(entry.meta.ID, func() error {
				results[i] = entry.payload.Search(q, set.stats, k, accept)
				return nil
			})
		}(i, entry)
	}
	wg.Wait()

	return mergeTopK(results, k)
}

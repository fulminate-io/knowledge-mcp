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
// ResidentSegmentFanoutBudget is the resident segment count the search fan-out's
// latency budget was measured at, and the count any resident-growth bound above
// this engine must agree with.
//
// SearchAccepting below fans out ONE GOROUTINE PER RESIDENT SEGMENT, so the
// resident count IS what a query pays while a graph is being indexed — and an
// externally reported host reached 16,761 resident segments between drains. The
// per-machine latency readings that count was chosen against are recorded in this
// package's search benchmarks (engine_bench_test.go); the fan-out itself is
// asserted as a count by TestSearchFansOutOncePerResidentSegment, because a
// wall-clock ratio measures the runner more than it measures the code.
//
// IT IS EXPORTED SO THE BOUND AND THE MEASUREMENT CANNOT DRIFT APART. The cap that
// keeps the resident set here lives in segmentdist, which imports this package;
// its own test asserts equality with this constant, so moving either one alone
// turns that test red instead of leaving a latency budget measured at a count
// production no longer permits.
const ResidentSegmentFanoutBudget = 4096

func (e *SegmentedIndex[Q, S]) Search(q Q, k int) []Hit {
	return e.SearchAccepting(q, k, nil)
}

// SearchAccepting is Search with a CALLER-SUPPLIED accept predicate composed on
// top of the liveness one. A nil predicate is exactly Search.
//
// IT IS APPLIED INSIDE TOP-K COLLECTION, WHICH IS THE WHOLE POINT. The predicate
// reaches each format's own collection loop through the same accept parameter
// the liveness filter rides, so a rejected candidate never consumes a slot in k
// and a narrow subset returns its full top-N rather than whatever survived a
// post-rank trim of a corpus-wide ranking. A caller that filtered the RESULT
// would get a short list and no way to tell a small subset from a deep one.
//
// THE COMPOSITION ORDER IS liveness FIRST. A deleted document is not a candidate
// at all, so the caller's predicate is never asked about one; that keeps the
// caller's predicate a pure membership question and stops it having to know
// about liveness to be correct.
func (e *SegmentedIndex[Q, S]) SearchAccepting(q Q, k int, accepts func(ExternalID) bool) []Hit {
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
				if !ok || !entry.live.Live(ord) {
					return false
				}
				return accepts == nil || accepts(id)
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

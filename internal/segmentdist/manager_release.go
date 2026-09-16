// SPDX-License-Identifier: Apache-2.0

// manager_release.go carries the SEAL path's half of mapping republication: after
// the durability write has made every resident id durable, the payloads that still
// hold their encoder output are republished over their stored files in one swap,
// under a bound on what a failure may leave for the search path to walk.
//
// The MERGE path's half — one output, one announcement — and the pending state both
// callers share are in manager_remap.go.

package segmentdist

import (
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// releasePendingCap bounds how many segments ONE release pass may leave pending,
// counting what is already there.
//
// IT IS A BOUND ON PER-SEARCH WORK, not on memory. drainRemapPending runs after
// every consumer search and walks the whole pending set, spending an os.Stat and
// an mmap per id; the merge path put ONE id there per merge, and the seal path can
// reach it with the entire resident set at once — the two failures that are
// reachable at that scale (an mmap that is structurally unavailable, and an L2
// cache whose cap evicts the blobs this drain just wrote) fail every id in the
// pass. Without a bound a 3,738-segment pool would spend thousands of syscalls on
// every search for the next remapMaxAttempts rounds, and persistResident would
// re-arm them on the next drain tick.
//
// IT BOUNDS THE ENQUEUES, NOT THE PASS. Every heap-backed id is attempted on every
// pass; what this cap limits is how many FAILURES may be left waiting for a consumer
// touch. An id whose mapping succeeds enqueues nothing, so a full pending set is no
// reason to skip it — and a pass that skipped it would release nothing at all for as
// long as the pending set stayed full, which is exactly the latch this constant's
// earlier reading produced. The mapping attempt itself is DURABILITY-path work, on a
// path that already walks the whole resident set to export it.
//
// THE DEFERRED REMAINDER IS NOT DROPPED. A failure this pass did not enqueue leaves
// its segment correct and heap-backed with nothing owed to it; the next
// persistResident re-derives the heap-backed set and releases what it can. So
// convergence for the overflow runs on the DURABILITY path, which is where the release
// belongs, rather than on the search path, which is the one this cap protects.
//
// 64 IS CHOSEN FOR THE WORK IT ADMITS, not for roundness: at most 64 stats and 64
// mmaps per search, which is the same order as one bucket's consolidation and two
// orders below the resident-set walk it replaces. A pool whose releases fail
// structurally therefore announces the condition once per pass and costs a bounded
// amount per search until the cause clears.
const releasePendingCap = 64

// releaseHeapBackedResident turns every resident payload that still holds its
// encoder output on the Go heap into a mapping of the file the L2 write just made
// durable. exported is the resident set persistResident already took, used only to
// carry a failed segment's bytes into the pending set.
//
// IT IS THE SEAL PATH'S HALF OF THE PROPERTY remapMerged GIVES THE MERGE PATH, and
// without it the merge path's is a half-measure: merges replace their constituents,
// but a drain's SEALED tails are never merged on a merge-disabled pool and a
// rebuild's partitions are published by a layer swap, so on the measured corpus the
// overwhelming majority of resident segments were heap-backed for the life of the
// process — 27.6 KB of retained blob per segment, 78% of the stored bytes held a
// second time.
//
// IT RUNS HERE BECAUSE THIS IS WHERE THE PRECONDITION BECOMES TRUE, not because it
// is convenient. remapOnce maps the blob out of the L2 cache, so it can only work
// once the bytes are in L2 — which is exactly what the call above establishes for
// every resident id, and exactly what evictResident's re-materializability gate
// already depends on. Remapping before the write would fail on cause
// remapCauseNotCached for every freshly sealed segment.
//
// SERIAL, AND THAT IS A CHOICE WITH A REASON. RemapResident copies the whole entry
// slice per CAS attempt, so N concurrent remaps over one engine make N copies of an
// N-entry slice contending on one pointer; the existing drain is serial for the same
// reason. The per-segment work is a mmap of a written file and a header-and-field-
// table decode, not a re-read of the corpus.
//
// A FAILURE IS PENDING, NEVER FORGOTTEN, on the same machinery the merge path uses:
// the payload stays CORRECT and heap-backed, the id is marked, and the next consumer
// touch drains it under remapMaxAttempts. A segment that stopped being resident in
// the window is not a failure — RemapResident declines and releases the mapping.
func (m *distManager[Q, S]) releaseHeapBackedResident(exported []searchengine.SegmentBlob) {
	ids := m.engine.HeapBackedResidentIDs()
	if len(ids) == 0 {
		return
	}
	bytesByID := make(map[searchengine.SegmentID]searchengine.SegmentBlob, len(exported))
	for _, b := range exported {
		// THE BYTES AND THE ENVELOPE ONLY, NEVER THE WHOLE BLOB. An exported blob
		// carries a pin on the resident ENTRY it came from (SegmentBlob.PinsMapping),
		// which is correct for a mapping-backed payload and is exactly wrong here: a
		// pending attempt that held it would keep the heap-backed entry — and the
		// encoder output this release exists to give up — reachable after eviction
		// dropped that entry from the set, so the pool's modeled heap would fall while
		// its real heap did not. Every id here is heap-backed by construction, so the
		// bytes are an ordinary slice that keeps its own backing array alive and needs
		// no pin at all. Rebuilding the struct is what drops it: keepAlive is
		// unexported, so a value built in this package cannot carry one.
		bytesByID[b.ID] = searchengine.SegmentBlob{ID: b.ID, Envelope: b.Envelope, Bytes: b.Bytes}
	}
	// pendingBlob is the bytes the bound's one additive re-Put needs. An id absent
	// from the export — the set moved between it and now — is still recorded, with
	// no bytes: drainRemapPending skips the re-Put for an attempt that has none
	// rather than inventing a blob to write.
	pendingBlob := func(id searchengine.SegmentID) searchengine.SegmentBlob {
		if b, ok := bytesByID[id]; ok {
			return b
		}
		return searchengine.SegmentBlob{ID: id}
	}

	// THE PASS ENQUEUES AT MOST enqueueBudget FAILURES, and that bound is what keeps
	// a structural failure from converting the whole resident set into per-search
	// work. See releasePendingCap.
	//
	// IT BOUNDS THE ENQUEUES AND NEVER THE PASS, and the difference is not a nicety.
	// This loop used to open `if enqueueBudget <= 0 { break }`, so once the pending set
	// stood at the cap every later pass exited on its FIRST id having released nothing
	// — observed live as `heap_backed=4007 released=2072 failed=64` followed by
	// `heap_backed=3401 released=0 deferred=3401`, with a quarter of a gigabyte of
	// encoder output still heap-resident beside its mappings. An id whose mapping
	// SUCCEEDS enqueues nothing and costs the pending set nothing, so a full pending set
	// is no reason to skip it. What the cap protects is per-SEARCH work — the size of
	// the set drainRemapPending walks — and that is bounded here exactly as before.
	enqueueBudget := releasePendingCap - m.pendingRemapCount()
	// THE SET AS IT STOOD BEFORE THIS PASS, read once. deferred counts the failures
	// this pass left with NOTHING OWED TO THEM, and an id that was ALREADY pending is
	// owed a repair on the next consumer touch whatever this pass did — counting it as
	// deferred reports a segment waiting on the durability path while the drain is
	// holding a repair for it. At the cap with every remap failing, the difference is
	// the whole number: `failed=80 deferred=80` against `failed=80 deferred=16
	// pending=64`.
	alreadyPending := m.pendingRemapSnapshot()
	mapped := make([]searchengine.SegmentBlob, 0, len(ids))
	failures := map[string]int{}
	deferred := 0
	for _, id := range ids {
		blob, cause, err := m.mapBlobForRemap(id)
		if cause != "" {
			failures[cause]++
			_ = err
			if enqueueBudget <= 0 {
				// This failure is not enqueued. The segment stays heap-backed and
				// correct with nothing owed to it, and the next persistResident
				// re-derives the heap-backed set and releases what it can — the same
				// next-touch convergence the drain gives the ids that ARE enqueued, on
				// the durability path rather than on the search path.
				if !alreadyPending[id] {
					deferred++
				}
				continue
			}
			m.recordRemapPending(pendingBlob(id))
			// RE-READ RATHER THAN DECREMENT, because recordRemapPending is keyed by id:
			// a failure for an id ALREADY pending records no new entry, and decrementing
			// for it would spend budget the pending set never grew by.
			enqueueBudget = releasePendingCap - m.pendingRemapCount()
			continue
		}
		mapped = append(mapped, blob)
	}

	// ONE SWAP FOR THE WHOLE DRAIN. The engine re-folds its corpus statistics per
	// snapshot, so remapping one segment at a time would pay that fold once per
	// segment over the whole resident set — quadratic in the segment count on the
	// exact path a large drain takes.
	results := m.engine.RemapResidentBatch(mapped)
	republished, declined, refused := tallyRemapOutcomes(results)
	failures[remapCauseRepublish] += refused
	for id, res := range results {
		switch res.Outcome {
		case searchengine.RemapRepublished:
			m.clearRemapPending(id)
		case searchengine.RemapDeclined:
			// The segment left the set while this pass ran. Nothing is degraded and
			// nothing is owed, so it is dropped from pending rather than retried —
			// and it is NOT counted as a release, because nothing was swapped.
			m.clearRemapPending(id)
		case searchengine.RemapFailed:
			// UNDER THE SAME BOUND AS THE MAPPING FAILURES ABOVE, because it enqueues
			// onto the same set that drainRemapPending walks per search. A batch every
			// one of whose blobs failed to decode is reachable at resident-set scale
			// for the same structural reasons a mapping failure is.
			if enqueueBudget <= 0 {
				if !alreadyPending[id] {
					deferred++
				}
				continue
			}
			m.recordRemapPending(pendingBlob(id))
			enqueueBudget = releasePendingCap - m.pendingRemapCount()
		}
	}

	// deferred IS "FAILED WITH NOTHING OWED", not "failed and not enqueued by this
	// pass": a failure for an id that was already pending is owed a repair on the next
	// consumer touch, and the WARN below carries that number as pending. So
	// failed - deferred is what the drain is holding, and deferred is what the next
	// durability pass will re-derive.
	slog.Info("segmentdist: released sealed segments to their mappings",
		"graph", m.target.GetGraph(), "name", m.target.GetName(), "repo", m.target.GetRepo(),
		"format", m.format, "heap_backed", len(ids), "released", republished,
		"declined", declined, "failed", failureTotal(failures), "deferred", deferred)

	// ONE LINE FOR THE WHOLE PASS, with the causes as a histogram. The per-segment
	// announcement is markRemapPending's and belongs to the merge path: the two
	// reachable failures here — a structurally unavailable mmap, and an L2 cache
	// whose cap evicted the blobs this drain just wrote — fail EVERY id in the pass,
	// so a line per segment is thousands of lines that say one thing.
	if total := failureTotal(failures); total > 0 {
		slog.Warn("segmentdist: sealed segments not republished — retained for repair on a later consumer touch",
			"graph", m.target.GetGraph(), "name", m.target.GetName(), "repo", m.target.GetRepo(),
			"format", m.format, "heap_backed", len(ids), "failed", total, "deferred", deferred,
			"cause_map_failed", failures[remapCauseMapFailed],
			"cause_not_cached", failures[remapCauseNotCached],
			"cause_republish", failures[remapCauseRepublish],
			"pending", m.pendingRemapCount(), "pending_cap", releasePendingCap)
	}
}

// tallyRemapOutcomes counts a batch's three outcomes.
//
// IT IS A FUNCTION RATHER THAN THREE ++ IN THE LOOP so the counting is observable
// on its own: the arm that matters — a DECLINE, which reports no error and swapped
// nothing — cannot be produced on demand from a manager test, because a segment
// has to leave the set between the caller's list and the engine's CAS for one to
// happen. Folding a decline into the release count is the defect this exists to
// keep pinned, and the operator-facing release line is read as evidence.
func tallyRemapOutcomes(results map[searchengine.SegmentID]searchengine.RemapResult) (republished, declined, failed int) {
	for _, res := range results {
		switch res.Outcome {
		case searchengine.RemapRepublished:
			republished++
		case searchengine.RemapDeclined:
			declined++
		case searchengine.RemapFailed:
			failed++
		}
	}
	return republished, declined, failed
}

// failureTotal sums a cause histogram.
func failureTotal(failures map[string]int) int {
	var n int
	for _, c := range failures {
		n += c
	}
	return n
}

// pendingRemapCount reports how many segments are waiting on a republication.
func (m *distManager[Q, S]) pendingRemapCount() int {
	m.resMu.Lock()
	defer m.resMu.Unlock()
	return len(m.remapPending)
}

// pendingRemapSnapshot is the pending set's MEMBERSHIP at one instant, taken once so
// a pass can tell a failure it is leaving with nothing owed from one the drain is
// already holding a repair for.
//
// A SNAPSHOT RATHER THAN A PER-ID PROBE, and not only to save locks: the question the
// callers ask is "was this id pending BEFORE this pass", and a probe taken mid-loop
// would answer "is it pending now", which the pass's own enqueues change underneath it.
func (m *distManager[Q, S]) pendingRemapSnapshot() map[searchengine.SegmentID]bool {
	m.resMu.Lock()
	defer m.resMu.Unlock()
	out := make(map[searchengine.SegmentID]bool, len(m.remapPending))
	for id := range m.remapPending {
		out[id] = true
	}
	return out
}

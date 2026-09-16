// SPDX-License-Identifier: Apache-2.0

// manager_rebuild_release.go — the RESET path's half of mapping republication: each
// partition of a built layer is made durable and handed back to the engine as a
// mapping of its stored copy AS IT IS BUILT, before the next partition exists.
//
// THE SEAL PATH'S HALF IS manager_release.go, and the two differ for one reason. A seal
// publishes first and the durability write follows, so its release can only run after
// the fact and over the whole resident set at once (releaseHeapBackedResident). A reset
// builds a COMPLETE LAYER ASIDE before publishing anything, so a release that ran after
// the build would be too late for the quantity that matters: the layer's every encoded
// partition is on the heap at the moment the process is at its largest. Measured at a
// corpus-scale rebuild's crest, that built layer was 38.5 % of the client's live heap.
//
// IT REUSES THE SEAL PATH'S MACHINERY WHOLE. mapBlobForRemap is the same mapping seam,
// recordRemapPending the same convergence record, releasePendingCap the same bound on
// per-search work, and the release line the same operator-facing record — so an
// operator reads one story for both producers and a reader learns one mechanism.

package segmentdist

import (
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// layerRelease is one reset finalize's per-partition persist hook plus the two records
// the finalize owes an operator afterwards: the L2 write diff, and the release tally.
//
// IT IS A STRUCT RATHER THAN A CLOSURE OVER LOCALS because the hook is called once per
// partition and the records are emitted once for the layer, so the counts have to
// outlive the calls that produce them and be readable by name.
type layerRelease[Q, S any] struct {
	bm *distManager[Q, S]

	// resident and written are the L2 write diff, accumulated per partition and
	// reported once: how many partitions the layer holds, and how many of them were
	// not already in the cache. A content-hash-unchanged rebuild writes zero.
	resident int
	written  int

	// released counts the partitions whose payload was swapped for a mapping — the
	// number the operator-facing line reports, and never a count of ids that merely
	// reached the seam.
	released int

	// failures is the cause histogram behind the partitions that stayed heap-backed,
	// keyed by the same remapCause* constants the seal path uses.
	failures map[string]int
}

// newLayerRelease builds the hook for one format's finalize.
func newLayerRelease[Q, S any](bm *distManager[Q, S]) *layerRelease[Q, S] {
	return &layerRelease[Q, S]{bm: bm, failures: map[string]int{}}
}

// persist is the searchengine.LayerPartitionPersist this finalize hands to the build:
// write the partition's bytes to L2 if the cache does not already hold them, then map
// the stored copy back so the built entry can give up its encoder output.
//
// THE DIFF IS KEYED ON CACHE PRESENCE, exactly as the whole-layer write it replaces
// was, which is what keeps a re-run over an unchanged corpus a content-hash no-op: a
// segment id IS its payload's content hash, so an unchanged partition mints an id the
// cache already holds and nothing is written.
//
// AND THE MAPPING IS ATTEMPTED WHETHER OR NOT THE WRITE RAN. A partition skipped as
// present is still heap-backed — its bytes were just built — so gating the release on
// len(diff) would leave the whole layer retained on exactly the re-run this diff makes
// cheap. That is the same rule persistResident states for the seal path.
//
// A WRITE FAILURE IS RETURNED AND ABORTS THE BUILD, because durability is the one
// precondition the swap cannot proceed without. A MAPPING failure is not an error at
// all: the partition stays correct, durable and heap-backed, and its id comes back
// through the built layer's Unreleased set for the pending-remap convergence below.
func (r *layerRelease[Q, S]) persist(
	blob searchengine.SegmentBlob,
) (searchengine.SegmentBlob, bool, error) {
	r.resident++
	if _, present := r.bm.cache.sizeOf(blob.ID); !present {
		if err := r.bm.writeNewBlobsToL2([]searchengine.SegmentBlob{blob}); err != nil {
			return searchengine.SegmentBlob{}, false, err
		}
		r.written++
	}

	mapped, cause, err := r.bm.mapBlobForRemap(blob.ID)
	if cause != "" {
		r.failures[cause]++
		// The cause is the structured attribute the histogram carries and the error is
		// its detail; the seal path's release makes the same trade at its own call for
		// the same reason — a pass that can fail every partition must not emit an error
		// per partition.
		_ = err
		return searchengine.SegmentBlob{}, false, nil
	}
	r.released++
	return mapped, true, nil
}

// reportWriteDiff emits the "L2 write diff resolved" record the reset has always
// emitted, from the per-partition counts rather than from one whole-layer write.
//
// THE RECORD IS UNCHANGED IN SHAPE AND MEANING, which is deliberate: it is the line an
// operator reads to tell a rebuild that wrote a layer from one that found every blob
// already present, and the 78-second rebuild that truncated a served corpus emitted no
// line at all. Splitting the write across partitions must not split the record.
func (r *layerRelease[Q, S]) reportWriteDiff() {
	slog.Info("segmentdist: L2 write diff resolved",
		"graph", r.bm.target.GetGraph(), "name", r.bm.target.GetName(), "repo", r.bm.target.GetRepo(),
		"format", r.bm.format, "resident", r.resident, "written", r.written,
		"skipped_as_present", r.resident-r.written)
}

// reportRelease emits the release tally for the layer, with the same message, the same
// keys and the same counting rule as the seal path's pass.
//
// declined IS ALWAYS ZERO HERE AND THE KEY IS STILL PRINTED. A decline is a segment
// that left the resident set between a caller's list and the engine's CAS, and a built
// partition cannot: it is not resident yet and nothing else can retire it. Printing the
// key regardless keeps one line shape for both producers, so an operator grepping the
// release record does not have to know which path emitted it.
func (r *layerRelease[Q, S]) reportRelease(unreleased, deferred int) {
	slog.Info("segmentdist: released sealed segments to their mappings",
		"graph", r.bm.target.GetGraph(), "name", r.bm.target.GetName(), "repo", r.bm.target.GetRepo(),
		"format", r.bm.format, "heap_backed", r.resident, "released", r.released,
		"declined", 0, "failed", unreleased, "deferred", deferred)

	if unreleased > 0 {
		slog.Warn("segmentdist: sealed segments not republished — retained for repair on a later consumer touch",
			"graph", r.bm.target.GetGraph(), "name", r.bm.target.GetName(), "repo", r.bm.target.GetRepo(),
			"format", r.bm.format, "heap_backed", r.resident, "failed", unreleased, "deferred", deferred,
			"cause_map_failed", r.failures[remapCauseMapFailed],
			"cause_not_cached", r.failures[remapCauseNotCached],
			"cause_republish", r.failures[remapCauseRepublish],
			"pending", r.bm.pendingRemapCount(), "pending_cap", releasePendingCap)
	}
}

// recordUnreleased puts the partitions that stayed heap-backed onto the pending-remap
// machinery so a later consumer touch repairs them, and reports how many it declined to
// enqueue under the per-search bound.
//
// IT RUNS AFTER THE SWAP, not after the build, and that ordering is the point: an id
// enqueued for a layer the degeneracy gate then REFUSED would be a repair owed to a
// segment that never became resident. drainRemapPending would drop it harmlessly on its
// first touch, but recording it at all would mean the pending set counted work that
// could not exist.
//
// THE BYTES TRAVEL AND THE PIN DOES NOT, which is the seal path's rule and its reason:
// a built layer's blob pins the ENTRY it came from, and a pending attempt holding that
// pin would keep the very payload this release exists to give up reachable after
// eviction dropped the entry. The bytes alone keep their own backing array alive and
// are what the bound's one additive re-Put writes.
func (r *layerRelease[Q, S]) recordUnreleased(
	unreleased []searchengine.SegmentID, blobs []searchengine.SegmentBlob,
) int {
	if len(unreleased) == 0 {
		return 0
	}
	byID := make(map[searchengine.SegmentID]searchengine.SegmentBlob, len(blobs))
	for _, b := range blobs {
		byID[b.ID] = searchengine.SegmentBlob{ID: b.ID, Envelope: b.Envelope, Bytes: b.Bytes}
	}

	// THE SET AS IT STOOD BEFORE THIS CALL. deferred is "left with NOTHING OWED", so a
	// partition that was ALREADY pending does not count as one — the drain is holding a
	// repair for it whatever this call did. Same rule and same reason as
	// releaseHeapBackedResident's.
	alreadyPending := r.bm.pendingRemapSnapshot()
	deferred := 0
	budget := releasePendingCap - r.bm.pendingRemapCount()
	for _, id := range unreleased {
		if budget <= 0 {
			// Not forgotten: the partition stays correct and heap-backed with nothing
			// owed to it, and the next durability pass over this pool re-derives the
			// heap-backed set and releases what it can.
			if !alreadyPending[id] {
				deferred++
			}
			continue
		}
		blob, ok := byID[id]
		if !ok {
			blob = searchengine.SegmentBlob{ID: id}
		}
		r.bm.recordRemapPending(blob)
		budget = releasePendingCap - r.bm.pendingRemapCount()
	}
	return deferred
}

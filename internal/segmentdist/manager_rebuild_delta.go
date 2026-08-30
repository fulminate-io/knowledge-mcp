// SPDX-License-Identifier: Apache-2.0

// manager_rebuild_delta.go — the rebuild driver's DELTA finalize: re-emit the
// partitions that own a watermark-scoped set of rebuilt documents, on the LOADED
// corpus, and publish once per format.
//
// It is a third caller of the partition machinery, distinct from the one-shot write
// entry points manager_bucket.go carries, because it has preconditions they do not:
// the engines must already hold the shipped corpus, and a run that cannot establish
// that has to say so rather than publish what it has.

package segmentdist

import (
	"context"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// ReEmitRebuiltDelta finalizes a WATERMARK-SCOPED rebuild run: it re-emits the
// partitions owning the scanned documents against the corpus the serving engines
// already hold, and publishes once per format.
//
// WHY NOT THE STAGING ENGINE. The deterministic staging engine never loads the
// existing corpus, so its Export is the scanned delta alone — finalizing a delta
// through it seals a thin, bucket-unaligned segment and adds it to the live set
// beside every untouched bucket blob. The serving engines DO hold the shipped corpus
// (the L2-first load Search already drives), so re-emitting through them realigns the
// touched partitions and leaves the rest referenced and untouched. That is what makes
// a one-node delta change exactly one blob instead of adding one.
//
// THE THREE RETURN VALUES ARE THREE DIFFERENT QUESTIONS, and collapsing any two of
// them is a corpus-wipe bug:
//
//   - applicable=false means THIS SHAPE CANNOT BE ATTEMPTED — the engines are not
//     holding a corpus to re-emit against. The caller must fall back to a full
//     from-scratch run, re-scanning from a zero watermark.
//   - swapped=false with applicable=true means ATTEMPTED AND FAILED, and the error
//     accompanying it is always non-nil: the two returns that report this shape are
//     the two re-emit failures below, where the rebuild could not run or its result
//     could not be made durable. So a caller separating this from applicable=false is
//     choosing between "report the failure" and "re-scan from zero", not between two
//     kinds of success.
//   - derivedBucketCount is the partition count the re-emit actually ran against, so
//     a caller comparing the live set's cardinality across the call can tell a
//     realignment (a crossing legitimately grows it) from the thin append it is
//     guarding against.
//
// THE APPLICABILITY GUARD IS NOT DEFENSIVE PADDING. On an engine that holds nothing,
// BucketCountFor(~0) derives ONE bucket, and re-emitting the delta under that count
// would collapse whatever it touched into a single partition — strictly worse than
// the thin append it replaces. residentBackstopFloor is the same threshold the
// read-side degeneracy backstop uses for the same reason.
//
// swapped is ANDed across the two formats. They are SEPARATE segment corpora in
// separate L2 pools over the same nodes, so a caller told "finalized" after a
// BM25-only swap would treat an unwritten vector corpus as durable — the same
// two-format reading FinalizeRebuild uses.
func (m *Manager) ReEmitRebuiltDelta(
	ctx context.Context, gt kgtypes.GraphType, name string, hnswDocs, bm25Docs []searchengine.Document,
) (swapped, applicable bool, derivedBucketCount int, err error) {
	dm := m.managerFor(gt, name)
	bm := m.bm25ManagerFor(gt, name)
	// BOTH RESIDENCY READ LOCKS, HELD ACROSS load() AND EVERY ENGINE READ, AND RELEASED
	// BEFORE THE FIRST PUBLISH. Each manager has its OWN residencyMu and its own
	// reclaimer-driven eviction, so dm's lock says nothing about bm's pool; covering
	// only one leaves the other's reads in exactly the window this closes. Acquired in
	// a fixed order and never held while waiting on anything else — evictResident takes
	// only its own manager's lock (manager_residency.go) — so there is no cycle to
	// deadlock on.
	dm.residencyMu.RLock()
	bm.residencyMu.RLock()
	// Idempotent: the once-guard makes this free on a daemon that has already served a
	// search for this graph, and pays the load exactly once otherwise.
	if lerr := dm.load(ctx); lerr != nil {
		bm.residencyMu.RUnlock()
		dm.residencyMu.RUnlock()
		return false, false, 0, lerr
	}
	if lerr := bm.load(ctx); lerr != nil {
		bm.residencyMu.RUnlock()
		dm.residencyMu.RUnlock()
		return false, false, 0, lerr
	}

	hnswResident, bm25Resident := dm.engine.ResidentDocCount(), bm.engine.ResidentDocCount()
	if hnswResident < residentBackstopFloor || bm25Resident < residentBackstopFloor {
		bm.residencyMu.RUnlock()
		dm.residencyMu.RUnlock()
		slog.Warn("segmentdist: delta re-emit NOT APPLICABLE — the serving engines hold no corpus to re-emit against (caller must re-scan from zero)",
			"graph_type", gt, "name", name, "hnsw_resident", hnswResident, "bm25_resident", bm25Resident,
			"floor", residentBackstopFloor)
		return false, false, 0, nil
	}

	// The corpus is the RESIDENT set alone: these documents are already resident,
	// sealed there by the embed writeback the scan read them from. Adding them again
	// would derive a partition count for twice the corpus that exists.
	hnswCorpus := dm.engine.DistinctResidentDocCount()
	// THE BM25 CORPUS IS HOISTED TO A LOCAL RATHER THAN READ INLINE AT ITS CALL, and the
	// hoist is what makes it coverable at all: it used to be the fifth argument of the
	// SECOND replaceBucketAndPublish below, and an argument evaluated inside a call that
	// runs after the unlock cannot be inside any span that ends before it. Reading it
	// here also takes both counts from ONE snapshot of the two pools instead of one from
	// before the hnsw publish and one from after.
	bm25Corpus := bm.engine.DistinctResidentDocCount()
	// UNLOCKED EXPLICITLY, NOT BY defer, AND BEFORE THE PUBLISHES. A `defer RUnlock()` at
	// the top would READ as an unlock before the publish while EXECUTING after it, which
	// is a blanket wrap: the calls below end in an L2 disk write, and Go's RWMutex
	// blocks new readers once a writer is waiting, so holding a read lock across them
	// stalls the reclaimer's Lock() and every reader queued behind it for the duration of
	// that I/O. Everything the span protects has been read into locals by this line.
	bm.residencyMu.RUnlock()
	dm.residencyMu.RUnlock()
	derivedBucketCount = searchengine.BucketCountFor(hnswCorpus)

	// THE SWAP IS OBSERVABLE DIRECTLY NOW. These two calls used to be bracketed by
	// reads of a landed-manifest-swap counter, because the publish they ended in could
	// decline with a NIL ERROR (the coverage gate, the agent's 409) and the error alone
	// could not tell a caller whether anything landed. With the publish gone the bucket
	// replace either succeeds or returns an error, so a nil return IS the swap.
	// singleL2WriteAttempt and logAbortedReclaimOnly: both of the delete re-emit's
	// extra policies are scoped to that path, and this rebuild-delta finalize keeps
	// the behaviour it has always had — one write attempt, and a merge reclaim its
	// group swap aborted stays an ERROR log rather than becoming this call's error.
	// Surfacing it here would turn an aborted reclaim into swapped=false, which the
	// rebuild driver reads as a failed finalize.
	if rerr := replaceBucketAndPublish(
		dm, docIDs(hnswDocs), hnswDocs, hnswCorpus, singleL2WriteAttempt, logAbortedReclaimOnly); rerr != nil {
		return false, true, derivedBucketCount, rerr
	}
	if rerr := replaceBucketAndPublish(
		bm, docIDs(bm25Docs), bm25Docs, bm25Corpus, singleL2WriteAttempt, logAbortedReclaimOnly); rerr != nil {
		return false, true, derivedBucketCount, rerr
	}

	return true, true, derivedBucketCount, nil
}

// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// ReplaceBucket re-emits whatever partitions the supplied documents and superseded
// ids belong to, then makes the result durable ONCE for the whole call.
//
// Documents are grouped by partition and each partition is rebuilt in a single
// atomic swap, so the partitions nobody touched stay resident and keep their
// stored copies. "Publish" on this path is that swap and nothing else: an
// in-process compare-and-swap of this engine's resident set, with no network
// anywhere on it. Durability is the L2 DISK WRITE that follows, and running that
// write once for the whole call rather than once per partition is what keeps one
// re-emit to one export-and-diff over the resident set.
//
// THE COUNT CAN CHANGE, AND REALIGNMENT IS WRITE-DRIVEN. The partition count is
// derived from the corpus size, so a corpus growing through a power of two moves
// it, and because the partition of a document is a modulo of that count, a
// partition under the old count splits into several under the new one. Only the
// partitions this call actually touches are realigned, together with whatever else
// their constituents hold; a partition no write reaches keeps its old alignment
// until a later write arrives. That is correct at every moment — every document
// stays live, in exactly one segment, and reachable — and it keeps a crossing
// proportional to the writes that cross it rather than to the whole corpus. The
// batch rebuild driver realigns everything in one pass when it runs.
func (m *Manager) ReplaceBucket(
	ctx context.Context, gt kgtypes.GraphType, name string,
	superseded []searchengine.ExternalID, docs []searchengine.Document,
) error {
	dm := m.managerFor(gt, name)
	// The incoming documents are NOT yet resident on this path, so the corpus they
	// will form is the resident set plus them.
	corpusDocs := dm.engine.DistinctResidentDocCount() + len(docs)
	// singleL2WriteAttempt and logAbortedReclaimOnly, always. This method used to be a
	// two-line wrapper over a private form taking both policies as arguments, so that
	// the delete re-emit could arm them without arming them here; the delete no longer
	// re-emits this format at all, leaving that form a single-caller indirection over
	// constant arguments, so it is gone and the policies are stated at the one call.
	return replaceBucketAndPublish(dm, superseded, docs, corpusDocs, singleL2WriteAttempt, logAbortedReclaimOnly)
}

// replaceBucketAndPublish is the shared body of every partition re-emit: rebuild the
// partitions the supplied documents and superseded ids belong to, then make the
// result durable ONCE for the whole call. The three callers differ only in the
// operands this signature makes explicit.
//
// THE REBUILD AND THE WRITE HAVE DIFFERENT FAILURE MODELS, and writeAttempts is
// where that difference is expressed. Everything replaceBucketGroups does is local
// and in memory, ending in the engine's own compare-and-swap, so an error from it is
// a code bug: it is returned immediately and unchanged, because repeating a
// deterministic bug produces the same wrong answer. The L2 disk write below it is the
// one step that can fail operationally, so the delete re-emit gives it
// l2WriteAttemptsOnDelete and every other caller leaves it at singleL2WriteAttempt.
//
// THE GROUP SWAP WRITES L2 TOO, AND THAT IS WHAT surfaceAbortedReclaim IS FOR. The
// swap fires the engine's merge hook, and reclaimMerged Puts the consolidated blob
// before removing the constituents it supersedes; a failed Put ABORTS that reclaim,
// which is deliberate and is logged, but returns nothing. So a re-emit whose OWN
// write succeeds — first try or on a retry — can still leave the pre-swap
// constituents on disk beside the blob it just wrote, and report clean success. A
// caller that asks gets that condition folded into its error; see the policy
// constants for why only the delete path asks.
//
// corpusDocs IS THE CALLER'S TO SUPPLY because the callers disagree about it, and
// getting it wrong moves the derived partition count. ReplaceBucket's documents are
// NOT yet resident, so its corpus is the resident set PLUS them; the rebuild's delta
// documents ARE already resident (the scan reads nodes whose vectors the embed
// writeback already sealed into the engine), so adding them again would derive a count
// for twice the corpus that exists.
//
// WHAT IS MADE DURABLE IS THIS ENGINE'S OWN RESIDENT SET. It used to take a set of
// sibling digests to reference alongside it, because the HNSW rebuild wrote a second
// engine keyed to the same manifest; with one engine per format there is no sibling,
// and there is no manifest either — the durable record is simply the blobs this
// pool's L2 cache holds.
//
// Generic over [Q, S] because distManager is, and the two live instantiations carry
// different type arguments (HNSW is [[]byte, struct{}], BM25 is
// [bm25.Query, *bm25.CorpusStats]); a non-generic helper cannot take both.
func replaceBucketAndPublish[Q, S any](
	dm *distManager[Q, S],
	superseded []searchengine.ExternalID, docs []searchengine.Document,
	corpusDocs int, writeAttempts int, surfaceAborted bool,
) error {
	// THE MARK IS TAKEN BEFORE THE SWAP, which is what bounds the question to this
	// call. The engine fires its merge hook synchronously inside ReplaceBucketGroup,
	// on this goroutine, so this re-emit's own aborted reclaim is inside the window
	// by construction; a bare read of the record afterwards would instead report any
	// abort this pool has ever seen.
	mark := dm.reclaimAbortMark()
	if _, _, err := replaceBucketGroups(dm, superseded, docs, nil, corpusDocs, nil); err != nil {
		return err
	}
	// Make the post-replace resident set durable. There is no reconcile set to diff
	// against: the replace already retired the superseded partitions in the engine.
	//
	// WHAT REAPS THE L2 COPIES OF WHAT IT DROPPED, stated precisely because this line
	// once claimed PruneCache did it unconditionally and then that nothing did. A
	// dropped copy is unlinked by the reclaim that supersedes it; when that reclaim
	// ABORTS, its obligation is RETAINED and discharged on the next consumer touch
	// (manager_reclaim_discharge.go). A prune reaps it too, but only because the blob
	// that superseded it now RECORDS that it did — see the reap paragraph at the top of
	// prune_cache.go, measured by TestPruneCacheReapsAnUnreclaimedMergeConstituent.
	_, err := dm.persistResidentWithWriteAttempts(writeAttempts)
	if !surfaceAborted {
		return err
	}
	// JOINED RATHER THAN PREFERRED. A re-emit can fail its own write AND abort a
	// reclaim in the same call, and the two name different states with different
	// remedies; reporting one would drop the other from the caller's qualifier.
	// errors.Join returns nil when both are nil, so the clean path is unchanged.
	return errors.Join(err, dm.abortedReclaimSince(mark))
}

// ReplaceBucketFields is the field-engine counterpart of ReplaceBucket. That engine
// has no deterministic sibling, so what it makes durable is exactly its own resident
// set.
func (m *Manager) ReplaceBucketFields(
	ctx context.Context, gt kgtypes.GraphType, name string,
	superseded []searchengine.ExternalID, docs []searchengine.Document,
) error {
	return m.replaceBucketFields(gt, name, superseded, docs, singleL2WriteAttempt, logAbortedReclaimOnly)
}

// replaceBucketFields is ReplaceBucketFields' body with the two per-caller re-emit
// policies — the L2 write-attempt bound and the aborted-reclaim report — made arguments,
// so the delete's field re-emit can arm them WITHOUT arming them for the exported entry
// point.
//
// THE SPLIT IS THE SCOPING, and it survives here because the delete still drives this
// format inline. The exported form is also called with an ADD shape (no superseded ids,
// incoming documents) by callers outside this package, and both policies are justified
// only for the delete re-emit's failure model; keeping the exported form at
// singleL2WriteAttempt and logAbortedReclaimOnly means neither reaches a caller whose
// failure model was never examined. The vector format had the same split until its
// delete leg became a live-bit kill, which left nothing to scope.
func (m *Manager) replaceBucketFields(
	gt kgtypes.GraphType, name string,
	superseded []searchengine.ExternalID, docs []searchengine.Document,
	writeAttempts int, surfaceAborted bool,
) error {
	dm := m.bm25ManagerFor(gt, name)
	corpusDocs := dm.engine.DistinctResidentDocCount() + len(docs)
	return replaceBucketAndPublish(dm, superseded, docs, corpusDocs, writeAttempts, surfaceAborted)
}

// DeleteFromBuckets makes a client-originated delete DURABLE, and it now does so on
// TWO DIFFERENT SCHEDULES. Synchronously it kills the named ids' live bits in the
// VECTOR corpus, re-emits the FIELD corpus's partitions inline, and seals the ids into
// the graph's durable tombstone record. The vector partitions themselves are re-emitted
// later, by the next drain that serves them (manager_bucket_backlog.go's
// ReEmitDirtyBuckets, reading the mask through deferredReEmitIDs).
//
// WHY THE VECTOR LEG MOVED AND THE FIELD LEG DID NOT. Rebuilding a partition's ANN
// graph is a from-scratch reconstruction whose cost is per-partition and superlinear in
// survivors, and a multi-id delete touches most partitions of a corpus — that
// reconstruction is what made a delete take seconds on the caller's own goroutine.
// Nothing about a delete's user-visible contract needs it: the live-bit kill removes the
// documents from search immediately, and the durable mask keeps them removed across a
// restart. The field corpus's per-partition rebuild is a different order of cost and
// stays inline.
//
// BOTH FORMATS ARE STILL REQUIRED. A node is indexed in the vector corpus and in the
// field corpus, and the two are SEPARATE segment corpora in separate L2 pools, so
// removing it from one leaves it in the other — still occupying rank slots there. The
// kill cannot fail, so the field re-emit's error is the re-emit error this function
// reports.
//
// WHAT A STALE COPY COSTS, stated precisely because it is easy to overstate: a
// removed node is NOT shown to anyone. The read path drops any ranked id that is
// missing from its tombstone-excluding hydrate, so the user never sees the node
// itself. What they see is a SHORTER result set, because the dead vector still
// competed for a top-k slot and that slot is discarded after ranking. The blob also
// keeps carrying the document, inflating every cache file and load of that
// partition.
//
// FOR THE FIELD CORPUS THAT DESCRIBES A FAILURE; FOR THE VECTOR CORPUS IT NOW DESCRIBES
// THE ORDINARY DEFERRED WINDOW, and the two must not be read as the same condition. A
// vector blob keeps carrying the deleted document until its partition is re-emitted,
// and throughout that window the document is invisible to every reader: the killed live
// bit excludes it in this process, and the durable mask seeds it dead in any process
// that imports the blob. What is outstanding is blob size, not visibility, and the
// window closes when the drain reaches that partition.
//
// IT CLOSES THE IMPORT WINDOW, which it did not use to. A blob shipped BEFORE the
// delete and re-imported afterwards starts all-live again unless the engine is handed a
// tombstone set, so the removed document came back on the next load — measured as one
// ordinary read after an aborted merge reclaim. sealDeletedIDs folds these ids into the
// graph's durable tombstone record and re-seeds the engines from it, so any blob
// imported afterwards has them masked; manager_bucket_delete_seal.go states that set's
// whole lifecycle, including where it ends. A delete through the LIVE engine cannot
// exercise it — the bit is already clear there — so the seal's tests import from L2 in a
// fresh process instead.
//
// THE WRITE BACKLOG IS PURGED FIRST, and that ordering is deliberate. The re-emit
// below removes these ids from their partitions, but the reconcile pass drains the
// write backlog immediately afterwards and would rebuild those same partitions FROM
// it — putting the documents straight back into both corpora and into the next
// shipped blob. Purging before the re-emit rather than after means a drain
// interleaving between the two finds nothing to resurrect, and if the re-emit itself
// fails the documents are queued nowhere, which is the direction matching the
// caller's intent.
//
// THE PURGE IS A WINDOW, NOT A BARRIER. It closes the resurrection window for every
// drain that snapshots AFTER it. A drain that had ALREADY taken its snapshot builds
// from that private copy and can still re-emit these ids one more time; that residual
// is bounded by a single reconcile interval, because the next tombstone-delta pass
// sees the ids as fresh and re-deletes them. The drain's own tombstone filter does not
// close that window either — this path sets no tombstones at all — so the same
// one-interval bound covers both.
//
// AN ID THIS POOL NEVER HELD COSTS NOTHING, and the short-circuit below is what
// makes that true rather than merely intended. The delete feed carries removals for
// every id the source deleted, not only the ids this client indexed, so a batch can
// name documents no partition here has ever carried. Without the check each one is
// still WORK: the partition re-emit marks a partition dirty for every superseded id
// by HASH alone, with no residency test, and then rebuilds it. Measured on the live
// corpus BEFORE this path deferred its vector leg, the VECTOR re-emit carried about 89%
// of a single-id delete's cost — 444ms of 497ms — and the field re-emit the remainder;
// the vector share is now deferred, so what the skip avoids on this path is the field
// rebuild alone — the seal above runs either way. Skipping is therefore a
// cost fix, not a correctness one: re-emitting a partition that does not hold the id
// removes nothing either way.
//
// THE AUTHORITY IS THE ENGINE'S OWN AND NOT A SECOND OPINION. SegmentedIndex's
// UncoveredFrom derives from residentMemberIn, which its own file names "the ONE
// searchability predicate", so this asks the same question the merge asks when it
// decides what a rebuilt partition actually carries. A residency test invented here
// could disagree with that predicate, and the disagreement would look exactly like a
// working skip.
//
// IT CHANGES NO OUTCOME. The answer decides ONLY whether to skip both legs entirely.
// When anything is held, both legs run with the FULL id slice exactly as before, so
// which partitions dirty and what they re-emit are byte-identical to the previous
// behaviour. A filtered slice is deliberately NOT passed down: that would change
// which buckets dirty, which is a behaviour change rather than a cost one.
//
// THE BOTH-FORMATS CONTRACT ABOVE IS NOT WEAKENED — read this carefully, because a
// skip sitting near that paragraph invites the opposite reading. The skip takes both
// legs TOGETHER and only when NEITHER engine holds a single named id. An id held in
// one format and not the other still runs both legs, which is exactly what the
// contract requires.
//
// THE PURGE STAYS UNCONDITIONAL AND STAYS FIRST. An id queued in the write backlog
// but not yet resident is precisely one UncoveredFrom reports as not held, so a purge
// placed behind this check would leave a deleted document queued to be written back —
// the resurrection the ordering above exists to prevent.
func (m *Manager) DeleteFromBuckets(
	ctx context.Context, gt kgtypes.GraphType, name string, ids []searchengine.ExternalID,
) error {
	if len(ids) == 0 {
		return nil
	}
	m.purgeDirty(gt, name, ids)

	// THE SEAL RUNS BEFORE THE SHORT-CIRCUIT BELOW, and that placement is load-bearing
	// rather than incidental. UncoveredFrom answers against the RESIDENT set, and a pool
	// that has not loaded L2 yet is resident-empty while its disk still holds every
	// pre-delete blob — exactly the state a cold process is in when a delete arrives. A
	// seal placed behind that check would skip the case it exists for. The skip's own
	// cost argument does not carry over either: it exists to avoid rebuilding partitions
	// through both formats, and this is a map union and one atomic file write.
	//
	// ITS FAILURE IS JOINED INTO THE RETURN RATHER THAN LOGGED, because an unsealed
	// delete is exactly what this function's callers qualify their acknowledgement with:
	// the rows are gone from the graph and the documents come back on the next import.
	// It does NOT abort the re-emit — the user's delete is still worth making durable in
	// the blobs — so the seal's error is carried to the end and joined there.
	sealErr := m.sealDeletedIDs(gt, name, ids)

	// THE PER-LEG TIMING IS ESTABLISHED BEFORE THE FIRST LEG RUNS, not written after
	// the second returns, and that ordering is the whole point. This ticket was
	// opened from a delete that never returned at all; a record emitted after both
	// legs reports nothing in exactly the state an operator needs it most, while a
	// deferred one still fires on a panic or an early return and shows which leg the
	// time went into. The two legs are metered separately because they are wildly
	// asymmetric, and the asymmetry ran the OTHER way from what this paragraph used to
	// claim: measured on the live corpus, an uncontended single-id delete spent 444ms in
	// the vector leg against 53ms in the field leg, so the vector reconstruction carried
	// roughly 89% of it, corroborated by the same daemon's per-partition populations —
	// a p50 of 332ms for the vector format against 104ms for the field format.
	//
	// AND THAT SPLIT NO LONGER DESCRIBES THIS PATH AT ALL, which is the point of stating
	// it: the vector leg is now a live-bit kill, so the number an operator reads in
	// hnsw_ms is near zero and the reconstruction it replaced rides the drain. The
	// per-leg metering stays because the two legs remain asymmetric — just in the other
	// direction now, with the whole of a delete's service time in the field leg.
	//
	// IT INSTALLS ABOVE THE SHORT-CIRCUIT, AND THAT PLACEMENT IS THE SENTENCE ABOVE
	// BEING TRUE RATHER THAN A PREFERENCE. A skip IS an early return, so a defer
	// installed after it would leave the one call shape that does no work as the one
	// call shape that reports nothing — and it would do so exactly when the skip rate
	// is the interesting number, because over-delivered erases are what make the skip
	// fire at all. skipped is on the record so an operator can read the no-op rate
	// directly instead of inferring it from two zero timings.
	//
	// ids is on the record because manage(prune) drives this same function with a
	// large id set, and a multi-id delete is not comparable to a single-id one.
	target := graphSelector(gt, name)
	var hnswMS, bm25MS int64
	skipped := false
	defer func() {
		slog.Info("segmentdist: delete_from_buckets",
			"graph", target.GetGraph(), "name", target.GetName(), "repo", target.GetRepo(),
			"ids", len(ids), "skipped", skipped, "hnsw_ms", hnswMS, "bm25_ms", bm25MS)
	}()

	if len(m.managerFor(gt, name).engine.UncoveredFrom(ids)) == len(ids) &&
		len(m.bm25ManagerFor(gt, name).engine.UncoveredFrom(ids)) == len(ids) {
		skipped = true
		return sealErr
	}

	// THE VECTOR LEG IS A LIVE-BIT KILL AND NOTHING ELSE. It is exactly the step
	// ReplaceBucketGroup performs before any harvest, so the documents leave search here
	// as completely as they did when this path rebuilt their partitions inline — what it
	// no longer does is reconstruct each touched partition's graph from scratch on the
	// caller's own goroutine. Those partitions are re-emitted by the next drain that
	// serves them, and the durable tombstone record sealed above is the ledger naming
	// what is owed; deferredReEmitIDs is where a drain reads it.
	//
	// IT RETURNS NO ERROR, and there is consequently no longer a first-error-wins rule
	// between the two legs: the field re-emit below is the only one that can fail, so
	// its error is the re-emit error this function joins.
	hnswStart := time.Now()
	m.managerFor(gt, name).engine.KillSuperseded(ids)
	hnswMS = time.Since(hnswStart).Milliseconds()

	// THE BM25 LEG STAYS INLINE AND KEEPS BOTH DELETE-ONLY POLICIES: the L2 write's
	// bounded retry, and the report of a merge reclaim the re-emit's own group swap
	// aborted. The exported ReplaceBucketFields keeps singleL2WriteAttempt and
	// logAbortedReclaimOnly — see replaceBucketFields for why the scoping is deliberate.
	//
	// THE SECOND POLICY EXISTS BECAUSE THE FIRST CREATED THE GAP IT COVERS. The re-emit
	// writes L2 twice — the group swap's merge hook, then persistResident — and the
	// retry is the second write's. A transient disk error that hits the FIRST and has
	// cleared by the second leaves the pre-delete constituent on disk beside the
	// post-delete blob while the retry reports clean success, which is a delete a fresh
	// process undoes with nothing having said so.

	bm25Start := time.Now()
	err := m.replaceBucketFields(gt, name, ids, nil, l2WriteAttemptsOnDelete, surfaceAbortedReclaim)
	bm25MS = time.Since(bm25Start).Milliseconds()
	// JOINED, NOT PREFERRED. An unsealed import window and a failed re-emit are
	// different states with different remedies — one resurrects the document on the next
	// load, the other leaves it in the shipped blob — so reporting one would drop the
	// other from the caller's qualifier. errors.Join returns nil when both are nil, so
	// the clean path is unchanged.
	return errors.Join(sealErr, err)
}

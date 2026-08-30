// SPDX-License-Identifier: Apache-2.0

// bucket_swap_group.go — the GROUP swap: one atomic snapshot in which several
// partitions sharing constituents are rebuilt together. Relocated verbatim from
// bucket_swap.go, which keeps the single-partition swap it generalizes.
//
// The group exists because a set of partitions sharing constituents CANNOT be
// published as a sequence of single swaps — whichever landed first would carry away
// members the others had not yet harvested, and doing them serially is the worst
// case rather than the safe one. That is the whole reason this file is separate
// from the single swap rather than an overload of it.

package searchengine

import (
	"runtime"
	"sync"
)

// BucketWork is one partition's share of a group swap: the partition number and
// the superseded ids and incoming documents that belong to it. The constituents
// are NOT per-partition — the group resolves them once and every partition
// harvests the same resolved set.
type BucketWork struct {
	Bucket     int
	Superseded []ExternalID
	Docs       []Document
}

// GroupHarvestStats reports what one ReplaceBucketGroup call actually resolved and
// what its harvests actually walked, so a caller metering the group swap reads the
// engine's own numbers instead of inferring them from what it handed in.
//
// IT IS A STRUCT RATHER THAN A PAIR OF BARE RETURN VALUES so a later statistic can
// be added without re-breaking every call site.
//
// RESOLVEDSEGMENTS IS NOT len(constituents), and that distinction is the whole
// reason this field exists. The resolve loop skips any id whose segment is no
// longer resident, and ReplaceBucket states the rule at the top of bucket_swap.go:
// "An id in constituents with no resident segment is skipped rather than treated as
// an error — a concurrent load may already have dropped it." That concurrency is
// observed rather than theoretical: the mutate-delete chain and the reconcile
// loop's drain run simultaneously on the same engine. So the caller's union size
// and this number genuinely differ by the resolve-miss count, and a gate comparing
// a walk count against the CALLER's number is wrong in both directions — it
// false-reds a correct accumulator and greens a narrowing that narrowed nothing.
type GroupHarvestStats struct {
	// ResolvedSegments is how many constituents were still resident when the group
	// took its one snapshot — the denominator a narrowing claim is measured against.
	ResolvedSegments int
	// WalkedSegments is the sum, over partitions, of the length of the constituent
	// slice each partition's harvest received: the group's TOTAL merge input.
	WalkedSegments int
	// MaxWalkedSegments is the largest single partition's constituent-slice length —
	// the per-partition cost that an aggregate total can hide.
	MaxWalkedSegments int
}

// ReplaceBucketGroup consolidates a GROUP of partitions that share constituents,
// in ONE atomic swap. It is the group form of ReplaceBucket, which is retained
// for callers rebuilding a single partition.
//
// THE GROUP IS THE UNIT BECAUSE THE CONSTITUENTS ARE SHARED. A segment aligned to
// an older partition count spans several of the current partitions, so more than
// one partition needs its members. ReplaceBucket removes every constituent it
// resolves while taking only its own partition's share, which is correct in
// isolation and destructive in a sequence: the first partition to swap carries
// away the members the others had not yet harvested. Closing the partition set
// under constituency SCHEDULES those partitions; it does not make their reads
// atomic against each other's swaps. This does:
//
//  1. RESOLVE the constituents ONCE, from one snapshot, for the whole group.
//  2. HARVEST per partition against THE CONSTITUENTS THAT SPAN THAT PARTITION,
//     drawn from that same resolved set — a constituent spanning four partitions
//     is read by those four and contributes a disjoint share to each, while one
//     spanning a single partition is read once rather than by the whole group.
//     The harvests run CONCURRENTLY on a bounded pool, and their results are
//     folded back SERIALLY in the original work order, so the published output is
//     independent of how the harvest was scheduled.
//  3. RETIRE each constituent ONCE, after every partition has harvested it.
//  4. PUBLISH in ONE CAS carrying the group's whole removal set and all outputs,
//     so no reader observes a partition's members missing.
//
// PARTIAL FAILURE IS ALL-OR-NOTHING. If any partition's build or merge fails the
// group publishes NOTHING and the resident set is unchanged. PUBLISHING THE
// PARTITIONS THAT SUCCEEDED WOULD BE THE ORIGINAL DEFECT MADE DETERMINISTIC — the
// removal set covers every constituent, so the failed partitions' members would
// be discarded with segments nobody rebuilt them from. "Publish what we have"
// reads as robustness and is data loss; the single-partition form already
// promises the same all-or-nothing and this only generalises it.
//
// MEMORY: one CAS holds every output in the group at once, so peak additional
// residency approaches a full copy of the resident corpus when the group is the
// whole partition set (on a 256-partition graph, the entire corpus alongside the
// existing one). That bound is ACCEPTED rather than capped: a cap would have to
// fall back to smaller groups, and the smallest group is the per-partition swap
// that loses data. Correctness first; the outputs are released as soon as the CAS
// publishes.
//
// It returns the published id per partition, keyed by partition number. A
// partition with nothing to consolidate contributes no entry. It ALSO returns what
// it resolved and what its harvests walked, because those quantities exist nowhere
// else — the caller knows only the union it handed in, which the resolve step
// filters.
func (e *SegmentedIndex[Q, S]) ReplaceBucketGroup(
	bucketCount int, constituents []SegmentID, work []BucketWork,
) (map[int]SegmentID, GroupHarvestStats, error) {
	set := e.set.Load()

	// (1) RESOLVE ONCE. Every partition below harvests against exactly these
	// entries, which is what makes the group's reads atomic with respect to one
	// another — the property the per-partition form cannot have.
	resolved := make([]*segmentEntry[Q, S], 0, len(constituents))
	for _, id := range constituents {
		if entry := set.entryByID(id); entry != nil {
			resolved = append(resolved, entry)
		}
	}
	stats := GroupHarvestStats{ResolvedSegments: len(resolved)}

	// (2) Kill superseded ids across the whole group before any harvest, so every
	// partition's accept predicate sees the same liveness.
	for _, w := range work {
		killSuperseded(set, w.Superseded)
	}

	// (3) HARVEST per partition. Build first, publish nothing yet. The removal set
	// is assembled here, before the harvest, because it is derived from the resolved
	// constituents alone and no worker touches it.
	//
	// THE REMOVAL SET STAYS THE WHOLE RESOLVED UNION even though the harvests are
	// narrowed. The group still RETIRES every constituent it resolved, so every one
	// of them must be removed; narrowing this to the spanning subset would leave
	// consumed segments resident beside their replacements. The narrowing is of the
	// MERGE INPUT only.
	added := make([]*segmentEntry[Q, S], 0, len(work))
	publishedBy := make(map[int]SegmentID, len(work))
	remove := make(map[SegmentID]bool, len(resolved)+len(work))
	removed := make([]SegmentID, 0, len(resolved))
	for _, entry := range resolved {
		remove[entry.meta.ID] = true
		removed = append(removed, entry.meta.ID)
	}

	// (3a) SPAN EVERY RESOLVED CONSTITUENT ONCE, for the whole group, so the
	// per-partition narrowing below is a map lookup rather than a rescan. Asking per
	// partition would cost O(partitions x corpus) — the very shape this narrowing
	// exists to remove — so it is computed here, once, and read by every harvest.
	//
	// MEMBERSHIP, NOT LIVENESS, and the asymmetry is deliberate. SegmentSpans does
	// not consult liveness either, and a SUPERSET is safe: an entry whose only
	// members in a partition are dead contributes nothing to that partition's merge,
	// so including it is the same no-op the union form already paid for. A SUBSET
	// would be data loss. Do not "tighten" this to live members only.
	spans := make(map[SegmentID]map[int]bool, len(resolved))
	for _, entry := range resolved {
		held := make(map[int]bool)
		for id := range entry.members {
			held[BucketOf(id, bucketCount)] = true
		}
		spans[entry.meta.ID] = held
	}

	// (3b) HARVEST THE PARTITIONS CONCURRENTLY, each against only the constituents
	// that span it. Nothing in one partition's harvest depends on another's — the
	// group's serialization points are the kill above and the CAS below — and the
	// results come back index-addressed, so the fold order is the original work
	// order rather than a completion order. That order is OBSERVABLE: step (5) below
	// hands the group's whole survivor set to the FIRST entry in added and nothing to
	// the rest, so a timing-dependent order would move the reclaim event between
	// segments run to run.
	harvested, freshIDs, harvestErrs, walked := e.harvestGroup(resolved, spans, work, bucketCount)

	// FOLD THE WALK COUNTERS AFTER THE JOIN, before the result fold, so the numbers
	// are populated on the error return below as well as on success — a group that
	// failed still walked what it walked, and the caller's diagnostic is established
	// before the call and fires either way.
	for _, n := range walked {
		stats.WalkedSegments += n
		stats.MaxWalkedSegments = max(stats.MaxWalkedSegments, n)
	}

	// (3b) FOLD SERIALLY, IN THE ORIGINAL work ORDER — the same body the harvest
	// loop ran inline before it was parallelized. Only WHERE the harvest happens has
	// changed; which partition contributes what, and in what order, has not.
	for i, w := range work {
		if err := harvestErrs[i]; err != nil {
			return nil, stats, err
		}
		if freshIDs[i] != "" {
			// The fresh segment was never published, but a previously-published
			// segment may carry the same content hash; dropping that id lets the
			// consolidated segment replace it rather than sit beside a duplicate.
			remove[freshIDs[i]] = true
		}
		// A partition whose harvest is empty contributes nothing to publish; the
		// constituents it read are still retired by the group, because some other
		// partition took their members.
		if harvested[i] == nil {
			continue
		}
		added = append(added, harvested[i])
		publishedBy[w.Bucket] = harvested[i].meta.ID
	}

	if len(added) == 0 {
		// Nothing to publish. The kills above stand; the constituents are left alone
		// rather than removed, because removing them here would discard their members
		// with no output carrying them — the defect.
		return publishedBy, stats, nil
	}

	// (4) THE GROUP'S SUPERSESSION SET, computed and recorded BEFORE the publish
	// because the entries it is stamped on are about to become part of an immutable
	// snapshot.
	survivors := stampGroupSupersession(added, removed)

	// (5) ONE CAS for the whole group. Retry on a lost race, re-reading the set —
	// removals are keyed by segment id, so they still apply against a snapshot
	// another writer changed underneath.
	for {
		cur := e.set.Load()
		next := cur.withReplacedGroup(e.format, remove, added)
		if e.set.CompareAndSwap(cur, next) {
			break
		}
	}

	// (6) Surface the supersession to the owner, with the same set the record carries.
	for _, entry := range added {
		e.fireMergeHook(entry, survivors)
		// The reclaim event carries the group's superseded set once; the remaining
		// outputs are reported with no removals so the owner learns their blobs
		// without being told to reclaim anything twice.
		survivors = nil
	}
	return publishedBy, stats, nil
}

// entriesSpanningBucket returns the resolved constituents that hold at least one
// member of the given partition, IN THE ORDER resolved gives them.
//
// ORDER PRESERVATION IS THE WHOLE CONTRACT, not a tidiness preference. The merge
// keeps the LAST copy of a repeated id, so constituent order decides which copy
// survives when two resident layers both carry a document — and the manager sorts
// its constituent lists by segment id precisely so that choice is arbitrary but
// STABLE, in a path whose contract is byte reproducibility. A filter that
// reordered would make the surviving copy depend on the filter rather than on the
// caller's sort.
//
// A MISSING SPAN ENTRY YIELDS NO MEMBERSHIP, so an entry the span map does not
// know about is simply not offered to this partition. That cannot silently drop
// members, because the map is built from the same resolved slice in the same call.
func entriesSpanningBucket[Q, S any](
	resolved []*segmentEntry[Q, S], spans map[SegmentID]map[int]bool, bucket int,
) []*segmentEntry[Q, S] {
	out := make([]*segmentEntry[Q, S], 0, len(resolved))
	for _, entry := range resolved {
		if spans[entry.meta.ID][bucket] {
			out = append(out, entry)
		}
	}
	return out
}

// harvestGroup runs every partition's harvest and reports, per partition, the
// entry it built, the id of any freshly built segment, the error it hit, and how
// many constituents it walked. It publishes nothing and removes nothing.
//
// IT IS SPLIT OUT OF ReplaceBucketGroup so the swap's bookkeeping and the
// concurrent harvest read as two things. Everything about the pool's contract is
// unchanged: workers = min(NumCPU, len(work)), a buffered work channel, results
// written into pre-sized index-addressed slices so the writes are disjoint and the
// fold order stays the ORIGINAL work order rather than a completion order.
func (e *SegmentedIndex[Q, S]) harvestGroup(
	resolved []*segmentEntry[Q, S], spans map[SegmentID]map[int]bool,
	work []BucketWork, bucketCount int,
) (harvested []*segmentEntry[Q, S], freshIDs []SegmentID, harvestErrs []error, walked []int) {
	harvested = make([]*segmentEntry[Q, S], len(work))
	freshIDs = make([]SegmentID, len(work))
	harvestErrs = make([]error, len(work))
	walked = make([]int, len(work))
	workers := min(runtime.NumCPU(), len(work))
	if workers <= 0 {
		return harvested, freshIDs, harvestErrs, walked
	}
	idxCh := make(chan int, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for i := range idxCh {
				// EACH PARTITION GETS ONLY THE CONSTITUENTS THAT SPAN IT. This is
				// the narrowing: the accept predicate already discarded every
				// non-member, so a constituent with nothing in this partition
				// contributed nothing to the output and cost a full dictionary
				// walk. walked[i] records what this harvest actually received.
				share := entriesSpanningBucket(resolved, spans, work[i].Bucket)
				walked[i] = len(share)
				harvested[i], freshIDs[i], harvestErrs[i] = e.harvestPartition(share, work[i], bucketCount)
			}
		})
	}
	for i := range work {
		idxCh <- i
	}
	close(idxCh)
	// JOIN BEFORE READING ANY RESULT. Returning on a first error while workers are
	// still running would be a data race on these slices, and would let the caller's
	// CAS publish from a partially-populated one. There is deliberately no
	// cancellation channel abandoning in-flight workers: PARTIAL FAILURE IS
	// ALL-OR-NOTHING is already the group's contract, so abandoning them changes
	// nothing observable except making the failure path racy.
	wg.Wait()
	return harvested, freshIDs, harvestErrs, walked
}

// excluding returns ids without any member of drop, sharing nothing with the
// input. It returns the input unchanged when none is present, the ordinary case.
//
// IT TAKES A SET, NOT ONE ID, AND THAT IS LOAD-BEARING. A group publishes several
// outputs against ONE union removal set, and a segment id is a content hash, so a
// SIBLING partition's output can carry the same id as a constituent this group
// consumed. Sparing only the caller's own output would tell the owner to reclaim
// the sibling's stored blob — deleting a segment that is live and just published.
//
// This is the common case rather than an exotic one: a partition the closure
// pulled in with no incoming documents is a merge of a single segment, and a
// merge of one converges to that segment's own content hash.
func excluding(ids []SegmentID, drop map[SegmentID]bool) []SegmentID {
	needed := false
	for _, id := range ids {
		if drop[id] {
			needed = true
			break
		}
	}
	if !needed {
		return ids
	}
	out := make([]SegmentID, 0, len(ids))
	for _, id := range ids {
		if !drop[id] {
			out = append(out, id)
		}
	}
	return out
}

// stampGroupSupersession records the group's durable supersession on EVERY output and
// returns the set the owner is told to reclaim.
//
// IT SPARES EVERY ID THE GROUP PUBLISHED — a sibling output can alias a consumed
// constituent's content hash, and telling anyone to retire that id would retire a
// segment that is live and just published.
//
// EVERY OUTPUT NAMES THE WHOLE COHORT. A group's constituents span its partitions, so
// it is the SET of outputs that carries their members forward and no single output can
// prove it alone; a reader honors the record only when every cohort member is present,
// which is exactly the condition "this group's replacement landed whole".
func stampGroupSupersession[Q, S any](added []*segmentEntry[Q, S], removed []SegmentID) []SegmentID {
	publishedIDs := make(map[SegmentID]bool, len(added))
	cohort := make([]SegmentID, 0, len(added))
	for _, entry := range added {
		publishedIDs[entry.meta.ID] = true
		cohort = append(cohort, entry.meta.ID)
	}
	survivors := excluding(removed, publishedIDs)
	for _, entry := range added {
		stampSupersession(entry, survivors, cohort)
	}
	return survivors
}

// fireMergeHook raises the supersession event for a completed consolidation, so
// the owner can reclaim the superseded segments' stored copies. It is the same
// event a completed background merge raises.
//
// On an Encode failure it does NOT fire: reclaiming the old copies without a
// durable copy of the consolidated one would discard the only remaining data. The
// swap has already succeeded by this point, so that failure is not an error — the
// consolidation stands, only the reclaim opportunity is skipped.
func (e *SegmentedIndex[Q, S]) fireMergeHook(entry *segmentEntry[Q, S], removed []SegmentID) {
	if e.opts.OnMerge == nil || len(removed) == 0 {
		return
	}
	envelope, payload, err := entry.blobParts()
	if err != nil {
		return
	}
	e.opts.OnMerge(MergeResult{
		Removed: removed,
		Merged: SegmentBlob{
			ID:         entry.meta.ID,
			Format:     e.format.Name(),
			Generation: entry.meta.Generation,
			DocCount:   entry.meta.DocCount,
			Bytes:      payload,
			Envelope:   envelope,
			// See doMerge: the entry is reachable for the hook's duration only
			// incidentally, so the blob pins it explicitly.
			keepAlive: entry,
		},
	})
}

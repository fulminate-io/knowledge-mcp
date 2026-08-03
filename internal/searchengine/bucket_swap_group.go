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

// BucketWork is one partition's share of a group swap: the partition number and
// the superseded ids and incoming documents that belong to it. The constituents
// are NOT per-partition — the group resolves them once and every partition
// harvests the same resolved set.
type BucketWork struct {
	Bucket     int
	Superseded []ExternalID
	Docs       []Document
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
//  2. HARVEST per partition against that same resolved set — a constituent
//     spanning four partitions is read four times and contributes a disjoint
//     share to each.
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
// partition with nothing to consolidate contributes no entry.
func (e *SegmentedIndex[Q, S]) ReplaceBucketGroup(
	bucketCount int, constituents []SegmentID, work []BucketWork,
) (map[int]SegmentID, error) {
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

	// (2) Kill superseded ids across the whole group before any harvest, so every
	// partition's accept predicate sees the same liveness.
	for _, w := range work {
		killSuperseded(set, w.Superseded)
	}

	// (3) HARVEST per partition. Build first, publish nothing yet.
	added := make([]*segmentEntry[Q, S], 0, len(work))
	publishedBy := make(map[int]SegmentID, len(work))
	remove := make(map[SegmentID]bool, len(resolved)+len(work))
	removed := make([]SegmentID, 0, len(resolved))
	for _, entry := range resolved {
		remove[entry.meta.ID] = true
		removed = append(removed, entry.meta.ID)
	}

	for _, w := range work {
		entry, freshID, err := e.harvestPartition(resolved, w, bucketCount)
		if err != nil {
			return nil, err
		}
		if freshID != "" {
			// The fresh segment was never published, but a previously-published
			// segment may carry the same content hash; dropping that id lets the
			// consolidated segment replace it rather than sit beside a duplicate.
			remove[freshID] = true
		}
		// A partition whose harvest is empty contributes nothing to publish; the
		// constituents it read are still retired by the group, because some other
		// partition took their members.
		if entry == nil {
			continue
		}
		added = append(added, entry)
		publishedBy[w.Bucket] = entry.meta.ID
	}

	if len(added) == 0 {
		// Nothing to publish. The kills above stand; the constituents are left alone
		// rather than removed, because removing them here would discard their members
		// with no output carrying them — the defect.
		return publishedBy, nil
	}

	// (4) ONE CAS for the whole group. Retry on a lost race, re-reading the set —
	// removals are keyed by segment id, so they still apply against a snapshot
	// another writer changed underneath.
	for {
		cur := e.set.Load()
		next := cur.withReplacedGroup(e.format, remove, added)
		if e.set.CompareAndSwap(cur, next) {
			break
		}
	}

	// (5) Surface the supersession, sparing EVERY id this group published — a
	// sibling output can alias a consumed constituent's content hash.
	publishedIDs := make(map[SegmentID]bool, len(added))
	for _, entry := range added {
		publishedIDs[entry.meta.ID] = true
	}
	survivors := excluding(removed, publishedIDs)
	for _, entry := range added {
		e.fireMergeHook(entry, survivors)
		// The reclaim event carries the group's superseded set once; the remaining
		// outputs are reported with no removals so the owner learns their blobs
		// without being told to reclaim anything twice.
		survivors = nil
	}
	return publishedBy, nil
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
	bytes, err := entry.payload.Encode()
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
			Bytes:      bytes,
		},
	})
}

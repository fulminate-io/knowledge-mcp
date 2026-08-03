package searchengine

// acceptLiveMembers returns the per-segment predicate Merge gates a segment's
// contribution with: a member survives when the entry still indexes it, its live
// bit is set, AND it belongs to the partition being rebuilt.
//
// THE PARTITION TEST IS WHAT MAKES CONSTITUENT POLLUTION IMPOSSIBLE rather than
// merely avoided by the caller's care. Without it a segment spanning more than one
// partition contributes ALL of its live members to whichever partition pulled it,
// so a segment left over from a smaller partition count is copied whole into each
// of the several partitions its members now hash to, multiplying the corpus.
//
// The cost is structurally nothing: Merge already consults a per-member accept
// hook, so this reuses the exact predicate it was going to call anyway.
func acceptLiveMembers[Q, S any](entry *segmentEntry[Q, S], bucket, bucketCount int) func(ExternalID) bool {
	return func(id ExternalID) bool {
		ord, ok := entry.members[id]
		return ok && entry.live.Live(ord) && BucketOf(id, bucketCount) == bucket
	}
}

// ReplaceBucket consolidates the named segments, plus a freshly built segment
// over docs, into ONE segment published in a single atomic swap. It is the write
// primitive an owner uses to re-emit one partition of the corpus without
// disturbing the rest.
//
// superseded lists documents whose current copies must stop being returned. Those
// ids are killed GLOBALLY — routed across the whole resident set, not only within
// constituents — because a caller re-emitting a partition may hold newer copies of
// documents that still live in segments outside it. Killing only within
// constituents would leave those other copies live alongside the freshly written
// one, and a search that merges per-segment hit lists does not deduplicate ids.
//
// The kills land BEFORE the swap and are visible to in-flight readers immediately,
// because live bits mutate atomically in place across snapshots that share an
// entry. That ordering is intended: a superseded document stops being returned at
// once, and the swap then removes the segment carrying it.
//
// bucket and bucketCount name the partition being rebuilt, and the accept
// predicate enforces it: a constituent contributes ONLY the live members that
// belong to this partition. A segment spanning several partitions may therefore be
// passed safely — the members it holds for other partitions are simply not taken.
//
// THAT SAFETY COMES WITH AN OBLIGATION ON THE CALLER. Every resolved constituent is
// REMOVED by the swap, so a constituent's members for partitions NOT being rebuilt
// in the same operation would be dropped. The caller must therefore close its set
// of rebuilt partitions under constituency: for every segment it offers, every
// partition that segment holds members of must also be rebuilt. Without that the
// predicate turns duplication into loss, which is strictly worse.
//
// An id in constituents with no resident segment is skipped rather than treated as
// an error — a concurrent load may already have dropped it. An empty docs slice is
// legal and consolidates the constituents alone, which is the delete-only shape.
// A Build, Merge or entry-construction error returns WITHOUT publishing, leaving
// the prior segments intact.
//
// It REPORTS the id it published, empty when there was nothing to consolidate and
// it published nothing. A caller must not infer that id by diffing the segment set
// around the call: a segment id is a content hash, so a consolidation can publish
// the very id one of its inputs already carried, and a caller that then removes
// "the old id" by name would remove the segment this call just published. Reporting
// the id is what lets the caller tell those apart.
func (e *SegmentedIndex[Q, S]) ReplaceBucket(
	bucket, bucketCount int,
	constituents []SegmentID, superseded []ExternalID, docs []Document,
) (SegmentID, error) {
	set := e.set.Load()

	// (1) Resolve the constituents against the current snapshot, skipping any that
	// are no longer resident.
	resolved := make([]*segmentEntry[Q, S], 0, len(constituents))
	for _, id := range constituents {
		if entry := set.entryByID(id); entry != nil {
			resolved = append(resolved, entry)
		}
	}

	// (2) Kill every superseded id wherever it currently lives.
	killSuperseded(set, superseded)

	// (3) Build the incoming documents into their own segment, when there are any.
	var fresh *segmentEntry[Q, S]
	if len(docs) > 0 {
		seg, err := e.format.Build(dedupeDocsByID(docs))
		if err != nil {
			return "", err
		}
		fresh, err = e.newEntry(seg, nil)
		if err != nil {
			return "", err
		}
	}

	// (4) Consolidate the constituents together with the fresh segment. Merge reads
	// live indexed data straight out of each sealed segment, which is what carries
	// the untouched members across without re-reading them from their source.
	segs := make([]Segment[Q, S], 0, len(resolved)+1)
	accept := make([]func(ExternalID) bool, 0, len(resolved)+1)
	remove := make(map[SegmentID]bool, len(resolved)+1)
	removed := make([]SegmentID, 0, len(resolved))
	for _, entry := range resolved {
		segs = append(segs, entry.payload)
		accept = append(accept, acceptLiveMembers(entry, bucket, bucketCount))
		remove[entry.meta.ID] = true
		removed = append(removed, entry.meta.ID)
	}
	if fresh != nil {
		segs = append(segs, fresh.payload)
		// The fresh segment's members are by construction all in this partition, so
		// the predicate is unchanged in effect here — it is passed the same arguments
		// so there is one rule rather than two.
		accept = append(accept, acceptLiveMembers(fresh, bucket, bucketCount))
		// The fresh segment was never published, but a previously-published segment
		// may carry the same content hash; dropping that id lets the consolidated
		// segment replace it rather than sit beside a duplicate.
		remove[fresh.meta.ID] = true
	}
	if len(segs) == 0 {
		// Nothing resident to consolidate and nothing to add. The kills above still
		// stand; there is no segment to publish.
		return "", nil
	}

	merged, err := e.format.Merge(segs, accept)
	if err != nil {
		return "", err
	}
	entry, err := e.newEntry(merged, nil)
	if err != nil {
		return "", err
	}

	// (5) Publish in ONE swap: the constituents leave and the consolidated segment
	// arrives together, so no reader ever observes a duplicate or a hole. Retry on a
	// lost race, re-reading the set — the removals are keyed by segment id, so they
	// still apply against a snapshot another writer changed underneath.
	for {
		cur := e.set.Load()
		next := cur.withReplaced(e.format, remove, entry)
		if e.set.CompareAndSwap(cur, next) {
			break
		}
	}

	// No signalMerge here, deliberately: an owner that drives its own segment layout
	// runs with the automatic merge triggers disarmed, so nudging them is pointless.

	// (6) Surface the supersession so the owner can reclaim the superseded segments'
	// stored copies — EXCEPT the id this call just published. A segment id is a
	// content hash, so consolidating a partition back to the bytes one of its inputs
	// already held republishes that same id. Leaving it in the removed list would
	// tell the owner to reclaim the stored copy of the segment that is now live.
	e.fireMergeHook(entry, excluding(removed, map[SegmentID]bool{entry.meta.ID: true}))
	return entry.meta.ID, nil
}

// harvestPartition builds ONE partition's output against the group's already
// resolved constituents. It never publishes and never removes anything — the
// group owns both — so a failure here leaves the resident set untouched, which is
// what makes the group's all-or-nothing contract possible.
//
// It returns a nil entry when the partition harvested nothing, and the id of the
// freshly built segment when there were incoming documents, so the caller can add
// that id to the group's removal set.
func (e *SegmentedIndex[Q, S]) harvestPartition(
	resolved []*segmentEntry[Q, S], w BucketWork, bucketCount int,
) (*segmentEntry[Q, S], SegmentID, error) {
	var fresh *segmentEntry[Q, S]
	if len(w.Docs) > 0 {
		seg, err := e.format.Build(dedupeDocsByID(w.Docs))
		if err != nil {
			return nil, "", err
		}
		fresh, err = e.newEntry(seg, nil)
		if err != nil {
			return nil, "", err
		}
	}

	segs := make([]Segment[Q, S], 0, len(resolved)+1)
	accept := make([]func(ExternalID) bool, 0, len(resolved)+1)
	for _, entry := range resolved {
		segs = append(segs, entry.payload)
		accept = append(accept, acceptLiveMembers(entry, w.Bucket, bucketCount))
	}
	var freshID SegmentID
	if fresh != nil {
		segs = append(segs, fresh.payload)
		accept = append(accept, acceptLiveMembers(fresh, w.Bucket, bucketCount))
		freshID = fresh.meta.ID
	}
	if len(segs) == 0 {
		return nil, freshID, nil
	}

	merged, err := e.format.Merge(segs, accept)
	if err != nil {
		return nil, "", err
	}
	entry, err := e.newEntry(merged, nil)
	if err != nil {
		return nil, "", err
	}
	if entry.meta.DocCount == 0 {
		return nil, freshID, nil
	}
	return entry, freshID, nil
}

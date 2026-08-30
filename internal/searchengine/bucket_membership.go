// SPDX-License-Identifier: Apache-2.0

// bucket_membership.go — which partitions a segment OCCUPIES, and the write-side
// supersede that keeps one live copy per id. Relocated verbatim from bucket_swap.go.
//
// SegmentSpans and BucketConstituents walk MEMBERS rather than computing siblings
// arithmetically, and that is the point of grouping them: a segment aligned to an
// older partition count spans one partition per doubling it is behind, so deriving
// siblings as bucket+oldCount finds one of them and silently drops the rest.
// AddSealAndSupersede sits with them because it is the write that creates the
// straddling state they exist to describe — a fresh tail segment holding documents
// spread across the whole corpus.

package searchengine

import "sort"

// killSuperseded clears the live bit of every id in superseded, wherever in the
// snapshot it currently lives. This is the same route-then-kill walk Delete
// performs, applied across the whole set rather than within any one segment.
func killSuperseded[Q, S any](set *segmentSet[Q, S], superseded []ExternalID) {
	for _, id := range superseded {
		sid, ok := set.route[id]
		if !ok {
			continue
		}
		entry := set.entryByID(sid)
		if entry == nil {
			continue
		}
		if ord, ok := entry.members[id]; ok {
			entry.live.Kill(ord)
		}
	}
}

// KillSuperseded is ReplaceBucketGroup's step (2) made callable WITHOUT the rebuild
// behind it: it clears the live bits of the named ids across the current snapshot and
// does nothing else. A caller that defers the partition re-emit therefore removes those
// documents from search exactly as a caller performing the re-emit does — the kill is
// the whole of what a delete's user-visible contract needs, and the reconstruction
// behind it is scheduling.
//
// IT DELEGATES AND MUST STAY A DELEGATE. Re-implementing the route-then-kill walk here
// would let the two copies drift, and the point of this method is that they cannot.
//
// IT DOES NOT SIGNAL THE MERGER, and that is deliberate rather than an omission.
// SegmentedIndex.Delete fires signalMerge PER ID, so a several-hundred-id delete would
// poke the background merger once per id from the caller's own goroutine — into the same
// per-partition graph reconstruction that deferring the re-emit exists to take off that
// path. The dead documents this leaves behind are worked off by the bounded deferred
// re-emit, and by the merger's own dead-ratio trigger in its own time.
func (e *SegmentedIndex[Q, S]) KillSuperseded(ids []ExternalID) {
	killSuperseded(e.set.Load(), ids)
}

// SealResult is what a seal-and-supersede reports: the segment now ANSWERING for the
// batch, and whether that segment was CREATED by this call or merely named by it.
//
// The distinction matters to a caller that later retires the segments its writes
// produced. On the created branch the segment is this window's own and retiring it
// is the window's business; on the aliased branch the id names a segment that was
// already resident before the call, which the window did not build and must not
// treat as its own to drop.
type SealResult struct {
	ID      SegmentID
	Created bool
}

// AddSealAndSupersede adds docs, seals them into their own immutable segment, and
// then clears any PRE-EXISTING copy of each added id from the segments that held
// it before the seal. It reports the segment answering for the batch, and whether
// the seal created it.
//
// The order is the contract: the new copy becomes searchable BEFORE the old one is
// cleared, so an id being re-added is continuously present to readers. The old
// copies are identified against the pre-seal snapshot, because the freshly sealed
// segment would otherwise shadow them in the route map — that map resolves an id
// to the LAST segment appended holding it, and the seal appends last, so a lookup
// made afterwards names the new copy and clearing through it would spare the old
// one instead.
//
// The seal does not always publish. A batch whose bytes reproduce a RESIDENT
// segment mints exactly that resident id and the append is dropped as idempotent,
// so on that branch the resident segment IS the new copy. The cleared copies are
// therefore the ones the ANSWERING segment does not hold, and the answering
// segment's own copies are made live rather than cleared — which is also how an
// id killed by an earlier delete comes back when the same content is re-added.
//
// WHICH BRANCH RAN IS NOW REPORTED, in SealResult.Created. A caller that RETIRES the
// segments its writes produced must not treat an aliased id as its own: that id
// names a segment resident before this call, holding whatever it held, and dropping
// it is not this window's decision to make.
//
// Capturing the old copies up front is safe because live bits mutate atomically in
// place across snapshots that share a segment: the snapshot taken before the seal
// holds the same segment objects the later one does, so clearing through it is
// immediately visible to current readers.
//
// It seals unconditionally, regardless of how few documents are supplied, and it
// neither merges nor ships.
func (e *SegmentedIndex[Q, S]) AddSealAndSupersede(docs []Document) (SealResult, error) {
	if len(docs) == 0 {
		return SealResult{}, nil
	}

	// (1)+(2) Resolve every incoming id to the copy that exists RIGHT NOW, before
	// anything is added. An id with no resident copy is a first-time add and
	// contributes no victim.
	// seg is captured as an ID rather than compared by pointer so the comparison
	// below survives any snapshot the engine publishes between here and the kill.
	type victim struct {
		entry *segmentEntry[Q, S]
		seg   SegmentID
		ord   int
	}
	snap := e.set.Load()
	victims := make([]victim, 0, len(docs))
	for _, d := range docs {
		sid, ok := snap.route[d.ID]
		if !ok {
			continue
		}
		entry := snap.entryByID(sid)
		if entry == nil {
			continue
		}
		if ord, ok := entry.members[d.ID]; ok {
			victims = append(victims, victim{entry: entry, seg: sid, ord: ord})
		}
	}

	// (3) Buffer and force-seal in one drain so the whole batch lands in exactly one
	// segment whose id the seal reports directly.
	e.activeMu.Lock()
	e.active = append(e.active, docs...)
	drained := e.active
	e.active = nil
	e.activeMu.Unlock()

	sealedID, created, err := e.seal(drained)
	if err != nil {
		return SealResult{}, err
	}

	// (4) Only now retire the previous copies. Doing this before the seal would
	// leave the id in neither segment for the duration of the build.
	//
	// seal reports the id of the segment ANSWERING for this batch whether or not
	// the publish appended: a batch whose bytes reproduce a resident segment mints
	// exactly that resident id, and publishAppend drops the append as idempotent.
	// Killing the captured victims regardless is what made a re-add destructive —
	// on that branch the only live copies died and no new copy ever landed, taking
	// a 20-document corpus from 20 searchable to 0 while the segment count and the
	// distinct doc count both still read healthy.
	//
	// ONE RULE COVERS BOTH BRANCHES: make the added ids live in the answering
	// segment, then kill every OTHER copy. On a genuine publish the answering
	// segment is the fresh all-live one, so the revive is a no-op and every victim
	// is elsewhere; on the aliased branch the answering segment IS where some
	// victims live, so they are skipped and the revive repairs an id an earlier
	// delete had killed there.
	//
	// AN UNRESOLVABLE ANSWERING SEGMENT KILLS NOTHING: a concurrent merge can
	// retire the segment between the seal and this read, and we cannot prove from
	// here that the new copy is searchable. A surviving stale copy is repaired by
	// the next drain; a blanked corpus is not.
	post := e.set.Load()
	answering := post.entryByID(sealedID)
	if answering == nil {
		return SealResult{ID: sealedID, Created: created}, nil
	}
	for _, d := range docs {
		if ord, ok := answering.members[d.ID]; ok {
			answering.live.Revive(ord)
		}
	}
	for _, v := range victims {
		if v.seg == sealedID {
			continue
		}
		v.entry.live.Kill(v.ord)
	}
	return SealResult{ID: sealedID, Created: created}, nil
}

// LiveMembersOutside reports, for each named segment, how many of its members are
// still LIVE and are NOT in covered. It is the containment question a caller asks
// before dropping a segment: a non-zero count means the segment is the last
// searchable home of that many documents, so dropping it would destroy them.
//
// A SEGMENT IS RECORDED ONLY WHEN ITS COUNT IS NON-ZERO, never as an explicit zero.
// Presence therefore reads as "unsafe to drop" on its own, and the map is empty in
// the converged case — the one a caller hits on nearly every call — so no allocation
// grows with a corpus that has nothing to report.
//
// A NAMED ID WITH NO RESIDENT ENTRY CONTRIBUTES NO CELL. A concurrent load may
// already have dropped it, and a segment that is not there holds nothing; this is
// how ReplaceBucket treats an unresolvable constituent too.
//
// Liveness comes from residentMemberIn, the one searchability predicate, so this
// count and every other membership answer agree about what "live" means. A deleted
// id keeps its members entry and only loses its live bit, so bare membership would
// hold a segment resident for documents no reader can reach.
//
// Segments are immutable, so a count computed here is stable for the whole
// operation that reads it — the same guarantee SegmentSpans documents.
//
// COST is O(#segments) for the one entry index plus the members of the NAMED
// segments alone. Nothing walks the corpus: the caller's segments are typically one
// write window's worth of documents.
func (e *SegmentedIndex[Q, S]) LiveMembersOutside(ids []SegmentID, covered map[ExternalID]bool) map[SegmentID]int {
	if len(ids) == 0 {
		return nil
	}
	set := e.set.Load()
	idx := entryIndex(set)
	out := make(map[SegmentID]int)
	for _, sid := range ids {
		entry := idx[sid]
		if entry == nil {
			continue
		}
		n := 0
		for id := range entry.members {
			if covered[id] {
				continue
			}
			if residentMemberIn(entry, id) {
				n++
			}
		}
		if n > 0 {
			out[sid] = n
		}
	}
	return out
}

// SegmentSpans reports, for every resident segment, the DISTINCT partitions it
// holds members of under the supplied count. It is the all-partitions form of
// BucketConstituents and answers the question that one cannot: not "who holds
// bucket b" but "what else does this segment hold".
//
// A CALLER REBUILDING SEVERAL PARTITIONS SHOULD PREFER THIS OVER A LOOP OF
// BucketConstituents, and not only for the closure it enables. Each
// BucketConstituents call walks the resident set looking for one partition, so
// asking per partition costs O(partitions x corpus); this answers every partition
// in ONE pass over the members, so the same work drops to O(corpus).
//
// A segment spanning more than one partition is the ordinary state after the
// partition count changes: the count is derived from corpus size, and every
// doubling reveals one more bit of each member's hash, so a segment aligned to an
// older count splits across the partitions its members now hash to. Spans are
// derived by walking membership, never by arithmetic on a partition number — a
// segment several counts behind spans more than a pair, and computing its siblings
// as bucket+oldCount would find one of them and silently miss the rest.
//
// Segments are immutable, so a span computed here is stable for the whole
// operation that reads it.
func (e *SegmentedIndex[Q, S]) SegmentSpans(bucketCount int) map[SegmentID][]int {
	set := e.set.Load()
	out := make(map[SegmentID][]int, len(set.entries))
	for _, entry := range set.entries {
		seen := make(map[int]bool)
		for id := range entry.members {
			seen[BucketOf(id, bucketCount)] = true
		}
		if len(seen) == 0 {
			continue
		}
		buckets := make([]int, 0, len(seen))
		for b := range seen {
			buckets = append(buckets, b)
		}
		sort.Ints(buckets)
		out[entry.meta.ID] = buckets
	}
	return out
}

// BucketConstituents reports which resident segments hold members of the given
// bucket, in the set's own order. It returns nil when no resident segment holds
// any member of that bucket.
//
// The bucket is DERIVED from membership rather than stored: every member of a
// bucket-aligned segment hashes to the same bucket, so the segment's bucket is
// recoverable from any one of its members. Nothing about the partition is
// persisted or carried in a segment's metadata.
//
// The scan stops at an entry's first matching member, which is what keeps this
// cheap: an aligned segment matches immediately, so the walk costs one probe per
// segment rather than a pass over the corpus. Only a segment holding none of the
// bucket's members is scanned in full.
//
// More than one id comes back while a partition is split across segments — the
// state a partial re-emit leaves behind — and the caller consolidates them.
//
// A CALLER REBUILDING SEVERAL PARTITIONS SHOULD PREFER SegmentSpans. Each call
// here walks the resident set looking for ONE partition, so asking per partition
// costs O(partitions x corpus); SegmentSpans answers every partition in one pass.
func (e *SegmentedIndex[Q, S]) BucketConstituents(bucket, bucketCount int) []SegmentID {
	set := e.set.Load()
	var out []SegmentID
	for _, entry := range set.entries {
		for id := range entry.members {
			if BucketOf(id, bucketCount) == bucket {
				out = append(out, entry.meta.ID)
				break
			}
		}
	}
	return out
}

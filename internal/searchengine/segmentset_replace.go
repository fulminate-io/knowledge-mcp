// SPDX-License-Identifier: Apache-2.0

// segmentset_replace.go — how a CONSOLIDATION derives the next snapshot: the
// entries that leave, what that does to the two-level route, and the counts and
// statistics the new snapshot carries.
//
// SPLIT OUT OF segmentset.go when that file reached the repository's hard
// 500-line cap; withReplaced is REWRITTEN here, the rest moved unchanged. The
// seam is the one the type already has: segmentset.go holds the snapshot, its
// route and every READER of that route, and this file holds the one derivation
// that removes entries rather than adding them — the only path that can leave the
// shared base map naming a segment that is gone.
//
// EVERYTHING HERE EXISTS BECAUSE THE BASE MAP CANNOT BE EDITED. Every older
// snapshot shares it, so a removal is recorded beside it instead of in it, which
// is what the drop set, its bound and the flatten triggers below are.

package searchengine

import "sync/atomic"

// routeDropLimit bounds THREE things a snapshot carries between flattens, all with
// one number: how many BASE entries have been consolidated out from under its flat
// route (the drop set), how many pre-resolved answers it holds for the ids those
// removals made ambiguous (the stale index), and how many ids one replacement may
// re-resolve before it flattens instead.
//
// THE SEGMENT COUNT ALONE IS NOT A BOUND ON THE WORK, which is why the stale index
// is bounded here too rather than riding the drop set's count. One consolidated-away
// segment is the base map's recorded holder for every id it held — 50 per segment in
// the fixture publish_route_stale_test.go builds, thousands on a real corpus — so a
// thousand dropped SEGMENTS can leave a hundred thousand ambiguous IDS. At 1024 the
// index holds at most that many resolved ids: about 80 KB of keys and slots by the
// residency model's own terms, against a flat rebuild of 151,206 ids.
//
// IT IS NOT A CORRECTNESS PARAMETER EITHER. Every value answers every lookup
// identically: a dropped base entry is recorded so a base hit naming it is
// re-resolved against the entries that survived, and a flatten reaches the same
// answers by rebuilding. What it trades is the COPY a replacement pays and the
// heap the snapshot holds against how often the corpus-sized rebuild is paid.
//
// ITS COST SHAPE IS NOT routeTailLimit'S, which is why it is a second constant
// rather than a reuse of the first. A tail entry costs every lookup a map probe
// until it is folded; a dropped id costs a lookup NOTHING unless the base actually
// answers with it, and then one probe of the pre-resolved stale index — the scan is
// paid once, by the replacement, in resolveHolders. So this bound is about the
// snapshot's size, not about lookup latency.
//
// 1024 MATCHES routeTailLimit ON PURPOSE. A replacement's copy at this bound is a
// thousand map inserts against the rebuild it replaces, which at a measured
// 151,206-document corpus is one insert per distinct id per resident holder — more
// than two orders of magnitude apart, so the bound is nowhere near the crossover
// and tuning it buys nothing. Matching the tail limit makes the two flattens
// COINCIDE rather than interleave, which is worth more than tuning either: at the
// measured regime (128 partitions per census, about nine constituents each) each
// trigger fires about once per census and one flatten resets both.
const routeDropLimit = 1024

// replaceParts is one replacement's bookkeeping, taken in a single pass over the
// receiver's entries: what survives plus the output, what left, how wide the base
// route now is, and which segment ids a base hit must now be checked against.
type replaceParts[Q, S any] struct {
	entries []*segmentEntry[Q, S]
	removed []*segmentEntry[Q, S]
	dropIDs []SegmentID
	baseN   int
}

// partitionForReplace compacts the entries and records what the removal did to the
// two-level route.
//
// baseN FALLS BY THE NUMBER OF REMOVED ENTRIES THAT SAT IN THE BASE, and that is
// not bookkeeping — it is what keeps entries[baseN:] the tail. The compaction
// preserves order, so after it the first baseN entries are exactly the surviving
// flattened ones; a baseN carried unchanged would slide the oldest tail entries
// behind a base map that does not index them, and they would stop answering.
func (s *segmentSet[Q, S]) partitionForReplace(
	removeIDs map[SegmentID]bool, published *segmentEntry[Q, S],
) replaceParts[Q, S] {
	p := replaceParts[Q, S]{
		entries: make([]*segmentEntry[Q, S], 0, len(s.entries)+1),
		removed: make([]*segmentEntry[Q, S], 0, len(removeIDs)),
		dropIDs: make([]SegmentID, 0, len(s.dropped)+len(removeIDs)),
		baseN:   s.baseN,
	}
	for id := range s.dropped {
		p.dropIDs = append(p.dropIDs, id)
	}
	for i, e := range s.entries {
		if !removeIDs[e.meta.ID] {
			p.entries = append(p.entries, e)
			continue
		}
		p.removed = append(p.removed, e)
		if i < s.baseN {
			p.baseN--
			// A TAIL entry needs no record: the tail is derived from the entries slice
			// itself, so removing it from the slice removes it from the route.
			p.dropIDs = append(p.dropIDs, e.meta.ID)
		}
	}
	if published != nil {
		p.entries = append(p.entries, published)
		// A segment id is a content hash, so a consolidation can publish the very id
		// one of its constituents carried — the same fact that makes ReplaceBucket
		// subtract its output from the reclaim list (bucket_swap.go). That segment is
		// resident again, in the tail, so a base hit naming it is not stale and
		// recording it as dropped would file a live segment as gone.
		p.dropIDs = excluding(p.dropIDs, map[SegmentID]bool{published.meta.ID: true})
	}
	return p
}

// residualMembers is every id the removed entries held that the published output
// does NOT carry. It is exactly the set of ids whose residency this replacement
// can end, and every other id in the corpus is untouched by it — which is what
// makes the distinct count maintainable in O(this partition) instead of O(corpus).
func residualMembers[Q, S any](
	removed []*segmentEntry[Q, S], published *segmentEntry[Q, S],
) map[ExternalID]struct{} {
	var carried idSet
	if published != nil {
		carried = published.members
	}
	residual := make(map[ExternalID]struct{})
	for _, e := range removed {
		for id := range e.members {
			if _, held := carried[id]; held {
				continue
			}
			residual[id] = struct{}{}
		}
	}
	return residual
}

// dropSetOf builds the immutable drop set. It returns nil for an empty one so a
// snapshot that has never been replaced into holds no map at all and every reader
// pays exactly what it paid before this existed.
func dropSetOf(ids []SegmentID) map[SegmentID]bool {
	if len(ids) == 0 {
		return nil
	}
	set := make(map[SegmentID]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

// endedResidency counts how many of the residual ids NO ENTRY OF THIS SNAPSHOT
// holds any more. Called on the snapshot being built, so its route already
// reflects the removal, the appended output and the new drops.
func (s *segmentSet[Q, S]) endedResidency(residual map[ExternalID]struct{}) int {
	lost := 0
	for id := range residual {
		if _, routed := s.routeOf(id); !routed {
			lost++
		}
	}
	return lost
}

// beganResidency counts the published entry's members the RECEIVER did not
// already route — the ids this replacement adds to the corpus. It is zero on the
// resident bound's path, which consolidates without incoming documents, and it is
// computed anyway because ReplaceBucket's docs parameter is public.
func (s *segmentSet[Q, S]) beganResidency(published *segmentEntry[Q, S]) int {
	if published == nil {
		return 0
	}
	gained := 0
	for id := range published.members {
		if _, resident := s.routeOf(id); !resident {
			gained++
		}
	}
	return gained
}

// routeBaseEntriesExamined counts how many FLATTENED entries the route has had to
// examine to resolve ambiguous base hits, across every snapshot in this process.
//
// IT IS THE COST THIS FILE'S DESIGN IS ABOUT, and it is a counter rather than a
// timing because the property is a count: a base hit naming a consolidated-away
// segment is ambiguous, and whether answering it costs one map probe or a walk of
// the whole flattened level is the difference between a publish that costs what its
// segment holds and one that costs what the engine holds. A wall-clock reading would
// measure the machine; this measures the code.
//
// WHERE IT IS ADDED TO IS THE WHOLE POINT. Every entry it counts is walked by
// resolveHolders, which runs ONCE PER REPLACEMENT over the ids that replacement made
// ambiguous — so a READ never adds to it, and publish_route_stale_test.go reads zero
// across a whole seal batch. It read 64,576 for a batch of 32 documents when the
// same question was answered by scanning at lookup time.
//
// It is added once per pass with the number of entries that pass walked, not once
// per entry, so the instrument costs one atomic add per replacement.
//
// replication: process-local by design — it is an instrument rather than state any
// behavior reads: nothing consults it across processes, a restart re-arms it at
// zero, and it lives in the per-machine client rather than in the replicated server
// whose N-pods-N-copies hazard that rule is about.
var routeBaseEntriesExamined atomic.Int64

// resolveHolders records, for every wanted id, the newest FLATTENED entry holding
// it. It is the pre-resolution the stale index is built from.
//
// IT WRITES ONLY HITS. An id no flattened entry holds gets no key at all, because a
// key whose answer is "absent" is one every reader already reaches by finding no key
// — it carries no information, and carrying it anyway would charge the snapshot's
// heap for it and trip the flatten that bounds the index on a corpus whose deletes
// left nothing ambiguous.
//
// ONE PASS, OLDEST FIRST, AND THE CHEAPER SIDE PER ENTRY. Oldest-first because a
// later write wins, which is flatRoute's own "last entry holding it" rule and
// therefore reaches the entry a rebuild would have indexed. The per-entry choice
// between walking that entry's members and probing the wanted set is what bounds
// the whole pass at one flat rebuild's worth of member probes: an entry smaller
// than the wanted set is walked, a larger one is probed.
//
// It is paid ONCE per replacement, over the residual ids routeDropLimit bounds,
// rather than once per lookup per id — which is the difference this index exists
// for.
func resolveHolders[Q, S any](
	base []*segmentEntry[Q, S], want map[ExternalID]struct{}, into map[ExternalID]*segmentEntry[Q, S],
) {
	for _, e := range base {
		if len(e.members) <= len(want) {
			for id := range e.members {
				if _, wanted := want[id]; wanted {
					into[id] = e
				}
			}
			continue
		}
		for id := range want {
			if _, held := e.members[id]; held {
				into[id] = e
			}
		}
	}
	routeBaseEntriesExamined.Add(int64(len(base)))
}

// staleAfterReplace derives the next snapshot's pre-resolved answers for the ids
// whose base record is, or has become, a segment that left the set.
//
// IT RESOLVES THE RESIDUAL AND NOTHING ELSE, and that is what keeps it affordable:
// an id the output CARRIES needs no answer here, because the tail holds it and the
// tail is consulted first — and if that output is itself removed later without
// carrying the id forward, the id is residual at THAT replacement and is resolved
// then. Resolving every removed member instead would cost a whole partition's ids
// per swap and trip the flatten every time.
//
// AND EVERY KEY IT HOLDS IS A REAL ANSWER. An id whose holders have all left gets
// no key: routeOf answers absent on a missing key exactly as it would on a nil one,
// so the two are the same answer and only one of them costs heap and counts toward
// the ceiling.
//
// AND IT DROPS EVERY ANSWER THIS REMOVAL INVALIDATES. A carried-over answer naming
// an entry this swap removes is skipped rather than copied: that id is residual
// here (so it is re-resolved below) or carried by the output (so the tail answers
// it), and keeping the old pointer would both lie and hold a departed entry
// reachable, delaying the release of its mapping.
func (s *segmentSet[Q, S]) staleAfterReplace(
	p replaceParts[Q, S], removeIDs map[SegmentID]bool, residual map[ExternalID]struct{},
) map[ExternalID]*segmentEntry[Q, S] {
	if len(s.staleIndex) == 0 && len(residual) == 0 {
		return nil
	}
	next := make(map[ExternalID]*segmentEntry[Q, S], len(s.staleIndex)+len(residual))
	for id, holder := range s.staleIndex {
		if holder != nil && removeIDs[holder.meta.ID] {
			continue
		}
		next[id] = holder
	}
	// A RESIDUAL ID'S ANSWER IS WHATEVER THE RESOLUTION FINDS, so any carried-over
	// answer for one is cleared first and the pass is the only writer.
	for id := range residual {
		delete(next, id)
	}
	if len(residual) > 0 {
		resolveHolders(p.entries[:p.baseN], residual, next)
	}
	if len(next) == 0 {
		return nil
	}
	return next
}

// withReplaced returns a NEW snapshot in which the entries whose SegmentID is in
// removeIDs are dropped and the consolidated entry is appended. Used by merge and
// by the resident-segment bound's per-partition consolidation. The receiver
// snapshot is left fully intact (old readers keep routing/listing the pre-change
// set).
//
// IT NO LONGER REBUILDS THE ROUTE, and that is the whole of this function's
// design. A rebuild is O(the ids held across every resident entry) and this is
// called ONCE PER PARTITION of a census: on a measured 151,206-document drain at
// 128 partitions it was 41% of the resident bound's CPU and the bound was 23% of
// the client's. The output joins the TAIL, where newest-append-wins keeps it
// answering for every id it carries forward; the removed base entries are recorded
// in the drop set so the shared base map keeps answering for everything else; and
// the count and the statistics are maintained rather than re-derived.
//
// IT STILL FLATTENS, on four triggers, because an amortization with no bound is a
// leak: the tail outgrowing routeTailLimit (the same trigger an append uses), the
// drop set outgrowing routeDropLimit, the STALE INDEX outgrowing it, and a residual
// larger than it.
//
// WHAT THE LAST TWO COST, honestly. Resolving the residual is ONE pass over the
// flattened entries taking the cheaper side per entry (resolveHolders), so it is
// bounded by one flat rebuild's worth of member probes and is paid ONCE per
// replacement — not once per lookup, which is what the scan it replaces cost and
// what made a re-drain after deletes pay O(documents x baseN) on the seal path. So
// the trigger is a ceiling that keeps the worst case at the order of the rebuild it
// avoids while the ordinary case — a residual of zero, every live member carried
// forward — costs nothing at all.
//
// At the measured regime each fires about once per census, against one rebuild per
// swap before.
func (s *segmentSet[Q, S]) withReplaced(
	f SegmentFormat[Q, S], removeIDs map[SegmentID]bool, entry *segmentEntry[Q, S],
) *segmentSet[Q, S] {
	p := s.partitionForReplace(removeIDs, entry)
	residual := residualMembers(p.removed, entry)
	if len(p.entries)-p.baseN > routeTailLimit ||
		len(p.dropIDs) > routeDropLimit ||
		len(residual) > routeDropLimit ||
		len(s.staleIndex)+len(residual) > routeDropLimit {
		return newSegmentSet(f, p.entries)
	}

	next := &segmentSet[Q, S]{
		entries:    p.entries,
		base:       s.base,
		baseN:      p.baseN,
		dropped:    dropSetOf(p.dropIDs),
		staleIndex: s.staleAfterReplace(p, removeIDs, residual),
		// RE-FOLDED, NEVER CARRIED, for the reason withReplacedPayloads states at
		// length: a format's statistics object retains the payloads it answers from,
		// and constituents just left this set. AppendStats over the output would also
		// count their documents twice and move every score.
		stats: aggregateStatsOver(f, p.entries),
	}
	// Maintained against the definition a FLATTEN uses — the number of distinct ids
	// held in any entry, live or dead — so the next flatten installs the same number
	// this one carried and no document appears or vanishes at the boundary.
	next.distinct = s.distinct - next.endedResidency(residual) + s.beganResidency(entry)
	return next
}

package searchengine

// engine_resident_aggregates.go — the RESIDENT-SET AGGREGATES: how many documents
// the published snapshot holds, how many of them are searchable, how many segments
// carry them, and which of a caller's ids are not covered.
//
// SPLIT OUT OF engine.go, unchanged, when that file reached the repository's hard
// 500-line cap. The seam is the one the aggregates already share: every answer here
// is a read of ONE immutable snapshot through the single searchability predicate
// below, so a count, a diff and a vector lookup can never disagree about what
// "covered" means. engine.go keeps the write side — the coalescing buffer, the seal,
// the entry construction and the publish.
//
// The modeled HEAP the same set holds is a different question and lives in
// residency.go: these are cardinalities, that one is bytes.

// ResidentDocCount sums meta.DocCount across every sealed segment currently
// resident in the searchable set — the in-memory engine's coverage. DocCount is
// stamped on BOTH locally-sealed (seal → newEntry) and imported (entryFromDecoded)
// segments, so the sum reflects all resident docs regardless of provenance. It is
// the read-side coverage signal the degeneracy backstop compares against the
// server's shipped doc count: a cold process whose load floor was poisoned ends up
// with a near-empty set here while the server holds the full corpus. Counts the
// SEALED set only (the lock-free atomic snapshot, same as Search/Export); the
// sub-threshold active buffer is unsearchable and intentionally excluded.
func (e *SegmentedIndex[Q, S]) ResidentDocCount() int {
	set := e.set.Load()
	total := 0
	for _, entry := range set.entries {
		total += entry.meta.DocCount
	}
	return total
}

// DistinctResidentDocCount reports how many DISTINCT documents the resident set
// holds. It is the corpus size a partition count must be derived from.
//
// WHY NOT ResidentDocCount. That one sums each segment's DocCount, so a document
// resident in more than one segment — the ordinary state after two rebuilds land
// without the first being retired — is counted once per SEGMENT. Deriving a
// partition count from it manufactures a crossing the real corpus never made,
// which is what puts segments spanning several partitions in front of a swap.
// DocCount counting distinct members within a segment does not fix that: the
// duplication here is ACROSS segments, and summing per-segment counts cannot see
// it.
//
// It is O(1) rather than a walk, and it stayed O(1) when the route became
// two-level. A snapshot MAINTAINS its distinct count as it is derived — the flat
// forms take it from the map they built, and an append adds only the ids the
// receiver did not already resolve — so this is a field read. No pass over the
// corpus is added to any path, which matters because a derivation that cost
// O(corpus) would tempt callers back onto the cheap wrong number.
func (e *SegmentedIndex[Q, S]) DistinctResidentDocCount() int {
	return e.set.Load().distinct
}

// ResidentSegmentCount reports HOW MANY sealed segments the published snapshot
// holds. It is one atomic load and a slice length — no allocation, no walk.
//
// IT IS NOT ResidentSegmentIDs' LENGTH, and the difference is why it exists. That
// one allocates a slice of every resident id, so a caller asking only for the SIZE
// of the set pays O(resident) bytes for an integer; the resident-growth bound asks
// this question on every write batch, which is precisely the path an O(resident)
// allocation must not be on. Metrics answers it too and walks every entry's
// liveDocs to do so.
func (e *SegmentedIndex[Q, S]) ResidentSegmentCount() int {
	return len(e.set.Load().entries)
}

// residentMemberIn is the ONE searchability predicate. Every membership answer in
// the PACKAGE derives from it — the aggregates below, and the by-id stored-vector
// read in vectorbyid.go — so a count, a diff and a vector lookup can never disagree
// about what "covered" means. It mirrors killSuperseded's route-then-kill walk and
// Search's accept closure: a deleted id keeps its route and members entries and only
// loses its live bit, so route presence alone is NOT membership.
//
// It takes the RESOLVED entry rather than looking it up, because entryByID is a
// linear scan over the snapshot's entries and calling it per id would make every
// aggregate below O(#resident x #segments).
func residentMemberIn[Q, S any](entry *segmentEntry[Q, S], id ExternalID) bool {
	if entry == nil {
		return false
	}
	ord, ok := entry.members[id]
	return ok && entry.live.Live(ord)
}

// entryIndex builds a SegmentID -> entry map over a snapshot so the aggregates
// below resolve each id in O(1) instead of rescanning every entry. Built once per
// aggregate call: O(#segments) here versus O(#resident x #segments) without it,
// which at production scale is millions of comparisons for a single answer.
func entryIndex[Q, S any](set *segmentSet[Q, S]) map[SegmentID]*segmentEntry[Q, S] {
	idx := make(map[SegmentID]*segmentEntry[Q, S], len(set.entries))
	for _, e := range set.entries {
		idx[e.meta.ID] = e
	}
	return idx
}

// LiveResidentCount reports how many resident documents are actually SEARCHABLE —
// distinct by construction (the route holds one entry per id) and live-true (a
// deleted-but-unpurged id is excluded).
//
// WHY NOT ResidentDocCount or liveDocs.LiveCount. ResidentDocCount sums per-segment
// DocCount, so an id resident in two segments counts twice. LiveCount is per-segment
// and summing it double-counts the same way. This walk asks the one predicate once
// per distinct id.
func (e *SegmentedIndex[Q, S]) LiveResidentCount() int {
	set := e.set.Load()
	idx := entryIndex(set)
	n := 0
	set.rangeRoute(func(id ExternalID, sid SegmentID) {
		if residentMemberIn(idx[sid], id) {
			n++
		}
	})
	return n
}

// UncoveredFrom returns the subset of ids that are NOT live-searchable in the
// current snapshot — the ids a repair pass would have to re-ship.
//
// The result is deliberately NOT pre-sized to len(ids): on a converged graph it is
// empty, and pre-sizing would allocate the whole corpus on every no-op pass.
func (e *SegmentedIndex[Q, S]) UncoveredFrom(ids []ExternalID) []ExternalID {
	set := e.set.Load()
	idx := entryIndex(set)
	var missing []ExternalID
	for _, id := range ids {
		sid, routed := set.routeOf(id)
		if !routed || !residentMemberIn(idx[sid], id) {
			missing = append(missing, id)
		}
	}
	return missing
}

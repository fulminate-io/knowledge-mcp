package searchengine

// idSet maps a member's ExternalID to its intra-segment ordinal (its index into
// the segment's IDs() / its liveDocs bit).
type idSet = map[ExternalID]int

// segmentEntry is one segment plus its mutable liveness and routing metadata.
// There is intentionally NO sourceDocs field: merge reads live INDEXED data from
// the sealed Segment via format.Merge, so the engine retains NO per-segment
// Documents (resolved open question, option c).
type segmentEntry[Q, S any] struct {
	payload Segment[Q, S]
	live    *liveDocs
	members idSet
	meta    SegmentMeta
	// record is what this entry's swap superseded, and what that swap published
	// alongside it (supersession.go). It travels into the entry's STORED bytes, which
	// is what lets a cold import decline a constituent with no external state. It is
	// written by the consolidating producer BEFORE the entry is published and never
	// afterwards — a published snapshot is immutable, liveDocs excepted.
	record supersessionRecord
	// heapPayload is how many GO HEAP bytes this entry's payload holds in its
	// encoded blob — the encoder's output when the segment was built here, and ZERO
	// when those bytes are page cache (a mapping) or memory another entry already
	// accounts for (a borrowed view, held by pin above).
	//
	// IT IS RECORDED AT CONSTRUCTION BECAUSE THE PAYLOAD CANNOT ANSWER IT. A format's
	// decoded segment reaches the engine identically whether its bytes came from a
	// Build on the heap or from a mapping of a stored file — bm25's openSegmentV2
	// takes an ordinary []byte either way and retains it verbatim — so the provenance
	// is knowable only where the entry is MADE. That is why newEntry takes it as a
	// required parameter rather than inferring it: a site that publishes a built
	// payload without saying so is a compile error, not a silently unaccounted
	// segment.
	//
	// IT IS WHAT MAKES THE RESIDENCY BUDGET REAL. A sealed segment retains its whole
	// encoded blob (78% of the on-disk size, measured), and a budget that cannot see
	// those bytes under-counts a heap-backed pool by an order of magnitude and evicts
	// on the wrong pressure signal — see ResidentHeapBytes.
	heapPayload int64
	// pin keeps alive whatever owns the memory this entry's payload reads, when
	// that owner is something OTHER than this entry.
	//
	// IT EXISTS BECAUSE A PAYLOAD CAN VIEW ANOTHER ENTRY'S MAPPING. Export hands
	// out a blob whose Bytes ARE the exporting entry's mapping, and an Import of
	// that blob decodes a new payload over those same bytes. The mapping's unmap is
	// keyed on the EXPORTING entry's reachability, and holding the bytes does not
	// make that entry reachable — so without this the imported entry reads memory
	// that is unmapped the moment the exporter is dropped. Observed as a
	// deterministic fault when a reset swap replaced a whole segment set while a
	// merge was reading segments imported from it.
	//
	// It is NOT the same thing as the cleanup attachBlobCleanup installs. That one
	// frees a mapping this entry OWNS; this one holds a reference to a mapping this
	// entry BORROWS. An entry can have either, both, or neither.
	pin any
}

// routeTailLimit is how many appended entries may sit in front of the flat base
// route before the next publish FLATTENS — rebuilds one map over every entry and
// starts a new tail.
//
// IT IS NOT A CORRECTNESS PARAMETER. Every value answers every lookup identically;
// what it trades is the per-lookup tail scan (at most this many map probes before
// the base is consulted) against the amortized cost of the flatten (one O(resident
// corpus) map rebuild every this-many publishes). Larger values make publishing
// cheaper and lookups dearer.
//
// 1024 IS MEASURED, not chosen for roundness. Amortized over a window spanning
// several flattens at 1,637 resident segments, per-publish cost across a 38x
// corpus range (13,096 to 499,285 distinct ids) was 220 µs / 104 KB to 353 µs /
// 182 KB at this value — a 1.6x/1.75x growth — against 130 µs / 69 KB to 470 µs /
// 387 KB at 256, a 3.6x/5.6x growth. The flatter curve wins because the corpus
// size a deployment reaches is not known in advance. Both beat the whole-map copy
// this replaced by more than two orders of magnitude at the large corpus.
const routeTailLimit = 1024

// segmentSet is an IMMUTABLE snapshot of the whole index: the entries, the
// externalID→SegmentID route, and the cached corpus stats. It is replaced
// wholesale via atomic.Pointer swap and NEVER mutated after publish, which is
// what makes the read path lock-free. The liveDocs inside each entry are the one
// exception — they mutate atomically in place across snapshots that share an entry.
//
// THE ROUTE IS TWO-LEVEL, and that is what keeps a publish proportional to the
// segment it publishes. A flat map over the whole corpus cannot be extended
// without copying it, so every appended segment used to copy one route entry per
// resident document — 42 MB and 60 ms of CPU per publish at half a million ids,
// paid once per sealed partition per write lease and paid AGAIN by any publisher
// that lost the publish race. Instead: entries[:baseN] are indexed by the flat
// base map, and the entries AFTER baseN are the TAIL, resolved by scanning their
// own members maps newest-first. Those maps already exist — newEntry builds one
// per segment — so a tail entry costs no additional allocation at all, and an
// append shares the base map by reference.
//
// NEWEST-APPEND-WINS SURVIVES THE SPLIT, and every lookup below depends on it:
// AddSealAndSupersede resolves an id to the LAST segment appended holding it, and
// the seal appends last (bucket_membership.go). So the tail is always scanned from
// its newest entry backwards, and only then does the base answer. A tail scanned
// oldest-first would resolve a superseded copy and the victim resolve would spare
// the stale one.
//
// A REPLACEMENT SHARES THE BASE TOO, and that is what the drop set below is for. A
// consolidation removes entries the base map indexes, and the map cannot be edited
// — every older snapshot shares it. So the removal is recorded BESIDE it instead:
// a base hit naming a dropped segment is not an answer, it is a question, and it
// is re-resolved against the entries that survived. See dropped.
type segmentSet[Q, S any] struct {
	entries []*segmentEntry[Q, S]
	// base is the flat route over entries[:baseN]. It is SHARED BY REFERENCE with
	// every snapshot derived by appending or replacing, and never written after
	// construction — which is what makes the sharing safe under the copy-on-write
	// contract above.
	base  map[ExternalID]SegmentID
	baseN int
	// dropped names the segments that a REPLACEMENT removed from entries[:baseN]
	// while carrying the base map forward. It is nil on every snapshot built flat,
	// which is every snapshot that has not yet been replaced into.
	//
	// A BASE HIT NAMING ONE OF THESE IS AMBIGUOUS, NOT ABSENT, and that is the
	// whole reason the set exists rather than the readers simply answering nil.
	// flatRoute indexes an id to the LAST entry holding it, so the base records one
	// holder and forgets the rest; an OLDER base entry may still hold the id, and
	// the rebuild this replaces would route to it. The readers therefore scan
	// entries[:baseN] newest-first on a stale hit and answer exactly what that
	// rebuild answered — which is what makes a consolidation observationally
	// identical whether it flattened or not.
	//
	// IT IS A SECOND IMMUTABLE STRUCTURE, never a mutation of the first: recording
	// a drop by writing into base would corrupt every older snapshot sharing it. So
	// a replacement copies it, which is what routeDropLimit bounds.
	dropped map[SegmentID]bool
	// staleIndex is the ANSWER to every stale base hit, resolved once when the
	// removal happens: id -> the newest surviving FLATTENED entry holding it, or nil
	// when none does. It is nil on every snapshot that has never been replaced into.
	//
	// IT EXISTS BECAUSE THE DROP SET BOUNDS SEGMENTS, NOT DOCUMENTS. routeDropLimit
	// caps how many consolidated-away segments a snapshot carries, and one of those
	// segments is the base map's recorded holder for every id it held — thousands on
	// a real corpus. Resolving each of those by SCANNING the flattened level costs
	// O(baseN) member probes per lookup, and the seal path looks an id up once per
	// incoming document (AddSealAndSupersede's victim resolve, killSuperseded), so a
	// graph that took deletes and is then re-drained paid O(documents x baseN) on the
	// one publish path whose whole design is to cost what the SEGMENT holds. Measured
	// on the fixture in publish_route_stale_test.go: 64,576 entries examined to seal
	// 32 documents at baseN 1,009. Pre-resolving moves that work to the replacement
	// that caused it, where it is paid ONCE, and leaves every reader one map probe.
	//
	// THE INVARIANT IT OWES: if base[id] names a dropped segment and no TAIL entry
	// holds id, this map has a NON-NIL key for id exactly when some older FLATTENED
	// entry still holds it, and no key at all otherwise — a missing key and an
	// absent answer are the same thing to every reader here. It is maintained by
	// resolving every non-carried member of every removed entry — the residual set
	// withReplaced already computes and routeDropLimit already bounds — and an id
	// the output DOES carry needs no key, because the tail answers it until the
	// output itself leaves, and its leaving makes that id residual.
	// requireAgreesWithAFlatRebuild is what proves the invariant holds: a hole in it
	// answers ABSENT where the flat rebuild answers an entry.
	//
	// IT NAMES ONLY RESIDENT ENTRIES. A replacement that removes an entry drops every
	// key resolving to it while copying, so the map never keeps a departed segment
	// reachable and never delays its mapping's release.
	staleIndex map[ExternalID]*segmentEntry[Q, S]
	// distinct is the number of DISTINCT resident ids across base and tail, kept
	// incrementally because DistinctResidentDocCount is O(1) by contract and derives
	// partition counts. A walk would cost O(corpus) on a path that must not.
	distinct int
	stats    S
}

// flatRoute indexes every id in entries to the LAST entry holding it.
func flatRoute[Q, S any](entries []*segmentEntry[Q, S]) map[ExternalID]SegmentID {
	route := make(map[ExternalID]SegmentID, len(entries)*4)
	for _, e := range entries {
		for id := range e.members {
			route[id] = e.meta.ID
		}
	}
	return route
}

// newSegmentSet builds the immutable snapshot for a fresh set of entries, with
// the route fully flattened (no tail) and the cached stats folded by the format.
// Every snapshot that is not an APPEND is built here: a fresh set has no
// predecessor whose base it could share, so there is nothing to amortize.
func newSegmentSet[Q, S any](f SegmentFormat[Q, S], entries []*segmentEntry[Q, S]) *segmentSet[Q, S] {
	route := flatRoute(entries)
	return &segmentSet[Q, S]{
		entries:  entries,
		base:     route,
		baseN:    len(entries),
		distinct: len(route),
		stats:    aggregateStatsOver(f, entries),
	}
}

// aggregateStatsOver folds the format's corpus statistics over every entry's
// payload. It is the fold newSegmentSet, withReplacedPayloads and withReplaced all
// owe: a format's statistics object RETAINS the segments it was folded over, so a
// snapshot whose entry set changed must re-fold rather than carry — see
// withReplacedPayloads for what the carried object goes on probing.
func aggregateStatsOver[Q, S any](f SegmentFormat[Q, S], entries []*segmentEntry[Q, S]) S {
	segs := make([]Segment[Q, S], len(entries))
	for i, e := range entries {
		segs[i] = e.payload
	}
	return f.AggregateStats(segs)
}

// tailHolderOf scans the UNFLATTENED tail newest-first for an entry holding id.
// It is newest-append-wins in its two-level form and it answers before the base
// does, on every reader.
func (s *segmentSet[Q, S]) tailHolderOf(id ExternalID) *segmentEntry[Q, S] {
	for i := len(s.entries) - 1; i >= s.baseN; i-- {
		if _, held := s.entries[i].members[id]; held {
			return s.entries[i]
		}
	}
	return nil
}

// routeOf resolves an id to the segment ANSWERING for it, newest-append-wins, or
// reports that no resident segment holds it. Route presence is not membership: a
// deleted id keeps its route and loses only its live bit (residentMemberIn).
//
// The tail is scanned newest-first and the base consulted only afterwards, which
// is the newest-append-wins contract in its two-level form. A base hit naming a
// DROPPED segment is answered from the pre-resolved stale index (see staleIndex).
func (s *segmentSet[Q, S]) routeOf(id ExternalID) (SegmentID, bool) {
	if entry := s.tailHolderOf(id); entry != nil {
		return entry.meta.ID, true
	}
	sid, ok := s.base[id]
	if !ok {
		return sid, false
	}
	if !s.dropped[sid] {
		return sid, true
	}
	if holder := s.staleIndex[id]; holder != nil {
		return holder.meta.ID, true
	}
	return "", false
}

// entryOf resolves an id straight to the ENTRY answering for it, or nil.
//
// IT IS THE FORM EVERY CALLER WANTS, and it is cheaper than the pair it replaced.
// Delete, VectorByID, killSuperseded and the victim resolve all used to route an
// id to a SegmentID and then call entryByID, which is a LINEAR SCAN over the
// snapshot's entries; at the segment counts a merge-disabled engine reaches
// between drains that scan dominated the lookup. An id answered out of the tail
// now needs no scan at all, and only a base hit pays one.
func (s *segmentSet[Q, S]) entryOf(id ExternalID) *segmentEntry[Q, S] {
	if entry := s.tailHolderOf(id); entry != nil {
		return entry
	}
	sid, ok := s.base[id]
	if !ok {
		return nil
	}
	if s.dropped[sid] {
		return s.staleIndex[id]
	}
	return s.entryByID(sid)
}

// rangeRoute calls fn once per DISTINCT resident id with the segment answering for
// it, in no particular order. It is the two-level form of ranging the flat map.
//
// IT ALLOCATES A DEDUPLICATION SET over the TAIL's ids only, not the corpus: an id
// answered by the base is emitted straight from the base walk, and the tail is
// bounded by routeTailLimit segments. The callers are the residency aggregates,
// not the search path.
func (s *segmentSet[Q, S]) rangeRoute(fn func(ExternalID, SegmentID)) {
	var seen map[ExternalID]struct{}
	if len(s.entries) > s.baseN {
		seen = make(map[ExternalID]struct{})
		for i := len(s.entries) - 1; i >= s.baseN; i-- {
			for id := range s.entries[i].members {
				if _, dup := seen[id]; dup {
					continue
				}
				seen[id] = struct{}{}
				fn(id, s.entries[i].meta.ID)
			}
		}
	}
	for id, sid := range s.base {
		if _, dup := seen[id]; dup {
			continue
		}
		if s.dropped[sid] {
			// The recorded holder left the set: emit the surviving one if there is
			// one, and emit nothing at all if there is not — an id no entry holds is
			// not a resident id, which is the same answer a rebuild reaches.
			if holder := s.staleIndex[id]; holder != nil {
				fn(id, holder.meta.ID)
			}
			continue
		}
		fn(id, sid)
	}
}

// withAppended returns a NEW snapshot with one entry added, leaving the receiver
// unmodified (copy-on-write). The entries slice is copied; the route is NOT.
//
// THE COMMON BRANCH COPIES NO MAP. The new entry joins the tail, the base map is
// carried by reference, and the only route work is counting how many of the new
// entry's ids were not already resident — so the publish costs what the SEGMENT
// holds rather than what the ENGINE holds. Once the tail would outgrow
// routeTailLimit the snapshot flattens instead, paying one corpus-sized rebuild to
// bound every subsequent lookup's tail scan.
func (s *segmentSet[Q, S]) withAppended(f SegmentFormat[Q, S], entry *segmentEntry[Q, S]) *segmentSet[Q, S] {
	entries := make([]*segmentEntry[Q, S], 0, len(s.entries)+1)
	entries = append(entries, s.entries...)
	entries = append(entries, entry)

	if len(entries)-s.baseN > routeTailLimit {
		return newSegmentSet(f, entries)
	}

	// Counted against the RECEIVER, which is the snapshot before this entry joined:
	// an id the new segment shares with a resident one is a re-add, not a new
	// document, and DistinctResidentDocCount must not move for it.
	distinct := s.distinct
	for id := range entry.members {
		if _, resident := s.routeOf(id); !resident {
			distinct++
		}
	}

	// AND IT FOLDS NO CORPUS STATISTICS EITHER. This used to build an O(resident)
	// Segment slice and hand it to AggregateStats on every publish, so a path whose
	// whole design is "cost what the SEGMENT holds, not what the ENGINE holds" paid
	// the engine-sized cost anyway — 49% of a measured client's CPU, inside the CAS
	// retry loop where a lost race re-pays it. AppendStats derives the next
	// generation's statistics from this snapshot's plus the one entry joining.
	return &segmentSet[Q, S]{
		entries: entries,
		base:    s.base,
		baseN:   s.baseN,
		// An append DROPS NOTHING, so the drop set and the stale answers it qualifies
		// are carried by reference beside the base map. Copying either here would put a
		// per-publish map copy back on the seal path this branch exists to keep free.
		dropped:    s.dropped,
		staleIndex: s.staleIndex,
		distinct:   distinct,
		stats:      f.AppendStats(s.stats, entry.payload),
	}
}

// withReplacedPayloads returns a NEW snapshot in which the named entries are
// replaced by entries holding the same segments under DIFFERENT PAYLOADS — the
// mapping swap RemapResident performs, for one segment or for a whole drain's
// worth at once.
//
// IT CARRIES THE ROUTE AND THE DISTINCT COUNT FORWARD UNTOUCHED, and that is a
// consequence of what a remap is rather than an optimisation to audit separately:
// each replacement reads BYTE-IDENTICAL bytes (a segment id IS their content hash),
// keeps the same meta.ID and the same members map, and therefore answers every
// route lookup exactly as its predecessor did. Nothing the route is derived from
// has changed.
//
// REBUILDING THE ROUTE INSTEAD IS WHAT MAKES THE RELEASE UNAFFORDABLE. newSegmentSet
// re-flattens the whole route — O(resident corpus) map inserts — so a pool releasing
// its sealed segments would pay that per swap; on a measured 9,277-segment pool
// carrying 156,723 distinct ids that is hundreds of millions of map inserts to swap
// payloads whose routing did not move. It also SILENTLY FLATTENS the two-level
// route, which moves where an appended tail entry is resolved from and makes a remap
// observable in a structure it must not touch.
//
// THE STATISTICS ARE RE-FOLDED RATHER THAN CARRIED, and that asymmetry is a
// correctness requirement, not an inconsistency. A format's stats object RETAINS ITS
// SEGMENTS — bm25's CorpusStats holds a probe chain of the very payloads it answers
// document-frequency questions from — so carrying the old object forward would leave
// the new snapshot probing the payload of an entry that is no longer in it. That old
// entry then becomes unreachable, its cleanup unmaps its blob, and the probe reads
// unmapped memory: a fault deep inside a search, at whatever later moment the
// address range is reused. The fold is O(resident segments) in header reads and
// allocates one chain node each — the same work newSegmentSet would have done — and
// it is paid ONCE per batch, which is why the release swaps a whole drain's segments
// in one call rather than one at a time.
func (s *segmentSet[Q, S]) withReplacedPayloads(
	f SegmentFormat[Q, S], replacements map[int]*segmentEntry[Q, S],
) *segmentSet[Q, S] {
	entries := make([]*segmentEntry[Q, S], len(s.entries))
	copy(entries, s.entries)
	for idx, entry := range replacements {
		entries[idx] = entry
	}
	return &segmentSet[Q, S]{
		entries: entries,
		base:    s.base,
		baseN:   s.baseN,
		// Carried beside the base map for the same reason the base map is: a remap
		// changes no segment id and no member set, so a base hit that was stale before
		// this call is stale after it, one that was not still is not, and every
		// pre-resolved answer still names an entry holding that id.
		dropped:    s.dropped,
		staleIndex: s.staleIndex,
		distinct:   s.distinct,
		stats:      aggregateStatsOver(f, entries),
	}
}

// withReplacedGroup is withReplaced for a GROUP of partitions swapped together:
// the entries in removeIDs are dropped and ALL of entries are appended, in ONE
// new snapshot. It exists because a group of partitions sharing constituents
// cannot be published as a sequence of single-partition swaps — the first swap
// would remove a constituent the later partitions have not yet harvested.
//
// IT ALSO COSTS LESS. Rebuilding the route map is O(resident corpus) and happens
// once per call, so a group of N partitions pays it ONCE rather than N times.
func (s *segmentSet[Q, S]) withReplacedGroup(
	f SegmentFormat[Q, S], removeIDs map[SegmentID]bool, added []*segmentEntry[Q, S],
) *segmentSet[Q, S] {
	entries := make([]*segmentEntry[Q, S], 0, len(s.entries)+len(added))
	for _, e := range s.entries {
		if !removeIDs[e.meta.ID] {
			entries = append(entries, e)
		}
	}
	entries = append(entries, added...)
	return newSegmentSet(f, entries)
}

// entryByID returns the entry owning the given SegmentID, or nil.
func (s *segmentSet[Q, S]) entryByID(id SegmentID) *segmentEntry[Q, S] {
	for _, e := range s.entries {
		if e.meta.ID == id {
			return e
		}
	}
	return nil
}

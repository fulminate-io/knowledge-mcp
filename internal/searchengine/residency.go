package searchengine

import "unsafe"

// mapSlotBytes is the modeled cost of ONE SLOT in a map[string]int — the key's
// string header, the int value, and the slot's share of its group's control word.
//
// THIS IS THE ONE PLACE THE MODEL LIVES for the membership term. Every other node
// that reasons about it cites this constant rather than restating a number.
//
// 27 IS MEASURED, over 2,000 membership maps at each of six densities, built
// exactly as entryFromDecoded builds them over ids that were already allocated
// (HeapAlloc after two forced collections): 30.8 bytes per slot at 17 members,
// 29.4 at 32, 27.7 at 94, 25.9 at 200 and 26.7 at 1,000. A runtime that changes
// its map layout moves it, and TestModelConstantsMatchTheRuntimesMapCost is what
// reddens when it does.
const mapSlotBytes = 27

// mapTableBytes is the fixed cost of a map that has outgrown ONE GROUP: its
// directory and table header. A single-group map has neither, which is why
// mapSlotsFor reports its size separately.
const mapTableBytes = 120

// mapSlotsFor reports how many SLOTS a Go map of n entries occupies, and whether
// it has grown past the single-group form.
//
// PER-ENTRY COST IS NOT A CONSTANT, AND MODELING IT AS ONE IS WHY THE MODEL DID
// NOT TRAVEL. A Go map holds groups of eight slots and doubles when a group would
// pass seven-eighths full, so the cost per entry sawtooths between about 32 and 58
// bytes as n moves through the cycle — measured, at eight densities. One constant
// fitted at one corpus's density (17 members per segment) therefore over-counted a
// denser corpus (94 per segment) by roughly 23 bytes per member, which on a
// 115,981-member corpus is 2.7 MB of phantom heap. The capacity IS knowable from n,
// so it is computed rather than averaged.
func mapSlotsFor(n int) (slots int64, grown bool) {
	if n <= 0 {
		return 0, false
	}
	groups := int64(1)
	for groups*7 < int64(n) {
		groups *= 2
	}
	return groups * 8, groups > 1
}

// mapHeapBytes models the Go heap a map of n entries holds, excluding the bytes
// its keys and values point at.
func mapHeapBytes(n int) int64 {
	slots, grown := mapSlotsFor(n)
	total := slots * mapSlotBytes
	if grown {
		total += mapTableBytes
	}
	return total
}

// membersHeapBytes models the Go heap one segment's membership index holds: every
// member's id bytes, plus the map structure those ids are keys of.
//
// The id bytes are counted because they are COPIED, not viewed. The mapped
// formats clone every id off the blob in IDs() precisely so the engine's route
// map can outlive the mapping, which means those bytes are genuinely on the Go
// heap and genuinely the engine's — they are not page cache.
func membersHeapBytes(m idSet) int64 {
	var n int64
	for id := range m {
		n += int64(len(id))
	}
	return n + mapHeapBytes(len(m))
}

// segmentIDHeaderBytes is the Go string header a SegmentID costs inside a slice,
// beyond the id's own bytes: a pointer and a length.
//
// IT IS A SEPARATE CONSTANT FROM THE MAP TERMS because it models a SLICE element
// rather than a map slot — no control byte, no load-factor slack — and conflating
// the two is how a model starts reporting one structure's cost for another's.
const segmentIDHeaderBytes = 16

// recordHeapBytes models the Go heap one entry's SUPERSESSION RECORD holds: the
// ids of what its swap replaced and of the cohort it was published with.
//
// THE IDS ARE COPIED, NOT VIEWED, and that is why they are heap at all. readIDs
// (supersession.go) builds each one with SegmentID(blob[cur:cur+n]), which is a
// STRING CONVERSION and therefore an allocation — the record outlives the envelope
// bytes it was decoded from, so it must. Every id is a 64-character content hash,
// so the term is ~80 bytes per recorded id and it scales with what a consolidation
// replaced rather than with what the segment holds.
//
// IT WAS THE MODEL'S LARGEST BLIND SPOT, and the shape of the corpus decides
// whether it shows: a merge-disabled code corpus carries NO records at all, so the
// model fitted on one reads within 3% while the same model reads 42% under on a
// REBUILT knowledge corpus, where 128 of 1,232 blobs carried 247,296 recorded ids
// — 15.8 MB of id bytes, 19.8 MB with their headers. A budget blind to it
// under-reads exactly the corpus the residency fences exist for.
func recordHeapBytes(rec supersessionRecord) int64 {
	var n int64
	for _, id := range rec.Superseded {
		n += int64(len(id)) + segmentIDHeaderBytes
	}
	for _, id := range rec.Cohort {
		n += int64(len(id)) + segmentIDHeaderBytes
	}
	return n
}

// routeEntryOverheadBytes is the modeled per-DISTINCT-ID cost of a snapshot's
// FLAT ROUTE MAP: the map slot holding an ExternalID key header and a SegmentID
// value header, plus the map's per-slot control and load-factor slack. Neither
// string's BYTES are counted here — the key shares its backing array with the
// members map's key and the value shares the entry's meta.ID — so this term is the
// map's own structure and nothing else.
//
// IT IS A SECOND PLACE THE ENGINE'S HEAP SCALES WITH THE CORPUS, and it was the
// whole of the residual once the blob term above landed: the route is one entry
// per distinct resident id across every pool, and the model reported none of it.
//
// 77 IS MEASURED, not chosen for roundness: the flat route over the 130,708
// distinct ids of 9,277 real stored segments, built with flatRoute's own sizing
// hint over keys and values that were already allocated, cost 77.0 bytes per
// distinct id of live heap (HeapAlloc after two forced collections). It is a Go
// map slot's cost for a two-string entry at this map's occupancy, so it moves with
// the runtime's map implementation rather than with anything in this package —
// which is why TestResidencyModelTracksMeasuredHeap (segmentdist) is kept as the
// instrument that re-measures it. Before this term existed the model reported
// nothing at all for the route, which with the members term's own shortfall is why
// a fully mapping-backed pool read ~40% under.
const routeEntryOverheadBytes = 77

// routeHeapBytes models the Go heap one published snapshot's ROUTE holds.
//
// IT COUNTS THE FLAT BASE ONLY, and the omission is the structure rather than an
// approximation: the two-level route resolves a TAIL entry out of that entry's own
// members map (segmentSet's route paragraph), and those maps are already counted
// by membersHeapBytes. A tail entry therefore adds no route bytes at all, and
// counting len(entries) here instead of the base would double-count every appended
// segment until the next flatten.
func routeHeapBytes[Q, S any](s *segmentSet[Q, S]) int64 {
	return int64(len(s.base)) * routeEntryOverheadBytes
}

// droppedHeapBytes models the Go heap one snapshot's BASE-DROP SET holds: the
// segment ids it names, plus the map structure those ids are keys of.
//
// THIS MAP IS THE LAST REFERENCE TO THOSE ID BYTES, which is what makes it a term
// rather than a rounding error. A dropped segment has LEFT the entry slice, so no
// members map, no supersession record and no meta holds its id any more — unlike
// the flat route's values, whose bytes are shared with a resident entry's meta.ID
// and are therefore deliberately NOT counted by routeEntryOverheadBytes.
//
// IT IS MODELED WITH THE MEMBERSHIP INDEX'S OWN MAP TERMS, which are fitted to a
// map[string]int rather than this map[string]bool, so the slot cost is over-read by
// the difference in value width. That is the safe direction for a budget, and it
// costs no second measured constant for a structure routeDropLimit bounds at a
// thousand entries.
func droppedHeapBytes(dropped map[SegmentID]bool) int64 {
	var n int64
	for id := range dropped {
		n += int64(len(id))
	}
	return n + mapHeapBytes(len(dropped))
}

// staleIndexHeapBytes models the Go heap one snapshot's PRE-RESOLVED STALE ANSWERS
// hold: the ids it is keyed by, plus the map structure those ids are keys of.
//
// THE VALUES ARE NOT COUNTED and that is the structure rather than an omission: each
// one is a POINTER to an entry this same walk already charges in full. The KEYS are
// counted, on the same footing as the drop set's ids: the id bytes are the map's own,
// copied from a members map that may since have left the set with its entry.
//
// It is modeled with the membership index's own map terms, which are fitted to a
// map[string]int — the same width as this map[string]pointer — so unlike the drop
// set's term there is no width correction to make. routeDropLimit bounds it at a
// thousand entries.
func staleIndexHeapBytes[Q, S any](stale map[ExternalID]*segmentEntry[Q, S]) int64 {
	var n int64
	for id := range stale {
		n += int64(len(id))
	}
	return n + mapHeapBytes(len(stale))
}

// entryStructuralBytes models what ONE resident entry costs beyond the four terms
// that scale with its contents: the entry struct itself, the liveDocs header in
// front of its words, the snapshot's pointer slot, and the format's per-segment
// statistics node.
//
// THE STRUCTS ARE TAKEN WITH unsafe.Sizeof rather than written as literals, for
// the reason bm25's own HeapBytes states: a model written as a number rots
// silently the next time either struct gains a field. Only the two terms this
// package cannot measure that way are constants — the snapshot's 8-byte pointer
// slot, and a statistics chain node, which is the format's own allocation and is
// one pointer and one interface wide in both shipped formats.
//
// IT USED TO BE AN EXCLUSION rather than a term, on the argument that ~180 bytes
// per segment is noise. On a 9,277-segment corpus it is 1.7 MB, which is most of
// the margin the ticket's 10% row allows, so it is counted.
const statsNodeAndSlotBytes = 32

func entryStructuralBytes[Q, S any](entry *segmentEntry[Q, S]) int64 {
	n := int64(unsafe.Sizeof(*entry)) + statsNodeAndSlotBytes
	if entry.live != nil {
		n += int64(unsafe.Sizeof(*entry.live))
	}
	return n
}

// ResidentHeapBytes models the total Go heap the resident segment set holds.
//
// THE NUMBER IS A MODELED ESTIMATE, NOT A MEASUREMENT, and a caller must not
// read it as an exact byte count. Go exposes no per-object heap query, so this
// is a documented formula over the things a resident segment actually keeps on
// the heap:
//
//  1. the payload's own declared heap        — payload.HeapBytes()
//  2. the payload's RETAINED ENCODED BLOB    — entry.heapPayload
//  3. the membership index                   — membersHeapBytes(entry.members)
//  4. the liveness bitset                    — 8 bytes per 64-bit word
//  5. the snapshot's flat route map          — routeHeapBytes(set)
//  6. the decoded supersession record        — recordHeapBytes(entry.record)
//  7. the entry's own structures              — entryStructuralBytes(entry)
//  8. the snapshot's base-drop set           — droppedHeapBytes(set.dropped)
//  9. its pre-resolved stale answers          — staleIndexHeapBytes(set.staleIndex)
//
// TERM 2 IS THE ONE A PAYLOAD CANNOT DECLARE FOR ITSELF, and leaving it out was
// a 10x under-count on a heap-backed pool: a sealed segment retains the whole
// encoder output (27.6 KB against a 35.6 KB stored blob, measured on a 3,789-
// segment drain) because the mapped formats read their postings and
// dictionaries in place over those same bytes. HeapBytes cannot see it — the
// format's decoded segment is the same type whether the bytes are heap or page
// cache — so the provenance is recorded on the ENTRY at construction
// (segmentEntry.heapPayload) and added here.
//
// WHAT IT STILL DELIBERATELY EXCLUDES is a MAPPED payload's blob, which is what
// term 2 reads as zero for. Those bytes are page cache: evictable, shared
// between processes, and invisible to the garbage collector. Counting them
// would meter memory the heap does not hold, which is the exact defect this
// meter replaces — a budget saturated by page-cache-backed bytes evicts on the
// wrong pressure signal. So the rule is neither "a blob is always counted" nor
// "a blob is never counted": a blob is counted when it is on the heap.
//
// WHAT IT LEAVES OUT, named so a residual is not mistaken for a defect: the
// segmentEntry struct, the liveDocs header in front of its words, the format's
// per-segment statistics node and the snapshot's entry slot — about 180 bytes per
// resident segment — and the allocator's size-class rounding on the copied member
// and record ids.
//
// MEASURED ON TWO CORPUS SHAPES, because one of them cannot see term 6 at all: a
// merge-disabled CODE corpus, whose blobs carry no supersession records, and a
// rebuilt KNOWLEDGE corpus, where a minority of blobs carry hundreds of recorded
// ids each. segmentdist's TestResidencyModelTracksMeasuredHeap is the instrument
// and bounds both at 10%; TestResidencyModelHoldsOnASyntheticCorpus keeps a
// record-carrying fixture in the repo so a runtime that moves the map or slice
// costs reddens without an operator corpus.
//
// CONCURRENCY: the entry set is read through the SAME single atomic load the
// query path uses (see SegmentedIndex.Search, which takes no mutex and no
// RLock). This is a read of the same immutable snapshot and takes the same
// route; a lock here would be the only lock on that structure and would
// contend with nothing but itself.
//
// COST: O(total resident members), because the membership term walks each
// map. That is the same order as one budget pass's existing candidate walk and
// sort, and the pass runs after a completed search or once per reconcile
// sweep — never in the query hot path.
func (e *SegmentedIndex[Q, S]) ResidentHeapBytes() int64 {
	set := e.set.Load()
	if set == nil {
		return 0
	}
	var n int64
	for _, entry := range set.entries {
		if entry == nil {
			continue
		}
		if entry.payload != nil {
			n += entry.payload.HeapBytes()
		}
		n += entry.heapPayload
		n += membersHeapBytes(entry.members)
		n += recordHeapBytes(entry.record)
		n += entryStructuralBytes(entry)
		if entry.live != nil {
			n += int64(len(entry.live.words)) * 8
		}
	}
	return n + routeHeapBytes(set) + droppedHeapBytes(set.dropped) + staleIndexHeapBytes(set.staleIndex)
}

// HeapBackedResidentIDs lists the resident segments whose payload still holds its
// encoded blob on the GO HEAP — the segments a republication over their stored
// file would turn into page cache.
//
// IT IS THE SEAL PATH'S WORK LIST. A merge learns which segment to remap from its
// own output; a SEAL does not, because the release happens later, on the
// durability path that writes the resident set to L2, and by then the only thing
// that distinguishes a freshly sealed segment from a loaded one is this
// provenance. Reading it from the published snapshot rather than from a caller's
// bookkeeping is what makes the list honest after an eviction, a merge or a group
// swap moved the set underneath.
//
// Same snapshot semantics as Export and ResidentSegmentIDs: one atomic load of the
// sealed set, no lock, and NO per-segment Encode — the answer is a field on the
// entry. A segment that is remapped, evicted or superseded between this call and
// the remap is handled by RemapResident, which declines and releases rather than
// resurrecting it.
func (e *SegmentedIndex[Q, S]) HeapBackedResidentIDs() []SegmentID {
	set := e.set.Load()
	if set == nil {
		return nil
	}
	var ids []SegmentID
	for _, entry := range set.entries {
		if entry != nil && entry.heapPayload > 0 {
			ids = append(ids, entry.meta.ID)
		}
	}
	return ids
}

package searchengine

// memberEntryOverheadBytes is the modeled per-member cost of the engine's
// membership index BEYOND the id's own bytes: the string header the map key
// holds, the int ordinal it maps to, and the map's per-slot bucket overhead.
//
// THIS IS THE ONE PLACE THE MODEL LIVES. Every other node that reasons about
// the membership term cites this constant rather than restating a number, so
// the model can be corrected in a single edit instead of drifting across the
// files that consume it.
const memberEntryOverheadBytes = 48

// membersHeapBytes models the Go heap one segment's membership index holds:
// each member's id bytes plus memberEntryOverheadBytes.
//
// The id bytes are counted because they are COPIED, not viewed. The mapped
// formats clone every id off the blob in IDs() precisely so the engine's route
// map can outlive the mapping, which means those bytes are genuinely on the Go
// heap and genuinely the engine's — they are not page cache.
func membersHeapBytes(m idSet) int64 {
	var n int64
	for id := range m {
		n += int64(len(id)) + memberEntryOverheadBytes
	}
	return n
}

// ResidentHeapBytes models the total Go heap the resident segment set holds.
//
// THE NUMBER IS A MODELED ESTIMATE, NOT A MEASUREMENT, and a caller must not
// read it as an exact byte count. Go exposes no per-object heap query, so this
// is a documented formula over the three things a resident segment actually
// keeps on the heap:
//
//  1. the payload's own declared heap        — payload.HeapBytes()
//  2. the membership index                   — membersHeapBytes(entry.members)
//  3. the liveness bitset                    — 8 bytes per 64-bit word
//
// WHAT IT DELIBERATELY EXCLUDES is a mapped payload's blob. Those bytes are
// page cache: evictable, shared between processes, and invisible to the
// garbage collector. Counting them would meter memory the heap does not hold,
// which is the exact defect this meter replaces — a budget saturated by
// page-cache-backed bytes evicts on the wrong pressure signal.
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
		n += membersHeapBytes(entry.members)
		if entry.live != nil {
			n += int64(len(entry.live.words)) * 8
		}
	}
	return n
}

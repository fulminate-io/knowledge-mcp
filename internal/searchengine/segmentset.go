package searchengine

import "maps"

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

// segmentSet is an IMMUTABLE snapshot of the whole index: the entries, an
// externalID→SegmentID route map, and the cached corpus stats. It is replaced
// wholesale via atomic.Pointer swap and NEVER mutated after publish, which is
// what makes the read path lock-free. The liveDocs inside each entry are the one
// exception — they mutate atomically in place across snapshots that share an entry.
type segmentSet[Q, S any] struct {
	entries []*segmentEntry[Q, S]
	route   map[ExternalID]SegmentID
	stats   S
}

// newSegmentSet builds the immutable snapshot for a fresh set of entries,
// computing the route map and (via the supplied format) the cached stats.
func newSegmentSet[Q, S any](f SegmentFormat[Q, S], entries []*segmentEntry[Q, S]) *segmentSet[Q, S] {
	route := make(map[ExternalID]SegmentID, len(entries)*4)
	segs := make([]Segment[Q, S], len(entries))
	for i, e := range entries {
		segs[i] = e.payload
		for id := range e.members {
			route[id] = e.meta.ID
		}
	}
	return &segmentSet[Q, S]{
		entries: entries,
		route:   route,
		stats:   f.AggregateStats(segs),
	}
}

// withAppended returns a NEW snapshot with one entry added, leaving the receiver
// unmodified. The entries slice and route map are copied (copy-on-write); the
// route rebuild touches only the new entry's ids, not the whole corpus.
func (s *segmentSet[Q, S]) withAppended(f SegmentFormat[Q, S], entry *segmentEntry[Q, S]) *segmentSet[Q, S] {
	entries := make([]*segmentEntry[Q, S], 0, len(s.entries)+1)
	entries = append(entries, s.entries...)
	entries = append(entries, entry)

	route := make(map[ExternalID]SegmentID, len(s.route)+len(entry.members))
	maps.Copy(route, s.route)
	for id := range entry.members {
		route[id] = entry.meta.ID
	}

	segs := make([]Segment[Q, S], len(entries))
	for i, e := range entries {
		segs[i] = e.payload
	}
	return &segmentSet[Q, S]{entries: entries, route: route, stats: f.AggregateStats(segs)}
}

// withReplaced returns a NEW snapshot in which the entries whose SegmentID is in
// removeIDs are dropped and the consolidated entry is appended. Used by merge.
// The receiver snapshot is left fully intact (old readers keep routing/listing
// the pre-change set).
func (s *segmentSet[Q, S]) withReplaced(f SegmentFormat[Q, S], removeIDs map[SegmentID]bool, entry *segmentEntry[Q, S]) *segmentSet[Q, S] {
	entries := make([]*segmentEntry[Q, S], 0, len(s.entries)+1)
	for _, e := range s.entries {
		if !removeIDs[e.meta.ID] {
			entries = append(entries, e)
		}
	}
	if entry != nil {
		entries = append(entries, entry)
	}

	route := make(map[ExternalID]SegmentID, len(s.route))
	for _, e := range entries {
		for id := range e.members {
			route[id] = e.meta.ID
		}
	}

	segs := make([]Segment[Q, S], len(entries))
	for i, e := range entries {
		segs[i] = e.payload
	}
	return &segmentSet[Q, S]{entries: entries, route: route, stats: f.AggregateStats(segs)}
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

	route := make(map[ExternalID]SegmentID, len(s.route))
	for _, e := range entries {
		for id := range e.members {
			route[id] = e.meta.ID
		}
	}

	segs := make([]Segment[Q, S], len(entries))
	for i, e := range entries {
		segs[i] = e.payload
	}
	return &segmentSet[Q, S]{entries: entries, route: route, stats: f.AggregateStats(segs)}
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

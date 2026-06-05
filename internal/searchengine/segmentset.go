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

// entryByID returns the entry owning the given SegmentID, or nil.
func (s *segmentSet[Q, S]) entryByID(id SegmentID) *segmentEntry[Q, S] {
	for _, e := range s.entries {
		if e.meta.ID == id {
			return e
		}
	}
	return nil
}

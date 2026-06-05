package searchengine

import (
	"fmt"
	"runtime"
	"sync"
)

// Export serializes every current segment to a shippable SegmentBlob. The
// client marshals these and ships them; the server stores them opaquely.
func (e *SegmentedIndex[Q, S]) Export() []SegmentBlob {
	set := e.set.Load()
	blobs := make([]SegmentBlob, 0, len(set.entries))
	for _, entry := range set.entries {
		bytes, err := entry.payload.Encode()
		if err != nil {
			continue
		}
		blobs = append(blobs, SegmentBlob{
			ID:         entry.meta.ID,
			Format:     e.format.Name(),
			Generation: entry.meta.Generation,
			Bytes:      bytes,
		})
	}
	return blobs
}

// Import decodes a batch of blobs into segments and publishes them in ONE CAS.
// Each decoded segment seeds its liveDocs from the tombstones (contract: liveDocs
// seeded from tombstones at Import), so already-deleted documents start dead.
//
// Imported and locally-built segments are MERGE-EQUIVALENT: background merge
// calls format.Merge, which reads live INDEXED data directly from a decoded
// Segment — no source Documents required. This is the whole point of the amended
// Merge contract.
func (e *SegmentedIndex[Q, S]) Import(blobs []SegmentBlob, tombstones []ExternalID) error {
	if len(blobs) == 0 {
		return nil
	}

	entries := make([]*segmentEntry[Q, S], len(blobs))
	errs := make([]error, len(blobs))
	sem := make(chan struct{}, runtime.NumCPU())
	var wg sync.WaitGroup
	for i, blob := range blobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, blob SegmentBlob) {
			defer wg.Done()
			defer func() { <-sem }()
			seg, err := e.format.Decode(blob.Bytes)
			if err != nil {
				errs[i] = fmt.Errorf("decode segment %s: %w", blob.ID, err)
				return
			}
			entries[i] = e.entryFromDecoded(seg, blob, tombstones)
		}(i, blob)
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return err
		}
	}

	e.publishImport(entries)
	return nil
}

// entryFromDecoded wraps a decoded segment into an entry: members from IDs(),
// liveDocs seeded from tombstones, meta carrying the blob's content-addressed ID
// and Generation.
func (e *SegmentedIndex[Q, S]) entryFromDecoded(seg Segment[Q, S], blob SegmentBlob, tombstones []ExternalID) *segmentEntry[Q, S] {
	ids := seg.IDs()
	members := make(idSet, len(ids))
	for ord, extID := range ids {
		members[extID] = ord
	}
	live := newLiveDocsFromTombstones(tombstones, members)
	return &segmentEntry[Q, S]{
		payload: seg,
		live:    live,
		members: members,
		meta: SegmentMeta{
			ID:         blob.ID,
			Format:     blob.Format,
			Generation: blob.Generation,
			DocCount:   len(ids),
			DeadCount:  live.DeadCount(),
		},
	}
}

// publishImport CAS-appends the imported entries to the current set in one swap,
// retrying on a lost CAS.
func (e *SegmentedIndex[Q, S]) publishImport(entries []*segmentEntry[Q, S]) {
	for {
		old := e.set.Load()
		merged := make([]*segmentEntry[Q, S], 0, len(old.entries)+len(entries))
		merged = append(merged, old.entries...)
		merged = append(merged, entries...)
		next := newSegmentSet[Q, S](e.format, merged)
		if e.set.CompareAndSwap(old, next) {
			e.signalMerge()
			return
		}
	}
}

// Unload drops the named segments from the searchable set via one CAS swap. The
// reload path (driven by a SegmentSource) is a later ticket.
func (e *SegmentedIndex[Q, S]) Unload(ids []SegmentID) {
	if len(ids) == 0 {
		return
	}
	remove := make(map[SegmentID]bool, len(ids))
	for _, id := range ids {
		remove[id] = true
	}
	for {
		old := e.set.Load()
		kept := make([]*segmentEntry[Q, S], 0, len(old.entries))
		for _, entry := range old.entries {
			if !remove[entry.meta.ID] {
				kept = append(kept, entry)
			}
		}
		next := newSegmentSet[Q, S](e.format, kept)
		if e.set.CompareAndSwap(old, next) {
			return
		}
	}
}

package searchengine

import (
	"crypto/sha256"
	"encoding/hex"
	"runtime"
	"sync"
	"sync/atomic"
)

// SegmentedIndex is the index-agnostic engine. It holds an immutable segmentSet
// behind an atomic.Pointer (the lock-free read path) and a small pre-seal
// coalescing buffer behind activeMu (the only write-side lock). There is NO
// per-sealed-segment Document retention: once a batch is sealed into a Segment,
// the source Documents are dropped — merge reads live indexed data from the
// sealed Segment via format.Merge.
type SegmentedIndex[Q, S any] struct {
	format SegmentFormat[Q, S]
	opts   Options

	// set is the published immutable snapshot. Search loads it with a single
	// atomic load and never takes a lock.
	set atomic.Pointer[segmentSet[Q, S]]

	// activeMu guards ONLY the active coalescing buffer (the write side). The
	// read path never touches it.
	activeMu sync.Mutex
	active   []Document

	// merge background machinery (startMerger/Close/Metrics live in merge.go).
	stopOnce    sync.Once
	stop        chan struct{}
	mergeSignal chan struct{}
	mergeCnt    atomic.Uint64
}

// New constructs an engine over the given format and options. It seeds an empty
// published set so Search never has to nil-check, and applies option defaults.
func New[Q, S any](f SegmentFormat[Q, S], opts Options) *SegmentedIndex[Q, S] {
	e := &SegmentedIndex[Q, S]{
		format:      f,
		opts:        opts.withDefaults(),
		stop:        make(chan struct{}),
		mergeSignal: make(chan struct{}, 1),
	}
	e.set.Store(newSegmentSet[Q, S](f, nil))
	e.startMerger()
	return e
}

// Add appends docs to the coalescing buffer; once the buffer reaches
// MinSegmentDocs it drains, seals one immutable segment via format.Build, and
// publishes it with a single CAS-append. Build (the heavy work) runs OUTSIDE the
// CAS loop; the set CAS is the only publish serialization.
func (e *SegmentedIndex[Q, S]) Add(docs []Document) error {
	if len(docs) == 0 {
		return nil
	}

	e.activeMu.Lock()
	e.active = append(e.active, docs...)
	if len(e.active) < e.opts.MinSegmentDocs {
		// Sub-threshold: stays buffered in the pre-seal staging slice
		// (unsearchable until sealed). Not per-segment retention — there is none.
		e.activeMu.Unlock()
		return nil
	}
	drained := e.active
	e.active = nil
	e.activeMu.Unlock()

	return e.seal(drained)
}

// Flush force-seals whatever is currently in the active coalescing buffer into
// an immutable segment, REGARDLESS of len(active) vs Options.MinSegmentDocs. It
// is the explicit counterpart to Add's threshold-gated seal: Add leaves a
// sub-threshold tail buffered (and therefore unsearchable + un-exportable);
// Flush seals that tail so it becomes searchable and ships on the next Export.
//
// A no-op (nil) when the buffer is empty. Reuses the same seal()/publishAppend
// path Add uses, so a flushed segment is indistinguishable from a
// threshold-sealed one. The one-time migration calls this (via Manager.Flush)
// so a graph with fewer than MinSegmentDocs indexed nodes — which would
// otherwise produce ZERO searchable segments — becomes searchable.
func (e *SegmentedIndex[Q, S]) Flush() error {
	e.activeMu.Lock()
	if len(e.active) == 0 {
		e.activeMu.Unlock()
		return nil
	}
	drained := e.active
	e.active = nil
	e.activeMu.Unlock()
	return e.seal(drained)
}

// seal builds an immutable segment from the drained docs and CAS-appends it.
func (e *SegmentedIndex[Q, S]) seal(docs []Document) error {
	seg, err := e.format.Build(docs)
	if err != nil {
		return err
	}
	entry, err := e.newEntry(seg, nil)
	if err != nil {
		return err
	}
	e.publishAppend(entry)
	e.signalMerge()
	return nil
}

// newEntry wraps a sealed segment into a segmentEntry: content-hash SegmentID,
// all-live (or tombstone-seeded) liveDocs, and the members route map. tombstones
// is nil for locally-built segments and set at Import.
func (e *SegmentedIndex[Q, S]) newEntry(seg Segment[Q, S], tombstones []ExternalID) (*segmentEntry[Q, S], error) {
	blob, err := seg.Encode()
	if err != nil {
		return nil, err
	}
	id := contentHash(blob)

	ids := seg.IDs()
	members := make(idSet, len(ids))
	for ord, extID := range ids {
		members[extID] = ord
	}

	var live *liveDocs
	if len(tombstones) == 0 {
		live = newLiveDocs(len(ids))
	} else {
		live = newLiveDocsFromTombstones(tombstones, members)
	}

	return &segmentEntry[Q, S]{
		payload: seg,
		live:    live,
		members: members,
		meta: SegmentMeta{
			ID:        id,
			Format:    e.format.Name(),
			DocCount:  len(ids),
			DeadCount: live.DeadCount(),
		},
	}, nil
}

// publishAppend CAS-publishes a new snapshot with entry appended, retrying on a
// lost CAS. The body is a slice+map copy (O(new segment)); no heavy work here.
func (e *SegmentedIndex[Q, S]) publishAppend(entry *segmentEntry[Q, S]) {
	for {
		old := e.set.Load()
		next := old.withAppended(e.format, entry)
		if e.set.CompareAndSwap(old, next) {
			return
		}
	}
}

// Delete routes id→segment and clears its liveDocs bit (O(1), atomic, lock-free
// against readers). An id still in the un-sealed active buffer is removed there.
// An unknown id is a no-op. No indexed data mutates — only the liveDocs bit.
func (e *SegmentedIndex[Q, S]) Delete(id ExternalID) {
	set := e.set.Load()
	if sid, ok := set.route[id]; ok {
		if entry := set.entryByID(sid); entry != nil {
			if ord, ok := entry.members[id]; ok {
				entry.live.Kill(ord)
				e.signalMerge()
			}
		}
		return
	}

	// Not yet sealed — drop it from the coalescing buffer.
	e.activeMu.Lock()
	for i := range e.active {
		if e.active[i].ID == id {
			e.active = append(e.active[:i], e.active[i+1:]...)
			break
		}
	}
	e.activeMu.Unlock()
}

// VectorByID resolves a member's stored vector by external id, or (nil,false) when
// no sealed segment holds it. It mirrors Delete's route-map walk (set.route → owning
// entry) for an O(1) lookup + O(#segments) entryByID scan — no full-corpus walk —
// then reads the vector off the segment's concrete payload via a runtime
// type-assert to the by-id accessor. The inline-interface assert keeps the method
// generic-safe across [Q,S]: the HNSW instantiation's payload (*hnswSegment)
// satisfies it; a payload without the accessor (e.g. bm25) fails the assert and
// yields (nil,false) — never a panic, never a wrong-type read. Vectors only exist
// on sealed segments, so the un-sealed active buffer is intentionally not consulted.
func (e *SegmentedIndex[Q, S]) VectorByID(externalID ExternalID) ([]byte, bool) {
	set := e.set.Load()
	sid, ok := set.route[externalID]
	if !ok {
		return nil, false
	}
	entry := set.entryByID(sid)
	if entry == nil {
		return nil, false
	}
	vb, ok := entry.payload.(interface {
		VectorByID(string) ([]byte, bool)
	})
	if !ok {
		return nil, false
	}
	return vb.VectorByID(externalID)
}

// Search runs a lock-free, parallel cross-segment query. It loads the immutable
// set with a SINGLE atomic load (NO mutex, NO RLock — activeMu is never touched
// here), fans out one goroutine per segment bounded by NumCPU, each writing a
// preallocated result slot (no shared-slice contention), then merges the global
// top-k. The liveDocs accept filter excludes deleted ids. The only
// synchronization is the atomic load + the fan-out WaitGroup/semaphore.
func (e *SegmentedIndex[Q, S]) Search(q Q, k int) []Hit {
	set := e.set.Load()
	if len(set.entries) == 0 || k <= 0 {
		return nil
	}

	results := make([][]Hit, len(set.entries))
	sem := make(chan struct{}, runtime.NumCPU())
	var wg sync.WaitGroup
	for i, entry := range set.entries {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, entry *segmentEntry[Q, S]) {
			defer wg.Done()
			defer func() { <-sem }()
			accept := func(id ExternalID) bool {
				ord, ok := entry.members[id]
				return ok && entry.live.Live(ord)
			}
			results[i] = entry.payload.Search(q, set.stats, k, accept)
		}(i, entry)
	}
	wg.Wait()

	return mergeTopK(results, k)
}

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

// contentHash returns the sha256 hex digest of a segment blob — the SegmentID.
func contentHash(blob []byte) SegmentID {
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:])
}

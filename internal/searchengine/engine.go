package searchengine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

	_, _, err := e.seal(drained)
	return err
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
	_, _, err := e.seal(drained)
	return err
}

// seal builds an immutable segment from the drained docs and CAS-appends it,
// returning the id of the segment it produced and whether the append actually
// landed. It reports the id directly rather than leaving a caller to infer it by
// comparing segment sets around the seal: publishAppend runs with activeMu released,
// so a concurrent drain on the same engine can publish between any two observations
// and such a comparison would attribute the wrong segment.
//
// THE RETURNED ID IS THE SEGMENT THIS BATCH DENOTES, not proof the set grew.
// publishAppend is idempotent by segment id, so re-sealing an unchanged group
// returns the id of the copy already resident and appends nothing. That is the
// answer callers want either way — the id names the segment now answering for
// these documents.
//
// THE SECOND RETURN IS WHICH OF THOSE TWO HAPPENED, and it is not decoration: a
// caller that later RETIRES the segments a write produced must be able to tell a
// segment it created from one it merely named. Retiring an id the batch aliased
// would drop a segment that was already resident and is not this window's to drop.
func (e *SegmentedIndex[Q, S]) seal(docs []Document) (SegmentID, bool, error) {
	seg, err := e.format.Build(dedupeDocsByID(docs))
	if err != nil {
		return "", false, err
	}
	entry, err := e.newEntry(seg, nil)
	if err != nil {
		return "", false, err
	}
	created := e.publishAppend(entry)
	e.signalMerge()
	return entry.meta.ID, created, nil
}

// dedupeDocsByID collapses a build batch to at most one document per id, LAST-WINS,
// preserving the order of the surviving entries.
//
// IT IS THE SEAL SIDE of newEntry's graph-equals-members invariant, and it exists
// because a repeated id in a build batch is LEGITIMATELY REACHABLE rather than a
// defect: an id sitting in an unsealed tail can also arrive in the next batch, and a
// drain seals the whole buffer in ONE Build, so both copies reach the builder through
// an ordinary, correct sequence. Rejecting that at newEntry would turn a normal write
// into a hard error, so the batch is normalised here instead.
//
// LAST-WINS is not an invented rule: the route map newEntry builds is itself
// last-append-wins, so keeping the last copy makes the built index agree with the
// membership the engine would have recorded anyway.
//
// This is deliberately NOT the merge path's answer. A merge that produces duplicates
// is the defect, and it must reach newEntry's error rather than be quietly repaired —
// see the invariant's comment for why silently deduplicating there would make the
// check unfalsifiable.
func dedupeDocsByID(docs []Document) []Document {
	lastAt := make(map[ExternalID]int, len(docs))
	for i, d := range docs {
		lastAt[d.ID] = i
	}
	if len(lastAt) == len(docs) {
		return docs // the overwhelmingly common case: nothing repeated, no copy.
	}
	out := make([]Document, 0, len(lastAt))
	for i, d := range docs {
		if lastAt[d.ID] == i {
			out = append(out, d)
		}
	}
	return out
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

	// GRAPH EQUALS MEMBERS, enforced here because this is the one FORMAT-AGNOSTIC
	// choke point every SEALED and every MERGED segment passes through. The two
	// numbers compared are ones this function already computes: len(ids) is what the
	// built index actually holds, len(members) is the distinct route map over it.
	//
	// WHY IT IS AN ERROR AND NEVER A SILENT DEDUP. A merge whose constituents share an
	// id admits BOTH copies into the builder, so the index carries two nodes for one id
	// while this map — last-wins — records a single ordinal. Membership then passes
	// everywhere (each id appears exactly once), VectorByID resolves (the route map
	// resolves), and retrieval craters, because half the graph is unreachable through
	// the accept path. Repairing that here would make the condition undetectable: the
	// check would silently fix the thing it exists to catch, and no gate over it could
	// ever go red again. Failing loudly is the point.
	//
	// THE COMPARISON MUST BE RAW-vs-DISTINCT. Comparing distinct to distinct is an
	// identity that holds no matter how duplicated the index is, and that identity is
	// exactly the masking this check undoes.
	//
	// THE UPSTREAM OBLIGATION IS SPLIT, on purpose. A MERGE that produces duplicates is
	// the defect and must reach this error — the formats deduplicate at item collection
	// so a correct merge never does. A SEAL over a batch that happens to repeat an id
	// is LEGITIMATE (an id in an unsealed tail can also arrive in the next batch, and
	// the drain seals both in one Build), so the seal path normalises its batch BEFORE
	// building rather than being rejected here.
	//
	// Import does NOT pass through this function (decoded segments take
	// entryFromDecoded), so a blob shipped before this check existed still loads.
	if len(ids) != len(members) {
		return nil, fmt.Errorf(
			"searchengine: segment holds %d nodes for %d distinct ids — a built index must carry exactly one node per id; "+
				"a merge whose constituents share ids must deduplicate at item collection",
			len(ids), len(members))
	}

	var live *liveDocs
	if len(tombstones) == 0 {
		live = newLiveDocs(len(ids))
	} else {
		live = newLiveDocsFromTombstones(len(ids), tombstones, members)
	}

	// DocCount COUNTS DISTINCT MEMBERS, not ordinals. A segment can carry the same
	// id more than once — a merge of constituents that each hold it produces one
	// item per copy — and counting those separately makes the corpus read larger
	// than it is. Every consumer of this number treats it as a corpus size: the
	// publish gate's coverage ratio, the degeneracy probe, the partition-count
	// derivation and the merge trigger's dead ratio. members is the last-wins map,
	// so its length IS the distinct count.
	//
	// EXPECT THIS NUMBER TO DROP on a corpus that accumulated duplicate ids. That
	// is the count becoming correct, not documents disappearing.
	return &segmentEntry[Q, S]{
		payload: seg,
		live:    live,
		members: members,
		meta: SegmentMeta{
			ID:        id,
			Format:    e.format.Name(),
			DocCount:  len(members),
			DeadCount: distinctDeadCount(members, live),
		},
	}, nil
}

// publishAppend CAS-publishes a new snapshot with entry appended, retrying on a
// lost CAS. The body is a slice+map copy (O(new segment)); no heavy work here.
//
// IDEMPOTENT BY SEGMENT ID, matching publishImport (distribution.go:116-143): an
// entry whose content-hash meta.ID is already resident is DROPPED rather than
// appended a second time. The two publish paths now agree, which they did not
// before — Import deduped and seal did not.
//
// WHY A SEAL CAN LEGITIMATELY REPRODUCE A RESIDENT ID: a segment id is the hash of
// its bytes and the builders are byte-reproducible, so re-emitting an unchanged
// group mints exactly the id the engine already holds. Two rebuilds of one corpus
// in a single process is the ordinary way to reach this, and before the skip it
// left the set carrying two entries per id: Export returned 2N blobs over N
// distinct ids, and ResidentDocCount — which SUMS per-segment DocCount — read
// double the corpus while DistinctResidentDocCount read it correctly. That
// inflated number is the publish gate's coverage numerator and the operator status
// column's resident reading, so the duplication was not merely redundant storage.
//
// THE RESIDENT CHECK IS RE-DERIVED EACH ITERATION, for publishImport's reason: a
// check computed once outside the loop goes stale when another writer publishes
// between two attempts, and the retry would then append a copy of an id that
// became resident in the meantime.
//
// It REPORTS which branch it took: true when the append landed, false when the id
// was already resident and the append was dropped. The branch already existed; only
// the answer is new, and a caller that later retires the segments a write produced
// needs it to avoid dropping a segment it merely named.
func (e *SegmentedIndex[Q, S]) publishAppend(entry *segmentEntry[Q, S]) bool {
	for {
		old := e.set.Load()
		if old.entryByID(entry.meta.ID) != nil {
			return false // already resident — idempotent, do not double-add.
		}
		next := old.withAppended(e.format, entry)
		if e.set.CompareAndSwap(old, next) {
			return true
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
// It is O(1) rather than a walk. The route map already indexes every resident id
// to the segment answering for it, one entry per DISTINCT id by construction, so
// its length IS the distinct corpus size. No pass over the corpus is added to any
// path, which matters because a derivation that cost O(corpus) would tempt callers
// back onto the cheap wrong number.
func (e *SegmentedIndex[Q, S]) DistinctResidentDocCount() int {
	return len(e.set.Load().route)
}

// residentMemberIn is the ONE searchability predicate. Every membership answer in
// this file derives from it, so a count and a diff can never disagree about what
// "covered" means. It mirrors killSuperseded's route-then-kill walk and Search's
// accept closure: a deleted id keeps its route and members entries and only loses
// its live bit, so route presence alone is NOT membership.
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
	for id, sid := range set.route {
		if residentMemberIn(idx[sid], id) {
			n++
		}
	}
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
		sid, routed := set.route[id]
		if !routed || !residentMemberIn(idx[sid], id) {
			missing = append(missing, id)
		}
	}
	return missing
}

// contentHash returns the sha256 hex digest of a segment blob — the SegmentID.
func contentHash(blob []byte) SegmentID {
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:])
}

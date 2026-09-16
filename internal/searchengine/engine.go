package searchengine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

	// publishMu serializes APPEND PUBLISHERS against each other. Readers never
	// touch it — e.set.Load() is unchanged — so the lock-free read path is exactly
	// as it was; this is the writer-serialized variant of the same pattern.
	//
	// IT IS HERE BECAUSE A LOST CAS REPEATS THE BUILD. publishAppend loads,
	// derives the next snapshot and swaps; a publisher that loses the swap starts
	// over, and with twenty concurrent embed workers (the shipped default) that
	// re-derivation was most of the CPU the reported host burned — the snapshot
	// build re-copied the route and re-folded the corpus stats every time. Holding
	// this across the build-and-swap makes the losers wait instead of work. The
	// CAS loop stays, because a GROUP swap publishes without this lock.
	//
	// IT IS A sync.Mutex, NEVER AN RWMutex. Readers do not participate, so a
	// reader-writer lock would add RLock cost to nothing and contend with the
	// writer for no gain.
	publishMu sync.Mutex

	// activeMu guards ONLY the active coalescing buffer (the write side). The
	// read path never touches it.
	activeMu sync.Mutex
	active   []Document

	// scratchMu guards scratchLive AND serializes the stale-scratch sweep against
	// scratch-file creation. Holding one lock across both is what makes the sweep
	// safe: without it a sweep could run between a sibling merge's CreateTemp and
	// its registration, and delete a file that merge is about to write into.
	scratchMu sync.Mutex
	// scratchLive is the set of scratch file NAMES this engine's in-flight merges
	// own. The sweep removes everything in the scratch directory that is not in
	// here — several merges of one engine share that directory (harvestGroup runs
	// min(NumCPU, partitions) of them at once), so "not mine" is the only safe
	// definition of stale.
	scratchLive map[string]bool

	// merge background machinery (startMerger/Close/Metrics live in merge.go).
	stopOnce sync.Once
	stop     chan struct{}
	// done is closed by the background merge goroutine as it exits, and it is what
	// makes Close a JOIN rather than a signal. Without it Close returned while a
	// merge was still in flight, and that merge's completion path fires OnMerge —
	// in production a cache.Put that writes a file — so an owner could have a blob
	// written into a directory it had already started tearing down.
	done        chan struct{}
	mergeSignal chan struct{}
	mergeCnt    atomic.Uint64
	// settleCnt counts merges whose doMerge has RETURNED, which is the publish plus
	// everything doMerge still owed afterwards — including the OnMerge hook on the
	// arms that fire it. mergeCnt moves at the CAS publish and settleCnt moves when
	// the work that publish set in motion is done, so the difference between them is
	// exactly "merges published whose completion work is still running". Pure
	// observability: nothing in the merge path reads it. See doMerge.
	settleCnt atomic.Uint64
	// mergeScanCnt counts the resident entries pickMergeTargets' dead-ratio loop has
	// WALKED. It is the observable the merger-tick bound is asserted on, because a
	// tick that selects nothing leaves no other trace: mergeCnt and settleCnt both
	// stay at zero whether the tick walked the whole resident set or returned at its
	// first line. Pure observability; nothing in the merge path reads it.
	mergeScanCnt atomic.Int64
}

// New constructs an engine over the given format and options. It seeds an empty
// published set so Search never has to nil-check, and applies option defaults.
func New[Q, S any](f SegmentFormat[Q, S], opts Options) *SegmentedIndex[Q, S] {
	e := &SegmentedIndex[Q, S]{
		format:      f,
		opts:        opts.withDefaults(),
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
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
	seg, rep, err := e.format.Build(dedupeDocsByID(docs))
	e.reportDegrade(rep)
	if err != nil {
		return "", false, err
	}
	// The seal's payload is the encoder's output and stays on the heap until the
	// distribution layer makes it durable and remaps it over the stored file.
	entry, err := e.newEntry(seg, nil, payloadBuilt)
	if err != nil {
		return "", false, err
	}
	created := e.publishAppend(entry)
	e.signalMerge()
	return entry.meta.ID, created, nil
}

// reportDegrade hands a build's census to the owner. BOTH GUARD CLAUSES ARE
// LOAD-BEARING: the emptiness check is what keeps the hook a signal rather than
// a per-build heartbeat, and the nil check is what makes an owner that wired
// nothing pay nothing. See Options.OnBuildDegrade.
func (e *SegmentedIndex[Q, S]) reportDegrade(rep BuildReport) {
	if len(rep.Degraded) == 0 || e.opts.OnBuildDegrade == nil {
		return
	}
	e.opts.OnBuildDegrade(rep)
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

// payloadProvenance says where a new entry's payload bytes LIVE. It is the one
// fact about a payload the payload itself cannot report, and the reason is the
// same on every format: a decoded segment is constructed from an ordinary []byte
// whether that slice is the encoder's output or a view over a mapping.
//
// IT IS A REQUIRED PARAMETER OF newEntry RATHER THAN A FIELD A SITE MAY SET. Every
// publisher already knows which it holds — the four Build sites hand in their
// encoder's output, mergeEntry hands in a payload decoded over the mapping it just
// made — and a parameter makes a site that forgets a compile error instead of a
// segment whose heap the residency budget cannot see.
type payloadProvenance int

const (
	// payloadBuilt is the encoder's output: a heap slice the payload retains for
	// its whole life, because the mapped formats read their postings, dictionaries
	// and member offsets in place rather than copying them out.
	payloadBuilt payloadProvenance = iota
	// payloadMapped is a view over a memory mapping: page cache, evictable, shared
	// between processes and invisible to the garbage collector. It is the state a
	// built payload is meant to REACH — see RemapResident — not only the state a
	// loaded one starts in.
	payloadMapped
)

// newEntry wraps a sealed segment into a segmentEntry: content-hash SegmentID,
// all-live (or tombstone-seeded) liveDocs, and the members route map. tombstones
// is nil for locally-built segments and set at Import.
//
// prov says whether the payload's bytes are heap or page cache; see
// payloadProvenance and segmentEntry.heapPayload.
func (e *SegmentedIndex[Q, S]) newEntry(
	seg Segment[Q, S], tombstones []ExternalID, prov payloadProvenance,
) (*segmentEntry[Q, S], error) {
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
	// THE BLOB THIS FUNCTION ALREADY HOLDS IS THE MEASUREMENT. seg.Encode returned
	// the payload's own bytes — on every mapped format that is an identity rather
	// than a re-serialization — so on a BUILT payload len(blob) is exactly the heap
	// the entry will retain, taken from the object itself rather than modeled.
	var heapPayload int64
	if prov == payloadBuilt {
		heapPayload = int64(len(blob))
	}

	return &segmentEntry[Q, S]{
		payload:     seg,
		live:        live,
		members:     members,
		heapPayload: heapPayload,
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
	// SERIALIZED, NOT LOCK-FREE, on the writer side only. See publishMu: concurrent
	// appenders that raced here re-derived the whole snapshot on every lost swap,
	// and the work a loser repeated was the work this fix exists to remove. The loop
	// below is still a CAS retry because a group swap (ReplaceBucketGroup) publishes
	// without this lock, so an append can still lose a race — it just no longer
	// loses one to another append.
	e.publishMu.Lock()
	defer e.publishMu.Unlock()
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
	if entry := set.entryOf(id); entry != nil {
		if ord, ok := entry.members[id]; ok {
			entry.live.Kill(ord)
			e.signalMerge()
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

// contentHash returns the sha256 hex digest of a segment blob — the SegmentID.
func contentHash(blob []byte) SegmentID {
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:])
}

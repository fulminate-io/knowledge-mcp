// SPDX-License-Identifier: Apache-2.0

// Package segmentdist is the CLIENT-side segment distribution layer: a
// content-addressed on-disk L2 disk cache, and a load/unload manager that ties the
// searchengine engine to it. The cache is the whole of segment storage on this side
// and the manager reads it DIRECTLY — there is no source abstraction between the two,
// because there is only one place segments can come from. It is a CONSUMER of
// cmd/knowledge/internal/searchengine — deliberately a SIBLING package, NOT inside
// the engine subpackage, so the engine stays import-clean (stdlib + own subpkgs)
// for a future service extraction (locked contract).
package segmentdist

import (
	"sync"
	"sync/atomic"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// segmentL2Cache is the L2 disk-cache seam distManager writes through, and it is
// now the ONLY cache contract in the tree. The concrete *diskSegmentCache satisfies
// it; tests substitute instrumented or fault-injecting implementations to exercise
// the prune-safety ordering.
//
// A NARROWER searchengine.SegmentCache USED TO SIT ABOVE IT, declared in the engine
// package and carrying Get/GetMapped/Put. It was never reused here — the reclaim,
// prune and eviction paths need Remove, Keys and sizeOf — and it turned out to be
// consumed by nothing anywhere: one declaration and one compile-time assertion
// against this same concrete type. Two seams over one implementation, where the
// narrower one had no client, is a boundary that documents a layering that does not
// exist, so it is deleted rather than kept for symmetry.
type segmentL2Cache interface {
	Get(id searchengine.SegmentID) ([]byte, bool)
	// GetMapped is the resident read path's variant: it returns the blob as a
	// memory mapping plus its release closure, so the bytes live in the OS page
	// cache instead of the Go heap. A non-nil error means the id IS cached but
	// could not be mapped, which callers surface rather than treat as a miss.
	GetMapped(id searchengine.SegmentID) (data []byte, release func(), ok bool, err error)
	// Put persists the blob. It RETURNS its write errors: this cache is the only
	// segment store, so a discarded Put error is a segment the engine believes is
	// resident and that no later process can load. Every caller must abort on it.
	Put(id searchengine.SegmentID, parts ...[]byte) error
	Remove(id searchengine.SegmentID)
	// Keys enumerates the L2-resident segment ids so load() can reconstruct the
	// resident set. It is the ONLY manifest there is — there is no second authority to
	// reconcile it against. It reads only the in-memory index: no disk re-read, no
	// network.
	Keys() []searchengine.SegmentID
	// sizeOf reports one id's stored byte size and whether it is L2-resident at
	// all. It is the eviction re-materializability gate's probe (evictResident,
	// manager_residency.go): Get would read the whole file back off disk and
	// MoveToFront the LRU, so gating on Get would turn a memory reclaim into a
	// disk-read storm AND perturb the very recency ordering the budget sorts on.
	// sizeOf reads the in-memory index only and is recency-neutral.
	sizeOf(id searchengine.SegmentID) (int64, bool)
}

// distManager ties one graph's searchengine.SegmentedIndex to its L2 DISK CACHE:
// it LAZILY LOADS the cached set into the engine and UNLOADS / RELOADS
// resident segments to bound memory.
//
// THERE IS NO SEGMENT SOURCE, and its absence is the end state this ticket was
// named for rather than an omission. A source seam existed to abstract "where
// segments come from" while there were two answers — a cloud registry and the local
// cache. There is one answer, the load path reads the cache directly, and a seam
// with a single implementation that nothing consults is a lie about where the data
// comes from. It is generic over the engine's [Q, S]
// format parameters so it works against ANY SegmentFormat (the mock format in
// tests; the real HNSW/BM25 formats once the migration wires the engine into
// client search).
type distManager[Q, S any] struct {
	engine *searchengine.SegmentedIndex[Q, S]
	cache  segmentL2Cache
	target *knowledgev1.GraphSelector

	// format is this engine's segment format name, as the format itself reports it.
	// Each cache is rooted at one (graph, format) pair (graphCacheDirFor), so a read
	// returns only THIS engine's format and there is no cross-format blob to filter
	// out. The format is therefore a TAG rather than a predicate anything screens
	// against. An empty format is
	// the mock-format engine in tests.
	format string

	// tombstoneSeed supplies the ids every Import must mark dead in the segments it
	// imports, so a blob shipped before a delete cannot resurrect the removed node.
	// It is a SUPPLIER rather than a stored slice because the owner's set changes as
	// deletes are learned and re-emitted, and an engine holding its own copy would
	// drift. nil-safe: a nil hook means no seeding, which is the correct behavior
	// for the test engines that construct a distManager directly.
	tombstoneSeed func() []searchengine.ExternalID

	// resident tracks the segments currently imported into the engine and their
	// stored sizes — envelope plus payload, the whole file as it exists on disk.
	// Guarded by resMu. A segment dropped under pressure is re-Imported from L2 on
	// the next load; there is no second map holding its bytes.
	resMu    sync.Mutex
	resident map[searchengine.SegmentID]residentSeg

	// evictedIDs is the EXACT id set the last evictResident unloaded, recorded so
	// the re-materialization is a STRICT reload of that set (manager_residency.go,
	// load()'s evicted branch) rather than a tolerant re-derive from cache.Keys(),
	// which would silently serve a SHORT hit list. Guarded by resMu, beside
	// resident.
	//
	// It has EXACTLY TWO writers, with different jobs, and neither does the
	// other's: markMaterialized DROPS it (the pool is resident again, latch
	// cleared), and reclaimMerged REWRITES it while the latch is still SET (drop
	// res.Removed, add res.Merged.ID) so a merge completing after eviction cannot
	// make the strict reload hard-error on data that is perfectly intact. A third
	// writer is a defect.
	evictedIDs []searchengine.SegmentID

	// remapPending holds the segments whose mapping republication has not yet
	// succeeded, keyed by segment id and guarded by resMu — the SAME lock that
	// already guards resident and evictedIDs, because this set has the same
	// lifecycle as those and a separate lock would create an ordering question
	// resMu's existing discipline (residencyMu before resMu, per evictResident)
	// already answers.
	//
	// A pending entry is a DEGRADED-BUT-CORRECT state, not a lost one: the
	// previous heap-backed payload is still published and still serves the same
	// results. Only the memory property is missing, which is precisely why the
	// condition used to be logged and forgotten — the damage is invisible at
	// every gate. Recording it is what makes the repair convergent instead.
	remapPending map[searchengine.SegmentID]remapAttempt

	// reclaimPending holds the merge supersession obligations an aborted reclaim
	// could not discharge — the consolidated blob that must land and the constituent
	// ids it supersedes — keyed by the merged blob's id and guarded by resMu, for the
	// reason remapPending records. drainReclaimPending discharges them on a later
	// consumer touch (manager_reclaim_discharge.go), which is what makes the abort
	// RECOVERABLE rather than permanent.
	//
	// IT IS A DIFFERENT RECORD FROM lastReclaimAbort BELOW AND NEITHER DOES THE
	// OTHER'S JOB. This one is what CONVERGES the state and is dropped the moment it
	// is discharged; that one is a report a re-emit's caller reads to learn the state
	// existed during ITS OWN call, and it is never cleared.
	reclaimPending map[searchengine.SegmentID]reclaimAttempt

	// lastReclaimAbort is the most recent merge reclaim this pool ABORTED because the
	// consolidated blob could not be persisted, guarded by resMu beside the fields
	// above for the reason remapPending records. It is the REPORTING channel that
	// abort has out of reclaimMerged: that handler is installed as the engine's
	// Options.OnMerge, whose signature returns nothing.
	//
	// ONE RECORD, NOT A LIST, and a sequence number rather than a bare flag — see
	// reclaimAbortRecord (manager_reclaim.go) for why both halves are load-bearing.
	lastReclaimAbort reclaimAbortRecord

	// lastSearchNanos is the last CONSUMER-SEARCH touch stamp (time.Now().UnixNano),
	// written by noteSearchTouch and read by lastSearchTouch. It defines hot/cold for
	// the residency budget, and it is stamped by the SEARCH path only: the reconcile,
	// coverage-probe and rebuild arms run against the whole working set hourly, so
	// counting them as touches would keep every pool permanently hot and defeat
	// eviction entirely. A never-searched pool reads 0 and is therefore the coldest.
	lastSearchNanos atomic.Int64

	// evicted latches true while this pool's segments have been unloaded to reclaim
	// memory and have not yet been re-materialized. It is what makes an evicted pool
	// DISTINGUISHABLE to a background arm (which must decline rather than resurrect
	// it) while staying INDISTINGUISHABLE to a searcher (whose load() transparently
	// re-materializes it). markMaterialized is the SINGLE owner of its clear.
	// Modeled on l2Loaded: a lock-free atomic.Bool.
	evicted atomic.Bool

	// residencyMu serializes eviction against the consumer load-and-search span.
	// engine.Unload's CAS swap is tear-safe on its own (searchengine/segmentset.go
	// declares segmentSet an immutable snapshot, and Search does one set.Load for
	// the whole call), but a CONSUMER calls load() and engine.Search as two separate
	// statements — an eviction landing between them leaves Search reading an empty
	// snapshot, which is a silent miss no property of the CAS prevents. Consumers
	// hold RLock across the whole load+Search span; evictResident holds Lock for its
	// whole body. sync.RWMutex is NOT reentrant, so nothing called while an RLock is
	// held may take Lock — see markMaterialized.
	residencyMu sync.RWMutex

	// l2Loaded is the L2-first once-guard. load() is L2-PRIMARY: the FIRST act is a
	// server-independent import of the L2-resident set (cache.Keys() -> reload()),
	// not a server List. Once that primary import (or the cold-cache List+Fetch
	// fallthrough) has run, l2Loaded is set true and a repeated load() short-circuits
	// to a bare return nil — matching the "Load is idempotent" contract that
	// manager_search.go relies on. A one-shot atomic.Bool, no lock.
	l2Loaded atomic.Bool
}

// residentSeg records one imported segment's size + format + generation so unload
// accounting and reload re-import are exact.
type residentSeg struct {
	// mappedBytes is the ENCODED blob length of this segment. It is metered and
	// reported, never compared against the residency budget: for a mapped
	// segment these bytes are page cache rather than Go heap, so the budget
	// reads the engine's modeled heap instead (see residentBytes).
	mappedBytes int
	format      string
	generation  uint64
}

// newDistManager wires a manager for one graph. format is the engine's segment
// format name, the tag its source stamps on the metas it returns; pass "" for the
// test mock format.
func newDistManager[Q, S any](
	engine *searchengine.SegmentedIndex[Q, S],
	cache segmentL2Cache,
	target *knowledgev1.GraphSelector,
	format string,
) *distManager[Q, S] {
	return &distManager[Q, S]{
		engine:       engine,
		cache:        cache,
		target:       target,
		format:       format,
		resident:     make(map[searchengine.SegmentID]residentSeg),
		remapPending: make(map[searchengine.SegmentID]remapAttempt),
		// Constructed here rather than lazily in the retain path: a nil map is a
		// panic on write and the retain path runs on the merge goroutine, where a
		// panic takes the engine's merger down with it.
		reclaimPending: make(map[searchengine.SegmentID]reclaimAttempt),
	}
}

// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// constructionGate is one graph+format's construction record: the engine, plus a
// channel closed once the seed that fills it has finished.
//
// IT EXISTS BECAUSE PUBLISHING AND BEING READY ARE DIFFERENT MOMENTS. A
// constructor must store its engine into the memo BEFORE running the seed, or
// every other graph's construction blocks behind this one's corpus copy; but a
// caller that finds that entry and searches drives load(), which latches its
// resident set ONCE and would latch on a partially copied engine. The branch then
// serves a partial corpus for the life of the process, with no error anywhere —
// and nothing can detect it after the fact, because a latch on an incomplete set
// looks exactly like a latch on a complete one.
//
// The gate makes the entry PUBLISHED-BUT-NOT-YET-READY expressible: waiters take
// the same instance every other caller gets, and what they wait for is the seed.
// A completed entry's channel is already closed, so the wait on it is immediate
// and the steady state is the same map hit it always was — which is why one
// branch serves both the completed and the in-flight case.
type constructionGate[Q any, S any] struct {
	dm   *distManager[Q, S]
	done chan struct{}
}

// managerFor lazily constructs (check-construct-store under the mutex) the
// per-graph distManager: a SegmentedIndex over the HNSW format and a
// content-addressed L2 cache rooted per-graph so distinct graphs never collide on
// the content-hash filename space.
// hnsw.New() is the deterministic builder (the only HNSW builder), so the live ship
// path is byte-reproducible — two writers building the same nodes mint the same
// content-hash blob, the content-addressed store dedups to one copy at refcount-N, and
// exact-match recall is recovered.
//
// IT IS THE ONE HNSW FACTORY. It used to be a shared body behind this wrapper, serving a
// second DETERMINISTIC rebuild engine keyed in its own per-graph map, and taking the
// destination map, the HNSW Format and an auto-reclaim flag as parameters so one body
// could build either. The rebuild finalizes at THIS engine now, so all three parameters
// had exactly one possible value — a parameter with one possible value advertises a
// flexibility that does not exist — and the second map is gone with the topology that
// needed it.
//
// The engine is built with the background merge triggers disarmed, because this
// package manages the segment layout itself and an automatic consolidation would
// merge across the boundaries it maintains. format.Merge is untouched — only the
// automatic trigger is off — and OnMerge stays wired, so a merge this package
// drives still reclaims the superseded constituents from the L2 cache.
//
// THE BRANCH SEED RUNS AFTER THE LOCK IS RELEASED, and that placement is chosen
// rather than incidental. Construction holds m.mu, which serializes EVERY graph's
// construction; holding it across a bounded-parallel file copy would put every
// other graph's first search behind this one's corpus copy. So the lock covers the map
// check-and-store only, and the seed runs on the released path just before the
// return. It is reached only for a branch-qualified name, and only once per
// process per graph because it sits after the memo check.
func (m *Manager) managerFor(gt kgtypes.GraphType, name string) *distManager[[]byte, struct{}] {
	hnswFormat := hnsw.New()
	k := graphKey{graphType: gt, graphName: name}
	m.mu.Lock()
	if gate, ok := m.managers[k]; ok {
		// RELEASE THE LOCK BEFORE WAITING. Waiting under m.mu would put every other
		// graph's construction behind this one's corpus copy — the exact coupling the
		// off-the-lock seed placement exists to avoid. A completed gate is already
		// closed, so this is an immediate return for the ordinary memo hit.
		m.mu.Unlock()
		<-gate.done
		return gate.dm
	}

	target := graphSelector(gt, name)
	// THE CACHE ROOT IS HOISTED so the L2 destination and the merge scratch are
	// provably the same filesystem rather than two derivations that happen to
	// agree. A merge writes its output into the scratch directory and the result is
	// copied into this root; a rename across filesystems would fail and a copy
	// across them would defeat the point.
	cacheRoot := graphCacheDirFor(m.cacheDir, gt, name, hnswFormat.Name())
	cache := newDiskSegmentCache(cacheRoot, m.maxBytes, hnswReadAdvice)
	scratch := mergeScratchDir(cacheRoot)
	// One-time reclamation of the tree the version-carrying format name retired,
	// mirroring bm25ManagerFor. It runs AFTER the cache is constructed and BEFORE
	// the engine: the docker segment-integrity arm relies on that ordering. The
	// marker is HNSW's own, so this reclaim and BM25's cannot suppress each other.
	removeRetiredTree(m.cacheDir, retiredHNSWTree, hnswFormat.Name(), retiredHNSWMarker)

	// var-before-assign: the OnMerge closure back-references the distManager that is
	// constructed AFTER the engine. Safe because OnMerge cannot fire before the
	// engine's first merge, which cannot happen before this function returns (the
	// engine holds no documents at construction and the first merge tick is 50ms
	// out against an empty set).
	var dm *distManager[[]byte, struct{}]
	// Background merge triggers off: this owner manages the segment layout itself,
	// so an automatic consolidation would merge across the boundaries it maintains.
	// Both HNSW engines route through here, so this one literal covers them.
	opts := searchengine.Options{
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
		ScratchDir:         scratch,
		// THE ADVICE IS THIS FORMAT'S OWN. The two constants exist because the two
		// formats' read patterns differ, and handing a merge output the wrong one
		// quietly changes the physical footprint of every merged segment.
		MapBlob: newMapBlobHook(hnswReadAdvice),
	}
	opts.OnMerge = func(res searchengine.MergeResult) { dm.reclaimMerged(res) }
	engine := searchengine.New[[]byte, struct{}](hnswFormat, opts)
	dm = newDistManager(engine, cache, target, hnswFormat.Name())
	// Seed every import from the owner's live tombstone set, read at import time
	// rather than captured, so ids learned after this engine was built still apply.
	dm.tombstoneSeed = func() []searchengine.ExternalID { return m.graphTombstones(gt, name) }
	gate := &constructionGate[[]byte, struct{}]{dm: dm, done: make(chan struct{})}
	m.managers[k] = gate
	m.mu.Unlock()
	// THE CLOSE IS DEFERRED, AND THAT IS THE WHOLE SAFETY ARGUMENT. A gate left
	// open by a seed that errors or panics strands every waiter forever, which
	// turns a partial corpus into a hung daemon — strictly worse than the defect
	// this gate fixes. A failed seed must still publish a usable manager, which is
	// what seedBranchAtConstruction is already built for: it logs at Error and
	// returns, and the branch rebuilds from its own embedded nodes instead.
	defer close(gate.done)

	// SeedBranchBucketFromBase, off the lock: a new branch starts from base's live
	// partitions instead of re-deriving its whole corpus from scratch. THIS
	// engine's own cache is the copy destination, so what the seed writes is what
	// this engine reads.
	m.seedBranchAtConstruction(gt, name, hnswFormat.Name(), cache)
	return dm
}

// bm25ManagerFor lazily constructs (check-construct-store under the mutex) the
// per-graph BM25 distManager: a SegmentedIndex over the BM25 format and a
// content-addressed L2 cache rooted under a BM25-distinct directory so HNSW and
// BM25 blobs never collide on the content-hash filename space. Mirrors
// managerFor; the only differences are the format type parameters and the format
// tag on the cache dir.
//
// It seeds its OWN format's bucket after the lock, on the same reasoning
// managerFor records: the two formats carry separate manifests over the same
// nodes, so a seed wired into only one leaves the other rebuilding from scratch
// and a completeness gate reading one format full and the other empty.
func (m *Manager) bm25ManagerFor(gt kgtypes.GraphType, name string) *distManager[bm25.Query, *bm25.CorpusStats] {
	k := graphKey{graphType: gt, graphName: name}
	m.mu.Lock()
	if gate, ok := m.bm25Managers[k]; ok {
		// Same release-then-wait as managerFor, and for the same reason: a wait held
		// under m.mu would serialize every graph's construction behind this seed. The
		// two maps are independent, so a branch has a gate in each and each closes
		// when ITS OWN format's seed finishes.
		m.mu.Unlock()
		<-gate.done
		return gate.dm
	}

	target := graphSelector(gt, name)
	// Hoisted for the reason managerFor records: the scratch directory and the L2
	// destination must share a filesystem, and deriving the root twice is how they
	// stop doing so.
	cacheRoot := graphCacheDirFor(m.cacheDir, gt, name, bm25.New().Name())
	cache := newDiskSegmentCache(cacheRoot, m.maxBytes, bm25ReadAdvice)
	scratch := mergeScratchDir(cacheRoot)
	// One-time reclamation of the tree the version-carrying format name retired.
	// Guarded by a marker so it runs once and never re-scans, and it declines
	// until the replacement directory exists. The marker is BM25's own, so this
	// reclaim and HNSW's cannot suppress each other.
	removeRetiredTree(m.cacheDir, retiredBM25Tree, bm25.New().Name(), retiredTreeMarker)

	// var-before-assign OnMerge: the BM25 engine is embed-only (no deterministic
	// variant), so it always auto-reclaims superseded constituents from its live L2
	// cache on a completed merge. Same back-reference-after-construction safety as
	// hnswManagerFor (OnMerge cannot fire before this returns). The background merge
	// triggers are disarmed here for the same reason as the HNSW engines.
	var dm *distManager[bm25.Query, *bm25.CorpusStats]
	engine := searchengine.New[bm25.Query, *bm25.CorpusStats](bm25.New(), searchengine.Options{
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
		OnMerge:            func(res searchengine.MergeResult) { dm.reclaimMerged(res) },
		ScratchDir:         scratch,
		// This format's own advice, for the reason managerFor records.
		MapBlob: newMapBlobHook(bm25ReadAdvice),
	})
	dm = newDistManager(engine, cache, target, bm25.New().Name())
	// Same import-time tombstone seed as the HNSW engines: the field corpus indexes
	// the same nodes and carries its own manifest, so a pre-delete BM25 blob
	// resurrects a removed node exactly as a vector blob would.
	dm.tombstoneSeed = func() []searchengine.ExternalID { return m.graphTombstones(gt, name) }
	gate := &constructionGate[bm25.Query, *bm25.CorpusStats]{dm: dm, done: make(chan struct{})}
	m.bm25Managers[k] = gate
	m.mu.Unlock()
	// Deferred for the reason managerFor states: a seed that errors or panics must
	// still release its waiters, or a partial corpus becomes a hung daemon.
	defer close(gate.done)

	// SeedBranchBucketFromBase, off the lock, for THIS format's bucket — and with
	// THIS format's own cache. The L2 cache is rooted per-format precisely so HNSW
	// and BM25 blobs cannot collide on the content-hash filename space, so passing
	// the other format's instance would point the copy at the wrong directory.
	m.seedBranchAtConstruction(gt, name, bm25.New().Name(), cache)
	return dm
}

// Close releases the background worker every per-graph engine this Manager owns.
//
// IT IS THE OTHER HALF OF THE TWO CONSTRUCTORS ABOVE. Each of them calls
// searchengine.New, and New starts a merger goroutine that no other event ever
// stops: the engines are memoized per (graph type, graph name) and this package
// removes an entry from neither map, so without this method every engine the
// process ever constructed keeps a mergeTickInterval ticker alive until the
// process dies. Eviction is deliberately NOT that half — evictResident unloads a
// pool's segments but keeps the engine, because an evicted pool re-materializes
// on its next consumer touch and Close is one-way (stopOnce), so closing there
// would leave the revived pool with no merger.
//
// It is idempotent and it does NOT wait on the construction gates. A gate still
// running its seed has an engine already stored, so closing that engine is the
// same operation as closing a settled one; waiting instead would put shutdown
// behind a corpus copy, which is the coupling the off-the-lock seed placement
// exists to avoid. A closed Manager is not meant to be reused — the maps are
// left as they are so a post-Close read still reports what this process built.
func (m *Manager) Close() {
	m.mu.Lock()
	hnswEngines := make([]*searchengine.SegmentedIndex[[]byte, struct{}], 0, len(m.managers))
	for _, gate := range m.managers {
		hnswEngines = append(hnswEngines, gate.dm.engine)
	}
	bm25Engines := make([]*searchengine.SegmentedIndex[bm25.Query, *bm25.CorpusStats], 0, len(m.bm25Managers))
	for _, gate := range m.bm25Managers {
		bm25Engines = append(bm25Engines, gate.dm.engine)
	}
	m.mu.Unlock()

	// Off the lock: Close takes no lock of this package's, but holding m.mu across
	// it would serialize shutdown against any construction still in flight.
	for _, e := range hnswEngines {
		e.Close()
	}
	for _, e := range bm25Engines {
		e.Close()
	}
	slog.Info("segmentdist: closed the per-graph segment engines",
		"hnsw_engines", len(hnswEngines), "bm25_engines", len(bm25Engines))
}

// baseNameOfBranch splits a branch-qualified graph name into the base name it
// derives from. It returns ok=false for a name that is not branch-qualified.
//
// The base name is DERIVED from the branch name rather than threaded in as a
// second parameter, because the constructors are keyed by one graph name and a
// second parameter would let a caller pair a branch with the wrong base.
func baseNameOfBranch(name string) (string, bool) {
	base, _, ok := strings.Cut(name, "@")
	if !ok || base == "" {
		return "", false
	}
	return base, true
}

// seedBranchAtConstruction runs the branch seed for one freshly-constructed
// per-graph engine. Both constructors call it with their own format.
//
// A FAILED SEED IS LOGGED, NOT FATAL, and the reason is what the seed IS: a cost
// optimization over a correctness path that stays intact. When it does not run,
// the rebuild axis refills the branch from the server exactly as it does today —
// the branch is correct, just more expensive to populate. Failing engine
// construction instead would take search down for that graph over a file copy.
// It is logged at ERROR rather than WARN because the accepted cost of this
// ticket was bought with this copy, and a copy that silently stopped happening
// would look like the feature working.
// THE CACHE IS THE CALLER'S, and each constructor passes its OWN format-scoped
// instance. It is the copy destination and the ship's read source, which is what
// makes a seed visible to the engine that triggered it rather than only after a
// restart: a diskSegmentCache indexes its directory once at construction, so a
// copy landing in any other instance leaves this engine's view empty.
func (m *Manager) seedBranchAtConstruction(gt kgtypes.GraphType, name, format string, cache *diskSegmentCache) {
	// TEST-ONLY hook. nil in production; see withSeedHook for why the seed is the only
	// place in the constructor where the construction gate itself is observable.
	if m.seedHook != nil {
		m.seedHook(seedPhaseConstruct, gt, name, format)
	}
	base, ok := baseNameOfBranch(name)
	if !ok {
		return
	}
	ctx := context.Background()
	// THE COPY ALONE IS THE WHOLE SEED. A second half used to follow it, shipping the
	// copied partitions under the branch's own object key so they could be published;
	// with no object store and no manifest there is nothing to do after the bytes land
	// in the branch's L2 bucket.
	if _, err := m.SeedBranchBucketFromBase(ctx, gt, base, name, format, cache); err != nil {
		slog.Error("segmentdist: branch segment seed failed — this branch will rebuild its segments from its own "+
			"embedded nodes instead of reusing base's, which is correct but costs a full rebuild",
			"graph_type", gt, "base", base, "branch", name, "format", format, "err", err)
	}
}

// graphCacheDirFor roots one graph's per-format L2 cache under
// <base>/<format>/<graphType>/<safeName> so distinct graphs AND distinct formats
// never collide on the content-hash filename space. The name is sanitized (path
// separators stripped) since it is used as a directory name.
func graphCacheDirFor(base string, gt kgtypes.GraphType, name, format string) string {
	safe := strings.NewReplacer("/", "_", "\\", "_", "..", "_").Replace(name)
	if safe == "" {
		safe = "_"
	}
	return filepath.Join(base, format, string(gt), safe)
}

// graphSelector builds the routing envelope for one graph. Mirrors the canonical
// mapping (topology/foundation/wire.go:graphTarget): the instance name lands in
// the field the server resolver keys off per graph type.
func graphSelector(gt kgtypes.GraphType, name string) *knowledgev1.GraphSelector {
	return graphsel.GraphSelectorFor(gt, name, false)
}

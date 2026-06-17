// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// Manager is the PRODUCTION owner of per-graph HNSW segment engines. It is the
// missing production constructor + per-graph routing layer over the (previously
// test-only) distManager: one searchengine.SegmentedIndex[[]byte, struct{}] over
// the HNSW format per (graphType, graphName), lazily constructed, each wired to
// the SegmentService wire via an rpcSegmentSource + an L2 diskSegmentCache.
//
// The client builds + ships HNSW segments from the binary vectors it already
// holds at the pipeline embed-writeback seam (the server is a
// dumb opaque blob store; the CLIENT builds + ships). AddAndShip is the HNSW
// entry point; AddAndShipFields is the BM25 entry point. ONE Manager owns BOTH
// formats per graph — two per-format per-graph maps, each lazily constructed and
// rooted under a format-distinct L2 cache directory so they never collide.
type Manager struct {
	caller   segmentCaller
	cacheDir string
	maxBytes int64

	mu sync.Mutex
	// managers holds the HNSW engine per graph (vectors). bm25Managers holds the
	// BM25 engine per graph (field-bearing text). Both guarded by mu.
	managers     map[graphKey]*distManager[[]byte, struct{}]
	bm25Managers map[graphKey]*distManager[bm25.Query, *bm25.CorpusStats]
	// detManagers holds the DETERMINISTIC HNSW engine per graph used ONLY by the
	// segment_rebuild path (hnsw.NewDeterministic()). Kept separate from managers
	// so the rebuild's byte-reproducible build engine never shares a coalescing
	// buffer with the embed engine for the same graph. Guarded by mu.
	detManagers map[graphKey]*distManager[[]byte, struct{}]
}

// graphKey routes one (graphType, graphName) to its dedicated engine+distManager.
type graphKey struct {
	graphType kgtypes.GraphType
	graphName string
}

// NewManager constructs the production owner. caller is the SegmentService
// surface (*graphclient.Router / *graphclient.GraphClient route
// cloud-when-logged-in / local-when-not through the same dispatch the Engine RPCs
// use). cacheDir roots the per-graph L2 disk caches; maxBytes <= 0 means an
// unbounded cache.
func NewManager(caller segmentCaller, cacheDir string, maxBytes int64) *Manager {
	return &Manager{
		caller:       caller,
		cacheDir:     cacheDir,
		maxBytes:     maxBytes,
		managers:     make(map[graphKey]*distManager[[]byte, struct{}]),
		bm25Managers: make(map[graphKey]*distManager[bm25.Query, *bm25.CorpusStats]),
		detManagers:  make(map[graphKey]*distManager[[]byte, struct{}]),
	}
}

// AddAndShip routes docs to the (graphType, graphName) engine, Adds them (the
// engine coalesces internally up to MinSegmentDocs — a sub-threshold batch just
// buffers, no graph rebuild), then ships any newly-sealed segments (the ship is a
// diff-gated no-op when nothing new sealed). Returns the first error from Add or
// ship; the pipeline caller treats any error as best-effort (WARN, never fail
// writeback) — a dropped ship self-heals on the next embed dirty-gen.
func (m *Manager) AddAndShip(ctx context.Context, gt kgtypes.GraphType, name string, docs []searchengine.Document) error {
	if len(docs) == 0 {
		return nil
	}
	dm := m.managerFor(gt, name)
	if err := dm.engine.Add(docs); err != nil {
		return err
	}
	// ROLE B (embed): reconcile-prune against locallyShipped only — restart-safe,
	// never prunes the prior corpus this process did not ship.
	_, err := dm.ship(ctx, dm.locallyShipped)
	return err
}

// AddAndShipFields is the BM25 counterpart to AddAndShip: it routes field-bearing
// Documents to the (graphType, graphName) BM25 engine, Adds them (the engine
// coalesces internally up to MinSegmentDocs — a sub-threshold batch just buffers,
// no segment seal), then ships any newly-sealed segments (diff-gated no-op when
// nothing new sealed). Returns the first error from Add or ship; the pipeline
// caller treats any error as best-effort (WARN, never fail writeback) — a dropped
// ship self-heals on the next embed dirty-gen.
func (m *Manager) AddAndShipFields(ctx context.Context, gt kgtypes.GraphType, name string, docs []searchengine.Document) error {
	if len(docs) == 0 {
		return nil
	}
	dm := m.bm25ManagerFor(gt, name)
	if err := dm.engine.Add(docs); err != nil {
		return err
	}
	// ROLE B (embed): reconcile-prune against locallyShipped only — restart-safe.
	_, err := dm.ship(ctx, dm.locallyShipped)
	return err
}

// Flush force-seals the sub-threshold coalescing tail of BOTH the HNSW and the
// BM25 engine for one (graphType, graphName), then ships the newly-sealed
// segments. It is the migration's force-seal: AddAndShip/AddAndShipFields leave
// a trailing buffer of fewer than MinSegmentDocs unsealed (and a whole graph
// with fewer than MinSegmentDocs indexed nodes produces ZERO sealed segments);
// Flush seals that tail so the graph becomes searchable. Returns the first error
// from either format's Flush or ship — the one-shot caller treats it as FATAL
// (unlike the best-effort pipeline path).
//
// Only touches engines that already exist (managerFor/bm25ManagerFor return the
// memoized instance the prior AddAndShip constructed); for a graph never added
// to, the lazily-constructed engine's buffer is empty and Flush is a no-op.
func (m *Manager) Flush(ctx context.Context, gt kgtypes.GraphType, name string) error {
	hnsw := m.managerFor(gt, name)
	if err := hnsw.engine.Flush(); err != nil {
		return err
	}
	// ROLE B (embed force-seal of the sub-threshold tail, NOT a complete corpus):
	// reconcile-prune against locallyShipped only — restart-safe.
	if _, err := hnsw.ship(ctx, hnsw.locallyShipped); err != nil {
		return err
	}
	bm := m.bm25ManagerFor(gt, name)
	if err := bm.engine.Flush(); err != nil {
		return err
	}
	_, err := bm.ship(ctx, bm.locallyShipped)
	return err
}

// AddDeterministic is the CONCURRENT-SAFE, Add-ONLY entrypoint of the
// deterministic segment_rebuild path. It routes docs to the DETERMINISTIC HNSW
// engine (hnsw.NewDeterministic() via the detManagers map) and calls ONLY
// engine.Add — it does NOT ship. The rebuild driver calls this from a NumCPU
// goroutine pool, ONE full MinSegmentDocs chunk per call: engine.Add is
// concurrent-safe (append+drain is atomic under activeMu; seal's format.Build
// runs OUTSIDE activeMu; publishAppend is a lock-free CAS loop), so exactly-
// MinSegmentDocs chunks seal with deterministic membership. The racy part —
// ship()/reconcilePrune over the shared advancing shippedIDs — is deferred to the
// single serial FlushDeterministic after the pool joins. NO ship() here.
func (m *Manager) AddDeterministic(ctx context.Context, gt kgtypes.GraphType, name string, docs []searchengine.Document) error {
	if len(docs) == 0 {
		return nil
	}
	dm := m.hnswManagerFor(m.detManagers, hnsw.NewDeterministic(), gt, name)
	return dm.engine.Add(docs)
}

// AddFields is the BM25 Add-ONLY entrypoint symmetric to AddDeterministic: it
// routes field-bearing docs to the (graphType, graphName) BM25 engine and calls
// ONLY engine.Add — no ship. The rebuild driver calls it from the same Add-only
// concurrent phase; FlushDeterministic seals + ships the BM25 tail once after the
// pool joins. (bm25.Build has no RNG; BM25 byte-determinism comes from the
// driver's id-ascending doc order.) The BM25 rebuild reuses the SAME bm25Managers
// engine as the embed path — BM25 has no determinism-variant Format, so there is
// no separate deterministic BM25 engine.
func (m *Manager) AddFields(ctx context.Context, gt kgtypes.GraphType, name string, docs []searchengine.Document) error {
	if len(docs) == 0 {
		return nil
	}
	dm := m.bm25ManagerFor(gt, name)
	return dm.engine.Add(docs)
}

// FlushDeterministic is the SINGLE serial finalizer of the deterministic rebuild
// path, called ONCE by the driver after every concurrent AddDeterministic /
// AddFields has published its segment. It seals the sub-threshold tail of the
// DETERMINISTIC HNSW engine and ships it ONCE (the only ship() on the
// deterministic path — because it runs after the pool joins, the single Export
// it diffs against is COMPLETE, so reconcilePrune can only drop genuinely
// merged-away ids, never a live sibling: this is the fix for the concurrent-ship
// data-loss race). It then seals + ships the BM25 tail once.
//
// PRUNE ROLE A (replace-prune): both ships pass shippedIDs (the server-seeded
// full set), NOT locallyShipped. The deterministic Export() IS the complete
// rebuilt corpus, so shippedIDs − Export() is exactly the old (possibly
// degenerate) corpus this rebuild supersedes — the rebuild MUST prune it to
// replace a degenerate pool. The driver error-gates the rebuild BEFORE calling
// FlushDeterministic, so a partial Export can never reach this prune. (A
// locallyShipped-only mechanic would orphan the old corpus here: a fresh
// process's locallyShipped is empty, so the rebuild would ship the new corpus
// alongside the stale old one and never prune it.)
//
// RETURNS the HNSW pruned []SegmentID (the merged-away ids reconcilePrune dropped
// server-side) so the driver feeds them to InvalidateLocal for local L2 .seg
// eviction. The BM25 ship's prune set is irrelevant to local embed-cache
// invalidation, so it is discarded.
func (m *Manager) FlushDeterministic(ctx context.Context, gt kgtypes.GraphType, name string) ([]searchengine.SegmentID, error) {
	hnswDM := m.hnswManagerFor(m.detManagers, hnsw.NewDeterministic(), gt, name)
	if err := hnswDM.engine.Flush(); err != nil {
		return nil, err
	}
	pruned, err := hnswDM.ship(ctx, hnswDM.shippedIDs)
	if err != nil {
		return nil, err
	}
	bm := m.bm25ManagerFor(gt, name)
	if err := bm.engine.Flush(); err != nil {
		return nil, err
	}
	if _, err := bm.ship(ctx, bm.shippedIDs); err != nil {
		return nil, err
	}
	return pruned, nil
}

// InvalidateLocal evicts the given superseded segment ids from the deterministic
// HNSW engine's local L2 disk cache. The driver obtains the ids from
// FlushDeterministic's return value (the server-side merged-away/pruned set) and
// passes them straight here so the local .seg files do not orphan until LRU —
// which never fires on an unbounded cache. A single explicit return path; no
// surfacing/discarding ambiguity.
func (m *Manager) InvalidateLocal(gt kgtypes.GraphType, name string, ids []searchengine.SegmentID) {
	if len(ids) == 0 {
		return
	}
	dm := m.hnswManagerFor(m.detManagers, hnsw.NewDeterministic(), gt, name)
	for _, id := range ids {
		dm.cache.Remove(id)
	}
}

// HasShippedSegments is the CHEAP zero-shipped-segments presence probe the
// auto-heal arm uses: a single ListDelta(sinceGen=0) RPC for the
// graph, returning true when the server holds at least one segment meta. It does
// NOT Fetch any blob (ListDeltaResponse carries only Metas — segment.pb.go) and
// does NOT touch the per-graph engines/maps, so it is safe to call on the embed
// drain edge without disturbing resident state. A fresh rpcSegmentSource is
// built per call (no engine, no cache) — strictly the presence list.
func (m *Manager) HasShippedSegments(ctx context.Context, gt kgtypes.GraphType, name string) (bool, error) {
	source := newRPCSegmentSource(m.caller, graphSelector(gt, name), context.Background())
	metas, err := source.List(ctx, 0)
	if err != nil {
		return false, err
	}
	return len(metas) > 0, nil
}

// ShippedSegmentDocCount is the coverage-ratio probe's data source: it sums the
// per-segment live doc count across the graph's HNSW-format shipped segments
// (covered) from a single ListDelta(sinceGen=0) — the same cheap, engine-free,
// blob-free presence list HasShippedSegments uses. It returns:
//
//   - covered: the summed HNSW meta.DocCount — "segment-covered docs". ONLY the
//     HNSW format is summed: BM25 metas index the SAME nodes, so summing both
//     would double-count; HNSW is the per-node vector coverage that mirrors the
//     graph's binary_vector_count denominator the coverage ratio compares against.
//   - anyUnknown: true when ANY summed HNSW meta has DocCount==0. A zero doc count
//     means that segment predates the doc_count wire plumbing (an old blob written
//     before the field existed), so its real coverage is UNKNOWN. The coverage
//     probe treats anyUnknown as the conservative-unknown signal and DISARMS the
//     ratio trigger (falling back to the zero-only heal) — without this guard a
//     fleet mid-migration, whose every shipped meta still reports doc_count=0,
//     would read covered=0 on every graph and trigger a fleet-wide rebuild storm.
//
// Does NOT Fetch any blob and does NOT touch the per-graph engines/maps — same
// presence-probe contract as HasShippedSegments (a fresh rpcSegmentSource per
// call, no engine, no cache).
func (m *Manager) ShippedSegmentDocCount(
	ctx context.Context, gt kgtypes.GraphType, name string,
) (covered int, anyUnknown bool, err error) {
	source := newRPCSegmentSource(m.caller, graphSelector(gt, name), context.Background())
	metas, err := source.List(ctx, 0)
	if err != nil {
		return 0, false, err
	}
	hnswFormat := hnsw.New().Name()
	for _, meta := range metas {
		if meta.Format != hnswFormat {
			continue
		}
		if meta.DocCount == 0 {
			anyUnknown = true
			continue
		}
		covered += meta.DocCount
	}
	return covered, anyUnknown, nil
}

// ResidentDocCount returns the LIVE in-memory HNSW engine resident doc count for
// one graph: the summed sealed-segment DocCount currently imported into the
// searchable set. It is the read-side coverage operand the degeneracy probe
// compares against the server's shipped doc count (the SAME operand
// recoverIfDegenerate uses internally) — distinct from ShippedSegmentDocCount,
// which reads the SERVER's shipped count. A graph that has never been searched or
// loaded returns 0 (the lazily-constructed engine's set is empty). It is a single
// atomic snapshot (SegmentedIndex.ResidentDocCount) with no RPC and no load — the
// caller decides whether to load() first (the reconcile probe does; the status
// column reads raw current resident).
func (m *Manager) ResidentDocCount(gt kgtypes.GraphType, name string) int {
	return m.managerFor(gt, name).engine.ResidentDocCount()
}

// managerFor lazily constructs (check-construct-store under the mutex) the
// per-graph distManager: a SegmentedIndex over the HNSW format, an
// rpcSegmentSource for the graph's selector, and a content-addressed L2 cache
// rooted per-graph so distinct graphs never collide on the content-hash filename
// space.
func (m *Manager) managerFor(gt kgtypes.GraphType, name string) *distManager[[]byte, struct{}] {
	return m.hnswManagerFor(m.managers, hnsw.New(), gt, name)
}

// hnswManagerFor is the shared HNSW distManager factory both the embed path
// (managerFor → m.managers, hnsw.New()) and the deterministic rebuild path
// (AddDeterministic/FlushDeterministic → m.detManagers, hnsw.NewDeterministic())
// route through. dst selects WHICH per-graph map the memoized instance is keyed
// in; fmtVariant selects the Format the engine builds with. The two maps are
// distinct so the embed and rebuild engines for the SAME graph never share a
// coalescing buffer or a shippedIDs seed — but they share one content-addressed
// cache root (hnsw.New().Name() == hnsw.NewDeterministic().Name() == "hnsw"),
// which is SAFE because content-hash filenames mean deterministic-vs-parallel
// segments for the same nodes hash differently and never collide.
func (m *Manager) hnswManagerFor(
	dst map[graphKey]*distManager[[]byte, struct{}], fmtVariant hnsw.Format, gt kgtypes.GraphType, name string,
) *distManager[[]byte, struct{}] {
	k := graphKey{graphType: gt, graphName: name}
	m.mu.Lock()
	defer m.mu.Unlock()
	if dm, ok := dst[k]; ok {
		return dm
	}

	target := graphSelector(gt, name)
	engine := searchengine.New[[]byte, struct{}](fmtVariant, searchengine.Options{})
	source := newRPCSegmentSource(m.caller, target, context.Background())
	cache := newDiskSegmentCache(graphCacheDirFor(m.cacheDir, gt, name, fmtVariant.Name()), m.maxBytes)
	dm := newDistManager(engine, source, cache, target, fmtVariant.Name())
	dst[k] = dm
	return dm
}

// bm25ManagerFor lazily constructs (check-construct-store under the mutex) the
// per-graph BM25 distManager: a SegmentedIndex over the BM25 format, an
// rpcSegmentSource for the graph's selector (same format-agnostic routing the
// HNSW path uses), and a content-addressed L2 cache rooted under a BM25-distinct
// directory so HNSW and BM25 blobs never collide on the content-hash filename
// space. Mirrors managerFor; the only differences are the format type parameters
// and the format tag on the cache dir.
func (m *Manager) bm25ManagerFor(gt kgtypes.GraphType, name string) *distManager[bm25.Query, *bm25.CorpusStats] {
	k := graphKey{graphType: gt, graphName: name}
	m.mu.Lock()
	defer m.mu.Unlock()
	if dm, ok := m.bm25Managers[k]; ok {
		return dm
	}

	target := graphSelector(gt, name)
	engine := searchengine.New[bm25.Query, *bm25.CorpusStats](bm25.New(), searchengine.Options{})
	source := newRPCSegmentSource(m.caller, target, context.Background())
	cache := newDiskSegmentCache(graphCacheDirFor(m.cacheDir, gt, name, bm25.New().Name()), m.maxBytes)
	dm := newDistManager(engine, source, cache, target, bm25.New().Name())
	m.bm25Managers[k] = dm
	return dm
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

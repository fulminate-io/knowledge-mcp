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
	// writerID is the stable per-machine identity threaded onto every
	// rpcSegmentSource this Manager constructs, so each outbound segment RPC
	// carries it for the server's last-connection liveness stamp. Set at
	// construction from the stable writer-id helper; "" is a tolerated no-op
	// server-side (an older client that does not supply one).
	writerID string

	mu sync.Mutex
	// managers holds the HNSW engine per graph (vectors). bm25Managers holds the
	// BM25 engine per graph (field-bearing text). Both guarded by mu.
	managers     map[graphKey]*distManager[[]byte, struct{}]
	bm25Managers map[graphKey]*distManager[bm25.Query, *bm25.CorpusStats]
	// detManagers holds the DETERMINISTIC HNSW engine per graph used ONLY by the
	// segment_rebuild path. Kept separate from managers so the rebuild's build
	// engine never shares a coalescing buffer with the embed engine for the same
	// graph. (Both engines now build byte-reproducibly — the HNSW builder is
	// deterministic everywhere — so the split is purely buffer/seed isolation, not a
	// build-variant distinction.) Guarded by mu.
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
// unbounded cache. The stable per-machine writer_id is resolved once
// here via the writer-id helper and threaded onto every outbound segment RPC.
func NewManager(caller segmentCaller, cacheDir string, maxBytes int64) *Manager {
	return &Manager{
		caller:       caller,
		cacheDir:     cacheDir,
		maxBytes:     maxBytes,
		writerID:     writerID(),
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
// writeback) — a dropped ship is RETRIED on the next embed dirty-gen, including a
// transient seed-List failure: the seed re-arms (it latches only on a List(0)
// success — ensureShippedSeeded), so a failed seed does not poison shipping for the
// process lifetime.
func (m *Manager) AddAndShip(ctx context.Context, gt kgtypes.GraphType, name string, docs []searchengine.Document) error {
	if len(docs) == 0 {
		return nil
	}
	dm := m.managerFor(gt, name)
	if err := dm.engine.Add(docs); err != nil {
		return err
	}
	// REGISTRY MODEL: publish this writer's RESIDENT live set as its
	// "hnsw" manifest, unioned with the deterministic engine's resident ids (both
	// share the one "hnsw" graphKey manifest, so the embed publish must keep the
	// det engine's resident blobs referenced or it would reap them). The server
	// refcount-GCs whatever dropped out of the live set — restart-safe because the
	// resident Export omits merged-away constituents and a fresh process's
	// re-imported corpus is its resident set, not a reaped diff.
	_, err := dm.shipAndPublish(ctx, m.detResidentHNSWIDs(gt, name), dm.locallyShipped)
	return err
}

// detResidentHNSWIDs returns the resident Export() ids of the DETERMINISTIC HNSW
// engine for (gt, name) when one exists, else nil. The embed HNSW publish unions
// these into its "hnsw" manifest because the embed and deterministic engines
// share ONE (graphKey, writer, "hnsw") manifest — without the union, an embed
// publish would reap the deterministic engine's still-resident blobs. Returns nil
// when no deterministic engine has been constructed for the graph (the common
// embed-only case).
func (m *Manager) detResidentHNSWIDs(gt kgtypes.GraphType, name string) []searchengine.SegmentID {
	m.mu.Lock()
	dm, ok := m.detManagers[graphKey{graphType: gt, graphName: name}]
	m.mu.Unlock()
	if !ok {
		return nil
	}
	exported := dm.engine.Export()
	ids := make([]searchengine.SegmentID, 0, len(exported))
	for _, b := range exported {
		ids = append(ids, b.ID)
	}
	return ids
}

// AddAndShipFields is the BM25 counterpart to AddAndShip: it routes field-bearing
// Documents to the (graphType, graphName) BM25 engine, Adds them (the engine
// coalesces internally up to MinSegmentDocs — a sub-threshold batch just buffers,
// no segment seal), then ships any newly-sealed segments (diff-gated no-op when
// nothing new sealed). Returns the first error from Add or ship; the pipeline
// caller treats any error as best-effort (WARN, never fail writeback) — a dropped
// ship is RETRIED on the next embed dirty-gen, including a transient seed-List
// failure: the seed re-arms (it latches only on a List(0) success —
// ensureShippedSeeded), so a failed seed does not poison shipping for the process
// lifetime.
func (m *Manager) AddAndShipFields(ctx context.Context, gt kgtypes.GraphType, name string, docs []searchengine.Document) error {
	if len(docs) == 0 {
		return nil
	}
	dm := m.bm25ManagerFor(gt, name)
	if err := dm.engine.Add(docs); err != nil {
		return err
	}
	// REGISTRY MODEL: publish the BM25 resident live set as its "bm25"
	// manifest. BM25 has a single engine per graph (no deterministic variant), so
	// there is no sibling-engine union to carry — the manifest is exactly this
	// engine's resident Export().
	_, err := dm.shipAndPublish(ctx, nil, dm.locallyShipped)
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
	// REGISTRY MODEL: embed force-seal of the sub-threshold tail, then
	// publish the resident live set as the manifest (unioned with the deterministic
	// engine's resident ids for the shared "hnsw" manifest) — restart-safe.
	if _, err := hnsw.shipAndPublish(ctx, m.detResidentHNSWIDs(gt, name), hnsw.locallyShipped); err != nil {
		return err
	}
	bm := m.bm25ManagerFor(gt, name)
	if err := bm.engine.Flush(); err != nil {
		return err
	}
	_, err := bm.shipAndPublish(ctx, nil, bm.locallyShipped)
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
	dm := m.hnswManagerFor(m.detManagers, hnsw.NewDeterministic(), gt, name, false)
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
// DETERMINISTIC HNSW engine and PUBLISHES its resident live set ONCE (the only
// ship on the deterministic path — because it runs after the pool joins, the
// single Export it publishes is COMPLETE). It then seals + publishes the BM25
// tail once.
//
// REGISTRY MODEL — ROLE A: the rebuild publishes its RESIDENT Export as
// the writer's "hnsw" manifest (NOT a force-reloaded set — a force-reload would
// re-List the OLD corpus and resurface superseded ids via publishImport, defeating
// the replace). It publishes the DETERMINISTIC resident set ONLY — NOT unioned with
// the embed engine's resident set: the rebuild's documented purpose is to REPLACE
// the (possibly degenerate, non-deterministic) embed corpus, whose blobs hash
// DIFFERENTLY from the deterministic rebuild and so are correctly reaped by the
// refcount-GC. Any node the rebuild ALSO covers hashes identically (deterministic
// build) and is already in the det Export, so the legitimate overlap is preserved
// without a union; an embed blob for a node OUTSIDE the rebuild scan self-heals on
// the next embed dirty-gen ship (which re-publishes the embed manifest) — the same
// self-healing the embed path already relies on. (A det∪embed union was
// considered; it pins the stale embed corpus the rebuild is replacing and breaks
// the replace-regression guards — det-only is the resolution that keeps every
// guard green and matches the production rebuild flow, where the rebuild driver
// never populates the embed engine.)
//
// The reconcile is against shippedIDs (the server-seeded full set), so
// shippedIDs − liveSet is exactly the superseded old corpus, returned for local L2
// eviction. The driver error-gates the rebuild BEFORE calling FlushDeterministic,
// and the publish coverage/subset gate blocks a degenerate rebuild from publishing
// a wipe-inducing manifest.
//
// RETURNS the HNSW superseded []SegmentID (the old-corpus ids the publish reaped
// server-side) so the driver feeds them to InvalidateLocal for local L2 .seg
// eviction. The BM25 publish's dropped set is irrelevant to local embed-cache
// invalidation, so it is discarded.
func (m *Manager) FlushDeterministic(ctx context.Context, gt kgtypes.GraphType, name string) ([]searchengine.SegmentID, error) {
	hnswDM := m.hnswManagerFor(m.detManagers, hnsw.NewDeterministic(), gt, name, false)
	if err := hnswDM.engine.Flush(); err != nil {
		return nil, err
	}
	// ROLE A: publish the deterministic resident Export, reconciling against
	// shippedIDs so the superseded old corpus is reaped + returned.
	superseded, err := hnswDM.shipAndPublish(ctx, nil, hnswDM.shippedIDs)
	if err != nil {
		return nil, err
	}
	bm := m.bm25ManagerFor(gt, name)
	if err := bm.engine.Flush(); err != nil {
		return nil, err
	}
	if _, err := bm.shipAndPublish(ctx, nil, bm.shippedIDs); err != nil {
		return nil, err
	}
	return superseded, nil
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
	dm := m.hnswManagerFor(m.detManagers, hnsw.NewDeterministic(), gt, name, false)
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
//
// Standalone wrapper for callers that probe presence ALONE (no co-located doc-count
// probe to share a snapshot with). The shared-snapshot heal path uses
// ShippedManifestSnapshot + HasShippedFromSnapshot to collapse its List(0)s.
func (m *Manager) HasShippedSegments(ctx context.Context, gt kgtypes.GraphType, name string) (bool, error) {
	snapshot, err := m.ShippedManifestSnapshot(ctx, gt, name)
	if err != nil {
		return false, err
	}
	return m.HasShippedFromSnapshot(snapshot), nil
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
//
// Standalone wrapper preserved for the external coverage seam
// (tools.SegmentCoverageReader → manage(status)), which probes ONE graph's doc count
// in isolation. The shared-snapshot heal path uses ShippedDocCountFromSnapshot.
func (m *Manager) ShippedSegmentDocCount(
	ctx context.Context, gt kgtypes.GraphType, name string,
) (covered int, anyUnknown bool, err error) {
	snapshot, err := m.ShippedManifestSnapshot(ctx, gt, name)
	if err != nil {
		return 0, false, err
	}
	covered, anyUnknown = m.ShippedDocCountFromSnapshot(snapshot)
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
	// Embed engine: hnsw.New() is the deterministic builder (the only HNSW builder),
	// so the live ship path is byte-reproducible — two writers building the same
	// nodes mint the same content-hash blob, the content-addressed store dedups to
	// one copy at refcount-N, and exact-match recall is recovered. Auto-reclaim
	// superseded constituents from the LIVE L2 cache on every background merge.
	return m.hnswManagerFor(m.managers, hnsw.New(), gt, name, true)
}

// hnswManagerFor is the shared HNSW distManager factory both the embed path
// (managerFor → m.managers) and the segment_rebuild path
// (AddDeterministic/FlushDeterministic → m.detManagers) route through. dst selects
// WHICH per-graph map the memoized instance is keyed in; fmtVariant is the HNSW
// Format (always the deterministic builder now). The two maps are distinct so the
// embed and rebuild engines for the SAME graph never share a coalescing buffer or a
// shippedIDs seed — but they share one content-addressed cache root (Name() is the
// constant "hnsw"), which is SAFE because content-hash filenames key on the bytes:
// both engines build the same nodes byte-identically and so correctly share one
// cache entry, while non-overlapping content lands under a distinct hash — no
// collision either way.
//
// autoReclaim gates the merge-completion hook: the EMBED engine (managerFor) wires
// Options.OnMerge so a background merge reclaims the superseded constituents from
// its live L2 cache; the DETERMINISTIC rebuild engine passes false (nil OnMerge),
// because its superseded segments are reclaimed through the ROLE-A
// FlushDeterministic→InvalidateLocal path, NOT the live embed cache — auto-
// reclaiming there would Remove against the wrong cache lifecycle.
func (m *Manager) hnswManagerFor(
	dst map[graphKey]*distManager[[]byte, struct{}], fmtVariant hnsw.Format, gt kgtypes.GraphType, name string, autoReclaim bool,
) *distManager[[]byte, struct{}] {
	k := graphKey{graphType: gt, graphName: name}
	m.mu.Lock()
	defer m.mu.Unlock()
	if dm, ok := dst[k]; ok {
		return dm
	}

	target := graphSelector(gt, name)
	source := newRPCSegmentSource(m.caller, target, m.writerID, context.Background())
	cache := newDiskSegmentCache(graphCacheDirFor(m.cacheDir, gt, name, fmtVariant.Name()), m.maxBytes)

	// var-before-assign: the OnMerge closure back-references the distManager that is
	// constructed AFTER the engine. Safe because OnMerge cannot fire before the
	// engine's first merge, which cannot happen before this function returns (the
	// engine holds no documents at construction and the first merge tick is 50ms
	// out against an empty set).
	var dm *distManager[[]byte, struct{}]
	opts := searchengine.Options{}
	if autoReclaim {
		opts.OnMerge = func(res searchengine.MergeResult) { dm.reclaimMerged(res) }
	}
	engine := searchengine.New[[]byte, struct{}](fmtVariant, opts)
	dm = newDistManager(engine, source, cache, target, fmtVariant.Name())
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
	source := newRPCSegmentSource(m.caller, target, m.writerID, context.Background())
	cache := newDiskSegmentCache(graphCacheDirFor(m.cacheDir, gt, name, bm25.New().Name()), m.maxBytes)

	// var-before-assign OnMerge: the BM25 engine is embed-only (no deterministic
	// variant), so it always auto-reclaims superseded constituents from its live L2
	// cache on a background merge. Same back-reference-after-construction safety as
	// hnswManagerFor (OnMerge cannot fire before this returns).
	var dm *distManager[bm25.Query, *bm25.CorpusStats]
	engine := searchengine.New[bm25.Query, *bm25.CorpusStats](bm25.New(), searchengine.Options{
		OnMerge: func(res searchengine.MergeResult) { dm.reclaimMerged(res) },
	})
	dm = newDistManager(engine, source, cache, target, bm25.New().Name())
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

// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// Manager is the PRODUCTION owner of per-graph HNSW segment engines. It is the
// production constructor + per-graph routing layer over the distManager: one
// searchengine.SegmentedIndex[[]byte, struct{}] over the HNSW format per
// (graphType, graphName), lazily constructed, each wired to its segment source (a
// GCS-agent source when logged in, an L2-only local source otherwise) + an L2
// diskSegmentCache.
//
// The client builds + ships HNSW segments from the binary vectors it already
// holds at the pipeline embed-writeback seam. AddAndShip is the HNSW
// entry point; AddAndShipFields is the BM25 entry point. ONE Manager owns BOTH
// formats per graph — two per-format per-graph maps, each lazily constructed and
// rooted under a format-distinct L2 cache directory so they never collide.
type Manager struct {
	caller   loginState
	cacheDir string
	maxBytes int64

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

	// segTransport lazily builds the agent /v1/segments control transport for the
	// cloud (logged-in) segment source. nil when no builder was supplied: on the
	// logged-in branch the source factory then returns the fail-loud
	// errorSegmentSource sentinel (a logged-in client with no/failed transport is
	// misconfigured — it must surface, not silently degrade). Sampled once per lazy
	// per-graph source construction. The production *auth.Transport it returns
	// satisfies the SegmentControlTransport seam; in-package tests inject a fake
	// through the same builder.
	segTransport func() (SegmentControlTransport, error)

	// testSource, when non-nil, is the segmentSource EVERY lazily-constructed
	// distManager uses, bypassing the newSegmentSource capability gate entirely. It
	// is TEST-ONLY (set via withSegmentSource) so the surviving in-package machinery
	// tests inject a fake segment source without threading it through the production
	// login/transport gate. nil in every production Manager.
	testSource segmentSource
}

// targetBindable is the seam newSegmentSource uses to re-bind an INJECTED test
// source to the per-graph target, without the production factory referencing a
// test-only concrete type. Only the in-package test fake implements it; production
// sources (gcs/local/error) do not, so the type-assert is a no-op for them.
type targetBindable interface {
	bindTarget(*knowledgev1.GraphSelector)
}

// ManagerOption configures a Manager at construction. See the With* functions.
type ManagerOption func(*Manager)

// WithSegmentTransport supplies the lazy agent /v1/segments control-transport
// builder that selects the cloud (GCS) segment source on the logged-in path. The
// production caller wraps cli.BuildSyncTransport (which returns the *auth.Transport
// satisfying SegmentControlTransport). Without this option a Manager has no transport
// builder and the logged-in path falls back to the RPC segment source (prior
// behavior).
func WithSegmentTransport(builder func() (SegmentControlTransport, error)) ManagerOption {
	return func(m *Manager) { m.segTransport = builder }
}

// withSegmentSource is the TEST-ONLY option that pins the segmentSource every
// lazily-constructed distManager uses, bypassing the newSegmentSource capability
// gate. The surviving in-package machinery tests inject a fakeSegmentSource through
// it so they exercise the manager over a controllable double without a live
// login/transport. It is unexported and never used by production code.
func withSegmentSource(src segmentSource) ManagerOption {
	return func(m *Manager) { m.testSource = src }
}

// graphKey routes one (graphType, graphName) to its dedicated engine+distManager.
type graphKey struct {
	graphType kgtypes.GraphType
	graphName string
}

// NewManager constructs the production owner. caller reports the live cloud login
// state (production *graphclient.Router.LoggedIn) so the source factory selects
// the GCS source when logged in and the L2-local source otherwise. cacheDir roots
// the per-graph L2 disk caches; maxBytes <= 0 means an unbounded cache.
//
// opts are optional construction knobs; WithSegmentTransport supplies the cloud
// segment-transport builder that selects the GCS source on the logged-in path.
func NewManager(caller loginState, cacheDir string, maxBytes int64, opts ...ManagerOption) *Manager {
	m := &Manager{
		caller:       caller,
		cacheDir:     cacheDir,
		maxBytes:     maxBytes,
		managers:     make(map[graphKey]*distManager[[]byte, struct{}]),
		bm25Managers: make(map[graphKey]*distManager[bm25.Query, *bm25.CorpusStats]),
		detManagers:  make(map[graphKey]*distManager[[]byte, struct{}]),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
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

// detResidentHNSWIDs returns the resident Export() digests (id + doc_count) of the
// DETERMINISTIC HNSW engine for (gt, name) when one exists, else nil. The embed HNSW
// publish unions these into its "hnsw" manifest because the embed and deterministic
// engines share ONE (graphKey, writer, "hnsw") manifest — without the union, an
// embed publish would reap the deterministic engine's still-resident blobs. Each
// digest carries the blob's DocCount so the GCS manifest records the sibling
// engine's real per-digest denominator. Returns nil when no deterministic engine has
// been constructed for the graph (the common embed-only case).
func (m *Manager) detResidentHNSWIDs(gt kgtypes.GraphType, name string) []segmentDigest {
	m.mu.Lock()
	dm, ok := m.detManagers[graphKey{graphType: gt, graphName: name}]
	m.mu.Unlock()
	if !ok {
		return nil
	}
	exported := dm.engine.Export()
	digests := make([]segmentDigest, 0, len(exported))
	for _, b := range exported {
		digests = append(digests, segmentDigest{ID: b.ID, DocCount: b.DocCount})
	}
	return digests
}

// hasDetManager reports whether a deterministic HNSW engine has ALREADY been
// constructed for (gt, name) — i.e. a real rebuild ran this process. The OSS
// PruneCache live-set builder uses it to avoid force-loading a FRESHLY-constructed
// det engine (whose scanExisting would pull accumulated on-disk orphans into the L2
// live set); on the cloud path the det force-load Lists the server, so a fresh
// construction is harmless there.
func (m *Manager) hasDetManager(gt kgtypes.GraphType, name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.detManagers[graphKey{graphType: gt, graphName: name}]
	return ok
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
// auto-heal arm uses: ONE ShippedManifestSnapshot read for the graph, returning true
// when it holds at least one segment meta. The snapshot is login-gated (cloud →
// the GCS agent manifest/read; OSS not-logged-in → the L2-local source's set), so
// this probe follows whichever source the graph runs on. It does NOT Fetch any blob
// and does NOT touch the per-graph engines/maps, so it is safe to call on the embed
// drain edge without disturbing resident state — strictly the presence list.
//
// Standalone wrapper for callers that probe presence ALONE (no co-located doc-count
// probe to share a snapshot with). The shared-snapshot heal path uses
// ShippedManifestSnapshot + HasShippedFromSnapshot to collapse its reads.
func (m *Manager) HasShippedSegments(ctx context.Context, gt kgtypes.GraphType, name string) (bool, error) {
	snapshot, err := m.ShippedManifestSnapshot(ctx, gt, name)
	if err != nil {
		return false, err
	}
	return m.HasShippedFromSnapshot(snapshot), nil
}

// ShippedSegmentDocCount is the coverage-ratio probe's data source: it reports the
// "segment-covered docs" count for the graph's HNSW coverage. It returns:
//
//   - covered: the segment-covered HNSW doc count. On the cloud path it is the
//     summed HNSW meta.DocCount from the GCS manifest snapshot; on the OSS path it is
//     the L2 resident HNSW doc count. ONLY the HNSW dimension is counted: BM25 metas
//     index the SAME nodes, so counting both would double-count; HNSW is the
//     per-node vector coverage that mirrors the graph's binary_vector_count
//     denominator the coverage ratio compares against.
//   - anyUnknown: true when ANY summed HNSW meta has DocCount==0 (cloud path only).
//     A zero doc count means that segment predates the doc_count wire plumbing (an
//     old blob written before the field existed), so its real coverage is UNKNOWN.
//     The coverage probe treats anyUnknown as the conservative-unknown signal and
//     DISARMS the ratio trigger (falling back to the zero-only heal) — without this
//     guard a fleet mid-migration, whose every shipped meta still reports
//     doc_count=0, would read covered=0 on every graph and trigger a fleet-wide
//     rebuild storm. The OSS/L2 path never returns anyUnknown (the resident count is
//     always a real, known denominator).
//
// It is SOURCE-AWARE, mirroring the heal path's healNeedsRebuildLocal split:
//
//   - OSS / L2-authoritative (not logged in): there is no server/GCS manifest, and
//     the local source's List stamps DocCount=0 (so the snapshot would report
//     covered=0/anyUnknown=true and wrongly disarm). Instead the covered count is
//     the L2 RESIDENT HNSW doc count (LoadResidentDocCount) — the same L2 numerator
//     the OSS heal decision uses. anyUnknown is false: the resident count is a real,
//     known denominator, never the pre-doc_count sentinel.
//   - CLOUD (logged-in): the GCS manifest carries real per-digest doc_counts, so the
//     covered count is summed from the ShippedManifestSnapshot (the prior behavior).
//
// The OSS branch loads the read engine (idempotent, L2-only); the cloud branch does
// NOT touch the per-graph engines/maps (one manifest read, no blob fetch).
//
// Standalone wrapper preserved for the external coverage seam
// (tools.SegmentCoverageReader → manage(status)), which probes ONE graph's doc count
// in isolation. The shared-snapshot heal path uses ShippedDocCountFromSnapshot.
func (m *Manager) ShippedSegmentDocCount(
	ctx context.Context, gt kgtypes.GraphType, name string,
) (covered int, anyUnknown bool, err error) {
	if m.IsL2Authoritative(gt, name) {
		// OSS path: the L2 resident HNSW doc count is the covered denominator (the
		// local source's List stamps DocCount=0, so the manifest snapshot cannot
		// supply it). Known count → anyUnknown is always false.
		resident, err := m.LoadResidentDocCount(ctx, gt, name)
		if err != nil {
			return 0, false, err
		}
		return resident, false, nil
	}
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

// IsL2Authoritative reports whether (gt, name) runs on the OSS-local L2-only source
// (the not-logged-in path) — reading the HNSW embed manager's l2Authoritative flag.
// The flag is uniform per graph across formats (both formats derive it from the same
// caller gate), so the HNSW manager's value is representative. The bootstrap heal
// path calls this to route the OSS degeneracy collapse: an L2-authoritative graph
// heals from resident-vs-embedded locally, with NO server presence probe.
func (m *Manager) IsL2Authoritative(gt kgtypes.GraphType, name string) bool {
	return m.managerFor(gt, name).l2Authoritative
}

// LoadResidentDocCount loads the graph's HNSW engine (idempotent; L2-only on the OSS
// path) and returns the resident HNSW doc count — the L2 resident numerator the OSS
// degeneracy collapse compares against the embedded-node denominator. It is the
// load-first variant of ResidentDocCount (the raw accessor does NOT load), needed
// because the heal path must import the warm L2 set before reading the count.
func (m *Manager) LoadResidentDocCount(ctx context.Context, gt kgtypes.GraphType, name string) (int, error) {
	dm := m.managerFor(gt, name)
	if err := dm.load(ctx); err != nil {
		return 0, err
	}
	return dm.engine.ResidentDocCount(), nil
}

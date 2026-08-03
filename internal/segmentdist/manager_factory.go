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

// managerFor lazily constructs (check-construct-store under the mutex) the
// per-graph distManager: a SegmentedIndex over the HNSW format, a segment source
// selected by the login gate (GCS when logged in, L2-local otherwise) for the
// graph's selector, and a content-addressed L2 cache rooted per-graph so distinct
// graphs never collide on the content-hash filename space.
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
func (m *Manager) managerFor(gt kgtypes.GraphType, name string) *distManager[[]byte, struct{}] {
	hnswFormat := hnsw.New()
	k := graphKey{graphType: gt, graphName: name}
	m.mu.Lock()
	defer m.mu.Unlock()
	if dm, ok := m.managers[k]; ok {
		return dm
	}

	target := graphSelector(gt, name)
	// Build the cache FIRST so the login gate can hand the OSS-local source its L2
	// backing (the localSegmentSource is L2-only). newSegmentSource picks
	// gcs-vs-local by the caller's live login state.
	cache := newDiskSegmentCache(graphCacheDirFor(m.cacheDir, gt, name, hnswFormat.Name()), m.maxBytes)
	source := m.newSegmentSource(cache, gt, name, target, hnswFormat.Name())

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
	}
	opts.OnMerge = func(res searchengine.MergeResult) { dm.reclaimMerged(res) }
	engine := searchengine.New[[]byte, struct{}](hnswFormat, opts)
	dm = newDistManager(engine, source, cache, target, hnswFormat.Name())
	// Suppression hook: when this engine's publish coverage gate becomes
	// unsatisfiable, record the graph so the periodic reconcile consumer looks
	// sooner. Same after-construction assignment safety as OnMerge above — the hook
	// cannot fire before this function returns. Because BOTH HNSW maps route through
	// here, this one wiring covers the embed and the deterministic engine alike.
	dm.onCoverageSuppressed = func() { m.flagReconcileNudge(gt, name) }
	// Record every LANDED manifest swap's fingerprint so the off-hot-path
	// completeness reconcile can gate its source read on a local comparison. BOTH
	// HNSW maps route through here, so the embed and the deterministic engine both
	// record — correctly, since they publish the SAME (graph, "hnsw") manifest and
	// whichever swapped last is the current one.
	dm.onManifestPublished = func(ids []searchengine.SegmentID) {
		if err := m.saveManifestFingerprint(gt, name, hnswFormat.Name(), fingerprintOf(ids)); err != nil {
			slog.Warn("segmentdist: could not record the published manifest fingerprint",
				"graph_type", gt, "name", name, "format", hnswFormat.Name(), "err", err)
		}
	}
	// Seed every import from the owner's live tombstone set, read at import time
	// rather than captured, so ids learned after this engine was built still apply.
	dm.tombstoneSeed = func() []searchengine.ExternalID { return m.graphTombstones(gt, name) }
	m.managers[k] = dm
	return dm
}

// bm25ManagerFor lazily constructs (check-construct-store under the mutex) the
// per-graph BM25 distManager: a SegmentedIndex over the BM25 format, a segment
// source selected by the login gate (same format-agnostic routing the HNSW path
// uses), and a content-addressed L2 cache rooted under a BM25-distinct directory so
// HNSW and BM25 blobs never collide on the content-hash filename space. Mirrors
// managerFor; the only differences are the format type parameters and the format
// tag on the cache dir.
func (m *Manager) bm25ManagerFor(gt kgtypes.GraphType, name string) *distManager[bm25.Query, *bm25.CorpusStats] {
	k := graphKey{graphType: gt, graphName: name}
	m.mu.Lock()
	defer m.mu.Unlock()
	if dm, ok := m.bm25Managers[k]; ok {
		return dm
	}

	target := graphSelector(gt, name)
	// Build the cache FIRST so the capability gate can hand the OSS-local source its
	// L2 backing (mirrors hnswManagerFor).
	cache := newDiskSegmentCache(graphCacheDirFor(m.cacheDir, gt, name, bm25.New().Name()), m.maxBytes)
	source := m.newSegmentSource(cache, gt, name, target, bm25.New().Name())

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
	})
	dm = newDistManager(engine, source, cache, target, bm25.New().Name())
	// Suppression hook, mirroring hnswManagerFor: the BM25 engine keeps its OWN skip
	// streak, so it can cross its suppression transition independently of the HNSW
	// engines for the same graph. The nudge set is keyed by graph, so those collapse.
	dm.onCoverageSuppressed = func() { m.flagReconcileNudge(gt, name) }
	// Same publish-time fingerprint record as the HNSW engines, under this format's
	// own key: the two formats carry SEPARATE manifests over the same nodes, so a
	// shortfall in one says nothing about the other and each needs its own number.
	dm.onManifestPublished = func(ids []searchengine.SegmentID) {
		if err := m.saveManifestFingerprint(gt, name, bm25.New().Name(), fingerprintOf(ids)); err != nil {
			slog.Warn("segmentdist: could not record the published manifest fingerprint",
				"graph_type", gt, "name", name, "format", bm25.New().Name(), "err", err)
		}
	}
	// Same import-time tombstone seed as the HNSW engines: the field corpus indexes
	// the same nodes and carries its own manifest, so a pre-delete BM25 blob
	// resurrects a removed node exactly as a vector blob would.
	dm.tombstoneSeed = func() []searchengine.ExternalID { return m.graphTombstones(gt, name) }
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

// newSegmentSource selects the per-graph segment source by the caller's live cloud
// login state. It is a TWO-way gate:
//
//  1. NOT logged in — the OSS path, where there is no cloud segment registry to
//     consult → the L2-only localSegmentSource.
//  2. LOGGED IN (cloud) → the GCS-agent segment source (newGCSSegmentSource) when a
//     segment-transport builder was supplied and succeeds; otherwise the fail-loud
//     errorSegmentSource sentinel — a logged-in client with no/failed transport is
//     misconfigured and MUST surface an operator-actionable error rather than
//     silently degrade (there is no server SegmentService fallback anymore).
//
// The production caller is *graphclient.Router, which reports LoggedIn ==
// (machineAuth || live keychain login) — the SAME cloud-vs-local signal Router.pick
// uses, so no new capability plumbing is introduced.
//
// The login state + transport are sampled ONCE here at lazy per-graph construction
// and memoized (the distManager keeps this source for its lifetime; it never
// re-selects). This is the ratified option (a): a mid-session OSS->cloud
// `knowledge login` requires a daemon restart before segments ship to the cloud
// registry — lossless, since segments are deterministically rebuildable cloud-side,
// and it avoids a risky live hot-swap of an L2-authoritative manager's source. It
// also means a transport-build failure PINS the errorSegmentSource sentinel for
// that graph until restart (accepted — a misconfigured logged-in client). cache is
// consumed only on the OSS-local branch (1); the gcs/error branches ignore it (a
// nil cache is safe there — ShippedManifestSnapshot's cloud arm passes nil).
//
// testSource short-circuits the whole gate (test-only, via withSegmentSource): the
// surviving in-package machinery tests inject a fakeSegmentSource here.
func (m *Manager) newSegmentSource(cache segmentL2Cache, gt kgtypes.GraphType, name string, target *knowledgev1.GraphSelector, format string) segmentSource {
	if m.testSource != nil {
		// Test injection: re-bind the injected source to the graph's target so its
		// backing server records/reads under the right target-key. The production
		// sources are per-graph; the single injected test double is re-bound to whichever
		// graph the manager is building/probing here (snapshot/coverage probes call
		// newSegmentSource fresh per graph, so the re-bind routes each probe correctly;
		// single-graph engine tests re-bind to the same target — a no-op). The
		// targetBindable seam keeps this production-visible without referencing a
		// test-only concrete type.
		if tb, ok := m.testSource.(targetBindable); ok {
			tb.bindTarget(target)
		}
		return m.testSource
	}
	if !m.caller.LoggedIn(context.Background()) {
		return newLocalSegmentSource(cache, format) // (1) OSS not-logged-in → L2-local
	}
	// (2) logged-in cloud: the GCS source when a transport builder is present and
	// succeeds, else the fail-loud sentinel.
	return m.cloudSegmentSource(gt, name, format)
}

// cloudSegmentSource builds the GCS-agent segment source for the logged-in cloud
// path when a transport builder is present and succeeds; otherwise it returns the
// fail-loud errorSegmentSource sentinel (no builder supplied, or the build failed).
// A logged-in client with a broken segment transport is misconfigured — the
// sentinel makes every leg surface an operator-actionable error rather than
// silently degrading to a phantom source.
func (m *Manager) cloudSegmentSource(gt kgtypes.GraphType, name, format string) segmentSource {
	if m.segTransport == nil {
		return &errorSegmentSource{reason: errNoSegmentTransportBuilder}
	}
	transport, err := m.segTransport()
	if err != nil {
		slog.Warn("segmentdist: segment transport build failed — logged-in cloud segment source unavailable (fail-loud sentinel)",
			"graph_type", gt, "name", name, "err", err)
		return &errorSegmentSource{reason: err}
	}
	if transport == nil {
		return &errorSegmentSource{reason: errNilSegmentTransport}
	}
	return newGCSSegmentSource(transport, string(gt), name, format)
}

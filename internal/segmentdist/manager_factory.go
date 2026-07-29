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
	// Build the cache FIRST so the login gate can hand the OSS-local source its L2
	// backing (the localSegmentSource is L2-only). newSegmentSource picks
	// gcs-vs-local by the caller's live login state.
	cache := newDiskSegmentCache(graphCacheDirFor(m.cacheDir, gt, name, fmtVariant.Name()), m.maxBytes)
	source := m.newSegmentSource(cache, gt, name, target, fmtVariant.Name())

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
	// Suppression hook: when this engine's publish coverage gate becomes
	// unsatisfiable, record the graph so the periodic reconcile consumer looks
	// sooner. Same after-construction assignment safety as OnMerge above — the hook
	// cannot fire before this function returns. Because BOTH HNSW maps route through
	// here, this one wiring covers the embed and the deterministic engine alike.
	dm.onCoverageSuppressed = func() { m.flagReconcileNudge(gt, name) }
	dst[k] = dm
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
	// cache on a background merge. Same back-reference-after-construction safety as
	// hnswManagerFor (OnMerge cannot fire before this returns).
	var dm *distManager[bm25.Query, *bm25.CorpusStats]
	engine := searchengine.New[bm25.Query, *bm25.CorpusStats](bm25.New(), searchengine.Options{
		OnMerge: func(res searchengine.MergeResult) { dm.reclaimMerged(res) },
	})
	dm = newDistManager(engine, source, cache, target, bm25.New().Name())
	// Suppression hook, mirroring hnswManagerFor: the BM25 engine keeps its OWN skip
	// streak, so it can cross its suppression transition independently of the HNSW
	// engines for the same graph. The nudge set is keyed by graph, so those collapse.
	dm.onCoverageSuppressed = func() { m.flagReconcileNudge(gt, name) }
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

// SPDX-License-Identifier: Apache-2.0

// manager_rebuild_entry.go — the Manager's WRITE + FINALIZE entry points: the
// migration/one-shot Flush, the reset rebuild's single staging entry point, the one
// serial FinalizeRebuild that sequences both formats, and the local L2 eviction its
// result feeds. Relocated verbatim from manager_owner.go, which keeps the type itself,
// its construction options, and the tombstone set.
//
// They sit together because they are ONE SEQUENCE: a rebuild stages every bucket,
// finalizes once here, and hands the superseded HNSW ids straight back to
// InvalidateLocal. The det-engine accessors that remain are the two-engine topology's
// last residue — nothing on the reset path writes that engine any more.

package segmentdist

import (
	"context"
	"log/slog"
	"slices"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
)

// Flush force-seals the sub-threshold coalescing tail of BOTH the HNSW and the
// BM25 engine for one (graphType, graphName), then — per format, AFTER that
// force-seal — ships+publishes the newly-sealed tail. Each format's ship/publish is
// gated on hasUnshippedExport()||publishRetryPending(), so a no-progress re-Flush
// (empty tail, everything already shipped, no pending retry) is a true no-op; the
// engine.Flush() force-seal itself is never gated. It is the migration's force-seal:
// a caller that Adds straight to the engine leaves a trailing buffer of fewer than
// MinSegmentDocs unsealed (and a whole graph with fewer than MinSegmentDocs indexed
// nodes produces ZERO sealed segments); Flush seals that tail so the graph becomes
// searchable. Returns the first error from either format's Flush or ship — the
// one-shot caller treats it as FATAL (unlike the best-effort pipeline path).
//
// THE EMBED PATH NO LONGER REACHES THIS PRECONDITION: its write entry points
// force-seal every batch, so Flush finds an empty buffer there and its force-seal
// is a no-op (the ship/publish gate can still open on a freshly sealed tail). The
// contract stays live for the migration/one-shot path and for direct-engine Adds.
//
// Only touches engines that already exist (managerFor/bm25ManagerFor return the
// memoized instance a prior write constructed); for a graph never added to, the
// lazily-constructed engine's buffer is empty and Flush is a no-op.
func (m *Manager) Flush(ctx context.Context, gt kgtypes.GraphType, name string) error {
	// Fail closed on an in-session account switch: shipping from a manager
	// bound to the previous account would publish it under the new tenancy.
	if err := m.checkAccountBinding(ctx); err != nil {
		return err
	}
	hnsw := m.managerFor(gt, name)
	if err := hnsw.engine.Flush(); err != nil {
		return err
	}
	// REGISTRY MODEL: embed force-seal of the sub-threshold tail, then
	// publish the resident live set as the manifest (unioned with the deterministic
	// engine's resident ids for the shared HNSW manifest) — restart-safe. The
	// ship/publish is gated AFTER the force-seal: a genuinely-sealed tail still
	// ships, but a no-progress re-Flush (empty tail, nothing unshipped, no pending
	// retry) is skipped.
	if unshipped, pending := hnsw.hasUnshippedExport(), hnsw.publishRetryPending(); unshipped || pending {
		// Re-fire observability (kept): a Flush that runs ship/publish ONLY because the
		// retry bit is pending (nothing genuinely unshipped) is the self-sustaining
		// publish-retry re-arm — logging the cause per Flush makes that cycle visible.
		slog.Debug("segmentdist: Flush ship/publish gate open",
			"graph_type", gt, "name", name, "format", hnsw.format, "unshipped", unshipped, "publish_retry_pending", pending)
		if _, err := hnsw.shipAndPublish(ctx, hnsw.locallyShipped); err != nil {
			return err
		}
	}
	bm := m.bm25ManagerFor(gt, name)
	if err := bm.engine.Flush(); err != nil {
		return err
	}
	// Same gate for the BM25 leg, after its own force-seal.
	if unshipped, pending := bm.hasUnshippedExport(), bm.publishRetryPending(); unshipped || pending {
		slog.Debug("segmentdist: Flush ship/publish gate open",
			"graph_type", gt, "name", name, "format", bm25.New().Name(), "unshipped", unshipped, "publish_retry_pending", pending)
		if _, err := bm.shipAndPublish(ctx, bm.locallyShipped); err != nil {
			return err
		}
	}
	return nil
}

// StageRebuildPartition stages ONE partition of a reset rebuild — BOTH formats, in
// one call — for the finalize to build, ship and swap. It adds nothing to any engine
// and ships nothing.
//
// ONE CALL CARRIES BOTH FORMATS, and that is a correctness device rather than tidying.
// A caller structurally cannot stage the vector share of a partition and forget the
// field share, which is precisely how the field corpus came to lag the vector corpus
// and produce the retirement defect this collapse closes. The two shares come from the
// SAME bucket grouping the driver already computed, so one call per bucket states a
// truth the caller already holds. (ReEmitRebuiltDelta takes both formats in one call
// for the delta path, so this is the established shape on this seam, not a new one.)
//
// IT STAGES RATHER THAN ADDS because a rebuild must not touch the corpus it is
// replacing until the replacement is ready. An incremental Add pollutes the SERVING
// engine one partition at a time, and the finalize then publishes the union of every
// layer that engine has held — measured live as three bm25 blobs where one was
// correct. Staging keeps both serving engines untouched until one swap per format
// replaces each layer whole.
//
// BYTE-DETERMINISM IS UNAFFECTED: it is a property of Format.Build (a fixed-seed
// serial builder over id-sorted input), not of where the documents waited, and the
// documents are carried through unmodified in the caller's order.
func (m *Manager) StageRebuildPartition(
	_ context.Context, gt kgtypes.GraphType, name string, hnswDocs, bm25Docs []searchengine.Document,
) error {
	if len(hnswDocs) == 0 && len(bm25Docs) == 0 {
		return nil
	}
	k := graphKey{graphType: gt, graphName: name}

	m.mu.Lock()
	defer m.mu.Unlock()
	staged, ok := m.rebuildWork[k]
	if !ok {
		staged = &stagedRebuild{}
		m.rebuildWork[k] = staged
	}
	if len(hnswDocs) > 0 {
		staged.hnsw = append(staged.hnsw, searchengine.BucketWork{
			Bucket: len(staged.hnsw), Docs: slices.Clone(hnswDocs),
		})
	}
	if len(bm25Docs) > 0 {
		staged.bm25 = append(staged.bm25, searchengine.BucketWork{
			Bucket: len(staged.bm25), Docs: slices.Clone(bm25Docs),
		})
	}
	return nil
}

// takeRebuildWork removes and returns a graph's staged partitions. It is taken exactly
// ONCE per finalize: a run that ends — landed, refused or failed — must not leave its
// documents staged for the next run to publish.
func (m *Manager) takeRebuildWork(gt kgtypes.GraphType, name string) *stagedRebuild {
	k := graphKey{graphType: gt, graphName: name}
	m.mu.Lock()
	defer m.mu.Unlock()
	staged, ok := m.rebuildWork[k]
	delete(m.rebuildWork, k)
	if !ok {
		return &stagedRebuild{}
	}
	return staged
}

// RebuildFinalizeResult is what one reset finalize reports back.
//
// RETIREMENT IS PER FORMAT, and that split has a measured origin. The finalize used to
// return the HNSW retired set alone, on the reasoning that only it feeds local cache
// invalidation. A live cloud run then read "0 superseded segments pruned" on its
// operator line while ALL EIGHT bm25 blobs demonstrably retired — the HNSW leg had
// already been converged by the tombstone-delta consumer, so the one number reported
// was the one format with nothing to say. An operator reading that concluded nothing
// was retired. The two corpora carry separate manifests and retire independently, so
// one number cannot describe both.
//
// REPORTING A SET IS NOT EVICTING IT. InvalidateLocal still consumes the HNSW set
// alone, which is correct and unchanged; retired BM25 blobs orphan locally until
// PruneCache reaps them. That is pre-existing and fail-safe in direction — an orphan
// wastes disk, it is never a false prune — and nothing here makes the finalize
// responsible for BM25 disk hygiene.
type RebuildFinalizeResult struct {
	HNSWSuperseded []searchengine.SegmentID
	BM25Superseded []searchengine.SegmentID
	// Swapped is true only when BOTH formats completed a manifest swap. They carry
	// SEPARATE manifests over the same nodes, so a caller told "finalized" after a
	// single-format swap would treat an unpublished corpus as durable.
	Swapped bool
}

// FinalizeRebuild is the SINGLE serial finalizer of a reset rebuild, called ONCE by
// the driver after every partition has been staged. It builds each format's staged
// layer ASIDE, ships it, gates it against the degeneracy policy, swaps it in with one
// CAS, and publishes the new resident set — per format, through one shared body.
//
// IT FINALIZES AT THE SERVING ENGINES. There is no staging engine: the build happens
// aside in memory and the swap replaces the layer the engine is serving, so the corpus
// is never half-replaced and no second engine has to be reconciled with the first
// afterwards. That is the collapse — one engine per format, and the manifest IS its
// Export.
//
// A SKIPPED PUBLISH IS NOT AN ERROR AND MUST NOT READ AS SUCCESS. Both the coverage
// gate and the agent's 409 decline with a NIL ERROR, so Swapped is read from a rise in
// each engine's landed-swap counter rather than from the absence of an error.
func (m *Manager) FinalizeRebuild(
	ctx context.Context, gt kgtypes.GraphType, name string,
) (RebuildFinalizeResult, error) {
	staged := m.takeRebuildWork(gt, name)

	hnswDM := m.managerFor(gt, name)
	hnswSuperseded, hnswSwapped, err := finalizeResetLayer(ctx, gt, name, hnswDM, staged.hnsw)
	if err != nil {
		return RebuildFinalizeResult{}, err
	}

	bm := m.bm25ManagerFor(gt, name)
	bmSuperseded, bmSwapped, err := finalizeResetLayer(ctx, gt, name, bm, staged.bm25)
	if err != nil {
		return RebuildFinalizeResult{}, err
	}

	return RebuildFinalizeResult{
		HNSWSuperseded: hnswSuperseded,
		BM25Superseded: bmSuperseded,
		Swapped:        hnswSwapped && bmSwapped,
	}, nil
}

// InvalidateLocal evicts the given superseded segment ids from the deterministic
// HNSW engine's local L2 disk cache. The driver obtains the ids from
// RebuildFinalizeResult.HNSWSuperseded (the server-side merged-away/pruned set) and
// passes them straight here so the local .seg files do not orphan until LRU —
// which never fires on an unbounded cache. A single explicit return path; no
// surfacing/discarding ambiguity.
//
// IT IS FED THE HNSW SET ONLY, and deliberately: the finalize also reports what BM25
// retired, but reporting a set is not evicting it — retired field blobs orphan locally
// until PruneCache reaps them, which is pre-existing and fail-safe in direction.
//
// IT EVICTS FROM THE SERVING ENGINE'S CACHE, which is a FIX, not a relocation. It used
// to resolve the DETERMINISTIC staging engine, and that accessor check-construct-STORES:
// the driver calls this immediately after every finalize, so a landed reset that pruned
// anything left a freshly-memoized staging engine behind — making the staging-engine
// probe read true against its own contract and rendering the OSS PruneCache orphan guard
// inert. With one engine per format there is one cache, and it is the one the reset's
// blobs were written to.
func (m *Manager) InvalidateLocal(gt kgtypes.GraphType, name string, ids []searchengine.SegmentID) {
	if len(ids) == 0 {
		return
	}
	dm := m.managerFor(gt, name)
	for _, id := range ids {
		dm.cache.Remove(id)
	}
}

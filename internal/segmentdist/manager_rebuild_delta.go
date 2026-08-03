// SPDX-License-Identifier: Apache-2.0

// manager_rebuild_delta.go — the rebuild driver's DELTA finalize: re-emit the
// partitions that own a watermark-scoped set of rebuilt documents, on the LOADED
// corpus, and publish once per format.
//
// It is a third caller of the partition machinery, distinct from the one-shot write
// entry points manager_bucket.go carries, because it has preconditions they do not:
// the engines must already hold the shipped corpus, and a run that cannot establish
// that has to say so rather than publish what it has.

package segmentdist

import (
	"context"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// ReEmitRebuiltDelta finalizes a WATERMARK-SCOPED rebuild run: it re-emits the
// partitions owning the scanned documents against the corpus the serving engines
// already hold, and publishes once per format.
//
// WHY NOT THE STAGING ENGINE. The deterministic staging engine never loads the
// existing corpus, so its Export is the scanned delta alone — finalizing a delta
// through it seals a thin, bucket-unaligned segment and appends it to the manifest
// beside every untouched bucket blob. The serving engines DO hold the shipped corpus
// (the L2-first load Search already drives), so re-emitting through them realigns the
// touched partitions and leaves the rest referenced and untouched. That is what makes
// a one-node delta change exactly one blob instead of adding one.
//
// THE THREE RETURN VALUES ARE THREE DIFFERENT QUESTIONS, and collapsing any two of
// them is a corpus-wipe bug:
//
//   - applicable=false means THIS SHAPE CANNOT BE ATTEMPTED — the engines are not
//     holding a corpus to re-emit against. The caller must fall back to a full
//     from-scratch run, re-scanning from a zero watermark.
//   - swapped=false with applicable=true means ATTEMPTED, PUBLISH DEFERRED. The
//     coverage gate or an agent 409 skipped the manifest swap, both with a NIL ERROR
//     (manager_publish_resident.go). The blobs are shipped; a later pass republishes.
//     A caller that read this as "not applicable" would fire a full re-scan on every
//     legitimately deferred publish.
//   - derivedBucketCount is the partition count the re-emit actually ran against, so
//     a caller comparing manifest cardinality across the call can tell a realignment
//     (a crossing legitimately grows the manifest) from the thin append it is
//     guarding against.
//
// THE APPLICABILITY GUARD IS NOT DEFENSIVE PADDING. On an engine that holds nothing,
// BucketCountFor(~0) derives ONE bucket, and re-emitting the delta under that count
// would collapse whatever it touched into a single partition — strictly worse than
// the thin append it replaces. residentBackstopFloor is the same threshold the
// read-side degeneracy backstop uses for the same reason.
//
// swapped is ANDed across the two formats. They carry SEPARATE manifests over the
// same nodes, so a caller told "finalized" after a BM25-only swap would treat an
// unpublished vector corpus as durable — the same two-format reading
// FinalizeRebuild uses.
func (m *Manager) ReEmitRebuiltDelta(
	ctx context.Context, gt kgtypes.GraphType, name string, hnswDocs, bm25Docs []searchengine.Document,
) (swapped, applicable bool, derivedBucketCount int, err error) {
	dm := m.managerFor(gt, name)
	bm := m.bm25ManagerFor(gt, name)
	// Idempotent: the once-guard makes this free on a daemon that has already served a
	// search for this graph, and pays the load exactly once otherwise.
	if lerr := dm.load(ctx); lerr != nil {
		return false, false, 0, lerr
	}
	if lerr := bm.load(ctx); lerr != nil {
		return false, false, 0, lerr
	}

	hnswResident, bm25Resident := dm.engine.ResidentDocCount(), bm.engine.ResidentDocCount()
	if hnswResident < residentBackstopFloor || bm25Resident < residentBackstopFloor {
		slog.Warn("segmentdist: delta re-emit NOT APPLICABLE — the serving engines hold no corpus to re-emit against (caller must re-scan from zero)",
			"graph_type", gt, "name", name, "hnsw_resident", hnswResident, "bm25_resident", bm25Resident,
			"floor", residentBackstopFloor)
		return false, false, 0, nil
	}

	// The corpus is the RESIDENT set alone: these documents are already resident,
	// sealed there by the embed writeback the scan read them from. Adding them again
	// would derive a partition count for twice the corpus that exists.
	hnswCorpus := dm.engine.DistinctResidentDocCount()
	derivedBucketCount = searchengine.BucketCountFor(hnswCorpus)

	// THE HNSW LEG PASSES THE SIBLING DIGESTS, and it depends on a lifecycle invariant
	// established elsewhere. Passing nil would make the NEXT ReEmitDirtyBuckets — which
	// unions them unconditionally — name a reference-counted-away id and take a
	// nil-error skip. Passing the union is safe only because a landed rebuild publish
	// drops the staging engine, so in the steady state this union is EMPTY and cannot
	// pin a retired layer. If that ever changes, the retired bucket ids must instead be
	// Unloaded from the staging engine in this same call.
	hnswBefore := dm.completedSwapCount()
	if rerr := replaceBucketAndPublish(ctx, dm, docIDs(hnswDocs), hnswDocs, hnswCorpus); rerr != nil {
		return false, true, derivedBucketCount, rerr
	}
	hnswSwapped := dm.completedSwapCount() > hnswBefore

	bmBefore := bm.completedSwapCount()
	if rerr := replaceBucketAndPublish(ctx, bm, docIDs(bm25Docs), bm25Docs, bm.engine.DistinctResidentDocCount()); rerr != nil {
		return false, true, derivedBucketCount, rerr
	}
	bmSwapped := bm.completedSwapCount() > bmBefore

	return hnswSwapped && bmSwapped, true, derivedBucketCount, nil
}

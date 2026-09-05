// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// manager_rebuild_layer_test.go covers what a rebuild run PUBLISHES, as opposed to
// what it builds. The existing tools-level fixture cannot reach either question:
// runRebuildOver (rebuild_segments_bucket_test.go) makes a fresh shipper per run and
// always scans the full id list, so it compares two independent FULL rebuilds and
// never sees engine state carried across runs or a watermark-scoped delta scan.
//
// Both tests here drive a real Manager and read its LOCAL state. They used to read a
// server model's per-writer manifests and refcount-GC; with no manifest and no
// server-side GC, the live layer is the engine's export and the surviving blob set is
// the L2 cache directory. The questions are the same three: what the layer holds,
// what bytes survive, and how many documents each partition carries.

// The two locked markers the phase criteria grep for alongside the FAIL line, so a
// compile error or a setup panic cannot be mistaken for the defect. They are matched
// literally — do not reword them.
const (
	layerAccumulationMarker = "FUL1058-LAYER-ACCUMULATION"
	thinAppendMarker        = "FUL1058-THIN-APPEND"
)

// bucketGroups splits docs into ascending-bucket groups under the count the corpus
// size derives, mirroring buildAndAddRebuildSegments (rebuild_segments_scan_build.go)
// so the fixture and the production build agree on membership by construction.
func bucketGroups(docs []searchengine.Document) (count int, order []int, groups map[int][]searchengine.Document) {
	// count-provenance: corpus-derived — docs is the fixture's whole corpus, which is
	// what makes this mirror buildAndAddRebuildSegments.
	count = searchengine.BucketCountFor(len(docs))
	groups = make(map[int][]searchengine.Document, count)
	for _, d := range docs {
		b := searchengine.BucketOf(d.ID, count)
		groups[b] = append(groups[b], d)
	}
	order = make([]int, 0, len(groups))
	for b := range groups {
		order = append(order, b)
	}
	sort.Ints(order)
	return count, order, groups
}

// stageRebuildRun drives ONE rebuild run's staging sequence — the production shape,
// serially in ascending bucket order through the single staging entry point, both
// formats per call, with no ship and no write to any engine. The finalize is the
// caller's, because what it returns is what most callers are measuring.
func stageRebuildRun(
	t *testing.T, ctx context.Context, mgr *Manager,
	gt kgtypes.GraphType, name string, docs []searchengine.Document,
) {
	t.Helper()
	_, order, groups := bucketGroups(docs)
	for _, b := range order {
		require.NoError(t, mgr.StageRebuildPartition(ctx, gt, name, groups[b], groups[b]))
	}
}

// segmentsByBucket maps each bucket to the segment holding it, read off the engine
// AFTER a finalize has landed.
//
// IT CANNOT BE LEARNED BY DIFFING Export PER PARTITION, which is how the Add+Seal
// helper it replaces did it: staging writes nothing to an engine, so no segment exists
// until the finalize builds every partition and swaps them in together — there is no
// per-bucket boundary left to diff across. SegmentSpans answers the question directly by
// walking each segment's members, which is also why it stays correct where arithmetic on
// a partition number does not, and reading it off the settled set makes it indifferent
// to the order ReplaceLayer landed the segments in.
func segmentsByBucket[Q, S any](
	t *testing.T, dm *distManager[Q, S], bucketCount int,
) map[int]searchengine.SegmentID {
	t.Helper()
	spans := dm.engine.SegmentSpans(bucketCount)
	out := make(map[int]searchengine.SegmentID, len(spans))
	for id, buckets := range spans {
		require.Len(t, buckets, 1,
			"segment %s spans buckets %v — a freshly built layer holds exactly one bucket per segment", id, buckets)
		out[buckets[0]] = id
	}
	require.NotEmpty(t, out, "the finalized layer must be resident, or every lookup below is vacuous")
	return out
}

// hnswLiveLayer returns the graph's LIVE hnsw layer as id -> doc count, read off the
// engine's export.
//
// IT USED TO READ A PUBLISHED SERVER MANIFEST, because "what the layer is" lived
// there and an engine's own export could not answer it. The manifest is gone and the
// engine's export IS the live layer now — the same question, asked of the authority
// that still holds the answer.
func hnswLiveLayer(mgr *Manager, gt kgtypes.GraphType, name string) map[string]int {
	out := map[string]int{}
	for _, b := range mgr.managerFor(gt, name).engine.Export() {
		if b.Format == hnswFormatName {
			out[string(b.ID)] = b.DocCount
		}
	}
	return out
}

// storedBlobIDs is the set of blob ids still ON DISK for a graph — what survived the
// reclaim the last layer swap drove.
//
// IT USED TO READ THE SERVER's blob store, which the refcount-GC pruned on publish.
// There is no server and no refcount-GC: the durable set is the L2 cache directory,
// and what prunes it is the local reclaim. The QUESTION is unchanged — did the
// superseded bytes actually go away — so this reads the surviving authority for it.
func storedBlobIDs(mgr *Manager, gt kgtypes.GraphType, name string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, id := range mgr.managerFor(gt, name).cache.Keys() {
		out[string(id)] = struct{}{}
	}
	return out
}

// TestResetRebuildPublishesOnlyThisRunsLayer is defect 1's reproduction: two rebuild
// cycles through ONE Manager, where the second must publish exactly its own layer and
// retire the first.
//
// THE DEFECT IT WAS WRITTEN AGAINST WAS ENGINE LIFECYCLE, NOT PRUNING. The rebuild wrote
// a memoized second engine that nothing ever dropped or unloaded, so cycle B's Export was
// cycle A's segments PLUS cycle B's; the publish path then computed
// dropped = reconcileAgainst − liveSet with liveSet a SUPERSET of the shipped set, so the
// dropped set came back empty — the "0 superseded segments pruned" a live reset reported
// while its coverage read 2205 of 1103. The reset builds each layer aside and REPLACES
// the resident set now, which is why this test is the cross-phase regression gate: it
// asserts the layer PROPERTY, so it holds across every mechanism that has served it.
//
// THE TWO CORPORA DERIVE DIFFERENT BUCKET COUNTS on purpose, asserted before
// anything else: it makes the fixture cover the doubling-boundary shape a live reset
// crosses, and a fixture that silently drifted into a same-count case would leave
// assertion (1) unable to distinguish a retired layer from an accumulated one. Their
// ids OVERLAP — cycle B is the same corpus grown, which is what a reset actually
// rebuilds — so the accumulated Export double-counts the shared documents and the
// doc-count assertion reads a real duplicate rather than two unrelated corpora summed.
func TestResetRebuildPublishesOnlyThisRunsLayer(t *testing.T) {
	requireMeasurementRun(t)
	t.Parallel()

	ctx := context.Background()
	// The smallest pair that crosses a BucketCountFor boundary while leaving the
	// post-fix publish clear of the coverage-ratio floor: the fixed cycle B publishes
	// 1025 resident documents against a 1125-document shipped denominator (the whole
	// blob store, cycle A included), well above residentBackstopRatio. corpusA also
	// stays above residentBackstopFloor so cycle A's own publish is gated on a live
	// ratio rather than passing through the tiny-graph disarm.
	const corpusA, corpusB = 100, 1025
	bucketsA, bucketsB := searchengine.BucketCountFor(corpusA), searchengine.BucketCountFor(corpusB)
	require.NotEqual(t, bucketsA, bucketsB,
		"the fixture must cross a bucket-count boundary: %d docs derive %d buckets, %d docs derive %d",
		corpusA, bucketsA, corpusB, bucketsB)

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	gt, name := kgtypes.GraphCode, "reset-layer"

	// --- cycle A: the prior layer -------------------------------------------------
	stageRebuildRun(t, ctx, mgr, gt, name, vecContentDocs(corpusA))
	resA, err := mgr.FinalizeRebuild(ctx, gt, name)
	require.NoError(t, err)
	swappedA := resA.Swapped
	require.True(t, swappedA, "cycle A's publish must LAND — a skipped publish also returns a nil error")

	layerA := hnswLiveLayer(mgr, gt, name)
	require.Len(t, layerA, bucketsA, "cycle A publishes one segment per bucket it built")

	// --- cycle B: the run under test ----------------------------------------------
	// NOTHING OPENS THE RUN ANY MORE, and that absence is the collapse. Staging is the
	// reset: the partitions accumulate in a work map the finalize takes once, so this
	// run's layer is its own by construction rather than because a prior step pinned
	// and dropped an engine ahead of the first Add.
	stageRebuildRun(t, ctx, mgr, gt, name, vecContentDocs(corpusB))
	resB, err := mgr.FinalizeRebuild(ctx, gt, name)
	require.NoError(t, err)
	superseded, swappedB := resB.HNSWSuperseded, resB.Swapped
	require.True(t, swappedB, "cycle B's publish must LAND — a skipped publish also returns a nil error")

	layerB := hnswLiveLayer(mgr, gt, name)

	// (1) the manifest holds THIS run's buckets, not both runs' segments.
	require.Len(t, layerB, bucketsB,
		"%s: the manifest must hold exactly cycle B's %d buckets, got %d segments (%v) — a longer set is cycle A's layer still referenced",
		layerAccumulationMarker, bucketsB, len(layerB), sortedKeys(layerB))

	// (2) the finalizer REPORTS what it retired, and everything it names is cycle A's.
	require.NotEmpty(t, superseded,
		"%s: the reset finalize must report cycle A's segments as superseded; an empty set means the new live set already contained everything the old one did",
		layerAccumulationMarker)
	for _, id := range superseded {
		require.Contains(t, layerA, string(id), "every superseded id must be one cycle A published")
	}

	// (3) and the server actually reaped them — the refcount-GC ran because no
	// manifest references them any more.
	stored := storedBlobIDs(mgr, gt, name)
	for id := range layerA {
		require.NotContains(t, stored, id,
			"%s: cycle A's segment %s must be reaped once no manifest references it", layerAccumulationMarker, id)
	}

	// (4) the coverage denominator a reader computes is THIS run's corpus. This is the
	// assertion that reproduces the live "coverage 2205 of 1103" reading directly: the
	// accumulated layer double-counts every document the two cycles share.
	summed := 0
	for _, dc := range layerB {
		summed += dc
	}
	require.Equal(t, corpusB, summed,
		"%s: the published manifest must sum to cycle B's %d documents, got %d — the excess is cycle A counted again",
		layerAccumulationMarker, corpusB, summed)
}

// TestRebuildDeltaReEmitsOwningBucketOnly is defect 2's reproduction: a one-document
// change after a full rebuild must re-emit the bucket that OWNS the document, not
// append a segment holding just it.
//
// THE DEFECT IT WAS WRITTEN AGAINST: the delta path drove the same Add / Seal /
// finalize sequence a full rebuild did, and a seal turned whatever was buffered into its
// own segment. So a one-document delta sealed a one-document segment and the publish
// named it alongside every untouched bucket blob — the manifest grew by one, nothing was
// retired, and the corpus acquired the 158-byte third segment a live one-node delta
// produced. The delta path re-emits the OWNING partition in place instead.
//
// THE CORPUS MUST DERIVE AT LEAST TWO BUCKETS, asserted up front. With a single
// bucket every change trivially touches it, so the "exactly the owning bucket
// changed" assertion would hold no matter what the delta path did.
func TestRebuildDeltaReEmitsOwningBucketOnly(t *testing.T) {
	requireMeasurementRun(t)
	t.Parallel()

	ctx := context.Background()
	// The smallest corpus deriving more than one bucket.
	const corpus = 1025
	buckets := searchengine.BucketCountFor(corpus)
	require.GreaterOrEqual(t, buckets, 2,
		"the fixture must derive at least 2 buckets or the owning-bucket assertion is vacuous (got %d)", buckets)

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	gt, name := kgtypes.GraphCode, "delta-bucket"

	docs := vecContentDocs(corpus)
	stageRebuildRun(t, ctx, mgr, gt, name, docs)
	seed, err := mgr.FinalizeRebuild(ctx, gt, name)
	require.NoError(t, err)
	swapped := seed.Swapped
	require.True(t, swapped, "the seeding rebuild's publish must LAND")

	// Which segment holds which bucket is only knowable once the finalize has built and
	// swapped the layer in — staging minted nothing.
	segByBucket := segmentsByBucket(t, mgr.managerFor(gt, name), buckets)

	before := hnswLiveLayer(mgr, gt, name)
	require.Len(t, before, buckets, "the seeding rebuild publishes one segment per bucket")

	// ONE document already in the corpus, carrying NEW content — the shape a re-embed
	// produces. Re-emitting it unchanged would encode to the same bytes and ship
	// nothing, which is correct behavior and indistinguishable from a broken path.
	changed := rewritten(docs[:1])
	owning := searchengine.BucketOf(changed[0].ID, buckets)

	swapped, applicable, derived, err := mgr.ReEmitRebuiltDelta(ctx, gt, name, changed, changed)
	require.NoError(t, err)
	require.True(t, applicable,
		"the serving engines must be holding the corpus — an inapplicable delta cannot exercise this defect at all")
	require.Equal(t, buckets, derived, "the delta must re-emit against the corpus's own partition count")
	require.True(t, swapped, "the delta's publish must LAND")

	after := hnswLiveLayer(mgr, gt, name)

	// (1) a delta re-emits, it does not grow the manifest.
	require.Len(t, after, len(before),
		"%s: a one-document delta must leave the manifest at %d segments, got %d (%v) — the extra entry is a thin appended segment",
		thinAppendMarker, len(before), len(after), sortedKeys(after))

	// (2) exactly one segment was replaced: one id retired, one id minted.
	var retired, minted []string
	for id := range before {
		if _, still := after[id]; !still {
			retired = append(retired, id)
		}
	}
	for id := range after {
		if _, had := before[id]; !had {
			minted = append(minted, id)
		}
	}
	sort.Strings(retired)
	sort.Strings(minted)
	require.Len(t, retired, 1,
		"%s: exactly ONE segment must be retired by a one-document delta, got %d (%v) — zero means the delta appended instead of re-emitting",
		thinAppendMarker, len(retired), retired)
	require.Len(t, minted, 1,
		"%s: exactly ONE segment must be minted by a one-document delta, got %d (%v)",
		thinAppendMarker, len(minted), minted)

	// (3) and it is the bucket that OWNS the changed document — pinning the SITE, not
	// just the count, since two wrong buckets changing would satisfy (2).
	require.Equal(t, string(segByBucket[owning]), retired[0],
		"the retired segment must be the one owning bucket %d (document %s)", owning, changed[0].ID)

	// (4) no published segment holds a single document. Stated as a property rather
	// than as the live 158-byte size, which is the encoder's business and would be a
	// false failure the moment it changed.
	for id, dc := range after {
		require.NotEqual(t, 1, dc,
			"%s: published segment %s holds a single document — that is the thin-append shape",
			thinAppendMarker, id)
	}
}

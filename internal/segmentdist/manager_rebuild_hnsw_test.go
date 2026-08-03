// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// manager_rebuild_hnsw_test.go is the vector-corpus twin of manager_rebuild_bm25_test.go:
// it gates the HNSW reset finalize now that it runs the same build-aside sequence the
// field leg proved first, through the same generic body.

// refieldedDocs returns copies of docs keeping their ids and vectors but carrying
// DIFFERENT field content. It is the asymmetric-change shape a per-format retirement
// test needs: the vector corpus re-encodes byte-identically and its layer is a
// content-hash no-op, while the field corpus mints new blobs and retires its old ones.
func refieldedDocs(docs []searchengine.Document) []searchengine.Document {
	out := make([]searchengine.Document, len(docs))
	for i, d := range docs {
		fields := make(map[string]string, len(d.Fields))
		for k, v := range d.Fields {
			fields[k] = v + " REVISED"
		}
		out[i] = searchengine.Document{ID: d.ID, Vector: d.Vector, Fields: fields}
	}
	return out
}

// TestHNSWResetRoutesThroughReplaceLayer asserts the HNSW reset finalizes through
// the layer REPLACEMENT primitive, and that no staging Add happens on the reset
// path at all.
//
// TWO PROPERTIES, AND EACH FAILS DIFFERENTLY. The replacement half is what makes a reset
// a reset: the prior layer must be gone from the serving engine and the resident set
// must be exactly what this run built, since an Add-shaped finalize leaves the union of
// both layers resident and publishes it — the accumulation measured live as a manifest
// holding twice its corpus. The where-it-landed half is what makes the FIRST half hold
// for the right reason: a run could publish a correct-looking layer while its blobs sat
// in some other engine's cache, which is exactly the split the two-engine topology had
// and this arc deleted.
//
// THE TWO CORPORA DERIVE DIFFERENT BUCKET COUNTS on purpose, asserted first: a same-count
// fixture cannot distinguish a replaced layer from an accumulated one by cardinality
// alone, which is the assertion doing the work here.
func TestHNSWResetRoutesThroughReplaceLayer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const corpusA, corpusB = 100, 1025
	bucketsA, bucketsB := searchengine.BucketCountFor(corpusA), searchengine.BucketCountFor(corpusB)
	require.NotEqual(t, bucketsA, bucketsB,
		"the fixture must cross a bucket-count boundary: %d docs derive %d buckets, %d docs derive %d",
		corpusA, bucketsA, corpusB, bucketsB)

	svc, view := newSegmentHarness(t)
	mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(view))
	gt, name := kgtypes.GraphCode, "hnsw-replace-route"
	target := graphSelector(gt, name)

	// Run A — the prior layer, so run B has something to replace.
	stageRebuildRun(t, ctx, mgr, gt, name, vecContentDocs(corpusA))
	resA, err := mgr.FinalizeRebuild(ctx, gt, name)
	require.NoError(t, err)
	require.True(t, resA.Swapped, "run A's publish must LAND — a skipped publish also returns a nil error")

	layerA := writerManifest(svc, target, "", hnswFormatName)
	require.Len(t, layerA, bucketsA, "run A publishes one hnsw segment per bucket it built")

	servingDM := mgr.managerFor(gt, name)
	priorResident := residentIDs(servingDM)
	require.Len(t, priorResident, bucketsA,
		"the SERVING engine holds run A's layer — the reset finalizes here, so this is what run B must replace")

	// Run B — the run under test.
	swapsBefore := servingDM.completedSwapCount()
	stageRebuildRun(t, ctx, mgr, gt, name, vecContentDocs(corpusB))
	resB, err := mgr.FinalizeRebuild(ctx, gt, name)
	require.NoError(t, err)
	require.True(t, resB.Swapped, "run B's publish must LAND")

	// (1) THE LAYER WAS REPLACED, not extended: resident is exactly run B's partitions
	// and not one id of run A's survives.
	nowResident := residentIDs(servingDM)
	require.Len(t, nowResident, bucketsB,
		"the serving engine must hold exactly run B's %d partitions — a longer set is run A's layer carried forward",
		bucketsB)
	for id := range priorResident {
		require.NotContains(t, nowResident, id,
			"run A's segment %s is STILL resident — a replacement carries nothing unsupplied forward", id)
	}

	// (2) ONE CAS. The whole layer changes hands in a single swap; a per-partition
	// finalize would raise the counter once per bucket and open a window between them in
	// which the corpus is half-replaced.
	require.Equal(t, uint64(1), servingDM.completedSwapCount()-swapsBefore,
		"the reset lands exactly ONE manifest swap on the serving engine, not one per partition")

	// (3) NO STAGING ADD — asserted where it is still observable. That there is no
	// SECOND HNSW engine is now structural (the type holds one map per format, so a
	// staging engine is not constructible), and an assertion over an absence the compiler
	// already guarantees would be vacuous. What IS observable, and what the two-engine
	// shape got wrong, is WHOSE L2 cache the reset's blobs landed in: they must be in the
	// cache the SERVING engine reads, because that is the engine now answering queries.
	for id := range nowResident {
		_, cached := servingDM.cache.Get(id)
		require.True(t, cached,
			"resident segment %s is missing from the serving engine's L2 cache — the reset wrote a different cache", id)
	}

	// (4) and the staged work was consumed, so a second finalize cannot republish it.
	leftover := mgr.takeRebuildWork(gt, name)
	require.Empty(t, leftover.hnsw, "the finalize takes its staged partitions exactly once")

	// (5) the published manifest agrees with what the engine serves — the retirement
	// reached the server rather than only the local set.
	layerB := writerManifest(svc, target, "", hnswFormatName)
	require.Len(t, layerB, bucketsB, "the published hnsw manifest holds exactly run B's partitions")
	require.NotEmpty(t, resB.HNSWSuperseded, "the finalize must REPORT what it retired")
	for _, id := range resB.HNSWSuperseded {
		require.Contains(t, layerA, string(id), "every superseded id must be one run A published")
	}
}

// TestFinalizeReportsPerFormatRetirement asserts that when the vector corpus has
// already converged and only the field corpus genuinely retires, the finalize
// result still carries that retirement.
//
// THIS IS THE CLOUD ROUND-1 SHAPE, REPRODUCED. There, the tombstone-delta consumer had
// converged HNSW seconds before the reset, so the finalize's HNSW-only return reported
// nothing retired while all eight bm25 blobs demonstrably retired and their local files
// were evicted. An operator reading that line concluded the reset had done nothing.
//
// IT IS UNSATISFIABLE AGAINST AN HNSW-ONLY RETURN, which is what makes it the catcher
// rather than a description: there is no field on such a result that could carry the
// non-empty set this asserts.
//
// THE CONVERGED HALF IS ASSERTED, NOT ASSUMED. A rebuild is convergent — identical
// documents encode to identical bytes under the same id — so re-staging the same vectors
// mints the same segment ids and retires nothing. Asserting that the HNSW set is EMPTY
// is what proves the fixture actually reached the asymmetry; without it a run that
// retired both formats would satisfy the BM25 assertion and say nothing about the defect.
func TestFinalizeReportsPerFormatRetirement(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const corpus = 1025

	svc, view := newSegmentHarness(t)
	mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(view))
	gt, name := kgtypes.GraphCode, "per-format-retire"
	target := graphSelector(gt, name)

	docs := vecContentDocs(corpus)

	// Run A — the prior layer for BOTH formats.
	stageRebuildRun(t, ctx, mgr, gt, name, docs)
	resA, err := mgr.FinalizeRebuild(ctx, gt, name)
	require.NoError(t, err)
	require.True(t, resA.Swapped, "run A's publish must LAND")

	priorBM25 := writerManifest(svc, target, "", bm25FormatName)
	require.NotEmpty(t, priorBM25, "run A must publish a bm25 layer for run B to retire")

	// Run B — the SAME vectors (a content-hash no-op for HNSW) with CHANGED field
	// content (new bm25 blobs, so the old ones retire).
	_, order, groups := bucketGroups(docs)
	for _, b := range order {
		require.NoError(t, mgr.StageRebuildPartition(ctx, gt, name, groups[b], refieldedDocs(groups[b])))
	}
	resB, err := mgr.FinalizeRebuild(ctx, gt, name)
	require.NoError(t, err)

	// THE CONVERGED LEG: the vector corpus re-minted the identical segment ids, so
	// nothing left it. This is the reading that made the old single-number line say
	// "0 superseded segments pruned".
	require.Empty(t, resB.HNSWSuperseded,
		"the vector corpus must be a content-hash no-op here, or the asymmetry this test exists for was never reached")

	// THE RETIRING LEG: the field corpus genuinely turned over, and the result SAYS SO.
	require.NotEmpty(t, resB.BM25Superseded,
		"the finalize must report the bm25 retirement — an HNSW-only return reports zero here while eight blobs retire")

	// And what it names is the prior field layer, not some unrelated set.
	priorSet := map[string]struct{}{}
	for _, id := range priorBM25 {
		priorSet[id] = struct{}{}
	}
	for _, id := range resB.BM25Superseded {
		require.Contains(t, priorSet, string(id), "every retired bm25 id must be one run A published")
	}

	// The server agrees: run A's field blobs are gone from the published manifest.
	nowBM25 := map[string]struct{}{}
	for _, id := range writerManifest(svc, target, "", bm25FormatName) {
		nowBM25[id] = struct{}{}
	}
	for _, id := range priorBM25 {
		require.NotContains(t, nowBM25, id, "run A's bm25 segment %s is still referenced after the reset", id)
	}
}

// TestInvalidateLocalEvictsFromTheServingCache is the CATCHER for the absorbed
// InvalidateLocal defect: the eviction must target the cache the SERVING engine reads,
// and must not construct a second engine on the way.
//
// THE DEFECT, as it stood. InvalidateLocal resolved its engine through the DETERMINISTIC
// staging map, and that accessor check-construct-STORES. The driver calls this
// immediately after every finalize, so every landed reset that pruned anything left a
// freshly-memoized staging engine behind — which made the staging-engine probe report
// TRUE against its own doc comment and rendered the OSS PruneCache orphan guard inert.
// The eviction itself also missed: it Removed from a cache the serving engine never
// reads, so the retired blob stayed on disk until PruneCache reaped it.
//
// IT IS DIRECTION-SAFE, WHICH IS WHY IT WAS INVISIBLE. An un-evicted blob wastes disk; it
// is never a false prune, and no test covered it.
//
// THE FIXTURE PUTS A BLOB IN THE SERVING CACHE AND ASKS FOR IT BACK. Seeding through the
// embed path is what makes the assertion meaningful: those blobs are in the serving
// engine's cache by construction, so an eviction that reached any other cache would
// leave them present and fail here. The pre-assertion is the known-positive — without it
// the absence afterwards could just mean the blob was never there.
func TestInvalidateLocalEvictsFromTheServingCache(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, view := newSegmentHarness(t)
	mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(view))
	gt, name := kgtypes.GraphCode, "invalidate-serving"

	// Seed a published corpus through the embed path: its blobs land in the SERVING
	// engine's L2 cache.
	seedShipped(t, ctx, mgr, gt, name, vecContentDocs(1024))
	servingDM := mgr.managerFor(gt, name)

	resident := servingDM.engine.Export()
	require.NotEmpty(t, resident, "the seed must leave the serving engine holding a layer")

	ids := make([]searchengine.SegmentID, 0, len(resident))
	for _, b := range resident {
		_, cached := servingDM.cache.Get(b.ID)
		require.True(t, cached,
			"KNOWN-POSITIVE: segment %s must be in the serving cache BEFORE the eviction, or the absence below proves nothing", b.ID)
		ids = append(ids, b.ID)
	}

	mgr.InvalidateLocal(gt, name, ids)

	for _, id := range ids {
		_, cached := servingDM.cache.Get(id)
		require.False(t, cached,
			"segment %s survived InvalidateLocal — the eviction reached a cache the serving engine does not read", id)
	}

	// AND IT CONSTRUCTED NO SECOND ENGINE. The graph's engine map holds exactly the one
	// instance that was already there; a call that resolved through a check-construct
	// accessor for a different map would have added another.
	mgr.mu.Lock()
	engines := len(mgr.managers)
	mgr.mu.Unlock()
	require.Equal(t, 1, engines,
		"InvalidateLocal must not construct an engine — one graph, one HNSW engine, however many times it is called")
}

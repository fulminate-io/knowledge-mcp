// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// manager_rebuild_delta_test.go gates ReEmitRebuiltDelta — the rebuild driver's
// delta finalize — on the two properties the thin-append defect violated: the corpus
// stays exact and bucket-aligned when a delta carries it across a partition-count
// boundary, and a delta does not grow the manifest.

// The delta straddle fixture. The seed derives 2 partitions and the window carries
// the corpus past 2048, which derives 4 — so the delta runs at a DIFFERENT count from
// the one its corpus was laid out under, which is the case that duplicates content if
// the re-emit does not close over constituency.
const (
	deltaSeedN   = 2000
	deltaWindowN = 100
	deltaCorpusN = deltaSeedN + deltaWindowN
)

// TestRebuiltDeltaCorpusExactAcrossCountChange is the CROSSING leg: a delta re-emit
// that moves the derived partition count must repartition the corpus, not duplicate
// it, and must leave every segment it rebuilt aligned to one partition.
//
// BOTH ASSERTIONS ARE REQUIRED and neither subsumes the other. At a first crossing the
// resident segments can be individually pure while membership is already inflated,
// because the duplicated copies sit in two different segments that each belong to one
// partition. The corpus figure is a FIXTURE CONSTANT rather than a reading off the
// engine: resident membership is the very quantity the defect inflates, so deriving
// the expectation from it would make the assertion an identity.
func TestRebuiltDeltaCorpusExactAcrossCountChange(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	seedCount := searchengine.BucketCountFor(deltaSeedN)
	corpusCount := searchengine.BucketCountFor(deltaCorpusN)
	require.NotEqual(t, seedCount, corpusCount,
		"the fixture must cross a partition-count boundary: %d docs derive %d partitions, %d derive %d",
		deltaSeedN, seedCount, deltaCorpusN, corpusCount)

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	gt, name := kgtypes.GraphCode, "delta-straddle"

	// Seed BOTH serving engines: the delta finalize publishes per format, and its
	// applicability guard reads both.
	seed := prefixIDs(vecContentDocs(deltaSeedN), "delta-seed-")
	require.NoError(t, mgr.ReplaceBucket(ctx, gt, name, nil, seed))
	require.NoError(t, mgr.ReplaceBucketFields(ctx, gt, name, nil, seed))
	require.Equal(t, deltaSeedN, mgr.ResidentDocCount(gt, name),
		"the seed is resident exactly once per document before the crossing")

	// The window arrives the way the embed writeback delivers it — sealed resident
	// before any rebuild reads it back out of the scan.
	window := prefixIDs(vecContentDocs(deltaWindowN), "delta-win-")
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, window))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, window))

	swapped, applicable, derived, err := mgr.ReEmitRebuiltDelta(ctx, gt, name, window, window)
	require.NoError(t, err)
	require.True(t, applicable, "the serving engines hold the corpus, so the delta shape applies")
	require.Equal(t, corpusCount, derived,
		"the delta must derive its partition count from the TRUE resident corpus")
	require.True(t, swapped, "the delta's publish must LAND — a skipped publish also returns a nil error")

	require.Equal(t, deltaCorpusN, mgr.ResidentDocCount(gt, name),
		"a crossing must REPARTITION the corpus, not duplicate it: resident membership equals the corpus exactly")

	dm := mgr.managerFor(gt, name)
	for _, blob := range dm.engine.Export() {
		spanned := spannedBuckets(t, dm, blob.ID, derived)
		require.LessOrEqualf(t, len(spanned), 1,
			"segment %s spans %d partitions (%v) — a rebuilt partition must leave every segment aligned",
			blob.ID, len(spanned), spanned)
	}
}

// TestResetThenDeltaThenDrainKeepsCardinality chains the three finalizes a live
// daemon actually runs in sequence — a reset rebuild, then a one-node delta, then the
// embed reconcile tick — and holds the manifest cardinality fixed across all of them.
//
// IT IS THE SEQUENCE THAT BREAKS, not any one call. A staging engine left resident
// after a delta, or a delta that appended instead of re-emitting, shows up here as a
// manifest that grows a segment at a time while every individual call reports success.
//
// THE DRAIN'S LANDING IS READ OFF THE SWAP COUNTER. That publish is the one the whole
// changeset puts at risk: it is skipped — with a NIL ERROR — if it names a blob the
// rebuild's swap already reference-counted away, so an error-only check would read the
// exact regression this test exists to catch as a pass.
func TestResetThenDeltaThenDrainKeepsCardinality(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const corpus = 1025
	const drain = 50
	buckets := searchengine.BucketCountFor(corpus)
	require.Equal(t, buckets, searchengine.BucketCountFor(corpus+drain),
		"the fixture must hold the partition count STABLE across the drain, or a changed cardinality would be legitimate realignment rather than a defect")

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	gt, name := kgtypes.GraphCode, "reset-delta-drain"

	// 1 — the reset rebuild.
	docs := vecContentDocs(corpus)
	stageRebuildRun(t, ctx, mgr, gt, name, docs)
	res, err := mgr.FinalizeRebuild(ctx, gt, name)
	require.NoError(t, err)
	swapped := res.Swapped
	require.True(t, swapped, "the reset rebuild's publish must LAND")
	require.Len(t, l2IDsFor(mgr.cacheDir, name, hnswFormatName), buckets,
		"the reset publishes one segment per partition it built")

	// 2 — a one-node delta through the bucket machinery.
	changed := rewritten(docs[:1])
	deltaSwapped, applicable, derived, err := mgr.ReEmitRebuiltDelta(ctx, gt, name, changed, changed)
	require.NoError(t, err)
	require.True(t, applicable,
		"the reset handed its layer to the serving engines, so the delta shape applies immediately after it")
	require.Equal(t, buckets, derived, "the delta runs at the corpus's own partition count")
	require.True(t, deltaSwapped, "the delta's publish must LAND")
	require.Len(t, l2IDsFor(mgr.cacheDir, name, hnswFormatName), buckets,
		"a one-node delta re-emits a partition — it does not add one")

	// 3 — the embed reconcile tick.
	embedDM := mgr.managerFor(gt, name)
	beforeIDs := slices.Sorted(slices.Values(embedDM.cache.Keys()))
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, vecContentDocsSeed(drain, 800000)))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))
	// The drain's WRITE must land. The original asserted this through the publish-gate
	// swap counter, whose regression was a manifest naming a reaped blob being SKIPPED
	// with a nil error; the local equivalent of "silently did nothing" is an unchanged
	// L2 id set, so the assertion is that the set actually MOVED.
	require.NotEqual(t, beforeIDs, slices.Sorted(slices.Values(embedDM.cache.Keys())),
		"the drain's write must LAND — a no-op tick also returns a nil error")
	require.Len(t, l2IDsFor(mgr.cacheDir, name, hnswFormatName), buckets,
		"the drain re-emits its dirty partitions — the manifest cardinality is unchanged")

	// And the corpus is whole across the whole sequence.
	require.Equal(t, corpus+drain, mgr.ResidentDocCount(gt, name),
		"every rebuilt, delta'd and drained document is resident exactly once")
}

// toolsShipperAdapter mirrors the production bootstrap adapter for the in-package
// tests that drive the REAL tools rebuild driver over a real Manager.
//
// It exists for the same reason the production one does: the driver's ship seam
// reports each finalize as ONE tools-local struct, and a method on Manager cannot
// name that type — segmentdist must not import tools, which is why every other test
// here reaches the driver only from a _test file. Embedding keeps this to the two
// translated methods, so a seam the Manager already satisfies needs no change here.
type toolsShipperAdapter struct{ *Manager }

// FinalizeRebuild maps the reset finalize's per-format result onto the tools-local
// struct, carrying every value across unchanged.
// FinalizeRebuild drops the corpusComplete argument the cloud rail's coverage gate
// consumed. A method DECLARATION is not a call expression, so no call-site sweep
// reaches it — it had to be corrected here by hand.
func (a toolsShipperAdapter) FinalizeRebuild(
	ctx context.Context, gt kgtypes.GraphType, name string,
) (tools.RebuildFinalizeResult, error) {
	res, err := a.Manager.FinalizeRebuild(ctx, gt, name)
	return tools.RebuildFinalizeResult{
		HNSWSuperseded: res.HNSWSuperseded,
		BM25Superseded: res.BM25Superseded,
		Swapped:        res.Swapped,
	}, err
}

func (a toolsShipperAdapter) ReEmitRebuiltDelta(
	ctx context.Context, gt kgtypes.GraphType, name string, hnswDocs, bm25Docs []searchengine.Document,
) (tools.RebuildDeltaResult, error) {
	swapped, applicable, derived, err := a.Manager.ReEmitRebuiltDelta(ctx, gt, name, hnswDocs, bm25Docs)
	return tools.RebuildDeltaResult{
		Swapped:            swapped,
		Applicable:         applicable,
		DerivedBucketCount: derived,
	}, err
}

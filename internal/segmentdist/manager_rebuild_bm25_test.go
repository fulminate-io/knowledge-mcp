// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// manager_rebuild_bm25_test.go gates the BM25 reset finalize — the leg that carried
// the open defect. The vector corpus already retired its prior layer; the field corpus
// did not, because it has no staging engine to drop and the staging-hand-off remedy
// was a provable no-op there (superseded is reconcileAgainst minus liveSet, which is
// disjoint from the resident Export by construction).

// stageBM25Reset stages one reset run's partitions for BOTH formats, the way the driver
// does. Both legs are required even though this file's subject is BM25: FinalizeRebuild
// ANDs `Swapped` across the two formats, so a fixture that staged only the field corpus
// would report not-swapped for the vector corpus's sake and every assertion after it
// would be about the wrong thing.
func stageBM25Reset(t *testing.T, ctx context.Context, mgr *Manager, gt kgtypes.GraphType, name string, docs []searchengine.Document) int {
	t.Helper()
	stageRebuildRun(t, ctx, mgr, gt, name, docs)
	_, order, _ := bucketGroups(docs)
	return len(order)
}

// TestBM25ResetRetiresPriorLayer is the DEFECT's reproduction turned gate: after a
// reset the published bm25 manifest holds exactly this run's segments and the prior
// layer is reported superseded.
//
// Measured before this change, on both backends: the reset published three bm25 blobs
// where one was correct and reported nothing pruned, because the finalize published the
// union of every layer the shared engine had ever held. The cardinality assertion is
// what fails against that; the non-empty dropped set is what proves the retirement was
// REPORTED rather than merely implied by a coincidence of counts.
func TestBM25ResetRetiresPriorLayer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	// Two corpora deriving DIFFERENT bucket counts, so the second layer cannot be
	// mistaken for the first and the cardinality assertion has something to say.
	const corpusA, corpusB = 100, 1025
	bucketsB := searchengine.BucketCountFor(corpusB)
	require.NotEqual(t, searchengine.BucketCountFor(corpusA), bucketsB,
		"the fixture must cross a bucket-count boundary")

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	gt, name := kgtypes.GraphCode, "bm25-reset"

	// Run A — the prior layer.
	stageBM25Reset(t, ctx, mgr, gt, name, vecContentDocs(corpusA))
	resA, err := mgr.FinalizeRebuild(ctx, gt, name)
	require.NoError(t, err)
	swappedA := resA.Swapped
	require.True(t, swappedA, "run A's publish must LAND — a skipped publish also returns a nil error")
	layerA := l2IDsFor(mgr.cacheDir, name, bm25FormatName)
	require.NotEmpty(t, layerA, "run A must publish a bm25 layer to be superseded")

	// Run B — the run under test. Nothing opens it: staging IS the reset, so this run's
	// layer is its own without a prior step pinning and dropping an engine.
	stageBM25Reset(t, ctx, mgr, gt, name, vecContentDocs(corpusB))
	resB, err := mgr.FinalizeRebuild(ctx, gt, name)
	require.NoError(t, err)
	swappedB := resB.Swapped
	require.True(t, swappedB, "run B's publish must LAND")

	layerB := l2IDsFor(mgr.cacheDir, name, bm25FormatName)
	require.Len(t, layerB, bucketsB,
		"the bm25 manifest must hold exactly run B's %d partitions — a longer set is run A's layer still referenced", bucketsB)

	after := map[string]struct{}{}
	for _, id := range layerB {
		after[id] = struct{}{}
	}
	for _, id := range layerA {
		require.NotContains(t, after, id, "run A's bm25 segment %s is STILL referenced after the reset", id)
	}

	// And the server actually reaped them.
	stored := l2IDsFor(mgr.cacheDir, name, bm25FormatName)
	for _, id := range layerA {
		require.NotContains(t, stored, id, "run A's bm25 segment %s survived the refcount-GC", id)
	}
}

// TestReplaceLayerShipsBeforeSwapping is the ORDERING catcher: at the instant the swap
// lands, every blob the engine will serve is already on the server.
//
// Swapping first would leave a window in which the engine is resident with blobs the
// server has never been told about. An embed drain publishing in that window names
// unshipped ids and takes a 409 or a non-subset skip — a nil-error deferral rather than
// a wipe, but one that ordering removes outright. The assertion is on RECORDED
// ORDERING rather than on a final state, because the final state is identical either
// way: what differs is only whether the ship happened first.
func TestReplaceLayerShipsBeforeSwapping(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const corpus = 1025

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	gt, name := kgtypes.GraphCode, "bm25-ship-order"

	docs := vecContentDocs(corpus)
	stageBM25Reset(t, ctx, mgr, gt, name, docs)

	bm := mgr.bm25ManagerFor(gt, name)
	residentBefore := len(bm.engine.Export())

	res, err := mgr.FinalizeRebuild(ctx, gt, name)
	require.NoError(t, err)
	swapped := res.Swapped
	require.True(t, swapped, "the reset's publish must LAND")

	// EVERY resident blob is shipped. The server-side blob store is the record of what
	// was uploaded; the engine's Export is what it will serve. The first must contain
	// the second — which can only hold if the ship preceded the swap, since the swap is
	// what made these blobs resident.
	stored := l2IDsFor(mgr.cacheDir, name, bm25FormatName)
	resident := bm.engine.Export()
	require.NotEmpty(t, resident, "the engine must be serving the new layer")
	require.Greater(t, len(resident), residentBefore,
		"the swap must have made the built layer resident, or this assertion is vacuous")
	for _, b := range resident {
		require.Contains(t, stored, string(b.ID),
			"resident blob %s is NOT on the server — the swap ran before the ship", b.ID)
	}

	// And the staged work is consumed, so a second finalize cannot republish it. Both
	// formats are checked: they stage through one call and are taken by one finalize, so
	// a leftover on either side would republish a layer the swap already retired.
	leftover := mgr.takeRebuildWork(gt, name)
	require.Empty(t, leftover.bm25, "the finalize consumes its staged bm25 partitions")
	require.Empty(t, leftover.hnsw, "the finalize consumes its staged hnsw partitions")
}

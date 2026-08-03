// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// newReBucketManager returns a Manager over the shared server fake, with a distinct
// graph name per leg so no leg inherits another's layout.
func newReBucketManager(t *testing.T) *Manager {
	t.Helper()
	_, gc := newSegmentHarness(t)
	return NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
}

// TestReBucketTriggerFiresOnlyWhenADoublingBehind pins the rule at both of its
// edges: it must fire a full doubling behind, and stay silent both one segment short
// of that and on a corpus that shrank under its layout.
//
// EVERY LEG ASSERTS ITS OPERANDS BEFORE THE BOOLEAN. Without that a fire is not
// attributable to a doubling and a silence is not attributable to the rule — the leg
// would be reporting on whatever layout it happened to build.
//
// The construction lever is replaceBucketGroups called directly with the corpus size
// the layout was derived from. That argument is documented as the size the partition
// count derives FROM, so passing the corpus AS IT WAS while supplying the documents
// the graph HAS constructs exactly the state the detector must recognize — a layout
// derived when the corpus was smaller — with no synthetic engine surgery.
//
// THE CATCHERS: leg (a) goes red if the detector is never wired or reads the wrong
// operand; legs (b) and (c) go red the moment the rule is weakened to
// candidate != current.
func TestReBucketTriggerFiresOnlyWhenADoublingBehind(t *testing.T) {
	t.Parallel()

	gt := kgtypes.GraphCode

	t.Run("fires a full doubling behind", func(t *testing.T) {
		const name = "rebucketFires"
		mgr := newReBucketManager(t)
		dm := mgr.managerFor(gt, name)

		// 2049 documents laid out at the count a 1024-document corpus derives: ONE
		// segment holding a corpus that now derives four.
		docs := prefixIDs(hnswVecDocs(2049), "fire-a-")
		_, err := replaceBucketGroups(dm, nil, docs, nil, 1024)
		require.NoError(t, err)

		require.Len(t, dm.engine.ResidentSegmentIDs(), 1,
			"fixture: the corpus must sit in exactly ONE segment, or this leg is not a doubling behind")
		require.Equal(t, 4, searchengine.BucketCountFor(dm.engine.DistinctResidentDocCount()),
			"fixture: the resident corpus must derive FOUR partitions against that one segment")

		candidate, current, needed := mgr.ReBucketNeeded(gt, name)
		require.True(t, needed,
			"a layout a full doubling behind must fire: candidate %d against current %d", candidate, current)
		require.Equal(t, 4, candidate)
		require.Equal(t, 1, current)
	})

	t.Run("silent on the nearest miss", func(t *testing.T) {
		const name = "rebucketNearMiss"
		mgr := newReBucketManager(t)
		dm := mgr.managerFor(gt, name)

		// Two partition segments from a layout derived at 2048, plus one freshly
		// sealed tail: three segments against a corpus deriving four.
		docs := prefixIDs(hnswVecDocs(2049), "miss-b-")
		_, err := replaceBucketGroups(dm, nil, docs, nil, 2048)
		require.NoError(t, err)
		tail := prefixIDs(hnswVecDocs(8), "miss-b-tail-")
		require.NoError(t, mgr.AddAndMarkDirty(context.Background(), gt, name, tail))

		require.Len(t, dm.engine.ResidentSegmentIDs(), 3,
			"fixture: two partition segments plus one sealed tail")
		require.Equal(t, 4, searchengine.BucketCountFor(dm.engine.DistinctResidentDocCount()),
			"fixture: the resident corpus must derive FOUR partitions")

		candidate, current, needed := mgr.ReBucketNeeded(gt, name)
		require.False(t, needed,
			"three segments against a derived four is ONE SHORT of a doubling and must stay silent: candidate %d, current %d", candidate, current)

		// This leg pins the accepted cost as much as the rule. At current 2 the SAME
		// corpus would fire, and one segment is the whole difference — so an
		// implementer who "fixes" this leg to expect true is changing the design's
		// scope, not repairing a test.
		require.Equal(t, 4, candidate)
		require.Equal(t, 3, current)
	})

	t.Run("silent on a shrunk over-partitioned corpus", func(t *testing.T) {
		const name = "rebucketShrunk"
		mgr := newReBucketManager(t)
		dm := mgr.managerFor(gt, name)

		// 1024 documents spread over the eight partitions an 8192-document corpus
		// derives: the layout is FINER than the corpus now needs.
		docs := prefixIDs(hnswVecDocs(1024), "shrunk-c-")
		_, err := replaceBucketGroups(dm, nil, docs, nil, 8192)
		require.NoError(t, err)

		require.Len(t, dm.engine.ResidentSegmentIDs(), 8,
			"fixture: the corpus must be spread over EIGHT segments")
		require.Equal(t, 1, searchengine.BucketCountFor(dm.engine.DistinctResidentDocCount()),
			"fixture: the resident corpus must derive ONE partition against those eight segments")

		candidate, current, needed := mgr.ReBucketNeeded(gt, name)
		require.False(t, needed,
			"a down-crossing must stay silent — over-partitioning costs only fan-out: candidate %d, current %d", candidate, current)
		require.Equal(t, 1, candidate)
		require.Equal(t, 8, current)
	})
}

// TestReBucketTriggerSuppressedDuringPartialRealignment is the STORM CATCHER: the
// two shapes a graph passes through in ordinary operation that a looser rule would
// fire on every tick.
//
// BOTH LEGS ARE BUILT THROUGH THE REAL DRAIN rather than a synthesized state,
// because the storm they prevent is a real drain state. Both go red against a
// candidate != current rule — which is the single most likely weakening of this
// detector, and the one that turns it into a per-tick rebuild storm.
func TestReBucketTriggerSuppressedDuringPartialRealignment(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gt := kgtypes.GraphCode

	t.Run("mid-realignment", func(t *testing.T) {
		const name = "rebucketMidRealign"
		mgr := newReBucketManager(t)

		// Seed an aligned corpus at four partitions, then grow it through a window
		// CONFINED to one of them so the drain realigns only what it reached.
		seed := prefixIDs(hnswVecDocs(2049), "mid-seed-")
		require.NoError(t, mgr.ReplaceBucket(ctx, gt, name, nil, seed))
		window := docsInBucket(t, 0, 4, 2048, "mid-win-")
		require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, window))
		require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

		candidate, current, needed := mgr.ReBucketNeeded(gt, name)
		// THE VACUITY GUARD. Silence over an already-converged layout is satisfied by
		// every possible rule and proves nothing, so the fixture must be shown to be
		// genuinely mid-realignment before the silence means anything.
		require.NotEqual(t, current, candidate,
			"fixture: the layout must be genuinely mid-realignment (candidate %d, current %d) or the silence below is vacuous", candidate, current)
		require.False(t, needed,
			"a graph the drain is already converging must not be rebuilt from under it: candidate %d, current %d", candidate, current)
	})

	t.Run("transient thin segment", func(t *testing.T) {
		const name = "rebucketThinTail"
		mgr := newReBucketManager(t)

		seed := prefixIDs(hnswVecDocs(2049), "thin-seed-")
		require.NoError(t, mgr.ReplaceBucket(ctx, gt, name, nil, seed))

		// PRECONDITION: the layout starts ALIGNED, so the tail below is the only thing
		// that moves it.
		candidate, current, needed := mgr.ReBucketNeeded(gt, name)
		require.Equal(t, 4, candidate)
		require.Equal(t, 4, current)
		require.False(t, needed, "fixture: an aligned layout must be silent before the tail lands")

		dm := mgr.managerFor(gt, name)
		distinctBefore := dm.engine.DistinctResidentDocCount()

		// One thin batch, sealed into exactly one segment however few documents it
		// carries, and NOT drained — the state every write leaves between ticks.
		require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, prefixIDs(hnswVecDocs(8), "thin-tail-")))

		// ASSERT THE TAIL ACTUALLY LANDED, or this leg is silence about a write that
		// never happened.
		require.Len(t, dm.engine.ResidentSegmentIDs(), current+1,
			"fixture: the thin batch must seal exactly ONE new segment")
		require.Equal(t, distinctBefore+8, dm.engine.DistinctResidentDocCount(),
			"fixture: the thin batch's eight documents must be resident")

		tailCandidate, tailCurrent, tailNeeded := mgr.ReBucketNeeded(gt, name)
		require.False(t, tailNeeded,
			"a sealed tail moves current UP and candidate not at all — the direction that suppresses: candidate %d, current %d", tailCandidate, tailCurrent)
	})
}

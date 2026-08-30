// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// TestPerFormatDegeneracyUsesTheEmbeddedDenominator is the TRAP GATE for the
// per-format degeneracy verdict's move out of segmentdist, which lost its shipped
// denominator, into bootstrap, which still holds the embedded one.
//
// IT PINS THE TWO PROPERTIES (A) COULD SILENTLY LOSE, plus the anti-vacuity leg.
//
// (1) THE DENOMINATOR IS THE SERVER EMBEDDED COUNT, not a local one. A graph whose
// resident count sits below the ratio of its embedded-node count is degenerate;
// raise the denominator's satisfaction and it is not. A LOCAL denominator cannot
// produce that pair: an L2 read carries no per-segment doc count at all — segment
// identity is a content hash, and nothing synthesizes a count beside it — so any
// L2-derived denominator is zero, which forces the disarm path and makes the verdict
// permanently false — the dead term step 3.3 forbids. This leg is what distinguishes
// a live verdict from a dead one.
//
// (2) THE PER-FORMAT DIMENSION SURVIVES. armObservation is per-FORMAT while
// healNeedsRebuildLocal is per-GRAPH and HNSW-only, so an (A) implementation that
// collapsed the formats to a single verdict would pass leg (1) and silently blind
// client_segment_bm25_gate.go, whose entire reason for existing is asking the BM25
// arm. The two arms must be able to DISAGREE.
//
// (3) AN EVICTED POOL YIELDS NO DEGENERATE VERDICT and is not re-materialized by the
// call. Its resident count reads 0 while every byte is still on disk, so an
// implementation that fed that zero to the predicate would storm a from-scratch
// rebuild on every eviction — undoing the eviction at the highest possible cost.
func TestPerFormatDegeneracyUsesTheEmbeddedDenominator(t *testing.T) {
	// opCtx, NOT a bare Background: these subtests call the heal deciders DIRECTLY
	// rather than through the reconcile pass, and the pass is what normally stamps the
	// operation. Stats is a covered RPC, so an unstamped ctx makes the embedded-count
	// read ERROR — and a decider that cannot read its denominator declines, which
	// would look exactly like the verdict some of these subtests assert.
	ctx := opCtx()

	t.Run("the embedded count is the denominator, and it can swing the verdict", func(t *testing.T) {
		// A large embedded corpus against a small resident one: degenerate.
		c, eng := buildOSSHealClient(t, 4096, "denomRepo")
		gt, name := kgtypes.GraphCode, "denomRepo"
		seedBothArms(t, ctx, c, gt, name, 96)

		obs, err := c.segmentMgr.ResidentObservationsByFormat(ctx, gt, name)
		require.NoError(t, err)
		require.NotEmpty(t, obs)

		embedded, err := tools.GraphEmbeddedCount(ctx, c.GraphCaller(), gt, name)
		require.NoError(t, err)
		require.Equal(t, 4096, embedded, "the denominator must come from the SERVER node store")

		hnswObs := observationFor(t, obs, hnsw.New().Name())
		require.Positive(t, hnswObs.ResidentAfterLoad,
			"the arm must have measured a real resident count, or the comparison below is vacuous")
		require.True(t, degenerateAgainstEmbedded(hnswObs.ResidentAfterLoad, embedded),
			"resident %d against embedded %d is below the ratio — degenerate",
			hnswObs.ResidentAfterLoad, embedded)

		// SAME resident set, denominator lowered to what the corpus actually covers:
		// the SAME arm is now healthy. Only the server-side operand changed, which is
		// what proves the denominator is doing the work.
		eng.embedded = int32(hnswObs.ResidentAfterLoad)
		embedded, err = tools.GraphEmbeddedCount(ctx, c.GraphCaller(), gt, name)
		require.NoError(t, err)
		require.False(t, degenerateAgainstEmbedded(hnswObs.ResidentAfterLoad, embedded),
			"resident %d against embedded %d fully covers the corpus — not degenerate",
			hnswObs.ResidentAfterLoad, embedded)
	})

	t.Run("HNSW and BM25 can DISAGREE — the per-format dimension survives", func(t *testing.T) {
		c, _ := buildOSSHealClient(t, 4096, "perFormatRepo")
		gt, name := kgtypes.GraphCode, "perFormatRepo"

		// Populate the HNSW arm richly and the BM25 arm not at all, so the two arms'
		// resident counts genuinely differ against ONE shared denominator.
		docs := armVerdictFixtureDocs(4096)
		require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, gt, name, docs))
		require.NoError(t, c.segmentMgr.ReEmitDirtyBuckets(ctx, gt, name))

		obs, err := c.segmentMgr.ResidentObservationsByFormat(ctx, gt, name)
		require.NoError(t, err)
		embedded, err := tools.GraphEmbeddedCount(ctx, c.GraphCaller(), gt, name)
		require.NoError(t, err)

		hnswObs := observationFor(t, obs, hnsw.New().Name())
		bm25Obs := observationFor(t, obs, bm25.New().Name())

		hnswDegenerate := degenerateAgainstEmbedded(hnswObs.ResidentAfterLoad, embedded)
		bm25Degenerate := degenerateAgainstEmbedded(bm25Obs.ResidentAfterLoad, embedded)

		require.False(t, hnswDegenerate,
			"the HNSW arm holds the whole corpus (resident %d vs embedded %d) — healthy",
			hnswObs.ResidentAfterLoad, embedded)
		require.True(t, bm25Degenerate,
			"the BM25 arm holds nothing (resident %d vs embedded %d) — degenerate",
			bm25Obs.ResidentAfterLoad, embedded)
		require.NotEqual(t, hnswDegenerate, bm25Degenerate,
			"THE TWO ARMS MUST BE ABLE TO DISAGREE: an implementation that collapsed the formats to one "+
				"per-graph verdict would blind the BM25 gate entirely")
	})

	t.Run("the CONSUMERS act on the split — BM25 heals while HNSW declines", func(t *testing.T) {
		// THE PRECEDING SUBTEST PROVES THE PREDICATE SPLITS; THIS ONE PROVES THE
		// PRODUCTION DECIDERS ACT ON THE SPLIT. Without it, both arms could compute
		// correct verdicts that no caller ever consulted — which is exactly the state
		// left by deleting the cloud branch that was healNeedsRebuildBM25's only
		// route: the arm still computed, and nothing called it.
		c, _ := buildOSSHealClient(t, 4096, "consumerRepo")
		gt, name := kgtypes.GraphCode, "consumerRepo"
		t.Cleanup(resetBM25HealProgress)

		// HNSW holds the whole corpus; BM25 holds a REMNANT — a handful of documents
		// against 4096 embedded nodes.
		//
		// WHY A REMNANT AND NOT NOTHING, which is what this fixture used to build.
		// healNeedsRebuildBM25's FIRST check is PRESENCE, and it declines outright for a
		// graph holding no BM25 corpus at all: that population recovers through ordinary
		// indexing traffic, and rebuilding it here would fire for every such graph on the
		// first tick. An empty-BM25 fixture therefore exercises the presence decline, not
		// the ratio, and asserting a rebuild against it asserts the opposite of what
		// production does. A remnant is the shape the gate is FOR — a corpus that exists
		// and has collapsed.
		docs := armVerdictFixtureDocs(4096)
		require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, gt, name, docs))
		require.NoError(t, c.segmentMgr.AddAndMarkDirtyFields(ctx, gt, name, docs[:8]))
		require.NoError(t, c.segmentMgr.ReEmitDirtyBuckets(ctx, gt, name))
		require.Positive(t, c.segmentMgr.CachedSegmentCount(gt, name, bm25.New().Name()),
			"FIXTURE CONTROL: the BM25 arm must actually HOLD a corpus, or the gate declines on "+
				"presence and the rebuild assertion below would be measuring the wrong branch")

		// The HNSW arm alone DECLINES — it is healthy, so on its own it would end the
		// decision and the collapsed field corpus would never be looked at.
		hnswNeeds, err := c.healNeedsRebuildLocal(ctx, gt, name)
		require.NoError(t, err)
		require.False(t, hnswNeeds,
			"the HNSW arm covers the corpus, so it declines — this is the 2026-07-27 shape: "+
				"a healthy vector arm masking a collapsed field arm")

		// The BM25 gate, asked directly, says REBUILD.
		bm25Needs, err := c.healNeedsRebuildBM25(ctx, gt, name, nil)
		require.NoError(t, err)
		require.True(t, bm25Needs,
			"the BM25 arm holds no corpus against 4096 embedded nodes — it must ask for a rebuild")

		// AND THE COMPOSITE DECISION AGREES WITH THE BM25 ARM. This is the assertion
		// the re-route exists to make true: before it, healNeedsRebuild was a one-line
		// delegate to the HNSW arm and would have returned false here, leaving the
		// collapsed field corpus with no trigger at all.
		composite, err := c.healNeedsRebuild(ctx, gt, name)
		require.NoError(t, err)
		require.True(t, composite,
			"healNeedsRebuild must surface the BM25 arm's verdict when the HNSW arm is satisfied; "+
				"a false here means the field corpus can never auto-heal")
	})

	t.Run("an EVICTED pool yields no degenerate verdict and is not re-materialized", func(t *testing.T) {
		c, gt, name, _ := evictedArmFixture(t)

		obs, err := c.segmentMgr.ResidentObservationsByFormat(ctx, gt, name)
		require.NoError(t, err)
		require.NotEmpty(t, obs)

		for _, o := range obs {
			require.True(t, o.Evicted,
				"arm %s must report Evicted — its pool was unloaded, not measured", o.Format)
		}
		require.True(t, c.segmentMgr.PoolEvicted(gt, name),
			"the observation probe must not have resurrected the pool")

		// The consumer contract: an evicted arm contributes NOTHING to the verdict.
		// Applying the predicate to its zero would return true (resident 0 against a
		// non-empty embedded corpus) and drive a rebuild — which is exactly why every
		// consumer branches on Evicted BEFORE reaching the predicate.
		require.True(t, degenerateAgainstEmbedded(0, 4096),
			"PRECONDITION: a zero resident against a populated corpus IS degenerate — which is precisely "+
				"why an evicted arm's zero must never reach this predicate")
	})
}

// observationFor selects one format's observation, failing loudly when the arm is
// absent rather than returning a zero value that would read as a measurement.
func observationFor(t *testing.T, obs []segmentdist.ArmObservation, format string) segmentdist.ArmObservation {
	t.Helper()
	for _, o := range obs {
		if o.Format == format {
			require.NoError(t, o.Err, "arm %s must have been measured cleanly", format)
			return o
		}
	}
	t.Fatalf("no observation for format %q", format)
	return segmentdist.ArmObservation{}
}

// seedBothArms writes n documents into BOTH format arms and makes them resident.
func seedBothArms(t *testing.T, ctx context.Context, c *client, gt kgtypes.GraphType, name string, n int) {
	t.Helper()
	docs := armVerdictFixtureDocs(n)
	require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, c.segmentMgr.AddAndMarkDirtyFields(ctx, gt, name, docs))
	require.NoError(t, c.segmentMgr.ReEmitDirtyBuckets(ctx, gt, name))
}

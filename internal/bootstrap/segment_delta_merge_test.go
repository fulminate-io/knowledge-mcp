// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestMergeLandsCoWorkerDeltaAndAdvancesWatermark is the currency path end to end: a
// window of LIVE items another machine wrote reaches this machine's local segments,
// and the durable horizon advances ONLY after the drain that made the merge durable.
//
// THE TWO-PART COMMIT IS THE SUBJECT OF THE SECOND HALF. A horizon advanced before
// the drain would move past a window whose documents never shipped, and the next pass
// would start after them — the one way this design can silently lose a co-worker's
// update. So the failing-drain case asserts the horizon did NOT move, which is what
// makes the window re-pullable.
func TestMergeLandsCoWorkerDeltaAndAdvancesWatermark(t *testing.T) {
	ctx := opCtx()
	const (
		repo     = "mergeLandsRepo"
		corpusN  = 128 // clears the resident backstop floor
		embedded = 100 // resident 128 >= 0.5*100, so the graph is HEALTHY
		// The horizon an earlier landed merge left. Without one the pass pulls nothing
		// at all, which is TestMergeSkippedUntilWatermarkSeeded's subject, not this
		// test's.
		seededHorizon = int64(1_600_000_000_000_000_000)
		// What the server serves THIS window up to.
		windowHorizon = int64(1_700_000_000_123_456_789)
	)

	c, eng := buildReconcileClientWith(t, embedded, repo)
	require.NoError(t, c.segmentMgr.SaveMergeWatermark(kgtypes.GraphCode, repo, seededHorizon))
	eng.setServedHorizon(windowHorizon)

	// A local corpus that does NOT contain the co-worker's id, so its arrival in the
	// searchable pool can only have come through the merge.
	docs := fastloadVecDocs(repo, corpusN)
	require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, docs))
	require.NoError(t, c.segmentMgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, repo))

	coWorker := docs[0]
	coWorkerID := "co-worker-" + coWorker.ID
	require.False(t, hitsContain(searchHits(t, c, ctx, repo, coWorker.Vector), coWorkerID),
		"PRECONDITION: the co-worker's id must NOT be searchable before the merge, or its presence afterwards means nothing")

	// THE CO-WORKER'S WRITE, arriving only as a LIVE item on the delta feed — the half
	// this path used to discard.
	eng.mu.Lock()
	eng.scanItems[repo] = []*knowledgev1.PipelineScanItem{
		{NodeId: coWorkerID, GraphName: repo, BinaryVector: coWorker.Vector},
	}
	eng.mu.Unlock()

	c.reconcileSegmentCoverage(ctx)

	require.Positive(t, eng.deltaScanCallCount(repo),
		"the bounded delta read is what must have carried the co-worker's write")
	require.True(t, hitsContain(searchHits(t, c, ctx, repo, coWorker.Vector), coWorkerID),
		"a co-worker's LIVE item must reach this machine's searchable segments — that is the whole currency path")

	// The drain succeeded, so part two of the commit ran and the horizon is durable.
	persisted, err := c.segmentMgr.LoadMergeWatermark(kgtypes.GraphCode, repo)
	require.NoError(t, err)
	require.Equal(t, windowHorizon, persisted,
		"a landed merge advances the durable horizon to the horizon the server served the window")

	// THE FAILING-DRAIN HALF. A second window against a graph whose drain fails must
	// leave the horizon where it is, so the window is re-pulled rather than skipped.
	const laterHorizon = int64(1_800_000_000_000_000_000)
	eng.setServedHorizon(laterHorizon)
	eng.setScanErr(errors.New("injected: the drain's ship path is down"))

	c.reconcileSegmentCoverage(ctx)

	stillPersisted, err := c.segmentMgr.LoadMergeWatermark(kgtypes.GraphCode, repo)
	require.NoError(t, err)
	require.Equal(t, windowHorizon, stillPersisted,
		"a pass that could not land its window must NOT advance the horizon — otherwise the next pass starts past documents that never shipped")
	require.NotEqual(t, laterHorizon, stillPersisted,
		"known-negative: the later horizon is what a commit-before-drain implementation would have written")
}

// TestMergeSkippedUntilWatermarkSeeded is the LOAD half of the design, and the half
// most likely to be quietly dropped because the code reads perfectly well without it
// — it just costs a full-corpus read per graph per process.
func TestMergeSkippedUntilWatermarkSeeded(t *testing.T) {
	ctx := opCtx()
	const (
		repo     = "mergeSeedRepo"
		corpusN  = 128
		embedded = 100
		// What the server serves the backstop's unwatermarked scan up to. It is what
		// must seed this graph's merge horizon.
		scanHorizon = int64(1_700_000_000_123_456_789)
	)

	c, eng := buildReconcileClientWith(t, embedded, repo)
	eng.setServedHorizon(scanHorizon)

	docs := fastloadVecDocs(repo, corpusN)
	require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, docs))
	require.NoError(t, c.segmentMgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, repo))

	// 1 — NO HORIZON OF ANY KIND, SO NO PULL. The assertion is on the CALL COUNT, not
	// on a log line: a zero-watermark read of this axis is the full vectored corpus,
	// and paying one per graph per process is exactly the load this rule removes.
	horizon, err := c.segmentMgr.LoadMergeWatermark(kgtypes.GraphCode, repo)
	require.NoError(t, err)
	require.Zero(t, horizon, "PRECONDITION: the graph starts with no merge horizon")
	w, _, err := c.segmentMgr.LoadRebuildState(kgtypes.GraphCode, repo)
	require.NoError(t, err)
	require.Zero(t, w, "PRECONDITION: and no rebuild watermark either")

	// ONE PASS, and the number is DERIVED rather than observed. The backstop grants ONE
	// graph the repair slot per pass and this fixture walks two — the code repo and
	// knowledge/default — in the working set's (graph type, name) order, which puts the
	// code repo FIRST. So pass 1 offers the repo the slot, declines it and seeds its
	// horizon, and the pull under test has already had its chance by then: the delta
	// read runs ahead of the backstop within the same pass, against a graph that at
	// that moment still had no horizon of any kind. A second pass here would find the
	// graph seeded and pull, which is step 3's subject rather than this one's. If a
	// fixture change moves this, RE-DERIVE it the same way rather than raising it to
	// whatever the run prints.
	c.reconcileSegmentCoverage(ctx)
	require.Zero(t, eng.deltaScanCallCount(repo),
		"a graph with no horizon of any kind must pull NOTHING — a zero-watermark window is the whole corpus")

	// 2 — THE BACKSTOP IS WHAT SEEDS IT. The passes above also ran the coverage
	// backstop, whose unwatermarked scan is served a horizon; that horizon is the
	// correct start for this graph's first delta window, because everything at or
	// before it was just examined by a full scan.
	seeded, err := c.segmentMgr.LoadMergeWatermark(kgtypes.GraphCode, repo)
	require.NoError(t, err)
	require.Equal(t, scanHorizon, seeded,
		"the backstop pass must persist the horizon its scan was served, or the graph never acquires one")

	// 3 — THE NEXT PASS PULLS FROM EXACTLY THAT HORIZON. This is the catcher for
	// seeding from time.Now(), which would silently drop every row stamped inside the
	// skew window and is invisible to every other assertion here.
	c.reconcileSegmentCoverage(ctx)
	require.Equal(t, 1, eng.deltaScanCallCount(repo),
		"the seeded graph pulls exactly one window on the next pass")
	require.Equal(t, []int64{scanHorizon}, eng.deltaSinceArgs(repo),
		"the window must start from the persisted horizon — not zero, and not the client's clock")

	// 4 — THE CONVERGED GRAPH, which the other three assertions never reach. A graph
	// whose fixture is converged returns at the backstop's band gate and never runs a
	// scan at all, so without a seed on THAT path its no-pull would be permanent
	// rather than one rotation — and converged graphs are most of a healthy corpus.
	t.Run("converged_graph_seeded_at_step4", func(t *testing.T) {
		const (
			convRepo = "mergeSeedConvergedRepo"
			// covered >= embedded: the arm declines at the band gate without looking at
			// anything, which is the path under test.
			convEmbedded = 10
			convCorpusN  = 128
		)
		cc, ceng := buildReconcileClientWith(t, convEmbedded, convRepo)
		ceng.setServedHorizon(scanHorizon)

		cdocs := fastloadVecDocs(convRepo, convCorpusN)
		require.NoError(t, cc.segmentMgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, convRepo, cdocs))
		require.NoError(t, cc.segmentMgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, convRepo))

		// One pass for the same derived rotation reason as the parent body: the code
		// repo sorts ahead of knowledge/default in the working set, so it is offered
		// the slot on the first pass.
		cc.reconcileSegmentCoverage(ctx)

		// A merge horizon WAS written for it, even though nothing scanned it.
		convHorizon, err := cc.segmentMgr.LoadMergeWatermark(kgtypes.GraphCode, convRepo)
		require.NoError(t, err)
		require.Equal(t, scanHorizon, convHorizon,
			"a graph the arm DECLINES must still acquire a horizon, or its no-pull is permanent")

		// And a repair record marking it converged — but NOT scanned.
		st, err := cc.segmentMgr.LoadRepairState(kgtypes.GraphCode, convRepo)
		require.NoError(t, err)
		require.True(t, st.Converged, "the arm has nothing to do for this graph, which is what the gate wants to know")
		require.False(t, st.Scanned,
			"NOTHING EXAMINED THIS GRAPH — the seed records a decline, not a verification, and the coverage column keys 'verified' on exactly this bit")

		// The horizon probe is guarded per graph per process: without the guard a graph
		// whose record write kept failing would re-issue it on every rotation forever.
		// The counter is the instrument — without it this degrades to "a horizon was
		// eventually written", which a probe re-issued forever satisfies just as well.
		require.Equal(t, 1, ceng.horizonSeedCallCount(convRepo),
			"the seed's horizon probe fires exactly once for the graph")
		cc.reconcileSegmentCoverage(ctx)
		cc.reconcileSegmentCoverage(ctx)
		require.Equal(t, 1, ceng.horizonSeedCallCount(convRepo),
			"and stays at one across later rotations — the once-per-process guard")
	})
}

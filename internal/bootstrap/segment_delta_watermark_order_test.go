// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// segment_delta_watermark_order_test.go gates the ARM of the merge's two-part
// commit that nothing else reaches: a window that PULLED but whose DRAIN failed.
//
// WHAT WAS AND WAS NOT ALREADY COVERED, stated precisely because the distinction
// is the whole reason this file exists. The commit at
// client_segment_reconcile_graph.go is
//
//	if err := c.segmentMgr.ReEmitDirtyBuckets(...); err != nil { slog.Warn(...) }
//	else { c.commitMergeWatermark(g, pending) }
//
// TestMergeLandsCoWorkerDeltaAndAdvancesWatermark exercises the ELSE branch, and
// its own "FAILING-DRAIN HALF" does NOT exercise the if-branch: that half pulls
// eng.setScanErr, which fails the SCAN, so consumeSegmentDelta returns Pull=false
// and commitMergeWatermark exits at its very first guard. The watermark is held
// there by the PULL-failed path, never by the DRAIN-failed one. This test holds it
// by the drain.

// switchAccountMidSession installs a process-wide account selection DIFFERENT from
// the one the client's Manager was constructed under, and returns nothing — the
// restore is registered on t.
//
// THIS IS THE ONE SURVIVING LEVER for forcing a ReEmitDirtyBuckets error, and it
// is deliberately the drain's OWN FIRST GATE: ReEmitDirtyBuckets opens with
// checkAccountBinding, which fails closed once the live selection has moved off
// the Manager's bound account.
//
// IT DOES NOT DISTURB THE PULL, which is what makes it a valid lever here rather
// than a second spelling of the scan-failure case. checkAccountBinding is consulted
// at exactly three sites — Search, the rebuild entry point, and ReEmitDirtyBuckets
// — and none of them is on the delta window's pull path: MergeSegmentDelta reads
// through the scanner and lands its live half through AddAndMarkDirty, neither of
// which consults the guard. The test asserts that separation rather than assuming
// it, below.
func switchAccountMidSession(t *testing.T) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(cfgPath, []byte("[default]\nprovider = \"anthropic\"\nmodel = \"claude-haiku-5\"\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	require.NoError(t, config.WriteSelectedAccountID(cfgPath, "acct_01SWITCHED"))
	t.Cleanup(auth.SetSelectedAccountForTest(auth.NewAccountSelection(cfgPath, time.Second)))
}

// TestFailedReEmitLeavesMergeWatermarkUnadvanced pins part TWO of the merge's
// two-part commit against the failure the design exists to survive: a window whose
// documents were pulled and merged in memory but whose DRAIN could not ship them.
//
// A horizon advanced on that branch would move past documents that never became
// durable, and the next pass would start AFTER them — the one way this design can
// silently lose a co-worker's update. Holding the horizon is what makes the window
// re-pullable, and the re-pull is safe because the merge's add is keyed by id.
func TestFailedReEmitLeavesMergeWatermarkUnadvanced(t *testing.T) {
	ctx := opCtx()
	const (
		repo     = "failedReEmitRepo"
		corpusN  = 128 // clears the resident backstop floor
		embedded = 100 // resident 128 >= 0.5*100, so the graph is HEALTHY
		// The horizon an earlier landed merge left. Without one the pass pulls
		// nothing at all and every assertion below would be vacuous.
		seededHorizon = int64(1_600_000_000_000_000_000)
		// What the server serves the FIRST (succeeding) window up to.
		windowHorizon = int64(1_700_000_000_123_456_789)
		// And the SECOND (failing-drain) window. It must never be persisted.
		laterHorizon = int64(1_800_000_000_000_000_000)
	)

	c, eng := buildReconcileClientWith(t, embedded, repo)
	require.NoError(t, c.segmentMgr.SaveMergeWatermark(kgtypes.GraphCode, repo, seededHorizon))
	eng.setServedHorizon(windowHorizon)

	docs := fastloadVecDocs(repo, corpusN)
	require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, docs))
	require.NoError(t, c.segmentMgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, repo))

	coWorker := docs[0]

	// PASS ONE — THE KNOWN-POSITIVE CONTROL, in the same run. A drain that SUCCEEDS
	// advances the horizon. Without it, "the horizon did not move" is satisfied just
	// as well by a fixture that never pulls anything at all.
	eng.mu.Lock()
	eng.scanItems[repo] = []*knowledgev1.PipelineScanItem{
		{NodeId: "co-worker-a-" + coWorker.ID, GraphName: repo, BinaryVector: coWorker.Vector},
	}
	eng.mu.Unlock()

	c.reconcileSegmentCoverage(ctx)

	persisted, err := c.segmentMgr.LoadMergeWatermark(kgtypes.GraphCode, repo)
	require.NoError(t, err)
	require.Equal(t, windowHorizon, persisted,
		"KNOWN-POSITIVE: a landed window MUST advance the durable horizon — if this fails, the held-horizon assertion below proves nothing")
	scansAfterControl := eng.deltaScanCallCount(repo)
	require.Positive(t, scansAfterControl,
		"KNOWN-POSITIVE: the control pass must actually have pulled a window")

	// PASS TWO — the drain fails. A NEW window is served, so a commit-before-drain
	// implementation has something strictly later to write.
	eng.setServedHorizon(laterHorizon)
	eng.mu.Lock()
	eng.scanItems[repo] = []*knowledgev1.PipelineScanItem{
		{NodeId: "co-worker-b-" + coWorker.ID, GraphName: repo, BinaryVector: coWorker.Vector},
	}
	eng.mu.Unlock()

	switchAccountMidSession(t)

	// THE LEVER IS PROVEN AT THE SEAM, not assumed: the drain the pass is about to
	// call really does return an error now.
	require.Error(t, c.segmentMgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, repo),
		"PRECONDITION: the lever must make the DRAIN fail; if it returns nil the pass below takes the else-branch and the test asserts nothing")

	c.reconcileSegmentCoverage(ctx)

	// AND THE PULL STILL HAPPENED. This is the assertion that separates this test
	// from the scan-failure case: without it, a lever that broke the PULL would hold
	// the horizon too, and this test would silently become a second spelling of
	// TestMergeLandsCoWorkerDeltaAndAdvancesWatermark's failing half.
	require.Greater(t, eng.deltaScanCallCount(repo), scansAfterControl,
		"PRECONDITION: the window must still have been PULLED — a held horizon means nothing if consumeSegmentDelta returned Pull=false and commitMergeWatermark exited at its first guard")

	stillPersisted, err := c.segmentMgr.LoadMergeWatermark(kgtypes.GraphCode, repo)
	require.NoError(t, err)
	require.Equal(t, windowHorizon, stillPersisted,
		"a window whose DRAIN failed must NOT advance the horizon — otherwise the next pass starts past documents that never shipped")
	require.NotEqual(t, laterHorizon, stillPersisted,
		"known-negative: the later horizon is exactly what a commit-outside-the-else-branch implementation would have written")
}

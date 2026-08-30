// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// segment_tick_nudge_test.go gates the CONSUMER end of the segment cheap tick at
// the bootstrap layer: a nudge recorded for a graph makes the reconcile pass pull
// THAT graph's delta, and a graph that was not nudged is not pulled at all.
//
// WHERE THE OTHER HALF LIVES, stated so neither test is read as covering more than
// it does. The DECISION — nudge only when the served stamp exceeds the last stamp
// this client poked on — is the pipeline's, and it is gated by
// TestSegmentTickDebouncesToOneNudgePerAdvance, which counts nudges across repeated
// identical polls (the only shape that distinguishes a correct debounce from a
// nudge-every-poll rebuild loop). THIS test gates the other side of that seam: that
// a recorded nudge actually narrows and drives a real delta pull. Two tests flank
// the seam; the pipeline one drives the real comparison, this one drives the real
// pull.

// TestSegmentTickNudgesOnlyGraphsPastTheLastPokedStamp pins both directions of the
// nudge's EFFECT: the nudged graph is scoped and pulled exactly once, the
// un-nudged graph is not pulled at all.
func TestSegmentTickNudgesOnlyGraphsPastTheLastPokedStamp(t *testing.T) {
	ctx := opCtx()
	const (
		nudgedRepo = "segTickNudgedRepo"
		quietRepo  = "segTickQuietRepo"
		corpusN    = 128
		embedded   = 100
		// Both graphs are SEEDED, so a graph that does not pull is a graph the scope
		// excluded rather than one refused by the unseeded-graph load rule.
		seededHorizon = int64(1_600_000_000_000_000_000)
		windowHorizon = int64(1_700_000_000_123_456_789)
	)

	c, eng := buildReconcileClientWith(t, embedded, nudgedRepo, quietRepo)
	require.NoError(t, c.segmentMgr.SaveMergeWatermark(kgtypes.GraphCode, nudgedRepo, seededHorizon))
	require.NoError(t, c.segmentMgr.SaveMergeWatermark(kgtypes.GraphCode, quietRepo, seededHorizon))
	eng.setServedHorizon(windowHorizon)

	for _, repo := range []string{nudgedRepo, quietRepo} {
		docs := fastloadVecDocs(repo, corpusN)
		require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, docs))
		require.NoError(t, c.segmentMgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, repo))
	}

	// THE TICK'S RECORDING, through the exact exported entry point the pipeline's
	// gen-poll consumer calls in production.
	c.segmentMgr.NudgeSegmentDelta(kgtypes.GraphCode, nudgedRepo)

	// Drain the nudge set exactly as runSegmentReconcileLoop's `case <-nudges:` arm
	// does, and scope the pass to it.
	nudged := c.segmentMgr.TakeReconcileNudges()
	require.Len(t, nudged, 1, "PRECONDITION: exactly the nudged graph must be recorded")
	require.Equal(t, nudgedRepo, nudged[0].Name)

	scope := make(map[segmentGraphRef]struct{}, len(nudged))
	for _, n := range nudged {
		scope[segmentGraphRef{gt: n.GraphType, name: n.Name}] = struct{}{}
	}

	beforeNudged := eng.deltaScanCallCount(nudgedRepo)
	beforeQuiet := eng.deltaScanCallCount(quietRepo)

	c.reconcileSegmentCoverageScoped(ctx, scope)

	// THE NUDGED GRAPH PULLED EXACTLY ONE WINDOW.
	require.Equal(t, beforeNudged+1, eng.deltaScanCallCount(nudgedRepo),
		"the nudged graph must pull exactly one delta window on the woken pass")

	// AND THE UN-NUDGED GRAPH PULLED NOTHING. This is the assertion that makes the
	// nudge a NARROWING rather than a general wake: without it, a pass that walked
	// every graph would satisfy the first assertion and still defeat the whole point
	// of scoping the woken pass.
	require.Equal(t, beforeQuiet, eng.deltaScanCallCount(quietRepo),
		"a graph that was NOT nudged must not be pulled on a nudge-scoped pass — the drained set scopes the whole walk, and a pass that ignored it would fan out across every graph on every tick")
}

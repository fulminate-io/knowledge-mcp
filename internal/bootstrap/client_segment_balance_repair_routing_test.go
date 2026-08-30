// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// client_segment_balance_repair_routing_test.go — a deficit that survives the reap is
// offered to the BOUNDED repair arm before the reset rebuild.
//
// WHY THREE TESTS. One proves the routing EXISTS; it does not prove it is BOUNDED or
// that it is SCOPED. The escalation test is the convergence proof — without it the
// routing is indistinguishable from a lane that can fire forever on the same cause. The
// out-of-band test is the scope fence — without it, an implementation that routed EVERY
// deficit to the bounded arm passes the other two.

// routingBandDocs and routingBandEmbedded put a graph INSIDE the bounded arm's band:
// the denominator is above tools.SegmentCoverageFloor, the numerator is short of it,
// and the ratio is above tools.CoverageRatioThreshold. They are fixture constants and
// every expectation is stated against them.
const (
	routingBandDocs     = 80
	routingBandEmbedded = 100
)

// routingBelowFloorEmbedded is a denominator BELOW tools.SegmentCoverageFloor, which is
// the clause that must send a deficit straight to the reset rebuild.
const routingBelowFloorEmbedded = 40

// countingRepairs installs a repair driver that records its invocations and optionally
// mutates the fixture, so the verdict's RE-READ observes a real change rather than the
// driver's own claim about one. It is the sibling of countingRebuilds.
func countingRepairs(c *client, ran bool, closeFn func()) *int {
	n := 0
	c.repairArm = func(context.Context, kgtypes.GraphType, string) (tools.RepairOutcome, error) {
		n++
		if closeFn != nil {
			closeFn()
		}
		return tools.RepairOutcome{Ran: ran}, nil
	}
	return &n
}

// routingFixture builds an armed, fused client whose reap reports nothing and changes
// nothing, so every deficit below SURVIVES the reap and reaches the routing branch.
func routingFixture(t *testing.T, embedded int32, docs int) (*client, kgtypes.GraphType, string) {
	t.Helper()
	gt, name := kgtypes.GraphKnowledge, propagationGraphName
	c, _, dir := buildReconcileClientWithDir(t, embedded)
	// SEED THE DIRECTORY BEFORE THE CLIENT READS IT — the disk cache indexes its root
	// at construction, and a corpus written afterwards is silently invisible, which
	// reads exactly like the collapse these tests must not hit.
	seedL2Corpus(t, dir, gt, name, docs)
	armFuse(t, c, gt, name)
	c.reaper = &countingReaper{}

	// FIXTURE CONTROL: the verdict must actually see a deficit, or every invocation
	// assertion below passes vacuously against a graph that was simply balanced.
	before := c.evaluateArmBalance(balanceCtx(), gt, name)
	require.Equal(t, armDeficit, before.verdict,
		"fixture control: the fixture must present a deficit, got %s", before.String())
	return c, gt, name
}

// TestBalanceVerdict_SmallDeficitTakesTheBoundedArmAndNeverRebuilds is the routing.
func TestBalanceVerdict_SmallDeficitTakesTheBoundedArmAndNeverRebuilds(t *testing.T) {
	c, gt, name := routingFixture(t, routingBandEmbedded, routingBandDocs)
	rebuilds := countingRebuilds(c)

	// The repair CLOSES the gap by actually shipping the missing ids into the client's
	// own pool, so the verdict's re-read observes balance rather than being told about
	// it. A driver that only reported success would let this test pass against an
	// implementation that concluded from the outcome struct instead of re-reading.
	repairs := countingRepairs(c, true, func() {
		ctx := context.Background()
		require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, gt, name,
			fastloadVecDocs(name, routingBandEmbedded)))
		require.NoError(t, c.segmentMgr.ReEmitDirtyBuckets(ctx, gt, name))
	})

	runBalanceEdge(t, c, gt, name)

	require.Equal(t, 1, *repairs,
		"a band-admitted deficit must be offered to the BOUNDED arm exactly once; zero here means the "+
			"routing does not exist and the edge went straight to the reset rebuild")
	require.Zero(t, *rebuilds,
		"and a bounded repair that CLOSED the deficit must drive NO reset rebuild — rebuilding a corpus "+
			"that has just converged is the amplification this routing exists to remove")
}

// TestBalanceVerdict_UnclosedBoundedRepairEscalatesToTheRebuild is the CONVERGENCE
// PROOF: the bounded arm gets one attempt, and a deficit that survives it escalates
// inside the SAME quiescence edge rather than being retried forever.
func TestBalanceVerdict_UnclosedBoundedRepairEscalatesToTheRebuild(t *testing.T) {
	c, gt, name := routingFixture(t, routingBandEmbedded, routingBandDocs)
	rebuilds := countingRebuilds(c)
	// Ran=true and NOTHING changes: the arm reports a pass that did not close the gap.
	repairs := countingRepairs(c, true, nil)

	runBalanceEdge(t, c, gt, name)

	// BOTH NUMBERS ARE ASSERTED, and that is the point. A test asserting only the
	// rebuild passes against an implementation that never tried the bounded arm at all.
	require.Equal(t, 1, *repairs, "the bounded arm must have been tried exactly once")
	require.Equal(t, 1, *rebuilds,
		"and a deficit that SURVIVED it must escalate to exactly one reset rebuild in the same edge — a "+
			"lane that can fire forever on one cause is hiding a defect rather than repairing one")
}

// TestBalanceVerdict_DeficitOutsideTheRepairBandGoesStraightToTheRebuild is the SCOPE
// FENCE.
func TestBalanceVerdict_DeficitOutsideTheRepairBandGoesStraightToTheRebuild(t *testing.T) {
	c, gt, name := routingFixture(t, routingBelowFloorEmbedded, balanceFixtureResidentDocs)
	rebuilds := countingRebuilds(c)
	repairs := countingRepairs(c, true, nil)

	// FIXTURE CONTROL: this graph really is outside the band, and by the FLOOR clause.
	require.False(t, repairBandAdmits(routingBelowFloorEmbedded, balanceFixtureResidentDocs),
		"fixture control: a denominator of %d is below tools.SegmentCoverageFloor, so the band must "+
			"decline it", routingBelowFloorEmbedded)

	runBalanceEdge(t, c, gt, name)

	require.Zero(t, *repairs,
		"a deficit below the coverage floor is not this arm's — the ratio is noise there and the "+
			"zero-presence heal owns the graph")
	require.Equal(t, 1, *rebuilds, "so it takes the reset rebuild directly, exactly once")
}

// SPDX-License-Identifier: Apache-2.0

// client_segment_evicted_deciders_test.go — the two deciders that read a residency
// count as evidence about the CORPUS. An evicted pool reports zero, and acting on
// that zero is the most expensive mistake in the residency ticket: a from-scratch
// rebuild on one side, a corpus-scale re-ship on the other.

package bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// TestEvictedPoolDeclinesTheHealAndRepairDeciders pins both declines, each with the
// KNOWN-POSITIVE CONTROL of the same fixture NOT evicted — without which a decider
// hard-wired to decline would be green.
func TestEvictedPoolDeclinesTheHealAndRepairDeciders(t *testing.T) {
	ctx := context.Background()

	t.Run("heal_declines", func(t *testing.T) {
		c, gt, name, docs := evictedArmFixture(t)

		needs, err := c.healNeedsRebuildLocal(ctx, gt, name)
		require.False(t, needs, "an evicted pool must not drive a from-scratch rebuild")
		// THE INSTRUMENT FOR "THE READS WERE NEVER REACHED": this fixture's router
		// points at an unreachable host, so GraphEmbeddedCount ERRORS whenever it is
		// actually called. A nil error therefore proves the decline landed ahead of it.
		require.NoError(t, err,
			"the decline sits ahead of the embedded-count read, so the unreachable server is never contacted")
		// THE SHARPER ONE: LoadResidentDocCount deliberately MATERIALIZES an evicted
		// pool, so a decline placed after it would leave the pool resident here.
		require.True(t, c.segmentMgr.PoolEvicted(gt, name),
			"the decline sits ahead of LoadResidentDocCount, which would have re-materialized the pool")

		// CONTROL: with the pool materialized the SAME call runs on past the gate and
		// surfaces the embedded-count read's error — so the nil above is the decline
		// rather than a decider that never reads anything.
		_, err = c.segmentMgr.Search(ctx, gt, name, "common", docs[0].Vector, 5)
		require.NoError(t, err)
		require.False(t, c.segmentMgr.PoolEvicted(gt, name))
		_, err = c.healNeedsRebuildLocal(ctx, gt, name)
		require.Error(t, err, "with the residency gate open the decider does reach GraphEmbeddedCount")
	})

	t.Run("repair_declines", func(t *testing.T) {
		g := repairTestGraph("evictedRepairRepo")
		c := &client{}
		deps := newFakeRepairDeps()
		// Squarely INSIDE the repair band, so nothing but the residency gate can be
		// what stops the arm.
		deps.embedded[g] = repairArmFixtureFloor
		deps.covered[g] = 70
		deps.evicted[g] = true

		runRepairPasses(c, deps, 1, g)

		require.Zero(t, deps.repairCount(), "an evicted pool must not be handed to a corpus-scale re-ship")
		require.Zero(t, deps.embeddedReads[g], "the decline is ahead of BOTH coverage reads")
		// NAMED CATCHER for the seed-on-decline mistake: the STEP 4a record exists to
		// stop the backstop re-reading a graph this arm has JUDGED. An evicted pool has
		// not been judged — it has not been looked at — and recording one would make an
		// unexamined pool read as verified.
		_, seeded := deps.repairState[g]
		require.False(t, seeded, "a declined-for-eviction graph earns NO repair-state record")
		require.Zero(t, deps.horizonReads[g], "and no merge-horizon seed either")

		// CONTROL: the IDENTICAL fixture with the pool resident does reach the arm.
		deps.evicted[g] = false
		runRepairPasses(c, deps, 1, g)
		require.Equal(t, 1, deps.repairCount(), "the identical in-band fixture DOES fire once the pool is resident")
		require.Positive(t, deps.embeddedReads[g], "and the coverage reads are reached")
	})
}

// repairArmFixtureFloorGuard keeps this file honest about the shared fixture
// constant it leans on: the band cases above are only "in band" while the embedded
// denominator sits at or above the coverage floor.
func TestRepairArmFixtureFloorIsAtOrAboveTheCoverageFloor(t *testing.T) {
	require.GreaterOrEqual(t, repairArmFixtureFloor, tools.SegmentCoverageFloor,
		"the shared repair fixture denominator must clear the coverage floor, or the band cases test the floor clause instead")
}

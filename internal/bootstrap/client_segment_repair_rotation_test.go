// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// client_segment_repair_rotation_test.go — the tick's one grant is spent on WORK.
//
// THE DEFECT IT CLOSES. The rotation used to take its grant at STEP 1, ahead of the
// coverage reads and ahead of every gate that can decline. A graph that took the grant
// and then declined at the band, the residue, the disarm or the breaker had consumed
// the tick's only grant, so a graph that genuinely needed a repair waited one whole
// tick per decliner ahead of it in the rotation — O(#graphs) ticks at a multi-minute
// cadence, on a daemon holding a dozen graphs.

// rotationDecliners is how many non-needing graphs sit AHEAD of the needing one. It is
// a fixture constant, and every expectation below is stated against it.
const rotationDecliners = 3

// TestRepairArmServicesANeedingGraphInOneTickBehindDecliners drives exactly ONE tick
// over a rotation whose first graphs all decline.
func TestRepairArmServicesANeedingGraphInOneTickBehindDecliners(t *testing.T) {
	c := &client{}
	deps := newFakeRepairDeps()

	graphs := make([]segmentGraphRef, 0, rotationDecliners+1)
	for i := range rotationDecliners {
		g := repairTestGraph("rotDecliner" + string(rune('A'+i)))
		// OUT OF BAND: covered == embedded is the "not a deficit" clause, so each of
		// these is offered the slot and declines at STEP 4.
		deps.embedded[g] = repairArmFixtureFloor
		deps.covered[g] = repairArmFixtureFloor
		graphs = append(graphs, g)
	}
	needing := repairTestGraph("rotNeedingGraph")
	deps.embedded[needing] = repairArmFixtureFloor
	deps.covered[needing] = 70 // in band, with a real gap
	graphs = append(graphs, needing)

	runRepairPasses(c, deps, 1, graphs...)

	require.Equal(t, []segmentGraphRef{needing}, deps.repaired,
		"the needing graph must be serviced in the SINGLE tick it was offered in, behind %d decliners. "+
			"An empty result here is the defect: the first decliner took the tick's only grant at STEP 1 "+
			"and every graph behind it waited a whole tick", rotationDecliners)

	// THE COST SHAPE IS PINNED, and this leg is not optional. Without it the property
	// above is satisfied by an implementation that abandons the rotation entirely and
	// probes every graph on every tick forever — meeting the service-time requirement by
	// paying an unbounded, non-extinguishing RPC cost.
	for _, g := range graphs[:rotationDecliners] {
		require.Equal(t, 1, deps.embeddedReads[g],
			"decliner %s must be probed exactly ONCE in this tick", g.name)
	}
	require.Equal(t, 2, deps.embeddedReads[needing],
		"the SERVICED graph reads its denominator twice — once for the band and once for the post-pass "+
			"recalibration — which is the shape the landed backstop gates already pin. Stating it here "+
			"keeps the decliners' count of 1 a measured contrast rather than an unexamined number")
}

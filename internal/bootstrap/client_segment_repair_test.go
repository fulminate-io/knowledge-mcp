// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// repairArmFixtureFloor is a denominator comfortably above tools.SegmentCoverageFloor
// so the band's floor clause is never what a case is accidentally testing.
const repairArmFixtureFloor = 100

// fakeRepairDeps drives the detector's four dependencies. It exists so the operand
// pair can be made to DISAGREE with the membership set the repair sees, which one
// concrete engine cannot be made to do — the disagreement is the whole subject of
// the convergence catchers.
type fakeRepairDeps struct {
	embedded map[segmentGraphRef]int
	covered  map[segmentGraphRef]int

	// coveredAfterRepair, when present for a graph, is what LiveResidentCount
	// returns once a repair pass has RUN for it — the post-pass re-read the
	// calibration is computed from.
	coveredAfterRepair map[segmentGraphRef]int
	// embeddedAfterRepair is the same lever for the DENOMINATOR: it models a corpus
	// that grew underneath the pass, which is the one input that separates the
	// calibration's denominator-still clause from its two per-format ones.
	embeddedAfterRepair map[segmentGraphRef]int

	outcome   tools.RepairOutcome
	repairErr error
	allow     bool

	// repaired records the graphs Repair was invoked for, in order. It is the SCAN
	// SEAM counter every one-scan-per-tick assertion reads.
	repaired []segmentGraphRef

	// repairState is the persisted backstop record per graph. ITS ZERO VALUE IS AN
	// ABSENT RECORD — the state in which the backstop is eligible to scan — because
	// that is what every landed band-gate case assumes when it drives the arm and
	// expects a pass to happen. A fake defaulting to a converged, freshly-verified
	// record would gate them all off at the backstop gate and redden them.
	repairState map[segmentGraphRef]segmentdist.RepairState
	// now is the injected clock, in nanos. Tests advance it to age a record past
	// tools.SegmentRepairBackstopInterval without sleeping.
	now int64
	// servedHorizon is what the single-row horizon probe reports, and horizonReads
	// counts the probe per graph — the instrument for "at most once per process",
	// which degrades to "a horizon was eventually written" without the counter.
	servedHorizon    int64
	servedHorizonErr error
	horizonReads     map[segmentGraphRef]int
	// mergeWatermark records what was persisted through the single horizon sink both
	// writers share.
	mergeWatermark map[segmentGraphRef]int64
	// embeddedReads counts the DENOMINATOR read per graph. It is the sharper of the
	// two boot-quiet instruments: a gate placed after the coverage reads would still
	// show zero scans while paying two RPCs per graph per pass, which is most of the
	// load the demotion exists to remove.
	embeddedReads map[segmentGraphRef]int
}

func newFakeRepairDeps() *fakeRepairDeps {
	return &fakeRepairDeps{
		embedded:           map[segmentGraphRef]int{},
		covered:            map[segmentGraphRef]int{},
		coveredAfterRepair: map[segmentGraphRef]int{},
		outcome:            tools.RepairOutcome{Ran: true},
		allow:              true,
		repairState:        map[segmentGraphRef]segmentdist.RepairState{},
		horizonReads:       map[segmentGraphRef]int{},
		mergeWatermark:     map[segmentGraphRef]int64{},
	}
}

func (f *fakeRepairDeps) EmbeddedCount(_ context.Context, g segmentGraphRef) (int, error) {
	if f.embeddedReads == nil {
		f.embeddedReads = map[segmentGraphRef]int{}
	}
	f.embeddedReads[g]++
	return f.embedded[g], nil
}

func (f *fakeRepairDeps) LiveResidentCount(_ context.Context, g segmentGraphRef) (int, error) {
	return f.covered[g], nil
}

func (f *fakeRepairDeps) Repair(_ context.Context, g segmentGraphRef) (tools.RepairOutcome, error) {
	f.repaired = append(f.repaired, g)
	if f.repairErr != nil {
		return tools.RepairOutcome{}, f.repairErr
	}
	if after, ok := f.coveredAfterRepair[g]; ok {
		f.covered[g] = after
	}
	if after, ok := f.embeddedAfterRepair[g]; ok {
		f.embedded[g] = after
	}
	return f.outcome, nil
}

func (f *fakeRepairDeps) BreakerAllows(segmentGraphRef) bool { return f.allow }

func (f *fakeRepairDeps) LoadRepairState(g segmentGraphRef) (segmentdist.RepairState, error) {
	return f.repairState[g], nil
}

func (f *fakeRepairDeps) SaveRepairState(g segmentGraphRef, st segmentdist.RepairState) error {
	f.repairState[g] = st
	return nil
}

func (f *fakeRepairDeps) NowNanos() int64 { return f.now }

func (f *fakeRepairDeps) ServedHorizon(_ context.Context, g segmentGraphRef) (int64, error) {
	f.horizonReads[g]++
	if f.servedHorizonErr != nil {
		return 0, f.servedHorizonErr
	}
	return f.servedHorizon, nil
}

func (f *fakeRepairDeps) SaveMergeWatermark(g segmentGraphRef, horizonNanos int64) error {
	f.mergeWatermark[g] = horizonNanos
	return nil
}

func (f *fakeRepairDeps) repairCount() int { return len(f.repaired) }

// runRepairPasses drives n whole reconcile passes over graphs, calling the same
// per-pass reset the real pass does. Sequential by design: this proves the rotation
// and the one-per-tick cap, NOT the mutex discipline, which is asserted by review
// and by `go test -race`.
func runRepairPasses(c *client, deps repairArmDeps, n int, graphs ...segmentGraphRef) {
	for range n {
		// EACH PASS IS A FRESH BACKSTOP INTERVAL. Without this, pass 2 of any
		// multi-pass case returns at the backstop gate on the record pass 1 wrote, and
		// every assertion below would be measuring that gate rather than the band or
		// calibration property it names — two cases would go red and two would stay
		// green while measuring the wrong thing.
		if f, ok := deps.(*fakeRepairDeps); ok {
			f.now += int64(tools.SegmentRepairBackstopInterval) + 1
		}
		c.beginRepairTick()
		for _, g := range graphs {
			c.repairUncoveredGraphWith(context.Background(), g, deps, true)
		}
	}
}

func repairTestGraph(name string) segmentGraphRef {
	return segmentGraphRef{gt: kgtypes.GraphCode, name: name}
}

// TestRepairUncoveredArm pins the band gate: the arm fires inside it, declines on
// both sides of it, and stops firing once calibrated.
func TestRepairUncoveredArm(t *testing.T) {
	g := repairTestGraph("bandRepo")

	t.Run("fires_in_band", func(t *testing.T) {
		c := &client{}
		deps := newFakeRepairDeps()
		deps.embedded[g] = repairArmFixtureFloor
		deps.covered[g] = 70 // >= 0.5*100, < 100, denominator over the floor

		runRepairPasses(c, deps, 1, g)
		require.Equal(t, 1, deps.repairCount(), "a graph in the coverage band is repaired")
	})

	t.Run("skips_below_band", func(t *testing.T) {
		c := &client{}
		deps := newFakeRepairDeps()
		deps.embedded[g] = repairArmFixtureFloor
		deps.covered[g] = 40 // under 0.5*100 — the existing heal owns this graph

		runRepairPasses(c, deps, 1, g)
		require.Zero(t, deps.repairCount(), "below the ratio the auto-heal owns the graph, not this arm")

		// Known positive on the SAME fixture: only the covered count moved, so the
		// zero above is the ratio clause and not a dead arm.
		deps.covered[g] = 70
		runRepairPasses(c, deps, 1, g)
		require.Equal(t, 1, deps.repairCount(), "the identical fixture inside the band DOES fire")
	})

	t.Run("skips_at_converged", func(t *testing.T) {
		c := &client{}
		deps := newFakeRepairDeps()
		deps.embedded[g] = repairArmFixtureFloor
		deps.covered[g] = repairArmFixtureFloor // covered == embedded

		runRepairPasses(c, deps, 1, g)
		require.Zero(t, deps.repairCount(), "at or above the denominator this is the residue class, not a gap")

		deps.covered[g] = 70
		runRepairPasses(c, deps, 1, g)
		require.Equal(t, 1, deps.repairCount(), "the identical fixture with a real gap DOES fire")
	})

	// calibration_stops_refiring is the arm's central property. The operands can never
	// agree on their own — the denominator counts vectored nodes the rebuild scan
	// excludes — so without calibration the arm would fire every tick forever.
	t.Run("calibration_stops_refiring", func(t *testing.T) {
		c := &client{}
		deps := newFakeRepairDeps()
		deps.embedded[g] = repairArmFixtureFloor
		deps.covered[g] = 70 // the 30 difference is structural: the repair cannot close it

		runRepairPasses(c, deps, 3, g)
		require.Equal(t, 1, deps.repairCount(),
			"the first pass calibrates on the gap it observed; later passes see gap <= residue and decline")
		require.Equal(t, 30, c.repairResidue(g), "the residue is the gap the finished pass settled at")
	})
}

// TestRepairArmDoesNotRefireOnTombstoneResidue is the regression for the trigger
// that cannot converge. A graph whose ENTIRE gap is retained tombstones and
// embed-failure-marked nodes is repaired at most once — a naive covered<embedded
// trigger scores three across three passes.
func TestRepairArmDoesNotRefireOnTombstoneResidue(t *testing.T) {
	c := &client{}
	g := repairTestGraph("tombstoneResidueRepo")
	deps := newFakeRepairDeps()
	deps.embedded[g] = repairArmFixtureFloor
	deps.covered[g] = 80
	// The pass runs and finds NOTHING missing: every one of the 20 the denominator
	// counts is tombstoned or embed-failure-marked, so the rebuild scan excludes it.
	deps.outcome = tools.RepairOutcome{Ran: true, ScannedEligible: 80}

	runRepairPasses(c, deps, 3, g)

	require.Equal(t, 1, deps.repairCount(),
		"a purely structural gap is repaired ONCE and then calibrated away, not re-scanned every tick")
	require.Equal(t, 20, c.repairResidue(g))
}

// TestRepairArmConvergesWhenOperandsDisagree is the catcher for the arm's central
// bound. The fake's live count deliberately disagrees with the membership set the
// repair reports, which is the shape that made an earlier split-corpus design storm
// forever.
//
// EXACTLY ONCE, not "at most once": zero also satisfies at-most-once, and an arm
// that never fires at all would pass that weaker assertion.
func TestRepairArmConvergesWhenOperandsDisagree(t *testing.T) {
	c := &client{}
	g := repairTestGraph("disagreeRepo")
	deps := newFakeRepairDeps()
	deps.embedded[g] = repairArmFixtureFloor
	deps.covered[g] = 66 // the trigger sees a 34 gap...
	// ...while the membership probe behind the pass reports the corpus fully covered,
	// so the pass ships nothing and the live count does not move.
	deps.outcome = tools.RepairOutcome{Ran: true, ScannedEligible: 66}

	runRepairPasses(c, deps, 3, g)

	require.Equal(t, 1, deps.repairCount(),
		"the arm fires EXACTLY once and then calibrates, however far the two operands disagree")
}

// TestRepairArmDoesNotCalibrateOnFailure pins that a pass which did NOT repair
// records nothing. Calibrating off an un-repaired gap makes gap <= residue hold
// forever and kills the arm for the process lifetime.
func TestRepairArmDoesNotCalibrateOnFailure(t *testing.T) {
	t.Run("errored_pass", func(t *testing.T) {
		c := &client{}
		g := repairTestGraph("erroredRepo")
		deps := newFakeRepairDeps()
		deps.embedded[g] = repairArmFixtureFloor
		deps.covered[g] = 70
		deps.repairErr = errors.New("scan boom")

		runRepairPasses(c, deps, 2, g)

		require.Equal(t, 2, deps.repairCount(), "a failed pass leaves the arm armed for the next tick")
		require.Zero(t, c.repairResidue(g), "a failed pass records no residue")
		require.Equal(t, 2, c.repairFailures(g), "consecutive failures are counted ARM-LOCALLY")
	})

	// coalesced_pass is the deadlock catcher. The single-flight returns the
	// ZERO-VALUE outcome with a NIL error, so a step guarding only on err != nil
	// falls through and records the un-repaired gap as settled residue.
	t.Run("coalesced_pass", func(t *testing.T) {
		c := &client{}
		g := repairTestGraph("coalescedRepo")
		deps := newFakeRepairDeps()
		deps.embedded[g] = repairArmFixtureFloor
		deps.covered[g] = 70
		deps.outcome = tools.RepairOutcome{} // Ran=false, nil error — the coalesce shape

		runRepairPasses(c, deps, 2, g)

		require.Equal(t, 2, deps.repairCount(), "a coalesced pass leaves the arm armed for the next tick")
		require.Zero(t, c.repairResidue(g), "a coalesced pass records NOTHING — another pass is doing the work")
		require.Zero(t, c.repairFailures(g), "a coalesce is not a failure")
	})
}

// TestRepairArmScanErrorDoesNotLatchHealBreaker keeps repair failures off the SHARED
// breaker. That breaker latches after two no-progress passes and would disarm the
// graph's whole auto-heal until a manual rebuild — a repair that cannot run is no
// reason to stop healing a pool that collapses later.
func TestRepairArmScanErrorDoesNotLatchHealBreaker(t *testing.T) {
	t.Run("breaker_untouched", func(t *testing.T) {
		c := &client{}
		g := repairTestGraph("breakerRepo")
		deps := newFakeRepairDeps()
		deps.embedded[g] = repairArmFixtureFloor
		deps.covered[g] = 70
		deps.repairErr = errors.New("scan boom")

		runRepairPasses(c, deps, 2, g)

		require.Equal(t, 2, c.repairFailures(g), "the ARM-LOCAL counter moved")
		require.True(t, c.healBreaker.Allow(g.gt, g.name),
			"two failed repairs must leave the SHARED auto-heal breaker unlatched")
	})

	// disarms_after_third_failure is the catcher for the disarm actually being
	// CONSULTED. Without that step the failure counter is decorative and the third
	// failure changes nothing.
	t.Run("disarms_after_third_failure", func(t *testing.T) {
		c := &client{}
		g := repairTestGraph("disarmRepo")
		deps := newFakeRepairDeps()
		deps.embedded[g] = repairArmFixtureFloor
		deps.covered[g] = 70
		deps.repairErr = errors.New("scan boom")

		runRepairPasses(c, deps, 3, g)
		require.Equal(t, repairFailureDisarm, deps.repairCount(), "three passes, three attempts")

		runRepairPasses(c, deps, 1, g)
		require.Equal(t, repairFailureDisarm, deps.repairCount(),
			"the fourth pass attempts NOTHING — the arm has disarmed itself for this graph")
	})
}

// TestRepairArmRoundRobinsOnePassPerTick bounds boot cost. Without the rotation every
// gapped graph would scan its whole corpus in the same reconcile tick.
func TestRepairArmRoundRobinsOnePassPerTick(t *testing.T) {
	graphs := []segmentGraphRef{
		repairTestGraph("rrRepoA"), repairTestGraph("rrRepoB"), repairTestGraph("rrRepoC"),
	}
	seed := func(deps *fakeRepairDeps) {
		for _, g := range graphs {
			deps.embedded[g] = repairArmFixtureFloor
			deps.covered[g] = 70
		}
	}

	t.Run("one_scan_per_tick", func(t *testing.T) {
		c := &client{}
		deps := newFakeRepairDeps()
		seed(deps)

		runRepairPasses(c, deps, 1, graphs...)
		require.Equal(t, 1, deps.repairCount(),
			"three gapped graphs, ONE pass, exactly one full-corpus scan")
	})

	t.Run("each_graph_served_once", func(t *testing.T) {
		c := &client{}
		deps := newFakeRepairDeps()
		seed(deps)
		// The repair leaves each graph exactly as it found it, so calibration is what
		// would otherwise stop a graph being served — here every graph must still get
		// its own tick.
		runRepairPasses(c, deps, 3, graphs...)

		require.Equal(t, graphs, deps.repaired,
			"three passes serve each graph once, in rotation order")
	})
}

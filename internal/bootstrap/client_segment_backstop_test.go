// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// runBackstopPasses drives n PERIODIC passes over graphs with a STILL CLOCK.
//
// IT IS DELIBERATELY NOT runRepairPasses. That helper advances the fake clock past
// the backstop interval before each pass, which is exactly the cross-pass suppression
// these tests exist to observe — using it here would make every one of them vacuous.
func runBackstopPasses(c *client, deps repairArmDeps, n int, graphs ...segmentGraphRef) {
	for range n {
		c.beginRepairTick()
		for _, g := range graphs {
			c.repairUncoveredGraphWith(context.Background(), g, deps, true)
		}
	}
}

// seededConverged is a record the backstop gate must accept: converged and stamped
// at the fake's current clock, so it is well inside the interval.
func seededConverged(now int64) segmentdist.RepairState {
	return segmentdist.RepairState{Converged: true, Scanned: true, VerifiedAtNanos: now}
}

// TestBackstopBootQuietWithSeededRecords is the ZERO half of the boot-quiet control,
// and it is meaningless without its known positive
// (TestBackstopScansOnceThenSeedsAndGoesQuiet): a zero alone is equally consistent
// with an arm that was deleted.
func TestBackstopBootQuietWithSeededRecords(t *testing.T) {
	c := &client{}
	deps := newFakeRepairDeps()
	deps.now = time.Now().UnixNano()

	graphs := []segmentGraphRef{
		repairTestGraph("quietAlpha"), repairTestGraph("quietBeta"), repairTestGraph("quietGamma"),
	}
	for _, g := range graphs {
		// Every fixture is IN THE REPAIR BAND, so the record is the only thing that can
		// be suppressing the scan — a converged fixture would decline at the band gate
		// and this test would pass without the backstop gate existing at all.
		deps.embedded[g] = repairArmFixtureFloor
		deps.covered[g] = 70
		deps.repairState[g] = seededConverged(deps.now)
	}

	runBackstopPasses(c, deps, 3, graphs...)

	require.Zero(t, deps.repairCount(),
		"a graph whose record is converged and fresh costs ZERO full-corpus scans")
	for _, g := range graphs {
		require.Zero(t, deps.embeddedReads[g],
			"and ZERO denominator reads — the gate must precede the coverage RPCs, not merely the scan")
	}
}

// TestBackstopScansOnceThenSeedsAndGoesQuiet is the KNOWN POSITIVE for the zeros
// above: the same arm, on a graph with no record, does scan — exactly once — and then
// suppresses itself for the interval.
func TestBackstopScansOnceThenSeedsAndGoesQuiet(t *testing.T) {
	c := &client{}
	deps := newFakeRepairDeps()
	deps.now = time.Now().UnixNano()

	// The unseeded graph's fixture MUST SIT INSIDE THE REPAIR BAND: denominator over
	// the floor, covered below the denominator and at or above the ratio. A fixture
	// that declines at the band gate takes the seed path instead, scans nothing, and
	// would read here as a broken arm.
	unseeded := repairTestGraph("scansOnce")
	deps.embedded[unseeded] = repairArmFixtureFloor
	deps.covered[unseeded] = 70

	seeded := []segmentGraphRef{repairTestGraph("quietOne"), repairTestGraph("quietTwo")}
	for _, g := range seeded {
		deps.embedded[g] = repairArmFixtureFloor
		deps.covered[g] = 70
		deps.repairState[g] = seededConverged(deps.now)
	}

	runBackstopPasses(c, deps, 3, append([]segmentGraphRef{unseeded}, seeded...)...)

	require.Equal(t, []segmentGraphRef{unseeded}, deps.repaired,
		"exactly one scan, on the one graph whose record could not answer for it")

	// THE 2 IS DERIVED, NOT OBSERVED. Pass 1 reaches the denominator read once, and
	// the calibration re-reads it a second time; passes 2 and 3 return at the backstop
	// gate because pass 1 wrote a converged record stamped at the still clock's now. If
	// a fixture change alters how many passes reach the calibration, RE-DERIVE this the
	// same way rather than adjusting it to whatever the test prints.
	require.Equal(t, 2, deps.embeddedReads[unseeded],
		"one read for the band gate and one for the calibration — then the record answers")
	for _, g := range seeded {
		require.Zero(t, deps.embeddedReads[g],
			"the already-answered graphs contribute nothing on any pass, because the gate precedes the reads")
	}
}

// TestBackstopSkippedOnNudgedPass pins that a search-nudged pass does not drag the
// expensive arm onto the interactive path — the exact shape the merge architecture
// retires — while the MERGE it was woken for still runs.
//
// THE SECOND HALF IS NOT OPTIONAL: without it the two zeros are satisfiable by a
// nudge path that is simply broken.
func TestBackstopSkippedOnNudgedPass(t *testing.T) {
	t.Run("the arm returns before every read on a scoped pass", func(t *testing.T) {
		c := &client{}
		deps := newFakeRepairDeps()
		deps.now = time.Now().UnixNano()

		// An UNSEEDED, in-band graph: on a periodic pass this fixture scans (that is
		// TestBackstopScansOnceThenSeedsAndGoesQuiet), so a zero here can only be the
		// scoped-pass gate.
		g := repairTestGraph("nudgedRepo")
		deps.embedded[g] = repairArmFixtureFloor
		deps.covered[g] = 70

		for range 3 {
			c.beginRepairTick()
			c.repairUncoveredGraphWith(context.Background(), g, deps, false /* a nudge-woken pass */)
		}

		require.Zero(t, deps.repairCount(), "a nudge-woken pass runs no full-corpus scan")
		require.Zero(t, deps.embeddedReads[g], "and pays no coverage read either")
	})

	t.Run("the merge still runs for the scoped graph", func(t *testing.T) {
		ctx := opCtx()
		const (
			repo          = "nudgedMergeRepo"
			seededHorizon = int64(1_600_000_000_000_000_000)
		)
		c, eng, _ := buildReconcileClientWithSeg(t, 100, repo)
		eng.setServedHorizon(1_700_000_000_000_000_000)
		require.NoError(t, c.segmentMgr.SaveMergeWatermark(kgtypes.GraphCode, repo, seededHorizon))

		// The graph must be IN the scope: a scoped pass skips every graph outside it, so
		// a fixture whose graph was absent would show the same zeros while proving
		// nothing about the gate.
		scope := map[segmentGraphRef]struct{}{{gt: kgtypes.GraphCode, name: repo}: {}}
		c.reconcileSegmentCoverageScoped(ctx, scope)

		require.Positive(t, eng.deltaScanCallCount(repo),
			"the woken pass must still pull the delta it was woken for")
		require.Zero(t, eng.scanCallCount(repo),
			"but it must not drag a full-corpus scan along with it")
	})
}

// managerBackedRepairDeps is fakeRepairDeps with the repair-record pair delegated to a
// REAL Manager over a temp cache root, so the calibration's assertions measure what
// landed on disk rather than what the test handed the fake.
type managerBackedRepairDeps struct {
	*fakeRepairDeps
	mgr *segmentdist.Manager
}

func (d managerBackedRepairDeps) LoadRepairState(g segmentGraphRef) (segmentdist.RepairState, error) {
	return d.mgr.LoadRepairState(g.gt, g.name)
}

func (d managerBackedRepairDeps) SaveRepairState(g segmentGraphRef, st segmentdist.RepairState) error {
	return d.mgr.SaveRepairState(g.gt, g.name, st)
}

// TestCalibrationPersistsSnapshotConvergedBit pins the trust bit as a SNAPSHOT
// claim: it says everything embedded as of scan start was covered, and it is
// therefore exactly the two per-format counts.
//
// Each per-format clause has its own catcher one input away from the converged base
// fixture, so deleting either reddens exactly one subtest and names itself. The third
// subtest is the SCOPE catcher rather than a clause catcher: it drives a write
// landing after the snapshot and requires that it does NOT withhold convergence,
// which is what keeps a continuously-written graph from re-scanning its whole corpus
// on every boot.
func TestCalibrationPersistsSnapshotConvergedBit(t *testing.T) {
	g := repairTestGraph("calibrationRepo")

	// newDeps builds the CONVERGED BASE FIXTURE: an in-band graph whose pass finds
	// nothing missing on either format.
	newDeps := func(t *testing.T) managerBackedRepairDeps {
		t.Helper()
		f := newFakeRepairDeps()
		f.now = time.Now().UnixNano()
		f.embedded[g] = repairArmFixtureFloor
		f.covered[g] = 70
		f.coveredAfterRepair[g] = 90
		f.outcome = tools.RepairOutcome{Ran: true, MissingHNSW: 0, MissingBM25: 0}
		return managerBackedRepairDeps{
			fakeRepairDeps: f,
			mgr:            segmentdist.NewManager(nil, t.TempDir(), 0),
		}
	}

	// THE BASE FIXTURE IS WHERE SCANNED IS LOAD-BEARING, so it is asserted here in the
	// parent body rather than as a fourth subtest. This is the one shape on which the
	// calibration writes a record that is converged AND scanned — what a correct
	// implementation must produce for the coverage column to ever read "gap-repairing"
	// for a real, verified gap. Asserting it inside the two UNCONVERGED subtests
	// would be near-vacuous, because the column ANDs Scanned with Converged and the bit
	// is unreadable there by the only consumer that cares.
	base := newDeps(t)
	runBackstopPasses(&client{}, base, 1, g)
	got, err := base.mgr.LoadRepairState(g.gt, g.name)
	require.NoError(t, err)
	require.True(t, got.Converged,
		"nothing missing on either format IS the converged shape")
	require.True(t, got.Scanned,
		"the calibration is the one path that actually examined the corpus, and it says so")
	require.Positive(t, got.VerifiedAtNanos, "and stamps when it looked")

	t.Run("unconverged_hnsw", func(t *testing.T) {
		// The formats DIVERGE in production — an id with no vector builds no HNSW
		// document — so this is a real asymmetry rather than defensive symmetry.
		deps := newDeps(t)
		deps.outcome = tools.RepairOutcome{Ran: true, MissingHNSW: 3, MissingBM25: 0}

		runBackstopPasses(&client{}, deps, 1, g)

		st, err := deps.mgr.LoadRepairState(g.gt, g.name)
		require.NoError(t, err)
		require.False(t, st.Converged, "a pass still missing HNSW documents has not settled")
		require.True(t, st.Scanned, "but it did look, and Scanned is set on this path unconditionally")
	})

	t.Run("unconverged_bm25", func(t *testing.T) {
		deps := newDeps(t)
		deps.outcome = tools.RepairOutcome{Ran: true, MissingHNSW: 0, MissingBM25: 5}

		runBackstopPasses(&client{}, deps, 1, g)

		st, err := deps.mgr.LoadRepairState(g.gt, g.name)
		require.NoError(t, err)
		require.False(t, st.Converged, "the inverted shape must redden too, or the clause is one-sided")
	})

	t.Run("mid_scan_write_does_not_block_convergence", func(t *testing.T) {
		// SNAPSHOT SEMANTICS. The corpus grows underneath the pass — 25 nodes embedded
		// after the scan started — and that must NOT block convergence. The backstop's
		// claim is bounded to the snapshot it examined: everything embedded AS OF SCAN
		// START is covered. A write landing after that instant belongs to the currency
		// path, not to this arm, so chasing it would make a continuously-written graph
		// unable to ever converge and re-scan its whole corpus on every boot.
		deps := newDeps(t)
		deps.embeddedAfterRepair = map[segmentGraphRef]int{g: repairArmFixtureFloor + 25}

		runBackstopPasses(&client{}, deps, 1, g)

		st, err := deps.mgr.LoadRepairState(g.gt, g.name)
		require.NoError(t, err)
		require.True(t, st.Converged,
			"the snapshot was clean on both formats, so the pass converged — the post-snapshot write is the currency path's business")
		require.True(t, st.Scanned, "and it is still a pass that looked")
	})
}

// TestRepairArmSeedsResidueFromPersistedRecord pins that the persisted residue seeds
// the gate ONLY when its converged trust bit is set.
//
// ALL THREE FIXTURES SIT INSIDE THE REPAIR BAND and stamp VerifiedAtNanos far in the
// past, so the backstop gate admits every case and the residue seeding is what is
// actually under test. A test that let the gate do the suppressing would pass while
// the seeding was never wired at all.
func TestRepairArmSeedsResidueFromPersistedRecord(t *testing.T) {
	g := repairTestGraph("residueSeedRepo")
	// Far enough in the past that the gate admits every case, whatever the clock reads.
	stale := time.Now().Add(-72 * time.Hour).UnixNano()

	inBand := func() *fakeRepairDeps {
		f := newFakeRepairDeps()
		f.now = time.Now().UnixNano()
		f.embedded[g] = repairArmFixtureFloor
		f.covered[g] = 70 // gap 30, in band
		return f
	}

	t.Run("converged_suppresses", func(t *testing.T) {
		deps := inBand()
		deps.repairState[g] = segmentdist.RepairState{
			Residue: 30, Converged: true, Scanned: true, VerifiedAtNanos: stale,
		}

		runBackstopPasses(&client{}, deps, 1, g)

		require.Zero(t, deps.repairCount(),
			"the convergence a previous process reached survives the restart — that is what the record is for")
	})

	t.Run("unconverged_scans", func(t *testing.T) {
		// THE SAME RESIDUE, trusted differently. Seeding the gate from the number alone
		// would let one pass that failed to settle mask a real gap permanently, where the
		// in-memory version bounded that mistake to a single process.
		deps := inBand()
		deps.repairState[g] = segmentdist.RepairState{
			Residue: 30, Converged: false, Scanned: true, VerifiedAtNanos: stale,
		}

		runBackstopPasses(&client{}, deps, 1, g)

		require.Equal(t, 1, deps.repairCount(),
			"an unconverged record describes nothing, so the arm still owes this graph one authoritative scan")
	})

	t.Run("absent_scans", func(t *testing.T) {
		deps := inBand()

		runBackstopPasses(&client{}, deps, 1, g)

		require.Equal(t, 1, deps.repairCount(), "no record at all behaves exactly as before any of this existed")
	})
}

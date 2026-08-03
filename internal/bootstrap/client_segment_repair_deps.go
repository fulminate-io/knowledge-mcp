// SPDX-License-Identifier: Apache-2.0

// client_segment_repair_deps.go — the coverage backstop's DEPENDENCY SEAM: the
// repairArmDeps surface the arm reads and acts through, and the clientRepairDeps
// binding that wires it to the live client.
//
// Split from client_segment_repair.go for the 500-line cap, along the seam between
// the arm's POLICY (its numbered decision cascade, which stays in the sibling) and
// the SURFACE that policy talks to. Nothing here decides anything: every method is
// the exact call the arm is specified to make, with no logic of its own. Same
// package, no signature changed.
package bootstrap

import (
	"context"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// repairArmDeps is the detector's read-and-act surface. Production wires it straight
// to the three calls the arm is specified to make; it exists as a seam because the
// convergence catchers must drive a live count that DISAGREES with the membership
// set behind it, which a single concrete engine cannot be made to do.
type repairArmDeps interface {
	// EmbeddedCount is the denominator — the single definition shared with the
	// manage(status) coverage column, so the column and the arm cannot disagree
	// about which graphs are short.
	EmbeddedCount(ctx context.Context, g segmentGraphRef) (int, error)
	// LiveResidentCount is the LOAD-FIRST half of the coverage pair. A decider must
	// not conclude "uncovered" from an engine that merely has not loaded: an
	// unloaded engine reads zero live members, which would drop the graph below the
	// band and hand it to a heal that is not the right owner.
	LiveResidentCount(ctx context.Context, g segmentGraphRef) (int, error)
	// Repair runs one pass, shipping only the difference.
	Repair(ctx context.Context, g segmentGraphRef) (tools.RepairOutcome, error)
	// BreakerAllows is a PURE READ of the shared auto-heal breaker. The arm consults
	// it so a graph whose heal is latched does not also collect repair traffic, and
	// never records against it.
	BreakerAllows(g segmentGraphRef) bool
	// LoadRepairState reads the graph's persisted backstop record. It is a DEP rather
	// than a direct Manager call for the same reason the other four are: the gate's
	// catchers must drive a record that disagrees with what any real Manager would
	// produce, which a concrete cache cannot be made to do.
	LoadRepairState(g segmentGraphRef) (segmentdist.RepairState, error)
	// SaveRepairState persists what a completed pass settled at — or, at STEP 4a, what
	// a DECLINED graph is worth recording so the backstop stops re-reading it. The two
	// writers differ in RepairState.Scanned; see that field's doc for why.
	SaveRepairState(g segmentGraphRef, st segmentdist.RepairState) error
	// NowNanos is the backstop's clock, injectable so a test can age a record past
	// tools.SegmentRepairBackstopInterval without sleeping for a day.
	NowNanos() int64
	// ServedHorizon reads the server's safe horizon for this graph WITHOUT paging its
	// corpus — the STEP 4a seed's only source. It is a dep because the seed's catcher
	// must count how many times it is called (at most once per graph per process) and
	// must drive a zero/failed horizon, neither of which a live server can be asked for.
	ServedHorizon(ctx context.Context, g segmentGraphRef) (int64, error)
	// SaveMergeWatermark persists the graph's delta-merge horizon. BOTH writers of that
	// horizon on this arm — the STEP 4a seed and the STEP 9 calibration — go through
	// this one method, so the merge-seeding catchers observe a single sink rather than
	// one dep call and one direct Manager call they would have to instrument twice.
	SaveMergeWatermark(g segmentGraphRef, horizonNanos int64) error
}

// clientRepairDeps binds repairArmDeps to the live client. Every method is the exact
// call the step prescribes, with no logic of its own.
type clientRepairDeps struct{ c *client }

func (d clientRepairDeps) EmbeddedCount(ctx context.Context, g segmentGraphRef) (int, error) {
	return tools.GraphEmbeddedCount(ctx, d.c.GraphCaller(), g.gt, g.name)
}

func (d clientRepairDeps) LiveResidentCount(ctx context.Context, g segmentGraphRef) (int, error) {
	return d.c.segmentMgr.LoadLiveResidentDocCount(ctx, g.gt, g.name)
}

func (d clientRepairDeps) Repair(ctx context.Context, g segmentGraphRef) (tools.RepairOutcome, error) {
	return tools.RepairUncoveredSegments(ctx, d.c.PipelineScanner(), d.c.segmentMgr, g.gt, g.name)
}

func (d clientRepairDeps) BreakerAllows(g segmentGraphRef) bool {
	return d.c.healBreaker.Allow(g.gt, g.name)
}

func (d clientRepairDeps) LoadRepairState(g segmentGraphRef) (segmentdist.RepairState, error) {
	return d.c.segmentMgr.LoadRepairState(g.gt, g.name)
}

func (d clientRepairDeps) SaveRepairState(g segmentGraphRef, st segmentdist.RepairState) error {
	return d.c.segmentMgr.SaveRepairState(g.gt, g.name, st)
}

func (d clientRepairDeps) NowNanos() int64 { return time.Now().UnixNano() }

func (d clientRepairDeps) ServedHorizon(ctx context.Context, g segmentGraphRef) (int64, error) {
	return tools.ReadServedHorizon(ctx, d.c.PipelineScanner(), g.gt, g.name)
}

func (d clientRepairDeps) SaveMergeWatermark(g segmentGraphRef, horizonNanos int64) error {
	return d.c.segmentMgr.SaveMergeWatermark(g.gt, g.name, horizonNanos)
}

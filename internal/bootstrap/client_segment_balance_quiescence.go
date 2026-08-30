// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// client_segment_balance_quiescence.go holds the QUIESCENCE-EDGE VERDICT: the closure
// the collector invokes once BOTH pipeline axes have drained at the current collect
// epoch. It is the consumer that makes the exact balance verdict real.

// ReapInvoker removes dead vectors server-side for one graph, given the observed gap,
// and reports how many it removed.
//
// IT IS AN INTERFACE RATHER THAN A DIRECT CALL so the verdict's ordering — reap, then
// RE-READ, then conclude — is testable without a server. The counting fake a test
// installs here is standing in for a DEPENDENCY (the server's reap), never for the code
// under test, which is the ordering logic in this file.
type ReapInvoker interface {
	ReapDeadVectors(ctx context.Context, gt kgtypes.GraphType, name string, gap int) (removed int, err error)
}

// rebuildDriver rebuilds one graph's segments from its already-embedded nodes. It is the
// verdict's repair for a shortfall the reap cannot close.
//
// IT IS A FIELD ON THE CLIENT rather than a direct tools.RebuildSegments call for the
// same reason ReapInvoker is: the properties under test here are INVOCATION COUNTS — a
// gap the reap closes must drive ZERO rebuilds, and one that survives must drive exactly
// one — and a count is not assertable through a package-level function.
type rebuildDriver func(ctx context.Context, gt kgtypes.GraphType, name string) error

// repairDriver runs the BOUNDED repair arm over one graph — the arm that ships only
// the uncovered ids rather than rebuilding the corpus. It is the verdict's first
// remedy for a deficit the reap could not close.
//
// IT IS A FIELD ON THE CLIENT for the same reason rebuildDriver is: the properties
// under test are INVOCATION COUNTS — a deficit the bounded arm closes must drive ONE
// repair and ZERO rebuilds, one it does not close must drive one of each — and a count
// is not assertable through a package-level function.
type repairDriver func(ctx context.Context, gt kgtypes.GraphType, name string) (tools.RepairOutcome, error)

// buildBalanceFactory constructs the per-graph QUIESCENCE-EDGE closure the pipeline
// injects into each collector.
//
// IT LIVES IN BOOTSTRAP FOR THE REASON buildHealFactory DOES: this is the only layer
// where the pipeline, the segment manager and the tools coverage read are all visible,
// and the import direction is bootstrap→pipeline. fuseCaughtUp and the reap invocation
// are therefore consulted INSIDE the closure rather than by the collector.
//
// A GRAPH WITH NO REBUILDABLE SEGMENTS GETS A NIL CLOSURE, the same gate the heal
// factory applies, so the two arms cannot drift about which graphs they service.
func (c *client) buildBalanceFactory() func(kgtypes.GraphType, string) func(context.Context) error {
	return func(gt kgtypes.GraphType, name string) func(context.Context) error {
		if !kgtypes.HasRebuildableSegments(gt) {
			return nil
		}
		return func(ctx context.Context) error {
			ctx = graphclient.WithOperation(ctx, graphclient.OpSegmentHeal)
			c.evaluateBalanceAtQuiescence(ctx, gt, name)
			return nil
		}
	}
}

// evaluateBalanceAtQuiescence is THE VERDICT, and it is HEAL-FIRST rather than
// DECIDE-FIRST.
//
// THE FLOW, and every step of it is load-bearing:
//
//  1. the fuse must be caught up, or nothing is asserted at all
//  2. evaluate the balance over the locked operands
//  3. if resident < done, invoke the reap
//  4. RE-READ the operands and RE-EVALUATE
//  5. only an imbalance that SURVIVES the reap is a defect
//  6. a surviving deficit inside the bounded arm's band is repaired by that arm, and
//     RE-READ again; only a deficit that survives THAT drives the reset rebuild
//
// THE REAP RUNS ON resident < done BECAUSE IT LOWERS done. That is the dead-vector
// direction: the vector count counts tombstoned and orphan vectors, the distinct live
// resident count does not, so the class the heal-through design exists for presents as
// resident < done. Removing dead vectors moves done toward resident and closes the gap.
// This is the same philosophy already applied to a shortfall — allow it to heal, and
// only a persistent one is real — extended to the direction a heal can repair.
//
// THE REAP CANNOT OVERSHOOT INTO resident > done, and that is what makes the re-read
// safe rather than a new hazard. It removes only vectors belonging to tombstoned, proxy
// or orphan nodes, and the resident reader counts an id only when it is live-searchable
// — which none of those are. So the reap lowers done by a quantity that was never in
// resident: it can reach equality but never pass it.
//
// resident > done DOES NOT DRIVE THE REAP. Lowering done would WIDEN that gap, not
// close it. It has no automated heal here at all: with the distinct-and-live reader
// wired, duplication — the originally-named cause of that direction — is out of the
// equation entirely, and the remaining causes are local documents whose vectors no
// longer exist server-side, which the fuse gate above is supposed to have excluded. So
// it is reported immediately, with no reap and no rebuild. A signal that something is
// wrong in a way this arm cannot repair beats a repair that makes it worse.
//
// NEITHER REMEDY IS A FALLBACK. Each repairs the condition it fires for and returns
// the system to its primary path, and neither can fire forever on the same cause: the
// reap runs ONCE and step 5 declares a defect on a surviving imbalance instead of
// reaping again, and the bounded repair likewise runs ONCE and ESCALATES to the reset
// rebuild within the SAME quiescence edge when the deficit survives it. Both
// escalations conclude from a RE-READ rather than from the remedy's own accounting.
func (c *client) evaluateBalanceAtQuiescence(ctx context.Context, gt kgtypes.GraphType, name string) {
	// CONDITION (b) OF THE QUIESCENCE DESIGN. Fuse quiescence proves nothing UNFUSED is
	// owed. Without it a verdict formed here would report a corpus merely in flight as
	// short, and drive a rebuild for documents that were about to arrive.
	caughtUp, why, err := c.fuseCaughtUp(ctx, gt, name)
	if err != nil {
		slog.Warn("bootstrap: balance verdict declined — the fuse position could not be read",
			"graph_type", gt, "name", name, "reason", why, "error", err)
		return
	}
	if !caughtUp {
		slog.Debug("bootstrap: balance verdict not evaluated — the fuse is not caught up",
			"graph_type", gt, "name", name, "reason", why)
		return
	}

	before := c.evaluateArmBalance(ctx, gt, name)
	if before.verdict == armNotEvaluated {
		// A REFUSAL IS REPORTED, NOT SWALLOWED. "We could not measure" and "we measured
		// balanced" are different facts, and collapsing them is how a check that never
		// ran reads as healthy.
		slog.Info("bootstrap: balance verdict not evaluated at quiescence",
			"graph_type", gt, "name", name, "verdict", before.String())
		return
	}
	if before.verdict == armBalanced {
		slog.Debug("bootstrap: balance verdict at quiescence", "graph_type", gt, "name", name,
			"verdict", before.String())
		return
	}

	// resident > done: NO REAP, NO REBUILD, REPORTED IMMEDIATELY. Written as the
	// inequality rather than the bare word, because the word means the opposite thing in
	// the two readings that meet here and reading one as the other inverted this trigger
	// once already.
	if before.verdict == armSurplus {
		slog.Error("bootstrap: segment balance defect at quiescence — the local index holds MORE "+
			"live documents than the graph has vectors; no reap and no rebuild is attempted, "+
			"because lowering the vector count would widen this gap rather than close it",
			"graph_type", gt, "name", name, "verdict", before.String())
		return
	}

	// resident < done — the dead-vector direction. Heal first.
	// THE GAP IS TAKEN AGAINST owed, THROUGH ITS ONE DEFINITION. Re-deriving the
	// subtraction here would give the reap a different target from the one the verdict
	// judged the moment the two subtractions diverged.
	gap := before.owed() - before.resident
	if c.reaper == nil {
		slog.Warn("bootstrap: segment balance imbalance at quiescence with no reap invoker wired — "+
			"reporting it rather than concluding, because an unhealed gap is not evidence of a defect",
			"graph_type", gt, "name", name, "verdict", before.String())
		return
	}
	removed, rErr := c.reaper.ReapDeadVectors(ctx, gt, name, gap)
	if rErr != nil {
		slog.Warn("bootstrap: dead-vector reap failed at quiescence; the verdict is not concluded "+
			"(an unhealed gap is not evidence of a defect)",
			"graph_type", gt, "name", name, "gap", gap, "error", rErr)
		return
	}

	// THE RE-READ IS NOT OPTIONAL AND NOT INFERRABLE. Computing the post-reap balance by
	// subtracting `removed` from the pre-reap operands would be arithmetic standing in
	// for a measurement — and it would report balanced whenever the reap's own
	// accounting is wrong, which is exactly the class of bug this verdict exists to
	// catch. The reap's reported count is for the log line and for its own escalation
	// decision, never for the verdict.
	after := c.evaluateArmBalance(ctx, gt, name)
	switch after.verdict {
	case armBalanced:
		// THE REAP CLOSED THE GAP: no defect, and NO REBUILD. This is the dead-vector
		// direction the heal-through design exists for, and rebuilding a graph that has
		// just converged would be churn on a healthy corpus.
		slog.Info("bootstrap: dead-vector reap healed the segment balance at quiescence",
			"graph_type", gt, "name", name, "reap_removed", removed,
			"before", before.String(), "after", after.String())
	case armNotEvaluated:
		slog.Warn("bootstrap: the post-reap balance could not be re-read, so no verdict is concluded",
			"graph_type", gt, "name", name, "reap_removed", removed, "after", after.String())
	case armSurplus:
		// The reap CANNOT overshoot into resident > done — it removes only vectors
		// belonging to tombstoned, proxy or orphan nodes, none of which the distinct
		// live resident reader counts, so it lowers done by a quantity that was never in
		// resident and can reach equality but not pass it. Reaching here therefore means
		// something moved underneath the two reads. Report it; do not repair it.
		slog.Error("bootstrap: segment balance defect at quiescence — the post-reap re-read "+
			"reports the local index holding MORE live documents than the graph has vectors, "+
			"which the reap cannot produce; no rebuild is attempted",
			"graph_type", gt, "name", name, "reap_removed", removed,
			"before", before.String(), "after", after.String())
	default:
		// ONLY AN IMBALANCE THAT SURVIVED THE REAP IS A DEFECT, and a surviving
		// resident < done is a genuine SHORTFALL rather than a dead-vector inflation:
		// the reap ran first and found nothing to remove. That is the direction a
		// rebuild repairs, so it is driven here — exactly once, with the next
		// quiescence evaluation deciding whether it worked.
		//
		// THE TWO CAUSES SHARE THIS DIRECTION, which is why the ordering matters: a
		// short drain and dead vectors both present as resident < done. The reap is what
		// separates them, and it has already run.
		slog.Error("bootstrap: segment balance deficit SURVIVED the dead-vector reap — the local "+
			"index is genuinely short",
			"graph_type", gt, "name", name, "reap_removed", removed,
			"before", before.String(), "after", after.String())
		if c.repairBoundedDeficit(ctx, gt, name, after) {
			return
		}
		if c.rebuild == nil {
			slog.Warn("bootstrap: no rebuild driver wired, so the surviving deficit is reported and not repaired",
				"graph_type", gt, "name", name)
			return
		}
		if err := c.rebuild(ctx, gt, name); err != nil {
			slog.Warn("bootstrap: the balance-driven rebuild failed (best-effort; the next collect's "+
				"quiescence re-evaluates)", "graph_type", gt, "name", name, "error", err)
		}
	}
}

// repairBoundedDeficit offers a surviving deficit to the BOUNDED repair arm before the
// reset rebuild, and reports whether that arm closed it. A false return means the
// caller proceeds to the reset, which is what every pre-routing edge did.
//
// IT DOES NOT PASS THROUGH THE BACKSTOP SWEEP'S SCHEDULING, and that is a statement
// about which population each mechanism serves rather than a bypass. The backstop's
// record gate and its rotation exist to bound the cost of a PERIODIC sweep across every
// segment-bearing graph on the machine; a graph with a measured, quiescence-confirmed
// deficit is not a sweep candidate and does not queue behind one. RepairUncoveredSegments'
// own per-graph single-flight is what keeps the two routes from colliding on one graph.
//
// THE BAND OPERANDS ARE ONE OBSERVATION, NOT TWO AUTHORITIES. Both come from the SAME
// evaluateArmBalance call — owed from the coverage stats read and resident from the
// segment manager's own count — which is the property evaluateArmBalance exists to
// guarantee. owed() is used through its one definition, for the same reason the gap
// above is.
//
// A REPAIR THAT DID NOT RUN IS NOT EVIDENCE OF ANYTHING. The single-flight coalesce
// returns a zero outcome with a NIL error, so Ran is checked separately from err and
// both fall through to the reset.
func (c *client) repairBoundedDeficit(
	ctx context.Context, gt kgtypes.GraphType, name string, after armBalance,
) bool {
	if c.repairArm == nil {
		return false
	}
	embedded, covered := after.owed(), after.resident
	if !repairBandAdmits(embedded, covered) {
		// WHICH CLAUSE DECLINED IS LOGGED, because falling through to a full rebuild is
		// the expensive outcome and an operator needs to know why this graph took it.
		slog.Info("bootstrap: the surviving deficit is outside the bounded repair arm's band, so the "+
			"reset rebuild is driven instead",
			"graph_type", gt, "name", name, "embedded", embedded, "covered", covered,
			"below_floor", embedded < tools.SegmentCoverageFloor,
			"not_a_deficit", covered >= embedded,
			"below_ratio", float64(covered) < tools.CoverageRatioThreshold*float64(embedded))
		return false
	}

	out, err := c.repairArm(ctx, gt, name)
	if err != nil {
		slog.Warn("bootstrap: the bounded repair of a surviving deficit failed, so the reset rebuild is "+
			"driven instead", "graph_type", gt, "name", name, "error", err)
		return false
	}
	if !out.Ran {
		slog.Info("bootstrap: the bounded repair coalesced into a pass already running, so this edge "+
			"drives the reset rebuild rather than concluding from a pass it did not make",
			"graph_type", gt, "name", name)
		return false
	}

	// THE RE-READ IS THE VERDICT, never out.Shipped*. Arithmetic standing in for a
	// measurement reports closed whenever the arm's own accounting is wrong, which is the
	// class of bug this verdict exists to catch — the same rule the reap already obeys.
	repaired := c.evaluateArmBalance(ctx, gt, name)
	if repaired.verdict == armBalanced {
		slog.Info("bootstrap: the bounded repair arm closed the surviving deficit — no rebuild",
			"graph_type", gt, "name", name, "before", after.String(), "after", repaired.String())
		return true
	}
	slog.Warn("bootstrap: the bounded repair arm did NOT close the surviving deficit, escalating to the "+
		"reset rebuild in this same edge",
		"graph_type", gt, "name", name, "before", after.String(), "after", repaired.String())
	return false
}

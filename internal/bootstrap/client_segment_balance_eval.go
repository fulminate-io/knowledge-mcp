// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// client_segment_balance_eval.go reads the balance verdict's OPERANDS and forms the
// verdict from them. It lives beside client_segment_balance.go, which holds the pure
// arithmetic and the rendering, so the two stay separately testable: the predicate is
// answerable without a client, and this file is where every "could not measure" decision
// is made.

// evaluateArmBalance forms the exact HNSW balance verdict for one graph.
//
// EVERY UNREADABLE OPERAND RETURNS notEvaluated WITH A REASON, never a zero. A zero
// operand is a MEASUREMENT — "this graph holds no documents" — and a graph whose engine
// could not be read is not that. Collapsing the two is how a check that could not run
// reads as healthy, which is the failure this whole line of work exists to remove.
//
// THE ORDER IS CHEAPEST-DECLINE-FIRST. The evicted check costs a local map read and the
// coverage read costs an RPC, so declining an evicted pool ahead of the RPC keeps a
// background pass from paying for a verdict it will not form. It also keeps the
// distinct-resident read — which MATERIALIZES a pool it is allowed to load — off an
// evicted graph, the same prohibition healNeedsRebuildLocal observes.
//
// ONE STATS READ SUPPLIES THE done OPERAND, THE FAILURE COUNT AND ITS
// STILL-VECTORED SUBSET, through tools.GraphCoverageCounts, and ONE SEGMENT READ
// supplies both resident counts. Two reads would be two snapshots that can disagree
// about the same graph, and the verdict's tolerance is zero — a skew of one between
// any two of these numbers is indistinguishable from a real imbalance of one.
func (c *client) evaluateArmBalance(ctx context.Context, gt kgtypes.GraphType, name string) armBalance {
	if c.segmentMgr == nil {
		return notEvaluated("no segment manager is wired on this client")
	}
	if c.PoolEvicted(gt, name) {
		// AN EVICTED POOL IS NOT A MEASUREMENT. Its resident count reads zero because
		// this client's residency budget dropped the segments from RAM, not because the
		// documents are gone — the segments are intact on disk and the next search
		// reloads them. A verdict formed here would report the whole corpus missing.
		return notEvaluated("this client's residency budget has evicted the segment pool, so resident is unmeasurable")
	}

	cov, err := tools.GraphCoverageCounts(ctx, c.GraphCaller(), gt, name)
	if err != nil {
		return notEvaluated(fmt.Sprintf("the graph coverage counts could not be read: %v", err))
	}
	if !cov.Measurable {
		// The router-less / degraded-headless client, which answers a zero-valued
		// coverage set with no error. Treating that zero as `done` would compare a real
		// resident count against a fabricated zero and report a surplus on every graph.
		return notEvaluated("no stats seam is wired on this client, so the vector count is unmeasurable")
	}

	// ONE READ SUPPLIES BOTH RESIDENT OPERANDS, for the reason the stats read above
	// supplies both of its own: two calls are two snapshots of one engine, and the
	// DIFFERENCE between them is the duplication signal — so a ship or merge landing
	// between them is indistinguishable from a genuinely duplicated document.
	//
	// THE DISTINCT-AND-LIVE COUNT IS THE EQUATION'S OPERAND, deliberately. The SUMMING
	// count cannot serve there: it counts an id resident in two segments twice, which
	// under a zero-tolerance equation is a permanent resident > done whose only repair —
	// a rebuild — writes another segment and can raise the very operand being judged. It
	// is reported BESIDE the verdict instead, as the duplication term.
	shipped, resident, poolEvicted, err := c.segmentMgr.LoadSegmentDocCounts(ctx, gt, name)
	if err != nil {
		return notEvaluated(fmt.Sprintf("the resident doc counts could not be read: %v", err))
	}
	if poolEvicted {
		// The pool was evicted between the check above and this read. A zero pair from
		// an evicted pool is not a measurement, for the reason that check states.
		return notEvaluated("this client's residency budget evicted the segment pool mid-read, so resident is unmeasurable")
	}

	// COUNT PROVENANCE — WHERE owed AND resident COME FROM.
	//
	// owed is cov.Embedded, read above via tools.GraphCoverageCounts. That call builds
	// its selector with graphsel.GraphSelectorFor(gt, name, false)
	// (cmd/knowledge/internal/tools/manage_status_coverage.go:333), whose switch has NO
	// Branch arm — so the request reaches the server with an empty branch, the server
	// resolves it through resolveCode's Retrieve arm, and the number is the BASE
	// graph's counter.
	//
	// resident is the distinct-and-live count from LoadSegmentDocCounts above, keyed by
	// the same (gt, name).
	//
	// owed and resident are both keyed by (gt, name) with no branch dimension
	//
	// The consequence a later reader needs: a code repo carrying a full branch overlay
	// has a SECOND counter that this comparison does not read and must not read, since
	// a collapsed branch scope serves that overlay as its own base. A verdict formed
	// here is therefore a statement about the base graph and about nothing else.
	b := balancedAtQuiescence(resident, cov.Embedded, cov.EmbedFailures,
		cov.EmbedFailuresHoldingVector, cov.EmbedFailuresHoldingVectorMeasured)

	// THE OPERAND CAVEAT, carried on the verdict itself so the numbers are never read as
	// exact when they are not. IT RETIRES ON PRESENCE OF THE SUBSET COUNT, NEVER ON ITS
	// VALUE: a server that omits the count is not a server reporting none, and treating
	// its absence as a measured zero would retire the caveat against a number nobody
	// produced — the exact "bad or absent input is labeled, not defaulted" rule.
	//
	// WHAT THE APPROXIMATION IS, on a server that omits it. The failure count is an
	// OVER-APPROXIMATION of what owed should subtract: it counts every live node carrying
	// an embed-failure marker with no regard for whether that node HOLDS a vector. Only
	// the ones that DO are inside `done`; subtracting the rest removes something that was
	// never there, and the equation absorbs a real shortfall of the same size — a genuine
	// gap reads BALANCED.
	if cov.EmbedFailures != 0 && !cov.EmbedFailuresHoldingVectorMeasured {
		b.reason = fmt.Sprintf(
			"operands approximate: %d marked failures were subtracted from the vector count, "+
				"but this server does not report how many of them still hold a vector, so an "+
				"unknown number were never counted in it — a real shortfall of up to that size "+
				"would read balanced",
			cov.EmbedFailures)
	}

	// DUPLICATION IS ITS OWN SIGNAL AND NEVER ENTERS THE EQUATION — see the armBalance
	// type for why folding it in would restore the non-convergence the distinct reader
	// exists to remove. It is the summing (shipped) count minus the distinct (live) one,
	// and BOTH are carried so the reported quantity names its operands in the same
	// grammar manage(status) prints for the same pair.
	//
	// IT IS MEASURED RATHER THAN DERIVED FROM A SECOND READ. The pair came off ONE
	// observation of one engine above, so there is no second snapshot for the difference
	// to be taken across — which is what makes the term exact rather than an artifact of
	// read ordering. The operands are carried unconditionally; whether the DIFFERENCE is
	// trustworthy is decided immediately below.
	b.duplicationMeasured = true
	b.shipped, b.live = shipped, resident
	// THE GUARD IS `>` RATHER THAN AN UNGUARDED SUBTRACTION, and it is carried over
	// UNCHANGED from the two-read form. The summing count is structurally at or above
	// the distinct one — it counts the same membership with duplicates and with
	// deleted-but-unpurged ids — so a negative difference is not a negative
	// duplication, and rendering it as "-3 duplicated resident documents" would report
	// a nonsense quantity as a finding.
	if shipped > resident {
		b.duplication = shipped - resident
	}
	return b
}

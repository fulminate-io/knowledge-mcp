// SPDX-License-Identifier: Apache-2.0

// manage_status_coverage_evicted.go — the `evicted` coverage band and the optional
// deps capability the table reads it through.
//
// It is a separate file because manage_status_coverage.go sits against a hard
// 500-line cap that the repo's pre-commit hook enforces, so everything that can live
// outside it does. What stays in place there is only what the band's mechanism
// requires: the CoverageRow input field, the one switch arm, the legend clause, and
// the reporter's residency fence.

package tools

import (
	"fmt"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// DispositionEvicted is the band for a graph whose segment pool THIS CLIENT'S
// RESIDENCY BUDGET has dropped from memory. It is a memory-management state, not a
// fault: the segments are intact on local disk, and the next consumer search
// re-materializes them from there with no server round-trip. It needs no operator
// action.
//
// IT IS A BAND OF ITS OWN because every other band would be a lie about the same
// row. An evicted pool reads a live-resident count of 0 by construction, which is
// exactly the shape `self-healing` and `gap-repairing` describe — so without this
// arm an evicted pool renders as a graph an arm is repairing, and the legend for
// self-healing promises the row "resolves within one reconcile interval" when in
// fact no arm will touch it until a user searches the graph.
//
// BRANCH ORDER, which is load-bearing in both directions:
//
//   - It must FOLLOW the no-segments and unmanaged arms, for the reasons those two
//     already give: a graph with no pool has no residency to report, and a graph
//     outside the working set is unmanaged whether or not its pool is resident.
//   - It must PRECEDE every arm below it — residue, converged, below-floor, stuck
//     and the ratio arms — because all of them read LiveResident, and on this row
//     that figure describes EVICTION rather than coverage. It is the same argument
//     the unmanaged arm makes for itself, one layer in. Precedence over the
//     `LiveResident == Embedded` arm matters concretely: without it an evicted pool
//     on a zero-embedded graph would read `converged`.
const DispositionEvicted = "evicted"

// poolEvictedReader is the OPTIONAL deps capability the evicted band reads through.
//
// It is TYPE-ASSERTED rather than added to ClientDeps or SegmentCoverageReader for
// the reason segmentStallReader and workingSetReader state
// (manage_status_coverage_collect.go): a required method would have to be
// implemented by every fake that already implements SegmentCoverage() — twenty-five
// of them — none of which runs a residency budget.
type poolEvictedReader interface {
	PoolEvicted(gt kgtypes.GraphType, name string) bool
}

// poolEvictedFor reads residency through the optional seam.
//
// AN UNWIRED DEPS REPORTS FALSE — not evicted — which is the OPPOSITE literal from
// inWorkingSetFor's true default, and the difference is not an inconsistency: the
// two predicates are opposite in SENSE and both defaults serve the same goal, which
// is to leave a deps that cannot answer with exactly its pre-existing bands. A
// fixture running no residency budget has no evicted pools, so false is the true
// statement about it; defaulting to true would relabel every row in every such
// fixture with a band about a mechanism that is not present.
func poolEvictedFor(deps ClientDeps, gt kgtypes.GraphType, name string) bool {
	r, ok := deps.(poolEvictedReader)
	if !ok {
		return false
	}
	return r.PoolEvicted(gt, name)
}

// segCoveredForEvicted is the reporter's half of the residency fence: the (covered,
// liveResident, hasSeg) triple an EVICTED pool's row is built from.
//
// WHY THE REPORTER NEEDS A FENCE AT ALL. segCoveredFor's probe
// (ShippedSegmentDocCount) routes on the OSS rail to LoadResidentDocCount, which
// deliberately MATERIALIZES an evicted pool — it has to, because the unified-search
// completeness gate consumes the same probe and its verdict is search-visible and
// cached. So without this fence every manage(status) an operator runs would
// re-materialize every evicted OSS pool and silently undo the whole policy.
//
// THE TRIPLE IS HONEST RATHER THAN CONVENIENT. hasSeg stays TRUE so the row still
// renders a segment cell instead of collapsing to the bare dash reserved for graphs
// with no pool at all; both counts are 0 because nothing was measured, which is what
// the row then says — "shipped 0 · live 0 [evicted]".
func segCoveredForEvicted() (covered, liveResident int, hasSeg bool) {
	return 0, 0, true
}

// coverageBandTerm renders the bracketed band term: the bare disposition for every
// band, plus a stall age for the stuck one. The age is rounded to the minute — the
// question it answers is "has this been true for a while", which minutes settle and
// seconds only clutter.
//
// It lives beside the band vocabulary's newest member rather than beside the table
// renderer because it is the one place a band can render MORE than its name, so a
// future band that needs extra detail is added here next to the reasoning for why
// `evicted` does not.
func coverageBandTerm(r CoverageRow) string {
	if r.SegDisposition != DispositionStuck || r.StalledSinceNanos == 0 {
		return r.SegDisposition
	}
	return fmt.Sprintf("%s %s", r.SegDisposition,
		time.Since(time.Unix(0, r.StalledSinceNanos)).Round(time.Minute))
}

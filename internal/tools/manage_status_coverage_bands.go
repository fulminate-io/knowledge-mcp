// SPDX-License-Identifier: Apache-2.0

// manage_status_coverage_bands.go — the coverage BAND vocabulary and the classifier
// that assigns one to a row.
//
// It is a separate file for the same reason manage_status_coverage_evicted.go is:
// manage_status_coverage.go sits against a hard 500-line cap the repo's pre-commit
// hook enforces, and the band machinery is the largest self-contained unit in it. The
// split is a PURE MOVE — the constants and segCoverageDisposition are byte-identical
// to what they were in that file, so a reviewer diffing this change reads a
// relocation rather than a rewrite.
//
// WHAT STAYS BEHIND: CoverageRow (the classifier's input), the renderer, and the
// collection helpers. Those are the row's shape and its presentation; this file is
// the judgment made about it.

package tools

// Coverage dispositions — the locked vocabulary segCoverageDisposition returns.
// They exist so an operator can tell a TRANSIENT drain lag from a PERMANENT hole:
// the two are visually identical without them, because a row covering far less
// than it has embedded renders exactly like a healthy one.
const (
	DispositionNoSegments   = "—"
	DispositionResidue      = "residue"
	DispositionConverged    = "converged"
	DispositionBelowFloor   = "below-floor"
	DispositionSelfHealing  = "self-healing"
	DispositionGapRepairing = "gap-repairing"
	// DispositionCacheAged is the honest answer for a row whose band the coverage
	// BACKSTOP has not verified. Under the merge architecture the backstop runs on a
	// slow interval rather than every boot, so "gap-repairing" would assert an
	// examination that may not have happened this process at all.
	DispositionCacheAged = "cache-aged"
	// DispositionStuck is the band for a graph this client maintains whose coverage
	// can no longer recover on its own: the heal breaker has latched it disarmed
	// (only a manual rebuild_segments or a restart re-arms). That state can persist
	// indefinitely, so the row renders an age rather than a promise.
	DispositionStuck = "stuck"
	// DispositionUnmanaged is the band for a graph OUTSIDE the working set: no
	// background arm services it, so no arm will move it. It is not a fault and not a
	// stall — it is the intended state for a graph this client has not been asked to
	// work on — which is why it is a band of its own rather than either neighbor.
	DispositionUnmanaged = "unmanaged"
)

// segCoverageDisposition classifies one coverage row into the band it sits in.
//
// IT CLASSIFIES ON LiveResident, NOT SegCovered, and that is load-bearing rather
// than cosmetic. SegCovered is the WITH-DUPLICATES summed resident count (formerly
// a server manifest sum); the repair arm triggers on the LIVE searchable count.
// Classifying on the with-duplicates number would let this column read "converged"
// for a graph the arm is actively repairing, and "gap-repairing" for one it skips —
// the column would narrate a different graph than the system is acting on.
//
// BRANCH ORDER IS THE DESIGN, and three orderings are load-bearing rather than
// incidental:
//
//   - The residue arm must precede the ratio arms: when live exceeds embedded the
//     ratio test is ALSO true, so a classifier checking the band first would label
//     the hard-delete residue class as this gate's under-coverage hole.
//   - The unmanaged arm must FOLLOW the no-segments arm. A graph with no segment
//     pool has no coverage to manage, and its disposition IS the bare dash
//     (formatCoverageRow), so labeling it unmanaged would replace a correct answer
//     with a redundant one for every non-segment graph in the account.
//   - The stuck arm must PRECEDE the ratio arm. A latched or suppressed graph sits
//     in exactly the resident-below-ratio shape, so the two arms collide by
//     construction; whichever runs first wins. Placing stuck second would leave the
//     ratio arm calling the row self-healing — the claim this band exists to stop,
//     because nothing is healing it.
//
// The band arms delegate to SegmentCoverageFloor and CoverageRatioThreshold so the
// column and the auto-heal cannot disagree about which graphs self-heal.
//
// The honesty of the residue, converged, below-floor and ratio arms is exactly the
// honesty of LiveResident, which is the DISTINCT live-searchable count rather than
// the summed residency figure.
func segCoverageDisposition(r CoverageRow) string {
	switch {
	case !r.HasSegments:
		return DispositionNoSegments
	case !r.InWorkingSet:
		// No arm services a graph outside the working set, so no band describing what
		// an arm is doing about it can be true.
		return DispositionUnmanaged
	case r.Evicted:
		return DispositionEvicted
	case r.LiveResident > r.Embedded:
		// Live exceeds embedded: the hard-delete residue class, a different gate's
		// territory than this column's under-coverage band.
		return DispositionResidue
	case r.LiveResident == r.Embedded:
		return DispositionConverged
	case r.Embedded < SegmentCoverageFloor:
		// Below the floor the ratio arm is disarmed entirely; only the
		// zero-presence probe heals there.
		return DispositionBelowFloor
	case r.StalledSinceNanos > 0:
		// The graph is in the working set and below coverage, but the arm that would
		// heal it has given up — a latched breaker. Reporting the ratio band here would
		// promise a recovery no arm is attempting.
		return DispositionStuck
	case float64(r.LiveResident) < CoverageRatioThreshold*float64(r.Embedded):
		return DispositionSelfHealing
	default:
		// ONLY THIS ARM CONSULTS THE BACKSTOP, and the restriction is the honesty
		// argument rather than caution. Every arm above is computed from LiveResident
		// and Embedded, which are LIVE readings taken this call — nothing about them is
		// cache-aged. What is unverified is specifically the claim THIS arm makes: that
		// the row's shortfall is a gap the repair arm is servicing. When the backstop
		// has not looked, that claim is unsupported, and this is the one cell that
		// should say so.
		if !r.RepairVerified {
			return DispositionCacheAged
		}
		return DispositionGapRepairing
	}
}

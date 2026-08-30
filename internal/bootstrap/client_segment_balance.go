// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"strings"
)

// client_segment_balance.go holds the EXACT per-arm health verdict and its
// rendering. It lives beside client_segment_heal_need.go, which holds the ratio
// band this verdict is the successor to, and in its own file so that one stays
// inside its context cap.

// armVerdict is the balance verdict's four-valued outcome.
//
// FOUR VALUES, NOT A BOOL, and the fourth is the one that matters. A bare bool
// cannot carry "not evaluated", and collapsing that into "balanced" is precisely
// how a check that could not run reads as healthy — which is the failure this
// whole line of work exists to remove, arriving through the verdict itself.
type armVerdict int

const (
	// armNotEvaluated is the answer when the verdict could not be formed at all:
	// an operand was unreadable, or the preconditions for evaluating it were not
	// met. It is NEVER an alias for healthy.
	armNotEvaluated armVerdict = iota
	armBalanced
	// armDeficit is resident < done. See the sign convention on armBalance.
	armDeficit
	// armSurplus is resident > done. See the sign convention on armBalance.
	armSurplus
)

// armBalance is the exact per-arm health verdict plus the operands it was formed
// from, so a consumer can render WHY rather than only WHAT.
//
// === THE SIGN CONVENTION. THIS IS ITS ONE AUTHORITATIVE DEFINITION. ===
//
//	DEFICIT  == resident <  done   (the local index holds FEWER live documents
//	                               than the graph has vectors: loss, or dead
//	                               vectors inflating done)
//	SURPLUS  == resident >  done   (the local index holds MORE live documents
//	                               than the graph has vectors)
//	BALANCED == resident == done   (after subtracting the marked failures that are
//	                               actually inside done — see the owed method, which
//	                               is the one authoritative definition of WHICH ones)
//
// THE BARE WORD "SURPLUS" IS AMBIGUOUS AND MUST NOT BE USED ALONE — not in
// comments, not in log lines, not in test names. It can mean "the resident side is
// in surplus" (this convention) or "the done side is in surplus" (the phrasing in
// which done being too high is the condition a dead-vector reap exists for). Those
// are OPPOSITE inequalities, and reading one as the other once already inverted a
// reap trigger, pinning it off for exactly the condition it was built for. ALWAYS
// WRITE THE INEQUALITY.
//
// WHICH SIGN THE DEAD-VECTOR CLASS PRODUCES, since it is the one a heal is for:
// dead vectors are counted by done (the vector count is unfiltered) and never by
// resident (the resident reader is distinct and live), so the class is
// resident < done — the DEFICIT direction under this convention.
//
// DUPLICATION IS ITS OWN SIGNAL AND NEVER ENTERS THE EQUATION. It is the summing
// resident count minus the distinct one — exactly the cross-segment double-counting
// the summing reader carries — reported BESIDE the verdict. Folding it into the
// balance would restore the non-convergence the distinct reader exists to remove
// (its repair, a rebuild, writes another segment and can raise the very operand
// being judged); dropping it would discard the duplication half of the health
// question entirely.
type armBalance struct {
	verdict armVerdict
	// resident is the DISTINCT-and-live local document count. done is the graph's
	// vector count, unchanged and with no compensating arithmetic applied — it is
	// made honest by the corpus converging, not by the equation subtracting.
	resident int
	done     int
	// failures is the count of nodes marked as permanently failed. It is CARRIED so a
	// corpus that legitimately shrank stays readable as such; what is SUBTRACTED from
	// done is failuresHoldingVector — see the owed method for the whole argument.
	failures int
	// failuresHoldingVector is the subset of failures whose nodes still HOLD a vector,
	// and failuresHoldingVectorMeasured says whether the server supplied it.
	//
	// AN ABSENT COUNT IS NOT A ZERO ONE. A server that does not produce this subset
	// leaves both fields zero, and reading that zero as a measurement would silently
	// promote the approximate equation to an exact one. The flag is what keeps the
	// exact form gated on a number somebody actually measured.
	failuresHoldingVector         int
	failuresHoldingVectorMeasured bool
	// shipped and live are the two counts the duplication term is formed from — the
	// same pair manage(status) renders as "shipped N · live M".
	//
	// THEY ARE CARRIED RATHER THAN ONLY THEIR DIFFERENCE so the reported quantity
	// names its operands. A lone difference cannot be checked against the status
	// table by the operator reading both, and it cannot distinguish a large duplicated
	// pool from a small one.
	shipped int
	live    int
	// duplication is the summing (shipped) resident count minus the distinct (live)
	// one. Reported, never compared.
	duplication int
	// duplicationMeasured reports whether the shipped/live pair that produces
	// `duplication` was actually READ.
	//
	// ITS FALSE CASE IS NOW REACHABLE ONLY ON A NOT-EVALUATED VERDICT. The two counts
	// used to come from two separate reads, so a verdict could form off the first while
	// the second failed; they now come from ONE read (LoadSegmentDocCounts), which
	// either produces both operands or fails the whole verdict. An EVALUATED verdict
	// therefore always carries a measured duplication, and the "duplication unmeasured"
	// clause the renderer still emits has no production path left to produce it. That
	// is recorded rather than deleted here because retiring the signal is a decision
	// about a safety surface, not a side effect of consolidating a read.
	//
	// WITHOUT IT A LOST READ IS INDISTINGUISHABLE FROM A CLEAN GRAPH, because both
	// leave duplication at zero — and the renderer omits a zero clause, so the loss
	// would be silent at exactly the surface built to make things visible. That is the
	// same "we could not measure" versus "we measured zero" distinction armNotEvaluated
	// exists for, applied to the signal reported beside the verdict rather than to the
	// verdict itself. The BALANCE still forms: duplication never enters the equation, so
	// losing it costs the signal and not the measurement.
	duplicationReason   string
	duplicationMeasured bool
	// reason carries a not-evaluated verdict's explanation, and on an EVALUATED verdict
	// it carries any CAVEAT that qualifies the numbers — see the operand-approximation
	// note in evaluateArmBalance. Empty means the verdict is unqualified.
	reason string
}

// balancedAtQuiescence is the exact per-arm health verdict. Its tolerance is ZERO
// in both directions, deliberately: a band is what allows a clog that never heals,
// and margin on this check is temporal — persistence across ticks, evaluated at
// quiescence — never numeric.
//
// THE FAILURE SUBTRACTION IS ON THE done SIDE because a marked failure's vector, when
// it has one, is counted in done and is refused by every segment build — so it is no
// longer work the local index is expected to hold. Subtracting it from resident
// instead would claim the index holds documents it does not.
//
// WHICH failures ARE SUBTRACTED IS THE WHOLE QUESTION, and the owed method below is
// its one authoritative answer. It is NOT all of them.
func balancedAtQuiescence(resident, done, failures, failuresHoldingVector int, holdingMeasured bool) armBalance {
	b := armBalance{
		resident: resident, done: done, failures: failures,
		failuresHoldingVector:         failuresHoldingVector,
		failuresHoldingVectorMeasured: holdingMeasured,
	}
	owed := b.owed()
	switch {
	case resident == owed:
		b.verdict = armBalanced
	case resident < owed:
		b.verdict = armDeficit
	default:
		b.verdict = armSurplus
	}
	return b
}

// owed is the quantity resident is compared against: how many documents the local
// index is actually expected to hold. IT IS THE ONE DEFINITION — the predicate, the
// renderer and the quiescence gap all call it, so no two of them can subtract a
// different set.
//
// === WHICH MARKED FAILURES LEAVE THE done SIDE, AND WHY IT IS THE HOLDING ONES ===
//
// `done` is the graph's UNFILTERED vector count, so a marked failure is inside it
// exactly when that node still HOLDS a vector. Those nodes can never be inside
// `resident`: the segment-rebuild scan requires vector possession AND an EMPTY
// embed-failure marker of every live row it emits (see visitLive / visitBranchProxy
// in cmd/knowledge-server/internal/store/composite_db_segment_rebuild.go), so a
// vectored node carrying the marker is excluded from every segment this client
// builds. In done, never in resident — precisely what owed must drop.
//
// THE MARKED FAILURES THAT HOLD NO VECTOR ARE NOT SUBTRACTED, and getting that
// backwards is the masking defect this method exists to remove. They were never
// counted in done, so removing them takes out something that was never there; the
// equation then absorbs a real shortfall of the same size and a genuine gap reads
// BALANCED. Measured shape: 8 resident against 10 vectors with 2 marked failures
// holding none is a 2-document shortfall, and subtracting both reported it balanced.
//
// AN UNMEASURED SUBSET FALLS BACK TO THE APPROXIMATE FORM RATHER THAN TO ZERO. A
// server that does not report the subset is not reporting none of them, and reading
// its silence as a measured zero would present the approximate answer as the exact
// one. The verdict carries a caveat in that case; see evaluateArmBalance.
func (b armBalance) owed() int {
	if b.failuresHoldingVectorMeasured {
		return b.done - b.failuresHoldingVector
	}
	return b.done - b.failures
}

// notEvaluated builds the refusal verdict, which always carries its reason. A
// caller that cannot read an operand returns this rather than a zero-valued
// balance, so "we could not measure" never renders as "we measured zero".
func notEvaluated(reason string) armBalance {
	return armBalance{verdict: armNotEvaluated, reason: reason}
}

// String renders the verdict for a log line or a status surface.
//
// IT NAMES THE INEQUALITY, NOT THE BARE WORD, for the reason the sign convention
// above gives at length.
//
// THE FAILURE AND DUPLICATION CLAUSES APPEAR ONLY WHEN NON-ZERO, and that is a
// requirement rather than tidiness. Carrying a count in the struct and never
// printing it satisfies the "failures stay visible" rule only on paper — a corpus
// that shrank because nodes permanently failed has to be readable as such. But a
// clause printed on every healthy graph is noise that trains a reader to skip the
// line, which costs the same visibility by a different route.
func (b armBalance) String() string {
	var sb strings.Builder
	if b.verdict == armNotEvaluated {
		sb.WriteString("not evaluated")
		if b.reason != "" {
			sb.WriteString(": " + b.reason)
		}
		return sb.String()
	}

	// THE COMPARED QUANTITY IS NAMED "owed", NOT "done", and when failures are
	// non-zero the subtraction is SHOWN inline. Rendering `done 5` beside a separate
	// `with 2 marked failures` clause invites the reader to ADD them back to 7 — the
	// arithmetic runs the other way, and a reader who reconstructs it wrongly
	// concludes the operands disagree on a graph that balances exactly. Naming the
	// compared quantity and showing its derivation removes the ambiguity rather than
	// relying on the reader to guess the sign.
	owed := b.owed()
	owedStr := fmt.Sprintf("owed %d", owed)
	switch {
	case b.failures == 0:
		// Nothing was subtracted and there is no derivation to show.
	case b.failuresHoldingVectorMeasured:
		// THE EXACT FORM NAMES THE SUBSET AND THE WHOLE, because the subtracted
		// number is smaller than the marked-failure count a reader can see elsewhere
		// (manage(status)'s own embed-failure column), and an unexplained mismatch
		// between two surfaces reads as a bug in one of them.
		owedStr = fmt.Sprintf("owed %d (done %d − %d of %d marked failures still holding a vector)",
			owed, b.done, b.failuresHoldingVector, b.failures)
	default:
		owedStr = fmt.Sprintf("owed %d (done %d − %d marked failures)", owed, b.done, b.failures)
	}
	switch b.verdict {
	case armBalanced:
		fmt.Fprintf(&sb, "balanced (resident %d == %s)", b.resident, owedStr)
	case armDeficit:
		fmt.Fprintf(&sb, "deficit (resident %d < %s)", b.resident, owedStr)
	case armSurplus:
		fmt.Fprintf(&sb, "surplus (resident %d > %s)", b.resident, owedStr)
	}
	// THE DUPLICATION CLAUSE NAMES ITS OPERANDS in the SAME grammar manage(status)
	// uses for the same pair ("shipped N · live M"), so an operator reading both
	// surfaces is reading one measurement rather than reconciling two spellings.
	if b.duplication != 0 {
		fmt.Fprintf(&sb, ", with %d duplicated resident documents (shipped %d · live %d)",
			b.duplication, b.shipped, b.live)
	}
	// A LOST DUPLICATION READ IS SAID OUT LOUD. Omitting it would render exactly like a
	// graph with no duplication, which is the silent-loss shape this file's doctrine
	// refuses everywhere else.
	if !b.duplicationMeasured {
		sb.WriteString(", duplication unmeasured")
		if b.duplicationReason != "" {
			sb.WriteString(": " + b.duplicationReason)
		}
	}
	// AND SO IS ANY CAVEAT ON THE OPERANDS THEMSELVES. An evaluated verdict that carries
	// a reason is one whose numbers are qualified; printing the numbers without the
	// qualification is how an approximation gets read as an exact result.
	if b.reason != "" {
		sb.WriteString(" [" + b.reason + "]")
	}
	return sb.String()
}

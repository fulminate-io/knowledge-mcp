// SPDX-License-Identifier: Apache-2.0

// manager_bucket_bound_latch.go — WHEN THE RESIDENT-SEGMENT BOUND IS RE-PAID: the
// census latch and the two pieces of arithmetic that decide it.
//
// SPLIT OUT OF manager_bucket_bound.go at the repository's hard 500-line cap, along
// the seam the two halves already had. That file is the consolidation WALK — the
// crossing gate, the ordered candidates, the per-partition swap and what it does when
// one fails. Everything here answers a different question: how far below the budget a
// consolidation drives, and whether the census that just ran bought enough for the
// next crossing to pay for another one. The walk calls all three and none of them
// calls the walk.

package segmentdist

import (
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// censusOutcome is what one census did, in the four numbers the latch's rule is
// written on: where the count stood when it began, where it left it, how many
// partitions it consolidated and how many segments a failure withdrew, plus the
// reduction it had to reach for its cost to have bought anything.
//
// IT IS A STRUCT RATHER THAN FIVE POSITIONAL ARGUMENTS because they are five ints and
// a reader of the call site cannot tell observed from after, or consolidated from
// withdrew, by position alone.
type censusOutcome struct {
	observed     int
	after        int
	consolidated int
	withdrew     int
	minReduction int
	// gen is the replacement generation this census SNAPSHOTTED WHEN IT BEGAN, read
	// beside the observed count. It is not a number about the census's own result: it
	// is how settleCensusLatch tells a census that walked THIS resident set from one
	// that walked a set a swap has retired since (distManager.replacementGen).
	gen int64
}

// settleCensusLatch records whether the census that just ran may be SKIPPED at the
// next crossing, and the rule is about what that census BOUGHT rather than where it
// left the count.
//
// WHAT COUNTS AS ACTING IS TWO THINGS, NOT ONE. A consolidation is the obvious one.
// The other is a WITHDRAWAL: a consolidation that failed on a corruption had that
// constituent withdrawn from the published set before its error came back, so the
// partition that just failed is consolidatable NOW and the candidate set is not the
// one this census walked. Counting that as a census which learned nothing would arm
// the latch and defer the retry by a whole slack of growth — which is bounded and
// self-healing, and was still a header claiming the partition is consolidated at the
// next crossing while the code deferred it. The caller passes the sum.
//
// A CENSUS THAT REDUCED THE COUNT BY minReduction CLEARS THE LATCH. It lowered the
// count the next crossing is compared against by enough to be worth the walk, so
// censusing there walks a set the seals have since grown rather than re-learning this
// one. The census-rate ceiling is unchanged by that: a crossing cannot recur until
// the seals have climbed back over the budget from the lower count this call left
// behind, which is what the ceiling is derived from in the first place.
//
// A CENSUS THAT CONSOLIDATED EVERY PARTITION AND BOUGHT LESS THAN THAT ARMS IT, and
// that is the v0.10.6 defect this rule closes. Acting is not the same as reducing:
// where most resident segments span several partitions (segments sealed before a
// doubling do, by construction), the count can never fall below the spanning backlog
// plus one output per partition, and that floor can sit above the low-water mark. The
// census then consolidates all 128 partitions, removes exactly the tails that arrived
// since the last one, lands back on the same floor — and, having acted, cleared the
// latch, so the next crossing a few hundred seals later pays the whole thing again.
// Measured on the release candidate: twelve consecutive censuses logging
// `partitions_consolidated=128 partitions_failed=0 resident_segments=3675
// low_water=3072`, identical every time, 98 of the bound's 99 CPU seconds inside
// their ReplaceBucket merges and none of it in the walk. The clear rule therefore
// asks what the census BOUGHT, and minCensusReduction says how much that has to be.
//
// A CENSUS THAT NEITHER CONSOLIDATED NOR WITHDREW ANYTHING ARMS IT, AT THE BUDGET
// RATHER THAN AT WHAT IT SAW.
// That is the stuck engine the gate exists for — every remaining candidate spans
// several partitions, nothing a per-partition swap may take will appear until the
// set has grown, and re-walking every live member on the next batch would learn
// exactly that again.
//
// THE ARMING POINT IS THE COUNT THE CENSUS LEFT, clamped to the budget. For a census
// that took nothing those are the same number, which is why this is not a change to
// the rule above; for one that took something and still bought too little, the count
// it left is where the set actually stands, and it is the TIGHTER of the two — the
// gate re-admits after a slack of growth from there rather than from a higher number
// the set no longer holds.
//
// ARMING IT AT THE OBSERVED COUNT MADE IT A RATCHET, WITH NO CEILING AT ALL, and
// that is the defect this clamp closes. The gate admits `last + slack` without
// censusing, so an arming point that FOLLOWS the observed count raises the admitted
// count by a whole slack on every stuck crossing. Measured on the v0.10.6 release
// candidate, where one corrupt segment made every census take nothing: 19
// consecutive stuck crossings at a slack of 1,024 took the resident count from
// ~4,042 to 22,474 against a budget of 4,096 — 5.5x the budget, ended by a rebuild
// rather than by this bound. THE INVARIANT IT RESTORES: the arming point is never
// above the budget, so the count this gate will admit without censusing is never
// above budget + slack, whatever a failed census observed. Measured here at
// bucketCount 2 (budget 4,096, slack 16) by
// TestResidentBoundLatchNeverRatchetsPastBudgetPlusSlack: five stuck crossings
// arming at 4,096 every time, then a trickle of four new segments — fewer than one
// slack — censused and consolidated at 4,181, where a latch armed at the ratcheted
// 4,161 declined to census until 4,177 and left the whole set resident.
//
// ABOVE THE CEILING THE CENSUS IS PAID PER BATCH, deliberately. An engine sitting
// over budget + slack has already lost the property this bound exists to hold, so
// re-walking its members to try again is the cheaper of the two mistakes — and
// since a census now SKIPS a failing partition rather than abandoning the walk,
// each of those censuses consolidates everything it still can.
//
// KEYING IT ON THE LOW-WATER MARK IS THE DEFECT THIS REPLACES, and the difference is
// not a corner: a consolidation reaches the mark only when the fullest partitions
// overshoot it, so one that merely ran out of candidates above the mark used to arm
// the latch at its PRE-consolidation count — a number already over budget. Measured
// here on the mock engine at bucketCount 2 (budget 4,096, low-water 4,080, slack 16)
// by TestResidentBoundReCensusesAfterAPartialConsolidation: a first bound took the
// one candidate partition and landed at 4,096, twelve consolidatable segments then
// arrived, and the second crossing at 4,108 returned at the gate without censusing —
// `4105 -> 4096 ... second crossing observed 4108 -> 4108 over 1 censuses`. The
// sustained count then climbed to last+slack before a census was re-paid, so the
// ceiling this bound actually held was budget PLUS the slack rather than the budget.
func settleCensusLatch[Q, S any](dm *distManager[Q, S], c censusOutcome) {
	// A CENSUS THAT WAS OVERTAKEN BY A REPLACEMENT SETTLES NOTHING, and this is the
	// first thing the rule asks because every line below it is a statement about the
	// set the census walked. Between this census's `after` read and this call, a layer
	// or group swap may have replaced that set and cleared the latch
	// (clearCensusLatchOnReplacement bumps the generation before it clears): storing
	// either number here would arm the retired floor over the new set and re-open the
	// deferral the clear exists to close. The floor is skipped with it — a floor from a
	// set that is gone is exactly what minCensusReduction must not compare against.
	if dm.replacementGen.Load() != c.gen {
		return
	}
	// THE FLOOR IS RECORDED WHATEVER THE RULE DECIDES, because the next census
	// compares its own reduction against this one's result whether or not this one
	// cleared the latch.
	dm.lastCensusFloor.Store(int64(c.after))
	if c.withdrew > 0 || (c.consolidated > 0 && c.observed-c.after >= c.minReduction) {
		dm.lastCensusCount.Store(0)
		return
	}
	// The clamp is what makes this a CEILING rather than a ratchet. The count this
	// census left is always at or below what it observed, and observed is always over
	// the budget here — the bound returns at its own gate otherwise — so the arming
	// point is never above the budget and the gate can admit at most budget + slack
	// without censusing. It is written as a minimum rather than as the constant so
	// that a census which left a SMALLER count arms at that count, which is the
	// tighter of the two.
	dm.lastCensusCount.Store(int64(min(c.after, searchengine.ResidentSegmentFanoutBudget)))
}

// clearCensusLatchOnReplacement discards the latch and its floor because the resident
// set they were taken over HAS BEEN REPLACED, so the first census on the set that
// replaced it runs at the ordinary gate.
//
// THE LATCH IS A CLAIM ABOUT ONE RESIDENT SET, and settleCensusLatch is the only
// writer that can KNOW that claim is still true: it is written by a census, about the
// set that census walked. Every rule above is about which censuses may be skipped over
// THAT set — the arming point is the count it left, the floor is what the next
// census's reduction is compared against, and the gate admits a slack of growth above
// the arming point. Replace the set underneath all three and every one of them is a
// statement about segments that are no longer resident.
//
// MEASURED ON THE v0.10.6 CANDIDATE, and this is the whole reason the function exists.
// A rebuild's layer swap took one graph's resident count from an armed floor of 3,675
// to 128 (`bm25_pruned=3675 resident_segments=128`), the field arm then re-drained the
// corpus from a reset cursor, and the gate compared the NEW set's climbing count
// against the OLD set's arming point plus the slack of the bucket count then in force:
// no census ran from 457 segments to 5,727 — 40 % above the budget the search fan-out
// was measured at — with nothing consolidated, no partition failed and nothing in the
// log to explain it. The bound was not holding; it was disabled, for the width of the
// re-drain, by a latch describing a set that had been retired.
//
// IT IS NOT A RELAXATION OF THE RATE LIMIT AND IT MOVES NO CEILING. Clearing can only
// make the NEXT crossing census — never defer one — so the census-rate ceiling this
// latch exists to hold is untouched for as long as the set it was armed over is the
// set being counted. What it removes is the window in which the rate limit applies to
// a set nobody has ever censused.
//
// THERE ARE EXACTLY TWO CALLERS, and each names itself so a log line says which one
// acted: the reset's layer swap (manager_rebuild_finalize.go, both formats through one
// generic body) and the group swap that rebuilds a closed set of partitions and retires
// their tails (manager_bucket_partition.go — the drain and the reset's build-window
// absorb). The bound's OWN per-partition swap is not among them: a census settles its
// own latch from what it bought, which is settleCensusLatch's rule and not this one.
//
// TWO SITES THAT LOOK LIKE CALLERS AND ARE NOT, named here because this list is where a
// reader checks whether "the resident set was replaced" is closed:
//
//	THE WHOLE-POOL EVICTION IS AN EXCEPTION, NOT AN OMISSION. It empties the searchable
//	set, but its reload is L2-strict and replays the EXACT unloaded id set, so the set is
//	RESTORED rather than replaced and the latch still describes it. The reasoning is
//	written at the site, beside the engine.Unload it deliberately does not clear after
//	(manager_residency.go).
//
//	THE BM25 FEED-CURSOR RESET IS OUT OF REACH (bm25_delta_state.go, driven by the
//	rebuild before its swap). It announces a re-drain of the whole corpus, but it is a
//	Manager method taking no request context, so it cannot resolve the {storage,account}
//	destination whose arm actually serves — a clear there would address an arm that on a
//	keyed client holds nothing. It needs none: the swap that follows it in the same run
//	clears on the destination-resolved arm, and writes landing in the window between them
//	land in the OLD set, which the latch still describes truthfully. When that swap is
//	REFUSED by the prospective-layer gate, no set is replaced at all and the re-drain pays
//	the ordinary band on the unchanged set — a question about the band's width rather than
//	about this clear.
//
// IT WINS AGAINST A CENSUS IN FLIGHT, and that is what the generation bump below is
// for rather than any ordering between the caller and the seal path. A census that
// read its count before this call and settles after it is stale by construction, and
// settleCensusLatch drops it: see distManager.replacementGen for the observed window.
//
// IT LOGS ONLY WHEN THE LATCH WAS ARMED. A clear over an already-clear latch is the
// common case — most swaps happen while nothing is deferred — and a line per swap
// would bury the one that matters. The armed case is exactly the state the reproduced
// excursion needed a log line for and did not have.
func clearCensusLatchOnReplacement[Q, S any](dm *distManager[Q, S], cause string) {
	// THE GENERATION IS BUMPED BEFORE THE CLEAR, NOT AFTER IT, and the order is the
	// whole race. A census that settles between the two stores must see a generation it
	// does not recognize and settle nothing; bumping afterwards would leave a window in
	// which a census settles against the generation it snapshotted and re-arms the
	// retired floor over the set this replacement just published — the interleaving
	// this counter exists for.
	dm.replacementGen.Add(1)
	armed := dm.lastCensusCount.Swap(0)
	floor := dm.lastCensusFloor.Swap(0)
	if armed <= 0 {
		return
	}
	slog.Info("segmentdist: cleared the resident-bound census latch — the resident set it was armed over has been replaced",
		"graph", dm.target.GetGraph(), "name", dm.target.GetName(), "repo", dm.target.GetRepo(),
		"format", dm.format, "cause", cause, "armed_at", armed, "last_floor", floor,
		"resident_segments", dm.engine.ResidentSegmentCount())
}

// minCensusReduction is how far a census must have driven the resident count for its
// cost to have bought anything, and it is derived from two things the caller already
// knows: the headroom the low-water mark buys, and where the LAST census left the
// count.
//
// THE FLOOR TERM IS THE ONE THAT ENDS THE TREADMILL. A census that lands back on the
// count the last one left removed exactly the material that arrived between them: it
// kept up with the write rate and moved the bound's floor by nothing, so re-paying it
// on the next crossing re-merges the whole consolidated corpus to land in the same
// place again. Demanding one segment more than the growth since the last census —
// `observed - lastFloor + 1`, which is the same statement as "the count it leaves must
// be BELOW the last floor" — is what distinguishes progress from keeping up.
//
// THE SLACK TERM IS THE FLOOR-LESS CASE AND THE MINIMUM. The first census after a
// clear has no previous floor to compare against (lastFloor is zero), and a census
// that reduced by a handful of segments has not bought a crossing whatever the floor
// did. Half the headroom the mark buys is the threshold: the mark leaves
// residentLowWaterBatches batches of headroom by construction, a batch adds at most
// bucketCount segments, so slack/2 is FOUR write batches' worth of seals — the point
// at which the walk and the merges are amortized over more than one crossing's worth
// of writes. Measured on the release candidate's regime (partition count 128, slack
// 1,024, threshold 512): each crossing there absorbed about 421 new segments and each
// census gave back exactly those, so both terms arm it.
//
// A FLOOR AT OR ABOVE THE BUDGET IS NEVER DEFERRED ON, and that is what keeps the
// partial-consolidation regime this latch was corrected for in round four. A census
// whose floor is still at or over the budget has not restored the property the bound
// exists to hold at all — consolidatable material can be sitting resident while the
// count is over — so it falls back to the slack term alone and the next crossing
// censuses as soon as it reduces by that much. Deferring there is the defect
// TestResidentBoundReCensusesAfterAPartialConsolidation pins: the sustained count sat
// at budget + slack while a partition a swap could take stayed resident.
func minCensusReduction(observed, lastFloor, slack int) int {
	slackTerm := slack / 2
	if lastFloor <= 0 || lastFloor >= searchengine.ResidentSegmentFanoutBudget {
		return slackTerm
	}
	return max(slackTerm, observed-lastFloor+1)
}

// residentLowWaterBatches is the headroom a consolidation leaves, MEASURED IN WRITE
// BATCHES rather than in segments.
//
// THE UNIT IS THE POINT. A lease seals one segment per partition it touches, so a
// batch adds up to bucketCount segments — and a mark expressed as "the budget less
// one partition count" would leave exactly ONE batch of headroom and be crossed
// again on the very next write, which is the per-batch censusing this mark exists to
// stop. Eight batches is the trade: eight crossings' worth of seals between censuses
// against a resident set that sits an eighth of that below its ceiling.
const residentLowWaterBatches = 8

// residentLowWater is the count a consolidation drives DOWN TO, and the slack it
// leaves below the budget.
//
// THE ARITHMETIC, with bucketCount as the variable: a write batch adds at most
// bucketCount segments, so leaving k x bucketCount of headroom buys k batches
// between crossings. lowWater = budget - k*bucketCount, clamped so the mark never
// falls below half the budget — at bucketCount 128 that is 4,096 - 1,024 = 3,072,
// and at BucketCountFor's 1,024 cap the unclamped value would go negative and the
// clamp gives 2,048. The slack returned is the REALIZED headroom (budget - lowWater)
// rather than k*bucketCount, because where the clamp binds those differ and a census
// gate keyed to the larger number would never re-arm.
//
// THE ENGINE IS NEVER QUIESCENT WHILE WRITES CONTINUE, and no choice of mark changes
// that. Each batch adds up to bucketCount segments BY CONSTRUCTION — that is what
// per-partition sealing means — so any bound on the resident count must do work
// proportional to the write rate. What this mark buys is AMORTIZATION, in the shape
// an LSM uses: each crossing merges one partition's accumulated tails, so a segment
// is re-merged once per k batches of its partition rather than once per batch.
// QUIESCENCE IS THE DRAIN ENDING, not this function converging: when writes stop the
// backlog drains, the merger tick does zero entry visits (it is constructed
// merge-disabled), and the full consolidation is the rebuild's group swap. This
// bound is the between-drains property only.
func residentLowWater(bucketCount int) (lowWater, slack int) {
	budget := searchengine.ResidentSegmentFanoutBudget
	if bucketCount < 1 {
		bucketCount = 1
	}
	lowWater = budget - residentLowWaterBatches*bucketCount
	if floor := budget / 2; lowWater < floor {
		lowWater = floor
	}
	return lowWater, budget - lowWater
}

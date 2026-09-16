// SPDX-License-Identifier: Apache-2.0

// manager_bucket_bound_reduction_test.go — THE CENSUS TREADMILL: a census that
// consolidates every partition it has and lands on exactly the count the last one
// landed on.
//
// THE REGIME IS THE ONE MEASURED ON THE v0.10.6 CANDIDATE, not a corner. Most
// resident segments were sealed under a SMALLER partition count and span several
// partitions at the current one, so a per-partition swap may never take them: the
// count can therefore never fall below "the spanning backlog plus one output per
// partition", and that floor sits ABOVE the low-water mark. The census consolidates
// all 128 partitions, removes exactly the tails that arrived since the last census,
// lands back on the same floor, and — because it consolidated something — cleared
// the latch, so the next crossing a few hundred seals later pays the whole thing
// again. Twelve consecutive censuses were captured at
// `partitions_consolidated=128 partitions_failed=0 resident_segments=3675
// low_water=3072`, byte-identical each time, and the merges inside them were 98 of
// the 99 CPU seconds the bound spent in a 285-second capture.
//
// THE FIXTURE BUILDS THE STATE DIRECTLY, on the same merge-disabled mock engine the
// sibling rows use, rather than sealing under one partition count and rebucketing to
// another. What the regime IS, in the only terms the bound can see, is a population
// of segments SegmentSpans reports as spanning several partitions plus a handful of
// single-partition candidates per partition; a doubling is one way to produce that
// and searching for ids that span two partitions at the evaluated count is another,
// and the second is deterministic under a hash change (spanningPair states the same
// discipline).
//
// THE ROW IS SERIAL. It reads the bound's own INFO lines, which means replacing the
// process-global slog default for the width of the drive; a sibling restoring that
// default halfway leaves the buffer holding a fraction of the run. The package's
// other log-reading rows are serial for the same reason.

package segmentdist

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// The fixture's shape, in the arithmetic the bound itself uses (budget 4,096,
// low-water 3,072, slack 1,024 at this partition count).
//
// reduceSpanning is chosen so the floor a consolidation can reach —
// reduceSpanning + reduceBuckets, one output per partition — sits between the mark
// and the budget AND leaves less than half the slack of headroom under the budget.
// That is the regime's defining number: the count the next crossing has to climb is
// SMALLER than the reduction a census would have to reach for its cost to have
// bought anything, so every crossing pays a full 128-partition consolidation to
// return to the same place.
const (
	reduceBuckets      = 128
	reduceSpanning     = 3585
	reduceInitialTails = 5
	reduceBatches      = 24
)

// TestResidentBoundStopsReCensusingWhenTheFloorDoesNotMove is requirement 1: a
// census that reduced the count by less than the reduction the latch demands arms it
// exactly as one that took nothing does.
//
// WHAT RED LOOKS LIKE, and it is the shape the capture holds: one consolidation pass
// over every partition on EVERY crossing, each ending at the identical resident
// count, once per few hundred seals for as long as the drain lasts. WHAT GREEN LOOKS
// LIKE: the first census (which has no previous floor to compare against and clears
// on the reduction alone), and then at most one pass per slack of growth.
//
// THE COUNT IS STILL BOUNDED while the latch is armed, by the clamp the sibling row
// pins: the arming point is never above the budget, so the gate admits at most
// budget + slack without censusing.
func TestResidentBoundStopsReCensusingWhenTheFloorDoesNotMove(t *testing.T) {
	engine := searchengine.New[mockQuery, mockStats](mockFormat{}, searchengine.Options{
		MinSegmentDocs:     1,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
	})
	t.Cleanup(engine.Close)
	dm := newDistManager(engine, nil, graphSelector(boundGraphType, "census-treadmill"), "mock")

	budget := searchengine.ResidentSegmentFanoutBudget
	lowWater, slack := residentLowWater(reduceBuckets)

	// THE BACKLOG THE PER-PARTITION SWAP MAY NEVER TAKE, and the floor it pins.
	for _, docs := range reduceSpanningSegments(t, reduceSpanning) {
		_, err := engine.AddSealAndSupersede(docs)
		require.NoError(t, err)
	}
	pool := reduceSoloPool(t, reduceInitialTails+reduceBatches)
	for tail := range reduceInitialTails {
		for bucket := range reduceBuckets {
			_, err := engine.AddSealAndSupersede([]searchengine.Document{pool[bucket][tail]})
			require.NoError(t, err)
		}
	}

	floor := reduceSpanning + reduceBuckets
	require.Greater(t, engine.ResidentSegmentCount(), budget,
		"PRECONDITION: the fixture must be over budget, or the bound returns at its crossing gate")
	require.Greater(t, floor, lowWater,
		"PRECONDITION: the floor a consolidation can reach (%d) must sit ABOVE the low-water mark (%d) — a "+
			"consolidation that reaches the mark buys its headroom by construction and is not this regime", floor, lowWater)
	require.Less(t, budget-floor, slack/2,
		"PRECONDITION: and the headroom that floor leaves (%d) must be less than half the slack (%d), or a crossing "+
			"absorbs enough new material for the census to pay for itself and the treadmill is not reached",
		budget-floor, slack/2)
	spanning := 0
	for _, buckets := range engine.SegmentSpans(reduceBuckets) {
		if len(buckets) > 1 {
			spanning++
		}
	}
	require.Equal(t, reduceSpanning, spanning,
		"PRECONDITION: every one of the backlog's segments must span more than one partition at %d, which is what "+
			"makes the floor unreachable by a per-partition swap", reduceBuckets)

	// THE DRIVE: one write batch's worth of seals — a lease seals at most one segment
	// per partition it touches — and then the bound, which is exactly how the seal
	// path calls it.
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	sustained := 0
	for batch := range reduceBatches {
		for bucket := range reduceBuckets {
			_, err := engine.AddSealAndSupersede([]searchengine.Document{pool[bucket][reduceInitialTails+batch]})
			require.NoError(t, err)
		}
		boundResidentSegments(dm, reduceBuckets)
		if n := engine.ResidentSegmentCount(); n > sustained {
			sustained = n
		}
	}
	slog.SetDefault(prev)

	passes := consolidationPasses(buf.String())
	censuses := dm.segmentSpanCensuses()
	for _, line := range passes {
		t.Log(line)
	}
	tails := reduceBatches * reduceBuckets
	ceiling := int64(1 + tails/slack)
	t.Logf("treadmill row: %d censuses and %d consolidation passes over %d batches (%d tails), ceiling %d; "+
		"sustained %d (budget=%d, low_water=%d, slack=%d, floor=%d)",
		censuses, len(passes), reduceBatches, tails, ceiling, sustained, budget, lowWater, slack, floor)

	require.Positive(t, censuses,
		"ANTI-VACUITY: the bound must have censused at least once, or this fixture never crossed the budget and "+
			"every ceiling below is satisfied by a mechanism that did nothing")
	require.NotEmpty(t, passes,
		"ANTI-VACUITY: and it must have consolidated at least once, or the regime under test — a consolidation that "+
			"lands back on the floor it started from — was never reached")
	require.Contains(t, passes[0], fmt.Sprintf("resident_segments=%d", floor),
		"PRECONDITION: the first consolidation lands on the floor the fixture pins (%d): every partition's tails "+
			"merge into one output and the spanning backlog stays where it is", floor)

	require.LessOrEqual(t, censuses, ceiling,
		"the bound censused %d times over %d tails against a ceiling of one census per slack of growth (%d): a "+
			"census that returned the count to the floor the last one left reduced NOTHING net, and re-paying it on "+
			"the next crossing re-merges the whole consolidated corpus to land in the same place",
		censuses, tails, ceiling)
	require.LessOrEqual(t, int64(len(passes)), ceiling,
		"and it consolidated %d times: the merges are the cost — 98 of the bound's 99 CPU seconds in the capture "+
			"that opened this work — so a census that is paid is a full re-merge of every consolidated partition",
		len(passes))
	require.LessOrEqual(t, sustained, budget+slack,
		"the SUSTAINED resident count reached %d against the absolute ceiling of budget + slack (%d); deferring a "+
			"census must never raise the count the gate will admit without censusing", sustained, budget+slack)
	require.LessOrEqual(t, sustained, floor+slack,
		"and it reached %d against ONE SLACK of growth from the floor the census left (%d + %d): the latch is armed "+
			"at the count the census LEFT, not at the higher count it observed on the way in, so the gate re-admits "+
			"a slack above where the set actually stands", sustained, floor, slack)
}

// TestResidentBoundNeverDefersWhileTheCountStaysOverBudget is the THIRD crossing of
// the regime TestResidentBoundReCensusesAfterAPartialConsolidation drives two of.
//
// IT EXISTS BECAUSE THE REDUCTION RULE COULD HAVE RE-OPENED THAT DEFECT. A
// consolidation that runs out of candidates at the budget leaves a floor AT the
// budget, and the growth between its crossings is then a handful of segments — so a
// reduction rule keyed on "more than arrived since the last census" would arm the
// latch there, and the sustained count would sit at budget + slack with material a
// per-partition swap could take resident, which is exactly what round four corrected.
// minCensusReduction's carve-out is what stops that: a census whose floor is still at
// or above the budget is compared against the slack term alone.
//
// The sibling row stops at the second crossing, which censuses on either rule. Only
// the third distinguishes them, because only there is a previous floor at the budget
// in play.
func TestResidentBoundNeverDefersWhileTheCountStaysOverBudget(t *testing.T) {
	t.Parallel()
	engine := searchengine.New[mockQuery, mockStats](mockFormat{}, searchengine.Options{
		MinSegmentDocs:     1,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
	})
	t.Cleanup(engine.Close)
	dm := newDistManager(engine, nil, graphSelector(boundGraphType, "stuck-at-budget"), "mock")

	budget := searchengine.ResidentSegmentFanoutBudget
	_, slack := residentLowWater(spanCount)
	const candidates = 12
	require.Less(t, candidates, slack,
		"PRECONDITION: each crossing must arrive with LESS new material than the slack (%d), or the gate re-arms on "+
			"growth alone and this row is green whatever the latch did", slack)

	for i := range budget - 1 {
		_, err := engine.AddSealAndSupersede(spanningPair(t, i))
		require.NoError(t, err)
	}
	seed := 0
	crossing := func(n int) {
		for range n {
			_, err := engine.AddSealAndSupersede([]searchengine.Document{soloBucketDoc(t, seed)})
			require.NoError(t, err)
			seed++
		}
		require.Greater(t, engine.ResidentSegmentCount(), budget, "PRECONDITION: the set crossed the budget")
		boundResidentSegments(dm, spanCount)
	}

	crossing(10)
	require.Equal(t, budget, engine.ResidentSegmentCount(),
		"PRECONDITION: the first consolidation takes every candidate it has and lands AT the budget — the floor this "+
			"row is about, and the one a reduction rule must not defer on")
	crossing(candidates)
	require.Equal(t, int64(2), dm.segmentSpanCensuses(), "PRECONDITION: the second crossing censused (the sibling row's assertion)")

	for range candidates {
		_, err := engine.AddSealAndSupersede([]searchengine.Document{soloBucketDoc(t, seed)})
		require.NoError(t, err)
		seed++
	}
	observed := engine.ResidentSegmentCount()
	require.NotEmpty(t, consolidatablePartitions(engine.SegmentSpans(spanCount)),
		"PRECONDITION: a partition a per-partition swap can reduce is resident, so a census would find work")

	boundResidentSegments(dm, spanCount)

	sustained := engine.ResidentSegmentCount()
	t.Logf("stuck-at-budget row: third crossing observed %d -> %d over %d censuses (budget=%d, slack=%d)",
		observed, sustained, dm.segmentSpanCensuses(), budget, slack)
	require.Equal(t, int64(3), dm.segmentSpanCensuses(),
		"the third crossing must census: its predecessor left the count AT the budget, so the bound has restored "+
			"nothing and deferring holds the count at budget + slack (%d) with consolidatable material resident",
		budget+slack)
	require.LessOrEqual(t, sustained, budget,
		"and the SUSTAINED count must be back inside the budget: %d against %d", sustained, budget)
}

// TestMinCensusReductionArithmetic pins the threshold's own two terms, on the numbers
// the regimes above are measured at.
func TestMinCensusReductionArithmetic(t *testing.T) {
	t.Parallel()
	budget := searchengine.ResidentSegmentFanoutBudget
	_, slack := residentLowWater(reduceBuckets)

	require.Equal(t, slack/2, minCensusReduction(budget+1, 0, slack),
		"with no previous census to compare against, the threshold is HALF the headroom the low-water mark buys — "+
			"four write batches' worth of seals — and never zero: a census that gave back a handful of segments "+
			"bought no crossing whatever the floor did")
	require.Equal(t, slack/2, minCensusReduction(4097, 3713, slack),
		"a census whose growth since the last floor (4097-3713) is under that minimum is held to the minimum")
	require.Equal(t, 1025, minCensusReduction(4737, 3713, slack),
		"and one whose growth is over it must give back MORE than the growth — one segment more than 4737-3713 — "+
			"because a consolidation landing back on the last floor removed exactly what had arrived since it and "+
			"moved the bound's floor by nothing")
	_, narrowSlack := residentLowWater(spanCount)
	require.Equal(t, narrowSlack/2, minCensusReduction(budget+12, budget, narrowSlack),
		"a floor at or above the budget is never deferred on: the bound has restored nothing there, so the growth "+
			"term is dropped and the next crossing censuses as soon as it reduces by the minimum")
}

// TestCensusLatchClearsAtExactlyTheThreshold pins the COMPARISON settleCensusLatch
// applies, which is a different declaration from the threshold's value.
//
// THE BOUNDARY IS WHERE THE TWO DISPOSITIONS MEET, and no behavioral fixture in this
// package lands on it: every one of them reduces by more than the threshold (the
// treadmill's first census 640 against 512, the round-four row 9 against 8, the
// third-crossing row 12 against 8) or by less (the treadmill's third census 1,024
// against 1,025). So `>=` could become `>` — a census that reduced by EXACTLY the
// documented threshold arming instead of clearing — with every other row in the
// package still green. minCensusReduction is documented as the reduction a census
// must REACH, so reaching it clears.
//
// IT IS PINNED AT THE SETTLE LEVEL rather than through a fixture because the boundary
// is a property of the comparison and of nothing else: settleCensusLatch reads no
// engine and writes only the two atomics, so a zero-value distManager is the whole
// world it needs, and a fixture built to land on the boundary would pin the fixture's
// arithmetic beside the rule.
func TestCensusLatchClearsAtExactlyTheThreshold(t *testing.T) {
	t.Parallel()
	budget := searchengine.ResidentSegmentFanoutBudget
	_, slack := residentLowWater(reduceBuckets)
	observed := budget + 600

	exact := &distManager[mockQuery, mockStats]{}
	settleCensusLatch(exact, censusOutcome{
		observed:     observed,
		after:        observed - slack/2,
		consolidated: 1,
		minReduction: slack / 2,
	})
	require.Zero(t, exact.lastCensusCount.Load(),
		"a census that reduced by EXACTLY minCensusReduction (%d) must CLEAR the latch: the threshold is the "+
			"reduction a census has to REACH, and arming at it defers a census that bought precisely what the "+
			"low-water mark's headroom is worth", slack/2)

	short := &distManager[mockQuery, mockStats]{}
	settleCensusLatch(short, censusOutcome{
		observed:     observed,
		after:        observed - slack/2 + 1,
		consolidated: 1,
		minReduction: slack / 2,
	})
	require.Equal(t, int64(min(observed-slack/2+1, budget)), short.lastCensusCount.Load(),
		"KNOWN NEGATIVE in the same shape: one segment short of the threshold arms, at the count the census left "+
			"clamped to the budget — so the assertion above is the boundary rather than a latch that never arms")
}

// consolidationPasses returns the bound's own INFO lines for the censuses that
// consolidated something, which is the instrument the capture was read through.
func consolidationPasses(logs string) []string {
	var out []string
	for line := range strings.SplitSeq(logs, "\n") {
		if strings.Contains(line, "partitions_consolidated=") {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}

// reduceSpanningSegments builds n segments' worth of documents, each segment holding
// two ids that hash to DIFFERENT partitions at reduceBuckets, so SegmentSpans reports
// every one of them as spanning and consolidatablePartitions omits them all.
//
// It PAIRS CONSECUTIVE ids by their measured partition rather than assuming a spread,
// for the reason spanningPair states: BucketOf is a hash, and a fixture that assumes
// its shape goes vacuous when the hash changes.
func reduceSpanningSegments(t *testing.T, n int) [][]searchengine.Document {
	t.Helper()
	out := make([][]searchengine.Document, 0, n)
	var pending searchengine.Document
	pendingBucket := -1
	for i := 0; len(out) < n; i++ {
		if i > 100*n {
			t.Fatalf("reduceSpanningSegments(%d): only %d spanning segments after %d ids", n, len(out), i)
		}
		id := fmt.Sprintf("wide-%07d", i)
		b := searchengine.BucketOf(id, reduceBuckets)
		if pendingBucket < 0 {
			pending, pendingBucket = doc(id, "alpha beta"), b
			continue
		}
		if b == pendingBucket {
			continue
		}
		out = append(out, []searchengine.Document{pending, doc(id, "alpha beta")})
		pendingBucket = -1
	}
	return out
}

// reduceSoloPool draws `per` documents for every partition at reduceBuckets, so a
// batch can seal one single-partition segment per partition the way a write lease
// does. A one-document segment occupies exactly one partition at every count it will
// ever be read under, which is what makes each of them a candidate a per-partition
// swap can take.
//
// It BINS MEASURED ids rather than searching per partition: one pass over a stream of
// ids fills every bucket at once, where a per-partition search re-hashes the whole
// stream for each of the 128 partitions.
func reduceSoloPool(t *testing.T, per int) [][]searchengine.Document {
	t.Helper()
	pool := make([][]searchengine.Document, reduceBuckets)
	filled := 0
	for i := 0; filled < reduceBuckets; i++ {
		if i > 1000*reduceBuckets*per {
			t.Fatalf("reduceSoloPool(%d): only %d of %d partitions filled after %d ids", per, filled, reduceBuckets, i)
		}
		id := fmt.Sprintf("solo-%07d", i)
		b := searchengine.BucketOf(id, reduceBuckets)
		if len(pool[b]) >= per {
			continue
		}
		pool[b] = append(pool[b], doc(id, "alpha beta"))
		if len(pool[b]) == per {
			filled++
		}
	}
	return pool
}

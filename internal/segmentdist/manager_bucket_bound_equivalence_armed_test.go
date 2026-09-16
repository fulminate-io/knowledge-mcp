// SPDX-License-Identifier: Apache-2.0

// manager_bucket_bound_equivalence_armed_test.go — RESULT EQUIVALENCE ON THE PATHS
// THE LATCH CHANGE ADDED: a census that consolidates and then ARMS the latch, and the
// census that follows it under the budget carve-out.
//
// WHY THE SIBLING FILE'S ROWS DO NOT COVER THESE. Instrumenting settleCensusLatch
// shows that every existing equivalence row settles on the CLEAR path —
// TestResultsAreIdenticalAcrossAConsolidation reduces 4,672 to 3,478 against a
// threshold of 32, and the quarantined sibling clears first on a withdrawal and then
// on a reduction of 3,367 against 2,049. The arming branch (consolidated > 0 with the
// reduction short of minCensusReduction) and the carve-out branch (the previous floor
// at or above the budget, so the growth term is dropped) are reached by no row that
// compares search results at all. The one row that does reach them, the treadmill in
// manager_bucket_bound_reduction_test.go, runs on the merge-disabled mock format and
// asserts counts.
//
// WHAT THE ROWS CAN AND CANNOT SAY. The consolidation loop is byte-identical on both
// paths — settleCensusLatch runs after it and touches no engine state — so these rows
// are the ticket's own requirement 4 rather than a suspected data defect: they pin
// that the answers do not move across the consolidations the new dispositions
// perform, on both real formats, and they are the rows that go red if a later change
// makes an armed census consolidate differently from a cleared one.
//
// BOTH ARMS, for the reason the sibling file states: BM25 would show a disturbed
// corpus statistic as a score change, and HNSW would show one as a changed neighbor
// order or a missing id.

package segmentdist

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// The fixture's shape, in the bound's own arithmetic at armedBuckets (budget 4,096,
// low-water 4,080, slack 16, so minCensusReduction is 8).
//
// TWO PARTITIONS is what makes the regime reachable with one candidate partition:
// every segment of the backlog spans BOTH of them and can never be taken, so the
// count a consolidation can reach is the backlog plus the one output the candidate
// partition leaves — and armedSpanning is chosen to put that floor AT the budget,
// which is both the arming regime (the reduction is the handful of tails, far short
// of the threshold) and the carve-out regime for the census after it (the floor is
// not below the budget, so the growth term is dropped).
const (
	armedBuckets  = 2
	armedSpanning = searchengine.ResidentSegmentFanoutBudget - 1
	armedSoloTail = 5
)

// TestResultsAreIdenticalAcrossAnArmedConsolidation is requirement 4 on the two paths
// the latch change added.
func TestResultsAreIdenticalAcrossAnArmedConsolidation(t *testing.T) {
	t.Run("the_field_engine_bm25v2", func(t *testing.T) {
		t.Parallel()
		const name = "equiv-armed-bm25"
		m := closeOnCleanup(t, NewManager(t.TempDir(), 0))
		engine := searchengine.New[bm25.Query, *bm25.CorpusStats](bm25.New(), searchengine.Options{
			MinSegmentDocs:     1,
			SegmentCountTarget: searchengine.MergeDisabledCountTarget,
			DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
		})
		t.Cleanup(engine.Close)
		installBM25Arm(m, name, engine)
		refill := seedArmedCorpus(t, func(batch []searchengine.Document) {
			_, err := engine.AddSealAndSupersede(batch)
			require.NoError(t, err)
		})

		queries := make([]string, 0, equivQueries)
		for q := range equivQueries {
			queries = append(queries, fmt.Sprintf("shared term%02d", q))
		}
		driveArmedEquivalence(t, m.bm25ManagerFor(boundGraphType, name), engine, refill,
			func() []string { return bm25Results(t, engine, queries) })
	})

	t.Run("the_vector_engine_hnswv3", func(t *testing.T) {
		t.Parallel()
		const name = "equiv-armed-hnsw"
		m := closeOnCleanup(t, NewManager(t.TempDir(), 0))
		engine := searchengine.New[[]byte, struct{}](hnsw.New(), searchengine.Options{
			MinSegmentDocs:     1,
			SegmentCountTarget: searchengine.MergeDisabledCountTarget,
			DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
		})
		t.Cleanup(engine.Close)
		installHNSWArm(m, name, engine)
		refill := seedArmedCorpus(t, func(batch []searchengine.Document) {
			_, err := engine.AddSealAndSupersede(batch)
			require.NoError(t, err)
		})

		probes := make([][]byte, 0, equivQueries)
		for q := range equivQueries {
			probes = append(probes, equivDoc(q*13).Vector)
		}
		driveArmedEquivalence(t, m.managerFor(boundGraphType, name), engine, refill,
			func() []string { return hnswResults(t, engine, probes) })
	})
}

// driveArmedEquivalence drives the two censuses the rows are about and compares the
// answers across each of them.
//
// THE RESULT COMPARISON COMES FIRST IN EACH LEG, before the controls that say which
// latch path was taken, and the order is load-bearing rather than stylistic: the
// mutation these rows exist to catch — a consolidation that publishes a partition
// without the members its constituents hold for the OTHER partition — also changes
// what the latch does with the reduction, so a control asserted first would tell a
// reader the fixture missed its regime when in fact documents are gone.
func driveArmedEquivalence[Q, S any](
	t *testing.T, dm *distManager[Q, S], engine *searchengine.SegmentedIndex[Q, S],
	refill []searchengine.Document, results func() []string,
) {
	t.Helper()
	budget := searchengine.ResidentSegmentFanoutBudget
	lowWater, slack := residentLowWater(armedBuckets)

	spanning := 0
	for _, buckets := range engine.SegmentSpans(armedBuckets) {
		if len(buckets) > 1 {
			spanning++
		}
	}
	require.Equal(t, armedSpanning, spanning,
		"PRECONDITION: the backlog must span both partitions — that is what holds the reachable floor above the "+
			"low-water mark (%d) and what makes the constituency-closure guard reachable, so a consolidation that "+
			"dropped a spanning segment's other-partition members would show up in the answers below", lowWater)
	require.Greater(t, engine.ResidentSegmentCount(), budget,
		"PRECONDITION: over budget, or the bound returns at its crossing gate and every equality here is vacuous")
	require.Less(t, armedSoloTail-1, slack/2,
		"PRECONDITION: the candidate partition's reduction (%d) must fall SHORT of minCensusReduction (%d), or the "+
			"census clears and this row drives the path the sibling file already covers", armedSoloTail-1, slack/2)

	// LEG ONE: the census that consolidates and ARMS.
	before := results()
	docsBefore := engine.DistinctResidentDocCount()
	segmentsBefore := engine.ResidentSegmentCount()

	boundResidentSegments(dm, armedBuckets)

	require.Equal(t, before, results(),
		"a consolidation whose census ARMS the latch must change NOTHING a searcher sees: same documents, same "+
			"order, same scores — the arming is a decision about the NEXT census, not about this merge")
	require.Equal(t, int64(1), dm.segmentSpanCensuses(), "ANTI-VACUITY: the first crossing censused")
	require.Less(t, engine.ResidentSegmentCount(), segmentsBefore,
		"ANTI-VACUITY: and it CONSOLIDATED (%d segments before) — a census that took nothing reaches the arming "+
			"branch by the older rule and would prove nothing about the new one", segmentsBefore)
	require.Equal(t, docsBefore, engine.DistinctResidentDocCount(),
		"and it moved no documents, so the equality above is the consolidation rather than a changed corpus")
	require.Positive(t, dm.lastCensusCount.Load(),
		"ANTI-VACUITY: the latch must be ARMED after it — that is the path this row exists to drive, and a census "+
			"that cleared would have compared the answers across the branch the sibling rows already cover")
	require.GreaterOrEqual(t, dm.lastCensusFloor.Load(), int64(budget),
		"PRECONDITION for the second leg: the floor this census left is at or above the budget, which is the state "+
			"minCensusReduction drops its growth term for")

	// LEG TWO: the census that follows it, under the carve-out. The refill is one
	// whole slack, because an armed latch admits nothing less.
	require.Len(t, refill, slack,
		"PRECONDITION: the refill must be exactly one slack (%d), or the gate declines the second census and the "+
			"leg below compares a reading with itself", slack)
	for _, d := range refill {
		_, err := engine.AddSealAndSupersede([]searchengine.Document{d})
		require.NoError(t, err)
	}
	require.GreaterOrEqual(t, int64(engine.ResidentSegmentCount()), dm.lastCensusCount.Load()+int64(slack),
		"PRECONDITION: and it must re-admit the gate")

	beforeSecond := results()
	docsSecond := engine.DistinctResidentDocCount()
	segmentsSecond := engine.ResidentSegmentCount()

	boundResidentSegments(dm, armedBuckets)

	require.Equal(t, beforeSecond, results(),
		"and the census that follows it — the one whose threshold comes from the budget carve-out — must change "+
			"nothing a searcher sees either")
	require.Equal(t, int64(2), dm.segmentSpanCensuses(),
		"ANTI-VACUITY: the second crossing censused, so the carve-out was actually evaluated")
	require.Less(t, engine.ResidentSegmentCount(), segmentsSecond,
		"ANTI-VACUITY: and it consolidated (%d segments before)", segmentsSecond)
	require.Equal(t, docsSecond, engine.DistinctResidentDocCount(), "and moved no documents")
	require.Zero(t, dm.lastCensusCount.Load(),
		"and it CLEARED: under the carve-out the threshold is the slack term alone (%d), which a census taking one "+
			"slack of tails reaches — this is the disposition the carve-out exists to produce", slack/2)
}

// seedArmedCorpus seals the backlog and the candidate partition's first tails, and
// returns the tails the second leg refills with. Nothing it seals runs the bound: the
// fixture is built first and the bound is driven directly, so every difference the
// rows read is the consolidation and nothing else.
func seedArmedCorpus(t *testing.T, seal func([]searchengine.Document)) []searchengine.Document {
	t.Helper()
	_, slack := residentLowWater(armedBuckets)
	spanning, solo := armedCorpusDocs(t, armedSpanning, armedSoloTail+slack)
	for _, pair := range spanning {
		seal(pair)
	}
	for _, d := range solo[:armedSoloTail] {
		seal([]searchengine.Document{d})
	}
	return solo[armedSoloTail:]
}

// armedCorpusDocs draws the fixture's documents from equivDoc's own index space in ONE
// pass, so no document is written twice and every one of them carries the distinct
// content length and vector the equality rows need.
//
// It MEASURES each id's partition rather than assuming a spread, the discipline
// spanningPair and reduceSpanningSegments state: BucketOf is a hash. A spanning
// segment is a pair of consecutive ids whose partitions differ; a solo is an id in
// armedSoloBucket, and they all share one partition because a candidate set spread
// over both would leave neither with the two constituents a swap needs.
func armedCorpusDocs(t *testing.T, spanningSegments, soloDocs int) (spanning [][]searchengine.Document, solo []searchengine.Document) {
	t.Helper()
	const armedSoloBucket = 0
	spanning = make([][]searchengine.Document, 0, spanningSegments)
	solo = make([]searchengine.Document, 0, soloDocs)
	var pending searchengine.Document
	pendingBucket := -1
	for i := 0; len(spanning) < spanningSegments || len(solo) < soloDocs; i++ {
		if i > 100*(spanningSegments+soloDocs) {
			t.Fatalf("armedCorpusDocs: only %d of %d spanning segments and %d of %d solo documents after %d ids",
				len(spanning), spanningSegments, len(solo), soloDocs, i)
		}
		d := equivDoc(i)
		b := searchengine.BucketOf(d.ID, armedBuckets)
		if len(spanning) < spanningSegments {
			if pendingBucket < 0 {
				pending, pendingBucket = d, b
				continue
			}
			if b == pendingBucket {
				continue
			}
			spanning = append(spanning, []searchengine.Document{pending, d})
			pendingBucket = -1
			continue
		}
		if b == armedSoloBucket {
			solo = append(solo, d)
		}
	}
	return spanning, solo
}

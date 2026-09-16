// SPDX-License-Identifier: Apache-2.0

// manager_bucket_bound_test.go — the RESIDENT SEGMENT COUNT BOUND, per format per
// graph, on every wiring.
//
// THE BOUND IS ON THE MAXIMUM, NEVER ON THE END STATE, and the distinction is the
// whole reason these rows are shaped as they are. The peak IS the harm: the capture
// that opened this work measured a 3.99 GB RSS spike at the moment a rebuild
// unioned 4,447 segments, and a count read after that rebuild finished records only
// that it finished. A test that writes, waits for quiescence and then reads the
// count is GREEN against the defect on the keyed wiring and certifies nothing.
//
// TWO MAXIMA ARE READ, AND THEY ARE DIFFERENT NUMBERS. The fixture drives the
// batches itself and reads ResidentSegmentCount after every one, which is the
// SUSTAINED count — what a search fans out over between write batches — and that is
// the one the budget bounds. It CANNOT see the excursion a batch's own seals make
// before the bound acts, because the bound acts inside the same call. The engine's
// INTRA-CALL peak is therefore sampled where it happens, on the seal path
// immediately before the bound (distManager.recordResidentPeak), and it is bounded
// separately: the excursion is at most the partitions ONE lease sealed, which is at
// most the partition count. Both are deterministic COUNTS and neither is a clock.
//
// BOTH FORMATS ASSERT SEPARATELY, because the two engines seal and publish
// independently and the crossing is already per format (see the tail cap's own
// test). AddAndMarkDirty drives hnswv3 and AddAndMarkDirtyFields drives bm25v2, and
// both reach the same sealPerPartition: the bound is a property of the seal path
// rather than of a wiring, which is why one mechanism answers the keyless BM25-only
// arm and the keyed full arm alike.

package segmentdist

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// boundBatches and boundBatchDocs size the fixture's drive.
//
// THE BATCHES ARE MULTI-DOCUMENT ON PURPOSE, and the size is the difference between
// a fixture that can reach the constituency-closure guard and one that cannot. A
// single-document batch seals a segment holding ONE id, which occupies exactly one
// partition at every count it will ever be read under — so a corpus built from them
// contains no segment spanning several partitions, the guard is never consulted, and
// removing it would LOSE DATA with every row here still green. A batch of this width
// spreads across partitions and, once the corpus crosses a doubling, leaves segments
// spanning several of them.
//
// The batch count is enough that the seals alone would pass the budget several times
// over: without a bound each batch seals one segment per partition it touches.
const (
	boundBatchDocs = 96
	boundBatches   = 384
)

// boundGraphType is the graph family every fixture here writes into. It is a const
// beside the fixture rather than a parameter because no case varies it, which is
// what a parameter would promise.
const boundGraphType = kgtypes.GraphCode

// boundBatchDocuments builds one batch, ids running from the batch's own offset so
// every document in the fixture is distinct.
func boundBatchDocuments(batch int) []searchengine.Document {
	docs := make([]searchengine.Document, boundBatchDocs)
	for i := range docs {
		docs[i] = boundDoc(batch*boundBatchDocs + i)
	}
	return docs
}

// boundDoc is one document for the fixture. It carries BOTH a vector and a content
// field so the same batch can be driven through either engine.
func boundDoc(i int) searchengine.Document {
	vec := make([]byte, 32)
	for b := range vec {
		vec[b] = byte((i*31 + b*7) % 251)
	}
	return searchengine.Document{
		ID:     fmt.Sprintf("bound-%05d", i),
		Vector: vec,
		Fields: map[string]string{searchengine.FieldContent: fmt.Sprintf("alpha beta bound%05d", i)},
	}
}

// driveBoundFixture issues boundBatches multi-document batches through one of the
// two write entry points and returns the maximum SUSTAINED resident segment count
// observed after a batch, alongside the partition count the last of them ran under.
//
// IT ROUTES ON A BOOL RATHER THAN THROUGH A WRITE CLOSURE, the same way the write
// entry points' own backlog does. The package's measurement census resolves each
// leg's CORPUS SIZE by following where a document slice flows, and a slice handed to
// a func VALUE flows somewhere the walk cannot resolve — it fails loud on that
// rather than guessing, which is the behaviour a size-decided membership needs.
func driveBoundFixture(t *testing.T, mgr *Manager, name, format string, fields bool) (sustained, bucketCount int) {
	t.Helper()
	sustained, bucketCount, _ = driveBoundFixtureFully(t, mgr, name, format, fields)
	return sustained, bucketCount
}

// driveBoundFixtureFully is driveBoundFixture plus the LOWEST count seen after a
// consolidation, which is what says the bound drove down to its low-water mark
// rather than stopping at the budget.
func driveBoundFixtureFully(
	t *testing.T, mgr *Manager, name, format string, fields bool,
) (sustained, bucketCount, lowest int) {
	t.Helper()
	ctx := context.Background()
	crossed := false
	lowest = -1
	for i := range boundBatches {
		docs := boundBatchDocuments(i)
		var err error
		if fields {
			err = mgr.AddAndMarkDirtyFields(ctx, boundGraphType, name, docs)
		} else {
			err = mgr.AddAndMarkDirty(ctx, boundGraphType, name, docs)
		}
		require.NoError(t, err)
		n := mgr.ResidentSegmentCount(boundGraphType, name, format)
		if n > sustained {
			sustained = n
		}
		// Only readings AFTER the set first grew big enough to be bounded say
		// anything about where the bound leaves it; the early ones are just a small
		// engine growing.
		if !crossed && n >= searchengine.ResidentSegmentFanoutBudget/2 {
			crossed = true
		}
		if crossed && (lowest < 0 || n < lowest) {
			lowest = n
		}
	}
	return sustained, searchengine.BucketCountFor(boundArm(mgr, name, format).distinctResidentDocCount()), lowest
}

// TestResidentBoundCensusesRarely is the cost of the bound itself.
//
// THE CENSUS IS THE O(resident) WALK THIS WORK REMOVED FROM THE PUBLISH PATH.
// SegmentSpans walks every live member of every resident segment; paying it once per
// write batch would have RELOCATED that cost to the seal path rather than removed
// it. An earlier revision stopped consolidating at exactly the budget, so after the
// first crossing every subsequent batch crossed again and censused again.
//
// THE CEILING IT ASSERTS IS DERIVED, not chosen for roundness. A consolidation
// drives the set down to residentLowWater, which leaves residentLowWaterBatches
// batches of headroom by construction, and a lease adds at most one partition count
// of segments — so a crossing cannot recur more often than once per
// residentLowWaterBatches batches. Over N batches that is at most
// ceil(N/residentLowWaterBatches) crossings, plus one for the first. The measured
// count is well under it, because a consolidation of the fullest partition
// overshoots below the mark.
func TestResidentBoundCensusesRarely(t *testing.T) {
	t.Parallel()
	const name = "bound-census-rate"
	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	format := bm25.New().Name()

	sustained, bucketCount, lowest := driveBoundFixtureFully(t, mgr, name, format, true)
	requireBoundHeld(t, mgr, name, format, sustained, bucketCount)

	lowWater, slack := residentLowWater(bucketCount)
	censuses := mgr.bm25ManagerFor(boundGraphType, name).segmentSpanCensuses()
	ceiling := int64((boundBatches+residentLowWaterBatches-1)/residentLowWaterBatches + 1)
	t.Logf("span censuses: %d over %d batches, ceiling %d (bucket_count=%d, low_water=%d, slack=%d, lowest_sustained=%d)",
		censuses, boundBatches, ceiling, bucketCount, lowWater, slack, lowest)

	require.Positive(t, censuses,
		"ANTI-VACUITY: the bound must have censused at least once, or this fixture never crossed the budget "+
			"and the ceiling below is satisfied by a mechanism that did nothing")
	require.LessOrEqual(t, censuses, ceiling,
		"the bound censused %d times over %d write batches against a derived ceiling of %d; SegmentSpans is "+
			"O(live members), so a census per batch is the O(resident) walk this work removed from the publish "+
			"path, relocated to the seal path", censuses, boundBatches, ceiling)

	require.LessOrEqual(t, lowest, lowWater,
		"a consolidation must drive the set DOWN TO the low-water mark (%d), which is %d write batches of "+
			"headroom below the budget; stopping at the budget leaves every subsequent batch crossing again",
		lowWater, residentLowWaterBatches)
}

// TestResidentBoundReCensusesAfterAPartialConsolidation is the regime the census
// latch decides the BOUND in: a consolidation that took something and then ran out
// of candidates ABOVE the low-water mark.
//
// IT IS THE COMMON CASE, NOT A CORNER. A consolidation reaches the mark only when
// the fullest partitions overshoot it; where most resident segments span several
// partitions — which segments sealed before a doubling do by construction
// (searchengine/bucket.go) — the per-partition candidates run out while the count is
// still between the mark and the budget. If the latch survives that, the next
// crossing declines to census until the set has grown by the whole slack, and the
// SUSTAINED count sits at budget + slack while consolidatable material waits
// resident. That is the budget the search fan-out's latency was measured at, missed
// by the width of the headroom.
//
// THE FIXTURE BUILDS THE STATE DIRECTLY, on the mock engine the disclosure row uses:
// one segment short of the budget that span BOTH partitions and can never be taken,
// plus a partition's worth of single-partition segments that can. The first bound
// takes every candidate it has and lands at the budget, above the mark; then fewer
// new consolidatable segments than the slack arrive and cross the budget again.
func TestResidentBoundReCensusesAfterAPartialConsolidation(t *testing.T) {
	t.Parallel()
	engine := searchengine.New[mockQuery, mockStats](mockFormat{}, searchengine.Options{
		MinSegmentDocs:     1,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
	})
	t.Cleanup(engine.Close)
	dm := newDistManager(engine, nil, graphSelector(boundGraphType, "partial-consolidation"), "mock")

	budget := searchengine.ResidentSegmentFanoutBudget
	lowWater, slack := residentLowWater(spanCount)
	const firstCandidates, secondCandidates = 10, 12
	require.Less(t, secondCandidates, slack,
		"PRECONDITION: the second crossing must arrive with LESS new material than the slack (%d), or the latch "+
			"re-arms on growth alone and this row is green whatever the bound does with a partial consolidation", slack)

	for i := range budget - 1 {
		_, err := engine.AddSealAndSupersede(spanningPair(t, i))
		require.NoError(t, err)
	}
	for i := range firstCandidates {
		_, err := engine.AddSealAndSupersede([]searchengine.Document{soloBucketDoc(t, i)})
		require.NoError(t, err)
	}
	before := engine.ResidentSegmentCount()
	require.Greater(t, before, budget, "PRECONDITION: the set must be over budget, or the bound returns at the crossing gate")

	boundResidentSegments(dm, spanCount)

	afterFirst := engine.ResidentSegmentCount()
	require.Equal(t, int64(1), dm.segmentSpanCensuses(), "PRECONDITION: the first crossing censused")
	require.LessOrEqual(t, afterFirst, budget, "PRECONDITION: the first consolidation took every candidate it had")
	require.Greater(t, afterFirst, lowWater,
		"PRECONDITION: and RAN OUT above the low-water mark (%d) — the regime this row exists for; a consolidation "+
			"that reached the mark is the case the sibling rows already drive", lowWater)

	for i := range secondCandidates {
		_, err := engine.AddSealAndSupersede([]searchengine.Document{soloBucketDoc(t, firstCandidates+i)})
		require.NoError(t, err)
	}
	observed := engine.ResidentSegmentCount()
	require.Greater(t, observed, budget, "PRECONDITION: the set crossed the budget again")
	require.NotEmpty(t, consolidatablePartitions(engine.SegmentSpans(spanCount)),
		"PRECONDITION: and a partition a per-partition swap can reduce is present, so a census would find work")

	boundResidentSegments(dm, spanCount)

	sustained := engine.ResidentSegmentCount()
	t.Logf("partial consolidation: %d -> %d (budget=%d, low_water=%d, slack=%d); second crossing observed %d -> %d over %d censuses",
		before, afterFirst, budget, lowWater, slack, observed, sustained, dm.segmentSpanCensuses())

	require.Equal(t, int64(2), dm.segmentSpanCensuses(),
		"the second crossing must census: the first consolidation TOOK something, so it lowered the count the next "+
			"crossing is compared against, and a latch armed with the pre-consolidation count declines the census "+
			"while consolidatable partitions sit resident")
	require.LessOrEqual(t, sustained, budget,
		"the SUSTAINED resident count after the second crossing was %d against a budget of %d; a latch that survives "+
			"a partial consolidation holds the count up at budget + slack (%d) while material a per-partition swap "+
			"could take stays resident", sustained, budget, budget+slack)
}

// soloBucketDoc builds a document whose id occupies ONE partition at spanCount, so a
// segment sealed over it alone is a candidate a per-partition swap can take. Every
// call returns an id in the SAME partition, because a candidate set spread over both
// would leave neither partition with the two constituents a swap needs. It SEARCHES
// for the id for the reason spanningPair states: BucketOf is a hash.
func soloBucketDoc(t *testing.T, seed int) searchengine.Document {
	t.Helper()
	for i := range 1000 {
		id := fmt.Sprintf("solo-%05d-%04d", seed, i)
		if searchengine.BucketOf(id, spanCount) == 0 {
			return doc(id, "alpha beta")
		}
	}
	t.Fatalf("soloBucketDoc(%d): no id landed in partition 0 of %d after 1000 tries", seed, spanCount)
	return searchengine.Document{}
}

// TestResidentLowWaterLeavesAtLeastALeaseOfSlack pins the mark's own arithmetic.
func TestResidentLowWaterLeavesAtLeastALeaseOfSlack(t *testing.T) {
	t.Parallel()
	budget := searchengine.ResidentSegmentFanoutBudget
	// 128 and 1,024 are the two the arithmetic is stated against: the first is the
	// largest count where k x bucketCount fits under the clamp (3,072), the second is
	// BucketCountFor's own cap, where it does not and the clamp gives 2,048.
	for _, bucketCount := range []int{1, 8, 64, 128, 1024} {
		low, slack := residentLowWater(bucketCount)
		require.Less(t, low, budget, "bucketCount %d: the mark must be BELOW the budget or it is not a low-water mark", bucketCount)
		require.Equal(t, budget-low, slack, "bucketCount %d: the slack reported must be the REALIZED headroom", bucketCount)
		require.GreaterOrEqual(t, slack, min(residentLowWaterBatches*bucketCount, budget/2),
			"bucketCount %d: the headroom must be %d write batches' worth — a batch adds up to bucketCount segments, "+
				"so one batch of headroom is crossed again on the very next write — unless the half-budget clamp binds",
			bucketCount, residentLowWaterBatches)
		require.GreaterOrEqual(t, low, budget/2,
			"bucketCount %d: and never below half the budget, or the bound gives back the residency it exists to hold", bucketCount)
	}
}

// boundArm resolves the engine arm a format names, so a row can read the seal-path
// high-water this package records per engine.
func boundArm(mgr *Manager, name, format string) coverageArm {
	if format == bm25.New().Name() {
		return mgr.bm25ManagerFor(boundGraphType, name)
	}
	return mgr.managerFor(boundGraphType, name)
}

// requireBoundHeld is what every per-format row asserts, so the two legs cannot
// drift into asserting different things about the same mechanism.
func requireBoundHeld(t *testing.T, mgr *Manager, name, format string, sustained, bucketCount int) {
	t.Helper()
	budget := searchengine.ResidentSegmentFanoutBudget

	require.Positive(t, mgr.ResidentSegmentCount(boundGraphType, name, format),
		"PRECONDITION: an engine holding nothing satisfies any bound and proves none")
	require.LessOrEqual(t, sustained, budget,
		"the SUSTAINED resident %s segment count — read after every write batch — peaked at %d against a budget of %d; "+
			"a search fans out one goroutine per resident segment, so this count is interactive query latency",
		format, sustained, budget)

	// AND THE INTRA-CALL EXCURSION, read where it happens. The bound acts inside the
	// write call, so the reading above cannot see the count the batch's own seals
	// reached before it acted; this one is sampled on the seal path immediately
	// before the bound. What it must hold is not the budget — a lease legitimately
	// seals one segment per partition it touches before anything can consolidate —
	// but the budget PLUS that width.
	peak := boundArmPeak(mgr, name, format)
	require.Greater(t, peak, budget,
		"ANTI-VACUITY: the seal path must actually have crossed the budget (%d against %d), or the bound was never asked to act "+
			"and every assertion here is about a fixture that stayed small", peak, budget)
	require.LessOrEqual(t, peak-budget, bucketCount,
		"the intra-call excursion was %d segments over the budget against a partition count of %d; "+
			"a lease may seal one segment per partition it touches before the bound acts, and no more",
		peak-budget, bucketCount)
}

// boundArmPeak reads the seal-path high-water for one format's engine.
func boundArmPeak(mgr *Manager, name, format string) int {
	if format == bm25.New().Name() {
		return mgr.bm25ManagerFor(boundGraphType, name).residentSegmentPeak()
	}
	return mgr.managerFor(boundGraphType, name).residentSegmentPeak()
}

// TestResidentSegmentCountStaysInsideTheFanoutBudget is the bound itself.
func TestResidentSegmentCountStaysInsideTheFanoutBudget(t *testing.T) {
	t.Run("the_field_engine_bm25v2", func(t *testing.T) {
		t.Parallel()
		const name = "bound-bm25"
		mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
		format := bm25.New().Name()
		sustained, bucketCount := driveBoundFixture(t, mgr, name, format, true)
		requireBoundHeld(t, mgr, name, format, sustained, bucketCount)
	})

	t.Run("the_vector_engine_hnswv3", func(t *testing.T) {
		t.Parallel()
		const name = "bound-hnsw"
		mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
		format := hnsw.New().Name()
		sustained, bucketCount := driveBoundFixture(t, mgr, name, format, false)
		// The keyed wiring breached on BOTH of its formats, so this leg is not the
		// field engine's shadow and asserts the same two maxima.
		requireBoundHeld(t, mgr, name, format, sustained, bucketCount)
	})

	t.Run("the_documents_stay_searchable_across_a_consolidation", func(t *testing.T) {
		t.Parallel()
		const name = "bound-searchable"
		mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
		ctx := context.Background()
		format := bm25.New().Name()
		sustained, bucketCount := driveBoundFixture(t, mgr, name, format, true)
		requireBoundHeld(t, mgr, name, format, sustained, bucketCount)

		// THE LOSS ASSERTION COMES FIRST, and the order is load-bearing rather than
		// stylistic. A consolidation REMOVES every constituent it resolves, so a
		// segment spanning several partitions offered to a PER-PARTITION swap takes
		// the members it holds for the partitions that swap is not rebuilding with
		// it. Dropping the closure guard makes that happen AND takes the spanning
		// segments out of the resident set, so an anti-vacuity row asserted first
		// would fail on the fixture rather than on the loss, and a reader would be
		// told the fixture was wrong when the data was gone.
		first := boundDoc(0)
		hits, err := mgr.Search(ctx, boundGraphType, name, "bound00000", nil, 10)
		require.NoError(t, err)
		ids := make([]searchengine.ExternalID, 0, len(hits))
		for _, h := range hits {
			ids = append(ids, h.ID)
		}
		require.Contains(t, ids, first.ID,
			"a document the consolidation failed to carry is GONE; this is the row that tells a bound from data loss")

		// AND THE FIXTURE MUST HAVE CONTAINED SEGMENTS SPANNING SEVERAL PARTITIONS,
		// or the row never reached the closure guard at all and the guard could be
		// deleted with it still green. Multi-document batches plus a corpus that
		// crossed a doubling are what produce them; single-document batches produce
		// NONE, because a segment holding one id occupies one partition at every
		// count it will ever be read under.
		spanning := 0
		for _, buckets := range mgr.bm25ManagerFor(boundGraphType, name).engine.SegmentSpans(bucketCount) {
			if len(buckets) > 1 {
				spanning++
			}
		}
		require.Positive(t, spanning,
			"ANTI-VACUITY: no resident segment spans more than one partition, so the closure guard was never consulted")
		t.Logf("resident segments spanning more than one partition at count %d: %d", bucketCount, spanning)
	})
}

// TestResidentBoundInputClasses walks the classes the specification names for the
// consolidation itself, on the cheap accounting rather than on a full fixture.
func TestResidentBoundInputClasses(t *testing.T) {
	t.Parallel()

	t.Run("no_spans_yields_no_candidates", func(t *testing.T) {
		t.Parallel()
		require.Empty(t, consolidatablePartitions(nil))
	})

	t.Run("a_partition_with_two_single_partition_segments_is_a_candidate", func(t *testing.T) {
		t.Parallel()
		got := consolidatablePartitions(map[searchengine.SegmentID][]int{"b": {3}, "a": {3}})
		require.Equal(t, map[int][]searchengine.SegmentID{3: {"a", "b"}}, got,
			"and the constituents come back SORTED, because SegmentSpans returns a map and Go randomizes map range")
	})

	t.Run("a_partition_a_swap_could_not_reduce_is_not_a_candidate", func(t *testing.T) {
		t.Parallel()
		got := consolidatablePartitions(map[searchengine.SegmentID][]int{"a": {3}, "b": {3}, "lonely": {1}})
		require.NotContains(t, got, 1,
			"consolidating a single-constituent partition merges one segment into itself, re-publishes the "+
				"content hash it already carried and removes nothing — a merge paid for no reduction")
		require.Contains(t, got, 3,
			"KNOWN POSITIVE in the same call: the reducible partition is still offered, so the omission above "+
				"is the rule rather than an empty answer")
	})

	t.Run("a_segment_spanning_more_than_one_partition_is_never_offered", func(t *testing.T) {
		t.Parallel()
		got := consolidatablePartitions(map[searchengine.SegmentID][]int{
			"wide": {0, 4}, "narrow": {0}, "alsoNarrow": {0}, "widerStill": {1, 2, 3},
		})
		require.Equal(t, map[int][]searchengine.SegmentID{0: {"alsoNarrow", "narrow"}}, got,
			"ReplaceBucket REMOVES every constituent it resolves, so offering a spanning segment to a "+
				"per-partition swap drops the members it holds for the partitions this call is not rebuilding")
	})
}

// TestResidentSegmentCountsReportsOnlyConstructedEngines is the status surface's
// own contract: the per-format count it renders must never fabricate a format.
func TestResidentSegmentCountsReportsOnlyConstructedEngines(t *testing.T) {
	t.Parallel()
	const name = "counts-one-arm"
	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	require.Nil(t, mgr.ResidentSegmentCounts(boundGraphType, name),
		"PRECONDITION: a graph whose engines have never been constructed reports nothing at all")

	ctx := context.Background()
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, boundGraphType, name, []searchengine.Document{boundDoc(0)}))

	got := mgr.ResidentSegmentCounts(boundGraphType, name)
	require.Contains(t, got, bm25.New().Name(), "the field engine was constructed and holds a segment")
	require.Positive(t, got[bm25.New().Name()])
	require.NotContains(t, got, hnsw.New().Name(),
		"the vector engine was never constructed for this graph, and an absent engine is not an empty one; "+
			"reporting it as zero would also CREATE it, because the per-format accessor constructs lazily")
}

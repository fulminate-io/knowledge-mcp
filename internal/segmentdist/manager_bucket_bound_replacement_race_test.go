// SPDX-License-Identifier: Apache-2.0

// manager_bucket_bound_replacement_race_test.go — the two rows the code review's T3
// findings name: a census OVERTAKEN by a replacement, and the DRAIN as the caller that
// reaches the group swap's clear.
//
// THEY ARE SPLIT FROM THE SIBLING FILE at the repository's file-length gate rather than
// at a seam of meaning; the sibling file holds the two rows that pin the clear itself.

package segmentdist

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestACensusOvertakenByAReplacementSettlesNothing is the race the corpus benchmark's
// own log showed the shape of: the keyless arm cleared the latch at 10:48:39.380 and a
// seal-path census landed 12 ms later.
//
// THE INTERLEAVING, in the bound's own terms (manager_bucket_bound.go): a census reads
// `observed` and the generation together, walks the partitions, reads `after`, and
// settles. A layer or group swap landing between that `after` read and the settle
// clears the latch — and the census then stores the arming point it computed over the
// RETIRED set, re-opening the deferral for a whole slack of growth on a set nobody has
// censused. The clear is not the last writer; the census is.
//
// IT IS DRIVEN AT THE TWO FUNCTIONS rather than through a spawned goroutine, because
// the window is four instructions wide and a scheduler is not an instrument: a
// concurrent drive would pass whether or not the rule holds, depending on the machine.
// The census's inputs ARE its arguments here, so calling the clear between the read and
// the settle reproduces the interleaving exactly and deterministically.
func TestACensusOvertakenByAReplacementSettlesNothing(t *testing.T) {
	t.Parallel()
	engine := searchengine.New[mockQuery, mockStats](mockFormat{}, searchengine.Options{
		MinSegmentDocs:     1,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
	})
	t.Cleanup(engine.Close)
	dm := newDistManager(engine, nil, graphSelector(boundGraphType, "settle-after-replacement"), "mock")

	budget := searchengine.ResidentSegmentFanoutBudget
	_, slack := residentLowWater(reduceBuckets)
	// A census that consolidated something and bought far less than the threshold: the
	// arming disposition, which is the only one that can re-arm a cleared latch.
	shortOfThreshold := func() censusOutcome {
		return censusOutcome{
			observed: budget + 600, after: budget + 596, consolidated: 1,
			minReduction: slack / 2, gen: dm.replacementGen.Load(),
		}
	}

	// CONTROL, BEFORE THE RACE: an ordinary census of this generation ARMS. Without it
	// a rule that simply stopped settling would satisfy every assertion below.
	settleCensusLatch(dm, shortOfThreshold())
	require.Positive(t, dm.lastCensusCount.Load(),
		"CONTROL: a census that consolidated and bought less than the threshold must ARM — this is the disposition "+
			"the race re-applies over a replaced set, and a settle that armed nothing here would make the row vacuous")

	// THE RACE: the second census reads its counts, the swap lands and clears, and only
	// then does the census settle.
	overtaken := shortOfThreshold()
	clearCensusLatchOnReplacement(dm, "reset layer swap")
	settleCensusLatch(dm, overtaken)

	require.Zero(t, dm.lastCensusCount.Load(),
		"a census that read its counts BEFORE a replacement and settled after it must settle NOTHING: the arming "+
			"point it computed (%d) describes the set the swap retired, and storing it here re-opens the deferral "+
			"the clear had just closed — for a whole slack of growth over a set no census has walked", overtaken.after)
	require.Zero(t, dm.lastCensusFloor.Load(),
		"and its FLOOR with it: minCensusReduction compares the next census's reduction against the count the last "+
			"one left, and the count this one left was in the retired set")

	// KNOWN POSITIVE, AFTER THE RACE: a census of the CURRENT generation still settles,
	// so the rule discriminates on the generation rather than disabling the latch.
	settleCensusLatch(dm, shortOfThreshold())
	require.Positive(t, dm.lastCensusCount.Load(),
		"KNOWN POSITIVE: the first census on the NEW set arms exactly as it always did — the generation check drops "+
			"a stale census, it does not retire the latch")
}

// The drain row's fixture, in the arithmetic of the count its own corpus derives
// (BucketCountFor(8,402) = 16, so low-water 3,968, slack 128, threshold 64).
//
// drainSpanning + drainSoloTails sits just above the budget with the tails as the only
// candidates a per-partition swap can take: the census gives back one segment against a
// threshold of 64 and ARMS. The doc count is chosen away from a bucket-count boundary,
// so the write batch below cannot double the count underneath the fixture.
const (
	drainBuckets   = 16
	drainSpanning  = 4200
	drainSoloTails = 2
)

// TestTheDrainClearsTheCensusLatch is the review's T3-2: the group swap's clear reached
// through the PRODUCTION CALLER rather than by calling replaceBucketGroups directly.
//
// THE DRAIN IS THE CALLER THAT MATTERS. ReEmitDirtyBuckets is what the reconcile tick
// and the repair path run, it is the consolidation the bound's own over-budget warning
// defers to by name, and it reaches the swap through drainFormat — three layers the
// sibling row's direct call does not cross. A clear that the drain could not reach
// would leave the latch armed over a set the drain had just rebuilt, which is the
// defect this ticket is about wearing a different coat.
//
// ONE ARM, THE FIELD CORPUS: the write is AddAndMarkDirtyFields, so only bm25 is dirty
// and no HNSW index is built. That keeps this row out of the package's measurement
// suite while driving every layer the defect lives in.
func TestTheDrainClearsTheCensusLatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const name = "latch-clear-on-drain"
	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	dm := mgr.bm25ManagerFor(boundGraphType, name)
	budget := searchengine.ResidentSegmentFanoutBudget

	// SEEDED BY SEALING INTO THE ARM'S OWN ENGINE, which is this package's fixture
	// idiom (the treadmill and equivalence rows do the same): the regime needs one
	// segment per write, and driving 4,202 of them through the write entry point would
	// pay a bound call per seal to build a state the bound is not what this row is
	// about. Everything the row ASSERTS on runs through the production entry points.
	for i := range drainSpanning {
		_, err := dm.engine.AddSealAndSupersede(drainSpanningPair(t, i))
		require.NoError(t, err)
	}
	for seed := range drainSoloTails {
		_, err := dm.engine.AddSealAndSupersede([]searchengine.Document{drainSoloDoc(t, seed)})
		require.NoError(t, err)
	}
	// count-provenance: seed-site. The read is taken over the corpus the loops above
	// just sealed into this arm's own engine, in the same function and with no eviction
	// or reload between, so the resident count IS the fixture's corpus rather than a
	// residency assumption about some caller's state.
	require.Equal(t, drainBuckets, searchengine.BucketCountFor(dm.engine.DistinctResidentDocCount()+1),
		"PRECONDITION: the corpus must derive %d partitions with the write batch below included, or the fixture's "+
			"spanning segments and its candidate partition are measured at a different count than the drain uses",
		drainBuckets)
	require.Greater(t, dm.engine.ResidentSegmentCount(), budget,
		"PRECONDITION: over budget, or the write's bound returns at its crossing gate and arms nothing")

	// ARMED THROUGH THE WRITE ENTRY POINT: AddAndMarkDirtyFields seals, calls the bound
	// (which censuses, consolidates the candidate partition and arms), and records the
	// partition dirty — which is what gives the drain below something to do.
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, boundGraphType, name,
		[]searchengine.Document{doc("drain-arrival", "alpha beta")}))
	armed := dm.lastCensusCount.Load()
	require.Positive(t, armed,
		"CONTROL: the write's census must have ARMED the latch — it consolidated the candidate partition and gave "+
			"back one segment against a threshold of %d — or the drain below is clearing nothing and this row is "+
			"green whatever it did", minCensusReduction(budget+1, 0, 128))

	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, boundGraphType, name))

	require.Zero(t, dm.lastCensusCount.Load(),
		"the drain's group rebuild replaced the partitions the latch was armed over (%d) and retired their tails, so "+
			"the arming point describes segments that are no longer resident: the next crossing must census the set "+
			"the drain published", armed)
	require.Zero(t, dm.lastCensusFloor.Load(),
		"and its FLOOR with it, for the reason the sibling rows state")
}

// drainSpanningPair builds one segment's worth of ids landing in DIFFERENT partitions
// at drainBuckets, so SegmentSpans reports the segment as spanning and a per-partition
// swap may never take it. It MEASURES the partitions rather than assuming a spread, the
// discipline spanningPair states: BucketOf is a hash.
func drainSpanningPair(t *testing.T, seed int) []searchengine.Document {
	t.Helper()
	first := doc(fmt.Sprintf("drain-wide-%06d-a", seed), "alpha beta")
	for i := range 1000 {
		second := doc(fmt.Sprintf("drain-wide-%06d-b%03d", seed, i), "alpha beta")
		if searchengine.BucketOf(first.ID, drainBuckets) != searchengine.BucketOf(second.ID, drainBuckets) {
			return []searchengine.Document{first, second}
		}
	}
	t.Fatalf("drainSpanningPair(%d): no second id landed outside partition %d after 1000 tries",
		seed, searchengine.BucketOf(first.ID, drainBuckets))
	return nil
}

// drainSoloDoc builds a document whose id occupies ONE partition at drainBuckets, and
// always the same one: a candidate set spread over several partitions would leave none
// of them with the two constituents a swap needs.
func drainSoloDoc(t *testing.T, seed int) searchengine.Document {
	t.Helper()
	for i := range 1000 {
		id := fmt.Sprintf("drain-solo-%06d-%03d", seed, i)
		if searchengine.BucketOf(id, drainBuckets) == 0 {
			return doc(id, "alpha beta")
		}
	}
	t.Fatalf("drainSoloDoc(%d): no id landed in partition 0 of %d after 1000 tries", seed, drainBuckets)
	return searchengine.Document{}
}

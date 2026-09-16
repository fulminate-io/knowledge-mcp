// SPDX-License-Identifier: Apache-2.0

// publish_route_census_test.go — the CENSUS the resident-segment bound runs, and
// what the readers answer across it.
//
// SPLIT OUT OF publish_route_replace_test.go when that file reached the
// repository's hard 500-line cap. The seam is the level: that file asserts one
// replacement at the SNAPSHOT level against a flat rebuild of the same entries,
// this one drives whole censuses through the ENGINE — ReplaceBucket per partition
// over a fixture the engine's own write paths built — and asks the engine's
// readers what they answer afterwards.
//
// EVERY FIXTURE STRADDLES THE BASE/TAIL BOUNDARY, for the reason
// publish_route_test.go's header gives: the drop set records base entries only, so
// a snapshot that is all tail cannot see any of this.

package searchengine

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

// routeCensusSmallN and routeCensusLargeN are the two partition counts the
// counting row drives one census at. They differ by a factor of four so a count
// that tracks N cannot coincide with a count that does not; both divide the
// fixture's segments into non-empty partitions at its size.
const (
	routeCensusSmallN = 32
	routeCensusLargeN = 128
)

// routeRebuildCounter counts ROUTE REBUILDS on a live engine by the IDENTITY of
// the base map the published snapshot carries.
//
// THE INSTRUMENT IS THE ROUTE'S OWN STRUCTURE rather than a call counter or the
// statistics fold. Exactly one constructor rebuilds the route — newSegmentSet —
// and it installs a NEW base map; every other snapshot constructor carries its
// predecessor's map by reference. So a base map whose pointer moved IS a rebuild
// and one whose pointer did not is not.
//
// AN AggregateStats COUNTER CANNOT SERVE, which is worth stating because it is the
// obvious instrument: a format's statistics object retains the segments it was
// folded over, so every replacement must re-fold them whether or not it rebuilds
// the route. Such a counter reads one per swap on both trees and can never turn
// green.
//
// IT RETAINS EVERY SNAPSHOT IT OBSERVED, and that is the instrument hazard this
// field exists to close: a map address is only unique while the map is alive, so a
// collected base map could hand its address to its successor and hide a rebuild.
// Holding the snapshots keeps every observed map reachable for the test's lifetime.
type routeRebuildCounter[Q, S any] struct {
	engine    *SegmentedIndex[Q, S]
	observed  []*segmentSet[Q, S]
	last      uintptr
	rebuilds  int
	published int
}

func newRouteRebuildCounter[Q, S any](e *SegmentedIndex[Q, S]) *routeRebuildCounter[Q, S] {
	set := e.set.Load()
	return &routeRebuildCounter[Q, S]{engine: e, observed: []*segmentSet[Q, S]{set}, last: baseMapIdentity(set)}
}

// observe reads the currently published snapshot and records whether its base map
// is a DIFFERENT map from the one the previous observation saw.
func (c *routeRebuildCounter[Q, S]) observe() {
	set := c.engine.set.Load()
	c.observed = append(c.observed, set)
	c.published++
	if id := baseMapIdentity(set); id != c.last {
		c.rebuilds++
		c.last = id
	}
}

func baseMapIdentity[Q, S any](set *segmentSet[Q, S]) uintptr {
	return reflect.ValueOf(set.base).Pointer()
}

// partitionConstituents names the resident segments holding at least one member of
// the given partition — what a caller closing its rebuilt partitions under
// constituency offers ReplaceBucket. Over the one-document segments of
// seedRouteFixture each segment falls in exactly one partition, so the census
// below is closed by construction.
func partitionConstituents[Q, S any](set *segmentSet[Q, S], bucket, bucketCount int) []SegmentID {
	ids := make([]SegmentID, 0, len(set.entries))
	for _, entry := range set.entries {
		for id := range entry.members {
			if BucketOf(id, bucketCount) == bucket {
				ids = append(ids, entry.meta.ID)
				break
			}
		}
	}
	return ids
}

// consolidateCensus drives ONE census: every partition of a bucketCount-way split
// consolidated through ReplaceBucket, in partition order, against that partition's
// own constituents. It is the shape the resident-segment bound runs.
func consolidateCensus(
	t *testing.T, e *SegmentedIndex[mockQuery, mockStats],
	c *routeRebuildCounter[mockQuery, mockStats], bucketCount int,
) {
	t.Helper()
	for bucket := range bucketCount {
		constituents := partitionConstituents(e.set.Load(), bucket, bucketCount)
		require.NotEmpty(t, constituents,
			"PRECONDITION: partition %d of %d holds no segment, so this census is shorter than N", bucket, bucketCount)
		id, err := e.ReplaceBucket(bucket, bucketCount, constituents, nil, nil)
		require.NoError(t, err)
		require.NotEmpty(t, id, "partition %d of %d published nothing", bucket, bucketCount)
		c.observe()
	}
}

// TestRouteRebuildsPerCensusDoNotGrowWithThePartitionCount is the counting row:
// the resident bound consolidates every partition of a census through
// ReplaceBucket, and the route must not be rebuilt once per partition.
//
// IT DRIVES TWO DIFFERENT N AND COMPARES THEM, because the requirement is about
// the SHAPE of the count rather than about any one number: a single census at one
// N cannot tell a constant apart from a count that happens to be small. A route
// rebuilt per swap reads N at each N; an amortized one reads the same figure at
// both, since what bounds it is the corpus the census walks rather than how many
// partitions it is cut into.
func TestRouteRebuildsPerCensusDoNotGrowWithThePartitionCount(t *testing.T) {
	rebuilds := map[int]int{}
	for _, bucketCount := range []int{routeCensusSmallN, routeCensusLargeN} {
		e := seedRouteFixture(t)
		distinctBefore := e.DistinctResidentDocCount()
		c := newRouteRebuildCounter(e)

		consolidateCensus(t, e, c, bucketCount)

		require.Equal(t, bucketCount, c.published,
			"PRECONDITION: the census must have published one segment per partition, or the counter observed fewer swaps than N")
		require.Equal(t, distinctBefore, e.DistinctResidentDocCount(),
			"a census that carries every live member forward changes no document; a moved count means the census lost or duplicated one")
		rebuilds[bucketCount] = c.rebuilds
	}

	require.Equal(t, rebuilds[routeCensusSmallN], rebuilds[routeCensusLargeN],
		"route rebuilds per census must not move with the partition count N: %d rebuilds at N=%d against %d at N=%d",
		rebuilds[routeCensusSmallN], routeCensusSmallN, rebuilds[routeCensusLargeN], routeCensusLargeN)
	require.Less(t, rebuilds[routeCensusLargeN], routeCensusSmallN,
		"a census must rebuild the route fewer times than the SMALLEST partition count it is driven at; %d rebuilds at N=%d is one per swap",
		rebuilds[routeCensusLargeN], routeCensusLargeN)
	require.LessOrEqual(t, rebuilds[routeCensusLargeN], 1+routeFixtureSegments/routeDropLimit,
		"the flatten triggers bound a census to one rebuild per routeDropLimit base entries it consolidates")
	t.Logf("route rebuilds per census: %d at N=%d, %d at N=%d, over %d resident segments",
		rebuilds[routeCensusSmallN], routeCensusSmallN, rebuilds[routeCensusLargeN], routeCensusLargeN, routeFixtureSegments)
}

// TestTheBaseDropSetStaysInsideItsBound is the flatten trigger routeDropLimit
// exists for: the amortization carries a per-snapshot record of what left the base,
// and a record with no bound is a leak rather than an optimisation.
//
// IT ASSERTS THE BOUND WAS REACHED, not merely respected. A census that never
// filled the drop set would satisfy the ceiling while proving nothing about the
// trigger, so the row requires the set to have grown past half the bound at some
// point in the run.
func TestTheBaseDropSetStaysInsideItsBound(t *testing.T) {
	e := seedRouteFixture(t)
	high := 0
	for bucket := range routeCensusLargeN {
		constituents := partitionConstituents(e.set.Load(), bucket, routeCensusLargeN)
		require.NotEmpty(t, constituents)
		_, err := e.ReplaceBucket(bucket, routeCensusLargeN, constituents, nil, nil)
		require.NoError(t, err)
		held := len(e.set.Load().dropped)
		require.LessOrEqual(t, held, routeDropLimit,
			"the drop set outgrew its bound at partition %d: %d recorded drops", bucket, held)
		high = max(high, held)
	}
	require.Greater(t, high, routeDropLimit/2,
		"the census never filled the drop set past %d, so the bound was never exercised", routeDropLimit/2)
}

// shadowedHolderIDs are the three roles the shadowed-holder fixture publishes: an
// id two base segments hold, an id the newer holder alone holds and the output
// carries forward, and an id the newer holder alone holds and the output drops.
const (
	shadowedHolderID  = "shadow-probe"
	shadowedCarriedID = "carried-probe"
	shadowedSoloID    = "solo-probe"
)

// shadowedHolder is the state the drop set is about and the reason the design
// needs a stale-base-hit scan at all: an id held by TWO base segments, whose
// NEWER holder a consolidation is about to remove without carrying it forward.
type shadowedHolder struct {
	engine *SegmentedIndex[mockQuery, mockStats]
	older  *segmentEntry[mockQuery, mockStats]
	newer  SegmentID
}

// seedShadowedHolder builds that state through the engine's own write paths and
// asserts its own preconditions, because a fixture where the base happened to
// record the OLDER holder would make every row below pass vacuously.
func seedShadowedHolder(t *testing.T) shadowedHolder {
	t.Helper()
	e := seedRouteFixture(t)

	// (1) An older holder for the shadowed id, sitting among the fixture's own
	// segments.
	_, err := e.AddSealAndSupersede([]Document{doc(shadowedHolderID, "probe")})
	require.NoError(t, err)
	older := e.set.Load().entryOf(shadowedHolderID)
	require.NotNil(t, older)

	// (2) The NEWER holder, carrying the shadowed id plus one id it alone holds and
	// will carry forward, and one it alone holds and will drop.
	newer, err := e.AddSealAndSupersede([]Document{
		doc(shadowedHolderID, "probe"), doc(shadowedCarriedID, "probe"), doc(shadowedSoloID, "probe"),
	})
	require.NoError(t, err)
	require.True(t, newer.Created)

	// (3) Flatten, so BOTH holders sit in the base and the base map — which records
	// only the last holder of an id — records the newer one.
	for i := range routeTailLimit + 1 {
		_, err := e.AddSealAndSupersede([]Document{doc(fmt.Sprintf("replace-fill%08d", i), "x")})
		require.NoError(t, err)
	}
	set := e.set.Load()
	require.Equal(t, newer.ID, set.base[shadowedHolderID], "PRECONDITION: the base must record the NEWER holder")
	require.Positive(t, set.baseN)
	require.NotNil(t, set.entryByID(older.meta.ID), "PRECONDITION: the older holder must still be resident")
	return shadowedHolder{engine: e, older: older, newer: newer.ID}
}

// TestReadersAgreeAcrossAConsolidationThatDropsABaseHolder is the hazard surface
// read through the ENGINE, reader by reader, on the state the design is about: an
// id held by two base segments whose NEWER holder a consolidation removes without
// carrying it forward.
//
// EVERY ROW CARRIES A KNOWN POSITIVE IN THE SAME RUN — the id the output DID carry
// — so an instrument that answered "absent" to everything cannot pass it.
func TestReadersAgreeAcrossAConsolidationThatDropsABaseHolder(t *testing.T) {
	const (
		shadowed = shadowedHolderID
		carried  = shadowedCarriedID
		solo     = shadowedSoloID
	)
	fx := seedShadowedHolder(t)
	e, olderShadow := fx.engine, fx.older

	// Kill the two ids the output must not carry, then consolidate the newer holder
	// alone. bucketCount 1 accepts every live member, so the output is exactly what
	// survives the deletes.
	e.Delete(shadowed)
	e.Delete(solo)
	require.False(t, residentMemberIn(e.set.Load().entryOf(shadowed), shadowed), "PRECONDITION: the routed copy must be dead")
	liveBefore := e.LiveResidentCount()
	distinctBefore := e.DistinctResidentDocCount()
	searchBefore := e.Search(mockQuery{term: "probe"}, 10)

	out, err := e.ReplaceBucket(0, 1, []SegmentID{fx.newer}, nil, nil)
	require.NoError(t, err)
	require.NotEmpty(t, out)

	after := e.set.Load()
	require.Nil(t, after.entryByID(fx.newer), "PRECONDITION: the newer holder must have left the set")

	// THE READERS, one at a time. The carried id is the known positive throughout.
	t.Run("routeOf_and_entryOf", func(t *testing.T) {
		sid, routed := after.routeOf(shadowed)
		require.True(t, routed, "a dropped base holder must fall back to the older surviving one, not answer absent")
		require.Equal(t, olderShadow.meta.ID, sid)
		// Guarded before the dereference, for the reason the VectorByID row below
		// states: a broken route answers nil here, and a nil deref reds by panic.
		resolved := after.entryOf(shadowed)
		require.NotNil(t, resolved, "entryOf must resolve the older surviving holder, not answer nil")
		require.Equal(t, olderShadow.meta.ID, resolved.meta.ID)

		_, routed = after.routeOf(solo)
		require.False(t, routed, "an id no surviving entry holds must be unrouted")
		require.Nil(t, after.entryOf(solo))

		sid, routed = after.routeOf(carried)
		require.True(t, routed, "KNOWN POSITIVE: the carried-forward id must resolve")
		require.Equal(t, out, sid, "the carried id must resolve to the consolidated output")
	})

	t.Run("rangeRoute", func(t *testing.T) {
		walked := rangeRouteMap(after)
		require.Equal(t, olderShadow.meta.ID, walked[shadowed], "the walk must emit the surviving holder")
		require.NotContains(t, walked, solo, "the walk must not emit an id that left the corpus")
		require.Equal(t, out, walked[carried], "KNOWN POSITIVE")
		require.Len(t, walked, after.distinct, "the walk must visit exactly the distinct resident ids")
	})

	t.Run("aggregates", func(t *testing.T) {
		require.Equal(t, distinctBefore-1, e.DistinctResidentDocCount(),
			"exactly one id — the one no surviving entry holds — leaves the resident corpus")
		require.Equal(t, liveBefore, e.LiveResidentCount(), "the consolidation carried every LIVE member forward")
		require.Equal(t, []ExternalID{shadowed}, e.UncoveredFrom([]ExternalID{shadowed}),
			"the shadowed id resolves to the older holder, whose copy is dead, so it is uncovered")
		require.Equal(t, []ExternalID{solo}, e.UncoveredFrom([]ExternalID{solo}))
		require.Empty(t, e.UncoveredFrom([]ExternalID{carried}), "KNOWN POSITIVE: the carried id is covered")
	})

	t.Run("search_returns_the_same_documents", func(t *testing.T) {
		require.NotEmpty(t, searchBefore, "KNOWN POSITIVE: the query must match something before the swap")
		require.Equal(t, searchBefore, e.Search(mockQuery{term: "probe"}, 10),
			"a consolidation that carries every live member forward must not move the hit list")
	})

	t.Run("delete_and_re_add", func(t *testing.T) {
		e.Delete(solo) // unrouted: a no-op rather than a panic.
		e.Delete(shadowed)
		require.False(t, residentMemberIn(after.entryOf(shadowed), shadowed))

		distinct := e.DistinctResidentDocCount()
		_, err := e.AddSealAndSupersede([]Document{doc(solo, "probe")})
		require.NoError(t, err)
		require.Equal(t, distinct+1, e.DistinctResidentDocCount(), "an id that left the corpus re-enters it as a NEW distinct id")
		_, err = e.AddSealAndSupersede([]Document{doc(shadowed, "probe")})
		require.NoError(t, err)
		require.Equal(t, distinct+1, e.DistinctResidentDocCount(), "an id an older entry still holds is NOT a new distinct id")
	})
}

// TestVectorByIDAgreesAcrossAConsolidationThatDropsABaseHolder is the same class
// read through the by-id stored-vector path, which is the one reader the mock
// format cannot exercise: mockSegment carries no VectorByID accessor, so every
// answer through it is (nil,false) and a row written on it would be vacuous.
func TestVectorByIDAgreesAcrossAConsolidationThatDropsABaseHolder(t *testing.T) {
	const (
		shadowed = "vec-shadow"
		carried  = "vec-carried"
	)
	e := closeOnCleanup(t, New[mockQuery, mockStats](vecFormat{}, Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  MergeDisabledDeadRatio,
		SegmentCountTarget: MergeDisabledCountTarget,
	}))
	olderVec := []byte{1, 2, 3}
	_, err := e.AddSealAndSupersede([]Document{vecDoc(shadowed, olderVec)})
	require.NoError(t, err)
	older := e.set.Load().entryOf(shadowed)
	require.NotNil(t, older)

	newer, err := e.AddSealAndSupersede([]Document{vecDoc(shadowed, []byte{9, 9, 9}), vecDoc(carried, []byte{4, 5, 6})})
	require.NoError(t, err)
	for i := range routeTailLimit + 1 {
		_, err := e.AddSealAndSupersede([]Document{vecDoc(fmt.Sprintf("vec-fill%08d", i), []byte{7})})
		require.NoError(t, err)
	}
	set := e.set.Load()
	require.Equal(t, newer.ID, set.base[shadowed], "PRECONDITION: the base must record the NEWER holder")

	got, ok := e.VectorByID(carried)
	require.True(t, ok, "KNOWN POSITIVE: the carried id must resolve a vector before the swap")
	require.Equal(t, []byte{4, 5, 6}, got)

	e.Delete(shadowed)
	_, err = e.ReplaceBucket(0, 1, []SegmentID{newer.ID}, nil, nil)
	require.NoError(t, err)

	require.Nil(t, e.set.Load().entryByID(newer.ID), "PRECONDITION: the newer holder must have left the set")
	// RESOLVED INTO A VARIABLE AND GUARDED, never dereferenced inline: entryOf is
	// exactly what the drop set and the stale-base-hit scan decide, so this is the
	// call a broken one answers nil for — and a nil deref here reds by PANIC, which
	// aborts the whole package binary and takes every later row's verdict with it.
	// A row's red must be an assertion.
	surviving := e.set.Load().entryOf(shadowed)
	require.NotNil(t, surviving, "the by-id read must resolve through the older surviving holder, not answer nil")
	require.Equal(t, older.meta.ID, surviving.meta.ID,
		"the by-id read must resolve through the older surviving holder")
	_, ok = e.VectorByID(shadowed)
	require.False(t, ok, "the older copy is dead, so the vector must not resolve — the same answer a flat rebuild gives")
	got, ok = e.VectorByID(carried)
	require.True(t, ok, "KNOWN POSITIVE: the carried id still resolves after the swap")
	require.Equal(t, []byte{4, 5, 6}, got)
}

// requireDistinctIsExactAcrossReplacements is TestDistinctResidentDocCountIsExact's
// REPLACEMENT arm, carried here because its classes need this file's fixtures. It
// walks the input classes of a consolidation against the count that derives
// partition numbers — the path that no longer rebuilds the route and therefore no
// longer re-derives that count from a fresh flatten.
//
// EACH CLASS GETS ITS OWN FIXTURE: the count is cumulative state, so classes
// sharing an engine would hide an error that cancels out.
func requireDistinctIsExactAcrossReplacements(t *testing.T) {
	t.Run("the_output_carries_every_constituent_id", func(t *testing.T) {
		e := seedRouteFixture(t)
		before := e.DistinctResidentDocCount()
		ids := partitionConstituents(e.set.Load(), 0, 2)
		require.NotEmpty(t, ids, "PRECONDITION: the partition must have constituents")
		_, err := e.ReplaceBucket(0, 2, ids, nil, nil)
		require.NoError(t, err)
		require.Equal(t, before, e.DistinctResidentDocCount(),
			"a consolidation that carries every live member forward adds and removes no document")
	})

	t.Run("a_deleted_id_no_other_entry_holds_leaves_the_count", func(t *testing.T) {
		e := seedRouteFixture(t)
		id := routeFixtureID(10)
		require.Equal(t, 1, holdersOf(e.set.Load(), id), "PRECONDITION: exactly one segment may hold this id")
		e.Delete(id)
		before := e.DistinctResidentDocCount()
		bucket := BucketOf(id, 2)
		_, err := e.ReplaceBucket(bucket, 2, partitionConstituents(e.set.Load(), bucket, 2), nil, nil)
		require.NoError(t, err)
		require.Equal(t, before-1, e.DistinctResidentDocCount(),
			"an id whose only holder was consolidated away without carrying it forward leaves the resident corpus")
	})

	t.Run("a_deleted_id_an_older_base_entry_still_holds_keeps_its_count", func(t *testing.T) {
		fx := seedShadowedHolder(t)
		e := fx.engine
		e.Delete(shadowedHolderID)
		before := e.DistinctResidentDocCount()
		_, err := e.ReplaceBucket(0, 1, []SegmentID{fx.newer}, nil, nil)
		require.NoError(t, err)
		require.Equal(t, before, e.DistinctResidentDocCount(),
			"an id an OLDER surviving entry still holds is still resident; a route that read a stale base hit as absent would lose it")

		// And still unchanged across the next flatten, which rebuilds the count from
		// the entries rather than carrying it.
		baseNBefore := e.set.Load().baseN
		for i := range routeTailLimit + 1 {
			_, err := e.AddSealAndSupersede([]Document{doc(fmt.Sprintf("post-replace%08d", i), "x")})
			require.NoError(t, err)
		}
		require.Greater(t, e.set.Load().baseN, baseNBefore, "PRECONDITION: a flatten must have happened")
		require.Equal(t, before+routeTailLimit+1, e.DistinctResidentDocCount(),
			"the flatten's own count must agree with the one the replacement maintained")
	})

	t.Run("the_constituents_span_the_base_tail_boundary", func(t *testing.T) {
		e := seedRouteFixture(t)
		set := e.set.Load()
		ids := partitionConstituents(set, 0, 2)
		inBase, inTail := 0, 0
		for i, entry := range set.entries {
			for _, id := range ids {
				if entry.meta.ID != id {
					continue
				}
				if i < set.baseN {
					inBase++
				} else {
					inTail++
				}
			}
		}
		require.Positive(t, inBase, "PRECONDITION: the constituents must include a BASE entry")
		require.Positive(t, inTail, "PRECONDITION: the constituents must include a TAIL entry")

		before := e.DistinctResidentDocCount()
		_, err := e.ReplaceBucket(0, 2, ids, nil, nil)
		require.NoError(t, err)
		require.Equal(t, before, e.DistinctResidentDocCount(),
			"a consolidation spanning the boundary must account for both levels")
	})
}

// holdersOf counts how many resident entries hold an id — the fixture control for
// a class whose whole point is that exactly one, or more than one, does.
func holdersOf[Q, S any](set *segmentSet[Q, S], id ExternalID) int {
	n := 0
	for _, entry := range set.entries {
		if _, held := entry.members[id]; held {
			n++
		}
	}
	return n
}

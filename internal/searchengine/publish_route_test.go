// SPDX-License-Identifier: Apache-2.0

// publish_route_test.go — the correctness matrix of the TWO-LEVEL ROUTE
// (GitHub issue #172, requirement R6).
//
// The publish-cost gates next door say a publish got cheap. These say it still
// answers the same questions: newest-append-wins across the base/tail boundary,
// superseded visibility on both sides of it, deletion on both sides, the exact
// distinct count that derives partition numbers, the cached corpus stats across a
// flatten, and copy-on-write at a scale where the flatten actually happens while
// readers hold an older snapshot.
//
// EVERY FIXTURE HERE STRADDLES THE BOUNDARY ON PURPOSE. A snapshot with fewer than
// routeTailLimit entries is all tail and exercises exactly one of the two levels,
// which is the shape TestSegmentSetCOW's N=1 already covers and the shape that
// cannot see a flatten at all.

package searchengine

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// routeFixtureSegments seeds past routeTailLimit so the snapshot has BOTH a
// flattened base and an unflattened tail. The margin over the limit is what makes
// the tail non-empty after the flatten.
const routeFixtureSegments = routeTailLimit + 200

// routeFixtureID is the id of the single document segment i carries.
func routeFixtureID(i int) ExternalID { return fmt.Sprintf("r%08d", i) }

// seedRouteFixture publishes routeFixtureSegments one-document segments and
// returns the engine, asserting that the snapshot really did straddle the
// boundary — a fixture that is all tail would make half of this file vacuous.
func seedRouteFixture(t *testing.T) *SegmentedIndex[mockQuery, mockStats] {
	t.Helper()
	e := publishCostEngine(t)
	for i := range routeFixtureSegments {
		_, err := e.AddSealAndSupersede([]Document{doc(routeFixtureID(i), "gen0")})
		require.NoError(t, err)
	}
	set := e.set.Load()
	require.Positive(t, set.baseN, "the fixture must have flattened at least once, or the base is never exercised")
	require.Greater(t, len(set.entries), set.baseN, "the fixture must carry an unflattened tail, or the tail is never exercised")
	return e
}

// TestPublishCostWindowSpansSeveralFlattens pins the measurement window in
// publish_cost_test.go to the tail limit it is derived from. Raising the tail
// limit past a quarter of the window would silently turn the amortized figures
// there into a reading of wherever the window happened to start.
func TestPublishCostWindowSpansSeveralFlattens(t *testing.T) {
	require.GreaterOrEqual(t, publishCostWindow, 4*routeTailLimit,
		"the publish-cost window must span at least four flattens; at a tail limit of %d it must be at least %d publishes",
		routeTailLimit, 4*routeTailLimit)
}

// TestRouteResolvesNewestAcrossTheBaseTailBoundary is the newest-append-wins
// contract in its two-level form: an id already answered by the flat base and then
// re-added into the tail must resolve to the TAIL copy, and must keep resolving to
// it once a later flatten folds the tail into a new base.
//
// A tail scanned oldest-first passes every other test in this file and fails this
// one, which is why it exists: the victim resolve in AddSealAndSupersede reads the
// pre-seal snapshot, so an inverted scan spares the stale copy and leaves two live
// copies of one document.
func TestRouteResolvesNewestAcrossTheBaseTailBoundary(t *testing.T) {
	e := seedRouteFixture(t)
	set := e.set.Load()

	// An id from a segment the flatten folded into the base.
	inBase := routeFixtureID(10)
	baseSeg, routed := set.routeOf(inBase)
	require.True(t, routed, "PRECONDITION: the id must be resident before it is re-added")
	require.Equal(t, baseSeg, set.base[inBase], "PRECONDITION: this id must be answered by the BASE, or the test straddles nothing")

	// RE-ADDED TWICE, and the second time is what makes this row discriminating. One
	// re-add leaves exactly ONE tail entry holding the id, and a tail scanned in
	// either direction finds that one — so a single re-add passes with the scan
	// inverted. Two put two copies in the tail, and only a newest-first scan
	// resolves the later one.
	first, err := e.AddSealAndSupersede([]Document{doc(inBase, "gen1")})
	require.NoError(t, err)
	require.True(t, first.Created)
	sealed, err := e.AddSealAndSupersede([]Document{doc(inBase, "gen2")})
	require.NoError(t, err)
	require.True(t, sealed.Created)
	require.NotEqual(t, first.ID, sealed.ID, "PRECONDITION: the two re-adds must be distinct segments")

	next := e.set.Load()
	require.Less(t, next.baseN, len(next.entries)-1, "PRECONDITION: both re-adds must be sitting in the unflattened tail")
	tailSeg, routed := next.routeOf(inBase)
	require.True(t, routed)
	require.Equal(t, sealed.ID, tailSeg,
		"an id re-added twice into the tail must resolve to the LAST copy appended; resolving to the earlier one is the inverted-scan defect, and it makes the victim resolve spare the stale copy")
	require.Equal(t, sealed.ID, next.entryOf(inBase).meta.ID, "entryOf must agree with routeOf about which copy answers")

	// Now force a flatten and ask again: the fold must preserve the same answer.
	for i := range routeTailLimit + 1 {
		_, err := e.AddSealAndSupersede([]Document{doc(fmt.Sprintf("flatten%08d", i), "x")})
		require.NoError(t, err)
	}
	flat := e.set.Load()
	require.Greater(t, flat.baseN, next.baseN, "PRECONDITION: the fixture must have flattened again")
	afterSeg, routed := flat.routeOf(inBase)
	require.True(t, routed)
	require.Equal(t, sealed.ID, afterSeg,
		"the flatten must fold the tail newest-wins; a fold that took the first copy it saw would resurrect the superseded one")
}

// TestSupersededVisibilityOnBothSidesOfTheBoundary is the AddSealAndSupersede
// order contract read through the two-level route: re-adding an id makes the new
// copy searchable and clears the old one, whether the old one was answered by the
// base or by the tail.
func TestSupersededVisibilityOnBothSidesOfTheBoundary(t *testing.T) {
	for _, tc := range []struct {
		name  string
		index int
	}{
		{name: "old_copy_in_the_flat_base", index: 10},
		{name: "old_copy_in_the_unflattened_tail", index: routeFixtureSegments - 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := seedRouteFixture(t)
			id := routeFixtureID(tc.index)
			before := e.set.Load()
			oldEntry := before.entryOf(id)
			require.NotNil(t, oldEntry, "PRECONDITION: the id must be resident")
			oldOrd, held := oldEntry.members[id]
			require.True(t, held)
			require.True(t, oldEntry.live.Live(oldOrd), "PRECONDITION: the old copy must be live before the re-add")

			liveBefore := e.LiveResidentCount()
			sealed, err := e.AddSealAndSupersede([]Document{doc(id, "gen1")})
			require.NoError(t, err)

			require.False(t, oldEntry.live.Live(oldOrd),
				"the pre-existing copy must lose its live bit; a route that resolved the FRESH copy for the victim walk would spare it")
			after := e.set.Load()
			newEntry := after.entryOf(id)
			require.Equal(t, sealed.ID, newEntry.meta.ID)
			require.True(t, residentMemberIn(newEntry, id), "the new copy must be searchable")
			require.Equal(t, liveBefore, e.LiveResidentCount(),
				"a re-add replaces one live copy with another; the live count must not move")
		})
	}
}

// TestDeletionOnBothSidesOfTheBoundary: a deleted id keeps its route entry and
// loses only its live bit, wherever the route answered it from.
func TestDeletionOnBothSidesOfTheBoundary(t *testing.T) {
	for _, tc := range []struct {
		name  string
		index int
	}{
		{name: "resolved_out_of_the_flat_base", index: 42},
		{name: "resolved_out_of_the_unflattened_tail", index: routeFixtureSegments - 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := seedRouteFixture(t)
			id := routeFixtureID(tc.index)
			liveBefore := e.LiveResidentCount()
			distinctBefore := e.DistinctResidentDocCount()

			e.Delete(id)

			set := e.set.Load()
			_, routed := set.routeOf(id)
			require.True(t, routed, "a delete clears a live bit; it must not remove the route entry")
			require.False(t, residentMemberIn(set.entryOf(id), id), "the deleted id must stop being searchable")
			require.Equal(t, liveBefore-1, e.LiveResidentCount(), "exactly one document must leave the live corpus")
			require.Equal(t, distinctBefore, e.DistinctResidentDocCount(),
				"the distinct RESIDENT count counts route entries, which a delete does not remove")
		})
	}
}

// TestDistinctResidentDocCountIsExact is contract 2 of the change: the count that
// derives partition numbers. It is compared against an expectation the TEST
// computes from its own fixture, never read back from the snapshot, because a
// count that drifts silently moves partition boundaries.
func TestDistinctResidentDocCountIsExact(t *testing.T) {
	e := seedRouteFixture(t)
	require.Equal(t, routeFixtureSegments, e.DistinctResidentDocCount(),
		"one document per seeded segment, all distinct")

	// Adding only NEW ids moves it by exactly the new ids.
	_, err := e.AddSealAndSupersede([]Document{doc("fresh-a", "x"), doc("fresh-b", "x")})
	require.NoError(t, err)
	require.Equal(t, routeFixtureSegments+2, e.DistinctResidentDocCount())

	// Re-adding RESIDENT ids must not move it at all — one in the base, one in the
	// tail, and one from the batch just published.
	_, err = e.AddSealAndSupersede([]Document{
		doc(routeFixtureID(7), "gen1"),
		doc(routeFixtureID(routeFixtureSegments-1), "gen1"),
		doc("fresh-a", "gen1"),
	})
	require.NoError(t, err)
	require.Equal(t, routeFixtureSegments+2, e.DistinctResidentDocCount(),
		"a re-add is not a new document; a count that grows here derives a partition count for a corpus that does not exist")

	// A flatten must preserve it.
	beforeFlatten := e.DistinctResidentDocCount()
	baseNBefore := e.set.Load().baseN
	for i := range routeTailLimit + 1 {
		_, err := e.AddSealAndSupersede([]Document{doc(fmt.Sprintf("post%08d", i), "x")})
		require.NoError(t, err)
	}
	require.Greater(t, e.set.Load().baseN, baseNBefore, "PRECONDITION: a flatten must have happened")
	require.Equal(t, beforeFlatten+routeTailLimit+1, e.DistinctResidentDocCount(),
		"the flatten must rebuild the count from the entries it folded, not lose or double it")

	// And a group swap, which rebuilds the route flat, must agree.
	distinctBeforeSwap := e.DistinctResidentDocCount()
	set := e.set.Load()
	constituents := make([]SegmentID, 0, len(set.entries))
	for _, entry := range set.entries {
		constituents = append(constituents, entry.meta.ID)
	}
	_, _, err = e.ReplaceBucketGroup(t.Context(), 1, constituents, []BucketWork{{Bucket: 0}})
	require.NoError(t, err)
	require.Equal(t, distinctBeforeSwap, e.DistinctResidentDocCount(),
		"consolidating every segment into one changes no document; the distinct count must be identical")

	// AND A SINGLE-PARTITION REPLACEMENT, which is the path that no longer rebuilds
	// the route and therefore no longer re-derives this count from a fresh flatten.
	// The classes live in publish_route_census_test.go, beside the fixtures they need.
	t.Run("across_a_replacement", requireDistinctIsExactAcrossReplacements)
}

// TestCachedStatsAgreeAcrossTheFlatten is contract 5: a snapshot's cached corpus
// stats are folded on EVERY publish, and the flattening and non-flattening
// branches must fold the same thing. A snapshot that shared its predecessor's
// stats after appending a segment would score against a corpus that no longer
// exists.
func TestCachedStatsAgreeAcrossTheFlatten(t *testing.T) {
	e := seedRouteFixture(t)

	// Publish up to exactly one entry before a flatten, then take two readings: the
	// one the FLATTENING publish produces, and the one an identical non-flattening
	// publish produces over the same entries.
	set := e.set.Load()
	for len(e.set.Load().entries)-e.set.Load().baseN < routeTailLimit {
		_, err := e.AddSealAndSupersede([]Document{doc(fmt.Sprintf("fill%08d", len(e.set.Load().entries)), "x")})
		require.NoError(t, err)
	}
	set = e.set.Load()
	require.Equal(t, routeTailLimit, len(set.entries)-set.baseN, "PRECONDITION: the next append must flatten")

	f := mockFormat{}
	seg, _, err := f.Build([]Document{doc("stats-probe", "x")})
	require.NoError(t, err)
	entry := &segmentEntry[mockQuery, mockStats]{
		payload: seg, live: newLiveDocs(1), members: idSet{"stats-probe": 0},
		meta: SegmentMeta{ID: "stats-probe-seg", DocCount: 1},
	}

	flattened := set.withAppended(f, entry)
	require.Equal(t, len(flattened.entries), flattened.baseN, "PRECONDITION: that append must have taken the flattening branch")

	// The same entries, built as a fresh flat snapshot — the independent expectation.
	independent := newSegmentSet(f, flattened.entries)
	require.Equal(t, independent.stats, flattened.stats,
		"the flattening publish must fold the corpus stats over the same entries any other snapshot of those entries would")

	// And the non-flattening branch: append onto a snapshot whose tail has room.
	room := newSegmentSet(f, set.entries)
	appended := room.withAppended(f, entry)
	require.Less(t, appended.baseN, len(appended.entries), "PRECONDITION: that append must have taken the tail branch")
	require.Equal(t, flattened.stats, appended.stats,
		"the two branches must fold identical stats over identical entries; a branch that skipped the fold would score against a stale corpus")
}

// TestPublishInputClasses walks the input classes of the publish path.
func TestPublishInputClasses(t *testing.T) {
	t.Run("empty_batch_is_a_no_op", func(t *testing.T) {
		e := publishCostEngine(t)
		res, err := e.AddSealAndSupersede(nil)
		require.NoError(t, err)
		require.Empty(t, res.ID)
		require.False(t, res.Created)
		require.Empty(t, e.set.Load().entries)
	})

	t.Run("publish_onto_an_engine_with_no_entries_at_all", func(t *testing.T) {
		e := publishCostEngine(t)
		set := e.set.Load()
		require.Empty(t, set.entries)
		require.Zero(t, set.baseN)
		res, err := e.AddSealAndSupersede([]Document{doc("only", "x")})
		require.NoError(t, err)
		require.True(t, res.Created)
		require.Equal(t, 1, e.DistinctResidentDocCount())
		sid, routed := e.set.Load().routeOf("only")
		require.True(t, routed)
		require.Equal(t, res.ID, sid)
	})

	t.Run("single_document_batch", func(t *testing.T) {
		e := seedRouteFixture(t)
		before := e.DistinctResidentDocCount()
		res, err := e.AddSealAndSupersede([]Document{doc("single", "x")})
		require.NoError(t, err)
		require.True(t, res.Created)
		require.Equal(t, before+1, e.DistinctResidentDocCount())
	})

	t.Run("a_batch_reproducing_a_resident_segment_is_dropped_as_idempotent", func(t *testing.T) {
		e := seedRouteFixture(t)
		id := routeFixtureID(routeFixtureSegments - 1)
		entriesBefore := len(e.set.Load().entries)
		distinctBefore := e.DistinctResidentDocCount()

		res, err := e.AddSealAndSupersede([]Document{doc(id, "gen0")})
		require.NoError(t, err)
		require.False(t, res.Created, "identical bytes mint the resident segment's own id; the append must be dropped")
		require.Len(t, e.set.Load().entries, entriesBefore, "the dropped append must add no entry")
		require.Equal(t, distinctBefore, e.DistinctResidentDocCount())
		require.True(t, residentMemberIn(e.set.Load().entryOf(id), id),
			"the aliased branch must leave the document searchable")
	})

	t.Run("a_batch_whose_every_id_is_already_resident", func(t *testing.T) {
		e := seedRouteFixture(t)
		distinctBefore := e.DistinctResidentDocCount()
		liveBefore := e.LiveResidentCount()
		docs := []Document{
			doc(routeFixtureID(1), "gen9"),
			doc(routeFixtureID(500), "gen9"),
			doc(routeFixtureID(routeFixtureSegments-2), "gen9"),
		}
		res, err := e.AddSealAndSupersede(docs)
		require.NoError(t, err)
		require.True(t, res.Created)
		require.Equal(t, distinctBefore, e.DistinctResidentDocCount(), "no new document arrived")
		require.Equal(t, liveBefore, e.LiveResidentCount(), "each old copy is replaced by exactly one new one")
	})
}

// TestSegmentSetCOWAtScaleWithConcurrentReaders is R6's explicit row: copy-on-write
// at N greater than one, across a FLATTEN, with readers holding older snapshots
// while publishes run.
//
// THE FLATTEN IS THE TRANSITION TestSegmentSetCOW AT N=1 CANNOT SEE. A reader
// holding a snapshot whose base map is later shared by a hundred descendants must
// keep resolving every id it could resolve when it took that snapshot: the base is
// carried BY REFERENCE, so a publish that mutated it instead of replacing it would
// corrupt every live reader at once, and only a reader that outlives a flatten can
// observe the difference.
//
// It is the -race row for this change: run it alone under -race.
func TestSegmentSetCOWAtScaleWithConcurrentReaders(t *testing.T) {
	e := seedRouteFixture(t)
	held := e.set.Load()
	heldIDs := make([]ExternalID, 0, routeFixtureSegments)
	for i := range routeFixtureSegments {
		heldIDs = append(heldIDs, routeFixtureID(i))
	}
	heldDistinct := held.distinct

	var stop atomic.Bool
	var readerRounds atomic.Int64
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for !stop.Load() {
				for _, id := range heldIDs {
					if _, routed := held.routeOf(id); !routed {
						// t.Error, never t.Fatal: FailNow off the test's goroutine is
						// undefined (gostyle-best-practices-t-fatal-goroutine).
						t.Errorf("a snapshot held across publishes lost id %s — copy-on-write violated", id)
						return
					}
				}
				if held.distinct != heldDistinct {
					t.Errorf("a held snapshot's distinct count moved from %d to %d", heldDistinct, held.distinct)
					return
				}
				readerRounds.Add(1)
			}
		})
	}

	// Publish enough to cross at least one flatten under the readers.
	baseNBefore := held.baseN
	for i := range routeTailLimit + 50 {
		_, err := e.AddSealAndSupersede([]Document{doc(fmt.Sprintf("cow%08d", i), "x")})
		require.NoError(t, err)
	}
	stop.Store(true)
	wg.Wait()

	require.Positive(t, readerRounds.Load(), "the readers must have completed at least one full pass, or they observed nothing")
	require.Greater(t, e.set.Load().baseN, baseNBefore, "PRECONDITION: the publishes must have crossed a flatten")
	require.Len(t, held.entries, routeFixtureSegments, "the held snapshot must be untouched by every publish above")
	require.Equal(t, heldDistinct, held.distinct)

	// And the CURRENT snapshot answers for everything, old ids included.
	current := e.set.Load()
	for _, id := range heldIDs {
		_, routed := current.routeOf(id)
		require.True(t, routed, "the current snapshot lost a seeded id across the flatten: %s", id)
	}
	require.Equal(t, routeFixtureSegments+routeTailLimit+50, current.distinct)
}

// TestRangeRouteVisitsEachDistinctIDOnce is the aggregate walk's own contract: one
// visit per DISTINCT id, with the segment that answers for it. The residency
// aggregates are built on it, and a walk that emitted a superseded copy would
// count a document twice or count the wrong copy's liveness.
func TestRangeRouteVisitsEachDistinctIDOnce(t *testing.T) {
	e := seedRouteFixture(t)
	// Re-add one base id and one tail id, so the same ids sit in more than one entry.
	reAdded := []ExternalID{routeFixtureID(3), routeFixtureID(routeFixtureSegments - 2)}
	for _, id := range reAdded {
		_, err := e.AddSealAndSupersede([]Document{doc(id, "gen1")})
		require.NoError(t, err)
	}

	set := e.set.Load()
	seen := map[ExternalID]int{}
	answered := map[ExternalID]SegmentID{}
	set.rangeRoute(func(id ExternalID, sid SegmentID) {
		seen[id]++
		answered[id] = sid
	})

	require.Len(t, seen, set.distinct, "the walk must visit exactly the distinct resident ids")
	for id, n := range seen {
		require.Equal(t, 1, n, "id %s was visited %d times", id, n)
	}
	for _, id := range reAdded {
		want, _ := set.routeOf(id)
		require.Equal(t, want, answered[id],
			"the walk must report the copy the route resolves — the newest — for a re-added id")
	}
}

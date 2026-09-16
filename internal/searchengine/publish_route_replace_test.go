// SPDX-License-Identifier: Apache-2.0

// publish_route_replace_test.go — what a CONSOLIDATION does to the two-level
// route, asserted at the snapshot level against the rebuild it replaces.
//
// publish_route_test.go pins the SEAL path: an append joins the tail, a flatten
// folds it, and every lookup answers the same on both sides of the boundary. This
// file pins the other publish that derives a snapshot: entries leave the set and
// one output joins it, and the route must reach the same answers without
// rebuilding itself over the whole corpus.
//
// THE ORACLE HERE IS THE FLAT REBUILD ITSELF. Every row compares the snapshot the
// replacement produced against a snapshot BUILT FLAT over the same entries — which
// is precisely what the replacement used to be — so "results are identical" is
// asserted against an independent implementation of the answer rather than against
// a hand-written expectation of it. A hand-written expectation is carried
// alongside for the cases the design names, because two implementations that agree
// can still both be wrong.
//
// THE ENGINE-LEVEL HALF IS IN publish_route_census_test.go: the census counting
// row and the readers, which need a fixture the engine's own write paths build.

package searchengine

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAReplacementWhoseResidualOutgrowsTheBoundFlattens is the third flatten
// trigger, and the one that keeps the amortization honest: maintaining the distinct
// count incrementally costs a probe per residual id, so a replacement whose
// constituents carry more dead members than routeDropLimit would spend more than
// the rebuild it is avoiding. It flattens instead.
//
// IT CARRIES ITS OWN NEGATIVE CONTROL — the same shape one member under the bound,
// which must NOT flatten — because a set that flattened for some other reason
// would pass the positive half alone.
func TestAReplacementWhoseResidualOutgrowsTheBoundFlattens(t *testing.T) {
	for _, tc := range []struct {
		name        string
		dead        int
		wantFlatten bool
	}{
		{name: "residual_inside_the_bound_stays_incremental", dead: routeDropLimit, wantFlatten: false},
		{name: "residual_past_the_bound_flattens", dead: routeDropLimit + 1, wantFlatten: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := mockFormat{}
			members := make([]ExternalID, 0, tc.dead)
			for i := range tc.dead {
				members = append(members, fmt.Sprintf("residual%08d", i))
			}
			wide := routeEntry(t, "wide", members, members...)
			set := newSegmentSet(f, []*segmentEntry[mockQuery, mockStats]{routeEntry(t, "b0", []ExternalID{"keep-a"}), wide})

			next := set.withReplaced(f, map[SegmentID]bool{"wide": true}, routeEntry(t, "out", []ExternalID{"keep-b"}))

			require.Equal(t, tc.wantFlatten, len(next.entries) == next.baseN && next.dropped == nil,
				"flattened=%v with %d residual ids against a bound of %d", !tc.wantFlatten, tc.dead, routeDropLimit)
			// Either way the answers are the flat rebuild's.
			requireAgreesWithAFlatRebuild(t, next, append([]ExternalID{"keep-a", "keep-b"}, members...))
		})
	}
}

// TestTheDropSetNamesExactlyTheBaseEntriesThatLeft is the drop set's own shape,
// asserted per input class so a record that is too wide or too narrow is a named
// failure rather than a downstream symptom.
//
// A DROP SET THAT NAMES A RESIDENT SEGMENT IS THE SUBTLE HALF. A segment id is a
// content hash, so a consolidation can republish the very id one of its
// constituents carried; that segment is resident again, and filing it as dropped
// would make the snapshot's own record disagree with its entries.
func TestTheDropSetNamesExactlyTheBaseEntriesThatLeft(t *testing.T) {
	recorded := 0
	for _, tc := range replaceScenarios(t) {
		t.Run(tc.name, func(t *testing.T) {
			f := mockFormat{}
			set := newSegmentSet(f, tc.base)
			for _, entry := range tc.tail {
				set = set.withAppended(f, entry)
			}
			remove := make(map[SegmentID]bool, len(tc.remove))
			for _, id := range tc.remove {
				remove[id] = true
			}
			next := set.withReplaced(f, remove, tc.output)

			got := make([]SegmentID, 0, len(next.dropped))
			for id := range next.dropped {
				got = append(got, id)
				require.Nil(t, next.entryByID(id), "the drop set must never name a RESIDENT segment (%s)", id)
			}
			require.ElementsMatch(t, tc.wantDropped, got, "the drop set must name exactly the base entries that left")
			recorded += len(got)
		})
	}
	require.Positive(t, recorded, "no scenario recorded a drop at all, so this row asserts nothing")
}

// routeEntry builds one segment entry over the named members, every member live
// except those named in dead. It is the hand-built counterpart of a sealed
// segment, and it exists because the rows below need live bits the engine's own
// write paths cannot produce: AddSealAndSupersede kills the copy it supersedes, so
// an OLDER holder that is still LIVE is unreachable through it.
func routeEntry(
	t *testing.T, id SegmentID, members []ExternalID, dead ...ExternalID,
) *segmentEntry[mockQuery, mockStats] {
	t.Helper()
	docs := make([]Document, 0, len(members))
	for _, m := range members {
		docs = append(docs, doc(m, "probe"))
	}
	seg, _, err := mockFormat{}.Build(docs)
	require.NoError(t, err)
	entry := &segmentEntry[mockQuery, mockStats]{
		payload: seg,
		live:    newLiveDocs(len(members)),
		members: make(idSet, len(members)),
		meta:    SegmentMeta{ID: id, DocCount: len(members)},
	}
	for i, m := range members {
		entry.members[m] = i
	}
	for _, d := range dead {
		ord, held := entry.members[d]
		require.True(t, held, "fixture: segment %s cannot kill member %s it does not hold", id, d)
		entry.live.Kill(ord)
	}
	return entry
}

// rangeRouteMap collects a snapshot's whole route walk into a comparable map.
func rangeRouteMap[Q, S any](set *segmentSet[Q, S]) map[ExternalID]SegmentID {
	out := map[ExternalID]SegmentID{}
	set.rangeRoute(func(id ExternalID, sid SegmentID) { out[id] = sid })
	return out
}

// requireAgreesWithAFlatRebuild is the differential oracle: the snapshot under
// test must answer every route question exactly as a snapshot BUILT FLAT over its
// own entries does. The flat build is the implementation the incremental
// replacement replaces, so this compares two implementations rather than comparing
// a subject with its own answer key.
//
// IT REQUIRES A RESOLVED ID IN THE SAME RUN. Every assertion here passes when both
// sides answer "absent", so a walk over ids none of which is resident compares two
// empty answers and proves nothing.
func requireAgreesWithAFlatRebuild(t *testing.T, got *segmentSet[mockQuery, mockStats], ids []ExternalID) {
	t.Helper()
	want := newSegmentSet(mockFormat{}, got.entries)

	resolved := 0
	for _, id := range ids {
		wantSID, wantOK := want.routeOf(id)
		gotSID, gotOK := got.routeOf(id)
		require.Equal(t, wantOK, gotOK, "routeOf(%s): residency must match the flat rebuild", id)
		require.Equal(t, wantSID, gotSID, "routeOf(%s): must resolve the segment the flat rebuild resolves", id)
		if gotOK {
			resolved++
		}

		wantEntry, gotEntry := want.entryOf(id), got.entryOf(id)
		if wantEntry == nil {
			require.Nil(t, gotEntry, "entryOf(%s): the flat rebuild answers nil", id)
			continue
		}
		require.NotNil(t, gotEntry, "entryOf(%s): the flat rebuild answers %s", id, wantEntry.meta.ID)
		require.Equal(t, wantEntry.meta.ID, gotEntry.meta.ID, "entryOf(%s): must answer the flat rebuild's entry", id)
	}
	require.Positive(t, resolved,
		"the differential resolved no id at all; two empty answers agree without asserting anything")

	// AND EVERY ID THE ROUTE COULD BE ASKED ABOUT, not only the ones the caller
	// named. The stale index answers a base hit whose recorded holder left, and it
	// holds a key only for the ids a removal made ambiguous — so a HOLE in that
	// invariant shows up as an id the flat rebuild resolves and this snapshot calls
	// absent. Walking the ids by hand cannot find a hole nobody thought of; walking
	// the whole base map and every member of every entry can.
	every := map[ExternalID]struct{}{}
	for id := range got.base {
		every[id] = struct{}{}
	}
	for _, entry := range got.entries {
		for id := range entry.members {
			every[id] = struct{}{}
		}
	}
	for id := range every {
		wantSID, wantOK := want.routeOf(id)
		gotSID, gotOK := got.routeOf(id)
		require.Equal(t, wantOK, gotOK, "routeOf(%s): residency must match the flat rebuild over EVERY routable id", id)
		require.Equal(t, wantSID, gotSID, "routeOf(%s): must resolve what the flat rebuild resolves", id)
	}

	// AND THE INDEX NAMES ONLY RESIDENT ENTRIES: a pre-resolved answer pointing at a
	// segment this snapshot no longer holds would both answer wrongly and keep that
	// entry reachable, which is what delays a mapping's release.
	for id, holder := range got.staleIndex {
		require.NotNil(t, holder,
			"the stale index keeps a NIL answer for %s; a missing key and a nil one are the same answer to every "+
				"reader, so the nil only charges the snapshot's heap and counts toward the flatten that bounds it", id)
		require.NotNil(t, got.entryByID(holder.meta.ID),
			"the stale index resolves %s to %s, which this snapshot does not hold", id, holder.meta.ID)
	}

	require.Equal(t, want.distinct, got.distinct,
		"the distinct count must equal the flatten's own definition, which is what the next flatten will install")
	require.Equal(t, rangeRouteMap(want), rangeRouteMap(got), "rangeRoute must emit exactly the flat rebuild's pairs")
	require.Equal(t, want.stats.totalDocs, got.stats.totalDocs,
		"the statistics must be re-folded over the surviving entries; a carried-forward fold scores against a corpus that left")
}

// replaceScenario is one consolidation at the snapshot level: the entries that are
// flattened into the base, the entries appended into the tail behind them, the ids
// the swap removes, and the output it publishes.
type replaceScenario struct {
	name        string
	base        []*segmentEntry[mockQuery, mockStats]
	tail        []*segmentEntry[mockQuery, mockStats]
	remove      []SegmentID
	output      *segmentEntry[mockQuery, mockStats]
	ids         []ExternalID
	wantRoute   map[ExternalID]SegmentID // "" means the id must be absent
	wantLost    int
	wantDropped []SegmentID
}

// TestTheReplacedRouteAnswersExactlyWhatAFlatRebuildAnswers walks the input
// classes of a consolidation against the two-level route, and asserts twice on
// each: against the flat rebuild of the same entries, and against the answer the
// design names for that class.
//
// THE CLASSES ARE THE ONES THE BASE MAP CANNOT ANSWER ALONE. A flat route records
// only the LAST base holder of an id, so a consolidation that removes that holder
// leaves a base hit that is AMBIGUOUS rather than absent: an older surviving base
// entry may still hold the id, in which case the flat rebuild routes to it. Classes
// 3 and 4 are that shape with the older copy dead and live; a route that answered
// "absent" on a stale base hit would pass every other class here.
func TestTheReplacedRouteAnswersExactlyWhatAFlatRebuildAnswers(t *testing.T) {
	for _, tc := range replaceScenarios(t) {
		t.Run(tc.name, func(t *testing.T) {
			f := mockFormat{}
			set := newSegmentSet(f, tc.base)
			require.Equal(t, len(tc.base), set.baseN, "PRECONDITION: the base entries must be flattened")
			for _, entry := range tc.tail {
				set = set.withAppended(f, entry)
			}
			require.Len(t, set.entries, len(tc.base)+len(tc.tail))
			distinctBefore := set.distinct

			remove := make(map[SegmentID]bool, len(tc.remove))
			for _, id := range tc.remove {
				require.NotNil(t, set.entryByID(id), "PRECONDITION: the swap must remove a RESIDENT segment (%s)", id)
				remove[id] = true
			}
			next := set.withReplaced(f, remove, tc.output)

			requireAgreesWithAFlatRebuild(t, next, tc.ids)
			for _, id := range tc.ids {
				sid, routed := next.routeOf(id)
				want, wanted := tc.wantRoute[id]
				require.Equal(t, wanted && want != "", routed, "routeOf(%s) residency", id)
				if routed {
					require.Equal(t, want, sid, "routeOf(%s) must resolve %s", id, want)
				}
			}
			require.Equal(t, distinctBefore-tc.wantLost, next.distinct,
				"the consolidation must end the residency of exactly %d id(s)", tc.wantLost)

			// The receiver is untouched: copy-on-write, asserted on the one structure
			// this change adds a second of.
			require.Len(t, set.entries, len(tc.base)+len(tc.tail), "the receiver snapshot must be unchanged by the replacement")
			require.Equal(t, distinctBefore, set.distinct)
		})
	}
}

// replaceScenarios is the input-class table. Ids are named for their role so a
// failure message reads as the class it belongs to.
//
//nolint:funlen // one table literal; splitting it would hide the classes from each other.
func replaceScenarios(t *testing.T) []replaceScenario {
	t.Helper()
	return []replaceScenario{
		{
			name:        "the_output_carries_every_constituent_member",
			base:        []*segmentEntry[mockQuery, mockStats]{routeEntry(t, "b0", []ExternalID{"keep-a"}), routeEntry(t, "b1", []ExternalID{"keep-b"})},
			remove:      []SegmentID{"b1"},
			output:      routeEntry(t, "out", []ExternalID{"keep-b"}),
			ids:         []ExternalID{"keep-a", "keep-b"},
			wantRoute:   map[ExternalID]SegmentID{"keep-a": "b0", "keep-b": "out"},
			wantDropped: []SegmentID{"b1"},
		},
		{
			name:        "a_dead_member_no_other_entry_holds_leaves_the_corpus",
			base:        []*segmentEntry[mockQuery, mockStats]{routeEntry(t, "b0", []ExternalID{"keep-a"}), routeEntry(t, "b1", []ExternalID{"keep-b", "solo"}, "solo")},
			remove:      []SegmentID{"b1"},
			output:      routeEntry(t, "out", []ExternalID{"keep-b"}),
			ids:         []ExternalID{"keep-a", "keep-b", "solo"},
			wantRoute:   map[ExternalID]SegmentID{"keep-a": "b0", "keep-b": "out", "solo": ""},
			wantLost:    1,
			wantDropped: []SegmentID{"b1"},
		},
		{
			name: "a_dead_member_an_older_base_entry_still_holds_stays_routed_to_that_entry",
			base: []*segmentEntry[mockQuery, mockStats]{
				routeEntry(t, "b0", []ExternalID{"shadowed", "keep-a"}, "shadowed"),
				routeEntry(t, "b1", []ExternalID{"shadowed", "keep-b", "solo"}, "shadowed", "solo"),
			},
			remove:      []SegmentID{"b1"},
			output:      routeEntry(t, "out", []ExternalID{"keep-b"}),
			ids:         []ExternalID{"shadowed", "keep-a", "keep-b", "solo"},
			wantRoute:   map[ExternalID]SegmentID{"shadowed": "b0", "keep-a": "b0", "keep-b": "out", "solo": ""},
			wantLost:    1,
			wantDropped: []SegmentID{"b1"},
		},
		{
			name: "the_older_base_holder_is_still_live_and_keeps_answering",
			base: []*segmentEntry[mockQuery, mockStats]{
				routeEntry(t, "b0", []ExternalID{"shadowed", "keep-a"}),
				routeEntry(t, "b1", []ExternalID{"shadowed", "keep-b"}, "shadowed"),
			},
			remove:      []SegmentID{"b1"},
			output:      routeEntry(t, "out", []ExternalID{"keep-b"}),
			ids:         []ExternalID{"shadowed", "keep-a", "keep-b"},
			wantRoute:   map[ExternalID]SegmentID{"shadowed": "b0", "keep-a": "b0", "keep-b": "out"},
			wantDropped: []SegmentID{"b1"},
		},
		{
			name: "the_constituents_span_the_base_tail_boundary",
			base: []*segmentEntry[mockQuery, mockStats]{
				routeEntry(t, "b0", []ExternalID{"shadowed", "keep-a"}, "shadowed"),
				routeEntry(t, "b1", []ExternalID{"shadowed", "base-solo"}, "shadowed", "base-solo"),
			},
			tail: []*segmentEntry[mockQuery, mockStats]{
				routeEntry(t, "t0", []ExternalID{"keep-b"}),
				routeEntry(t, "t1", []ExternalID{"tail-solo"}, "tail-solo"),
			},
			remove: []SegmentID{"b1", "t1"},
			output: routeEntry(t, "out", []ExternalID{"keep-c"}),
			ids:    []ExternalID{"shadowed", "keep-a", "keep-b", "keep-c", "base-solo", "tail-solo"},
			wantRoute: map[ExternalID]SegmentID{
				"shadowed": "b0", "keep-a": "b0", "keep-b": "t0", "keep-c": "out",
				"base-solo": "", "tail-solo": "",
			},
			wantLost:    1, // base-solo and tail-solo leave; keep-c joins.
			wantDropped: []SegmentID{"b1"},
		},
		{
			name: "the_output_republishes_a_constituents_own_segment_id",
			base: []*segmentEntry[mockQuery, mockStats]{
				routeEntry(t, "b0", []ExternalID{"keep-a"}),
				routeEntry(t, "b1", []ExternalID{"keep-b"}),
			},
			remove:    []SegmentID{"b1"},
			output:    routeEntry(t, "b1", []ExternalID{"keep-b"}),
			ids:       []ExternalID{"keep-a", "keep-b"},
			wantRoute: map[ExternalID]SegmentID{"keep-a": "b0", "keep-b": "b1"},
			// No drop: the output republished the removed segment's own content hash, so
			// that id is resident again and a base hit naming it is not stale.
			wantDropped: nil,
		},
		{
			name:        "the_consolidation_publishes_nothing",
			base:        []*segmentEntry[mockQuery, mockStats]{routeEntry(t, "b0", []ExternalID{"keep-a"}), routeEntry(t, "b1", []ExternalID{"solo"}, "solo")},
			remove:      []SegmentID{"b1"},
			output:      nil,
			ids:         []ExternalID{"keep-a", "solo"},
			wantRoute:   map[ExternalID]SegmentID{"keep-a": "b0", "solo": ""},
			wantLost:    1,
			wantDropped: []SegmentID{"b1"},
		},
	}
}

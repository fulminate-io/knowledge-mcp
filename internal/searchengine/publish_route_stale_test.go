// SPDX-License-Identifier: Apache-2.0

// publish_route_stale_test.go — WHAT A STALE BASE HIT COSTS THE SEAL PATH.
//
// The drop set records CONSOLIDATED-AWAY SEGMENTS, and routeDropLimit bounds how
// many of them a snapshot may carry. That bound says nothing about how many
// DOCUMENTS are affected: one consolidated-away base segment can be the base map's
// recorded holder for every id it held, which on a real corpus is thousands. So the
// cost of resolving a stale hit is multiplied by the number of ids, not by the
// number of segments — and it is multiplied again by how often those ids are looked
// up.
//
// THE SEAL PATH LOOKS THEM UP ONCE PER INCOMING DOCUMENT. AddSealAndSupersede
// resolves each document's id against the pre-seal snapshot to find the copy it
// supersedes (bucket_membership.go), and killSuperseded walks the same route. A
// graph that took deletes and is then re-drained therefore hits dropped holders
// once per document in the batch — on the publish path whose whole design goal is
// to cost what the SEGMENT holds rather than what the ENGINE holds.
//
// THE INSTRUMENT IS A COUNT, NOT A CLOCK: routeBaseEntriesExamined, which the route
// adds to every time it has to look at the flattened entries to answer. A wall
// reading on a shared machine would measure the machine.

package searchengine

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	// staleWideSegments x staleWideDocs is a slab of MULTI-DOCUMENT base segments —
	// the shape that makes a stale base hit expensive. The drop set bounds SEGMENTS;
	// one dropped segment is the base map's recorded holder for every id it held, so
	// these two numbers are the ratio the cost hole lives in.
	staleWideSegments = 16
	staleWideDocs     = 50
	// staleLivePerWide is how many members of each wide segment survive the deletes,
	// so the consolidation publishes a real output rather than an empty one and the
	// non-carried residual stays inside routeDropLimit — this row is about the cost
	// of the stale hits, not about tripping the flatten that would erase them.
	staleLivePerWide = 2
	// staleReDrainDocs is the size of the re-drain batch whose cost is measured. It
	// is small on purpose: the row is about the cost PER DOCUMENT, and a batch large
	// enough to hide a per-document term would defeat it.
	staleReDrainDocs = 32
)

// staleWideID is the id of document d of wide segment sg.
func staleWideID(round, sg, d int) ExternalID { return fmt.Sprintf("wide%02d-%03d-%04d", round, sg, d) }

// seedStaleBaseHits builds the state the cost hole lives in: a flattened snapshot
// whose base map records, for hundreds of ids, a holder that a consolidation has
// since removed.
//
// IT ASSERTS ITS OWN PRECONDITIONS, because every number below is a reading of a
// state rather than of a code path: a fixture whose base hits were not stale, whose
// drop set was empty because a flatten erased it, or whose stale ids merely matched
// its dropped segments one for one, would measure nothing and pass.
func seedStaleBaseHits(t *testing.T) *SegmentedIndex[mockQuery, mockStats] {
	t.Helper()
	e := publishCostEngine(t)
	wide := sealStaleWideSlab(t, e, 0, staleWideSegments)

	// THE INSTRUMENT'S OWN KNOWN POSITIVE, taken around the consolidation that seeds
	// the fixture. The row below asserts a ZERO, and a zero is what a counter nobody
	// increments reads too — so the same counter must be seen MOVING, by the one pass
	// that legitimately walks the flattened level, before its zero on the seal path
	// means anything.
	beforeSeed := routeBaseEntriesExamined.Load()
	consolidateWide(t, e, wide)
	seeded := routeBaseEntriesExamined.Load() - beforeSeed

	set := e.set.Load()
	require.GreaterOrEqual(t, seeded, int64(set.baseN),
		"CONTROL: the consolidation's resolution pass must have walked the SURVIVING flattened level once "+
			"(%d entries, and it examined %d); an instrument that never moves reads zero on the seal path "+
			"whether or not the seal path scans", set.baseN, seeded)

	require.NotEmpty(t, set.dropped, "PRECONDITION: the consolidation must have dropped base entries and kept them recorded")
	require.Positive(t, set.baseN, "PRECONDITION: the snapshot must still have a flattened level")
	stale := 0
	for sg := range staleWideSegments {
		for d := range staleWideDocs - staleLivePerWide {
			if sid, recorded := set.base[staleWideID(0, sg, d)]; recorded && set.dropped[sid] {
				stale++
			}
		}
	}
	require.Greater(t, stale, 10*len(set.dropped),
		"PRECONDITION: the stale base hits (%d) must far outnumber the dropped SEGMENTS (%d) — that gap is the "+
			"cost hole this row exists to measure, and a fixture without it measures nothing",
		stale, len(set.dropped))
	t.Logf("stale-hit fixture: %d stale base hits over %d dropped segments, baseN %d, index %d",
		stale, len(set.dropped), set.baseN, len(set.staleIndex))
	return e
}

// staleFlatten publishes until the next FLATTEN lands, so the segments sealed
// before it are what the BASE map records for their members rather than tail
// entries resolved from their own member maps. A flatten sets baseN to the whole
// entry count, so the snapshot being fully flattened is the loop's own exit
// condition. The tag keeps each call's filler ids distinct: a batch reproducing a
// resident segment's bytes is dropped as idempotent and would never grow the tail.
func staleFlatten(t *testing.T, e *SegmentedIndex[mockQuery, mockStats], round int, tag string) {
	t.Helper()
	set := e.set.Load()
	for i := 0; len(set.entries) != set.baseN; i++ {
		require.Less(t, i, 2*routeTailLimit, "the fixture must reach a flatten")
		_, err := e.AddSealAndSupersede([]Document{doc(fmt.Sprintf("stale-fill%s%02d-%08d", tag, round, i), "x")})
		require.NoError(t, err)
		set = e.set.Load()
	}
}

// sealStaleWideSlab seals one slab of MULTI-DOCUMENT segments over ids an OLDER
// flattened segment already holds, flattens so the base map records the newer
// copies, and deletes all but a couple of members of each — leaving one segment
// standing as the recorded holder for dozens of ids that a consolidation will make
// ambiguous.
func sealStaleWideSlab(t *testing.T, e *SegmentedIndex[mockQuery, mockStats], round, segments int) []SegmentID {
	t.Helper()
	// THE SHADOW SLAB FIRST, and it is what makes the stale hits REAL answers rather
	// than absences. The index keys only ids an older flattened entry still holds, so
	// a corpus whose deleted ids had exactly one holder each leaves nothing to
	// pre-resolve. Two holders is the honest shape of a re-drain: a document written
	// twice leaves its earlier copy in an older segment, dead but still held.
	for sg := range segments {
		batch := make([]Document, 0, staleWideDocs)
		for d := range staleWideDocs {
			batch = append(batch, doc(staleWideID(round, sg, d), "shadow"))
		}
		_, err := e.AddSealAndSupersede(batch)
		require.NoError(t, err)
	}
	staleFlatten(t, e, round, "shadow")

	for sg := range segments {
		batch := make([]Document, 0, staleWideDocs)
		for d := range staleWideDocs {
			batch = append(batch, doc(staleWideID(round, sg, d), "wide"))
		}
		_, err := e.AddSealAndSupersede(batch)
		require.NoError(t, err)
	}
	staleFlatten(t, e, round, "wide")

	// Delete all but a couple of members of each wide segment. The merge takes live
	// members only, so the deleted ids are not carried into the output and their
	// recorded holders leave the set under them.
	for sg := range segments {
		for d := range staleWideDocs - staleLivePerWide {
			e.Delete(staleWideID(round, sg, d))
		}
	}

	wide := make([]SegmentID, 0, segments)
	for sg := range segments {
		entry := e.set.Load().entryOf(staleWideID(round, sg, staleWideDocs-1))
		require.NotNil(t, entry, "PRECONDITION: wide segment %d must still be resident", sg)
		wide = append(wide, entry.meta.ID)
	}
	return wide
}

// consolidateWide consolidates the named wide segments in ONE swap. bucketCount 1
// accepts every live member, so the output is exactly what survived the deletes.
func consolidateWide(t *testing.T, e *SegmentedIndex[mockQuery, mockStats], wide []SegmentID) {
	t.Helper()
	_, err := e.ReplaceBucket(0, 1, wide, nil, nil)
	require.NoError(t, err)
}

// TestTheSealPathDoesNotScanTheFlattenedLevelPerDocument is the cost row.
//
// A RE-DRAIN IS THE SHAPE A REAL DEPLOYMENT TAKES: the ids whose holders were
// consolidated away are exactly the ids a later drain re-ships, so the batch hits
// them by construction rather than by contrivance.
func TestTheSealPathDoesNotScanTheFlattenedLevelPerDocument(t *testing.T) {
	e := seedStaleBaseHits(t)
	baseN := e.set.Load().baseN

	// Every document in the batch is an id whose base holder was dropped, so every
	// one of them takes the stale path in the victim resolve.
	batch := make([]Document, 0, staleReDrainDocs)
	for i := range staleReDrainDocs {
		batch = append(batch, doc(staleWideID(0, i%staleWideSegments, i/staleWideSegments), "re-drained"))
	}
	for _, d := range batch {
		set := e.set.Load()
		sid, recorded := set.base[d.ID]
		require.True(t, recorded, "PRECONDITION: %s must be recorded in the base map", d.ID)
		require.True(t, set.dropped[sid], "PRECONDITION: %s's recorded holder must be dropped", d.ID)
	}

	before := routeBaseEntriesExamined.Load()
	_, err := e.AddSealAndSupersede(batch)
	require.NoError(t, err)
	examined := routeBaseEntriesExamined.Load() - before

	t.Logf("seal of %d re-drained documents examined %d flattened entries (baseN %d, %.1f per document)",
		staleReDrainDocs, examined, baseN, float64(examined)/float64(staleReDrainDocs))
	require.LessOrEqual(t, examined, int64(staleReDrainDocs),
		"sealing %d documents examined %d flattened entries — the seal path must resolve a stale base hit in "+
			"O(1), not by walking the flattened level (%d entries) once per document. The publish path's whole "+
			"design is to cost what the SEGMENT holds rather than what the ENGINE holds",
		staleReDrainDocs, examined, baseN)
}

// TestTheStaleIndexStaysInsideItsBound is the fourth flatten trigger: the
// pre-resolved answers are per-snapshot heap and a record with no bound is a leak,
// exactly as the drop set is — and the drop set's own count cannot bound it, since
// one dropped segment can make dozens of ids ambiguous.
//
// IT CONSOLIDATES ONE SLAB IN TWO HALVES, with no flatten in between, because that
// is the only way the index ACCUMULATES: a flatten discards it, so a fixture that
// re-flattens between consolidations tops out at one consolidation's residual and
// never reaches the ceiling it claims to be testing. The row asserts the trigger
// actually fired rather than only that the ceiling held.
func TestTheStaleIndexStaysInsideItsBound(t *testing.T) {
	e := publishCostEngine(t)
	wide := sealStaleWideSlab(t, e, 0, 2*staleWideSegments)

	high, flattens := 0, 0
	for _, half := range [][]SegmentID{wide[:len(wide)/2], wide[len(wide)/2:]} {
		consolidateWide(t, e, half)
		set := e.set.Load()
		require.LessOrEqual(t, len(set.staleIndex), routeDropLimit,
			"the stale index outgrew its bound: %d pre-resolved answers", len(set.staleIndex))
		if len(set.entries) == set.baseN {
			flattens++
			require.Empty(t, set.staleIndex, "a flatten must release the pre-resolved answers")
		}
		high = max(high, len(set.staleIndex))
		t.Logf("after a half-slab consolidation: index %d, dropped %d, baseN %d of %d entries",
			len(set.staleIndex), len(set.dropped), set.baseN, len(set.entries))
	}
	require.Greater(t, high, routeDropLimit/2,
		"the halves never filled the index past %d, so the ceiling was never approached", routeDropLimit/2)
	require.Positive(t, flattens,
		"no consolidation tripped the bound, so this row asserts a ceiling nothing ever reached")
}

// ---------------------------------------------------------------------------
// THE INDEX'S OWN DERIVATION. The rows above measure what a stale hit COSTS; the
// rows below pin how the answer is arrived at and carried — which snapshot
// constructors keep it, which entry it names when several could, and when it is
// dropped. They live beside the cost rows rather than beside the differential
// table because all of them are about this structure rather than about one
// replacement's output.
// ---------------------------------------------------------------------------

// TestThePreResolvedAnswersNeverNameADepartedSegment is the one property of the
// stale index that no single replacement can show, and the one a carried-forward
// answer breaks silently.
//
// THE ID THAT EXPOSES IT IS ONE THE OUTPUT CARRIES. An answer whose holder a later
// swap removes is re-resolved for free when that id is RESIDUAL — the residual pass
// overwrites it. When the output carries the id instead, the id is not residual, the
// answer is not re-resolved, and only dropping it while copying keeps the map honest.
// Nothing a reader asks would notice: the tail answers that id until the output
// itself leaves. What notices is the heap — a departed entry named by a live
// snapshot's map stays reachable, and its mapping is never released.
func TestThePreResolvedAnswersNeverNameADepartedSegment(t *testing.T) {
	f := mockFormat{}
	older := routeEntry(t, "b-old", []ExternalID{"shadowed", "keep-a"}, "shadowed")
	newer := routeEntry(t, "b-new", []ExternalID{"shadowed", "keep-b"}, "shadowed")
	set := newSegmentSet(f, []*segmentEntry[mockQuery, mockStats]{older, newer})
	require.Equal(t, SegmentID("b-new"), set.base["shadowed"],
		"PRECONDITION: the base must record the NEWER of the two holders")

	// (1) The newer holder leaves without carrying the shadowed id, so the answer is
	// pre-resolved to the older one.
	one := set.withReplaced(f, map[SegmentID]bool{"b-new": true}, routeEntry(t, "out1", []ExternalID{"keep-b"}))
	require.Same(t, older, one.staleIndex["shadowed"],
		"PRECONDITION: the replacement must have resolved the stale hit to the older holder")

	// (2) The older holder leaves too, but this output CARRIES the shadowed id — so it
	// is not residual and the residual pass never touches its answer.
	two := one.withReplaced(f, map[SegmentID]bool{"b-old": true}, routeEntry(t, "out2", []ExternalID{"shadowed", "keep-a"}))
	require.Nil(t, two.entryByID("b-old"), "PRECONDITION: the older holder must have left the set")
	require.NotContains(t, residualMembers([]*segmentEntry[mockQuery, mockStats]{older},
		routeEntry(t, "out2", []ExternalID{"shadowed", "keep-a"})), ExternalID("shadowed"),
		"PRECONDITION: the id must NOT be residual, or the residual pass would re-resolve it and this row would "+
			"pass without the copy dropping anything")

	requireAgreesWithAFlatRebuild(t, two, []ExternalID{"shadowed", "keep-a", "keep-b"})
}

// TestAnAppendCarriesThePreResolvedAnswers is the SEAL path's half of the carry.
//
// withAppended shares the base map by reference and must share what QUALIFIES it on
// the same terms: an append drops nothing, so every pre-resolved answer is still the
// answer. Publishing nil there is invisible to every other row in this package —
// the shipped suite stays green — because no other fixture asks the route a question
// after appending onto a snapshot that had been replaced into.
func TestAnAppendCarriesThePreResolvedAnswers(t *testing.T) {
	f := mockFormat{}
	older := routeEntry(t, "b-old", []ExternalID{"shadowed", "keep-a"}, "shadowed")
	newer := routeEntry(t, "b-new", []ExternalID{"shadowed", "keep-b"}, "shadowed")
	set := newSegmentSet(f, []*segmentEntry[mockQuery, mockStats]{older, newer})

	one := set.withReplaced(f, map[SegmentID]bool{"b-new": true}, routeEntry(t, "out1", []ExternalID{"keep-b"}))
	require.Same(t, older, one.staleIndex["shadowed"],
		"PRECONDITION: the replacement must have pre-resolved the stale hit")

	appended := one.withAppended(f, routeEntry(t, "t1", []ExternalID{"fresh"}))
	require.Same(t, older, appended.staleIndex["shadowed"],
		"an append drops nothing, so it must carry the answers the base map still needs")
	requireAgreesWithAFlatRebuild(t, appended, []ExternalID{"shadowed", "keep-a", "keep-b", "fresh"})
}

// TestARemapCarriesThePreResolvedAnswers is the mapping swap's half.
//
// A remap replaces payloads under the SAME segment ids and the same member sets, so
// nothing the route is derived from moves — which is exactly why it carries the base
// map, the drop set and these answers by reference rather than re-deriving them. Its
// entries are keyed by INDEX, not by id.
func TestARemapCarriesThePreResolvedAnswers(t *testing.T) {
	f := mockFormat{}
	older := routeEntry(t, "b-old", []ExternalID{"shadowed", "keep-a"}, "shadowed")
	newer := routeEntry(t, "b-new", []ExternalID{"shadowed", "keep-b"}, "shadowed")
	set := newSegmentSet(f, []*segmentEntry[mockQuery, mockStats]{older, newer})

	one := set.withReplaced(f, map[SegmentID]bool{"b-new": true}, routeEntry(t, "out1", []ExternalID{"keep-b"}))
	require.NotNil(t, one.staleIndex["shadowed"], "PRECONDITION: the replacement must have pre-resolved the stale hit")
	require.Equal(t, SegmentID("b-old"), one.entries[0].meta.ID, "PRECONDITION: the remapped index must name the older holder")

	remapped := routeEntry(t, "b-old", []ExternalID{"shadowed", "keep-a"}, "shadowed")
	remap := one.withReplacedPayloads(f, map[int]*segmentEntry[mockQuery, mockStats]{0: remapped})
	require.NotNil(t, remap.staleIndex["shadowed"],
		"a remap changes no segment id and no member set, so every pre-resolved answer is still one")
	requireAgreesWithAFlatRebuild(t, remap, []ExternalID{"shadowed", "keep-a", "keep-b"})
}

// TestThePreResolutionAnswersTheNEWESTSurvivingHolder is flatRoute's "last entry
// holding it wins" read through the pre-resolution, and it needs THREE holders.
//
// EVERY OTHER FIXTURE IN THIS PACKAGE HAS TWO, and with two there is exactly one
// survivor after the removal, so a pass walking the flattened level in either
// direction lands on it. Only a third holder separates oldest-first-with-overwrite
// from newest-first-with-first-wins, and the difference is a resurrected older copy:
// the same defect an inverted TAIL scan would cause, which
// TestRouteResolvesNewestAcrossTheBaseTailBoundary exists for on the other level.
func TestThePreResolutionAnswersTheNEWESTSurvivingHolder(t *testing.T) {
	f := mockFormat{}
	const id = "three-holders"
	oldest := routeEntry(t, "h-oldest", []ExternalID{id}, id)
	middle := routeEntry(t, "h-middle", []ExternalID{id}, id)
	newest := routeEntry(t, "h-newest", []ExternalID{id, "keep"}, id)
	set := newSegmentSet(f, []*segmentEntry[mockQuery, mockStats]{oldest, middle, newest})
	require.Equal(t, SegmentID("h-newest"), set.base[id], "PRECONDITION: the base must record the newest of the three")

	// (1) Remove the newest without carrying the id: the MIDDLE holder answers, not
	// the oldest.
	one := set.withReplaced(f, map[SegmentID]bool{"h-newest": true}, routeEntry(t, "out1", []ExternalID{"keep"}))
	sid, routed := one.routeOf(id)
	require.True(t, routed)
	require.Equal(t, SegmentID("h-middle"), sid,
		"the pre-resolution must answer the NEWEST surviving flattened holder; answering %s is the oldest, which "+
			"resurrects a copy the flat rebuild would never route to", sid)
	requireAgreesWithAFlatRebuild(t, one, []ExternalID{id, "keep"})

	t.Run("the_middle_holder_leaves_without_the_id", func(t *testing.T) {
		two := one.withReplaced(f, map[SegmentID]bool{"h-middle": true}, routeEntry(t, "out2", []ExternalID{"keep-2"}))
		sid, routed := two.routeOf(id)
		require.True(t, routed)
		require.Equal(t, SegmentID("h-oldest"), sid, "the answer must re-resolve to the remaining holder")
		requireAgreesWithAFlatRebuild(t, two, []ExternalID{id, "keep", "keep-2"})
	})

	t.Run("the_middle_holder_leaves_carrying_the_id", func(t *testing.T) {
		// Carried, so the id is not residual and the tail answers it; when THAT output
		// leaves without it, the answer must re-resolve to the oldest holder.
		two := one.withReplaced(f, map[SegmentID]bool{"h-middle": true}, routeEntry(t, "out2", []ExternalID{id, "keep-2"}))
		sid, routed := two.routeOf(id)
		require.True(t, routed)
		require.Equal(t, SegmentID("out2"), sid, "the tail holds it, so the tail answers")
		requireAgreesWithAFlatRebuild(t, two, []ExternalID{id, "keep", "keep-2"})

		three := two.withReplaced(f, map[SegmentID]bool{"out2": true}, routeEntry(t, "out3", []ExternalID{"keep-3"}))
		sid, routed = three.routeOf(id)
		require.True(t, routed)
		require.Equal(t, SegmentID("h-oldest"), sid,
			"once the carrier leaves, the id is residual and must re-resolve to the flattened holder that remains")
		requireAgreesWithAFlatRebuild(t, three, []ExternalID{id, "keep", "keep-2", "keep-3"})
	})
}

// SPDX-License-Identifier: Apache-2.0

// manager_bucket_backlog_retire_test.go — what the drain is allowed to DESTROY.
//
// These pin ONE obligation: a drain must never Unload a segment holding live
// members it did not rebuild. drainFormat hands snap.tails to replaceBucketGroups
// as the EXCLUDE set, so a tail is never offered as a constituent and its spans are
// never walked by the constituency closure; a member of that tail therefore survives
// the rebuild only if the drain carried it in docs. Retiring the tail regardless
// destroys every other member it held.
//
// THE BACKLOG STATE IS BUILT DIRECTLY (recordDirty), not driven through
// AddAndMarkDirty, and that is deliberate. The write path keeps pending and tails in
// lockstep only by a NON-LOCAL coincidence: AddSealAndSupersede appends to the
// engine's active buffer and drains it under one lock hold, so the batch it seals is
// exactly the batch it was handed. Nothing enforces that from here, and two
// documented behaviors already break the lockstep on purpose — withoutTombstoned
// drops pending entries while carrying tails through unfiltered, and purgeDirty drops
// pending documents while deliberately leaving the tails alone. So these tests pin
// the DRAIN'S OWN obligation rather than that coincidence: whatever put a live member
// in a tail the drain did not rebuild, the drain must not take it with the tail.
//
// Expectations are FIXTURE CONSTANTS throughout — never a number read back from the
// engine under test.

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestDrainKeepsTailMembersOutsideItsDocs is the SINGLE-TAIL catcher: one tail holds
// two live members and the drain is told about only one of them.
//
// The unrecorded member is in no partition the rebuild touches — the tail is excluded
// from constituency, so nothing else holds it — and the drain unloads the tail
// anyway. Its member is then gone from the resident set while the drain reports
// success.
func TestDrainKeepsTailMembersOutsideItsDocs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	const (
		gt   = kgtypes.GraphCode
		name = "retireSingleTail"
	)
	dm := mgr.managerFor(gt, name)

	docs := hnswVecDocs(4)
	a, b := docs[0], docs[1]

	// Both members land in ONE tail; only `a` is recorded against it.
	sealed, err := dm.engine.AddSealAndSupersede([]searchengine.Document{a, b})
	require.NoError(t, err)
	require.True(t, sealed.Created, "the seal must CREATE the tail this fixture retires")
	tail := []searchengine.SegmentID{sealed.ID}
	mgr.recordDirty(gt, name, false, []searchengine.Document{a}, tail, mgr.nextWriteSeq())

	// KNOWN-POSITIVE CONTROL: without this the catcher below would pass just as well
	// against a fixture whose member was never searchable to begin with.
	_, ok := dm.engine.VectorByID(b.ID)
	require.True(t, ok, "precondition: the unrecorded member must be searchable BEFORE the drain")

	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	_, haveA := dm.engine.VectorByID(a.ID)
	_, haveB := dm.engine.VectorByID(b.ID)
	require.True(t, haveA, "the drain lost the member it rebuilt")
	require.True(t, haveB,
		"the drain destroyed a live member it never rebuilt: it excluded the tail from constituency "+
			"and then unloaded it (a=%v b=%v distinct=%d segments=%d)",
		haveA, haveB, dm.engine.DistinctResidentDocCount(), len(dm.engine.Export()))
}

// TestDrainRetainsOnlyUncoveredTails is the MULTI-TAIL catcher, and it is not
// redundant with the single-tail one: that case cannot tell "retire nothing" apart
// from "retire exactly the safe subset", so an implementation that simply stopped
// unloading would satisfy it while letting the resident segment set grow without
// bound.
//
// Four tails of three documents each. The EVEN-indexed ones have only their first
// document recorded, so two members of each are uncovered; the ODD-indexed ones are
// fully recorded and are therefore genuinely spent. The captured tail ids make the
// per-tail outcome observable.
func TestDrainRetainsOnlyUncoveredTails(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	const (
		gt        = kgtypes.GraphCode
		name      = "retireMultiTail"
		tailCount = 4
		perTail   = 3
		corpus    = tailCount * perTail // FIXTURE CONSTANT — 12 distinct members.
	)
	dm := mgr.managerFor(gt, name)

	docs := hnswVecDocs(corpus)
	tailIDs := make([]searchengine.SegmentID, 0, tailCount)
	for i := range tailCount {
		batch := docs[i*perTail : i*perTail+perTail]
		sealed, err := dm.engine.AddSealAndSupersede(batch)
		require.NoError(t, err)
		require.True(t, sealed.Created, "tail %d: the seal must CREATE its own segment", i)
		tailIDs = append(tailIDs, sealed.ID)

		recorded := batch
		if i%2 == 0 {
			recorded = batch[:1] // uncovered: two of its three members are never rebuilt.
		}
		mgr.recordDirty(gt, name, false, recorded, []searchengine.SegmentID{sealed.ID}, mgr.nextWriteSeq())
	}

	require.Equal(t, corpus, dm.engine.DistinctResidentDocCount(),
		"precondition: every fixture member must be resident before the drain")

	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	present := presentMemberIDs(dm, docs)
	lost := make([]string, 0, corpus)
	for _, d := range docs {
		if !present[d.ID] {
			lost = append(lost, d.ID)
		}
	}
	require.Empty(t, lost,
		"the drain destroyed live members held by tails it did not rebuild (distinct=%d segments=%d)",
		dm.engine.DistinctResidentDocCount(), len(dm.engine.Export()))

	// THE RETIRE SIDE, and it is the only assertion in this file that tells "retire
	// exactly the safe subset" apart from "retire nothing". Every survival assertion
	// above is satisfied by a drain that unloads nothing at all, which would leave
	// every window's tails resident forever.
	//
	// It is asserted against the CAPTURED IDS, never a segment count: a count is
	// satisfied by any arrangement summing to the number and cannot tell "the two
	// right tails retired" from "two others did". The ids are unambiguous because the
	// rebuild carries 8 documents and a segment id is the hash of its bytes, so the
	// freshly published segment cannot alias a 3-document tail.
	resident := map[searchengine.SegmentID]bool{}
	for _, blob := range dm.engine.Export() {
		resident[blob.ID] = true
	}
	for i, id := range tailIDs {
		if i%2 == 0 {
			require.True(t, resident[id],
				"tail %d holds live members the drain never rebuilt and must be KEPT resident", i)
			continue
		}
		require.False(t, resident[id],
			"tail %d was fully rebuilt and is spent — leaving it resident grows the segment set without bound", i)
	}
}

// TestDrainRebuildingNothingKeepsLiveTailMembers is the WHOLE-CORPUS catcher.
//
// Tombstoning the only recorded document filters pending down to nothing while the
// tail is carried through unfiltered, so the drain reaches the rebuild with no
// documents and no superseded ids — which publishes nothing at all. The retire then
// takes its short-circuit and names EVERY tail, so a drain that rebuilt nothing
// unloads the entire window and empties the graph.
func TestDrainRebuildingNothingKeepsLiveTailMembers(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	const (
		gt   = kgtypes.GraphCode
		name = "retireNothingRebuilt"
	)
	dm := mgr.managerFor(gt, name)

	docs := hnswVecDocs(2)
	dead, live := docs[0], docs[1]

	sealed, err := dm.engine.AddSealAndSupersede([]searchengine.Document{dead, live})
	require.NoError(t, err)
	require.True(t, sealed.Created, "the seal must CREATE the tail this fixture carries")
	mgr.recordDirty(gt, name, false, []searchengine.Document{dead},
		[]searchengine.SegmentID{sealed.ID}, mgr.nextWriteSeq())
	mgr.SetGraphTombstones(gt, name, []searchengine.ExternalID{dead.ID})

	_, ok := dm.engine.VectorByID(live.ID)
	require.True(t, ok, "precondition: the live member must be searchable BEFORE the drain")

	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	_, haveLive := dm.engine.VectorByID(live.ID)
	require.True(t, haveLive,
		"a drain that rebuilt NOTHING unloaded the whole window and emptied the graph "+
			"(live-member-present=%v distinct=%d segments=%d)",
		haveLive, dm.engine.DistinctResidentDocCount(), len(dm.engine.Export()))
}

// TestRetainedTailIsAbsorbedByTheNextDrain is the LEAK catcher: retention is a
// deferral, not a permanent resident segment.
//
// A retained tail leaves the backlog anyway — the consume takes the snapshot's tails
// by multiset regardless of what the retire decided — so it is no longer in the next
// drain's exclude set. The following drain therefore resolves it as an ordinary
// constituent and absorbs it. Without this, "keep instead of unload" would be
// indistinguishable from "never retire anything".
func TestRetainedTailIsAbsorbedByTheNextDrain(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	const (
		gt   = kgtypes.GraphCode
		name = "retainedTailAbsorbed"
	)
	dm := mgr.managerFor(gt, name)

	docs := hnswVecDocs(4)
	a, b, c := docs[0], docs[1], docs[2]

	sealed, err := dm.engine.AddSealAndSupersede([]searchengine.Document{a, b})
	require.NoError(t, err)
	require.True(t, sealed.Created, "the seal must CREATE the tail this fixture retains")
	retainedTail := sealed.ID
	mgr.recordDirty(gt, name, false, []searchengine.Document{a},
		[]searchengine.SegmentID{sealed.ID}, mgr.nextWriteSeq())

	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	afterFirst := map[searchengine.SegmentID]bool{}
	for _, blob := range dm.engine.Export() {
		afterFirst[blob.ID] = true
	}
	require.True(t, afterFirst[retainedTail],
		"precondition: the uncovered tail must have been KEPT by the first drain")

	// The previously-uncovered member now arrives as an ordinary write, so the second
	// drain rebuilds its partition and pulls the retained tail in as a constituent.
	//
	// A THIRD DOCUMENT RIDES ALONG, and it is what makes the absorption observable.
	// A segment id is the hash of its bytes, so rebuilding this partition from {a,b}
	// alone reproduces the tail's own id: the tail would then still be resident AS the
	// published segment, and the assertion below could not tell that apart from a tail
	// that was never absorbed. Rebuilding to {a,b,c} gives the published segment a
	// different membership and therefore a different id.
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, []searchengine.Document{b, c}))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	_, haveA := dm.engine.VectorByID(a.ID)
	_, haveB := dm.engine.VectorByID(b.ID)
	_, haveC := dm.engine.VectorByID(c.ID)
	require.True(t, haveA, "the absorbing drain lost a member")
	require.True(t, haveB, "the absorbing drain lost a member")
	require.True(t, haveC, "the absorbing drain lost a member")

	afterSecond := map[searchengine.SegmentID]bool{}
	for _, blob := range dm.engine.Export() {
		afterSecond[blob.ID] = true
	}
	require.False(t, afterSecond[retainedTail],
		"the retained tail is still resident after the drain that rebuilt its members — "+
			"retention has become a permanent segment leak rather than a deferral")
}

// TestDrainConsumesEntriesItFilteredOutOfTheBuild is the BACKLOG-LEAK catcher.
//
// The drain derives its work flags from the FILTERED snapshots, which correctly
// decide whether there is anything to BUILD. But a window whose pending filters down
// to nothing and whose tails are empty then has no work at all, so the tick returns
// before the consume — and those entries stay queued forever, their bytes still
// charging the backlog's byte cap and re-triggering a drain on every tick.
//
// The state is built directly because the write path cannot reach it today; it
// becomes reachable the moment the backlog stops recording a segment the window
// merely aliased rather than created.
func TestDrainConsumesEntriesItFilteredOutOfTheBuild(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	const (
		gt   = kgtypes.GraphCode
		name = "backlogFilteredToNothing"
	)

	docs := hnswVecDocs(3)
	ids := make([]searchengine.ExternalID, 0, len(docs))
	for _, d := range docs {
		ids = append(ids, d.ID)
	}
	// Documents with NO tail: the shape a window that created no segment leaves.
	mgr.recordDirty(gt, name, false, docs, nil, mgr.nextWriteSeq())
	mgr.SetGraphTombstones(gt, name, ids)

	hnswBefore, _ := mgr.snapshotDirty(gt, name)
	require.Len(t, hnswBefore.pending, len(docs),
		"precondition: the entries must be queued before the drain, or this test proves nothing")

	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	hnswAfter, bm25After := mgr.snapshotDirty(gt, name)
	require.Empty(t, hnswAfter.pending,
		"the drain filtered every pending document out of the build and then never consumed them — "+
			"they stay queued forever, still charging the backlog byte cap")
	require.Empty(t, bm25After.pending)
}

// TestRetiringRetiresOnlyCoveredUnpublishedTails exercises the retire decision
// directly, at the level where the two-slice contract lives.
//
// The FOURTH row is the one that cannot be dropped: an empty published slice used to
// short-circuit and name every tail for retirement, which is how a drain that rebuilt
// nothing emptied a whole graph. Pinning it here means the short-circuit cannot be
// reintroduced without a unit failure.
func TestRetiringRetiresOnlyCoveredUnpublishedTails(t *testing.T) {
	t.Parallel()

	const (
		tailA = searchengine.SegmentID("seg-a")
		tailB = searchengine.SegmentID("seg-b")
	)

	t.Run("a republished tail is never retired", func(t *testing.T) {
		t.Parallel()
		retire, retained := retiring(
			[]searchengine.SegmentID{tailA},
			[]searchengine.SegmentID{tailA},
			nil)
		require.Empty(t, retire, "unloading a republished id removes the segment the rebuild just published")
		require.Equal(t, []searchengine.SegmentID{tailA}, retained)
	})

	t.Run("a tail with an uncovered live member is retained", func(t *testing.T) {
		t.Parallel()
		retire, retained := retiring(
			[]searchengine.SegmentID{tailA},
			[]searchengine.SegmentID{tailB},
			map[searchengine.SegmentID]int{tailA: 2})
		require.Empty(t, retire)
		require.Equal(t, []searchengine.SegmentID{tailA}, retained)
	})

	t.Run("an unpublished tail with no uncovered member is retired", func(t *testing.T) {
		t.Parallel()
		retire, retained := retiring(
			[]searchengine.SegmentID{tailA},
			[]searchengine.SegmentID{tailB},
			nil)
		require.Equal(t, []searchengine.SegmentID{tailA}, retire)
		require.Empty(t, retained)
	})

	t.Run("nothing published still retains an uncovered tail", func(t *testing.T) {
		t.Parallel()
		retire, retained := retiring(
			[]searchengine.SegmentID{tailA},
			nil,
			map[searchengine.SegmentID]int{tailA: 1})
		require.Empty(t, retire,
			"a drain that published nothing must not name every tail for retirement — that empties the graph")
		require.Equal(t, []searchengine.SegmentID{tailA}, retained)
	})
}

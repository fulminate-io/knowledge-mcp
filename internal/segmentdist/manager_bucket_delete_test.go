// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// deleteFixtureN is small enough to keep the whole corpus in one partition, so the
// delete's effect is read without partition shape entering the picture.
const deleteFixtureN = 200

// bothFormatDocs builds documents carrying BOTH a vector and BM25 fields, so one
// corpus feeds both engines and a delete can be asserted independently on each.
func bothFormatDocs(n int, prefix string) []searchengine.Document {
	vecs := prefixIDs(hnswVecDocs(n), prefix)
	fields := bm25FieldDocs(n)
	for i := range vecs {
		vecs[i].Fields = fields[i].Fields
	}
	return vecs
}

// TestDeletedNodeLeavesTheSearchableCorpus is the REPRODUCTION for the delete
// propagation defect: a delete must survive in the SHIPPED BLOBS, not only as a
// cleared bit in the writer's memory.
//
// THE FRESH ENGINE IS THE POINT. Asserting against the writer's own engine proves
// nothing — the kill cleared the bit there, so the node is gone from that view
// whether or not anything was re-emitted. Loading a SECOND manager from the same
// L2 segments is what reads what actually got written: against an unfixed tree the
// blob still carries the document and the fresh engine returns it.
//
// "Searchable" here means ENGINE-LEVEL. The read path above this hides a removed
// node from the user regardless; what a stale copy costs is a top-k slot and blob
// size, not a wrong row.
func TestDeletedNodeLeavesTheSearchableCorpus(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const name = "deleteRepro"
	gt := kgtypes.GraphCode

	dir := t.TempDir()

	mgr := closeOnCleanup(t, NewManager(dir, 0))

	docs := bothFormatDocs(deleteFixtureN, "del-")
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, docs))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	victim := docs[0]
	require.True(t, residentInFreshEngine(t, ctx, dir, gt, name, victim),
		"PRECONDITION: the node must be in the shipped corpus before the delete, or this test proves nothing")

	require.NoError(t, mgr.DeleteFromBuckets(ctx, gt, name, []searchengine.ExternalID{victim.ID}))

	require.False(t, residentInFreshEngine(t, ctx, dir, gt, name, victim),
		"a deleted node must be absent from a FRESH engine loaded from the re-emitted segments — the delete has to reach the blob, not just the live bit")
}

// residentInFreshEngine loads a SECOND manager over the same segment source and
// reports whether the vector engine still serves the document — reading what was
// written rather than what the writer remembers.
//
// IT IS A LIVE-CORPUS PROBE, NOT A PHYSICAL-RESIDENCE ONE. VectorByID consults
// liveDocs, so this returns false for a document that is still physically in the
// blob but masked dead — which is exactly the state a delete whose PARTITION
// rewrite failed while its TOMBSTONE record landed leaves behind. For the ordinary
// delete paths below the two readings coincide, because re-emitting the bucket
// removes the document outright; where they can diverge, assert the counts through
// freshEngineCounts instead and say which one the property is about.
func residentInFreshEngine(
	t *testing.T, ctx context.Context, dir string,
	gt kgtypes.GraphType, name string, doc searchengine.Document,
) bool {
	t.Helper()
	fresh := closeOnCleanup(t, NewManager(dir, 0))
	_, ok, err := fresh.VectorByID(ctx, gt, name, doc.ID)
	require.NoError(t, err)
	return ok
}

// freshEngineCounts loads a SECOND manager over the same segment source and returns
// its (physical, live) resident document counts: what the blobs on disk actually
// contain, and what survives the tombstone seed applied at import.
//
// The pair exists so a test can say WHICH residence it means. A property about
// whether a rebuilt partition reached disk is about the physical count; a property
// about what a reader can see is about the live one. Reading either through the
// other is how a failed write gets mistaken for a successful delete.
func freshEngineCounts(
	t *testing.T, ctx context.Context, dir string,
	gt kgtypes.GraphType, name string,
) (physical, live int) {
	t.Helper()
	fresh := closeOnCleanup(t, NewManager(dir, 0))
	dm := fresh.managerFor(gt, name)
	require.NoError(t, dm.load(ctx))
	return dm.engine.ResidentDocCount(), dm.engine.LiveResidentCount()
}

// TestDeleteCoversBothFormats is THE CATCHER for the two-manifest requirement.
//
// The vector corpus and the field corpus are separate engines with separate
// manifests, so a delete that re-emits only one leaves the node indexed in the
// other — where it goes on consuming BM25 rank slots. A vector-only assertion stays
// green through exactly that mistake, so each format is asserted independently.
func TestDeleteCoversBothFormats(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const name = "deleteBothFormats"
	gt := kgtypes.GraphCode

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	docs := bothFormatDocs(deleteFixtureN, "delboth-")
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, docs))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	hnswDM := mgr.managerFor(gt, name)
	bm25DM := mgr.bm25ManagerFor(gt, name)
	victim := docs[0]

	require.Equal(t, deleteFixtureN, hnswDM.engine.ResidentDocCount(), "PRECONDITION: the vector corpus indexes the whole fixture")
	require.Equal(t, deleteFixtureN, bm25DM.engine.ResidentDocCount(), "PRECONDITION: the field corpus indexes the whole fixture")

	require.NoError(t, mgr.DeleteFromBuckets(ctx, gt, name, []searchengine.ExternalID{victim.ID}))

	// EACH FORMAT IS ASSERTED SEPARATELY. Omitting the field leg leaves the field
	// corpus at its full count while the vector corpus drops, so a vector-only
	// assertion would stay green through exactly the mistake this test exists for.
	//
	// AND EACH IS ASSERTED THROUGH THE COUNT ITS OWN LEG MOVES. The vector leg kills the
	// live bit and defers the partition re-emit, so what drops there is the LIVE count
	// while the member count holds until a drain serves that partition; the field leg
	// re-emits inline, so its member count drops. Asserting the vector pool's member
	// count would be asserting the inline re-emit this ticket removed, and asserting the
	// field pool's live count would let a field leg that never ran pass on the mask.
	require.Equal(t, deleteFixtureN-1, hnswDM.engine.LiveResidentCount(),
		"one delete must remove the node from the searchable VECTOR corpus — exactly the one id, not its neighbors")
	require.Equal(t, deleteFixtureN, hnswDM.engine.ResidentDocCount(),
		"and it must do so WITHOUT re-emitting the vector partition: the document is still a resident "+
			"member, dead, until the deferred re-emit lands")
	require.Equal(t, deleteFixtureN-1, bm25DM.engine.ResidentDocCount(),
		"one delete must ALSO remove it from the FIELD corpus — the two carry separate manifests, so re-emitting only the vector leg leaves it holding BM25 rank slots")

	// And the id that went is genuinely the victim's.
	_, stillThere, err := mgr.VectorByID(ctx, gt, name, victim.ID)
	require.NoError(t, err)
	require.False(t, stillThere, "the deleted id must no longer resolve to a vector")
}

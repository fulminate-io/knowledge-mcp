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
	_, gc := newSegmentHarness(t)
	mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, dir, 0, withSegmentSource(gc)))

	docs := bothFormatDocs(deleteFixtureN, "del-")
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, docs))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	victim := docs[0]
	require.True(t, residentInFreshEngine(t, ctx, gc, dir, gt, name, victim),
		"PRECONDITION: the node must be in the shipped corpus before the delete, or this test proves nothing")

	require.NoError(t, mgr.DeleteFromBuckets(ctx, gt, name, []searchengine.ExternalID{victim.ID}))

	require.False(t, residentInFreshEngine(t, ctx, gc, dir, gt, name, victim),
		"a deleted node must be absent from a FRESH engine loaded from the re-emitted segments — the delete has to reach the blob, not just the live bit")
}

// residentInFreshEngine loads a SECOND manager over the same segment source and
// reports whether the vector engine still resolves the document — reading what was
// written rather than what the writer remembers.
func residentInFreshEngine(
	t *testing.T, ctx context.Context, src segmentSource, dir string,
	gt kgtypes.GraphType, name string, doc searchengine.Document,
) bool {
	t.Helper()
	fresh := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, dir, 0, withSegmentSource(src)))
	_, ok, err := fresh.VectorByID(ctx, gt, name, doc.ID)
	require.NoError(t, err)
	return ok
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

	_, gc := newSegmentHarness(t)
	mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))

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
	require.Equal(t, deleteFixtureN-1, hnswDM.engine.ResidentDocCount(),
		"one delete must remove the node from the VECTOR corpus — exactly the one id, not its neighbors")
	require.Equal(t, deleteFixtureN-1, bm25DM.engine.ResidentDocCount(),
		"one delete must ALSO remove it from the FIELD corpus — the two carry separate manifests, so re-emitting only the vector leg leaves it holding BM25 rank slots")

	// And the id that went is genuinely the victim's.
	_, stillThere, err := mgr.VectorByID(ctx, gt, name, victim.ID)
	require.NoError(t, err)
	require.False(t, stillThere, "the deleted id must no longer resolve to a vector")
}

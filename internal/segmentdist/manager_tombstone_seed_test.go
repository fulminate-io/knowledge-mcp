// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestPreDeleteBlobDoesNotResurrectDeletedNode is THE CATCHER for tombstone
// seeding at the load paths, and it is the only test in this phase that can see
// the gap.
//
// WHY THE SIBLING STEP'S TESTS CANNOT COVER THIS. Those delete through the LIVE
// engine, where the delete clears the live bit in memory, so they stay green
// whether or not an import seeds anything. The window this closes is a DIFFERENT
// process — or the same one after a restart — importing a blob that was shipped
// BEFORE the delete. Nothing is wrong with that blob: it is a faithful record of
// the corpus at the moment it was written, and it still contains the node. An
// import that starts every member live therefore brings the node back.
//
// The fixture makes that ordering explicit: ship the corpus FIRST, learn the
// delete SECOND, and only then let a cold engine import. Against an unfixed load
// path the post-import search returns the deleted id.
func TestPreDeleteBlobDoesNotResurrectDeletedNode(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const name = "resurrectRepro"
	const corpusN = 200
	gt := kgtypes.GraphCode

	dir := t.TempDir()

	producer := closeOnCleanup(t, NewManager(dir, 0))

	docs := prefixIDs(hnswVecDocs(corpusN), "resurrect-")
	require.NoError(t, producer.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, producer.ReEmitDirtyBuckets(ctx, gt, name))

	// The blob now on the server and in L2 predates the delete and CONTAINS the
	// victim. That is the whole precondition: nothing rewrites it below.
	victim := docs[0]

	// A cold consumer that has learned the delete. Its load must not bring the
	// victim back, even though the only blob it can import still holds it.
	consumer := closeOnCleanup(t, NewManager(dir, 0))
	consumer.SetGraphTombstones(gt, name, []searchengine.ExternalID{victim.ID})

	dm := consumer.managerFor(gt, name)
	require.NoError(t, dm.load(ctx))
	require.Positive(t, dm.engine.ResidentDocCount(),
		"PRECONDITION: the consumer must actually have imported the pre-delete blob, or this proves nothing")

	// SEARCH IS THE OBSERVABLE HERE, deliberately. The seed clears LIVE BITS; it does
	// not rewrite the blob, which still physically contains the document. Search
	// applies the liveDocs filter and is therefore what a seeded import changes.
	// (VectorByID consults liveDocs too — see
	// TestSeededDeadDocumentDoesNotResolveAVector below, which is where that leg is
	// asserted. This test keeps SEARCH as its single observable so a failure here
	// names the seeding rather than the by-id read.)
	require.False(t, searchReturnsID(dm, victim),
		"a node deleted BEFORE this import must not come back: the imported blob predates the delete, so its members have to be seeded dead")

	// A neighbor that was never deleted is untouched — seeding kills exactly the
	// tombstoned ids and nothing else.
	survivor := docs[1]
	require.True(t, searchReturnsID(dm, survivor),
		"a node that was never deleted must survive the seeded import")
}

// TestSeededDeadDocumentDoesNotResolveAVector is the by-id half of the seeding
// contract, and the one state in which the two halves can disagree.
//
// WHY THE ORDINARY DELETE TESTS CANNOT SEE THIS. DeleteFromBuckets re-emits the
// bucket, so the document leaves the blob outright and every by-id read misses
// because the id is no longer ROUTED. This fixture is the other shape: the blob
// predates the delete and is never rewritten, so the document stays physically
// resident and is masked only by a cleared live bit. That is the sole state where
// "routed" and "live" disagree, and therefore the only one that can tell a
// liveness-aware by-id read from a raw payload read.
//
// WHY IT MATTERS ABOVE THIS PACKAGE: Manager.VectorByID is the query-vector source
// for search mode:"similar" (tools/search_similar_node.go), and nothing on that
// path checks that the named node still exists. Resolving a seeded-dead vector
// there anchors a whole neighbor search on a node the corpus no longer has, and
// reports it as an ordinary success.
//
// KNOWN POSITIVE, same run: an untouched neighbor still resolves byte-equal. A
// read that declined everything, or a manager that failed to load at all, would
// otherwise satisfy the ok=false assertion for the wrong reason.
func TestSeededDeadDocumentDoesNotResolveAVector(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const name = "seededDeadVector"
	const corpusN = 200
	// GraphKnowledge deliberately: mode:"similar" resolves against knowledge, so the
	// fixture stands on the graph the reachable caller actually reads.
	gt := kgtypes.GraphKnowledge

	dir := t.TempDir()

	producer := closeOnCleanup(t, NewManager(dir, 0))
	docs := prefixIDs(hnswVecDocs(corpusN), "seededdead-")
	require.NoError(t, producer.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, producer.ReEmitDirtyBuckets(ctx, gt, name))

	victim := docs[0]
	survivor := docs[1]

	// A cold consumer that has learned the delete. The only blob it can import
	// predates that delete and still holds the victim.
	consumer := closeOnCleanup(t, NewManager(dir, 0))
	consumer.SetGraphTombstones(gt, name, []searchengine.ExternalID{victim.ID})

	dm := consumer.managerFor(gt, name)
	require.NoError(t, dm.load(ctx))

	// PRECONDITION, AND THE LEG THAT KEEPS THE ASSERTION FROM PASSING VACUOUSLY. The
	// two counts must DISAGREE BY EXACTLY ONE: ResidentDocCount counts what is
	// physically in the segments (the victim included — seeding clears a live bit and
	// never rewrites the blob), LiveResidentCount counts what is searchable (the
	// victim excluded). Equal counts would mean nothing was masked; a physical count
	// short of the corpus would mean the victim was never imported, and then ok=false
	// below would be true for a reason that has nothing to do with liveness.
	require.Equal(t, corpusN, dm.engine.ResidentDocCount(),
		"PRECONDITION: the whole pre-delete corpus must be PHYSICALLY resident, victim included — the seed masks, it does not rewrite the blob")
	require.Equal(t, corpusN-1, dm.engine.LiveResidentCount(),
		"PRECONDITION: exactly one id must be masked dead by the seed")

	_, ok, err := consumer.VectorByID(ctx, gt, name, victim.ID)
	require.NoError(t, err, "a seeded-dead id is not a load error")
	require.False(t, ok,
		"a seeded-dead document must not resolve a vector: it is physically resident but not in the live corpus, and mode:\"similar\" would otherwise seed a whole neighbor search from it")

	// KNOWN POSITIVE: a neighbor that was never deleted still resolves, byte-equal.
	got, ok, err := consumer.VectorByID(ctx, gt, name, survivor.ID)
	require.NoError(t, err)
	require.True(t, ok, "a node that was never deleted must still resolve — the consult declines exactly the tombstoned ids")
	require.True(t, bytes.Equal(got, survivor.Vector), "the survivor's resolved vector is byte-equal to the shipped one")
}

// seedProbeK is the top-k every seeding probe below queries at. A document's own
// vector is its nearest neighbor, so any k at all would resolve it on a healthy
// engine; the width is here purely so a seeded-dead document cannot be missed by
// falling just outside a narrow window.
const seedProbeK = 64

// searchReturnsID reports whether the engine returns the document for its own
// vector — the read-side observable, distinct from the by-id resolve above.
func searchReturnsID(dm *distManager[[]byte, struct{}], doc searchengine.Document) bool {
	for _, h := range dm.engine.Search(doc.Vector, seedProbeK) {
		if h.ID == doc.ID {
			return true
		}
	}
	return false
}

// TestTombstoneSeedIsReadPerImport pins that the seed is a SUPPLIER rather than a
// snapshot taken when the engine was built.
//
// Engines are constructed lazily on first use, which is routinely BEFORE the
// process has learned anything about deletes. An engine that captured the set at
// construction would hold an empty one forever and seed nothing, and every
// assertion above would still pass because that test sets the tombstones before it
// touches the engine. This one sets them AFTER.
func TestTombstoneSeedIsReadPerImport(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const name = "seedAfterConstruction"
	const corpusN = 200
	gt := kgtypes.GraphCode

	dir := t.TempDir()

	producer := closeOnCleanup(t, NewManager(dir, 0))

	docs := prefixIDs(hnswVecDocs(corpusN), "seedafter-")
	require.NoError(t, producer.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, producer.ReEmitDirtyBuckets(ctx, gt, name))

	consumer := closeOnCleanup(t, NewManager(dir, 0))
	// Force the engine into existence BEFORE the delete is known.
	dm := consumer.managerFor(gt, name)

	victim := docs[0]
	consumer.SetGraphTombstones(gt, name, []searchengine.ExternalID{victim.ID})

	require.NoError(t, dm.load(ctx))
	require.False(t, searchReturnsID(dm, victim),
		"a tombstone learned AFTER the engine was constructed must still seed its imports — the seed is read per import, not captured at construction")
}

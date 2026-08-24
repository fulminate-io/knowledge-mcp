// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
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
	_, gc := newSegmentHarness(t)
	producer := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, dir, 0, withSegmentSource(gc)))

	docs := prefixIDs(hnswVecDocs(corpusN), "resurrect-")
	require.NoError(t, producer.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, producer.ReEmitDirtyBuckets(ctx, gt, name))

	// The blob now on the server and in L2 predates the delete and CONTAINS the
	// victim. That is the whole precondition: nothing rewrites it below.
	victim := docs[0]

	// A cold consumer that has learned the delete. Its load must not bring the
	// victim back, even though the only blob it can import still holds it.
	consumer := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, dir, 0, withSegmentSource(gc)))
	consumer.SetGraphTombstones(gt, name, []searchengine.ExternalID{victim.ID})

	dm := consumer.managerFor(gt, name)
	require.NoError(t, dm.load(ctx))
	require.Positive(t, dm.engine.ResidentDocCount(),
		"PRECONDITION: the consumer must actually have imported the pre-delete blob, or this proves nothing")

	// SEARCH IS THE OBSERVABLE, deliberately. The seed clears LIVE BITS; it does not
	// rewrite the blob, which still physically contains the document. Search applies
	// the liveDocs filter and is therefore what a seeded import changes. VectorByID
	// is NOT a liveness probe — it routes by id and reads the payload directly, so it
	// resolves a seeded-dead document and would make this test fail for a reason that
	// has nothing to do with the seeding.
	require.False(t, searchReturnsID(dm, victim),
		"a node deleted BEFORE this import must not come back: the imported blob predates the delete, so its members have to be seeded dead")

	// A neighbor that was never deleted is untouched — seeding kills exactly the
	// tombstoned ids and nothing else.
	survivor := docs[1]
	require.True(t, searchReturnsID(dm, survivor),
		"a node that was never deleted must survive the seeded import")
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
	_, gc := newSegmentHarness(t)
	producer := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, dir, 0, withSegmentSource(gc)))

	docs := prefixIDs(hnswVecDocs(corpusN), "seedafter-")
	require.NoError(t, producer.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, producer.ReEmitDirtyBuckets(ctx, gt, name))

	consumer := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, dir, 0, withSegmentSource(gc)))
	// Force the engine into existence BEFORE the delete is known.
	dm := consumer.managerFor(gt, name)

	victim := docs[0]
	consumer.SetGraphTombstones(gt, name, []searchengine.ExternalID{victim.ID})

	require.NoError(t, dm.load(ctx))
	require.False(t, searchReturnsID(dm, victim),
		"a tombstone learned AFTER the engine was constructed must still seed its imports — the seed is read per import, not captured at construction")
}

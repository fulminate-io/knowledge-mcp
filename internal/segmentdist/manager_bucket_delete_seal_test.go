// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// manager_bucket_delete_seal_test.go is the gate on the DELETE closing its own import
// window.
//
// THE WINDOW, stated as the delete path's own doc used to state it: a blob shipped
// BEFORE the delete and re-imported afterwards starts every member LIVE, so the removed
// document comes back into the searchable set. DeleteFromBuckets now seeds the deleted
// ids into the graph's tombstone record — the durable one this package already owns
// (rebuild_state.go) — so any blob imported afterwards has them masked.
//
// THE RESTART IS THE INTERESTING DIRECTION and is why these tests build a SECOND
// Manager over the same cache directory. An in-process seal is provable against the
// same Manager's own map; only a fresh process proves the record was persisted and is
// read back, which is what a COLD LOAD needs.

// sealFixture drives one ORDINARY delete and returns the cache directory plus the
// victim and a control document that shares its blob.
//
// THE STATE THIS SEAL IS THE ONLY ANSWER TO is a stored corpus in which the PRE-DELETE
// blob is the newest thing on disk and NOTHING on disk supersedes it. That used to
// require failing every L2 write: the delete re-emitted the vector partition inline, and
// its post-delete blob carried a supersession record naming the constituent, so an import
// declined the old blob on the record's strength and the tombstone set was never
// consulted. The delete no longer re-emits that partition at all, so an ordinary,
// entirely successful delete now produces the state directly — which is also what makes
// this fixture worth reading twice: the window the seal covers is no longer an injected
// failure mode but the NORMAL post-delete state of every graph until a drain serves it.
func sealFixture(t *testing.T, name string) (
	cacheDir string, gt kgtypes.GraphType, victim, control searchengine.Document,
) {
	t.Helper()

	ctx := context.Background()
	mgr, gt, nm, hdm, _, docs := deleteRetryFixtureOfSize(t, name, deleteFixtureN)

	preDelete := servingIDs(hdm)
	require.Len(t, preDelete, 1,
		"FIXTURE PRECONDITION: the corpus is one partition, so there is exactly one pre-delete blob")

	require.NoError(t, mgr.DeleteFromBuckets(ctx, gt, nm, []searchengine.ExternalID{docs[0].ID}))
	require.Equal(t, preDelete, l2HNSWIDs(mgr.cacheDir, nm),
		"FIXTURE PRECONDITION: L2 still holds exactly the PRE-DELETE blob and nothing that supersedes "+
			"it — with a post-delete blob on disk the import would decline this one on its supersession "+
			"record and the tombstone seal would never be reached")

	return mgr.cacheDir, gt, docs[0], docs[1]
}

// searchHitIDs runs one vector search and returns the ids it ranked.
//
// THE SEARCHABLE SET IS THE RIGHT INSTRUMENT HERE, and Manager.VectorByID is NOT: that
// method routes an id to its segment and reads the stored vector off the payload
// WITHOUT consulting liveDocs (searchengine/engine.go), so a tombstone seed cannot
// change its answer. What a tombstone seed governs is exactly what a delete's own doc
// says a stale copy costs — the dead document competing for a top-k slot.
func searchHitIDs(
	t *testing.T, mgr *Manager, gt kgtypes.GraphType, name string, vec []byte, k int,
) map[searchengine.ExternalID]struct{} {
	t.Helper()
	hits, err := mgr.Search(context.Background(), gt, name, "", vec, k)
	require.NoError(t, err)
	out := make(map[searchengine.ExternalID]struct{}, len(hits))
	for _, h := range hits {
		out[h.ID] = struct{}{}
	}
	return out
}

// TestDeleteSealsTheImportWindowAcrossARestart is the seam's headline property: a fresh
// process's cold load of a pre-delete blob does NOT put the deleted document back into
// the searchable set.
func TestDeleteSealsTheImportWindowAcrossARestart(t *testing.T) {
	t.Parallel()

	const name = "sealrestart"
	cacheDir, gt, victim, control := sealFixture(t, name)

	// A RESTART: a fresh Manager over the same L2 cache, whose in-memory tombstone
	// set starts empty and whose engine has loaded nothing.
	p2 := closeOnCleanup(t, NewManager(cacheDir, 0))

	require.NotContains(t, searchHitIDs(t, p2, gt, name, victim.Vector, 5), victim.ID,
		"a cold load of the stranded pre-delete constituent must NOT return the deleted id — the "+
			"delete's own persisted tombstone record is what masks it at Import")

	// KNOWN POSITIVE, in the same run and through the same instrument: a document this
	// delete did NOT name IS returned by the same engine. Without it the assertion
	// above is equally satisfied by a load that imported nothing, by a search that
	// ranks nothing, and by an empty cache directory.
	require.Contains(t, searchHitIDs(t, p2, gt, name, control.Vector, 5), control.ID,
		"CONTROL: a document the delete did not name is still searchable after the same cold load")
}

// TestTheSealIsTheRecordAndNotTheBlob is the control that says WHAT masks the id.
//
// IT REMOVES THE PERSISTED RECORD AND NOTHING ELSE, then repeats the cold load. The
// deleted document comes back — which proves two things one assertion cannot: the
// stranded blob genuinely still carries the victim (so the test above is not asserting
// against a corpus that lost it some other way), and the durable record is what the
// masking depends on.
func TestTheSealIsTheRecordAndNotTheBlob(t *testing.T) {
	t.Parallel()

	const name = "sealrecord"
	cacheDir, gt, victim, _ := sealFixture(t, name)

	statePath := rebuildStatePathFor(cacheDir, gt, name)
	require.FileExists(t, statePath,
		"the delete must have PERSISTED its tombstone record — an in-memory-only set is not a seal "+
			"across a restart")
	require.NoError(t, os.Remove(statePath))

	p2 := closeOnCleanup(t, NewManager(cacheDir, 0))
	require.Contains(t, searchHitIDs(t, p2, gt, name, victim.Vector, 5), victim.ID,
		"CONTROL: with the record gone the stranded pre-delete blob DOES return the deleted id — so "+
			"the blob really carries it, and the record really is the seal")
}

// TestTheSealMergesRatherThanReplaces pins the membership rule the persisted record
// carries, at the one call site that could erase it.
//
// SetGraphTombstones REPLACES a graph's set, so handing it this delete's own window
// would drop every id an earlier pass had accumulated and re-open their windows. The
// record is read, merged into, and written back.
func TestTheSealMergesRatherThanReplaces(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mgr, gt, nm, _, _, docs := deleteRetryFixtureOfSize(t, "sealmerge", deleteFixtureN)

	// An earlier pass's accumulated set, exactly as the delta consumer leaves it.
	prior := []searchengine.ExternalID{"earlier-pass-id-a", "earlier-pass-id-b"}
	require.NoError(t, mgr.SaveRebuildState(gt, nm, 4242, prior))
	mgr.SetGraphTombstones(gt, nm, prior)

	require.NoError(t, mgr.DeleteFromBuckets(ctx, gt, nm, []searchengine.ExternalID{docs[0].ID}))

	watermark, carried, err := mgr.LoadRebuildState(gt, nm)
	require.NoError(t, err)
	require.EqualValues(t, 4242, watermark,
		"the seal must not move the watermark — that value is the rebuild's durability contract and "+
			"may advance only when a publish landed")
	require.Subset(t, carried, prior,
		"the earlier pass's ids must survive the delete's own seal — a replace here erases an "+
			"accumulated set and re-opens every window it was holding closed")
	require.Contains(t, carried, docs[0].ID, "and this delete's id is added to it")
	require.Len(t, carried, len(prior)+1, "and nothing else is")

	require.ElementsMatch(t, carried, mgr.graphTombstones(gt, nm),
		"and the engines are seeded from the MERGED set, so an Import masks every id the record holds")
}

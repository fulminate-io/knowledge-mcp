// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// multiwriter_e2e_test.go is the integration capstone: N fleet writers run a mixed
// build / merge / restart / depart workload against ONE (graph, name), and the suite
// asserts the three end-to-end properties together — the server corpus stays BOUNDED
// (it tracks the UNION of writers' live sets, never the cumulative ship history), no
// still-live writer ever loses a live blob (every Export id is on the server AND in
// its manifest), and every writer's Search stays correct (self-recall over its live
// corpus holds and no reaped/merged-away/deleted doc leaks into results).
//
// The fleet builds a SHARED deterministic corpus (the production multi-machine shape:
// every writer building the same graph converges on the same content-hash blobs, so
// the registry dedups to one copy at refcount-N and every writer's resident set
// covers the whole graph — the publish coverage ratio is naturally armed-and-passing,
// never gated). hnswRecallOK is reached through mgr.managerFor(gt, name) (the
// *distManager its signature requires), NOT Manager.Search.

// TestMultiWriterE2ELifecycle runs the N-writer mixed-lifecycle workload.
func TestMultiWriterE2ELifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const k = 3
	mgrs, svc := newMultiWriterFleet(t, k)
	gt, name := kgtypes.GraphKnowledge, "e2e-fleet"
	target := graphSelector(gt, name)

	// The shared deterministic corpus every writer builds (converges to ONE blob).
	// Kept small (under residentBackstopFloor=64) so that when one writer later merges
	// to a DISTINCT consolidated blob while the others still pin the original, the
	// transient two-blob server state stays under the floor and the publish coverage
	// ratio disarms — never gating the legitimate post-merge republish. (In production
	// every machine builds the same full corpus and converges, so the ratio is
	// naturally armed-and-passing; the small corpus is the test analog.)
	const corpusN = 30
	docs := vecContentDocs(corpusN)
	noneDeleted := map[searchengine.ExternalID]struct{}{}

	// BUILD: every writer AddAndShips + Flushes the shared corpus (sub-MinSegmentDocs
	// seals on Flush) + publishes. Deterministic convergence ⇒ one content-hash blob X
	// referenced by all K writers (refcount K).
	for _, mgr := range mgrs {
		require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, docs))
		require.NoError(t, mgr.Flush(ctx, gt, name))
	}
	dm0 := mgrs[0].managerFor(gt, name)
	require.Len(t, dm0.engine.Export(), 1, "the shared deterministic corpus seals one segment per writer")
	sharedX := dm0.engine.Export()[0].ID
	require.Equal(t, k, blobRefCount(svc, target, sharedX),
		"the converged blob is referenced by all K writers (refcount K)")

	// BOUNDED: the server holds ONE blob (deduped), not K copies.
	require.Equal(t, 1, serverSegCount(t, svc, target),
		"the server corpus is bounded to the union (one deduped blob), not K ship copies")

	// Every writer's self-recall over the full corpus holds, nothing absent leaks.
	for i, mgr := range mgrs {
		dm := mgr.managerFor(gt, name)
		recall, leaked := hnswRecallOK(dm, docs, noneDeleted)
		require.GreaterOrEqual(t, recall, 0.95, "writer %d self-recall over its live corpus", i)
		require.False(t, leaked, "writer %d leaks no absent doc", i)
	}

	// MERGE: writer 0 deletes > 33% of its docs and consolidates the survivors into a
	// new blob. Its deleted docs become its `absent` set; the other writers are
	// untouched. The merge occasion is applied directly (applyMerge) because the
	// engines a Manager builds have the automatic triggers disarmed — the fleet
	// properties under test are unchanged by how the consolidation was initiated.
	deleted := map[searchengine.ExternalID]struct{}{}
	dead := corpusN/3 + 1
	for i := range dead {
		dm0.engine.Delete(docs[i].ID)
		deleted[docs[i].ID] = struct{}{}
	}
	applyMerge(t, dm0, []searchengine.SegmentID{sharedX}, consolidatedHNSWBlob(t, docs[dead:]))
	require.NoError(t, mgrs[0].Flush(ctx, gt, name)) // re-publish writer 0's consolidated live set

	// Writer 0's search now excludes its deleted docs (no absent leak); its live-set
	// self-recall still holds.
	live0 := idSetExcept(docs, deleted)
	recall0, leaked0 := hnswRecallOK(dm0, docs, deleted)
	require.GreaterOrEqual(t, recall0, 0.95, "writer 0 self-recall over its post-merge live set")
	require.False(t, leaked0, "writer 0 leaks no deleted doc after the merge")
	require.NotEmpty(t, live0)

	// No live-blob loss: every id in writer 0's resident Export is present + referenced.
	assertLiveSetPublished(t, svc, mgrs[0], gt, name, "after writer 0's merge republish")

	// The other writers still reference the ORIGINAL shared blob X (they did not
	// merge), so X survives writer 0 dropping it — bounded server set, no loss.
	require.True(t, serverHasBlob(svc, target, sharedX),
		"the original shared blob survives writer 0's merge — writers 1..K still reference it")

	// RESTART: writer 1 restarts (fresh Manager, SAME writer_id + cache dir), reloads,
	// re-publishes — reaps nothing, and its reloaded engine still recalls the corpus.
	r1 := restartFleetMember(t, svc, 1, mgrs[1].cacheDir)
	r1DM := r1.managerFor(gt, name)
	require.NoError(t, r1DM.load(ctx))
	_, err := r1DM.shipAndPublish(ctx, r1DM.locallyShipped)
	require.NoError(t, err)
	recall1, leaked1 := hnswRecallOK(r1DM, docs, noneDeleted)
	require.GreaterOrEqual(t, recall1, 0.95, "restarted writer 1 recalls its reloaded corpus")
	require.False(t, leaked1, "restarted writer 1 leaks nothing")

	// DEPART: writer 2 simply stops (no further publishes). Writers 0 and 1 continue —
	// writer 2's referenced blobs must NOT be reaped by their publishes (no server
	// departed-writer sweep runs on the client side; refcount keeps them alive).
	departedRef := writerManifest(svc, target, mgrs[2].writerID, hnsw.New().Name())
	require.NotEmpty(t, departedRef, "departed writer 2 left a manifest")
	// Writers 0 and 1 publish again.
	require.NoError(t, mgrs[0].Flush(ctx, gt, name))
	_, err = r1DM.shipAndPublish(ctx, r1DM.locallyShipped)
	require.NoError(t, err)
	for _, id := range departedRef {
		require.True(t, serverHasBlob(svc, target, id),
			"departed writer 2's referenced blob %s survives the other writers' publishes (refcount)", id)
	}

	// FINAL BOUNDED CHECK: the server corpus tracks the UNION of the live writers'
	// resident sets + the departed writer's pinned set — never the cumulative ship
	// history. Compute the expected union of distinct ids across all live manifests +
	// writer 2's pinned manifest, and assert the server count equals it.
	expected := map[string]struct{}{}
	for _, mgr := range []*fleetMember{mgrs[0], r1, mgrs[2]} {
		for _, id := range writerManifest(svc, target, mgr.writerID, hnsw.New().Name()) {
			expected[id] = struct{}{}
		}
	}
	require.Equal(t, len(expected), serverSegCount(t, svc, target),
		"the server corpus equals the union of the live + departed writers' manifests — bounded, not cumulative")
}

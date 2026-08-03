// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// multiwriter_publish_reclaim_test.go proves writer A's merge-driven republish
// reaps its OWN merged-away constituents (bounded server corpus) while a concurrent
// writer B publishes its own distinct corpus on the SAME graph — and A's publish
// NEVER reaps B's referenced blob, nor ever under-publishes A's own live set. It
// lifts the TestRegistryReclaimE2E ship+merge+publish shape to a real-HNSW fleet
// writer, overlaying an independently-publishing B.

// assertLiveSetPublished asserts every id in mgr's resident Export() is present on
// the server AND referenced by mgr's current manifest — the "live set is never
// under-published" invariant, checked at a publish observation point.
func assertLiveSetPublished(t *testing.T, svc *sharedServerFake, mgr *fleetMember, gt kgtypes.GraphType, name, where string) {
	t.Helper()
	target := graphSelector(gt, name)
	manifest := map[string]bool{}
	for _, id := range writerManifest(svc, target, mgr.writerID, "hnsw") {
		manifest[id] = true
	}
	for _, b := range mgr.managerFor(gt, name).engine.Export() {
		id := b.ID
		require.True(t, serverHasBlob(svc, target, id), "%s: live id %s must be present on the server", where, id)
		require.True(t, manifest[id], "%s: live id %s must be referenced by the writer's manifest", where, id)
	}
}

// TestMultiWriterPublishReclaim drives writer A through a dead-ratio merge +
// republish while writer B publishes its own distinct corpus on the same graph.
// Tiny corpora keep the server's shipped doc count under residentBackstopFloor so
// the coverage ratio disarms and the legitimate post-merge republish is not gated.
func TestMultiWriterPublishReclaim(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mgrs, svc := newMultiWriterFleet(t, 2)
	a, b := mgrs[0], mgrs[1]
	gt, name := kgtypes.GraphCode, "publish-reclaim"
	target := graphSelector(gt, name)

	// Tiny corpora: even with A's old constituent, A's merged segment, and B's
	// segment all transiently present on the server, the total shipped doc count
	// stays under residentBackstopFloor (64) so the coverage RATIO disarms and the
	// legitimate post-merge republish is never gated. 18+18+~11 = 47 < 64.
	const seg = 18

	// B ships + publishes its OWN distinct corpus (seed far from A's).
	bDocs := vecContentDocsSeed(seg, 5000)
	require.NoError(t, b.AddAndMarkDirty(ctx, gt, name, bDocs))
	require.NoError(t, b.Flush(ctx, gt, name))
	bExport := b.managerFor(gt, name).engine.Export()
	require.Len(t, bExport, 1, "B seals one segment")
	bSeg := bExport[0].ID
	require.Equal(t, 1, blobRefCount(svc, target, bSeg), "B's segment is referenced only by B")

	// A ships + publishes its own distinct corpus (one segment).
	aDocs := vecContentDocsSeed(seg, 0)
	require.NoError(t, a.AddAndMarkDirty(ctx, gt, name, aDocs))
	require.NoError(t, a.Flush(ctx, gt, name))
	aDM := a.managerFor(gt, name)
	preExport := aDM.engine.Export()
	require.Len(t, preExport, 1, "A seals one segment")
	aSeg0 := preExport[0].ID

	// OBSERVATION POINT 1: both writers' live sets are fully published; neither
	// reaped the other's blob.
	assertLiveSetPublished(t, svc, a, gt, name, "after A's first publish")
	assertLiveSetPublished(t, svc, b, gt, name, "after A's first publish (B)")
	require.True(t, serverHasBlob(svc, target, bSeg), "B's blob survives A's publish")

	// A merges: delete > 33% of A's docs and consolidate the survivors, so aSeg0
	// becomes the merged-away constituent behind a NEW content-hash. The occasion is
	// applied directly because this Manager's engines have the automatic triggers
	// disarmed; the publish-reclaim behavior under test is unchanged.
	aDead := seg/3 + 1
	for i := range aDead {
		aDM.engine.Delete(aDocs[i].ID)
	}
	applyMerge(t, aDM, []searchengine.SegmentID{aSeg0}, consolidatedHNSWBlob(t, aDocs[aDead:]))
	postExport := aDM.engine.Export()
	require.Len(t, postExport, 1, "A's merge consolidates to exactly one segment")
	aSegMerged := postExport[0].ID
	require.NotEqual(t, aSeg0, aSegMerged, "the merge produces a new content hash")

	// A re-publishes its consolidated resident live set (Flush → shipAndPublish).
	require.NoError(t, a.Flush(ctx, gt, name))

	// (1) The merged-away constituent is reaped by A's publish refcount-GC.
	require.NotContains(t, shippedHNSWIDs(svc), aSeg0,
		"the merged-away constituent is reaped by A's publish (no manifest references it)")
	require.False(t, serverHasBlob(svc, target, aSeg0), "aSeg0 absent from the server after the merge republish")

	// (2) A's live set is fully published — never under-published.
	assertLiveSetPublished(t, svc, a, gt, name, "after A's merge republish")

	// (3) BOUNDED: server count == A's live set + B's distinct set, NOT the pre-merge
	// accumulation (which would also carry aSeg0).
	require.Equal(t, len(postExport)+len(bExport), serverSegCount(t, svc, target),
		"server count is bounded — A's consolidated live set plus B's distinct set, not the pre-merge accumulation")

	// B's blob was NEVER reaped by A's merge-driven publish (cross-writer safety).
	require.True(t, serverHasBlob(svc, target, bSeg), "B's referenced blob survives A's merge republish")
	require.Equal(t, 1, blobRefCount(svc, target, bSeg), "B's blob still referenced only by B (A never touched it)")
}

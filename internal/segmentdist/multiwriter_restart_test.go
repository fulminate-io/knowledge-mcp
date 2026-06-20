// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// (corpora helpers convergeDocs / restartFleetMember / writerManifest / blobRefCount
// are defined in multiwriter_convergence_test.go + multiwriter_testkit_test.go.)

// multiwriter_restart_test.go proves two client-view properties under a fleet:
//  1. RESTART SAFETY — a writer that restarts (fresh Manager, SAME stable writer_id,
//     SAME cache dir), reloads its prior corpus, and re-publishes reaps NOTHING of
//     its own prior corpus (the restart-tail guard) NOR a concurrent
//     writer's blobs, and the server holds ONE manifest per writer_id (the restarted
//     manifest REPLACES the prior one, never duplicates it).
//  2. GOLDEN-GRAPH SURVIVAL — a read-only writer that reloads + Search()es but never
//     re-publishes keeps its manifest + blobs while other fleet writers churn
//     (ship/merge/publish) repeatedly: no other writer's publish-driven refcount-GC
//     reaps a blob the read-only writer's manifest still references.
//
// Corpora are deliberately TINY: in production every writer is a separate machine
// building the SAME deterministic corpus, so each writer's resident set covers the
// whole graph and the publish coverage ratio never gates it. A multi-writer TEST
// where writers ship DISJOINT slices of one graph would trip that ratio (a writer's
// slice is below 0.5 of the union) — so the corpora are kept under
// residentBackstopFloor (64 docs) where the ratio legitimately disarms, isolating
// the refcount/restart properties under test from the unrelated coverage gate.

// addFlushSeg AddAndShips + Flushes a tiny corpus through mgr, sealing one segment,
// and returns its content-hash id.
//
//nolint:unparam // gt kept on the signature — it pairs with name to form the graph selector this helper publishes to.
func addFlushSeg(t *testing.T, mgr *Manager, gt kgtypes.GraphType, name string, seed int) string {
	t.Helper()
	ctx := context.Background()
	docs := convergeDocs(8, seed)
	require.NoError(t, mgr.AddAndShip(ctx, gt, name, docs))
	require.NoError(t, mgr.Flush(ctx, gt, name))
	export := mgr.managerFor(gt, name).engine.Export()
	require.NotEmpty(t, export)
	return export[len(export)-1].ID
}

// TestMultiWriterRestartReapsNothing is the restart-safety arm.
func TestMultiWriterRestartReapsNothing(t *testing.T) {
	ctx := context.Background()
	mgrs, svc := newMultiWriterFleet(t, 2)
	a, b := mgrs[0], mgrs[1]
	gt, name := kgtypes.GraphCode, "restart-safety"
	target := graphSelector(gt, name)

	// A ships a multi-segment corpus (three tiny segments) + publishes its manifest.
	const aSegs = 3
	for s := range aSegs {
		addFlushSeg(t, a, gt, name, s)
	}
	aPriorManifest := writerManifest(svc, target, a.writerID, "hnsw")
	require.Len(t, aPriorManifest, aSegs, "A's manifest references its whole corpus")

	// B independently ships + publishes its own distinct segment.
	bSeg := addFlushSeg(t, b, gt, name, 500)
	require.Equal(t, 1, blobRefCount(svc, target, bSeg), "B's segment referenced only by B")

	priorServer := shippedHNSWIDs(svc)
	require.Len(t, priorServer, aSegs+1, "server holds A's corpus + B's segment")

	// RESTART: a fresh Manager with A's SAME writer_id + SAME cache dir. Its load()
	// re-imports the WHOLE server corpus for the graph (List is writer-agnostic — it
	// returns every blob under the graphKey, A's AND B's), then re-publishes its
	// (fully reloaded) resident live set. A fully-reloaded resident set passes the
	// coverage gate, so the publish references the whole corpus and reaps nothing.
	aRestart := restartFleetMember(t, a.caller, 0, a.cacheDir)
	aRestartDM := aRestart.managerFor(gt, name)
	require.NoError(t, aRestartDM.load(ctx))
	require.Len(t, aRestartDM.engine.Export(), aSegs+1,
		"the restarted writer re-imports the whole server corpus for the graph (writer-agnostic List)")
	_, err := aRestartDM.shipAndPublish(ctx, nil, aRestartDM.locallyShipped)
	require.NoError(t, err)

	// The restarted publish reaped NOTHING of the prior corpus — the restart-tail
	// guard: every prior blob, A's AND B's, survives.
	now := shippedHNSWIDs(svc)
	require.Len(t, now, aSegs+1, "the restart re-publish reaps nothing — the corpus is unchanged")
	for id := range priorServer {
		require.Contains(t, now, id, "every prior blob survives the restart re-publish")
	}
	require.True(t, serverHasBlob(svc, target, bSeg), "B's blob survives A's restart re-publish")

	// ONE manifest per writer_id: A's restarted manifest REPLACED its prior one (it
	// did NOT create a second manifest for the same writer_id). The replacement
	// references at least all of A's original ids (the restart re-imported them), and
	// the server holds exactly 2 distinct writer manifests (A + B).
	aNow := writerManifest(svc, target, a.writerID, "hnsw")
	for _, id := range aPriorManifest {
		require.Contains(t, aNow, id, "A's restarted manifest still references its original corpus (replaced, not lost)")
	}
	require.Equal(t, 2, distinctWriterManifestCount(svc, target),
		"the server holds exactly one manifest per writer_id (A + B), not a duplicate for the restart")
}

// distinctWriterManifestCount returns how many distinct writer manifests exist for
// the graph (one per writer_id\x00format key) — the "one manifest per writer_id"
// signal a restart must preserve.
func distinctWriterManifestCount(svc *fakeSegmentService, target *knowledgev1.GraphSelector) int {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	return len(svc.manifests[svc.key(target)])
}

// TestMultiWriterGoldenGraphSurvival is the golden-graph (read-only writer) arm: a
// writer that reloads + Search()es but never re-publishes keeps its manifest +
// blobs across other writers' repeated ship/publish churn.
func TestMultiWriterGoldenGraphSurvival(t *testing.T) {
	ctx := context.Background()
	mgrs, svc := newMultiWriterFleet(t, 2)
	golden, churner := mgrs[0], mgrs[1]
	gt, name := kgtypes.GraphCode, "golden-survival"
	target := graphSelector(gt, name)

	// The GOLDEN writer ships + publishes its corpus once, then only ever reads.
	goldenSeg := addFlushSeg(t, golden, gt, name, 0)
	goldenVec := convergeDocs(8, 0)[0].Vector

	// The CHURNER repeatedly ships + publishes its own distinct corpus on the SAME
	// graph — every cycle drives a refcount-GC that must never reach golden's blob.
	for c := range 3 {
		churnSeg := addFlushSeg(t, churner, gt, name, 1000+c)

		// The golden writer only READS (its load is idempotent; it never publishes).
		_, err := golden.Search(ctx, gt, name, "", goldenVec, 5)
		require.NoError(t, err)

		// Golden's blob is NEVER reaped by the churner's publish-driven refcount-GC.
		require.True(t, serverHasBlob(svc, target, goldenSeg),
			"golden's blob survives churner cycle %d — no other writer's GC reaps a referenced blob", c)
		require.Contains(t, writerManifest(svc, target, golden.writerID, "hnsw"), goldenSeg,
			"golden's manifest still references its blob after churner cycle %d", c)
		require.True(t, serverHasBlob(svc, target, churnSeg), "the churner's own latest blob is present")
	}

	// Final: golden's self-recall still works over its surviving corpus.
	hits, err := golden.Search(ctx, gt, name, "", goldenVec, 1)
	require.NoError(t, err)
	require.NotEmpty(t, hits, "the read-only golden writer can still search its surviving corpus")
}

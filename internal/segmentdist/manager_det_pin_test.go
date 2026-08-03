// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// manager_det_pin_test.go covers what an embed drain sees ACROSS a reset rebuild: the
// drain that lands after the rebuild retired the prior layer must still publish, and
// must carry the new layer forward rather than the reaped one.
//
// TWO PIN TESTS USED TO LIVE HERE AND ARE GONE, because their subject is. They asserted
// that the outgoing layer stayed referenced while a rebuild held its replacement in a
// SECOND engine — a window that opened only because the reset dropped that engine before
// building. A reset now builds ASIDE and swaps at the serving engine, so the engine goes
// on serving the old layer right up to the CAS and there is no unreferenced interval to
// pin across. The property they protected is not weakened, it is structural.
//
// EVERY "the publish landed" ASSERTION HERE READS THE SWAP COUNTER, never a nil
// error. Two paths skip a publish with a nil error (the coverage gate and the agent's
// 409), so an error-only check reads a skip as a success and every downstream
// assertion then holds vacuously against a manifest nothing swapped.

// pinFixture is the shared setup: a Manager over a shared server fake, plus the
// target its manifests are keyed under.
type pinFixture struct {
	svc    *sharedServerFake
	view   *fakeSegmentSource
	mgr    *Manager
	gt     kgtypes.GraphType
	name   string
	target *knowledgev1.GraphSelector
}

// driveEmbedPublish runs a full embed write-then-tick cycle and asserts its publish
// LANDED, read off the embed engine's swap counter. It is the operation a rebuild has
// to stay safe against: whatever this publishes becomes the whole "hnsw" manifest.
func driveEmbedPublish(
	t *testing.T, ctx context.Context, mgr *Manager,
	gt kgtypes.GraphType, name string, docs []searchengine.Document,
) {
	t.Helper()
	embedDM := mgr.managerFor(gt, name)
	before := embedDM.completedSwapCount()
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))
	require.Greater(t, embedDM.completedSwapCount(), before,
		"the embed publish must LAND — a skipped publish also returns a nil error")
}

// newPinFixture builds the Manager + shared server these tests drive.
func newPinFixture(t *testing.T, name string) pinFixture {
	t.Helper()
	svc, view := newSegmentHarness(t)
	mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(view))
	gt := kgtypes.GraphCode
	return pinFixture{svc: svc, view: view, mgr: mgr, gt: gt, name: name, target: graphSelector(gt, name)}
}

// TestEmbedDrainAfterRebuildDropsRetiredLayer is THE CATCHER for a drain stranded by a
// reset: after a reset rebuild retires the prior layer, the serving engine's next drain
// publish must still LAND, and must name the new layer rather than the reaped one.
//
// THE BREAKAGE IS SILENT WITHOUT IT, which is why the landing assertion reads the swap
// counter. If the serving engine were still holding the prior layer once the rebuild's
// publish reference-counted those blobs away, its next publish would name ids the server
// no longer has. That publish is refused by the subset gate on the local path and by the
// agent's 409 on the cloud path — BOTH of which return a NIL ERROR. So every embed write
// after a rebuild would stop becoming durable, and nothing in the error path would say
// so.
//
// THE CLAIM IS UNCHANGED FROM THE TWO-ENGINE VERSION; THE SETUP IS SIMPLER. It used to
// need a hand-off step to move the rebuilt layer from a staging engine into the serving
// one. There is one engine now, so the swap IS the hand-off and there is nothing to
// arrange — which is exactly what makes the property structural rather than maintained.
func TestEmbedDrainAfterRebuildDropsRetiredLayer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	f := newPinFixture(t, "handoff-drain")

	// The rebuild corpus is deliberately LARGER than the prior layer: the rebuild's own
	// publish divides its resident doc count by the whole shipped store (the prior layer
	// included), so equal-sized corpora would sit exactly on the coverage ratio and the
	// publish this test depends on could be skipped rather than landed.
	const priorDocs, rebuildDocs, drainDocs = 300, 1025, 60
	rebuildCorpus := vecContentDocs(rebuildDocs)
	buckets := searchengine.BucketCountFor(rebuildDocs)
	require.GreaterOrEqual(t, buckets, 2,
		"the rebuild corpus must derive at least 2 partitions so a one-partition drain leaves a new-layer segment standing")

	// The prior layer, held by the SERVING engine exactly as a live daemon holds it.
	seedShipped(t, ctx, f.mgr, f.gt, f.name, vecContentDocs(priorDocs))
	prior := writerManifest(f.svc, f.target, "", hnswFormatName)
	require.NotEmpty(t, prior, "the prior layer must be published before the rebuild runs")

	// The rebuild: it publishes its own layer and reaps the prior one.
	stageRebuildRun(t, ctx, f.mgr, f.gt, f.name, rebuildCorpus)
	res, err := f.mgr.FinalizeRebuild(ctx, f.gt, f.name)
	require.NoError(t, err)
	superseded, swapped := res.HNSWSuperseded, res.Swapped
	require.True(t, swapped, "the rebuild's publish must LAND — a skipped publish also returns a nil error")

	retired := map[string]struct{}{}
	for _, id := range superseded {
		retired[id] = struct{}{}
	}
	for _, id := range prior {
		require.Contains(t, retired, id, "the rebuild must retire every prior-layer segment")
	}

	newLayer := writerManifest(f.svc, f.target, "", hnswFormatName)
	require.Len(t, newLayer, buckets, "the rebuild publishes one segment per partition it built")
	for _, id := range newLayer {
		require.NotContains(t, retired, id,
			"the retired set must be disjoint from the published layer — an Unload of a published id would delete it")
	}

	// THE SWAP ITSELF: the serving engine now holds the new layer and not the old.
	servingResident := residentIDs(f.mgr.managerFor(f.gt, f.name))
	for _, id := range newLayer {
		require.Contains(t, servingResident, id,
			"the serving engine must hold the layer the rebuild just published")
	}
	for _, id := range prior {
		require.NotContains(t, servingResident, id,
			"the serving engine must have dropped the layer the rebuild retired")
	}

	// The drain, confined to ONE partition so exactly one new-layer segment is rebuilt.
	batch := docsInBucket(t, 0, buckets, drainDocs, "handoff-drain-")
	driveEmbedPublish(t, ctx, f.mgr, f.gt, f.name, batch)

	published := map[string]struct{}{}
	for _, id := range writerManifest(f.svc, f.target, "", hnswFormatName) {
		published[id] = struct{}{}
	}

	// (a) the drain named nothing the rebuild retired.
	for _, id := range prior {
		require.NotContains(t, published, id,
			"the drain publish must not name a segment the rebuild's swap already reference-counted away")
	}

	// (b) it carried the new layer forward. The one segment it does NOT name is the
	// partition it rebuilt — asserted as a count, so a drain that lost the layer
	// wholesale cannot pass by claiming everything was "re-emitted".
	dropped := 0
	for _, id := range newLayer {
		if _, named := published[id]; !named {
			dropped++
		}
	}
	require.Equal(t, 1, dropped,
		"the drain re-emits exactly the ONE partition its batch touched; every other new-layer segment stays named")

	// (c) and the corpus is whole: every rebuilt and drained document is live in
	// exactly one published segment.
	summed := 0
	for _, m := range f.svc.manifestMetas(f.target, "") {
		if m.GetFormat() == hnswFormatName {
			summed += int(m.GetDocCount())
		}
	}
	require.Equal(t, rebuildDocs+drainDocs, summed,
		"the published manifest must hold the rebuilt corpus plus the drained batch, each exactly once")
}

// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// recallFloor is the tree's own HNSW integration floor (engine_integration_test.go),
// adopted here rather than invented. The bar is deliberately NOT 1.000: HNSW is
// APPROXIMATE search, so a correct implementation can legitimately miss a neighbor
// and an exact assertion would be unsatisfiable by correct work.
const recallFloor = 0.90

// recallSampleN is how many ids each gate probes, spread by stride across the corpus
// so the sample is not clustered in one partition.
const recallSampleN = 120

// sampledStoredVectorRecall measures recall@10 over a stride-spread sample, querying
// each id with the vector READ BACK FROM THE ENGINE for that id.
//
// READING THE VECTOR BACK IS AN INTENTIONAL EXCEPTION to the plan's fixture-constant
// rule, and the exception is the point. That rule exists so a test never compares the
// engine against itself on the quantity under test — and it governs COUNTS. It must
// NOT govern the query VECTOR here, because the fixture holds two different vectors
// per id and WHICH copy survives is the implementation's ruled choice. Hard-coding
// one layer's vector would fail a correct implementation that legitimately kept the
// other: measured at 0.183/0.254 with layer 0's vector against 0.417 with the stored
// one, and that gap is implementation choice, not defect.
//
// It does not soften the gate. The defect measures 0.417 with the stored vector,
// because duplicate NODES break retrieval even when the query is exactly what the
// engine holds for that id.
func sampledStoredVectorRecall(
	t *testing.T, mgr *Manager, dm *distManager[[]byte, struct{}],
	gt kgtypes.GraphType, name string, docs []searchengine.Document, sampleN int,
) float64 {
	t.Helper()
	ctx := context.Background()
	stride := max(len(docs)/sampleN, 1)

	found, probed := 0, 0
	for i := 0; i < len(docs) && probed < sampleN; i += stride {
		d := docs[i]
		vec, ok := dm.engine.VectorByID(d.ID)
		require.True(t, ok, "id %q must resolve a stored vector — VectorByID routes through the members map, so a miss here is a membership failure rather than a retrieval one", d.ID)
		probed++
		hits, err := mgr.Search(ctx, gt, name, dupLayerToken(i), vec, 10)
		require.NoError(t, err)
		if hitsContain(hits, d.ID) {
			found++
		}
	}
	require.Positive(t, probed, "the sample must probe something")
	return float64(found) / float64(probed)
}

// TestRecallSurvivesDuplicateIdMerge asserts recall@10 >= 0.90 on the
// duplicate-layer fixture after the repartition merges both layers.
//
// WHAT IT CATCHES that six membership gates could not. When a merge's constituents
// carry the same id with different vectors, both copies used to reach the builder, so
// the graph held TWO NODES PER ID while the route map — last-wins — recorded one.
// members then holds every id exactly once (membership passes), VectorByID resolves
// (the route map resolves), resident reads the distinct count — and 83% of the corpus
// could not be retrieved. Only a probe that SEARCHES sees it.
//
// WHICH PRODUCTION CALL WOULD HAVE TO BE WRONG for this to go red: hnsw
// Format.Merge's item collection (and the engine's newEntry invariant behind it).
//
// RED-FIRST EVIDENCE, measured by THIS test against the unfixed merge (dedup removed,
// invariant disarmed): recall 0.458 with the stored vector, against this 0.90 bar. The
// investigation's own scratch runs measured 0.417 with the stored vector and
// 0.254/0.183 with a hard-coded layer vector — same defect, same order.
func TestRecallSurvivesDuplicateIdMerge(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gt, name := kgtypes.GraphCode, "repro" // twoLayerFixture hardcodes both; a different name searches an EMPTY graph.
	mgr, dm, base := twoLayerFixture(t)

	// Drive the repartition that merges the two layers — the state that breaks. A
	// measurement taken BEFORE this merge measures nothing: the duplicated state
	// retrieves at 1.000.
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, base[:dupLayerWindow]))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	recall := sampledStoredVectorRecall(t, mgr, dm, gt, name, base, recallSampleN)
	t.Logf("POST-MERGE recall@10=%.3f over %d sampled ids (resident=%d segments=%d)",
		recall, recallSampleN, dm.engine.ResidentDocCount(), len(dm.engine.Export()))

	require.GreaterOrEqual(t, recall, recallFloor,
		"a merge over constituents sharing ids must leave the corpus RETRIEVABLE: duplicate nodes in the built graph break search even when the query vector is exactly what the engine stored for that id")
}

// TestRecallSurvivesWindowScaleDuplicateMerge is the PRODUCTION-SHAPE arm — a
// ~100-duplicate-id window against a large single-layer corpus, drained so the
// merge consumes the window while it still holds both copies.
//
// LABEL, WRITTEN FROM THIS TEST'S FIRST RUN AND NOT RELABELLED: MEASURED at recall
// 1.000 against the UNFIXED merge (dedup removed, invariant disarmed), so this arm is
// a REGRESSION FENCE rather than a red-first gate. That result is itself the sizing
// answer the step asked for — the defect concentrates at LAYER scale, where a full
// second copy of every document reaches each merge, and does NOT reach the transient
// the write path actually produces. Its layer-scale sibling measured 0.458 in the
// same control run; this one deliberately carries no red, and relabelling it to match
// would misreport the blast radius.
//
// SCALE NOTE: the window is dupLayerWindow (the fixture's own write-window constant),
// which is smaller than the "~100" the criterion describes. It is the constant the
// write path's transient is defined by in this fixture, and inventing a second one
// would break the derive-expectations-from-fixture-constants rule.
//
// THE FRAMING THAT MAKES IT MEANINGFUL: the duplicated state retrieves at 1.000
// BEFORE a merge, so the transient window itself is not the hazard. The hazard is a
// MERGE that consumes a window still holding both copies — which is why the drain
// below is what the measurement is taken after, never before.
func TestRecallSurvivesWindowScaleDuplicateMerge(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gt, name := kgtypes.GraphCode, "windowscale"

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	dm := mgr.managerFor(gt, name)

	// A large SINGLE-layer corpus: the ordinary steady state.
	base := dupLayerDocs(dupLayerCorpus, 0)
	require.NoError(t, mgr.ReplaceBucket(ctx, gt, name, nil, base))

	// The production transient: ~100 ids rewritten with DIFFERENT bytes. The write
	// path force-seals them into a tail segment while the partitions still hold the
	// previous copies, so for this instant the corpus carries two copies of exactly
	// these ids — and the drain below makes a merge consume that state.
	window := dupLayerDocs(dupLayerWindow, 99)
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, window))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	require.Equal(t, dupLayerCorpus, dm.engine.ResidentDocCount(),
		"a rewrite of existing ids must not grow the corpus — resident is the distinct id count")

	recall := sampledStoredVectorRecall(t, mgr, dm, gt, name, base, recallSampleN)
	t.Logf("WINDOW-SCALE recall@10=%.3f over %d sampled ids (window=%d duplicate ids against a %d corpus, segments=%d)",
		recall, recallSampleN, dupLayerWindow, dupLayerCorpus, len(dm.engine.Export()))

	require.GreaterOrEqual(t, recall, recallFloor,
		"a merge consuming a transient window that still holds both copies must leave the corpus retrievable")
}

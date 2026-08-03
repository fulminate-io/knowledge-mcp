// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestCoverageGateStillRefusesDegenerateAfterGroupSwap is the NON-WEAKENING guard
// for the group-swap change.
//
// The coverage gate is the control that kept the incident's damage local to the
// client: it refused to publish the degenerate live set the discard had already
// produced, so the shipped manifest and blobs survived. It neither caused nor
// missed the loss. Nothing in the group swap may make publishes succeed more
// readily.
//
// TWO LEGS, and each is useless without the other:
//
//   - REFUSAL, WITH THE REASON PINNED. Asserting merely that "a skip occurred"
//     would be satisfied by an unrelated failure — a List error, a 409, a
//     transport fault — and would pass even if the coverage check itself had been
//     removed. The COVERAGE arm specifically must still fire.
//   - A HEALTHY CONTROL. Without it, an implementation that refuses everything —
//     the trivially "safe" regression — passes the refusal leg perfectly.
//
// ASSERT THE OUTCOME, NEVER THE CONSTANT. A gate that reads the ratio constant
// passes a change that lowers it. Never adjust the constant to make a fixture
// pass: after DocCount began counting distinct members the coverage NUMERATOR
// halves on a duplicated corpus while the DENOMINATOR lags on shipped metas built
// before the change, so a transitional skew is expected — and the correct response
// to it is to re-ship or conservative-disarm, never to lower the bar.
func TestCoverageGateStillRefusesDegenerateAfterGroupSwap(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gt, name := kgtypes.GraphCode, "gateRepo"

	// Ship a real multi-segment corpus so the coverage denominator is armed.
	svc, gc := newSegmentHarness(t)
	mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
	const corpusSegs = 3
	for b := range corpusSegs {
		batch := hnswVecDocs(searchCorpusN)
		for i := range batch {
			batch[i].ID = fmt.Sprintf("gate-b%d-%s", b, batch[i].ID)
		}
		require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, batch))
	}
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))
	require.NotEmpty(t, shippedHNSWIDs(svc), "the fixture must ship a corpus or the denominator is unarmed")

	dm := mgr.managerFor(gt, name)

	t.Run("healthy live set publishes", func(t *testing.T) {
		// The engine's own resident set IS the shipped corpus: a non-degenerate,
		// fully-covered live set. It must be publishable.
		liveSet := map[searchengine.SegmentID]struct{}{}
		for _, blob := range dm.engine.Export() {
			liveSet[blob.ID] = struct{}{}
		}
		require.NotEmpty(t, liveSet)

		ok, reason, err := dm.publishCoverageOK(ctx, liveSet, dm.engine.ResidentDocCount())
		require.NoError(t, err)
		require.True(t, ok,
			"a healthy live set must PUBLISH — a gate that refuses everything is the trivially safe regression this leg exists to catch (reason given: %q)", reason)
	})

	t.Run("degenerate live set is refused with the coverage reason", func(t *testing.T) {
		// A FRESH PROCESS over the same shipped corpus, holding only a handful of
		// documents — the exact shape a degenerate live set has: non-empty, a genuine
		// subset of what the server holds, and far below the coverage ratio. This is
		// the state the discard left the client in during the incident.
		fresh := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
		require.NoError(t, fresh.AddAndMarkDirty(ctx, gt, name, hnswVecDocs(4)))
		freshDM := fresh.managerFor(gt, name)

		exported := freshDM.engine.Export()
		require.NotEmpty(t, exported, "the degenerate fixture must hold at least one segment — an EMPTY set is refused by a different arm")
		liveSet := map[searchengine.SegmentID]struct{}{}
		for _, blob := range exported {
			liveSet[blob.ID] = struct{}{}
		}

		ok, reason, err := freshDM.publishCoverageOK(ctx, liveSet, freshDM.engine.ResidentDocCount())
		require.NoError(t, err)
		require.False(t, ok, "a live set far below the shipped corpus must NOT be publishable")
		require.Equal(t, "resident doc count below coverage ratio of shipped corpus", reason,
			"the COVERAGE arm specifically must be what refused this; any other reason means the coverage check no longer fires")
	})
}

// TestSwapTimeGateRefusesADegenerateBuiltLayer is THE CATCHER for the prerequisite
// this whole arc rests on: the degeneracy policy must be answerable about a layer that
// has been BUILT but is not yet resident.
//
// WHY IT MATTERS ONLY NOW. While a degenerate rebuild lands in a second engine, a
// publish-time gate is sufficient — the serving engine keeps the good corpus and a
// refused publish leaves the prior manifest and every blob intact, which is what the
// skip path's own comment claims. Once a rebuild replaces the serving set IN PLACE the
// swap is the destructive act: by publish time reads are already being served from the
// degenerate layer, so the manifest would be protected and the corpus would not.
//
// BOTH LEGS ARE REQUIRED and the healthy one is not decoration. A gate that refused
// every prospective layer would satisfy the refusal leg perfectly while making every
// rebuild impossible — the same trivially-safe regression the sibling test above
// exists to catch. The third leg pins the "before it becomes resident" clause
// literally: consulting the gate must not itself move the engine.
func TestSwapTimeGateRefusesADegenerateBuiltLayer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gt, name := kgtypes.GraphCode, "swapGateRepo"

	// A real multi-segment corpus, so the shipped denominator is armed and the ratio
	// arm can actually fire. Without this the tiny-graph disarm makes every verdict a
	// pass and the refusal leg is vacuous.
	svc, gc := newSegmentHarness(t)
	mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
	const corpusSegs = 3
	for b := range corpusSegs {
		batch := hnswVecDocs(searchCorpusN)
		for i := range batch {
			batch[i].ID = fmt.Sprintf("swapgate-b%d-%s", b, batch[i].ID)
		}
		require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, batch))
	}
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))
	require.NotEmpty(t, shippedHNSWIDs(svc), "the fixture must ship a corpus or the denominator is unarmed")

	dm := mgr.managerFor(gt, name)
	priorResident := residentIDs(dm)
	require.NotEmpty(t, priorResident, "the prior layer must be serving before the gate is consulted")

	t.Run("a healthy prospective layer is admitted", func(t *testing.T) {
		// The engine's own Export stands in for a rebuild that produced the same corpus:
		// real ids the server holds, and the full document count.
		ok, reason, err := dm.prospectiveLayerOK(ctx, dm.engine.Export())
		require.NoError(t, err)
		require.True(t, ok,
			"a prospective layer covering the corpus must be ADMITTED — a gate that refuses everything makes every rebuild impossible (reason given: %q)", reason)
	})

	t.Run("a degenerate prospective layer is refused on the coverage arm", func(t *testing.T) {
		// The shape a degenerate rebuild produces: a non-empty layer holding a handful of
		// documents against a shipped corpus of thousands. It is NOT resident — that is
		// the whole point, since under build-aside this verdict is taken before the swap.
		degenerate := []searchengine.SegmentBlob{
			{ID: "prospective-degenerate-seg", Format: hnswFormatName, DocCount: 4},
		}
		ok, reason, err := dm.prospectiveLayerOK(ctx, degenerate)
		require.NoError(t, err)
		require.False(t, ok, "a built layer far below the shipped corpus must NOT be admitted to the serving set")
		require.Equal(t, "resident doc count below coverage ratio of shipped corpus", reason,
			"the COVERAGE arm specifically must refuse this; any other reason means the prospective count is not reaching the ratio check")
	})

	t.Run("an empty prospective layer is refused", func(t *testing.T) {
		ok, reason, err := dm.prospectiveLayerOK(ctx, nil)
		require.NoError(t, err)
		require.False(t, ok)
		require.Contains(t, reason, "empty")
	})

	// THE PRIOR SET STILL SERVES. Consulting the gate is a verdict, not a mutation, so
	// a refusal cannot have cost the corpus anything.
	require.Equal(t, priorResident, residentIDs(dm),
		"the prior layer must still be resident and serving after a refused prospective layer")
}

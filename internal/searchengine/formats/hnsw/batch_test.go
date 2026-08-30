// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// batchEvent is one observed step of the expansion: either a neighbor RUN was
// requested, or a batched SCORE was issued for some ids.
type batchEvent struct {
	kind string // "run" or "score"
	ids  []uint32
}

// batchTrace records runs and scores IN ORDER on a single timeline.
//
// THE ORDERING IS THE WHOLE INSTRUMENT. Counting runs and scores separately and
// comparing totals is not enough — a first draft of this test did exactly that
// and PASSED against an implementation that split every run into one call per
// id, because with many expansions the split call count still sat under the run
// count. Interleaving them on one timeline makes "one score per run" checkable
// directly instead of inferred from totals.
type batchTrace struct {
	events []batchEvent
}

func (tr *batchTrace) record(kind string, ids []uint32) {
	rec := make([]uint32, len(ids))
	copy(rec, ids)
	tr.events = append(tr.events, batchEvent{kind: kind, ids: rec})
}

// installProbe wraps vb's resolved batch scorer, DELEGATING to the real one so
// the search still produces correct results. A probe that replaced the scorer
// would be testing the probe.
//
// The ids are copied because the caller reuses that buffer across runs;
// recording the slice itself would leave every entry aliasing the last run.
func (tr *batchTrace) installProbe(vb *vectorBlock) {
	real := vb.batchScore
	vb.batchScore = func(dst []float32, q *preparedQuery, b *vectorBlock, ids []uint32) {
		tr.record("score", ids)
		real(dst, q, b, ids)
	}
}

// tracingNeighborSource records each neighbor run on the same timeline.
type tracingNeighborSource struct {
	inner neighborSource
	tr    *batchTrace
}

func (c *tracingNeighborSource) nodeCount() int { return c.inner.nodeCount() }

func (c *tracingNeighborSource) neighborsAt(id uint32, layer int) []uint32 {
	run := c.inner.neighborsAt(id, layer)
	c.tr.record("run", run)
	return run
}

// assertOneScorePerRun walks the timeline and requires that no neighbor run is
// followed by more than one scoring call — the exact property "batched" means.
func assertOneScorePerRun(t *testing.T, tr *batchTrace) {
	t.Helper()
	scores, sawScore := 0, false
	for _, e := range tr.events {
		if e.kind == "run" {
			scores = 0
			continue
		}
		sawScore = true
		scores++
		require.LessOrEqual(t, scores, 1,
			"a single neighbor run produced %d scoring calls — the run was split instead of batched", scores)
	}
	require.True(t, sawScore, "control: the expansion must score through the batched seam at all")
}

// assertEachIDScoredOnce requires no id to be scored twice anywhere in the
// search — the property pass 1's visited-marking exists to guarantee.
func assertEachIDScoredOnce(t *testing.T, tr *batchTrace) {
	t.Helper()
	seen := map[uint32]int{}
	for _, e := range tr.events {
		if e.kind != "score" {
			continue
		}
		require.NotEmpty(t, e.ids, "a scoring call was issued for an empty run")
		for _, id := range e.ids {
			seen[id]++
		}
	}
	for id, n := range seen {
		require.Equal(t, 1, n,
			"id %d was scored %d times; pass 1 marks visited before pass 2 precisely so this cannot happen", id, n)
	}
}

// TestSearchLayerBatchesThroughGather proves the expansion scores a whole
// neighbor run in ONE batched call rather than one candidate at a time.
//
// PROVEN ABLE TO FAIL: with scoreCollected temporarily rewritten to issue one
// call per id, assertOneScorePerRun fails with "a single neighbor run produced 2
// scoring calls". That experiment is what caught this test's first draft being
// vacuous.
func TestSearchLayerBatchesThroughGather(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		dtype byte
	}{
		{"float32", dtypeFloat32},
		{"ubinary", dtypeUbinary},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			const dim = 32
			items := float32Items(64, dim)
			blob, err := encodeGraphV3(
				buildBinaryHNSWSerialDeterministic(items, dim*4, tc.dtype, defaultM, defaultEfConstruction))
			require.NoError(t, err)
			g, err := openGraphV3(blob)
			require.NoError(t, err)

			tr := &batchTrace{}
			tr.installProbe(&g.vectorBlock)
			ns := &tracingNeighborSource{inner: g, tr: tr}

			q := g.prepareQuery(items[0].vec)
			out := searchLayer(&g.vectorBlock, ns, &q, []uint32{g.entryPoint}, len(items), 0)
			require.Positive(t, out.Len(), "control: the search must actually find something")

			assertOneScorePerRun(t, tr)
			assertEachIDScoredOnce(t, tr)
		})
	}

	// A run whose length is not a multiple of four exercises the fused kernel's
	// TAIL, which scores the leftover rows one at a time inside the kernel. It is
	// the path where a batching bug drops or duplicates the last candidates, and
	// veckernel's own comment records that this remainder fires on real
	// traversals constantly.
	//
	// THE RUN LENGTH IS FORCED, NOT HOPED FOR: with 8 nodes and mMax0 = 64 every
	// node links to all seven others, so the first expansion collects 6 or 7 ids
	// rather than depending on how a random graph happened to wire up.
	//
	// The length is required to be BOTH not-a-multiple-of-four AND greater than
	// four, which is what makes it a genuine multi-group run with a remainder.
	// Without the second half, an implementation that split every run into calls
	// of one would satisfy "not a multiple of four" trivially — measured, not
	// supposed: that is how the split implementation passed the first draft.
	t.Run("run length not a multiple of four", func(t *testing.T) {
		t.Parallel()

		const dim = 32
		items := float32Items(8, dim)
		blob, err := encodeGraphV3(
			buildBinaryHNSWSerialDeterministic(items, dim*4, dtypeFloat32, defaultM, defaultEfConstruction))
		require.NoError(t, err)
		g, err := openGraphV3(blob)
		require.NoError(t, err)

		tr := &batchTrace{}
		tr.installProbe(&g.vectorBlock)
		ns := &tracingNeighborSource{inner: g, tr: tr}

		q := g.prepareQuery(items[0].vec)
		out := searchLayer(&g.vectorBlock, ns, &q, []uint32{g.entryPoint}, len(items), 0)
		require.Positive(t, out.Len())

		tail := false
		for _, e := range tr.events {
			if e.kind == "score" && len(e.ids)%4 != 0 && len(e.ids) > 4 {
				tail = true
			}
		}
		require.True(t, tail,
			"no scored run was a multi-group run with a remainder, so the kernel's tail path was never exercised; trace=%v", tr.events)

		assertOneScorePerRun(t, tr)
		assertEachIDScoredOnce(t, tr)
	})
}

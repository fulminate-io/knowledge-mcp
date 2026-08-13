// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pass_reads_equivalence_test.go is the SAME-ARTIFACTS gate for the per-pass read
// memo: the reflect output is identical whether each stage composes its own
// full-corpus reads (the loop as the source, i.e. today's behavior) or every stage
// shares ONE composition through passReads. Fewer reads is the point; producing the
// same artifacts is the requirement, and only this pair pins the second.
//
// It reuses the corpus_equivalence_test.go harness verbatim — the reflectEquivFake
// wire double, defaultEquivSpec, computeEquivSurface, canonicalizeSurface, equivLoop
// and cacheFromNodes — with the ONE parameterisation that harness gained: the src it
// threads. Nothing about the fixture, the canonicalisation or the compared surface
// is duplicated here.

// surfaceFromLoop threads the loop as BOTH the harness loop and its corpus source —
// the shape the two pre-existing cache-equivalence tests use. It exists so those
// tests keep their one-line call after computeEquivSurface gained an explicit src,
// and so corpus_equivalence_test.go (at its 500-line cap) did not have to grow.
func surfaceFromLoop(t *testing.T, gc *reflectEquivFake, p *PropagationLoop) equivSurface {
	t.Helper()
	return computeEquivSurface(t, gc, p, p)
}

// TestPassReadsEquivalence_MemoEqualsFresh is the load-bearing gate: a pass whose
// stages share one passReads memo produces byte-identical clusters, adjacency,
// propagated valence/magnitude, topic labels and blind-spot report to a pass that
// re-reads at every stage. Both runs read the SAME corpus from the SAME resident
// cache; the ONLY variable is whether the reads are memoized.
func TestPassReadsEquivalence_MemoEqualsFresh(t *testing.T) {
	spec := defaultEquivSpec()

	// FRESH run: the loop is the source, so every stage composes its own reads.
	gcFresh := newReflectEquivFake(spec)
	loopFresh := equivLoop(gcFresh, cacheFromNodes(gcFresh.corpusNodesSlice()))
	oFresh := surfaceFromLoop(t, gcFresh, loopFresh)

	// MEMOIZED run: same corpus, the per-pass memo as the source — a distinct fake
	// instance so the fresh run's writeback cannot leak into it.
	gcMemo := newReflectEquivFake(spec)
	loopMemo := equivLoop(gcMemo, cacheFromNodes(gcMemo.corpusNodesSlice()))
	oMemo := computeEquivSurface(t, gcMemo, loopMemo, newPassReads(loopMemo))

	// The gate is only meaningful if the surface is non-trivial: real clusters, real
	// propagated values, a non-empty blind-spot report and the topic-label override.
	require.Len(t, oFresh.clusters, 2, "two persisted clusters in the corpus")
	require.NotEmpty(t, oFresh.valence, "propagation produced valence rows")
	require.True(t, hasBlindSpotItems(oFresh.blindSpots), "blind-spot report is non-empty")
	require.Equal(t, "Topic Alpha", labelOfCluster(oFresh.clusters, "cA"),
		"topic doc overrode cluster cA's label")

	require.Equal(t, oFresh, oMemo,
		"the memoized pass must produce the same artifacts as the re-reading pass — "+
			"the memo changes how many times a pass READS, never what it sees")
}

// TestPassReadsEquivalence_PoisonedMemoGoesRed PROVES the gate above can FAIL, and
// in doing so proves the memo is genuinely CONSULTED. A pre-seeded adjacency entry
// with one edge removed — the shape of a memo that served a stale or partial read —
// diverges the surface. If this divergence did NOT show up, either the memo is never
// read (so the gate above compares a pass to itself) or the surface does not depend
// on the memoized value; both make the gate worthless.
func TestPassReadsEquivalence_PoisonedMemoGoesRed(t *testing.T) {
	spec := defaultEquivSpec()
	ctx := context.Background()

	gcFresh := newReflectEquivFake(spec)
	loopFresh := equivLoop(gcFresh, cacheFromNodes(gcFresh.corpusNodesSlice()))
	oFresh := surfaceFromLoop(t, gcFresh, loopFresh)

	gcMemo := newReflectEquivFake(spec)
	loopMemo := equivLoop(gcMemo, cacheFromNodes(gcMemo.corpusNodesSlice()))

	// Build the TRUE adjacency, then poison a copy by cutting the a1<->a2 edge from
	// both endpoints, and pre-seed it as the memo's answer.
	nodeIDs, adj, err := fetchAdjacencyAllUncached(ctx, gcMemo, loopMemo)
	require.NoError(t, err)
	require.Contains(t, adj["a1"], "a2", "fixture sanity: a1 and a2 are adjacent before poisoning")

	poisoned := make(map[string][]string, len(adj))
	for id, neighbors := range adj {
		poisoned[id] = dropNeighbor(neighbors, map[string]string{"a1": "a2", "a2": "a1"}[id])
	}
	pr := newPassReads(loopMemo)
	pr.adjNodeIDs, pr.adj, pr.adjBuilt = nodeIDs, poisoned, true

	oMemo := computeEquivSurface(t, gcMemo, loopMemo, pr)

	assert.NotEqual(t, oFresh, oMemo,
		"a memo serving a cut edge MUST diverge the surface — proves the equivalence "+
			"gate above is not trivially green and that the memo is actually consulted")
	assert.NotEqual(t, oFresh.adjacency, oMemo.adjacency,
		"the cut a1<->a2 edge shows up in the compared adjacency")
}

// dropNeighbor returns neighbors with every occurrence of victim removed. An empty
// victim (a node the poison map does not name) returns the list unchanged.
func dropNeighbor(neighbors []string, victim string) []string {
	if victim == "" {
		return neighbors
	}
	out := make([]string, 0, len(neighbors))
	for _, n := range neighbors {
		if n != victim {
			out = append(out, n)
		}
	}
	return out
}

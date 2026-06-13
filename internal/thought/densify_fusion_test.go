// SPDX-License-Identifier: Apache-2.0

package thought

// densify_fusion_test.go — the capstone end-to-end fusion test. It proves
// the ticket's central claim: WITHIN a topic, kNN densify edges are dense enough to
// fuse disjoint structural components AND to merge two settled Leiden communities
// under CPM (where a single bridge would NOT — the contrast with the single-
// bridge case). Modeled DIRECTLY on TestLeidenRehydrate_BridgingEdgeJoinsViaIncremental
// (loop_rehydrate_test.go) and TestDirtyComponentClosure_BridgingEdgeFusesComponents
// (closure_test.go). The ReflectDirtyGen edge-bump consequence is covered structurally
// by the existing TestReflectDirtyGen_EdgeBump (composite_db_reflect_gen_test.go) — not
// re-proven here (store territory).

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/graph"
)

// TestDensifyFusion (FAILS-WHEN-ABSENT) covers both legs:
//
//	STRUCTURAL — a topic whose members start in 2 disjoint relates-to components
//	collapses to 1 after the densify kNN edges (findConnectedComponents).
//	LEIDEN — a rehydrated two-community LeidenState, fed the densify edges as the
//	set-diff delta, collapses to <2 communities (UpdateIncremental), matching a full
//	RunLeiden over the same bridged adjacency. Fails if densify edges are too sparse
//	to fuse (e.g. k=1 single bridges would not merge under CPM).
func TestDensifyFusion(t *testing.T) {
	const gamma = 0.5

	// Two clusters of near-duplicate member thoughts. Within a cluster the vectors are
	// near-identical; across clusters they are STILL above the densify threshold (the
	// whole topic is semantically coherent), so kNN selects dense cross-cluster edges.
	members := []string{"a1", "a2", "a3", "b1", "b2", "b3"}
	base := bitVec(0, 1, 2, 3, 4, 5, 6, 7)
	vectorIndex := map[string][]byte{
		"a1": vecDifferingBy(base, 100),
		"a2": vecDifferingBy(base, 101),
		"a3": vecDifferingBy(base, 102),
		"b1": vecDifferingBy(base, 103),
		"b2": vecDifferingBy(base, 104),
		"b3": vecDifferingBy(base, 105),
	}
	// Every pair differs by exactly 2 bits → sim 254/256≈0.992, all above 0.95.

	aNodes := []string{"a1", "a2", "a3"}
	bNodes := []string{"b1", "b2", "b3"}

	// BEFORE: two internally-connected triangles, NO cross edges → 2 components.
	baselineAdj := map[string][]string{
		"a1": {"a2", "a3"}, "a2": {"a1", "a3"}, "a3": {"a1", "a2"},
		"b1": {"b2", "b3"}, "b2": {"b1", "b3"}, "b3": {"b1", "b2"},
	}
	require.Len(t, findConnectedComponents(members, baselineAdj), 2,
		"precondition: two disjoint relates-to components before densification")

	// Run the densify selector over the topic with k=3 (enough fan-out to cross the
	// cluster boundary densely, not a single bridge).
	cands := selectTopicKNN(members, vectorIndex, 3, 0.95)
	require.NotEmpty(t, cands, "densify must select within-topic kNN candidates")

	// ---- STRUCTURAL leg: the densify edges fuse the two components into one. ----
	afterAdj := make(map[string][]string, len(baselineAdj))
	for k, v := range baselineAdj {
		afterAdj[k] = append([]string(nil), v...)
	}
	crossEdges := 0
	for _, c := range cands {
		afterAdj[c.A] = append(afterAdj[c.A], c.B)
		afterAdj[c.B] = append(afterAdj[c.B], c.A)
		if (slices.Contains(aNodes, c.A) && slices.Contains(bNodes, c.B)) || (slices.Contains(bNodes, c.A) && slices.Contains(aNodes, c.B)) {
			crossEdges++
		}
	}
	require.Greater(t, crossEdges, 1,
		"the kNN densify edges must form a DENSE cross-cluster set (not a single bridge)")
	require.Len(t, findConnectedComponents(members, afterAdj), 1,
		"the densify edges must fuse the two structural components into one")

	// ---- LEIDEN leg: rehydrate 2 communities, feed the densify delta, expect <2. ----
	partition := map[string]string{
		"a1": "cA", "a2": "cA", "a3": "cA",
		"b1": "cB", "b2": "cB", "b3": "cB",
	}
	state := graph.RehydrateLeidenState(partition, baselineAdj, gamma)
	require.Equal(t, 2, communityCountOf(state.CommunityOf),
		"two settled clusters must rehydrate into exactly 2 communities")

	delta := graph.ComputeEdgeChanges(baselineAdj, afterAdj)
	require.NotEmpty(t, delta, "the densify edges must surface as added edges in the set-diff")
	merged := state.UpdateIncremental(delta, afterAdj)
	assert.Less(t, communityCountOf(merged), 2,
		"the dense kNN densify edges must merge the two communities under CPM (single bridges would not)")

	// EQUIVALENCE: a full RunLeiden over the densified adjacency yields the same merge.
	full := graph.RunLeiden(members, afterAdj, gamma)
	assert.Equal(t, full["a1"], full["b1"],
		"a full pass over the densified adjacency must also co-cluster the two clusters")
}

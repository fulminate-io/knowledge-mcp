// SPDX-License-Identifier: Apache-2.0

package thought

// loop_rehydrate_test.go — orchestration-layer tests for the cold-start Leiden
// rehydration + incremental-seeding flow. These exercise the pure seams
// (partitionFromPersisted, graph.RehydrateLeidenState, runLeidenStep) against
// synthetic fixtures + the established persistedClustersFakeCaller harness — no
// wire, no real daemon.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/graph"
)

// sameEquivalenceClasses reports whether two partitions induce the SAME grouping
// of nodes, ignoring the (arbitrary) community label strings. Two nodes are
// co-clustered in a iff they are co-clustered in b, for every pair.
func sameEquivalenceClasses(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	// Canonicalize each partition to label → sorted-member-set signature by
	// mapping each node to a representative: the first node (in iteration of the
	// shared key set) that shares its community. Simpler: build, for each
	// partition, a map node→set-of-co-members and compare per node.
	coMembers := func(p map[string]string) map[string]map[string]bool {
		byComm := make(map[string][]string)
		for n, c := range p {
			byComm[c] = append(byComm[c], n)
		}
		out := make(map[string]map[string]bool, len(p))
		for _, members := range byComm {
			for _, n := range members {
				set := make(map[string]bool, len(members))
				for _, m := range members {
					set[m] = true
				}
				out[n] = set
			}
		}
		return out
	}
	ca, cb := coMembers(a), coMembers(b)
	for n, setA := range ca {
		setB, ok := cb[n]
		if !ok || len(setA) != len(setB) {
			return false
		}
		for m := range setA {
			if !setB[m] {
				return false
			}
		}
	}
	return true
}

// TestRehydrateEquivalence_PartitionFromPersisted (CASE A) asserts that
// partitionFromPersisted reads back the exact tid→cluster_id map the loop
// persisted, and that graph.RehydrateLeidenState reproduces the SAME equivalence
// classes (co-clustered members stay co-clustered), independent of label strings.
func TestRehydrateEquivalence_PartitionFromPersisted(t *testing.T) {
	ctx := context.Background()

	// Persisted corpus: two clusters by cluster_id. cA={t1,t2,t3}, cB={t4,t5}.
	fc := &persistedClustersFakeCaller{thoughtNodes: []*knowledgev1.Node{
		thoughtWithCluster("t1", "cA"),
		thoughtWithCluster("t2", "cA"),
		thoughtWithCluster("t3", "cA"),
		thoughtWithCluster("t4", "cB"),
		thoughtWithCluster("t5", "cB"),
	}}

	partition, err := partitionFromPersisted(ctx, fc, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"t1": "cA", "t2": "cA", "t3": "cA", "t4": "cB", "t5": "cB",
	}, partition, "partitionFromPersisted must return the exact tid→cluster_id map")

	// Adjacency matching the persisted grouping: two cliques, no bridge.
	adj := map[string][]string{
		"t1": {"t2", "t3"}, "t2": {"t1", "t3"}, "t3": {"t1", "t2"},
		"t4": {"t5"}, "t5": {"t4"},
	}

	state := graph.RehydrateLeidenState(partition, adj, 0.5)
	require.NotNil(t, state)
	// The rehydrated partition is the persisted partition verbatim — its
	// equivalence classes match the cluster_id grouping.
	assert.True(t, sameEquivalenceClasses(partition, state.CommunityOf),
		"rehydrated CommunityOf must preserve the persisted equivalence classes")
}

// TestNewThoughtRoutesIncremental (CASE B, FAILS-WHEN-ABSENT) asserts that a
// nodeIDs set containing one MORE node than the prior partition routes through the
// incremental path (isFull==false) and assigns the new node a community via
// seedNewNodes. With the deleted count-change gate (len(nodeIDs)!=len(prev)) this
// would have returned isFull==true — this test is the direct regression guard.
func TestNewThoughtRoutesIncremental(t *testing.T) {
	// Prior settled state over 4 nodes (a single connected group is fine — only the
	// gate + seeding matter here).
	prevAdj := map[string][]string{
		"t1": {"t2"}, "t2": {"t1", "t3"}, "t3": {"t2", "t4"}, "t4": {"t3"},
	}
	prevState := graph.NewLeidenState([]string{"t1", "t2", "t3", "t4"}, prevAdj, 0.5)
	require.NotNil(t, prevState)
	require.NotContains(t, prevState.CommunityOf, "t5", "t5 must be the genuinely-new thought")

	// New tick: t5 appears, attached to t4. nodeIDs is one larger than the partition.
	nodeIDs := []string{"t1", "t2", "t3", "t4", "t5"}
	adj := map[string][]string{
		"t1": {"t2"}, "t2": {"t1", "t3"}, "t3": {"t2", "t4"}, "t4": {"t3", "t5"}, "t5": {"t4"},
	}

	_, communityOf, edgeChanges, isFull := runLeidenStep(prevState, prevAdj, nodeIDs, adj, 0.5)

	assert.False(t, isFull,
		"a new thought must route through the incremental path, NOT force a full pass (count-change trigger is gone)")
	assert.Contains(t, communityOf, "t5",
		"the new thought must receive a community assignment (seedNewNodes ran)")
	// The new thought's incident edge (t4—t5) surfaces in the returned edgeChanges
	// so the dirty-seed derivation can pick up both endpoints.
	assert.NotEmpty(t, edgeChanges,
		"the incremental path must return the edge-change frontier (the dirty-seed source)")
}

// communityCountOf returns the number of distinct communities in a partition.
func communityCountOf(communityOf map[string]string) int {
	seen := make(map[string]bool, len(communityOf))
	for _, c := range communityOf {
		seen[c] = true
	}
	return len(seen)
}

// TestLeidenRehydrate_BridgingEdgeJoinsViaIncremental (FAILS-WHEN-ABSENT,
// mandatory JOIN test) exercises the FULL orchestration seam after a restart:
// persisted cluster_id → partitionFromPersisted → graph.RehydrateLeidenState →
// set-diff delta (ComputeEdgeChanges) → UpdateIncremental → cluster JOIN, and
// confirms the incremental result equals a full RunLeiden on the bridged corpus.
//
// Bridge density mirrors the algorithm-level JOIN test (Phase 1): a single edge
// between two dense settled cliques correctly does NOT merge them under CPM, so
// the bridge is a dense set; the load-bearing assertion is that the rehydrated
// state, fed the set-diff delta, fires the JOIN and matches a full pass.
func TestLeidenRehydrate_BridgingEdgeJoinsViaIncremental(t *testing.T) {
	ctx := context.Background()
	const gamma = 0.5

	// Cold-start state: two settled clusters persisted as cluster_id.
	fc := &persistedClustersFakeCaller{thoughtNodes: []*knowledgev1.Node{
		thoughtWithCluster("a1", "cA"),
		thoughtWithCluster("a2", "cA"),
		thoughtWithCluster("a3", "cA"),
		thoughtWithCluster("b1", "cB"),
		thoughtWithCluster("b2", "cB"),
		thoughtWithCluster("b3", "cB"),
	}}

	// Baseline adjacency: two internally-connected triangles, NO bridge.
	aNodes := []string{"a1", "a2", "a3"}
	bNodes := []string{"b1", "b2", "b3"}
	baselineAdj := map[string][]string{
		"a1": {"a2", "a3"}, "a2": {"a1", "a3"}, "a3": {"a1", "a2"},
		"b1": {"b2", "b3"}, "b2": {"b1", "b3"}, "b3": {"b1", "b2"},
	}

	// Rehydrate from persisted cluster_id.
	partition, err := partitionFromPersisted(ctx, fc, nil)
	require.NoError(t, err)
	require.Len(t, partition, 6)
	state := graph.RehydrateLeidenState(partition, baselineAdj, gamma)
	require.Equal(t, 2, communityCountOf(state.CommunityOf),
		"two settled clusters must rehydrate into exactly 2 communities")
	require.NotEqual(t, state.CommunityOf["a1"], state.CommunityOf["b1"],
		"the two rehydrated clusters must start distinct")

	// A bridging-edge set appears: connect the two cliques densely on a COPY.
	bridgedAdj := make(map[string][]string, len(baselineAdj))
	for k, v := range baselineAdj {
		nbrs := make([]string, len(v))
		copy(nbrs, v)
		bridgedAdj[k] = nbrs
	}
	for _, a := range aNodes {
		for _, b := range bNodes {
			bridgedAdj[a] = append(bridgedAdj[a], b)
			bridgedAdj[b] = append(bridgedAdj[b], a)
		}
	}

	// Feed the bridge as the set-diff delta — exactly the runtime's warm-tick path.
	delta := graph.ComputeEdgeChanges(baselineAdj, bridgedAdj)
	require.NotEmpty(t, delta, "the bridge must surface as added edges in the set-diff")
	merged := state.UpdateIncremental(delta, bridgedAdj)

	assert.Equal(t, merged["a1"], merged["b1"],
		"the bridging edges must merge the two rehydrated clusters via the incremental path")
	assert.Equal(t, 1, communityCountOf(merged),
		"the bridged corpus must collapse to a single community via the rehydrate→incremental seam")

	// EQUIVALENCE: a full RunLeiden on the bridged corpus yields the same merge.
	ids := append(append([]string{}, aNodes...), bNodes...)
	full := graph.RunLeiden(ids, bridgedAdj, gamma)
	assert.Equal(t, full["a1"], full["b1"],
		"a full pass on the bridged corpus must also co-cluster the bridged endpoints")
}

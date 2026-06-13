// SPDX-License-Identifier: Apache-2.0

package graph

// leiden_test.go — Pure-algorithm tests for the static and incremental
// Leiden paths. These tests build adjacency maps directly (no wire, no
// fixture) and exercise RunLeiden / NewLeidenState / UpdateIncremental
// against synthetic graphs with known community structure.

import (
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// twoCliqueGraph builds an adjacency map with two fully-connected groups of n
// nodes each, connected by a single bridge edge between the two groups.
func twoCliqueGraph(n int) ([]string, map[string][]string) {
	total := n * 2
	ids := make([]string, total)
	for i := range total {
		ids[i] = stableCommID(i)
	}

	adj := make(map[string][]string, total)
	for i := range total {
		adj[ids[i]] = nil
	}

	// Fully connect each clique.
	for i := range n {
		for j := range n {
			if i != j {
				adj[ids[i]] = append(adj[ids[i]], ids[j])
				adj[ids[n+i]] = append(adj[ids[n+i]], ids[n+j])
			}
		}
	}

	// Single bridge edge between the two cliques.
	adj[ids[0]] = append(adj[ids[0]], ids[n])
	adj[ids[n]] = append(adj[ids[n]], ids[0])

	return ids, adj
}

// twoCliqueGraphNoBridge builds two fully-connected cliques of n nodes each with
// NO edge between them — two settled, separate communities. Sibling of
// twoCliqueGraph minus the bridge edges (L43-44 of twoCliqueGraph), used as the
// "two settled clusters" precondition for the bridging-edge JOIN test. Returns
// the node IDs, the adjacency, and the two representative endpoints (one per
// clique) a JOIN test can later bridge.
func twoCliqueGraphNoBridge(n int) (ids []string, adj map[string][]string, aNode, bNode string) {
	total := n * 2
	ids = make([]string, total)
	for i := range total {
		ids[i] = stableCommID(i)
	}

	adj = make(map[string][]string, total)
	for i := range total {
		adj[ids[i]] = nil
	}

	// Fully connect each clique; NO bridge between them.
	for i := range n {
		for j := range n {
			if i != j {
				adj[ids[i]] = append(adj[ids[i]], ids[j])
				adj[ids[n+i]] = append(adj[ids[n+i]], ids[n+j])
			}
		}
	}

	return ids, adj, ids[0], ids[n]
}

// communityCount returns the number of distinct communities in a communityOf map.
func communityCount(communityOf map[string]string) int {
	seen := make(map[string]bool)
	for _, c := range communityOf {
		seen[c] = true
	}
	return len(seen)
}

// TestRunLeiden_TwoClique verifies that a two-clique graph (two groups of 5
// fully-connected nodes, one edge between them) produces exactly 2 communities.
func TestRunLeiden_TwoClique(t *testing.T) {
	ids, adj := twoCliqueGraph(5)

	result := RunLeiden(ids, adj, 0.5)
	require.Len(t, result, len(ids), "every node must have a community assignment")

	numComms := communityCount(result)
	assert.Equal(t, 2, numComms, "two-clique graph should separate into exactly 2 communities")

	// Verify the two cliques end up in different communities.
	group1Comm := result[ids[0]]
	group2Comm := result[ids[5]]
	assert.NotEqual(t, group1Comm, group2Comm, "the two cliques must be in different communities")

	// All nodes in each clique should share the same community.
	for i := 1; i < 5; i++ {
		assert.Equal(t, group1Comm, result[ids[i]], "node %d should be in group1 community", i)
		assert.Equal(t, group2Comm, result[ids[5+i]], "node %d should be in group2 community", 5+i)
	}
}

// TestLeidenLabelScheme_MinMemberFullEqualsIncremental locks the canonical
// min-member-nodeID label scheme shared by the full (renumberIntToMap) and
// incremental (renumber) paths. It runs ONE partition through both paths and
// asserts (1) the two node→label maps are IDENTICAL (no full-vs-incremental
// sort-key divergence) and (2) every label equals its community's
// lexicographically smallest member nodeID. Fails if either path reverts to a
// divergent sort key (e.g. stableCommID over representative- vs prior-label
// order).
func TestLeidenLabelScheme_MinMemberFullEqualsIncremental(t *testing.T) {
	const gamma = 0.5
	// Two settled cliques of 5: ids "0".."4" and "5".."9". Min member per clique
	// is "0" and "5" respectively.
	ids, adj := twoCliqueGraph(5)

	// FULL path: NewLeidenState runs runLeidenFull → renumberIntToMap.
	fullState := NewLeidenState(ids, adj, gamma)
	full := fullState.CommunityOf

	// INCREMENTAL path: rehydrate the SAME partition (as raw equivalence classes)
	// then drive one UpdateIncremental whose set-diff is empty BUT which still runs
	// renumber via a no-op affected seed, so the labels are assigned by renumber.
	// Build the incremental partition from the full grouping but with DELIBERATELY
	// non-canonical seed labels, so only a correct min-member renumber can converge
	// to the same labels as the full path.
	seeded := make(map[string]string, len(full))
	for n, c := range full {
		seeded[n] = "seed-" + c // arbitrary non-min label to force a real relabel
	}
	incState := RehydrateLeidenState(seeded, adj, gamma)
	// A single self-edge change re-seeds every node in both cliques as affected so
	// renumber relabels the whole partition to the canonical scheme.
	var changes []EdgeChange
	for _, id := range ids {
		changes = append(changes, EdgeChange{From: id, To: id, Removed: false})
	}
	incremental := incState.UpdateIncremental(changes, adj)

	// (1) Identical maps across the two paths.
	assert.Equal(t, full, incremental,
		"full (renumberIntToMap) and incremental (renumber) must assign IDENTICAL labels to the same partition")

	// (2) Every label equals its community's min member nodeID.
	for partName, part := range map[string]map[string]string{"full": full, "incremental": incremental} {
		membersByLabel := make(map[string][]string)
		for n, c := range part {
			membersByLabel[c] = append(membersByLabel[c], n)
		}
		for label, members := range membersByLabel {
			assert.Equal(t, minMemberLabel(members), label,
				"%s: community label %q must equal its min member nodeID", partName, label)
		}
	}

	// Concretely: clique {0..4} labels as "0", clique {5..9} labels as "5".
	assert.Equal(t, "0", full[ids[0]], "clique-1 label is its min member nodeID")
	assert.Equal(t, "5", full[ids[5]], "clique-2 label is its min member nodeID")
}

// TestRunLeiden_SingleNode verifies that a single-node graph returns 1 community.
func TestRunLeiden_SingleNode(t *testing.T) {
	ids := []string{"only"}
	adj := map[string][]string{"only": nil}

	result := RunLeiden(ids, adj, 0.5)
	require.Len(t, result, 1)
	assert.Equal(t, 1, communityCount(result))
}

// TestRunLeiden_Empty verifies that an empty graph returns an empty map.
func TestRunLeiden_Empty(t *testing.T) {
	result := RunLeiden(nil, nil, 0.5)
	assert.Empty(t, result)
}

// TestRunLeiden_ConnectedGraph verifies that Leiden produces fewer communities
// than nodes on a connected graph (i.e., it actually merges nodes).
func TestRunLeiden_ConnectedGraph(t *testing.T) {
	// Path graph: 0-1-2-3-4-5-6-7-8-9
	n := 10
	ids := make([]string, n)
	for i := range n {
		ids[i] = stableCommID(i)
	}
	adj := make(map[string][]string, n)
	for i := range n {
		adj[ids[i]] = nil
	}
	for i := 0; i < n-1; i++ {
		adj[ids[i]] = append(adj[ids[i]], ids[i+1])
		adj[ids[i+1]] = append(adj[ids[i+1]], ids[i])
	}

	result := RunLeiden(ids, adj, 0.5)
	require.Len(t, result, n)

	numComms := communityCount(result)
	assert.Less(t, numComms, n, "Leiden should merge some nodes on a connected graph")
}

// TestRunLeiden_TwoIsolated verifies that two isolated nodes each form their own community.
func TestRunLeiden_TwoIsolated(t *testing.T) {
	ids := []string{"a", "b"}
	adj := map[string][]string{"a": nil, "b": nil}

	result := RunLeiden(ids, adj, 0.5)
	require.Len(t, result, 2)
	assert.Equal(t, 2, communityCount(result), "two isolated nodes should each be their own community")
}

// TestLeidenState_NewAndIncremental verifies that LeidenState can be created and
// then updated incrementally when an edge is added.
func TestLeidenState_NewAndIncremental(t *testing.T) {
	ids, adj := twoCliqueGraph(4)

	state := NewLeidenState(ids, adj, 0.5)
	require.NotNil(t, state)
	require.Len(t, state.CommunityOf, len(ids))

	initialComms := communityCount(state.CommunityOf)
	assert.GreaterOrEqual(t, initialComms, 1)

	// Add an extra internal edge within clique 1 (already fully connected so this
	// is a no-op edge structurally, but tests the incremental path).
	changes := []EdgeChange{
		{From: ids[0], To: ids[1], Removed: false},
	}
	result := state.UpdateIncremental(changes, adj)
	require.Len(t, result, len(ids))
}

// TestLeidenState_EmptyChanges verifies that UpdateIncremental with no changes
// returns the current partition unchanged.
func TestLeidenState_EmptyChanges(t *testing.T) {
	ids, adj := twoCliqueGraph(3)
	state := NewLeidenState(ids, adj, 0.5)

	before := make(map[string]string, len(state.CommunityOf))
	maps.Copy(before, state.CommunityOf)

	result := state.UpdateIncremental(nil, adj)
	assert.Equal(t, before, result)
}

// TestRunLeiden_GammaEffect verifies that a higher gamma produces at least as
// many communities as a lower gamma (tighter resolution → smaller communities).
func TestRunLeiden_GammaEffect(t *testing.T) {
	ids, adj := twoCliqueGraph(5)

	lowGamma := RunLeiden(ids, adj, 0.1)
	highGamma := RunLeiden(ids, adj, 2.0)

	nLow := communityCount(lowGamma)
	nHigh := communityCount(highGamma)
	assert.GreaterOrEqual(t, nHigh, nLow,
		"higher gamma should produce at least as many communities as lower gamma")
}

// TestRunLeiden_StablePartition verifies that running Leiden twice on the same
// graph produces the same partition (deterministic output).
func TestRunLeiden_StablePartition(t *testing.T) {
	ids, adj := twoCliqueGraph(5)

	r1 := RunLeiden(ids, adj, 0.5)
	r2 := RunLeiden(ids, adj, 0.5)

	for _, id := range ids {
		assert.Equal(t, r1[id], r2[id], "partition should be deterministic for node %s", id)
	}
}

// TestRunLeiden_LargeCliqueSmallGamma verifies that a single large clique with a
// low gamma value results in a single community.
func TestRunLeiden_LargeCliqueSmallGamma(t *testing.T) {
	n := 10
	ids := make([]string, n)
	for i := range n {
		ids[i] = stableCommID(i)
	}
	adj := make(map[string][]string, n)
	for i := range n {
		for j := range n {
			if i != j {
				adj[ids[i]] = append(adj[ids[i]], ids[j])
			}
		}
	}

	result := RunLeiden(ids, adj, 0.01)
	require.Len(t, result, n)

	// With very low gamma (almost no penalty for large communities), a fully
	// connected clique should collapse to 1 community.
	assert.Equal(t, 1, communityCount(result),
		"fully connected clique with low gamma should form a single community")
}

// TestLeidenState_BridgingEdgeJoinsClusters is the algorithm-layer half of the
// mandatory cluster-JOIN test: two SETTLED, separate cliques (no bridge) start as
// two communities; feeding the bridging EdgeChanges to UpdateIncremental merges
// them; and RunLeiden on the same bridged corpus produces the same merge —
// proving the incremental Dynamic Frontier path is equivalent to a full pass on
// the joined graph.
//
// Bridge density note: under the CPM objective (bestMove gain
// (eC - gamma*sizeB) - (eCur - gamma*(sizeA-1))), a SINGLE bridge edge between two
// dense cliques correctly does NOT merge them — that gain is negative for
// non-trivial clique sizes (cf. TestRunLeiden_TwoClique, which asserts two
// 5-cliques WITH a single bridge stay 2 communities). A genuine JOIN between two
// non-trivial settled clusters therefore requires a bridge dense enough to cross
// the threshold, so this test bridges the two cliques with a set of edges and
// feeds the whole set as the EdgeChange delta. The load-bearing assertion is that
// the JOIN fires through the incremental path and matches a full pass — not the
// edge count.
func TestLeidenState_BridgingEdgeJoinsClusters(t *testing.T) {
	const gamma = 0.5
	const n = 3
	ids, adj, aNode, bNode := twoCliqueGraphNoBridge(n)

	// Precondition: two settled, separate communities.
	state := NewLeidenState(ids, adj, gamma)
	require.Len(t, state.CommunityOf, len(ids))
	require.Equal(t, 2, communityCount(state.CommunityOf),
		"two unbridged cliques must settle into exactly 2 communities")
	require.NotEqual(t, state.CommunityOf[aNode], state.CommunityOf[bNode],
		"the two clique endpoints must start in different communities")

	// Bridge the two cliques on a COPY of the adjacency, recording each new edge as
	// an EdgeChange so the bridge endpoints become Dynamic Frontier seeds.
	bridgedAdj := make(map[string][]string, len(adj))
	for k, v := range adj {
		nbrs := make([]string, len(v))
		copy(nbrs, v)
		bridgedAdj[k] = nbrs
	}
	var changes []EdgeChange
	for i := range n {
		for j := range n {
			a, b := ids[i], ids[n+j]
			bridgedAdj[a] = append(bridgedAdj[a], b)
			bridgedAdj[b] = append(bridgedAdj[b], a)
			changes = append(changes, EdgeChange{From: a, To: b, Removed: false})
		}
	}

	// JOIN via the incremental path: the bridge endpoints become DF seeds.
	result := state.UpdateIncremental(changes, bridgedAdj)
	require.Len(t, result, len(ids))
	assert.Equal(t, result[aNode], result[bNode],
		"the bridging edges must merge the two cliques into one community via the incremental path")
	assert.Equal(t, 1, communityCount(result),
		"the bridged corpus must collapse to a single community via the incremental path")

	// Equivalence leg: a full RunLeiden on the bridged corpus co-clusters the same
	// endpoints — the incremental path matches a full pass on the joined graph.
	full := RunLeiden(ids, bridgedAdj, gamma)
	require.Len(t, full, len(ids))
	assert.Equal(t, full[aNode], full[bNode],
		"a full pass on the bridged corpus must also co-cluster the bridge endpoints")
}

// TestComputeEdgeChanges_SymmetricDiff verifies the adjacency-diff helper used
// by the thought-package incremental Leiden path: edges present only in oldAdj
// are Removed, edges present only in newAdj are added, and the diff treats
// edges as undirected.
func TestComputeEdgeChanges_SymmetricDiff(t *testing.T) {
	oldAdj := map[string][]string{"a": {"b"}, "b": {"a"}}
	newAdj := map[string][]string{"a": {"b", "c"}, "b": {"a"}, "c": {"a"}}

	changes := ComputeEdgeChanges(oldAdj, newAdj)
	require.Len(t, changes, 1, "exactly one edge (a-c) was added")
	assert.False(t, changes[0].Removed, "the a-c edge is an addition")
	got := sortedStrings([]string{changes[0].From, changes[0].To})
	assert.Equal(t, []string{"a", "c"}, got, "the added edge connects a and c")
}

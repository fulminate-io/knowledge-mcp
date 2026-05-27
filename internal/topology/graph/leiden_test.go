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

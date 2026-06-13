// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedSet builds a map[string]bool seed from a list of node IDs.
func seedSet(ids ...string) map[string]bool {
	s := make(map[string]bool, len(ids))
	for _, id := range ids {
		s[id] = true
	}
	return s
}

// TestDirtyComponentClosure (FAILS-WHEN-ABSENT for the closure invariant) drives
// dirtyComponentClosure over components produced by the REAL findConnectedComponents
// so the bridging-edge fusion case is exercised end-to-end.
func TestDirtyComponentClosure(t *testing.T) {
	// Three disjoint components: {a1,a2}, {b1,b2,b3}, {c1}.
	nodeIDs := []string{"a1", "a2", "b1", "b2", "b3", "c1"}
	adj := map[string][]string{
		"a1": {"a2"}, "a2": {"a1"},
		"b1": {"b2"}, "b2": {"b1", "b3"}, "b3": {"b2"},
		"c1": nil,
	}
	components := findConnectedComponents(nodeIDs, adj)
	require.Len(t, components, 3, "fixture must yield exactly 3 disjoint components")

	t.Run("seed_in_one_component_returns_only_that_component", func(t *testing.T) {
		closure := dirtyComponentClosure(seedSet("b2"), components)
		sort.Strings(closure)
		assert.Equal(t, []string{"b1", "b2", "b3"}, closure,
			"a seed in component B returns exactly B, excluding A and C")
	})

	t.Run("seed_spanning_two_components_returns_both", func(t *testing.T) {
		closure := dirtyComponentClosure(seedSet("a1", "c1"), components)
		sort.Strings(closure)
		assert.Equal(t, []string{"a1", "a2", "c1"}, closure,
			"seeds in A and C return both whole components, excluding B")
	})

	t.Run("no_seed_returns_empty", func(t *testing.T) {
		closure := dirtyComponentClosure(seedSet("nonexistent"), components)
		assert.Empty(t, closure, "a seed touching no component returns the empty closure")
	})
}

// TestDirtyComponentClosure_BridgingEdgeFusesComponents (FAILS-WHEN-ABSENT for the
// JOIN invariant) proves the join-safe boundary: when a bridging edge fuses two
// formerly-separate components into ONE (as findConnectedComponents sees over the
// NEW adjacency), the closure of EITHER endpoint seed pulls in the whole fused
// component. Fails if the closure were computed per-old-component instead of over
// the new adjacency.
func TestDirtyComponentClosure_BridgingEdgeFusesComponents(t *testing.T) {
	nodeIDs := []string{"x1", "x2", "y1", "y2"}

	// BEFORE the bridge: two separate components {x1,x2} and {y1,y2}.
	before := map[string][]string{
		"x1": {"x2"}, "x2": {"x1"},
		"y1": {"y2"}, "y2": {"y1"},
	}
	require.Len(t, findConnectedComponents(nodeIDs, before), 2,
		"precondition: two separate components before the bridge")

	// AFTER the bridge (x2—y1): the two components fuse into ONE.
	bridged := map[string][]string{
		"x1": {"x2"}, "x2": {"x1", "y1"},
		"y1": {"y2", "x2"}, "y2": {"y1"},
	}
	fused := findConnectedComponents(nodeIDs, bridged)
	require.Len(t, fused, 1, "the bridge must fuse the two components into one")

	// Closure from EITHER endpoint seed returns the whole fused component.
	for _, seed := range []string{"x1", "y2"} {
		closure := dirtyComponentClosure(seedSet(seed), fused)
		sort.Strings(closure)
		assert.Equal(t, []string{"x1", "x2", "y1", "y2"}, closure,
			"closure from seed %q must span the whole fused (bridged) component", seed)
	}
}

// TestDiffMetadataUpdates (FAILS-WHEN-ABSENT for the diff invariant) proves the
// O(|changed|) writeback gate: only rows whose value differs from the persisted
// value survive; all-unchanged yields empty; a nil current accessor (cold case)
// yields every row.
func TestDiffMetadataUpdates(t *testing.T) {
	mkRow := func(id, key, val string) map[string]any {
		return map[string]any{"id": id, "metadata": map[string]string{key: val}}
	}

	t.Run("keeps_only_changed_rows", func(t *testing.T) {
		// Persisted: t1.cluster_id="cA", t2.cluster_id="cB", t3.cluster_id="cC".
		persisted := map[string]string{"t1": "cA", "t2": "cB", "t3": "cC"}
		current := func(id, key string) string {
			if key == "cluster_id" {
				return persisted[id]
			}
			return ""
		}
		desired := []map[string]any{
			mkRow("t1", "cluster_id", "cA"), // unchanged → dropped.
			mkRow("t2", "cluster_id", "cZ"), // changed → kept.
			mkRow("t3", "cluster_id", "cC"), // unchanged → dropped.
		}
		got := diffMetadataUpdates(desired, current)
		require.Len(t, got, 1, "only the one changed row survives")
		assert.Equal(t, "t2", got[0]["id"])
	})

	t.Run("all_unchanged_yields_empty", func(t *testing.T) {
		persisted := map[string]string{"t1": "cA", "t2": "cB"}
		current := func(id, key string) string { return persisted[id] }
		desired := []map[string]any{
			mkRow("t1", "cluster_id", "cA"),
			mkRow("t2", "cluster_id", "cB"),
		}
		got := diffMetadataUpdates(desired, current)
		assert.Empty(t, got, "all-unchanged rows produce an empty writeback (O(|changed|)=0)")
	})

	t.Run("nil_current_keeps_all_rows_cold_case", func(t *testing.T) {
		desired := []map[string]any{
			mkRow("t1", "cluster_id", "cA"),
			mkRow("t2", "cluster_id", "cB"),
		}
		got := diffMetadataUpdates(desired, nil)
		assert.Equal(t, desired, got, "a nil current accessor (cold case) keeps every row")
	})

	t.Run("multi_key_row_changed_when_any_key_differs", func(t *testing.T) {
		persisted := map[string]map[string]string{
			"t1": {"propagated_valence": "0.500000", "propagated_magnitude": "1.000000"},
		}
		current := func(id, key string) string { return persisted[id][key] }
		// valence matches, magnitude differs → row kept.
		desired := []map[string]any{{
			"id": "t1",
			"metadata": map[string]string{
				"propagated_valence":   "0.500000",
				"propagated_magnitude": "2.000000",
			},
		}}
		got := diffMetadataUpdates(desired, current)
		require.Len(t, got, 1, "a row with ANY differing key is kept")
	})
}

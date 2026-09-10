// SPDX-License-Identifier: Apache-2.0

// edge_types_version_test.go — the CLIENT half of the two-module constant
// assertion on the versioned-twin edge type.
//
// IT IS THE SAME SHAPE THE HUB EDGE TYPE USES NEXT DOOR, and for the same
// measured reason: neither module carries a census or parity test over its
// edge-type const block, so a spelling added on one side only compiles, passes
// `make test` in both modules, and first surfaces in production as a read
// refusal naming the graph's vocabulary on whichever flavor is missing it.
//
// The server half is cmd/knowledge-server/internal/store/edge_types_version_test.go
// and asserts the identical literal. Two tests, one literal; a one-sided add
// fails somewhere.

package kgtypes

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEdgeNextVersion_WireSpelling pins the client copy of the version edge type.
func TestEdgeNextVersion_WireSpelling(t *testing.T) {
	t.Parallel()

	// THE LITERAL IS WRITTEN OUT, not taken from the constant. Comparing the
	// constant to itself would pass under any spelling, including one that had
	// drifted from the server's.
	require.Equal(t, "next-version", string(EdgeNextVersion),
		"the version edge type's wire spelling is a two-module contract: "+
			"cmd/knowledge-server/internal/store.EdgeNextVersion must carry this same literal, "+
			"and nothing else in the tree compares them")

	// IT MUST NOT COLLIDE WITH THE TWO -from EDGES A LANDED PRACTICE NODE ALSO
	// CARRIES. next-version runs old → new inside the combined graph; sourced-from
	// runs node → hub; translated-from is cross-graph provenance the recipe
	// emitter no longer writes at all. Three questions, three spellings.
	assert.NotEqual(t, EdgeSourcedFrom, EdgeNextVersion,
		"next-version links two versions of one node; sourced-from links a node to its hub")
	assert.NotEqual(t, EdgeTranslatedFrom, EdgeNextVersion,
		"next-version is intra-graph versioning; translated-from was cross-graph transformer provenance")

	// THE CONTROL on both assertions above: the neighbors must still hold their
	// own spellings, so the inequalities are three distinct live constants rather
	// than one of them having been emptied.
	require.Equal(t, "sourced-from", string(EdgeSourcedFrom),
		"the hub edge must still carry its spelling, or the non-collision assertion is vacuous")
	require.Equal(t, "translated-from", string(EdgeTranslatedFrom),
		"the provenance edge must still carry its spelling, or the non-collision assertion is vacuous")
}

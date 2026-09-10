// SPDX-License-Identifier: Apache-2.0

// edge_types_hub_test.go — the CLIENT half of the two-module constant assertion
// on the practice hub edge type.
//
// WHY A HAND-WRITTEN PAIR RATHER THAN A CENSUS. The node-type vocabulary is held
// across the two modules by census twins that fail when the copies drift. The
// EDGE vocabulary has no equivalent: neither cmd/knowledge/internal/kgtypes nor
// cmd/knowledge-server/internal/store carries a census or parity test over its
// edge-type const block, so a spelling added on one side only is caught by
// nothing and first surfaces as a read refusal on one flavor — the reader is
// told the type is not in the graph's vocabulary, which is true and useless.
//
// So the two copies are pinned by an assertion in EACH module against the same
// literal, written out rather than derived. The server half is
// cmd/knowledge-server/internal/store/edge_types_hub_test.go and asserts the
// identical string. Two tests, one literal; a one-sided add fails somewhere.

package kgtypes

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEdgeSourcedFrom_WireSpelling pins the client copy of the hub edge type.
func TestEdgeSourcedFrom_WireSpelling(t *testing.T) {
	t.Parallel()

	// THE LITERAL IS WRITTEN OUT, not taken from the constant. Comparing the
	// constant to itself would pass under any spelling, including one that had
	// drifted from the server's.
	require.Equal(t, "sourced-from", string(EdgeSourcedFrom),
		"the hub edge type's wire spelling is a two-module contract: "+
			"cmd/knowledge-server/internal/store.EdgeSourcedFrom must carry this same literal, "+
			"and nothing else in the tree compares them")

	// IT MUST NOT COLLIDE WITH THE PROVENANCE EDGE IT SITS BESIDE. A migrated
	// practice node carries both — translated-from to the source it was
	// synthesized from, sourced-from to its hub — so a test or a reader treating
	// either as the other is answering a different question.
	assert.NotEqual(t, EdgeTranslatedFrom, EdgeSourcedFrom,
		"sourced-from is intra-graph hub membership; translated-from is cross-graph transformer provenance")

	// THE CONTROL on the assertion above: translated-from must still hold its own
	// spelling, so the inequality is two distinct live constants rather than one
	// of them having been emptied.
	require.Equal(t, "translated-from", string(EdgeTranslatedFrom),
		"the neighboring provenance edge must still carry its spelling, or the non-collision assertion is vacuous")
}

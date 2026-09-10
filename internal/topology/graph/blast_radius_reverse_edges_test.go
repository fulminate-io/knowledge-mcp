// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// blast_radius_reverse_edges_test.go — THE SURVIVING ARMS OF reverseEdgeTypesFor
// ARE PINNED WHILE THE CLOUD ONE GOES.
//
// WHY A REGISTRY TEST CANNOT SEE THIS. blast_radius stays REGISTERED through
// this change; what it loses is one case arm. The analyzer-absence test next
// door asserts the twelve deleted analyzers are gone from the registry, and it
// would pass unchanged if this function's code arm were deleted, if its default
// set were emptied, or if the two were swapped. The reverse edge set is what the
// BFS walks, so an arm that moves silently changes every blast score the
// analyzer produces without failing anything.
//
// THE DEFAULT ARM IS THE DELIBERATE ANSWER FOR A FAMILY THIS BINARY DOES NOT
// KNOW. The cloud arm named an edge vocabulary the built-in cloud collectors
// emitted; with those collectors gone the vocabulary has no producer to
// enumerate, so a contrib collector's graph reaches the default set. That is a
// choice rather than an oversight, and it is asserted as one: a registered
// custom family gets the knowledge set, and the row below says so.

func TestReverseEdgeTypesFor_TheCodeArmIsTheCallGraph(t *testing.T) {
	got := reverseEdgeTypesFor(kgtypes.GraphCode)
	require.NotEmpty(t, got, "the code arm returns a non-empty set")
	assert.Equal(t, []kgtypes.EdgeType{kgtypes.EdgeCalls}, got,
		"the code graph's reverse dependency edge is the call edge, and only that: widening it "+
			"changes every blast score over a code graph")
}

// TestReverseEdgeTypesFor_EveryOtherFamilyTakesTheKnowledgeSet drives the
// default arm through EACH family a caller can name, rather than through one
// representative — the arm is reached by falling through the switch, so a case
// added for any one of them would divert it without failing a single-family row.
func TestReverseEdgeTypesFor_EveryOtherFamilyTakesTheKnowledgeSet(t *testing.T) {
	want := []kgtypes.EdgeType{
		kgtypes.EdgeRelatesTo,
		kgtypes.EdgeKGContains,
		kgtypes.EdgeInformedBy,
		kgtypes.EdgeProduced,
	}

	for _, family := range []kgtypes.GraphType{
		kgtypes.GraphKnowledge,
		kgtypes.GraphPractice,
		kgtypes.GraphLinkage,
		// A REGISTERED CUSTOM FAMILY, which is what a contrib collector's graph
		// is. It reaches the default arm deliberately: this binary carries no
		// constants for a vocabulary a contrib collector emits, so enumerating a
		// per-family reverse set for it is separate work with its own evidence.
		kgtypes.GraphType("aws"),
		kgtypes.GraphType("acme-tracker"),
	} {
		assert.Equalf(t, want, reverseEdgeTypesFor(family),
			"the %q family must take the default reverse set; a case arm added for it here changes "+
				"what the blast BFS walks for that family and no other test would see it", family)
	}
}

// TestReverseEdgeTypesFor_NoArmNamesARetiredFamily is the deletion's own
// observable: the cloud arm is gone, and the way to say so without asserting
// about a constant that no longer exists is to check that the retired family
// NAMES take the default arm like any other unknown one.
func TestReverseEdgeTypesFor_NoArmNamesARetiredFamily(t *testing.T) {
	def := reverseEdgeTypesFor(kgtypes.GraphType("zzz-never-a-family"))
	for _, retired := range []string{"cloud", "logs"} {
		assert.Equalf(t, def, reverseEdgeTypesFor(kgtypes.GraphType(retired)),
			"the retired family %q must be indistinguishable from any other unknown name here. A "+
				"surviving case arm for it would keep a vocabulary alive whose producer was deleted",
			retired)
	}

	// THE CONTROL, in the same run: code is NOT indistinguishable from an unknown
	// name, so the equality above is a statement about the retired families
	// rather than about a function that returns one set for everything.
	assert.NotEqual(t, def, reverseEdgeTypesFor(kgtypes.GraphCode),
		"control: the code arm is still a distinct answer")
}

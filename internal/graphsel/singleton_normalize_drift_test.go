// SPDX-License-Identifier: Apache-2.0

package graphsel_test

// singleton_normalize_drift_test.go is the drift guard between two packages that
// cannot import each other.
//
// WHY THE FACT IS WRITTEN TWICE. graphsel owns the per-family selector policy and
// answers which families carry no instance field; workingset owns the ""→default
// collapse and is a declared LEAF, depending only on kgtypes and the standard
// library so pipeline, tools and bootstrap can all import it without a cycle.
// Asking graphsel from inside workingset would retire that leaf property for one
// predicate, so workingset enumerates the single-instance families itself. This
// test is what stops the two enumerations drifting: it lives in an EXTERNAL test
// package that can see both, so neither package's import graph changes.
//
// THE DRIFT IT CATCHES IS SILENT AND HAS ALREADY HAPPENED ONCE. The checks graph
// was declared to carry no instance field on the graphsel side while the
// normalizer still refused its empty name, so every read of it was structurally
// unable to admit the graph — the catalog loop registered no collector and its
// nodes stayed unembedded through every drain, with nothing in any log saying so.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// TestNoInstanceFieldFamiliesNormalizeTheirEmptyName walks EVERY builtin graph
// type and requires the two sides to agree.
//
// It is a walk rather than a list of the families known today, so a singleton
// added later arrives in the input set for free and lands red until both sides
// know about it — which is the whole reason the guard exists rather than a
// comment asking the next author to remember.
func TestNoInstanceFieldFamiliesNormalizeTheirEmptyName(t *testing.T) {
	t.Parallel()

	names := kgtypes.BuiltinGraphTypeNames()
	require.NotEmpty(t, names, "the builtin enumeration is empty — this walk would be vacuous")

	var sawNoInstanceField bool
	for _, name := range names {
		gt := kgtypes.GraphType(name)
		if graphsel.InstanceField(gt) != graphsel.FieldNone {
			continue
		}
		sawNoInstanceField = true
		t.Run(name, func(t *testing.T) {
			ref, ok := workingset.Normalize(gt, "")
			assert.True(t, ok,
				"graphsel declares %q carries no instance field, so its empty name IS its one instance — "+
					"workingset.Normalize must not refuse it, or every read of that graph is structurally "+
					"unable to admit it", name)
			assert.Equal(t, "default", ref.Name,
				"a single-instance family's empty name must collapse to the default instance, "+
					"so the spelling a reader sends and the spelling the catalog loop asks for are ONE member")
		})
	}

	// KNOWN-POSITIVE CONTROL on the walk itself. Every assertion above lives
	// inside a filtered loop, so a filter that matched nothing — a renamed Field
	// constant, an enumeration that stopped returning builtins — would report a
	// clean pass having tested nothing at all.
	require.True(t, sawNoInstanceField,
		"no builtin family classified as carrying no instance field: the filter matched nothing, "+
			"so this test asserted nothing")

	// THE OTHER DIRECTION, which is what keeps the collapse from widening. A
	// family that DOES carry an instance field must still refuse an empty name:
	// there it means the caller named no repo, account or language, and admitting
	// a catalog enumeration is the failure the structural half of the admission
	// gate exists to prevent.
	for _, gt := range []kgtypes.GraphType{kgtypes.GraphCode, kgtypes.GraphCloud, kgtypes.GraphCICD, kgtypes.GraphPractice} {
		require.NotEqual(t, graphsel.FieldNone, graphsel.InstanceField(gt),
			"control: %q must carry an instance field for the next assertion to mean anything", gt)
		_, ok := workingset.Normalize(gt, "")
		assert.False(t, ok,
			"%q carries an instance field, so an empty one is an absent selector and must admit nothing", gt)
	}
}

// TestCanonicalInstanceNameAgreesWithTheNormalizer is the SECOND half of the
// drift guard, and it exists because the conflation kept resurfacing at seams
// rather than at the normalizer.
//
// THE TWO ANSWERS MUST BE THE SAME STRING. Normalize decides what the working set
// and the collector registry key a graph under; CanonicalInstanceName is what the
// (gt, name)-keyed CONSUMING seams — the segment engine lookup, the segment
// rebuild — ask before addressing it. If those two ever disagreed, a graph would
// be sealed under one name and read under another, which is the silent zero this
// whole family of defects produced three separate times: no error, no log line,
// just an empty result from an instance nothing had written to.
//
// It is asserted for EVERY builtin rather than for the singletons alone, because
// the identity half is what makes the helper safe to apply on shared paths that
// also serve code and custom graphs.
func TestCanonicalInstanceNameAgreesWithTheNormalizer(t *testing.T) {
	t.Parallel()

	names := kgtypes.BuiltinGraphTypeNames()
	require.NotEmpty(t, names, "the builtin enumeration is empty — this walk would be vacuous")

	var sawCollapse, sawIdentity bool
	for _, name := range names {
		gt := kgtypes.GraphType(name)
		canonical := workingset.CanonicalInstanceName(gt, "")
		ref, normalized := workingset.Normalize(gt, "")

		if normalized {
			sawCollapse = true
			assert.Equal(t, ref.Name, canonical,
				"%q normalizes to %q, so every (graph type, name)-keyed seam must address it under that same "+
					"name — a seam asking for anything else reads an instance nothing wrote to", name, ref.Name)
			continue
		}
		sawIdentity = true
		assert.Empty(t, canonical,
			"%q carries a real instance field, so canonicalization must be the identity and leave the "+
				"caller's (absent) name alone", name)
	}

	// BOTH ARMS MUST HAVE RUN. Each assertion sits on one side of a branch, so a
	// walk that only ever took one of them would report a clean pass having
	// checked half the property.
	require.True(t, sawCollapse, "no builtin exercised the collapse arm — this test asserted only the identity half")
	require.True(t, sawIdentity, "no builtin exercised the identity arm — this test asserted only the collapse half")
}

// SPDX-License-Identifier: Apache-2.0

// graphsel_canonical_test.go — the CLIENT's canonical graph-NAME rule.
//
// IT USED TO BE canonical_vector_test.go, THE CLIENT HALF OF A TWO-MODULE PARITY
// CHECK. testdata/practice_graph_name_canonical_cases.json at the repo root was a
// DATA file, not a shared Go package: cmd/knowledge and cmd/knowledge-server
// share nothing hand-written, so the two copies of the practice SLUG rule were
// held together by a table each module read for itself, through a symlink under
// its own testdata that put the table in that package's test-cache key.
//
// THE TABLE WENT WITH THE TRANSFORM IT SPECIFIED. Every one of its rows pinned
// one function — SlugifyPracticeLanguage here and store.SlugifyLanguage on the
// server — and both existed to spell the name of one of the eight per-language
// practice graphs the legacy `language` selector addressed. Those graphs are
// retired and the selector is refused on every arm, so neither transform had a
// caller and nothing was left to keep in parity. The table, both symlinks, the
// server's reader and the client's rows all went together; testdata/
// contribution_hash_vector.json remains as the surviving example of the pattern.
//
// WHAT IS LEFT IS THE RULE THAT IS NOT A TRANSFORM: what an UNSELECTED address
// of a singleton family canonicalises to. That is a CONSTANT, which no case
// table can pin, and the tests below are it.

package graphsel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestCanonicalGraphName_PracticeIsASingleton pins the arm practice moved into.
//
// It is the assertion the shared vector could never make: a vector pins a
// TRANSFORM and the singleton rule is a CONSTANT. While both existed, a
// regression that put practice back on the slug arm left the vector green — the
// slug function still passed it — while every practice write named a graph the
// server's empty policy row refuses. The vector is gone with the transform; this
// is the whole rule now.
func TestCanonicalGraphName_PracticeIsASingleton(t *testing.T) {
	for _, name := range []string{"", "go", "design-patterns", "Go / Design Patterns++"} {
		assert.Equal(t, "default", CanonicalGraphName(kgtypes.GraphPractice, name),
			"practice holds ONE graph, so %q canonicalises to default", name)
	}

	assert.True(t, IsCanonicalGraphName(kgtypes.GraphPractice, "default"),
		"default is the one legitimate practice spelling")
	assert.False(t, IsCanonicalGraphName(kgtypes.GraphPractice, "go"),
		"a per-language practice name is not canonical any more: that is the refusal every create channel acts on")

	// THE CONTROL that keeps the rule from being satisfied by a function that
	// returns "default" for everything: a family that keys its name raw must get
	// its name back unchanged, including one carrying every transformable
	// character. It used to be the slug transform still transforming, which was
	// the same discrimination against the one other rule this file then held.
	require.Equal(t, "JavaScript/TypeScript",
		CanonicalGraphName(kgtypes.GraphCode, "JavaScript/TypeScript"),
		"a raw-keyed family is returned unchanged, or the singleton assertions above are vacuous")
}

// TestCanonicalGraphName_IdentityForEveryOtherFamily pins that the rule leaves
// every raw-keyed family alone.
//
// THE CLOSING ASSERTION IS THE DISCRIMINATING CONTROL. Without it every identity
// claim below would hold just as well for a function that returned its argument
// unchanged for every family, which is precisely the regression this test exists
// to catch.
func TestCanonicalGraphName_IdentityForEveryOtherFamily(t *testing.T) {
	// One input carrying every transformable character: uppercase, a slash, a
	// space and a plus.
	const hostile = "Go / Design Patterns++"

	for _, gt := range []kgtypes.GraphType{
		kgtypes.GraphCode,
		kgtypes.GraphWebRaw,
		kgtypes.GraphPDFRaw,
		kgtypes.GraphKnowledge,
	} {
		assert.Equal(t, hostile, CanonicalGraphName(gt, hostile),
			"%s keys raw at both ends, so its name must not be transformed", gt)
		assert.True(t, IsCanonicalGraphName(gt, hostile),
			"%s must accept a raw name as already canonical", gt)
	}

	// THE CONTROL: the same input MUST come back "default" under a singleton
	// family, so the seven identity claims above are the rule being family-keyed
	// rather than the rule doing nothing at all. Practice is the singleton used
	// here because it is the family that MOVED — before the combined graph it
	// discriminated by transforming, and it must still discriminate.
	require.Equal(t, "default", CanonicalGraphName(kgtypes.GraphPractice, hostile),
		"practice is a singleton, so it must collapse the same input the raw-keyed families leave alone")
	require.False(t, IsCanonicalGraphName(kgtypes.GraphPractice, hostile),
		"practice must refuse the same input the raw-keyed families accept")
}

// TestCanonicalGraphName_SingletonFamiliesHaveOneName pins the singleton arm.
// "default" is a CONSTANT rather than a transform, which is why these families
// are asserted here instead of in the shared table.
func TestCanonicalGraphName_SingletonFamiliesHaveOneName(t *testing.T) {
	for _, gt := range []kgtypes.GraphType{kgtypes.GraphLinkage, kgtypes.GraphChecks, kgtypes.GraphPractice} {
		// Any name canonicalises to "default" — including one carrying every
		// character the legacy practice slug rule transforms, which this arm must
		// collapse rather than rewrite.
		assert.Equal(t, "default", CanonicalGraphName(gt, "knowledge"),
			"%s resolves a hardcoded \"default\", so every other name canonicalises to it", gt)
		assert.Equal(t, "default", CanonicalGraphName(gt, "Go / Design Patterns++"),
			"%s must collapse a slug-shaped name rather than transforming it", gt)

		// A non-default name is NOT canonical: this is the refusal the create
		// channels act on.
		assert.False(t, IsCanonicalGraphName(gt, "knowledge"),
			"%s under a non-default name names a graph no read of the family can open", gt)

		// And "default" itself still passes, so the refusal is not universal.
		assert.True(t, IsCanonicalGraphName(gt, "default"),
			"%s under \"default\" is the one legitimate spelling", gt)
	}
}

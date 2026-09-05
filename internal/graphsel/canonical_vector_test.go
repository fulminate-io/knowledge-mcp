// SPDX-License-Identifier: Apache-2.0

// canonical_vector_test.go — the CLIENT half of the two-module parity check on
// the canonical practice-graph-name rule.
//
// testdata/practice_graph_name_canonical_cases.json at the repo root is a DATA
// file, not a shared Go package: cmd/knowledge and cmd/knowledge-server share
// nothing hand-written, so the two copies of this rule are held together by a
// table each module reads for itself. The server half is
// cmd/knowledge-server/internal/store/practice_slug_vector_test.go, and it
// asserts store.SlugifyLanguage — the authority — against these same rows.
//
// This follows testdata/contribution_hash_vector.json, which is read the same
// way by a test in each module for the same reason.

package graphsel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// canonicalCase is one row of the shared table.
type canonicalCase struct {
	Label     string `json:"label"`
	Input     string `json:"input"`
	Canonical string `json:"canonical"`
}

type canonicalVector struct {
	Spec  string          `json:"spec"`
	Cases []canonicalCase `json:"cases"`
}

// sharedTestdataPath resolves a file in the SHARED testdata directory by
// walking up from this package to the first ancestor that carries BOTH a
// go.mod and that file under testdata/.
//
// NO FIXED ".." COUNT SPELLS BOTH LAYOUTS. The fixture is read by a test in
// each of the two modules, so it sits ABOVE both module roots here; the
// published mirror is a single module whose root carries testdata/ directly,
// because the sync script copies cmd/knowledge/internal to internal/ and the
// shared testdata tree to the mirror root. Walking for the artifact itself
// survives both layouts and a package move, and fails loudly rather than
// silently comparing against nothing.
func sharedTestdataPath(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			candidate := filepath.Join(dir, "testdata", name)
			if _, statErr := os.Stat(candidate); statErr == nil {
				return candidate
			}
		}
		parent := filepath.Dir(dir)
		require.NotEqualf(t, parent, dir,
			"walked to the filesystem root from the test working directory without finding testdata/%s beside a go.mod", name)
		dir = parent
	}
}

// loadCanonicalVector reads the shared table.
func loadCanonicalVector(t *testing.T) canonicalVector {
	t.Helper()

	raw, err := os.ReadFile(sharedTestdataPath(t, "practice_graph_name_canonical_cases.json"))
	require.NoError(t, err, "the shared canonical-name vector must be readable from this module")

	var v canonicalVector
	require.NoError(t, json.Unmarshal(raw, &v))
	require.NotEmpty(t, v.Cases, "the shared vector carried no cases, so this test measured nothing")

	return v
}

// TestCanonicalGraphName_MatchesSharedVector asserts the CLIENT copy of the
// practice rule against the shared table, one subtest per row.
func TestCanonicalGraphName_MatchesSharedVector(t *testing.T) {
	v := loadCanonicalVector(t)

	for _, c := range v.Cases {
		t.Run(c.Label, func(t *testing.T) {
			assert.Equal(t, c.Canonical, CanonicalGraphName(kgtypes.GraphPractice, c.Input),
				"the client rule disagrees with the shared vector on %q", c.Input)

			// The predicate must agree with the transform on the same row: a
			// name is canonical exactly when the transform leaves it alone.
			assert.Equal(t, c.Input == c.Canonical, IsCanonicalGraphName(kgtypes.GraphPractice, c.Input),
				"IsCanonicalGraphName disagrees with CanonicalGraphName on %q", c.Input)
		})
	}
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
		kgtypes.GraphCloud,
		kgtypes.GraphCICD,
		kgtypes.GraphLogs,
		kgtypes.GraphWebRaw,
		kgtypes.GraphPDFRaw,
		kgtypes.GraphKnowledge,
	} {
		assert.Equal(t, hostile, CanonicalGraphName(gt, hostile),
			"%s keys raw at both ends, so its name must not be transformed", gt)
		assert.True(t, IsCanonicalGraphName(gt, hostile),
			"%s must accept a raw name as already canonical", gt)
	}

	// THE CONTROL: the same input MUST transform under practice, so the eight
	// identity claims above are the rule being family-keyed rather than the rule
	// doing nothing at all.
	require.NotEqual(t, hostile, CanonicalGraphName(kgtypes.GraphPractice, hostile),
		"practice must transform the same input the raw-keyed families leave alone")
	require.False(t, IsCanonicalGraphName(kgtypes.GraphPractice, hostile),
		"practice must refuse the same input the raw-keyed families accept")
}

// TestCanonicalGraphName_SingletonFamiliesHaveOneName pins the singleton arm.
// "default" is a CONSTANT rather than a transform, which is why these families
// are asserted here instead of in the shared table.
func TestCanonicalGraphName_SingletonFamiliesHaveOneName(t *testing.T) {
	for _, gt := range []kgtypes.GraphType{kgtypes.GraphLinkage, kgtypes.GraphChecks} {
		// Any name canonicalises to "default" — including one the practice rule
		// would have transformed into something else entirely.
		assert.Equal(t, "default", CanonicalGraphName(gt, "knowledge"),
			"%s resolves a hardcoded \"default\", so every other name canonicalises to it", gt)
		assert.Equal(t, "default", CanonicalGraphName(gt, "Go / Design Patterns++"),
			"%s must not fall through to the practice transforms", gt)

		// A non-default name is NOT canonical: this is the refusal the create
		// channels act on.
		assert.False(t, IsCanonicalGraphName(gt, "knowledge"),
			"%s under a non-default name names a graph no read of the family can open", gt)

		// And "default" itself still passes, so the refusal is not universal.
		assert.True(t, IsCanonicalGraphName(gt, "default"),
			"%s under \"default\" is the one legitimate spelling", gt)
	}
}

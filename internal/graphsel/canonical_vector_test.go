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
	"strings"
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

// sharedVectorLink is the shared canonical-name table, READ THROUGH THIS
// PACKAGE'S OWN testdata LINK.
//
// THE LINK IS A TEST-CACHE FENCE, and it REPLACES a walk-up resolver that could
// not be one. `go test` keys a package's stored result on the files a run opened
// and DROPS any opened name that does not resolve inside the tested package's
// own module root. The previous helper walked up to the first ancestor carrying
// both a go.mod and the table, which here is the REPO root — above this module —
// so the shared table was never in this package's key: editing a row and
// re-running returned `ok (cached)`, a stored PASS for the guard that exists to
// notice exactly that edit. A name under this package's own testdata is inside
// cmd/knowledge, so the go tool records it, and os.Stat follows the link to the
// target's size and modification time.
//
// THE LINK'S DEPTH DIFFERS IN THE PUBLISHED MIRROR, which is why
// scripts/sync-to-oss.sh re-points it: here it sits five levels below the repo
// root, and there the same file sits three levels below the mirror root, because
// the script maps cmd/knowledge/internal to internal/. That is the same
// re-point, for the same reason, that internal/tools/testdata/docs already
// carries.
const sharedVectorLink = "testdata/practice_graph_name_canonical_cases.json"

// sharedVectorTarget is what the link must resolve to. Asserted separately, and
// never used to READ: resolving the link first names the repo-root path again,
// which is outside this module, and undoes the fence.
const sharedVectorTarget = "testdata/practice_graph_name_canonical_cases.json"

// loadCanonicalVector reads the shared table.
func loadCanonicalVector(t *testing.T) canonicalVector {
	t.Helper()

	raw, err := os.ReadFile(sharedVectorLink)
	require.NoError(t, err, "the shared canonical-name vector must be readable from this module")

	var v canonicalVector
	require.NoError(t, json.Unmarshal(raw, &v))
	require.NotEmpty(t, v.Cases, "the shared vector carried no cases, so this test measured nothing")

	return v
}

// TestCanonicalGraphName_MatchesSharedVector asserts the CLIENT copy of the
// legacy practice SLUG rule against the shared table, one subtest per row.
//
// THE SUBJECT MOVED WITH THE SINGLETON. Before the combined practice graph this
// table pinned CanonicalGraphName, because a practice graph's NAME was the slug.
// Practice is now a singleton named "default", so CanonicalGraphName holds no
// transform to compare and the transform it used to hold lives on as
// SlugifyPracticeLanguage, for the legacy `language` read selector. The server
// half still asserts store.SlugifyLanguage against these same rows, so the
// two-module parity this file exists for is unchanged; only the client symbol
// under test moved.
func TestCanonicalGraphName_MatchesSharedVector(t *testing.T) {
	v := loadCanonicalVector(t)

	for _, c := range v.Cases {
		t.Run(c.Label, func(t *testing.T) {
			assert.Equal(t, c.Canonical, SlugifyPracticeLanguage(c.Input),
				"the client slug rule disagrees with the shared vector on %q", c.Input)
		})
	}
}

// TestSharedVectorLinkResolves is the PIN on the fence. A checkout without
// symlink support materializes the link as a one-line text stub and the read
// above fails to unmarshal; this test says which of the two it is. The mutation
// that must turn it red is replacing the link with a regular file holding the
// same bytes — a copy reads fine and puts this package straight back to being
// cacheable against a table it never re-read.
//
// IT ALSO PINS THE MIRROR. The suffix assertion holds in both layouts, which is
// what makes the sync script's re-pointed link checkable by running this test in
// the staged mirror rather than by reading the script's text.
func TestSharedVectorLinkResolves(t *testing.T) {
	info, err := os.Lstat(sharedVectorLink)
	require.NoError(t, err, "%s must exist — it is what puts the shared table in this package's test-cache key", sharedVectorLink)
	require.NotZero(t, info.Mode()&os.ModeSymlink,
		"%s is not a symlink (a checkout without symlink support materializes it as a text stub); "+
			"this package would then be cacheable against a table it never read", sharedVectorLink)

	// Abs BEFORE EvalSymlinks: handed a relative name EvalSymlinks returns a
	// relative result, and the suffix assertion would compare the link's own
	// target text rather than the resolved path.
	abs, err := filepath.Abs(sharedVectorLink)
	require.NoError(t, err)
	resolved, err := filepath.EvalSymlinks(abs)
	require.NoError(t, err, "%s must resolve; a dangling link fences nothing", sharedVectorLink)
	require.True(t, strings.HasSuffix(filepath.ToSlash(resolved), sharedVectorTarget),
		"%s resolves to %s, which is not the shared canonical-name vector", sharedVectorLink, resolved)
	require.NotContains(t, filepath.ToSlash(resolved), "internal/graphsel/",
		"control: the link must reach the table ABOVE this package, not a copy inside it")

	// KNOWN POSITIVE: it serves the real table, not an empty stub.
	raw, err := os.ReadFile(sharedVectorLink)
	require.NoError(t, err)
	require.Contains(t, string(raw), "\"cases\"", "the link must serve the shared vector's rows")
}

// TestCanonicalGraphName_PracticeIsASingleton pins the arm practice moved into.
//
// It is the assertion the shared vector can no longer make: the vector pins a
// TRANSFORM and the singleton rule is a CONSTANT. Without it, a regression that
// put practice back on the slug arm would leave the vector green — the slug
// function still passes it — while every practice write named a graph the
// server's now-empty policy row refuses.
func TestCanonicalGraphName_PracticeIsASingleton(t *testing.T) {
	for _, name := range []string{"", "go", "design-patterns", "Go / Design Patterns++"} {
		assert.Equal(t, "default", CanonicalGraphName(kgtypes.GraphPractice, name),
			"practice holds ONE graph, so %q canonicalises to default", name)
	}

	assert.True(t, IsCanonicalGraphName(kgtypes.GraphPractice, "default"),
		"default is the one legitimate practice spelling")
	assert.False(t, IsCanonicalGraphName(kgtypes.GraphPractice, "go"),
		"a per-language practice name is not canonical any more: that is the refusal every create channel acts on")

	// THE CONTROL that keeps the two rules distinguishable: the slug transform
	// still transforms, so the assertions above are practice being a singleton
	// rather than SlugifyPracticeLanguage having been quietly gutted.
	require.Equal(t, "javascript-typescript", SlugifyPracticeLanguage("JavaScript/TypeScript"),
		"the legacy slug rule must still transform, or the singleton assertions above are vacuous")
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

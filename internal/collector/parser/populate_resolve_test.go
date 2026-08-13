// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// These tests drive the real chunker through chunkResultsToPopulate so the
// fixtures exercise the actual per-language query sets, which is the coverage
// the resolver has never had: its only other test hand-builds a Go-shaped
// symbolMap. Everything is in-memory — nothing reads the filesystem.

type fixtureFile struct {
	path string
	src  string
}

// populateFixture chunks each file IN THE GIVEN ORDER and runs the populate +
// resolve pass over the results. Order is a parameter rather than an
// implementation detail because chunkResultsToPopulate builds ONE symbolMap for
// every language, so a later writer can take a key an earlier one wrote.
func populateFixture(t *testing.T, files []fixtureFile) PopulateResult {
	t.Helper()
	chunker := treesitter.NewChunker()
	defer chunker.Close()

	results := make([]*treesitter.Result, 0, len(files))
	for _, f := range files {
		r, err := chunker.ChunkFile(context.Background(), f.path, []byte(f.src))
		require.NoError(t, err, "chunking %s", f.path)
		results = append(results, r)
	}
	return chunkResultsToPopulate("testrepo", "", results)
}

// edgesFrom returns the ToIDs of every edge of the given type leaving from.
func edgesFrom(res PopulateResult, edgeType kgtypes.EdgeType, from string) []string {
	var out []string
	for _, e := range res.Edges {
		if kgtypes.EdgeType(e.Type) == edgeType && e.FromId == from {
			out = append(out, e.ToId)
		}
	}
	return out
}

func hasEdge(res PopulateResult, edgeType kgtypes.EdgeType, from, to string) bool {
	for _, e := range res.Edges {
		if kgtypes.EdgeType(e.Type) == edgeType && e.FromId == from && e.ToId == to {
			return true
		}
	}
	return false
}

func countEdges(res PopulateResult, edgeType kgtypes.EdgeType) int {
	n := 0
	for _, e := range res.Edges {
		if kgtypes.EdgeType(e.Type) == edgeType {
			n++
		}
	}
	return n
}

const pyAnimalsSrc = `class Animal:
    def speak(self):
        return "..."

    def describe(self):
        return self.speak() + " (animal)"


class Dog(Animal):
    def speak(self):
        return bark_sound()


def bark_sound():
    return "woof"


def make_sound(a: Animal) -> str:
    return a.speak()
`

const pyMainSrc = `from pkg.animals import Dog, make_sound, bark_sound


def helper():
    return bark_sound()


def run():
    d = Dog()
    local = helper()
    return make_sound(d) + local
`

func pythonFixture() []fixtureFile {
	return []fixtureFile{
		{path: "pkg/animals.py", src: pyAnimalsSrc},
		{path: "pkg/main.py", src: pyMainSrc},
	}
}

func TestPopulate_PythonEdgesSurvive(t *testing.T) {
	res := populateFixture(t, pythonFixture())

	// FILE → SYMBOL CONTAINS: one per declared symbol, asserted by identity
	// and then pinned by a fixture-derived count.
	fileContains := edgesFrom(res, kgtypes.EdgeContains, "pkg/animals.py")
	for _, want := range []string{
		"pkg/animals.py:Animal",
		"pkg/animals.py:Animal.speak",
		"pkg/animals.py:Animal.describe",
		"pkg/animals.py:Dog",
		"pkg/animals.py:Dog.speak",
		"pkg/animals.py:bark_sound",
		"pkg/animals.py:make_sound",
	} {
		assert.Contains(t, fileContains, want)
	}
	assert.Len(t, fileContains, 7, "seven declared symbols in the fixture")

	// CALLS. The same-class call is the end-to-end proof of the class-aware
	// parent: describe's own FromID supplies the receiver context that sends
	// the bare "speak" to Animal's method rather than Dog's.
	assert.True(t, hasEdge(res, kgtypes.EdgeCalls,
		"pkg/animals.py:Animal.describe", "pkg/animals.py:Animal.speak"))
	assert.True(t, hasEdge(res, kgtypes.EdgeCalls,
		"pkg/animals.py:Dog.speak", "pkg/animals.py:bark_sound"))
	// Cross-file, same derived namespace.
	assert.True(t, hasEdge(res, kgtypes.EdgeCalls,
		"pkg/main.py:run", "pkg/animals.py:make_sound"))
	assert.True(t, hasEdge(res, kgtypes.EdgeCalls,
		"pkg/main.py:run", "pkg/main.py:helper"))

	// USES_TYPE from the parameter annotation.
	assert.True(t, hasEdge(res, kgtypes.EdgeUsesType,
		"pkg/animals.py:make_sound", "pkg/animals.py:Animal"))

	// NEGATIVE, with the four surviving CALLS above as its known positive:
	// `a.speak()` carries no receiver context, so both candidates remain and
	// the ambiguous edge is correctly dropped rather than guessed.
	for _, to := range edgesFrom(res, kgtypes.EdgeCalls, "pkg/animals.py:make_sound") {
		assert.False(t, strings.HasSuffix(to, ":Animal.speak"),
			"ambiguous a.speak() must not resolve to Animal.speak")
		assert.False(t, strings.HasSuffix(to, ":Dog.speak"),
			"ambiguous a.speak() must not resolve to Dog.speak")
	}
}

func TestPopulate_PythonTwoClassesSameMethod(t *testing.T) {
	res := populateFixture(t, pythonFixture())

	// Each class CONTAINS its OWN method and only its own.
	animalMembers := edgesFrom(res, kgtypes.EdgeContains, "pkg/animals.py:Animal")
	dogMembers := edgesFrom(res, kgtypes.EdgeContains, "pkg/animals.py:Dog")

	assert.Contains(t, animalMembers, "pkg/animals.py:Animal.speak")
	assert.Contains(t, animalMembers, "pkg/animals.py:Animal.describe")
	assert.Contains(t, dogMembers, "pkg/animals.py:Dog.speak")

	// The discriminator: a bare-name implementation would attach both speaks
	// to whichever class resolved first.
	assert.NotContains(t, animalMembers, "pkg/animals.py:Dog.speak")
	assert.NotContains(t, dogMembers, "pkg/animals.py:Animal.speak")
	assert.NotContains(t, dogMembers, "pkg/animals.py:Animal.describe")

	// Cardinality from fixture-derived constants, never from the result set.
	assert.Len(t, animalMembers, 2)
	assert.Len(t, dogMembers, 1)

	// The two speaks are distinct, unsuffixed nodes: distinct parents mean no
	// collision, so no astPathHash rename.
	ids := map[string]bool{}
	for _, n := range res.Nodes {
		ids[n.Id] = true
	}
	assert.True(t, ids["pkg/animals.py:Animal.speak"])
	assert.True(t, ids["pkg/animals.py:Dog.speak"])
	for id := range ids {
		assert.NotContains(t, id, "#", "no chunk in this fixture collides")
	}

	// Top-level declarations have no enclosing scope, so nothing may emit a
	// parent edge for them.
	assert.Empty(t, edgesFrom(res, kgtypes.EdgeContains, "pkg/main.py:run"))
	assert.Empty(t, edgesFrom(res, kgtypes.EdgeContains, "pkg/main.py:helper"))
}

func TestResolveEdges_EmptySymbolMapDropsAll(t *testing.T) {
	edges := []*knowledgev1.Edge{
		{FromId: "pkg.Caller", ToId: "pkg.Callee", Type: string(kgtypes.EdgeCalls)},
		{FromId: "pkg/a.go", ToId: "pkg.Caller", Type: string(kgtypes.EdgeContains)},
		{FromId: "pkg.Caller", ToId: "pkg.Thing", Type: string(kgtypes.EdgeUsesType)},
		{FromId: "pkg/a.go", ToId: "fmt", Type: string(kgtypes.EdgeImports)},
	}
	nodeIDs := map[string]bool{"pkg/a.go": true}

	// With no symbols recorded, only the pre-resolution IMPORTS edge survives —
	// the shape every non-Go language had before this ticket, because their
	// empty namespace made recordSymbol a no-op.
	gotEmpty := resolveEdges(cloneEdges(edges), map[string]string{}, nodeIDs)
	require.Len(t, gotEmpty, 1)
	assert.Equal(t, string(kgtypes.EdgeImports), gotEmpty[0].Type)

	// KNOWN-POSITIVE CONTROL: the identical edge set against a populated
	// symbolMap keeps all four. Without this the test could not tell a
	// namespace-driven drop from an unconditional one.
	symbolMap := map[string]string{
		"pkg.Caller": "pkg/a.go:Caller",
		"pkg.Callee": "pkg/b.go:Callee",
		"pkg.Thing":  "pkg/b.go:Thing",
	}
	gotFull := resolveEdges(cloneEdges(edges), symbolMap, nodeIDs)
	assert.Len(t, gotFull, 4, "every edge resolves once the symbols are known")
}

// cloneEdges copies the fixture edges because resolveEdges rewrites FromId and
// ToId in place on the pointers it is handed.
func cloneEdges(in []*knowledgev1.Edge) []*knowledgev1.Edge {
	out := make([]*knowledgev1.Edge, 0, len(in))
	for _, e := range in {
		out = append(out, &knowledgev1.Edge{FromId: e.FromId, ToId: e.ToId, Type: e.Type})
	}
	return out
}

// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	// THE MODULE PATH IS SUPPLIED, NOT LEFT ZERO. A registered arm that maps
	// import paths onto repo directories needs it, and with it absent the Go
	// arm takes its zero-result path — so every cross-package Go row would
	// report External while the unqualified rows still passed, which is the
	// most dangerous direction for a fixture to be wrong in. Only ModulePath is
	// set: the harness chunks its fixtureFile list directly rather than walking
	// a directory, so THE FIXTURE LIST IS THE DISCOVERED SET, and Go's arm
	// reads neither Root nor Files.
	// "example.com/fixture" is the module every fixture in this package imports
	// through: a file under dir/ is imported as "example.com/fixture/dir".
	return chunkResultsToPopulate("testrepo",
		&treesitter.RepoContext{ModulePath: "example.com/fixture"}, results)
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
	// Same-file: binds by the own-scope rule.
	assert.True(t, hasEdge(res, kgtypes.EdgeCalls,
		"pkg/main.py:run", "pkg/main.py:helper"))

	// RESTORED, EXACTLY AS THE PREVIOUS STATE OF THIS ASSERTION PREDICTED. The
	// cross-file call had been asserted ABSENT, because python's resolution
	// unit is the FILE and the edge's only previous route was a name-wide
	// search that ignored scope entirely — the same search that let references
	// escape their own scope and bind to unrelated same-named declarations.
	// That assertion carried its own expiry: "registering that arm is what
	// restores this edge, and it will restore it EXACTLY rather than by name."
	//
	// The python BindsResolver arm is now registered, so `from pkg.animals
	// import make_sound` binds the name into pkg/animals.py's scope and the
	// unqualified-import rung resolves it there. THE ROUTE IS WHAT MATTERS, not
	// merely the edge's presence: it binds through the import into ONE named
	// file's scope, never by matching a name anywhere in the corpus.
	assert.True(t, hasEdge(res, kgtypes.EdgeCalls,
		"pkg/main.py:run", "pkg/animals.py:make_sound"),
		"the import arm binds a cross-file python call by scope")

	// USES_TYPE from the parameter annotation.
	assert.True(t, hasEdge(res, kgtypes.EdgeUsesType,
		"pkg/animals.py:make_sound", "pkg/animals.py:Animal"))

	// RE-DERIVED AGAIN, and this time the OPEN SET COLLAPSES TO ONE EXACT EDGE.
	// The history is worth keeping because each step was this fixture's point in
	// turn. The python Calls query once reached PAST the attribute node to the
	// bare trailing identifier, so `a.speak()` arrived as `speak`, matched no
	// unparented declaration and vanished. It then arrived as `a.speak`, whose
	// qualifier is a VALUE that no declared parent matches, so the ladder fell to
	// the dynamic rung and offered BOTH speak methods at Confidence 1/N — one
	// open-set edge per candidate, deliberately not guessing between them.
	//
	// A REGISTERED PYTHON QUALIFIER-TYPE ARM ANSWERS THE QUESTION THE FAN-OUT WAS
	// ADMITTING IT COULD NOT. `make_sound(a: Animal)` DECLARES the receiver's
	// type, so the typed-qualifier rung binds `a` to Animal and resolves the call
	// to Animal.speak exactly — one edge, full confidence, no group. Dog.speak is
	// no longer offered, and its absence is the improvement rather than a loss:
	// the source said Animal, and a set containing Dog said only that nobody had
	// read the annotation.
	//
	// The four surviving CALLS above remain this block's known positive.
	var speakTargets, speakMethods []string
	var speakConfidence []float64
	for _, e := range res.Edges {
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeCalls || e.FromId != "pkg/animals.py:make_sound" {
			continue
		}
		if strings.HasSuffix(e.ToId, ".speak") {
			speakTargets = append(speakTargets, e.ToId)
			speakMethods = append(speakMethods, e.Method)
			speakConfidence = append(speakConfidence, float64(e.Confidence))
		}
	}
	require.Len(t, speakTargets, 1, "the annotation resolves the receiver exactly, so there is no fan-out")
	assert.Equal(t, "pkg/animals.py:Animal.speak", speakTargets[0],
		"the declared parameter type decides the target")
	assert.Equal(t, string(RuleTypedQualifier), speakMethods[0],
		"and it is the typed-qualifier rung that decided it, not a name match")
	assert.InDelta(t, float64(0), speakConfidence[0], 1e-9,
		"an exactly-bound edge emits no group and so carries no 1/N confidence at all — "+
			"the 0.5 this fixture used to assert was the fan-out's share, and its absence is the point")
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

// TestResolveEdges_EmptySymbolMapDropsAll pins what survives when the
// declaration index knows nothing.
//
// The name predates the index — the scalar symbol map it refers to is gone —
// but the property it guards is unchanged and still worth pinning: a reference
// the index cannot see emits NOTHING rather than a guess, while the two edge
// shapes that never depended on name resolution survive regardless. Containment
// is now among those: it is addressed by chunk slot, so it no longer passes
// through name resolution at all.
func TestResolveEdges_EmptySymbolMapDropsAll(t *testing.T) {
	ref := refSiteFor("pkg/a.go", "dir:pkg", "")
	newResults := func() []*treesitter.Result {
		return []*treesitter.Result{{
			FilePath: "pkg/a.go",
			Language: treesitter.LangGo,
			Edges: []treesitter.Edge{
				{FromID: "pkg/a.go:Caller", ToID: "Callee", Type: treesitter.EdgeCalls, Ref: ref},
				{FromID: "pkg/a.go", ToID: "pkg/a.go:Caller", Type: treesitter.EdgeContains},
				{FromID: "pkg/a.go:Caller", ToID: "Thing", Type: treesitter.EdgeUsesType, Ref: ref},
				{FromID: "pkg/a.go", ToID: "fmt", Type: treesitter.EdgeImports},
			},
		}}
	}
	nodeIDs := map[string]bool{
		"pkg/a.go": true, "pkg/a.go:Caller": true,
		"pkg/b.go:Callee": true, "pkg/b.go:Thing": true,
	}

	// An EMPTY index resolves no reference: both the CALLS and the USES_TYPE
	// edge emit nothing. IMPORTS and the slot-addressed CONTAINS survive
	// because neither consults the index.
	gotEmpty := resolveEdges(newResults(), newDeclIndex(0), nodeIDs)
	require.Len(t, gotEmpty, 2)
	types := []string{gotEmpty[0].Type, gotEmpty[1].Type}
	assert.ElementsMatch(t, []string{string(kgtypes.EdgeContains), string(kgtypes.EdgeImports)}, types,
		"only the two shapes that never needed name resolution survive an empty index")

	// KNOWN-POSITIVE CONTROL: the identical edge set against a POPULATED index
	// keeps all four. Without it this test could not tell a lookup-driven drop
	// from an unconditional one.
	ix := indexOf(t,
		&declRec{NodeID: "pkg/b.go:Callee", File: "pkg/b.go", Scope: "dir:pkg", Name: "Callee"},
		&declRec{NodeID: "pkg/b.go:Thing", File: "pkg/b.go", Scope: "dir:pkg", Name: "Thing"},
	)
	gotFull := resolveEdges(newResults(), ix, nodeIDs)
	assert.Len(t, gotFull, 4, "every edge resolves once the declarations are indexed")
}

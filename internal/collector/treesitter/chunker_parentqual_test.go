// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// symbolKeyFor mirrors parser.recordSymbol's key construction
// (populate.go: "<ns>.<Name>", or "<ns>.<ParentName>.<Name>" when a parent is
// set). Edge endpoints must equal this string or the edge fails resolution —
// that agreement is what these tests pin.
func symbolKeyFor(c Chunk) string {
	if c.ParentName != "" {
		return c.Context.PackageName + "." + c.ParentName + "." + c.Name
	}
	return c.Context.PackageName + "." + c.Name
}

func edgesOfType(edges []Edge, t EdgeType) []Edge {
	var out []Edge
	for _, e := range edges {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

func TestDeclEdges_ParentQualifiedPython(t *testing.T) {
	chunker := NewChunker()
	defer chunker.Close()

	src := []byte(`class Animal:
    def speak(self):
        return "..."

    def describe(self):
        return self.speak()

class Dog:
    def speak(self):
        return "woof"
`)
	result, err := chunker.ChunkFile(context.Background(), "pkg/animals.py", src)
	require.NoError(t, err)

	const ns = "python:pkg"

	// File → declaration CONTAINS carries the parent-qualified member name.
	var fileContains []string
	for _, e := range edgesOfType(result.Edges, EdgeContains) {
		if e.FromID == "pkg/animals.py" {
			fileContains = append(fileContains, e.ToID)
		}
	}
	assert.Contains(t, fileContains, ns+".Animal.speak")
	assert.Contains(t, fileContains, ns+".Dog.speak")
	assert.Contains(t, fileContains, ns+".Animal.describe")

	// Each class emits a parent → member CONTAINS to its OWN method, never
	// the other class's.
	byParent := map[string][]string{}
	for _, e := range edgesOfType(result.Edges, EdgeContains) {
		if e.FromID == ns+".Animal" || e.FromID == ns+".Dog" {
			byParent[e.FromID] = append(byParent[e.FromID], e.ToID)
		}
	}
	sort.Strings(byParent[ns+".Animal"])
	assert.Equal(t, []string{ns + ".Animal.describe", ns + ".Animal.speak"}, byParent[ns+".Animal"])
	assert.Equal(t, []string{ns + ".Dog.speak"}, byParent[ns+".Dog"])

	// The CALLS edge out of describe is attributed to the parent-qualified
	// caller, so it matches the symbolMap key its chunk will be recorded under.
	var callFroms []string
	for _, e := range edgesOfType(result.Edges, EdgeCalls) {
		callFroms = append(callFroms, e.FromID)
	}
	assert.Contains(t, callFroms, ns+".Animal.describe")

	// Every emitted endpoint that names a chunk agrees with that chunk's
	// symbolMap key — the by-construction property this step exists to create.
	keys := map[string]bool{}
	for _, c := range result.Chunks {
		if c.Name != "" {
			keys[symbolKeyFor(c)] = true
		}
	}
	for _, want := range []string{ns + ".Animal.speak", ns + ".Dog.speak", ns + ".Animal.describe"} {
		assert.True(t, keys[want], "no chunk is keyed %q", want)
	}
}

func TestWalkTopLevel_DupNamesSuffixedAtEmission(t *testing.T) {
	chunker := NewChunker()
	defer chunker.Close()

	// Two module-level defs share a name; a third is unique (the control).
	src := []byte(`def handler():
    return 1

def handler():
    return 2

def unique_one():
    return 3
`)
	result, err := chunker.ChunkFile(context.Background(), "pkg/dup.py", src)
	require.NoError(t, err)

	const ns = "python:pkg"

	var suffixed, control []Chunk
	for _, c := range result.Chunks {
		switch {
		case strings.HasPrefix(c.Name, "handler"):
			suffixed = append(suffixed, c)
		case c.Name == "unique_one":
			control = append(control, c)
		}
	}
	require.Len(t, suffixed, 2, "both colliding defs must still be chunked")
	require.Len(t, control, 1)

	var fileContains []string
	for _, e := range edgesOfType(result.Edges, EdgeContains) {
		if e.FromID == "pkg/dup.py" {
			fileContains = append(fileContains, e.ToID)
		}
	}

	// Each colliding chunk carries a distinct hash-suffixed Name, and its
	// CONTAINS edge carries that SAME suffixed name — the agreement that makes
	// the edge resolvable.
	seen := map[string]bool{}
	for _, c := range suffixed {
		assert.Regexp(t, `^handler#[0-9a-f]{8}$`, c.Name)
		assert.False(t, seen[c.Name], "colliding chunks must get distinct names")
		seen[c.Name] = true
		assert.Contains(t, fileContains, ns+"."+c.Name)
		// The suffix is the chunk's own PathHash — the same value
		// DeduplicateChunks would append, so node IDs are unchanged.
		assert.Equal(t, "handler#"+c.PathHash, c.Name)
	}

	// Control: the uniquely-named decl is untouched on both sides. Without it,
	// a bug that suffixed everything would pass every assertion above.
	assert.Equal(t, "unique_one", control[0].Name)
	assert.NotContains(t, control[0].Name, "#")
	assert.Contains(t, fileContains, ns+".unique_one")
	for _, id := range fileContains {
		if strings.HasPrefix(id, ns+".unique_one") {
			assert.Equal(t, ns+".unique_one", id, "control edge must carry no suffix")
		}
	}
}

func TestGoNestedTypeDecl_EdgeMatchesKey(t *testing.T) {
	chunker := NewChunker()
	defer chunker.Close()

	src := []byte(`package svc

func Build() any {
	type Inner struct {
		A int
	}
	return Inner{}
}
`)
	result, err := chunker.ChunkFile(context.Background(), "pkg/svc.go", src)
	require.NoError(t, err)

	// The nested type IS chunked, and it takes the enclosing func as parent.
	var nested *Chunk
	for i, c := range result.Chunks {
		if c.Name == "Inner" {
			nested = &result.Chunks[i]
		}
	}
	require.NotNil(t, nested, "a type declared inside a func body must be chunked")
	assert.Equal(t, "Build", nested.ParentName)
	assert.Equal(t, "svc.Build.Inner", symbolKeyFor(*nested))

	// Its CONTAINS endpoints equal that key — asserted positively, with no
	// either-way branch.
	var fileContains, parentContains []string
	for _, e := range edgesOfType(result.Edges, EdgeContains) {
		switch e.FromID {
		case "pkg/svc.go":
			fileContains = append(fileContains, e.ToID)
		case "svc.Build":
			parentContains = append(parentContains, e.ToID)
		}
	}
	assert.Contains(t, fileContains, "svc.Build.Inner")
	assert.Contains(t, parentContains, "svc.Build.Inner")
}

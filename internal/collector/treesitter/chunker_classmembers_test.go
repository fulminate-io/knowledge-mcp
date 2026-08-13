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

// memberChunks returns the method_definition chunks keyed by parent, with any
// emission-time collision suffix stripped so a getter/setter pair reads as the
// two "width" entries it is.
func memberChunks(result *Result) map[string][]string {
	byParent := map[string][]string{}
	for _, c := range result.Chunks {
		if c.ChunkType != "method_definition" {
			continue
		}
		name, _, _ := strings.Cut(c.Name, "#")
		byParent[c.ParentName] = append(byParent[c.ParentName], name)
	}
	for k := range byParent {
		sort.Strings(byParent[k])
	}
	return byParent
}

func allChunkNames(result *Result) []string {
	var out []string
	for _, c := range result.Chunks {
		out = append(out, c.Name)
	}
	return out
}

// The fixtures deliberately contain BOTH shapes: an object literal whose
// shorthand members are method_definition nodes too, and real class bodies.
// The excluded kinds are therefore proven absent from a run in which the
// included kinds matched — an absence with a known positive beside it, not
// silence.
const tsClassFixture = `const opts = {
  area() { return 1; },
  get w() { return 2; },
  set w(v: number) {},
};

export class Box {
  constructor(private wide: number) {}
  area(): number { return this.wide; }
  get width(): number { return this.wide; }
  set width(v: number) { this.wide = v; }
  static make(): Box { return new Box(1); }
  async load(): Promise<void> {}
  [Symbol.iterator]() { return null; }
  prop = 3;
}

class Circle {
  area(): number { return 3; }
}

const Anon = class { hidden() { return 0; } };
`

func TestChunkTS_ClassMembersChunked(t *testing.T) {
	chunker := NewChunker()
	defer chunker.Close()

	result, err := chunker.ChunkFile(context.Background(), "web/shapes.ts", []byte(tsClassFixture))
	require.NoError(t, err)

	byParent := memberChunks(result)

	// INCLUDED: constructor, plain method, getter, setter, static, async.
	assert.Equal(t, []string{"area", "constructor", "load", "make", "width", "width"}, byParent["Box"])
	assert.Equal(t, []string{"area"}, byParent["Circle"])
	// A class EXPRESSION's body is a class_body too, so its member is chunked;
	// the expression is anonymous, so the member's parent is empty rather than
	// the variable it is assigned to.
	assert.Equal(t, []string{"hidden"}, byParent[""])

	// EXCLUDED, each present in the source above: object-literal shorthand
	// (no class_body anchor), computed names (computed_property_name, not
	// property_identifier), and class fields (public_field_definition).
	names := allChunkNames(result)
	assert.NotContains(t, names, "w", "object-literal getter/setter must not be chunked")
	assert.NotContains(t, names, "prop", "class fields must not be chunked")
	for _, n := range names {
		assert.NotContains(t, n, "Symbol.iterator", "computed-name members must not be chunked")
	}
	// The object literal's own shorthand `area` is excluded while both classes'
	// `area` are included — so exactly two area members, not three.
	areaCount := 0
	for _, members := range byParent {
		for _, m := range members {
			if m == "area" {
				areaCount++
			}
		}
	}
	assert.Equal(t, 2, areaCount, "only the two class areas are chunked, not the object literal's")

	// The getter/setter pair collides on (parent, name) and is disambiguated at
	// emission, so the two chunks carry distinct suffixed names.
	var widths []string
	for _, c := range result.Chunks {
		if c.ChunkType == "method_definition" && strings.HasPrefix(c.Name, "width") {
			widths = append(widths, c.Name)
		}
	}
	require.Len(t, widths, 2)
	assert.NotEqual(t, widths[0], widths[1], "get/set width must not share a name")

	// TSX rides a second grammar but the same query set (language.go registers
	// LangTypeScript and LangTSX against tsQueries), so the same members are
	// chunked from a .tsx file. Verified rather than assumed.
	tsxResult, err := chunker.ChunkFile(context.Background(), "web/shapes.tsx", []byte(tsClassFixture))
	require.NoError(t, err)
	tsxByParent := memberChunks(tsxResult)
	assert.Equal(t, byParent["Box"], tsxByParent["Box"])
	assert.Equal(t, byParent["Circle"], tsxByParent["Circle"])
}

const jsClassFixture = `const opts = {
  area() { return 1; },
  get w() { return 2; },
  set w(v) {},
};

export class Box {
  constructor(w) { this.w = w; }
  area() { return this.w; }
  get width() { return this.w; }
  set width(v) { this.w = v; }
  static make() { return new Box(1); }
  async load() {}
  [Symbol.iterator]() { return null; }
  #secret() { return 42; }
  prop = 3;
}

class Circle {
  area() { return 3; }
}

const Anon = class { hidden() { return 0; } };
`

func TestChunkJS_ClassMembersChunked(t *testing.T) {
	chunker := NewChunker()
	defer chunker.Close()

	result, err := chunker.ChunkFile(context.Background(), "web/shapes.js", []byte(jsClassFixture))
	require.NoError(t, err)

	byParent := memberChunks(result)

	assert.Equal(t, []string{"area", "constructor", "load", "make", "width", "width"}, byParent["Box"])
	assert.Equal(t, []string{"area"}, byParent["Circle"])
	assert.Equal(t, []string{"hidden"}, byParent[""])

	// EXCLUDED, all four present in the source: object-literal shorthand,
	// computed names, private names (#secret is a private_property_identifier),
	// and class fields (field_definition in JavaScript).
	names := allChunkNames(result)
	assert.NotContains(t, names, "w", "object-literal getter/setter must not be chunked")
	assert.NotContains(t, names, "prop", "class fields must not be chunked")
	assert.NotContains(t, names, "secret", "private-name members must not be chunked")
	assert.NotContains(t, names, "#secret")
	for _, n := range names {
		assert.NotContains(t, n, "Symbol.iterator", "computed-name members must not be chunked")
	}

	// JavaScript's class name is an identifier where TypeScript's is a
	// type_identifier; the parent resolves either way because the ascent reads
	// the name FIELD, not the child's kind.
	assert.Contains(t, byParent, "Box")
	assert.Contains(t, byParent, "Circle")
}

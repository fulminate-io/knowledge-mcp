// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// declNameFixture is one source file per registered language, reused by the
// parity gate and by the per-language expectations below.
type declNameFixture struct {
	lang Language
	path string
	src  string
}

const groovyDeclSrc = `class HelpersSpec {
  def fixtureFor(x) { return x }
}
def utility(y) { return y }
`

const luaDeclSrc = `local M = {}
function M.helper() end
function M:method() end
local function localFn() end
function globalFn() end
local a, b = 1, 2
local single = 3
`

const ocamlDeclSrc = `module Outer = struct
  module Inner = struct
    let helper x = x
  end
  type t = int
  let outer_fn () = ()
end
let a = 1
let top_level = 2
type color = Red | Green
`

const ocamlNegativeSrc = `let () = print_string "x"

let%test "login" = true

let named_one = 1
`

const elmDeclSrc = `module Main exposing (..)

type Color = Red | Green

type alias Point = { x : Int }

area : Int -> Int
area n = n * n
`

const phpDeclSrc = `<?php
namespace App\Models;
class User { function getName() {} }
`

const phpGlobalNamespaceSrc = `<?php
namespace { function f() {} }
namespace App\B { class C {} }
`

func declNameFixtures() []declNameFixture {
	return []declNameFixture{
		{LangGroovy, "pkg/a.groovy", groovyDeclSrc},
		{LangLua, "pkg/a.lua", luaDeclSrc},
		{LangOCaml, "pkg/a.ml", ocamlDeclSrc},
		{LangElm, "pkg/A.elm", elmDeclSrc},
		{LangPHP, "pkg/a.php", phpDeclSrc},
	}
}

// TestDeclNameRegistry is the catcher for a resolver written but never
// registered — a mistake no compiler and no other test can see, because an
// unregistered resolver is simply never called.
func TestDeclNameRegistry(t *testing.T) {
	want := []Language{LangGroovy, LangLua, LangOCaml, LangElm, LangPHP}
	for _, lang := range want {
		assert.Contains(t, declNameResolvers, lang, "%s must register a declNameResolver", lang)
	}
	// Cardinality is asserted against a literal constant rather than against
	// len(want), so adding a language to one list and not the other cannot
	// pass. Elixir is the sixth and is asserted by name in
	// TestDeclNameRegistryWithElixir; it registers separately because its
	// resolver is unreachable until the Elixir TopLevel query stops binding a
	// name of its own.
	assert.Len(t, declNameResolvers, 6)
}

// TestDeclNameChunkSetUnchanged is the parity gate: the resolvers may add a
// Name and may change NOTHING else. It compares the ordered
// (ChunkType, StartByte, EndByte) triples with the registry populated against
// the same triples with it emptied, and requires at least one Name to differ so
// a build where every resolver returned "" cannot pass the parity half
// trivially.
func TestDeclNameChunkSetUnchanged(t *testing.T) {
	type triple struct {
		chunkType  string
		start, end int
	}
	shape := func(result *Result) []triple {
		out := make([]triple, 0, len(result.Chunks))
		for _, c := range result.Chunks {
			out = append(out, triple{c.ChunkType, c.StartByte, c.EndByte})
		}
		return out
	}
	names := func(result *Result) []string {
		out := make([]string, 0, len(result.Chunks))
		for _, c := range result.Chunks {
			out = append(out, c.Name)
		}
		return out
	}

	for _, fx := range declNameFixtures() {
		t.Run(string(fx.lang), func(t *testing.T) {
			withResolvers := chunkFile(t, fx.path, fx.src)

			populated := declNameResolvers
			declNameResolvers = map[Language]declNameResolver{}
			without := chunkFile(t, fx.path, fx.src)
			declNameResolvers = populated

			require.Len(t, without.Chunks, len(withResolvers.Chunks),
				"a resolver must never add or drop a chunk")
			assert.Equal(t, shape(without), shape(withResolvers),
				"chunk kinds and byte ranges must be identical with and without the registry")
			assert.NotEqual(t, names(without), names(withResolvers),
				"KNOWN-POSITIVE CONTROL: at least one Name must differ, or the parity half proves nothing")
		})
	}
}

// chunkKindNames returns the ordered (ChunkType, Name) pairs. Order matters: a
// resolver that names the wrong node of the right kind produces the right SET
// and the wrong sequence.
func chunkKindNames(result *Result) [][2]string {
	out := make([][2]string, 0, len(result.Chunks))
	for _, c := range result.Chunks {
		out = append(out, [2]string{c.ChunkType, c.Name})
	}
	return out
}

// TestDeclNameFixtures pins each resolver against a fixture whose expectations
// were derived by running that language's own TopLevel query over it. Each
// language's negative case is asserted POSITIVELY — the chunk exists at its
// expected byte range and carries an empty Name — because asserting merely that
// no wrong name appears would also pass in a build where the fixture failed to
// chunk at all.
func TestDeclNameFixtures(t *testing.T) {
	t.Run("groovy", func(t *testing.T) {
		result := chunkFile(t, "pkg/a.groovy", groovyDeclSrc)
		assert.Equal(t, [][2]string{
			{"class_definition", "HelpersSpec"},
			{"function_definition", "fixtureFor"},
			{"function_definition", "utility"},
		}, chunkKindNames(result))
		// The class half is the one that is easy to omit: without a NAME the
		// class chunk is skipped by recordSymbol and the parent-to-member edge
		// its methods emit resolves to nothing on the from side.
		assert.Equal(t, "HelpersSpec", result.Chunks[1].ParentName)
	})

	t.Run("lua", func(t *testing.T) {
		result := chunkFile(t, "pkg/a.lua", luaDeclSrc)
		assert.Equal(t, [][2]string{
			{"variable_declaration", "M"},
			{"function_statement", "M.helper"}, // qualified form preserved verbatim
			{"function_statement", "M:method"}, // the colon is inert to edge resolution
			{"function_statement", "localFn"},  // name: binds a plain identifier here
			{"function_statement", "globalFn"},
			{"variable_declaration", ""}, // NEGATIVE: local a, b = 1, 2
			{"variable_declaration", "single"},
		}, chunkKindNames(result))

		// NEGATIVE CASE, asserted positively: the multi-declarator statement is
		// still chunked over its full range and simply carries no name, rather
		// than being named after its first variable while spanning two.
		neg := result.Chunks[5]
		assert.Equal(t, "variable_declaration", neg.ChunkType)
		assert.Empty(t, neg.Name)
		// The Lua grammar's statement node starts at the preceding newline, so
		// the span is asserted as the chunker actually reports it.
		assert.Equal(t, "\nlocal a, b = 1, 2", luaDeclSrc[neg.StartByte:neg.EndByte])
	})

	t.Run("ocaml", func(t *testing.T) {
		result := chunkFile(t, "pkg/a.ml", ocamlDeclSrc)
		assert.Equal(t, [][2]string{
			{"module_definition", "Outer"},
			{"module_definition", "Inner"},
			{"value_definition", "helper"},
			{"type_definition", "t"},
			{"value_definition", "outer_fn"},
			{"value_definition", "a"},
			{"value_definition", "top_level"},
			{"type_definition", "color"},
		}, chunkKindNames(result))

		// NEGATIVE CASES, asserted positively. These two are the whole reason
		// this is a resolver and not a query edit: a query requiring
		// pattern:(value_name) would delete both chunks outright.
		neg := chunkFile(t, "pkg/n.ml", ocamlNegativeSrc)
		assert.Equal(t, [][2]string{
			{"value_definition", ""},          // let () = ... — the pattern is unit
			{"value_definition", ""},          // let%test "login" = ... — the pattern is a string
			{"value_definition", "named_one"}, // known-positive in the same file
		}, chunkKindNames(neg))
		assert.Equal(t, `let () = print_string "x"`, ocamlNegativeSrc[neg.Chunks[0].StartByte:neg.Chunks[0].EndByte])
		assert.Equal(t, `let%test "login" = true`, ocamlNegativeSrc[neg.Chunks[1].StartByte:neg.Chunks[1].EndByte])
	})

	t.Run("elm", func(t *testing.T) {
		result := chunkFile(t, "pkg/A.elm", elmDeclSrc)
		assert.Equal(t, [][2]string{
			{"type_declaration", "Color"},
			{"type_alias_declaration", "Point"},
			// The first lower_case_identifier under function_declaration_left
			// is the function; the ones after it are its parameters.
			{"value_declaration", "area"},
		}, chunkKindNames(result))
	})

	t.Run("php", func(t *testing.T) {
		result := chunkFile(t, "pkg/a.php", phpDeclSrc)
		assert.Equal(t, [][2]string{
			{"namespace_definition", `App\Models`},
			{"class_declaration", "User"},
			{"method_declaration", "getName"},
		}, chunkKindNames(result))

		// NEGATIVE CASE, asserted positively: the unnamed global namespace keeps
		// its chunk and its byte range, alongside a named namespace in the same
		// file as the known-positive control.
		global := chunkFile(t, "pkg/b.php", phpGlobalNamespaceSrc)
		assert.Equal(t, [][2]string{
			{"namespace_definition", ""},
			{"function_definition", "f"},
			{"namespace_definition", `App\B`},
			{"class_declaration", "C"},
		}, chunkKindNames(global))
		assert.Equal(t, "namespace { function f() {} }",
			phpGlobalNamespaceSrc[global.Chunks[0].StartByte:global.Chunks[0].EndByte])
	})
}

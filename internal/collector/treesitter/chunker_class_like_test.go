// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parentMemberEdges returns the parent-to-member CONTAINS edges in a result —
// every CONTAINS edge whose source is a declaration rather than the file
// itself. The file-to-declaration edge is addressed positionally from the
// file path and is emitted for every chunk, so excluding it is what leaves
// the container ascent's own output behind.
func parentMemberEdges(result *Result, path string) []Edge {
	var out []Edge
	for _, e := range result.Edges {
		if e.Type == EdgeContains && e.FromID != path {
			out = append(out, e)
		}
	}
	return out
}

// TestModuleKindIsRubyOnly pins that a TypeScript `module X {}` block does NOT
// parent its members, while Ruby's `module X` still does.
//
// The container-ascent admission is keyed on the bare tree-sitter node kind
// `module`, a spelling five grammars share, so an admission made for Ruby
// reaches TypeScript's unrelated `module` block: `module Sink { export
// function write(): void {} }` yields the node <file>:Sink.write, parenting a
// TypeScript module's function as if the module were a class-like container.
//
// ns.ts IS THE POST-FIX ORACLE. TypeScript's `namespace Sink { ... }` parses
// as kind internal_module, which no admission holds, so it already produces
// the correct containment. Once the leak is closed the `module` form must
// produce byte-identical behavior to the `namespace` form.
//
// m.rb IS THE KNOWN-POSITIVE CONTROL that keeps the negatives from being
// vacuous: it distinguishes "module no longer reaches TypeScript" from
// "module was removed outright". m.py and M.elm are characterization guards —
// both grammars put a `module` node on the tree (python's is the file root,
// elm's is a keyword leaf) and neither names anything today.
func TestModuleKindIsRubyOnly(t *testing.T) {
	t.Run("typescript module block", func(t *testing.T) {
		const path = "mod.ts"
		const src = "module Sink {\n  export function write(): void {}\n}\n"

		got, _ := chunkNameParents(t, path, src)
		require.Contains(t, got, "write", "control: the module's member is chunked at all")
		assert.Empty(t, got["write"], "typescript module block must not parent its members")

		edges := parentMemberEdges(chunkFile(t, path, src), path)
		assert.Empty(t, edges, "typescript module block must emit no parent-to-member CONTAINS edge")
	})

	t.Run("tsx module block", func(t *testing.T) {
		// .tsx rides the SEPARATE typescript/tsx grammar, so it is a second
		// independent defect site rather than a duplicate of the .ts leg.
		const path = "mod.tsx"
		const src = "module Sink {\n  export function write(): void {}\n}\n"

		got, _ := chunkNameParents(t, path, src)
		require.Contains(t, got, "write", "control: the module's member is chunked at all")
		assert.Empty(t, got["write"], "tsx module block must not parent its members")

		edges := parentMemberEdges(chunkFile(t, path, src), path)
		assert.Empty(t, edges, "tsx module block must emit no parent-to-member CONTAINS edge")
	})

	t.Run("typescript namespace block is the oracle", func(t *testing.T) {
		const path = "ns.ts"
		const src = "namespace Sink {\n  export function write(): void {}\n}\n"

		got, _ := chunkNameParents(t, path, src)
		require.Contains(t, got, "write", "control: the namespace's member is chunked at all")
		assert.Empty(t, got["write"], "typescript namespace block must not parent its members")

		edges := parentMemberEdges(chunkFile(t, path, src), path)
		assert.Empty(t, edges, "typescript namespace block must emit no parent-to-member CONTAINS edge")
	})

	t.Run("ruby module block still parents", func(t *testing.T) {
		const path = "m.rb"
		const src = "module Sink\n  def write\n  end\nend\n"

		got, _ := chunkNameParents(t, path, src)
		require.Contains(t, got, "write", "control: the module's member is chunked at all")
		assert.Equal(t, "Sink", got["write"], "ruby module block must still parent its members")

		edges := parentMemberEdges(chunkFile(t, path, src), path)
		assert.NotEmpty(t, edges,
			"known positive: ruby still emits a parent-to-member CONTAINS edge, so the empty "+
				"assertions above measure a real absence rather than a dead probe")
	})

	t.Run("python module is the file root", func(t *testing.T) {
		const path = "m.py"
		const src = "def top():\n    pass\n\nclass K:\n    def inner(self):\n        pass\n"

		got, _ := chunkNameParents(t, path, src)
		require.Contains(t, got, "top", "control: the top-level function is chunked at all")
		require.Contains(t, got, "inner", "control: the class member is chunked at all")
		assert.Empty(t, got["top"], "python's file-root module must not parent a top-level declaration")
		assert.Equal(t, "K", got["inner"], "known positive: a python class still parents its members")
	})

	t.Run("elm module is a keyword leaf", func(t *testing.T) {
		const path = "M.elm"
		const src = "module M exposing (..)\n\nfoo : Int\nfoo = 1\n"

		got, _ := chunkNameParents(t, path, src)
		require.Contains(t, got, "foo", "control: the value declaration is chunked at all")
		assert.Empty(t, got["foo"], "elm's module keyword must not parent a value declaration")
	})
}

// TestClassLikeAdmissionByLanguage pins the MEASURED containment every
// admitting language's row produces, one fixture per (language, kind) pair.
//
// THIS IS THE BEHAVIORAL HALF OF THE CENSUS, and it exists because the
// structural tests cannot cover it. TestClassLikeByLangMatchesGrammars proves
// every pair names a kind the grammar declares and
// TestClassLikeByLangCoversEveryRegisteredLanguage proves every language
// answered; neither can see a row that DROPS a kind the language really uses.
// That failure silently unparents real members while the whole suite stays
// green, so each row is pinned here by the containment it actually produces.
//
// EVERY EXPECTATION WAS MEASURED by running the language's own TopLevel query
// over the fixture through the real Chunker — never derived by reading the
// grammar. This is the discipline TestContainerFixtures states at
// chunker_container_test.go:45, and it is not optional here: five of these
// pairs chunked NO member until the query arms that capture their members
// landed, so a table written from the grammar rather than from a run would
// have recorded four languages as inert that in fact parent real members.
//
// The chunk COUNT is asserted alongside the parent map because the map
// collapses duplicate names by construction — the count is what makes a
// vanished chunk visible.
func TestClassLikeAdmissionByLanguage(t *testing.T) {
	cases := []struct {
		name      string
		path      string
		src       string
		want      map[string]string
		wantCount int
	}{
		{"python class_definition", "p.py", "class Sink:\n    def write(self):\n        pass\n",
			map[string]string{"write": "Sink"}, 2},
		{"scala class_definition", "s.scala", "class Sink { def write() = 1 }\n",
			map[string]string{"write": "Sink"}, 2},
		{"scala object_definition", "s.scala", "object Sink { def write() = 1 }\n",
			map[string]string{"write": "Sink"}, 2},
		// A trait's abstract members chunk, so the trait parents them.
		{"scala trait_definition", "s.scala", "trait Sink { def write(): Unit }\n",
			map[string]string{"write": "Sink"}, 2},
		{"groovy class_definition", "g.groovy", "class Sink {\n  def write() { }\n}\n",
			map[string]string{"write": "Sink"}, 2},
		{"ocaml module_binding", "o.ml", "module M = struct\n  let write x = x\nend\n",
			map[string]string{"write": "M"}, 2},
		{"typescript class_declaration", "c.ts", "class Sink { write() { } }\n",
			map[string]string{"write": "Sink"}, 2},
		// An interface's method_signature members chunk, so the interface
		// parents them.
		{"typescript interface_declaration", "c.ts", "interface I { write(): void }\n",
			map[string]string{"write": "I"}, 2},
		{"tsx class_declaration", "c.tsx", "class Sink { write() { } }\n",
			map[string]string{"write": "Sink"}, 2},
		{"tsx interface_declaration", "c.tsx", "interface I { write(): void }\n",
			map[string]string{"write": "I"}, 2},
		{"javascript class_declaration", "c.js", "class Sink {\n  write() { }\n}\n",
			map[string]string{"write": "Sink"}, 2},
		{"java class_declaration", "C.java", "class Sink { void write() { } }\n",
			map[string]string{"write": "Sink"}, 2},
		{"java interface_declaration", "C.java", "interface Sink { void write(); }\n",
			map[string]string{"write": "Sink"}, 2},
		{"java enum_declaration", "C.java", "enum Suit { H; void label() { } }\n",
			map[string]string{"label": "Suit"}, 2},
		{"csharp class_declaration", "C.cs", "class Sink { void Write() { } }\n",
			map[string]string{"Write": "Sink"}, 2},
		{"csharp interface_declaration", "C.cs", "interface ISink { void Write(); }\n",
			map[string]string{"Write": "ISink"}, 2},
		{"csharp struct_declaration", "C.cs", "struct P { void Write() { } }\n",
			map[string]string{"Write": "P"}, 2},
		// Containment is SINGLE-ANCESTOR: the method takes the class, not the
		// namespace above it.
		{"csharp namespace_declaration", "C.cs", "namespace App { class Sink { void W() { } } }\n",
			map[string]string{"Sink": "App", "W": "Sink"}, 3},
		{"php class_declaration", "c.php", "<?php\nclass Sink { function write() { } }\n",
			map[string]string{"write": "Sink"}, 2},
		{"php trait_declaration", "c.php", "<?php\ntrait T { function write() { } }\n",
			map[string]string{"write": "T"}, 2},
		{"php enum_declaration", "c.php",
			"<?php\nenum Suit {\n  case Hearts;\n  public function label(): string { return 'x'; }\n}\n",
			map[string]string{"label": "Suit"}, 2},
		{"php namespace_definition", "c.php", "<?php\nnamespace App { class Sink { function w() { } } }\n",
			map[string]string{"Sink": "App", "w": "Sink"}, 3},
		{"swift class_declaration", "c.swift", "class Sink { func write() { } }\n",
			map[string]string{"write": "Sink"}, 2},
		// A protocol's function declarations chunk, so the protocol parents them.
		{"swift protocol_declaration", "c.swift", "protocol Sink { func write() }\n",
			map[string]string{"write": "Sink"}, 2},
		{"kotlin class_declaration", "c.kt", "class Sink { fun write() { } }\n",
			map[string]string{"write": "Sink"}, 2},
		{"kotlin object_declaration", "c.kt", "object Sink { fun write() { } }\n",
			map[string]string{"write": "Sink"}, 2},
		{"cpp class_specifier", "c.cpp", "class Sink { void write() { } };\n",
			map[string]string{"write": "Sink"}, 2},
		{"cpp struct_specifier", "c.cpp", "struct Sink {\n  void write() { }\n};\n",
			map[string]string{"write": "Sink"}, 2},
		{"cpp namespace_definition", "c.cpp", "namespace App { void write() { } }\n",
			map[string]string{"write": "App"}, 2},
		{"rust mod_item", "c.rs", "mod sink { pub fn write() { } }\n",
			map[string]string{"write": "sink"}, 2},
		// The struct and its impl collide on the name S, so both container
		// chunks take a path-hash suffix (normalized to a bare "#" here) and
		// collapse to one map key. The MEMBER's own ParentName stays as the
		// source wrote it, unsuffixed — which is why the count, not the map, is
		// what shows both containers survived.
		{"rust impl_item", "c.rs", "struct S;\nimpl S { fn write(&self) { } }\n",
			map[string]string{"write": "S", "S#": ""}, 3},
		// A trait's method specs chunk, so the trait parents them.
		{"rust trait_item", "c.rs", "trait Sink { fn write(&self); }\n",
			map[string]string{"write": "Sink"}, 2},
		// READ THIS ROW TWICE. It asserts BOTH directions at once: the
		// function-pointer FIELD takes the struct, and the top-level function
		// beside it takes nothing. Dropping the C row would leave `write` -> ""
		// satisfied while breaking `flush` -> "Sink", so the row is
		// self-protecting rather than trivially green.
		{"c struct_specifier", "c.c",
			"struct Sink {\n  int n;\n  void (*flush)(int);\n};\n\nvoid write(void) { }\n",
			map[string]string{"flush": "Sink", "write": ""}, 3},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, count := chunkNameParents(t, tc.path, tc.src)
			assert.Equal(t, tc.wantCount, count, "chunk count for %s", tc.path)
			for name, parent := range tc.want {
				require.Contains(t, got, name,
					"control: %q must be chunked at all, or the parent assertion below is vacuous", name)
				assert.Equal(t, parent, got[name], "ParentName of %q", name)
			}
		})
	}

	// The C row pins the preservation DECISION as well as its effect: the pair
	// was recorded as inert before its field arm landed, and an admission kept
	// on measurement rather than on absence is one a future census must not
	// quietly drop.
	assert.True(t, classLikeByLang[LangC]["struct_specifier"],
		"c's struct_specifier admission parents a real member and must be preserved")
}

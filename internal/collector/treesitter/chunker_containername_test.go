// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nodesOfType returns every node of the given kind in pre-order, so a fixture
// holding several containers of one kind can address them by position.
func nodesOfType(n *sitter.Node, kind string) []*sitter.Node {
	var out []*sitter.Node
	if n.Type() == kind {
		out = append(out, n)
	}
	for i := range int(n.NamedChildCount()) {
		out = append(out, nodesOfType(n.NamedChild(i), kind)...)
	}
	return out
}

// TestContainerNameSources exercises containerName directly against each of its
// three sources, plus the two shapes that must resolve to "" so the ascent keeps
// walking. Every expectation here was derived by parsing the snippet and reading
// the node's actual fields, not by assuming symmetry with another grammar.
func TestContainerNameSources(t *testing.T) {
	cases := []struct {
		name string
		lang Language
		src  string
		kind string
		idx  int
		want string
	}{
		// Source 1 — the name: field.
		{"rust mod_item binds name:", LangRust, "mod outer { }\n", "mod_item", 0, "outer"},
		{"cpp namespace binds name:", LangCPP, "namespace inner { void f() {} }\n", "namespace_definition", 0, "inner"},
		{"csharp namespace keeps the dotted qualified_name", LangCSharp,
			"namespace App.Models\n{\n  public class User { }\n}\n", "namespace_declaration", 0, "App.Models"},
		{"ocaml module_binding binds name:", LangOCaml, "module Inner = struct\n  let helper x = x\nend\n",
			"module_binding", 0, "Inner"},
		// The C++17 nested spelling arrives as ONE name node, so the full path
		// is kept for free — no chain-building is involved.
		{"cpp a::b arrives as one name node", LangCPP, "namespace a::b { struct Deep { }; }\n",
			"namespace_definition", 0, "a::b"},

		// Source 2 — the type: field, accepted only for a type_identifier.
		{"rust impl binds type:", LangRust, "impl Thing { }\n", "impl_item", 0, "Thing"},
		// type: binds the TYPE being implemented, never the trait, so an
		// inherent impl and a trait impl resolve to the same container name.
		{"rust trait impl still resolves to the type", LangRust, "impl Speak for Thing { }\n", "impl_item", 0, "Thing"},

		// Source 3 — the positional scan. Kotlin's grammar attaches no field
		// name to any node, and `modifiers` occupies index 0 on both of these,
		// so a first-named-child rule would return "" for both.
		{"kotlin data class scans past modifiers", LangKotlin, "data class Point(val x: Int, val y: Int)\n",
			"class_declaration", 0, "Point"},
		{"kotlin private class scans past modifiers", LangKotlin, "private class Dog : Animal(\"dog\") { fun bark() {} }\n",
			"class_declaration", 0, "Dog"},
		{"kotlin object_declaration resolves positionally", LangKotlin, "object Registry { fun register() {} }\n",
			"object_declaration", 0, "Registry"},

		// The two shapes that must stay empty. Both are asserted positively —
		// the node exists and containerName declines it — rather than by the
		// absence of a chunk somewhere downstream.
		{"generic rust impl is rejected", LangRust, "impl<T> Gen<T> {\n    pub fn get(&self) -> &T { &self.t }\n}\n",
			"impl_item", 0, ""},
		{"anonymous cpp namespace has no name", LangCPP, "namespace { void anonFn() {} }\n",
			"namespace_definition", 0, ""},
	}

	p := NewParser()
	defer p.Close()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := []byte(tc.src)
			tree, err := p.Parse(context.Background(), src, tc.lang)
			require.NoError(t, err)
			defer tree.Close()

			found := nodesOfType(tree.RootNode(), tc.kind)
			require.Greater(t, len(found), tc.idx, "fixture must contain a %s at index %d", tc.kind, tc.idx)
			assert.Equal(t, tc.want, containerName(found[tc.idx], src))
		})
	}

	// Known-positive control for the two empty expectations above: the same
	// helper on the same grammars does return a name, so an empty result is a
	// rejection rather than a probe that resolves nothing at all.
	tree, err := p.Parse(context.Background(), []byte("impl Thing { }\n"), LangRust)
	require.NoError(t, err)
	defer tree.Close()
	require.NotEmpty(t, containerName(nodesOfType(tree.RootNode(), "impl_item")[0], []byte("impl Thing { }\n")))
}

// TestClassLikeTypesContainers pins the five namespace-style container kinds
// admitted alongside the class-like ones, the kinds that must stay out of every
// row, and the `module` spelling's per-language split.
func TestClassLikeTypesContainers(t *testing.T) {
	// Admitted, each under the language that owns the kind: measured as a true
	// named ancestor of the declarations that language's TopLevel query chunks.
	for _, tc := range []struct {
		lang Language
		kind string
	}{
		{LangRust, "mod_item"},
		{LangRust, "impl_item"}, // named through containerName's type: source
		{LangCPP, "namespace_definition"},
		{LangPHP, "namespace_definition"}, // braced form
		{LangCSharp, "namespace_declaration"},
		{LangOCaml, "module_binding"},
	} {
		assert.True(t, classLikeByLang[tc.lang][tc.kind],
			"classLikeByLang[%q] must hold %q", tc.lang, tc.kind)
	}

	// THE `module` SPELLING IS THE REASON THE TABLE HAS A LANGUAGE DIMENSION.
	// Five grammars declare the kind; only Ruby's names a container of the
	// members below it. A TypeScript or TSX `module X {}` block is a namespace
	// and its functions belong to the file, so those two rows must NOT hold it
	// — and Ruby's must, which is what keeps the two negatives from being
	// satisfied by an admission deleted outright.
	assert.False(t, classLikeByLang[LangTypeScript]["module"],
		"a typescript module block must not be a class-like container")
	assert.False(t, classLikeByLang[LangTSX]["module"],
		"a tsx module block must not be a class-like container")
	assert.True(t, classLikeByLang[LangRuby]["module"],
		"known positive: ruby's module is still admitted")

	for _, lang := range RegisteredLanguages() {
		// Excluded: Go's containers, whose absence is what makes Go unchanged
		// by construction rather than by measurement.
		for _, kind := range []string{"type_declaration", "type_spec", "struct_type", "interface_type"} {
			assert.False(t, classLikeByLang[lang][kind],
				"classLikeByLang[%q] must not hold Go's %q", lang, kind)
		}

		// Excluded: Elixir's container and member are both the call kind, so no
		// kind-based rule can tell defmodule from def.
		assert.False(t, classLikeByLang[lang]["call"],
			"Elixir's call kind must stay out of classLikeByLang[%q]", lang)

		// Excluded: C#'s file-scoped namespace is a SIBLING of the declarations
		// it names, so no upward walk reaches it; it is resolved from the
		// file's own declaration instead.
		assert.False(t, classLikeByLang[lang]["file_scoped_namespace_declaration"],
			"a file-scoped namespace is not an ancestor of the types it names")
	}
}

// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// armFixture builds one file's Result carrying the import table an arm reads.
// Every chunk of one file carries the same context, so one chunk suffices.
func armFixture(path string, lang Language, bindings ...ImportBinding) *Result {
	return &Result{
		FilePath: path,
		Language: lang,
		Chunks:   []Chunk{{Context: ChunkContext{ImportBindings: bindings}}},
	}
}

// declFile builds a target file whose top-level declaration names decl, which
// is what an arm's byPath lookup has to find for a candidate path to resolve.
func declFile(path string, lang Language, decl string) *Result {
	return &Result{
		FilePath: path,
		Language: lang,
		Chunks:   []Chunk{{Name: decl}},
	}
}

// TestBindsResolverArms pins each registered arm's mapping from an import onto
// the SCOPE it binds into, dispatching through the production registry rather
// than calling the functions directly, so a language registered under the wrong
// constant fails here.
//
// THE OUT-OF-REPO CASES ARE NOT OMISSIONS TO FIX. An arm records a bind for
// every import it can attribute even when no candidate path exists, carrying
// the first candidate's scope; the index then reports that scope empty and the
// reference TERMINATES instead of manufacturing a dynamic edge to a local of
// the same name. Skipping is the only way to fail the contract.
func TestBindsResolverArms(t *testing.T) {
	rc := &RepoContext{}

	t.Run("java", func(t *testing.T) {
		byPath := map[string]*Result{"a/b/C.java": declFile("a/b/C.java", LangJava, "C")}
		self := armFixture("app/Main.java", LangJava,
			ImportBinding{Specifier: "a.b", Imported: "C", Local: "C", Kind: ImportNamed},
			ImportBinding{Specifier: "x.y", Kind: ImportWildcard})

		got := BindsFor(rc, byPath, self)
		assert.Equal(t, map[string]Bind{"C": {Scope: "ns:java:a_b"}}, got.Binds,
			"a wildcard import binds no name and must not be expanded into a guess")
	})

	t.Run("java_static_import_binds_the_type_path", func(t *testing.T) {
		// `import static a.b.C.d` names the TYPE in its specifier and a member
		// in its name, so the TYPE's path is the specifier's own — the second
		// candidate — and the PACKAGE is everything before that last segment.
		// The first candidate, a/b/C/d.java, is absent, and that absence is the
		// whole evidence that this is the static reading.
		//
		// THE Container IS PART OF THIS ROW'S SUBJECT NOW: resolving through the
		// second candidate IS the static reading, and that reading is the only
		// one in which the bound name is a member of something. What the field
		// then buys the resolution rungs is proved in
		// TestJVMStaticImportRecordsContainer beside this file.
		byPath := map[string]*Result{"a/b/C.java": declFile("a/b/C.java", LangJava, "d")}
		self := armFixture("app/Main.java", LangJava,
			ImportBinding{Specifier: "a.b.C", Imported: "d", Local: "d", Kind: ImportNamed})

		got := BindsFor(rc, byPath, self)
		assert.Equal(t, map[string]Bind{"d": {Scope: "ns:java:a_b", Container: "C"}}, got.Binds)
	})

	t.Run("java_out_of_repo_records_the_bind_anyway", func(t *testing.T) {
		self := armFixture("app/Main.java", LangJava,
			ImportBinding{Specifier: "java.util", Imported: "List", Local: "List", Kind: ImportNamed})

		got := BindsFor(rc, nil, self)
		require.Contains(t, got.Binds, "List",
			"omitting an out-of-repo bind is what lets a bare List.of() reach a LOCAL List")
		assert.Equal(t, "ns:java:java_util", got.Binds["List"].Scope,
			"neither candidate resolves, so the plain reading stands and the specifier IS the package")
	})

	t.Run("kotlin_alias_carries_the_declared_name", func(t *testing.T) {
		byPath := map[string]*Result{"a/b/D.kt": declFile("a/b/D.kt", LangKotlin, "D")}
		self := armFixture("app/Main.kt", LangKotlin,
			ImportBinding{Specifier: "a.b", Imported: "D", Local: "E", Kind: ImportNamed})

		got := BindsFor(rc, byPath, self)
		assert.Equal(t, map[string]Bind{"E": {Scope: "ns:kotlin:a_b", Name: "D"}}, got.Binds,
			"the reference writes E and the target declares D")
	})

	t.Run("scala", func(t *testing.T) {
		byPath := map[string]*Result{"a/D.scala": declFile("a/D.scala", LangScala, "D")}
		self := armFixture("app/Main.scala", LangScala,
			ImportBinding{Specifier: "a", Imported: "D", Local: "F", Kind: ImportNamed})

		got := BindsFor(rc, byPath, self)
		assert.Equal(t, map[string]Bind{"F": {Scope: "ns:scala:a", Name: "D"}}, got.Binds)
	})

	t.Run("python_package_resolves_through_init", func(t *testing.T) {
		byPath := map[string]*Result{"x/y/__init__.py": declFile("x/y/__init__.py", LangPython, "a")}
		self := armFixture("app.py", LangPython,
			ImportBinding{Specifier: "x.y", Imported: "a", Local: "b", Kind: ImportNamed})

		got := BindsFor(rc, byPath, self)
		assert.Equal(t, map[string]Bind{"b": {Scope: "file:x/y/__init__.py", Name: "a"}}, got.Binds,
			"a python package directory is reached through its __init__.py")
	})

	t.Run("rust_candidate_ladder", func(t *testing.T) {
		byPath := map[string]*Result{"src/a/b/mod.rs": declFile("src/a/b/mod.rs", LangRust, "b")}
		self := armFixture("app/main.rs", LangRust,
			ImportBinding{Specifier: "a", Imported: "b", Local: "z", Kind: ImportNamed})

		got := BindsFor(rc, byPath, self)
		assert.Equal(t, map[string]Bind{"z": {Scope: "file:src/a/b/mod.rs", Name: "b"}}, got.Binds,
			"the fourth candidate layout resolves when the first three are absent")
	})

	t.Run("swift_records_a_scope_that_matches_nothing", func(t *testing.T) {
		self := armFixture("app.swift", LangSwift,
			ImportBinding{Specifier: "Ext", Imported: "Helper", Local: "Helper", Kind: ImportNamed},
			ImportBinding{Specifier: "Foundation", Kind: ImportWildcard})

		got := BindsFor(rc, nil, self)
		assert.Equal(t, map[string]Bind{"Helper": {Scope: "file:"}}, got.Binds,
			"a swift import names a module and the source carries no path to derive")
	})

	t.Run("csharp_binds_into_the_declared_namespace", func(t *testing.T) {
		self := armFixture("app/Main.cs", LangCSharp,
			ImportBinding{Specifier: "Foo", Imported: "Bar", Local: "X", Kind: ImportNamed},
			ImportBinding{Specifier: "Foo.Bar", Kind: ImportWildcard})

		got := BindsFor(rc, nil, self)
		assert.Equal(t, map[string]Bind{"X": {Scope: "ns:csharp:Foo", Name: "Bar"}}, got.Binds,
			"a plain using names a namespace and binds nothing; only the alias form binds")
	})

	t.Run("php_binds_into_the_declared_namespace", func(t *testing.T) {
		self := armFixture("app/main.php", LangPHP,
			ImportBinding{Specifier: "Foo", Imported: "Bar", Local: "Qux", Kind: ImportNamed})

		got := BindsFor(rc, nil, self)
		assert.Equal(t, map[string]Bind{"Qux": {Scope: "ns:php:Foo", Name: "Bar"}}, got.Binds)
	})

	t.Run("c_include_binds_every_top_level_header_declaration", func(t *testing.T) {
		header := &Result{
			FilePath: "inc/a.h",
			Language: LangC,
			Chunks: []Chunk{
				{Name: "helper"},
				{Name: "other"},
				{Name: "member", ParentName: "Thing"},
				{Name: ""},
			},
		}
		self := &Result{
			FilePath: "src/main.c",
			Language: LangC,
			Chunks: []Chunk{{Context: ChunkContext{
				Imports: []string{"../inc/a.h", "<stdio.h>", "missing.h"},
			}}},
		}

		got := BindsFor(rc, map[string]*Result{"inc/a.h": header}, self)
		assert.Equal(t, map[string]Bind{
			"helper": {Scope: "file:inc/a.h"},
			"other":  {Scope: "file:inc/a.h"},
		}, got.Binds,
			"an angle include and an unresolvable include record nothing; a member is unreachable through a bind")
	})

	t.Run("cpp_shares_the_include_arm", func(t *testing.T) {
		header := declFile("inc/a.hpp", LangCPP, "helper")
		self := &Result{
			FilePath: "src/main.cpp",
			Language: LangCPP,
			Chunks:   []Chunk{{Context: ChunkContext{Imports: []string{"../inc/a.hpp"}}}},
		}

		got := BindsFor(rc, map[string]*Result{"inc/a.hpp": header}, self)
		assert.Equal(t, map[string]Bind{"helper": {Scope: "file:inc/a.hpp"}}, got.Binds)
	})

	t.Run("an_unregistered_language_binds_nothing", func(t *testing.T) {
		// THE KNOWN-NEGATIVE CONTROL for every equality above: ruby is dynamic
		// by language property and registers no arm, so a fixture carrying the
		// same shape comes back empty. Without it, a BindsFor that returned an
		// empty map for everything could not be told from correct work.
		self := armFixture("app/main.rb", LangRuby,
			ImportBinding{Specifier: "a", Imported: "B", Local: "B", Kind: ImportNamed})

		got := BindsFor(rc, nil, self)
		assert.Empty(t, got.Binds)
	})
}

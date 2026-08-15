// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// matrixRow is one language's resolution proof: a fixture of at least two
// files, and whether the language PARTICIPATES in resolution at all.
//
// A participating language names its declarations, so they enter the index and
// its references are resolved. A non-participating one names nothing — its
// query set captures structure with no @name — so nothing is indexed and no
// reference can bind. Asserting "no wrong edges" for those would be vacuous,
// so their row asserts the EXEMPTION instead: chunks exist, and every one is
// unnamed.
type matrixRow struct {
	participates bool
	files        []fixtureFile
	// anchored records the live-census shape a fixture reproduces, so a row
	// cannot quietly pass on a shape that never occurs in real source.
	anchored string

	// importBound, when non-empty, is the repo-relative path this row's
	// fixture imports FROM. Setting it turns the import exemption at (3) from
	// a clause nothing takes into an asserted POSITIVE: at least one reference
	// must resolve through RuleUnqualifiedImport onto a declaration in THAT
	// file's scope rather than the reference's own.
	//
	// It is set only for languages with a registered BindsResolver arm. For
	// every other row the exemption stays untaken, which is the honest state:
	// their references can only bind through a scope rule.
	importBound string

	// expect is the outcome the row's COLLIDING PAIR must produce. The zero
	// value means "no outcome expectation", for a row this ticket does not
	// touch.
	expect rowExpect
}

// rowExpect is the resolution one designated reference must reach: the rung
// that fires, how many candidates survive, and the Method the emitted group
// carries.
//
// THE METHOD IS ASSERTED, NOT INFERRED FROM THE COUNT. A row asserting only
// "more than one candidate" is satisfied by an AMBIGUOUS group where the
// language means a DYNAMIC one — a closed "exactly one of these is the
// referent" standing in for an open "one of these, or something static analysis
// cannot reach". Conflating the two is precisely what this ticket exists to
// stop, so the constant is named per row.
type rowExpect struct {
	// Ref is the exact callee text the designated reference emits.
	Ref string
	// Rule is the rung expected to fire.
	Rule RefRule
	// Method is the emitted group's Method, EMPTY for a single-candidate
	// outcome, which emits no group at all.
	Method string
	// Candidates is the expected candidate count, derived from the fixture's
	// own source rather than from another set's length.
	Candidates int
}

// testResolutionMatrix carries one row per registered language. The test
// derives its subject list from the treesitter registry, so a 33rd language
// fails this table until a row is added rather than silently shrinking the
// proof — the same closed-allowlist discipline the corpus matrix uses.
var testResolutionMatrix = map[treesitter.Language]matrixRow{
	treesitter.LangGo: {participates: true,
		anchored: "go same-package init, and cross-directory package main",
		files: []fixtureFile{
			{path: "svc/a.go", src: "package svc\n\nfunc init() {\n\tprintln(\"a\")\n}\n\nfunc Handle() int { return 1 }\n"},
			{path: "svc/b.go", src: "package svc\n\nfunc init() {\n\tprintln(\"b\", \"second\")\n}\n\nfunc Use() int { return Handle() }\n"},
		}},
	treesitter.LangTypeScript: {participates: true,
		anchored:    "typescript e2e-fixture pair, both declaring the same name, one importing the other under an alias",
		importBound: "e2e/one.fixture.ts",
		files: []fixtureFile{
			{path: "e2e/one.fixture.ts", src: "export const client = { base: 'http://localhost:8080' };\n\nexport function setup(): number { return 3; }\n"},
			{path: "e2e/two.fixture.ts", src: "import { setup as base } from './one.fixture';\n\nexport const client = { base: 'http://example.test' };\n\nexport function setup(): number { return base() + 9; }\n"},
		}},
	treesitter.LangTSX: {participates: true,
		anchored:    "tsx component and its co-located test file — 370 of the 844-edge census — the test importing the component while declaring a same-named harness member",
		importBound: "web/Banner.tsx",
		files: []fixtureFile{
			{path: "web/Banner.tsx", src: "export function Banner() {\n  return <div className=\"bg\">hi</div>;\n}\n"},
			{path: "web/Banner.test.tsx", src: "import { Banner } from './Banner';\n\nexport class Harness {\n  Banner() { return null; }\n}\n\nexport function renders() { return Banner(); }\n"},
		}},
	treesitter.LangPython: {participates: true,
		anchored: "two python classes in one module both declaring handle, referenced through the class",
		expect:   rowExpect{Ref: "A.handle", Rule: RuleQualifiedParent, Candidates: 1},
		files: []fixtureFile{
			{path: "bin/one.py", src: "class A:\n    def handle(self):\n        return 1\n\n\n" +
				"class B:\n    def handle(self):\n        return 2\n\n\ndef use():\n    return A.handle()\n"},
			{path: "bin/two.py", src: "import json\n\n\ndef main():\n    return json.dumps({'a': 1})\n"},
		}},
	treesitter.LangJavaScript: {participates: true,
		anchored:    "two mjs tool scripts both declaring the same name, one importing the other under an alias",
		importBound: "tools/one.mjs",
		files: []fixtureFile{
			{path: "tools/one.mjs", src: "export const scriptDir = '/tools/one';\n\nexport function resolvePath() { return scriptDir; }\n"},
			{path: "tools/two.mjs", src: "import { resolvePath as base } from './one.mjs';\n\nexport const scriptDir = '/tools/two';\n\nexport function resolvePath() { return base(); }\n"},
		}},
	// BASH CANNOT BE DYNAMIC, and its row says so. Its separator row is
	// explicitly EMPTY — a command name legitimately contains dots
	// (`./deploy.sh`) — so resolveRef never enters the qualified arm at all and
	// bash cannot reach the dynamic rung, which has exactly one producer there.
	// Two same-named functions in ONE file therefore resolve through the
	// own-scope rung to a CLOSED ambiguous pair.
	treesitter.LangBash: {participates: true,
		anchored: "one test script declaring fail twice, the second overriding the first",
		expect: rowExpect{Ref: "fail", Rule: RuleOwnScope,
			Method: kgtypes.EdgeMethodAmbiguousName, Candidates: 2},
		files: []fixtureFile{
			{path: "scripts/a.test.sh", src: "set -euo pipefail\n\nfail() {\n  echo \"a failed\" >&2\n}\n\n" +
				"fail() {\n  echo \"b failed differently\" >&2\n}\n\nrun() {\n  fail\n}\n"},
			{path: "scripts/b.test.sh", src: "set -euo pipefail\n\nfail() {\n  echo \"b failed differently\" >&2\n}\n"},
		}},
	treesitter.LangJava: {participates: true,
		anchored: "two classes in one file both declaring handle, referenced through the class",
		expect:   rowExpect{Ref: "A.handle", Rule: RuleQualifiedParent, Candidates: 1},
		files: []fixtureFile{
			{path: "m/A.java", src: "class A { int handle() { return 1; } }\n" +
				"class B { int handle() { return 2; } }\n" +
				"class C { int use() { return A.handle(); } }\n"},
			{path: "m/B.java", src: "class D { int handle() { return 3; } }\n"},
		}},
	treesitter.LangRust: {participates: true,
		anchored: "two impl blocks in one file both declaring handle, referenced through the scope operator",
		expect:   rowExpect{Ref: "A::handle", Rule: RuleQualifiedParent, Candidates: 1},
		files: []fixtureFile{
			{path: "m/a.rs", src: "pub struct A;\nimpl A { pub fn handle() -> i32 { 1 } }\n" +
				"pub struct B;\nimpl B { pub fn handle() -> i32 { 2 } }\n" +
				"pub fn use_it() -> i32 { A::handle() }\n"},
			{path: "m/b.rs", src: "pub fn handle() -> i32 { 2 }\n"},
		}},
	// C HAS NO QUALIFIED CALL FORM and an explicitly empty separator row, so
	// its reference is BARE and resolves through the own-scope rung. The
	// one-definition rule is what makes that one candidate rather than two: the
	// same name in a sibling translation unit is a different scope entirely.
	treesitter.LangC: {participates: true,
		anchored: "two translation units both declaring handle, each referencing its own",
		expect:   rowExpect{Ref: "handle", Rule: RuleOwnScope, Candidates: 1},
		files: []fixtureFile{
			{path: "m/a.c", src: "int handle(void) { return 1; }\nint use(void) { return handle(); }\n"},
			{path: "m/b.c", src: "int handle(void) { return 2; }\n"},
		}},
	treesitter.LangCPP: {participates: true,
		anchored:    "two namespaces in one translation unit both declaring g, plus an included header",
		importBound: "inc/a.hpp",
		expect:      rowExpect{Ref: "ns::g", Rule: RuleQualifiedParent, Candidates: 1},
		files: []fixtureFile{
			{path: "inc/a.hpp", src: "int helper() { return 7; }\n"},
			{path: "m/a.cpp", src: "#include \"../inc/a.hpp\"\n\n" +
				"namespace ns { int g() { return 1; } }\n" +
				"namespace nt { int g() { return 2; } }\n" +
				"int use() { return ns::g(); }\n" +
				"int use_import() { return helper(); }\n"},
		}},
	treesitter.LangCSharp: {participates: true,
		anchored: "two classes in one file both declaring Handle, referenced through the class",
		expect:   rowExpect{Ref: "A.Handle", Rule: RuleQualifiedParent, Candidates: 1},
		files: []fixtureFile{
			{path: "m/A.cs", src: "class A { int Handle() { return 1; } }\n" +
				"class B { int Handle() { return 2; } }\n" +
				"class C { int Use() { return A.Handle(); } }\n"},
			{path: "m/B.cs", src: "namespace App { class D { int Handle() { return 3; } } }\n"},
		}},
	// RUBY, LUA AND GROOVY ARE THE DYNAMIC THREE. Their qualifier is a VALUE
	// rather than a declared container, so no parent matches it and the ladder
	// falls to the dynamic rung — an OPEN set at Confidence 1/N, never the
	// closed ambiguous group a cardinality-only assertion would accept.
	treesitter.LangRuby: {participates: true,
		anchored: "two classes in one file both declaring handle, reached through a value receiver",
		expect: rowExpect{Ref: "obj.handle", Rule: RuleDynamicScope,
			Method: kgtypes.EdgeMethodDynamic, Candidates: 2},
		files: []fixtureFile{
			{path: "m/a.rb", src: "class A\n  def handle\n    1\n  end\nend\n\n" +
				"class B\n  def handle\n    2\n  end\nend\n\ndef use\n  obj.handle()\nend\n"},
			{path: "m/b.rb", src: "def handle\n  2\nend\n"},
		}},
	treesitter.LangPHP: {participates: true,
		anchored: "two classes in one file both declaring handle, referenced through the scope operator",
		expect:   rowExpect{Ref: "A::handle", Rule: RuleQualifiedParent, Candidates: 1},
		files: []fixtureFile{
			{path: "m/a.php", src: "<?php\nclass A { function handle() { return 1; } }\n" +
				"class B { function handle() { return 2; } }\n" +
				"class C { function use_it() { return A::handle(); } }\n"},
			{path: "m/b.php", src: "<?php\nfunction handle() { return 2; }\n"},
		}},
	treesitter.LangSwift: {participates: true,
		anchored: "two classes in one file both declaring handle, referenced through the class",
		expect:   rowExpect{Ref: "A.handle", Rule: RuleQualifiedParent, Candidates: 1},
		files: []fixtureFile{
			{path: "m/a.swift", src: "class A { func handle() -> Int { return 1 } }\n" +
				"class B { func handle() -> Int { return 2 } }\n" +
				"class C { func use() -> Int { return A.handle() } }\n"},
			{path: "m/b.swift", src: "func handle() -> Int { return 2 }\n"},
		}},
	treesitter.LangKotlin: {participates: true,
		anchored:    "two classes in one file both declaring handle, plus an aliasless import of a third file's class",
		importBound: "a/b/D.kt",
		expect:      rowExpect{Ref: "A.handle", Rule: RuleQualifiedParent, Candidates: 1},
		// THE IMPORTED FILE DECLARES ITS PACKAGE, and it has to: kotlin resolves
		// at PACKAGE scope, so a file declaring none sits on a directory scope
		// while `import a.b.D` names the package a.b — the two would not meet.
		files: []fixtureFile{
			{path: "a/b/D.kt", src: "package a.b\n\nclass D { fun go(): Int { return 7 } }\n"},
			{path: "m/a.kt", src: "import a.b.D\n\n" +
				"class A { fun handle(): Int { return 1 } }\n" +
				"class B { fun handle(): Int { return 2 } }\n" +
				"class C { fun use(): Int { return A.handle() } }\n" +
				"fun useImport(x: D): Int { return 3 }\n"},
		}},
	treesitter.LangScala: {participates: true,
		anchored:    "two objects in one file both declaring handle, plus an import of a third file's object",
		importBound: "a/b/D.scala",
		expect:      rowExpect{Ref: "A.handle", Rule: RuleQualifiedParent, Candidates: 1},
		// THE PACKAGE IS TWO SEGMENTS DELIBERATELY. ScopeID falls back to a
		// directory scope when a declared namespace equals the one derived from
		// the file's own directory basename, so a single-segment `package a` in
		// a directory named a would take the fallback and the arm's namespace
		// bind would not meet it — the known narrow miss csharpBinds documents,
		// which this row must not sit on top of.
		files: []fixtureFile{
			{path: "a/b/D.scala", src: "package a.b\n\nclass D { def go(): Int = 7 }\n"},
			{path: "m/a.scala", src: "import a.b.D\n\n" +
				"object A { def handle(): Int = 1 }\n" +
				"object B { def handle(): Int = 2 }\n" +
				"object C { def use(): Int = A.handle() }\n" +
				"class UseImport(x: D)\n"},
		}},
	// ELIXIR IS DYNAMIC HERE, AND THAT IS A MEASUREMENT RATHER THAN A CHOICE.
	// A `def` inside a `defmodule` carries NO ParentName — probed directly:
	// every chunk in this fixture comes back with an empty parent, the module
	// included — so the qualifier `A` matches no declared container and the
	// qualified-parent rung cannot fire for this language at all. The ladder
	// falls to the dynamic rung, which is also what the ticket classifies
	// elixir as: an OPEN set, tracked as dynamic and never as ambiguous.
	treesitter.LangElixir: {participates: true,
		anchored: "two modules in one file both declaring handle, reached through a module qualifier the index does not parent",
		expect: rowExpect{Ref: "A.handle", Rule: RuleDynamicScope,
			Method: kgtypes.EdgeMethodDynamic, Candidates: 2},
		files: []fixtureFile{
			{path: "m/a.ex", src: "defmodule A do\n  def handle do\n    1\n  end\nend\n\n" +
				"defmodule B do\n  def handle do\n    2\n  end\nend\n\n" +
				"defmodule C do\n  def use do\n    A.handle()\n  end\nend\n"},
			{path: "m/b.ex", src: "defmodule D do\n  def handle do\n    2\n  end\nend\n"},
		}},
	// LUA'S COLLIDING PAIR IS TWO TOP-LEVEL FUNCTIONS OF ONE NAME, not two
	// table members. Lua names arrive PRE-QUALIFIED from the grammar and are
	// kept verbatim, so `function A.handle()` is a declaration NAMED
	// "A.handle" — probed directly — and a reference to `handle` matches
	// neither. Two `A.handle`/`B.handle` declarations are therefore distinct
	// names rather than a collision, and the pair that genuinely collides is
	// this one. The colon call is what exercises lua's own separator row.
	treesitter.LangLua: {participates: true,
		anchored: "one file declaring handle twice, reached through a colon call on a value",
		expect: rowExpect{Ref: "obj:handle", Rule: RuleDynamicScope,
			Method: kgtypes.EdgeMethodDynamic, Candidates: 2},
		files: []fixtureFile{
			{path: "m/a.lua", src: "function handle()\n  return 1\nend\n\n" +
				"function handle()\n  return 2\nend\n\nfunction use()\n  return obj:handle()\nend\n"},
			{path: "m/b.lua", src: "function handle()\n  return 3\nend\n"},
		}},
	treesitter.LangGroovy: {participates: true,
		anchored: "two classes in one file both declaring handle, reached through a value receiver",
		expect: rowExpect{Ref: "obj.handle", Rule: RuleDynamicScope,
			Method: kgtypes.EdgeMethodDynamic, Candidates: 2},
		files: []fixtureFile{
			{path: "m/a.groovy", src: "class A { int handle() { return 1 } }\n" +
				"class B { int handle() { return 2 } }\n" +
				"class C { int use() { return obj.handle() } }\n"},
			{path: "m/b.groovy", src: "class D { int handle() { return 3 } }\n"},
		}},
	treesitter.LangElm: {participates: true, files: []fixtureFile{
		{path: "m/A.elm", src: "module A exposing (handle)\n\n\nhandle : Int\nhandle =\n    1\n"},
		{path: "m/B.elm", src: "module B exposing (handle)\n\n\nhandle : Int\nhandle =\n    2\n"},
	}},
	treesitter.LangOCaml: {participates: true, files: []fixtureFile{
		{path: "m/a.ml", src: "let handle () = 1\n"},
		{path: "m/b.ml", src: "let handle () = 2\n"},
	}},
	// hcl, protobuf, sql and svelte name NOTHING: their query sets capture
	// structure with no @name, so no declaration enters the index. Verified by
	// probe rather than assumed — each fixture below declares the shapes most
	// likely to be named (resource/variable/module, message/service/enum,
	// table/view/function, script members) and every resulting chunk came back
	// unnamed.
	treesitter.LangHCL: {participates: false, files: []fixtureFile{
		{path: "m/a.tf", src: "resource \"null_resource\" \"handle\" {\n  count = 1\n}\n"},
		{path: "m/b.tf", src: "resource \"null_resource\" \"handle\" {\n  count = 2\n}\n"},
	}},
	treesitter.LangProtobuf: {participates: false, files: []fixtureFile{
		{path: "m/a.proto", src: "syntax = \"proto3\";\n\nmessage Handle {\n  string id = 1;\n}\n"},
		{path: "m/b.proto", src: "syntax = \"proto3\";\n\nmessage Handle {\n  string name = 1;\n}\n"},
	}},
	treesitter.LangSQL: {participates: false, files: []fixtureFile{
		{path: "m/a.sql", src: "CREATE TABLE handle (id INT);\n"},
		{path: "m/b.sql", src: "CREATE TABLE handle (name TEXT);\n"},
	}},
	treesitter.LangSvelte: {participates: false, files: []fixtureFile{
		{path: "m/a.svelte", src: "<script>\n  export let handle = 1;\n</script>\n<div>{handle}</div>\n"},
		{path: "m/b.svelte", src: "<script>\n  export let handle = 2;\n</script>\n<span>{handle}</span>\n"},
	}},
	treesitter.LangCSS: {participates: false, files: []fixtureFile{
		{path: "m/a.css", src: ".handle { color: red; }\n"},
		{path: "m/b.css", src: ".handle { color: blue; font-weight: bold; }\n"},
	}},
	treesitter.LangHTML: {participates: false, files: []fixtureFile{
		{path: "m/a.html", src: "<html><body><div id=\"handle\">a</div></body></html>\n"},
		{path: "m/b.html", src: "<html><body><span id=\"handle\">b</span></body></html>\n"},
	}},
	treesitter.LangDockerfile: {participates: false, files: []fixtureFile{
		{path: "m/Dockerfile", src: "FROM alpine:3.19\nRUN echo one\n"},
		{path: "m/Dockerfile.dev", src: "FROM alpine:3.19\nRUN echo two\nCMD [\"sh\"]\n"},
	}},
	treesitter.LangToml: {participates: false, files: []fixtureFile{
		{path: "m/a.toml", src: "[handle]\nvalue = 1\n"},
		{path: "m/b.toml", src: "[handle]\nvalue = 2\nextra = \"x\"\n"},
	}},
	treesitter.LangYaml: {participates: false,
		anchored: "yaml orphan-only — the largest uncontained population at 4,608",
		files: []fixtureFile{
			{path: "deploy/app.yaml", src: "name: my-application\nspec:\n  replicas: 3\n"},
			{path: "deploy/job.yaml", src: "name: my-job\nspec:\n  schedule: \"0 * * * *\"\n"},
		}},
	treesitter.LangMarkdown: {participates: false, files: []fixtureFile{
		{path: "m/a.md", src: "# Handle\n\nsome text here\n"},
		{path: "m/b.md", src: "# Handle\n\ndifferent text entirely\n"},
	}},
	treesitter.LangCue: {participates: false, files: []fixtureFile{
		{path: "m/a.cue", src: "handle: {\n\tvalue: 1\n}\n"},
		{path: "m/b.cue", src: "handle: {\n\tvalue: 2\n}\n"},
	}},
}

// assertImportBound runs the row's fixture through the production ordering —
// deduplicate, fill binds, index — and requires that at least one reference
// resolves through the unqualified-import rule onto a declaration in the
// IMPORTED file's scope.
//
// It materializes the fixture on disk and passes a real RepoContext, because a
// module resolver given no root and no discovered file set resolves nothing:
// with the empty RepoContext populateFixture passes, every assertion here would
// be vacuous.
func assertImportBound(t *testing.T, lang treesitter.Language, row matrixRow) {
	t.Helper()

	root := t.TempDir()
	discovered := make([]string, 0, len(row.files))
	for _, f := range row.files {
		full := filepath.Join(root, filepath.FromSlash(f.path))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o750))
		require.NoError(t, os.WriteFile(full, []byte(f.src), 0o600))
		discovered = append(discovered, f.path)
	}

	results := chunkFixture(t, row.files)
	// Production ordering: names are final before the arm reads them, and the
	// binds are filled before the index the ladder consults is built.
	DeduplicateChunks(results)
	rc := treesitter.RepoContext{Root: root, Files: discovered}
	fillBinds(&rc, results)
	ix := indexResults(t, results)

	// THE DECLARED NAMESPACE IS DERIVED THE WAY THE INDEX DERIVES IT, never
	// threaded through as a duplicate argument. populate builds every
	// declaration's scope from the chunked file's OWN ChunkContext.PackageName,
	// so recomputing `want` from the path with an empty third argument could
	// never agree for a declared-namespace language: csharp and php index under
	// their namespace while the empty form yields a directory scope, so the
	// assertion would fail against CORRECT work and its failure message would
	// name a scope pointing nowhere near the real one.
	//
	// It is behavior-preserving for every ScopeFile row — ScopeID ignores its
	// third argument entirely there — which is why the rows that use this
	// helper today are unaffected by the repair.
	want := treesitter.ScopeID(row.importBound, lang, importedPackageName(results, row.importBound))
	var bound []string
	for _, result := range results {
		for i := range result.Edges {
			e := &result.Edges[i]
			if e.Ref == nil {
				continue
			}
			got := resolveRef(ix, e.Ref, e.ToID)
			if got.Rule != RuleUnqualifiedImport {
				continue
			}
			for _, c := range got.Candidates {
				if c.Scope == want && c.Scope != e.Ref.Scope {
					bound = append(bound, e.ToID+" -> "+c.NodeID)
				}
			}
		}
	}
	require.NotEmpty(t, bound,
		"%s: no reference bound into the imported scope %q through the import rule; "+
			"the exemption at (3) is untaken and this row proves only that nothing broke",
		lang, want)
}

// TestResolutionParticipationParity lives in resolution_parity_test.go, split
// out when the row rewrites pushed this file past the 500-line block.

// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// refEdgeIn is refEdgeFrom's general form: the referencing FILE, the EDGE TYPE
// and the verbatim target. refEdgeFrom fixes the file at app/main.go and the
// type at CALLS, which every fixture in the landed ladder shares; the
// per-language arm cases below put their references in a file named for their
// own language and reach several of them through a TYPE reference, so both
// have to be parameters here.
func refEdgeIn(
	t *testing.T, results []*treesitter.Result,
	file string, edgeType treesitter.EdgeType, target string,
) *treesitter.Edge {
	t.Helper()
	for _, r := range results {
		if r.FilePath != file {
			continue
		}
		for i := range r.Edges {
			e := &r.Edges[i]
			if e.ToID == target && e.Type == edgeType {
				return e
			}
		}
	}
	t.Fatalf("no %s edge to %q in %s", edgeType, target, file)
	return nil
}

// resolveFixtureRef chunks a fixture through the production ordering — chunk,
// fill binds in place, index — and resolves ONE reference off it.
//
// NOTHING HERE REGISTERS AN ARM. chunker_binds.go's init has already registered
// every language these cases exercise for the whole test binary, so a case that
// called RegisterBindsResolver would shadow the very arm it is proving, and an
// UnregisterBindsResolver in a cleanup would DELETE the production registration
// for every test running after it in the same binary — with no compile error.
func resolveFixtureRef(
	t *testing.T, files []fixtureFile,
	file string, edgeType treesitter.EdgeType, target string,
) (*declIndex, *treesitter.Edge) {
	t.Helper()
	results := chunkFixture(t, files)
	fillBinds(&treesitter.RepoContext{}, results)
	ix := indexResults(t, results)
	return ix, refEdgeIn(t, results, file, edgeType, target)
}

// resolveRepoFixtureRef is resolveFixtureRef for an arm that is a MODULE
// RESOLVER rather than a specifier reader.
//
// resolveFixtureRef hands the pass an EMPTY RepoContext, which is sufficient for
// every arm proved beside it — java, python, rust, c and swift all derive their
// bind from the import specifier's own text. The ECMAScript arm cannot work that
// way: it turns './lib/alpha' into a file by consulting the repository, so with
// no Root and no Files it resolves nothing and binds nothing. Writing the
// fixture to a real tree and passing the discovered file list is what lets it
// run at all, and is the same shape populateRepoFixture uses one file over.
//
// THE FIXTURE LIST IS THE DISCOVERED SET, exactly as in the sibling harness: the
// files written here are the files the arm is told exist.
//
// THE EDGE TYPE IS FIXED AT CALLS rather than a parameter, on the same rule
// refEdgeFrom states: every case reaching this harness resolves a call, so a
// type argument would carry no information and the unparam linter says so. A
// case needing a TYPE reference through a module resolver reintroduces the
// parameter — the general form, refEdgeIn, still takes one.
func resolveRepoFixtureRef(
	t *testing.T, files []fixtureFile,
	file string, target string,
) (*declIndex, *treesitter.Edge) {
	t.Helper()
	root := t.TempDir()
	discovered := make([]string, 0, len(files))
	for _, f := range files {
		writeFixtureFile(t, root, f.path, f.src)
		discovered = append(discovered, f.path)
	}

	results := chunkFixture(t, files)
	fillBinds(&treesitter.RepoContext{Root: root, Files: discovered}, results)
	ix := indexResults(t, results)
	return ix, refEdgeIn(t, results, file, treesitter.EdgeCalls, target)
}

// resolveFixtureParentedRef is resolveFixtureRef narrowed to the reference a
// PARENTED declaration emitted.
//
// A CONTAINER CHUNK AND ITS MEMBER CHUNK BOTH WALK THE MEMBER'S BODY, so one
// source token yields two edges carrying the same target — the class's, whose
// site has no Parent, and the method's, whose site does. A case whose whole
// point is the parented site has to pick that one explicitly instead of taking
// whichever the walk emitted first.
func resolveFixtureParentedRef(
	t *testing.T, files []fixtureFile,
	file string, edgeType treesitter.EdgeType, target string,
) (*declIndex, *treesitter.Edge) {
	t.Helper()
	results := chunkFixture(t, files)
	fillBinds(&treesitter.RepoContext{}, results)
	ix := indexResults(t, results)
	for _, r := range results {
		if r.FilePath != file {
			continue
		}
		for i := range r.Edges {
			e := &r.Edges[i]
			if e.ToID == target && e.Type == edgeType && e.Ref != nil && e.Ref.Parent != "" {
				return ix, e
			}
		}
	}
	t.Fatalf("no %s edge to %q from a PARENTED declaration in %s", edgeType, target, file)
	return nil, nil
}

// ladderImportArmCases carries the four cases that prove the java, python and
// rust arms resolve, plus the per-language precedence knob. They live in their
// own file, and are invoked from TestResolveRefRuleLadder so their subtest
// names read as that test's, because resolve_walk_test.go is close enough to
// the 500-line lefthook block that adding them inline would breach it.
func ladderImportArmCases(t *testing.T) {
	t.Helper()

	t.Run("java_import_binds", func(t *testing.T) {
		// A bare `C` where NO local of that name exists, so a naive
		// "always prefer the local" implementation fails this case just as an
		// implementation with no arm at all does. Neither this case nor
		// python_local_shadows_import below catches the other's error.
		//
		// BOTH FILES DECLARE THEIR PACKAGE, and that is not decoration: java
		// forbids importing out of the default package, so a fixture without
		// package clauses states a case real source never takes — and java now
		// resolves at PACKAGE scope, so a package-less fixture would put the
		// declaration on a directory scope the arm never names.
		ix, e := resolveFixtureRef(t, []fixtureFile{
			{path: "a/b/C.java", src: "package a.b;\n\nclass C { void go() {} }\n"},
			{path: "app/Main.java", src: "" +
				"package app;\n\nimport a.b.C;\n\nclass Main {\n    C field;\n}\n"},
		}, "app/Main.java", treesitter.EdgeUsesType, "C")

		require.NotNil(t, e.Ref)
		require.Equal(t, "ns:java:a_b", e.Ref.Binds["C"].Scope,
			"the java arm binds into the PACKAGE the dotted specifier names")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleUnqualifiedImport, got.Rule)
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "a/b/C.java", got.Candidates[0].File)
	})

	t.Run("python_local_shadows_import", func(t *testing.T) {
		// THE ONLY CASE IN THIS PLAN THAT FAILS IF THE PRECEDENCE HALF OF THE
		// LANGUAGE PROFILE IS DROPPED. app.py imports foo AND declares its own
		// foo; python's rule is that the local rebinds the name and wins. With
		// ImportsBeatLocals defaulting to true this binds x.py's foo instead.
		ix, e := resolveFixtureRef(t, []fixtureFile{
			{path: "x.py", src: "def foo():\n    return 1\n"},
			{path: "app.py", src: "" +
				"from x import foo\n\n\ndef foo():\n    return 2\n\n\ndef run():\n    return foo()\n"},
		}, "app.py", treesitter.EdgeCalls, "foo")

		require.NotNil(t, e.Ref)
		require.Equal(t, "file:x.py", e.Ref.Binds["foo"].Scope,
			"the import IS recorded — the local wins on precedence, not on the arm being absent")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleOwnScope, got.Rule)
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "app.py", got.Candidates[0].File,
			"a python local legally rebinds an imported name and takes precedence")
	})

	t.Run("rust_alias_binds_declared_name", func(t *testing.T) {
		// THE ONLY CATCHER FOR Bind.Name. An arm that recorded
		// Bind{Scope: ..., Name: ""} for an aliased import leaves the
		// unqualified-import rung looking up `z` at the right scope, which
		// misses, and the reference lands external with no gate red.
		//
		// THE MODULE SITS UNDER THE CRATE ROOT rather than beside it. The rust
		// arm anchors a bare first segment on the directory holding the crate's
		// root module file — app/, because app/main.rs is one — so app/x.rs is
		// where `use x::y` names a module, and a sibling x/y.rs at the
		// repository root is a layout rustc itself could not resolve.
		ix, e := resolveFixtureRef(t, []fixtureFile{
			{path: "app/x.rs", src: "pub fn y() -> i32 { 1 }\n"},
			{path: "app/main.rs", src: "" +
				"use x::y as z;\n\nfn main() -> i32 { z() }\n"},
		}, "app/main.rs", treesitter.EdgeCalls, "z")

		require.NotNil(t, e.Ref)
		require.Equal(t, "y", e.Ref.Binds["z"].Name,
			"the alias renames the reference, never the declaration")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleUnqualifiedImport, got.Rule)
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "y", got.Candidates[0].Name,
			"the reference writes z and resolves to the declaration named y")
	})

	t.Run("python_external_qualifier_terminates", func(t *testing.T) {
		// AN ABSENCE ASSERTION, and its positive-only form would be VACUOUS: a
		// reference with NO bind recorded also fails to resolve — it just fails
		// by falling into the dynamic rung and emitting an edge to the local
		// `run` first. The control below is what distinguishes
		// recorded-and-terminated from never-recorded.
		//
		// The qualifier is EXACTLY THE NAME THE ARM BOUND, which is why this
		// case is python and not java: the external-qualifier rung keys on the
		// qualifier, and a java reference written fully qualified would split
		// into a package path the arm never bound.
		ix, e := resolveFixtureRef(t, []fixtureFile{
			{path: "app.py", src: "" +
				"from ext import helper\n\n\ndef run():\n    return 1\n\n\ndef go():\n    return helper.run()\n"},
		}, "app.py", treesitter.EdgeCalls, "helper.run")

		require.NotNil(t, e.Ref)
		require.NotEmpty(t, e.Ref.Binds["helper"].Scope,
			"the arm records the bind even though nothing in the fixture is at that path")
		require.False(t, ix.hasScope(e.Ref.Binds["helper"].Scope),
			"the bind must name a scope the index genuinely does not hold")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefExternal, got.Status)
		assert.Equal(t, RuleExternalQualifier, got.Rule)
		assert.Empty(t, got.Candidates, "termination emits no edge to the local run at all")

		// KNOWN-POSITIVE CONTROL: the same reference with no bind recorded
		// falls through to the dynamic rung and reaches the local `run` —
		// exactly the wrong edge the termination removes.
		unbound := *e.Ref
		unbound.Binds = map[string]treesitter.Bind{}
		ctrl := resolveRef(ix, &unbound, e.ToID)
		assert.Equal(t, RefDynamic, ctrl.Status)
		assert.NotEmpty(t, ctrl.Candidates,
			"control: with no bind recorded, the reference reaches the local run")
	})
}

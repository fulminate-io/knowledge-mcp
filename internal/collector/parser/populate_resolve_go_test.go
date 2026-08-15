// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// storeNode is the cross-package declaration most rows below bind to. It lives
// in its own directory because GO'S RESOLUTION UNIT IS THE DIRECTORY.
var storeNode = fixtureFile{
	path: "domains/store/node.go",
	src:  "package store\n\ntype Node struct{}\n",
}

// goFixtureSites chunks a fixture, builds the declaration index, and runs the
// REAL binds pass over the results — so a subtest can ask the resolution walk
// which RULE fired, and can inspect a file's reference site directly.
//
// It is the same chunk-index-fill path chunkResultsToPopulate runs; what it
// adds is a handle on the intermediate state the emitted edges do not carry.
func goFixtureSites(t *testing.T, files []fixtureFile) ([]*treesitter.Result, *declIndex) {
	t.Helper()
	results := chunkFixture(t, files)
	ix := indexResults(t, results)
	// The SAME module path populateFixture supplies — every fixture import
	// below is written as "example.com/fixture/<dir>", so a drift between the
	// two would leave every cross-package row here reporting external.
	fillBinds(&treesitter.RepoContext{ModulePath: "example.com/fixture"}, results)
	return results, ix
}

// siteFor returns one file's file-level reference site.
func siteFor(t *testing.T, results []*treesitter.Result, path string) *treesitter.RefSite {
	t.Helper()
	for _, r := range results {
		if r.FilePath == path {
			require.NotNil(t, r.Ref, "%s produced no reference site", path)
			return r.Ref
		}
	}
	t.Fatalf("no chunk result for %s", path)
	return nil
}

// TestGoExactBinding proves the Go arm's output actually reaches the resolution
// walk — through the query, the import arm, the carrier, the binds pass, the
// fill-in-place write, the by-value reference-site copies and the declaration
// index. Every row asserts a PRESENCE and the corresponding ABSENCE, because
// "the edge exists" and "no wrong edge exists" are different claims.
func TestGoExactBinding(t *testing.T) {
	t.Run("cross_directory_qualified_type", func(t *testing.T) {
		// THE WHOLE TICKET IN ONE ASSERTION. It fails four separate ways
		// without this plan: the capture loses the package, no Go import arm
		// fills the carrier, no arm is registered, and no module path reaches
		// the pass.
		res := populateFixture(t, []fixtureFile{
			storeNode,
			{path: "app/use.go", src: "package app\n\nimport \"example.com/fixture/domains/store\"\n\nfunc use(n store.Node) {}\n"},
		})

		assert.Equal(t, []string{"domains/store/node.go:Node"},
			edgesFrom(res, kgtypes.EdgeUsesType, "app/use.go:use"),
			"the qualified reference binds across the directory boundary, and to nothing else")
	})

	t.Run("mirrored_tree_same_name", func(t *testing.T) {
		// The dominant Go collision shape: one package name declared in two
		// directories of one repo.
		const mirrored = "package svc\n\ntype Config struct{}\n"
		res := populateFixture(t, []fixtureFile{
			{path: "cmd/agent/svc/svc.go", src: mirrored},
			{path: "cmd/executor/svc/svc.go", src: mirrored},
			{path: "cmd/agent/app/a.go", src: "package app\n\nimport \"example.com/fixture/cmd/agent/svc\"\n\nfunc use(c svc.Config) {}\n"},
		})

		assert.Equal(t, []string{"cmd/agent/svc/svc.go:Config"},
			edgesFrom(res, kgtypes.EdgeUsesType, "cmd/agent/app/a.go:use"),
			"the import names ONE of the two directories, and the scope is the directory")
	})

	t.Run("aliased_import", func(t *testing.T) {
		// THE END-TO-END CATCHER FOR THE IMPORT ARM: an arm that dropped Local,
		// or a query that lost the name field, leaves this row external while
		// every plain row still passes. The alias and the package name differ
		// deliberately, and the target's declared name is Node — which is what
		// makes this also the end-to-end proof that a Go alias renames the
		// PACKAGE and not the member.
		res := populateFixture(t, []fixtureFile{
			storeNode,
			{path: "app/alias.go", src: "package app\n\nimport kgstore \"example.com/fixture/domains/store\"\n\nfunc useAlias(n kgstore.Node) {}\n"},
		})

		assert.Equal(t, []string{"domains/store/node.go:Node"},
			edgesFrom(res, kgtypes.EdgeUsesType, "app/alias.go:useAlias"))
	})

	t.Run("parented_method_binds", func(t *testing.T) {
		// REQUIRED BY THE FILL-IN-PLACE RULE, and a file-level-only fixture
		// PASSES UNDER THE BROKEN SHAPE: the chunker copies the reference site
		// BY VALUE for a parented declaration, so a pass that ASSIGNED a fresh
		// Binds map would update the file-level site alone and every reference
		// inside a Go method would see nil binds — no compile error, no other
		// gate moving.
		res := populateFixture(t, []fixtureFile{
			storeNode,
			{path: "app/svc.go", src: "package app\n\nimport \"example.com/fixture/domains/store\"\n\ntype Svc struct{}\n\nfunc (s *Svc) load(n store.Node) {}\n"},
		})

		assert.True(t, hasEdge(res, kgtypes.EdgeUsesType, "app/svc.go:Svc.load", "domains/store/node.go:Node"),
			"a reference inside a METHOD must reach the same binds the file-level site carries")
	})

	t.Run("external_out_of_module_terminates", func(t *testing.T) {
		// THE LOCAL DECLARATION IS THE POINT. The arm records Bind{Scope:
		// "dir:fmt"} like any other import; the qualified rule finds no
		// candidates; the external-qualifier rule sees the index holds no such
		// scope and TERMINATES. Without that rung the reference falls to the
		// dynamic rung and emits an open-set edge to the LOCAL Println.
		res := populateFixture(t, []fixtureFile{
			{path: "app/std.go", src: "package app\n\nimport \"fmt\"\n\nfunc printIt() {\n\tfmt.Println(\"x\")\n}\n\nfunc Println() {}\n"},
		})

		assert.Empty(t, edgesFrom(res, kgtypes.EdgeCalls, "app/std.go:printIt"),
			"an out-of-module qualifier terminates; it must not bind to the same-named local")
		assert.False(t, hasEdge(res, kgtypes.EdgeCalls, "app/std.go:printIt", "app/std.go:Println"))
	})

	t.Run("external_in_repo_unindexed", func(t *testing.T) {
		// THE ROW PROVING THE ARM MAKES NO IN-REPO JUDGMENT. The arm records
		// Bind{Scope: "dir:generated"} EXACTLY as it would for an indexed
		// directory; nothing is contributed under generated/, so the INDEX is
		// what makes this terminate.
		res := populateFixture(t, []fixtureFile{
			{path: "app/gen.go", src: "package app\n\nimport \"example.com/fixture/generated\"\n\ntype Thing struct{}\n\nfunc useGen(t generated.Thing) {}\n"},
		})

		assert.Empty(t, edgesFrom(res, kgtypes.EdgeUsesType, "app/gen.go:useGen"),
			"an under-module qualifier the index never heard of terminates like any other")
		assert.False(t, hasEdge(res, kgtypes.EdgeUsesType, "app/gen.go:useGen", "app/gen.go:Thing"))
	})

	t.Run("dot_import_binds_cross_scope", func(t *testing.T) {
		files := []fixtureFile{
			storeNode,
			{path: "app/dot.go", src: "package app\n\nimport . \"example.com/fixture/domains/store\"\n\nfunc useDot(n Node) {}\n"},
		}

		res := populateFixture(t, files)
		assert.Equal(t, []string{"domains/store/node.go:Node"},
			edgesFrom(res, kgtypes.EdgeUsesType, "app/dot.go:useDot"),
			"a dot import folds the whole scope into this file's unqualified namespace")

		// THE RULE IS ASSERTED TOO, not just the edge: a bind under
		// RuleOwnScope would mean the name was found locally and the dot scope
		// was never consulted at all.
		results, ix := goFixtureSites(t, files)
		got := resolveRef(ix, siteFor(t, results, "app/dot.go"), "Node")
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleDotScope, got.Rule)
	})

	t.Run("dot_import_two_scopes_ambiguous", func(t *testing.T) {
		// THIS FIXTURE DOES NOT COMPILE, AND THAT IS FINE. Two dot imports
		// exporting one name is `Item redeclared in this block`, a hard compile
		// error — but the chunker does not typecheck, so it sees both dot
		// scopes. The honest output is the multi-candidate group: a program the
		// language rejects has no winner, and picking one would state a fact
		// the language refuses to state.
		res := populateFixture(t, []fixtureFile{
			{path: "domains/one/item.go", src: "package one\n\ntype Item struct{ a int }\n"},
			{path: "domains/two/item.go", src: "package two\n\ntype Item struct{ b string }\n"},
			{path: "app/dot2.go", src: "package app\n\nimport (\n\t. \"example.com/fixture/domains/one\"\n\t. \"example.com/fixture/domains/two\"\n)\n\nfunc useItem(i Item) {}\n"},
		})

		got := groupEdgesOf(res, kgtypes.EdgeMethodAmbiguousName)
		require.Len(t, got, 2, "one edge per candidate — a CLOSED group, not a narrowed guess")
		for _, e := range got {
			assert.Equal(t, "app/dot2.go:useItem", e.FromId)
			assert.InDelta(t, 0.5, e.Confidence, 1e-9, "Confidence is 1/N")
			assert.Equal(t, kgtypes.EdgeMethodAmbiguousName, e.Method)
		}
		assert.ElementsMatch(t,
			[]string{"domains/one/item.go:Item", "domains/two/item.go:Item"},
			[]string{got[0].ToId, got[1].ToId})
		require.Len(t, evidenceKeys(got), 1, "both members of one group share one key")
	})

	t.Run("blank_import", func(t *testing.T) {
		files := []fixtureFile{
			storeNode,
			{path: "app/blank.go", src: "package app\n\nimport _ \"example.com/fixture/domains/store\"\n\nfunc nothing() int {\n\treturn 1\n}\n"},
		}

		res := populateFixture(t, files)
		assert.Empty(t, edgesFrom(res, kgtypes.EdgeUsesType, "app/blank.go:nothing"))
		assert.False(t, hasEdge(res, kgtypes.EdgeCalls, "app/blank.go:nothing", "domains/store/node.go:Node"))

		// THE CATCHER FOR AN ARM THAT TREATED "_" LIKE ".": a blank import
		// binds nothing and folds in no scope.
		results, _ := goFixtureSites(t, files)
		assert.Empty(t, siteFor(t, results, "app/blank.go").DotScopes,
			"a blank import introduces no name and no scope")
	})

	t.Run("package_main_cross_directory", func(t *testing.T) {
		// GO'S SCOPE UNIT IS THE DIRECTORY, NOT THE PACKAGE NAME: two `package
		// main` directories declare the same names without colliding. It needs
		// no imports and no arm, so it is ALSO THE KNOWN-POSITIVE CONTROL for
		// every absence above — it proves this harness resolves at all when the
		// binds path is uninvolved.
		res := populateFixture(t, []fixtureFile{
			{path: "cmd/one/main.go", src: "package main\n\nfunc run() int {\n\treturn 1\n}\n\nfunc main() {\n\t_ = run()\n}\n"},
			{path: "cmd/two/main.go", src: "package main\n\nfunc run() int {\n\treturn 22222\n}\n\nfunc main() {\n\t_ = run()\n}\n"},
		})

		assert.Equal(t, []string{"cmd/one/main.go:run"},
			edgesFrom(res, kgtypes.EdgeCalls, "cmd/one/main.go:main"))
		assert.Equal(t, []string{"cmd/two/main.go:run"},
			edgesFrom(res, kgtypes.EdgeCalls, "cmd/two/main.go:main"))
	})
}

// TestGoLegitimateAmbiguity pins the two shapes that MUST REMAIN AMBIGUOUS.
// The ticket requires it verbatim: "build-tag variants and multiple init
// functions remain legitimately Ambiguous → multi-bind per the core design".
// Both are several declarations of one name in ONE scope, which the ladder
// resolves to a closed multi-candidate group — the correct answer, not a
// residue to drive to zero.
//
// BOTH ROWS ARE ALSO THE CATCHER FOR AN OVER-EAGER ARM: an implementation that
// tried to make Go "fully exact" by collapsing multi-candidate scopes would
// turn these into single bound edges and both subtests would red.
func TestGoLegitimateAmbiguity(t *testing.T) {
	t.Run("same_package_init_pair", func(t *testing.T) {
		res := populateFixture(t, []fixtureFile{
			{path: "domains/boot/a.go", src: "package boot\n\nfunc init() {\n\tregistered = 1\n}\n\nvar registered int\n"},
			{path: "domains/boot/b.go", src: "package boot\n\nfunc init() {\n\tregistered = 22222\n}\n"},
			{path: "domains/boot/caller.go", src: "package boot\n\nfunc Reinit() {\n\tinit()\n}\n"},
		})

		got := groupEdgesOf(res, kgtypes.EdgeMethodAmbiguousName)
		require.Len(t, got, 2, "one edge PER CANDIDATE — the pair is the answer, not a defect")
		for _, e := range got {
			assert.Equal(t, "domains/boot/caller.go:Reinit", e.FromId)
			assert.InDelta(t, 0.5, e.Confidence, 1e-9, "Confidence is 1/N")
			assert.Equal(t, kgtypes.EdgeMethodAmbiguousName, e.Method)
		}
		require.Len(t, evidenceKeys(got), 1,
			"the shared key is what distinguishes a correct multi-bind from two unrelated edges")
	})

	t.Run("build_tag_variants", func(t *testing.T) {
		// THIS IS CORRECT, NOT A DEFECT. The chunker does not evaluate build
		// constraints, so both declarations are real source in one scope.
		// Enumerating both as an explicit exactly-one-of group is a truthful
		// answer where picking one would be a manufactured one.
		res := populateFixture(t, []fixtureFile{
			{path: "domains/plat/impl_linux.go", src: "//go:build linux\n\npackage plat\n\nfunc platform() string {\n\treturn \"linux\"\n}\n"},
			{path: "domains/plat/impl_darwin.go", src: "//go:build darwin\n\npackage plat\n\nfunc platform() string {\n\treturn \"a different string for darwin\"\n}\n"},
			{path: "domains/plat/caller.go", src: "package plat\n\nfunc Name() string {\n\treturn platform()\n}\n"},
		})

		got := groupEdgesOf(res, kgtypes.EdgeMethodAmbiguousName)
		require.Len(t, got, 2, "both build-tag variants are candidates")
		require.Len(t, evidenceKeys(got), 1, "one reference, one group key")
		assert.ElementsMatch(t,
			[]string{"domains/plat/impl_linux.go:platform", "domains/plat/impl_darwin.go:platform"},
			[]string{got[0].ToId, got[1].ToId})
	})
}

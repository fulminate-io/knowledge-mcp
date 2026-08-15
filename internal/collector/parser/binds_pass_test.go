// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// TestFillBinds_DotScopesOnly is THE STRUCTURAL CATCHER for the skip that would
// silently drop dot-import support entirely.
//
// A Go dot import establishes NO per-name bind — it folds a whole scope into
// the importing file's unqualified namespace — so a file whose ONLY import is a
// dot import produces an EMPTY Binds map and a single dot scope. A fillBinds
// that keys its skip on `len(built.Binds) == 0` alone hits `continue` on
// exactly that file and never reads its dot scope: no compile error, and every
// other gate in this change still green. This test is the one that reds.
//
// THE ARM IS REGISTERED BEFORE CHUNKING because the CHUNKER is what allocates
// the maps; an arm registered afterwards leaves them nil and the pass has
// nothing to fill in place. That ordering is a property of the landed design,
// documented on fillBinds itself.
func TestFillBinds_DotScopesOnly(t *testing.T) {
	const dotScope = "dir:lib"

	treesitter.RegisterBindsResolver(treesitter.LangGo,
		func(_ *treesitter.RepoContext, _ map[string]*treesitter.Result, self *treesitter.Result) treesitter.BindsResult {
			if self.FilePath == "app/main.go" {
				// EXACTLY THE SHAPE A DOT-IMPORT-ONLY FILE PRODUCES: an empty
				// (non-nil) Binds map, and one dot scope.
				return treesitter.BindsResult{
					Binds:     map[string]treesitter.Bind{},
					DotScopes: []string{dotScope},
				}
			}
			return treesitter.BindsResult{}
		})
	// RESTORE, NEVER DELETE. Go ships a real arm registered at init, and
	// UnregisterBindsResolver would delete it for every later test in this
	// binary — leaving cross-package Go references resolving as external in
	// tests that had nothing to do with this one.
	t.Cleanup(func() { treesitter.RegisterGoBindsResolver() })

	results := chunkFixture(t, []fixtureFile{
		{path: "lib/lib.go", src: "" +
			"package lib\n\nfunc Helper() int {\n\treturn 1\n}\n"},
		{path: "app/main.go", src: "" +
			"package main\n\nimport . \"example.com/lib\"\n\nfunc Run() int {\n\treturn Helper()\n}\n"},
	})

	filled := fillBinds(&treesitter.RepoContext{}, results)

	var site *treesitter.RefSite
	for _, r := range results {
		if r.FilePath == "app/main.go" {
			site = r.Ref
		}
	}
	// KNOWN-POSITIVE CONTROL: the chunker really did build a site and allocate
	// the set, so a miss below is the pass's fault and not an absent fixture.
	require.NotNil(t, site, "the fixture file must have produced a reference site")
	require.NotNil(t, site.DotScopes, "the chunker allocates the set when a language has an arm")

	assert.True(t, site.DotScopes[dotScope],
		"a file whose ONLY import is a dot import must still get its dot scope filled")
	assert.Empty(t, site.Binds, "a dot import establishes no per-name bind")

	// THE CENSUS IS DRIVEN NON-ZERO HERE. Every corpus this ships against
	// measures zero dot imports, so without this assertion a hardcoded zero and
	// a wired zero are indistinguishable for the life of the release.
	assert.Equal(t, 1, filled, "the pass reports the one dot scope it filled")
}

// bindDeclaredNameFixture is the two-declaration module both cases resolve
// against: a class A and a function foo, in one TypeScript file.
var bindDeclaredNameFixture = []fixtureFile{
	{path: "web/x.ts", src: "" +
		"export class A {}\n\nexport function foo() { return 1; }\n"},
}

// TestResolveRef_BindDeclaredName pins the ONE rung the declared-name override
// applies to, in both directions.
//
// A renaming import — `import {A as B}` — writes B at the reference while the
// target declares A, so the unqualified rule must look up the BIND's name. The
// qualified rule must NOT: `import * as ns` renames the MODULE, not its
// members, so a namespace member keeps the spelling the reference used.
// Applying the override in both places is the mirror-image mistake, and the
// second case is what catches it.
//
// The reference sites are constructed DIRECTLY rather than through a
// registered arm, so this test is independent of any per-language arm.
func TestResolveRef_BindDeclaredName(t *testing.T) {
	results := chunkFixture(t, bindDeclaredNameFixture)
	ix := indexResults(t, results)

	// KNOWN-POSITIVE CONTROL for the whole test: both declarations really are
	// in the index under the scope the binds name, so a miss below is the
	// override's fault and not an empty fixture's.
	require.True(t, ix.hasScope("file:web/x.ts"))
	require.Len(t, ix.lookup(declKey{Scope: "file:web/x.ts", Name: "A"}), 1)
	require.Len(t, ix.lookup(declKey{Scope: "file:web/x.ts", Name: "foo"}), 1)

	t.Run("renamed_named_import", func(t *testing.T) {
		ref := &treesitter.RefSite{
			File:  "web/main.ts",
			Scope: "file:web/main.ts",
			Lang:  treesitter.LangTypeScript,
			// `import {A as B} from './x'` — the reference writes B.
			Binds: map[string]treesitter.Bind{
				"B": {Scope: "file:web/x.ts", Name: "A"},
			},
		}

		got := resolveRef(ix, ref, "B")
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleUnqualifiedImport, got.Rule)
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "web/x.ts:A", got.Candidates[0].NodeID,
			"the bind's DECLARED name is what the lookup uses, not the local alias")

		// FALSIFIER: without the override the lookup asks for B, which nothing
		// declares, and the reference lands external.
		noOverride := *ref
		noOverride.Binds = map[string]treesitter.Bind{"B": {Scope: "file:web/x.ts"}}
		fallthroughRes := resolveRef(ix, &noOverride, "B")
		assert.Equal(t, RefExternal, fallthroughRes.Status,
			"the override is load-bearing: without it there is nothing named B")
	})

	t.Run("namespace_verbatim", func(t *testing.T) {
		ref := &treesitter.RefSite{
			File:  "web/main.ts",
			Scope: "file:web/main.ts",
			Lang:  treesitter.LangTypeScript,
			// `import * as ns from './x'`, with a Name deliberately set to a
			// value that matches NO declaration: if the qualified rule applied
			// the override, the lookup would ask for renamedModule and miss.
			Binds: map[string]treesitter.Bind{
				"ns": {Scope: "file:web/x.ts", Name: "renamedModule"},
			},
		}

		got := resolveRef(ix, ref, "ns.foo")
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleQualifiedImport, got.Rule)
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "web/x.ts:foo", got.Candidates[0].NodeID,
			"a namespace member keeps its OWN spelling — the module was renamed, not the member")
	})
}

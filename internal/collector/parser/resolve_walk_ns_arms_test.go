// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// ladderNamespaceArmCases carries the four cases the matrix's importBound route
// structurally cannot serve.
//
// THAT HELPER RECOMPUTES THE IMPORTED FILE'S SCOPE FROM ITS PATH. csharp and
// php are the only two declared-namespace languages, so their arms bind into a
// NAMESPACE scope while the helper would compute a directory one — the two
// cannot agree. Swift's arm records a scope that equals no file's, by design.
// And C's proof is CROSS-FILE by construction and its matrix row holds a single
// file. All four are proved here instead, where the other arms are proved too.
func ladderNamespaceArmCases(t *testing.T) {
	t.Helper()

	t.Run("csharp_alias_binds_declared_name", func(t *testing.T) {
		// THIS CASE CARRIES THE FILL-IN-PLACE DUTY as well as the alias one:
		// its reference sits INSIDE A CLASS MEMBER, and a parented reference
		// site is a by-value copy taken during chunking. A pass that ASSIGNED a
		// fresh Binds map instead of filling the allocated one in place would
		// leave this site's map nil, with no compile error and no other gate red.
		//
		// n/Lib.cs uses the FILE-SCOPED namespace form deliberately: a braced
		// `namespace Foo { }` is declined by the file-namespace reader and the
		// file would fall back to a directory scope, which breaks the case.
		ix, e := resolveFixtureParentedRef(t, []fixtureFile{
			{path: "n/Lib.cs", src: "namespace Foo;\n\nclass Bar { public void Go() {} }\n"},
			{path: "app/Main.cs", src: "" +
				"using X = Foo.Bar;\n\nclass Main {\n    void M() { X y; }\n}\n"},
		}, "app/Main.cs", treesitter.EdgeUsesType, "X")

		require.NotNil(t, e.Ref)
		require.NotEmpty(t, e.Ref.Parent,
			"the reference must come from a PARENTED declaration — this is the fill-in-place catcher")
		require.Equal(t, "ns:csharp:Foo", e.Ref.Binds["X"].Scope,
			"the csharp arm binds into the DECLARED NAMESPACE, not a directory")
		require.Equal(t, "Bar", e.Ref.Binds["X"].Name,
			"the reference writes X while the target declares Bar")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleUnqualifiedImport, got.Rule)
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "n/Lib.cs", got.Candidates[0].File)
		assert.Equal(t, "Bar", got.Candidates[0].Name,
			"the Bind.Name override is what turns X into Bar")
	})

	t.Run("php_use_binds_namespace", func(t *testing.T) {
		// The sibling `namespace Foo;` form — a namespace_definition carrying a
		// name and NO body — is what makes n/lib.php claim that namespace as
		// its scope rather than its parent directory.
		ix, e := resolveFixtureRef(t, []fixtureFile{
			{path: "n/lib.php", src: "<?php\nnamespace Foo;\n\nclass Bar { function go() {} }\n"},
			{path: "app/main.php", src: "" +
				"<?php\nnamespace App;\n\nuse Foo\\Bar;\n\nfunction run(Bar $x) { return 1; }\n"},
		}, "app/main.php", treesitter.EdgeUsesType, "Bar")

		require.NotNil(t, e.Ref)
		require.Equal(t, "ns:php:Foo", e.Ref.Binds["Bar"].Scope)
		require.Empty(t, e.Ref.Binds["Bar"].Name,
			"an unaliased use binds the declared name itself")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleUnqualifiedImport, got.Rule)
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "n/lib.php", got.Candidates[0].File)
	})

	t.Run("swift_import_terminates", func(t *testing.T) {
		// A swift import names a MODULE and the source carries no path to
		// derive, so the arm records a scope that equals no file's. The bind is
		// still RECORDED, and that is what lets the reference TERMINATE at the
		// external-qualifier rung instead of falling into the dynamic one and
		// emitting an edge to the local `run`.
		ix, e := resolveFixtureRef(t, []fixtureFile{
			{path: "app.swift", src: "" +
				"import struct Ext.Helper\n\nfunc run() -> Int { return 1 }\n\nfunc go() -> Int { return Helper.run() }\n"},
		}, "app.swift", treesitter.EdgeCalls, "Helper.run")

		require.NotNil(t, e.Ref)
		_, bound := e.Ref.Binds["Helper"]
		require.True(t, bound, "the arm records the declaration-import's bound name")
		require.False(t, ix.hasScope(e.Ref.Binds["Helper"].Scope),
			"the recorded scope equals no indexed scope")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefExternal, got.Status)
		assert.Equal(t, RuleExternalQualifier, got.Rule)
		assert.Empty(t, got.Candidates, "no edge reaches the local run")

		// KNOWN-POSITIVE CONTROL, the same shape the landed external-qualifier
		// case uses: with no bind recorded the reference falls to the dynamic
		// rung and does reach the local run.
		unbound := *e.Ref
		unbound.Binds = map[string]treesitter.Bind{}
		ctrl := resolveRef(ix, &unbound, e.ToID)
		assert.Equal(t, RefDynamic, ctrl.Status)
		assert.NotEmpty(t, ctrl.Candidates)
	})

	t.Run("c_include_binds_header", func(t *testing.T) {
		// THE CROSS-FILE PROOF OF THE INCLUDE ARM. A SINGLE-FILE C fixture
		// cannot exercise this at all: the name would already be in the file's
		// own scope and the own-scope rung would resolve it without the arm
		// ever running, which is a vacuous pass. The include resolves relative
		// to src/, so the derived candidate is inc/a.h — the byPath key the arm
		// must hit — and the bound candidate must be THE HEADER, not the .c.
		ix, e := resolveFixtureRef(t, []fixtureFile{
			{path: "inc/a.h", src: "int helper(void) { return 1; }\n"},
			{path: "src/main.c", src: "" +
				"#include \"../inc/a.h\"\n\nint main(void) { return helper(); }\n"},
		}, "src/main.c", treesitter.EdgeCalls, "helper")

		require.NotNil(t, e.Ref)
		require.Equal(t, "file:inc/a.h", e.Ref.Binds["helper"].Scope,
			"the include is resolved against the INCLUDING file's directory")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleUnqualifiedImport, got.Rule)
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "inc/a.h", got.Candidates[0].File,
			"the reference binds to the header, never to the .c")

		// KNOWN-POSITIVE CONTROL asserting the FULL TRIPLE the unfixed path
		// produces: with no bind, the bare `helper` reaches the sibling rung
		// (skipped, no parent), then the own scope of src/main.c, which
		// declares nothing of that name, and terminates undeclared. A control
		// asserting only "not RuleUnqualifiedImport" would also pass on a dozen
		// wrong resolutions.
		unbound := *e.Ref
		unbound.Binds = map[string]treesitter.Bind{}
		ctrl := resolveRef(ix, &unbound, e.ToID)
		assert.Equal(t, RefExternal, ctrl.Status)
		assert.Equal(t, RuleNotDeclared, ctrl.Rule)
		assert.Empty(t, ctrl.Candidates)
	})
}

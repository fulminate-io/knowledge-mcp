// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// goBindsFixture builds the one Go file the arm reads: a Result whose first
// chunk carries the captured import table. Every chunk of one file carries the
// same context, so the first is representative.
func goBindsFixture(bindings ...ImportBinding) *Result {
	return &Result{
		FilePath: "app/use.go",
		Language: LangGo,
		Chunks:   []Chunk{{Context: ChunkContext{ImportBindings: bindings}}},
	}
}

// goNamespaceImport is the shape parseGoImport records for every Go import
// except the blank one: the package bound under one local name, naming no
// member of itself.
func goNamespaceImport(specifier, local string) ImportBinding {
	return ImportBinding{Specifier: specifier, Local: local, Kind: ImportNamespace}
}

// TestGoBindsResolver pins every rule of the Go binding mapping, each subtest
// with a DISTINCT concrete value. Every binding subtest asserts the FULL
// returned Binds map by equality rather than containment, so a spurious extra
// binding fails.
func TestGoBindsResolver(t *testing.T) {
	const modulePath = "example.com/mod"
	rc := &RepoContext{ModulePath: modulePath}

	t.Run("under_module_indexed_shape", func(t *testing.T) {
		got := goBindsResolver(rc, nil, goBindsFixture(goNamespaceImport("example.com/mod/domains/store", "")))
		assert.Equal(t, map[string]Bind{"store": {Scope: "dir:domains/store"}}, got.Binds)
		assert.Empty(t, got.DotScopes)
	})

	t.Run("under_module_unindexed_shape", func(t *testing.T) {
		// The gitignored-codegen shape. THE ASSERTION IS THAT THE ARM DOES NOT
		// SPECIAL-CASE IT: it is recorded identically to the indexed row above,
		// and the declaration index — not the arm — is what makes it terminate.
		got := goBindsResolver(rc, nil, goBindsFixture(goNamespaceImport("example.com/mod/generated", "")))
		assert.Equal(t, map[string]Bind{"generated": {Scope: "dir:generated"}}, got.Binds)
	})

	t.Run("aliased_under_module", func(t *testing.T) {
		got := goBindsResolver(rc, nil, goBindsFixture(goNamespaceImport("example.com/mod/domains/store", "kgstore")))
		assert.Equal(t, map[string]Bind{"kgstore": {Scope: "dir:domains/store"}}, got.Binds)
		assert.NotContains(t, got.Binds, "store", "an aliased import is written with its ALIAS, never its package name")
	})

	t.Run("name_override_is_always_empty", func(t *testing.T) {
		// THE CATCHER FOR RULE B3N, invisible otherwise because the qualified
		// import rule ignores Bind.Name. A Go alias renames the PACKAGE, not
		// any member of it, so `kgstore.Node` still names Node at the target.
		got := goBindsResolver(rc, nil, goBindsFixture(goNamespaceImport("example.com/mod/domains/store", "kgstore")))
		require.Contains(t, got.Binds, "kgstore")
		assert.Empty(t, got.Binds["kgstore"].Name)
	})

	t.Run("module_root", func(t *testing.T) {
		// Fails on any implementation that forgets filepath.Dir's ".".
		got := goBindsResolver(rc, nil, goBindsFixture(goNamespaceImport("example.com/mod", "")))
		assert.Equal(t, map[string]Bind{"mod": {Scope: "dir:."}}, got.Binds)
	})

	t.Run("out_of_module_third_party", func(t *testing.T) {
		// The path is kept VERBATIM. Asserts the no-op prefix strip rather than
		// an omission or a sentinel value.
		got := goBindsResolver(rc, nil, goBindsFixture(goNamespaceImport("github.com/other/thing", "")))
		assert.Equal(t, map[string]Bind{"thing": {Scope: "dir:github.com/other/thing"}}, got.Binds)
	})

	t.Run("out_of_module_stdlib", func(t *testing.T) {
		// A second out-of-module row because stdlib and third-party reach the
		// same branch by different path shapes.
		got := goBindsResolver(rc, nil, goBindsFixture(goNamespaceImport("fmt", "")))
		assert.Equal(t, map[string]Bind{"fmt": {Scope: "dir:fmt"}}, got.Binds)
	})

	t.Run("prefix_is_not_a_segment", func(t *testing.T) {
		// Correct by construction, since the strip tests the trailing
		// separator. The row guards against anyone "optimizing" it into a
		// Contains or a manual slice, which would yield "dir:els/x".
		got := goBindsResolver(rc, nil, goBindsFixture(goNamespaceImport("example.com/models/x", "")))
		assert.Equal(t, map[string]Bind{"x": {Scope: "dir:example.com/models/x"}}, got.Binds)
	})

	t.Run("dot_import_records_no_bind", func(t *testing.T) {
		got := goBindsResolver(rc, nil, goBindsFixture(goNamespaceImport("example.com/mod/domains/store", ".")))
		assert.Empty(t, got.Binds, "a dot import introduces no qualifier, so there is no Binds key to hold")
		assert.NotContains(t, got.Binds, ".")
	})

	t.Run("dot_import_reports_the_scope", func(t *testing.T) {
		got := goBindsResolver(rc, nil, goBindsFixture(goNamespaceImport("example.com/mod/domains/store", ".")))
		assert.Equal(t, []string{"dir:domains/store"}, got.DotScopes)
	})

	t.Run("blank_import", func(t *testing.T) {
		got := goBindsResolver(rc, nil, goBindsFixture(
			ImportBinding{Specifier: "example.com/mod/domains/store", Local: "_", Kind: ImportSideEffect}))
		assert.Empty(t, got.Binds, "a blank import introduces no name, so no reference can name it")
		assert.NotContains(t, got.Binds, "_")
		assert.Empty(t, got.DotScopes, "a blank import is not a dot import")
	})

	t.Run("no_module_path", func(t *testing.T) {
		// RULE B6. Asserted NIL on both fields rather than merely empty: the
		// pass reads emptiness across both, and nil pins the allocation-free
		// path this short-circuit exists for.
		got := goBindsResolver(&RepoContext{}, nil, goBindsFixture(goNamespaceImport("example.com/mod/domains/store", "")))
		require.Nil(t, got.Binds)
		require.Nil(t, got.DotScopes)
	})
}

// TestGoBindsScopeAgreesWithScopeID IS THE MOST LOAD-BEARING TEST OF THE ARM.
//
// TWO STAMPERS PRODUCE ONE STRING: the arm stamps the Bind.Scope a reference is
// looked up under, and ScopeID stamps the scope every declaration is filed
// under — and the SAME ScopeID value populates the declaration index's scope
// set. A drift between them breaks the qualified import rule and the
// external-qualifier rule together, and both failures look identical from the
// outside, like a reference that was simply external.
func TestGoBindsScopeAgreesWithScopeID(t *testing.T) {
	const modulePath = "example.com/mod"
	rc := &RepoContext{ModulePath: modulePath}

	cases := []struct {
		name string
		dir  string // repo-relative directory the imported package lives in
	}{
		{name: "nested_directory", dir: "domains/store"},
		{name: "single_segment_directory", dir: "app"},
		{name: "module_root", dir: "."},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			specifier := modulePath
			if tc.dir != "." {
				specifier = modulePath + "/" + tc.dir
			}
			want := ScopeID(filepath.Join(tc.dir, "f.go"), LangGo, "")

			got := goBindsResolver(rc, nil, goBindsFixture(goNamespaceImport(specifier, "target")))
			require.Contains(t, got.Binds, "target")
			assert.Equal(t, want, got.Binds["target"].Scope,
				"the arm's Bind.Scope must be byte-identical to the scope ScopeID files declarations under")

			// The DOT scope takes the same derivation, so this pair covers the
			// dot-import rule too — a drift there would break the dot-scope
			// rung the same way.
			dot := goBindsResolver(rc, nil, goBindsFixture(goNamespaceImport(specifier, ".")))
			assert.Equal(t, []string{want}, dot.DotScopes)
		})
	}
}

// TestGoBindsRegistered goes through BindsFor — the exported entry point the
// binds pass actually calls — rather than the arm directly, so an arm that is
// written but never registered still reds.
func TestGoBindsRegistered(t *testing.T) {
	rc := &RepoContext{ModulePath: "example.com/mod"}

	got := BindsFor(rc, nil, goBindsFixture(goNamespaceImport("example.com/mod/domains/store", "")))
	assert.Equal(t, map[string]Bind{"store": {Scope: "dir:domains/store"}}, got.Binds)

	// KNOWN-NEGATIVE CONTROL: the same file shape in a language with no
	// registered arm returns the zero result, so the assertion above is
	// evidence of Go's registration and not of BindsFor answering everything.
	noArm := goBindsFixture(goNamespaceImport("example.com/mod/domains/store", ""))
	noArm.Language = LangHCL
	require.False(t, hasBindsResolver(LangHCL), "the control language must genuinely have no arm")
	none := BindsFor(rc, nil, noArm)
	assert.Nil(t, none.Binds)
	assert.Nil(t, none.DotScopes)
}

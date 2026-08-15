// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRustModuleAnchors pins the four anchor kinds a `use` path's first segment
// selects, ONE SUBTEST PER KIND so a single rule regressing is attributable to
// that rule rather than to "the rust arm".
//
// EVERY FIXTURE CARRIES A DECOY the old repo-root-relative derivation would have
// taken, because the anchor rules are only observable against an alternative:
// a ladder that anchored at the repository root resolves the decoy, and a ladder
// that anchors correctly resolves the real module file.
func TestRustModuleAnchors(t *testing.T) {
	rc := &RepoContext{}

	t.Run("crate_anchors_at_the_crate_root", func(t *testing.T) {
		byPath := map[string]*Result{
			"src/lib.rs":  declFile("src/lib.rs", LangRust, "root"),
			"src/util.rs": declFile("src/util.rs", LangRust, "Dir"),
			// THE DECOY: the pre-fix ladder joined the module path onto the
			// repository root, so it would have taken this file.
			"util.rs": declFile("util.rs", LangRust, "Dir"),
		}
		self := armFixture("src/app/run.rs", LangRust,
			ImportBinding{Specifier: "crate::util", Imported: "Dir", Local: "Dir", Kind: ImportNamed})

		got := BindsFor(rc, byPath, self)
		assert.Equal(t, map[string]Bind{"Dir": {Scope: "file:src/util.rs"}}, got.Binds,
			"crate:: anchors at the directory holding the root module file, not at the repository root")
	})

	t.Run("crate_empty_path_reaches_the_root_module_file", func(t *testing.T) {
		// `use crate::Item` carries NO module path at all: the item is declared
		// in the crate's own root module, so the candidate is the root module
		// FILE rather than a directory joined from segments. It is the single
		// largest population in the measured corpora and the easiest to omit.
		byPath := map[string]*Result{
			"src/lib.rs": declFile("src/lib.rs", LangRust, "Item"),
		}
		self := armFixture("src/app/run.rs", LangRust,
			ImportBinding{Specifier: "crate", Imported: "Item", Local: "Item", Kind: ImportNamed})

		got := BindsFor(rc, byPath, self)
		assert.Equal(t, map[string]Bind{"Item": {Scope: "file:src/lib.rs"}}, got.Binds)
	})

	t.Run("self_anchors_at_the_importing_module", func(t *testing.T) {
		// src/util.rs IS the module util, so its children live in src/util/.
		byPath := map[string]*Result{
			"src/lib.rs":        declFile("src/lib.rs", LangRust, "root"),
			"src/util/inner.rs": declFile("src/util/inner.rs", LangRust, "Thing"),
			// THE DECOY the crate-root anchor would take.
			"src/inner.rs": declFile("src/inner.rs", LangRust, "Thing"),
		}
		self := armFixture("src/util.rs", LangRust,
			ImportBinding{Specifier: "self::inner", Imported: "Thing", Local: "Thing", Kind: ImportNamed})

		got := BindsFor(rc, byPath, self)
		assert.Equal(t, map[string]Bind{"Thing": {Scope: "file:src/util/inner.rs"}}, got.Binds,
			"self:: in a non-root module file anchors at <dir>/<stem>, never at <dir>")
	})

	t.Run("self_in_a_root_module_file_anchors_at_its_own_directory", func(t *testing.T) {
		// src/util/mod.rs is ALSO the module util, but its children live beside
		// it rather than under a directory named mod.
		byPath := map[string]*Result{
			"src/lib.rs":            declFile("src/lib.rs", LangRust, "root"),
			"src/util/inner.rs":     declFile("src/util/inner.rs", LangRust, "Thing"),
			"src/util/mod/inner.rs": declFile("src/util/mod/inner.rs", LangRust, "Thing"),
		}
		self := armFixture("src/util/mod.rs", LangRust,
			ImportBinding{Specifier: "self::inner", Imported: "Thing", Local: "Thing", Kind: ImportNamed})

		got := BindsFor(rc, byPath, self)
		assert.Equal(t, map[string]Bind{"Thing": {Scope: "file:src/util/inner.rs"}}, got.Binds)
	})

	t.Run("super_anchors_one_module_up", func(t *testing.T) {
		byPath := map[string]*Result{
			"src/lib.rs":         declFile("src/lib.rs", LangRust, "root"),
			"src/util/helper.rs": declFile("src/util/helper.rs", LangRust, "Thing"),
			// THE DECOY the crate-root anchor would take.
			"src/helper.rs": declFile("src/helper.rs", LangRust, "Thing"),
		}
		self := armFixture("src/util/inner.rs", LangRust,
			ImportBinding{Specifier: "super::helper", Imported: "Thing", Local: "Thing", Kind: ImportNamed})

		got := BindsFor(rc, byPath, self)
		assert.Equal(t, map[string]Bind{"Thing": {Scope: "file:src/util/helper.rs"}}, got.Binds,
			"super:: from a non-root module file names that file's own directory")
	})

	t.Run("super_from_a_root_module_file_leaves_its_directory", func(t *testing.T) {
		byPath := map[string]*Result{
			"src/lib.rs":         declFile("src/lib.rs", LangRust, "root"),
			"src/helper.rs":      declFile("src/helper.rs", LangRust, "Thing"),
			"src/util/helper.rs": declFile("src/util/helper.rs", LangRust, "Other"),
		}
		self := armFixture("src/util/mod.rs", LangRust,
			ImportBinding{Specifier: "super::helper", Imported: "Thing", Local: "Thing", Kind: ImportNamed})

		got := BindsFor(rc, byPath, self)
		assert.Equal(t, map[string]Bind{"Thing": {Scope: "file:src/helper.rs"}}, got.Binds,
			"a root module file IS its module, so super:: is the directory above its own")
	})

	t.Run("bare_first_segment_anchors_at_the_crate_root", func(t *testing.T) {
		byPath := map[string]*Result{
			"src/lib.rs":  declFile("src/lib.rs", LangRust, "root"),
			"src/util.rs": declFile("src/util.rs", LangRust, "Dir"),
			"util.rs":     declFile("util.rs", LangRust, "Dir"),
		}
		self := armFixture("src/app/run.rs", LangRust,
			ImportBinding{Specifier: "util", Imported: "Dir", Local: "Dir", Kind: ImportNamed})

		got := BindsFor(rc, byPath, self)
		assert.Equal(t, map[string]Bind{"Dir": {Scope: "file:src/util.rs"}}, got.Binds,
			"a bare first segment is the 2015-edition reading: crate-root relative")
	})

	t.Run("imported_name_that_is_itself_a_module_resolves", func(t *testing.T) {
		// `use crate::util::dir` where dir is a MODULE rather than an item: the
		// last two rungs of the non-empty ladder exist for exactly this shape.
		byPath := map[string]*Result{
			"src/lib.rs":      declFile("src/lib.rs", LangRust, "root"),
			"src/util/dir.rs": declFile("src/util/dir.rs", LangRust, "dir"),
		}
		self := armFixture("src/app/run.rs", LangRust,
			ImportBinding{Specifier: "crate::util", Imported: "dir", Local: "dir", Kind: ImportNamed})

		got := BindsFor(rc, byPath, self)
		assert.Equal(t, map[string]Bind{"dir": {Scope: "file:src/util/dir.rs"}}, got.Binds)
	})

	t.Run("external_crate_still_terminates", func(t *testing.T) {
		// THE R2X INPUT. std:: matches no candidate, the arm records the
		// best-effort bind anyway, and the scope it names is a path the fixture
		// does not hold — which is what the external-qualifier rung consumes to
		// terminate the reference instead of manufacturing an edge to a local.
		byPath := map[string]*Result{
			"src/lib.rs": declFile("src/lib.rs", LangRust, "root"),
		}
		self := armFixture("src/app/run.rs", LangRust,
			ImportBinding{Specifier: "std::collections", Imported: "HashMap", Local: "HashMap", Kind: ImportNamed})

		got := BindsFor(rc, byPath, self)
		require.Contains(t, got.Binds, "HashMap",
			"omitting an out-of-repo bind is what lets a bare HashMap::new() reach a LOCAL HashMap")
		scope := got.Binds["HashMap"].Scope
		require.Equal(t, "file:", scope[:5], "the recorded scope is still a file scope")
		_, held := byPath[scope[len("file:"):]]
		assert.False(t, held, "the recorded scope must name a path the index does not hold: %s", scope)
	})

	t.Run("workspace_crate_root_is_not_the_repo_root", func(t *testing.T) {
		// THE RIPGREP SHAPE. crates/core holds main.rs directly — no src/ and no
		// Cargo.toml of its own — so a Cargo.toml walk anchoring at <dir>/src
		// finds a directory that does not exist, while the root-module-file walk
		// anchors at crates/core.
		byPath := map[string]*Result{
			"crates/core/main.rs": declFile("crates/core/main.rs", LangRust, "main"),
			"crates/core/util.rs": declFile("crates/core/util.rs", LangRust, "Dir"),
			"Cargo.toml":          declFile("Cargo.toml", LangToml, ""),
			// THE DECOYS: the repo root and the workspace src/ the two rejected
			// derivations would have taken.
			"util.rs":     declFile("util.rs", LangRust, "Dir"),
			"src/util.rs": declFile("src/util.rs", LangRust, "Dir"),
		}
		self := armFixture("crates/core/app/run.rs", LangRust,
			ImportBinding{Specifier: "crate::util", Imported: "Dir", Local: "Dir", Kind: ImportNamed})

		got := BindsFor(rc, byPath, self)
		assert.Equal(t, map[string]Bind{"Dir": {Scope: "file:crates/core/util.rs"}}, got.Binds,
			"the crate root is the nearest ancestor holding a root module file")
	})

	t.Run("alias_carries_the_declared_name", func(t *testing.T) {
		byPath := map[string]*Result{
			"src/lib.rs":  declFile("src/lib.rs", LangRust, "root"),
			"src/util.rs": declFile("src/util.rs", LangRust, "Dir"),
		}
		self := armFixture("src/app/run.rs", LangRust,
			ImportBinding{Specifier: "crate::util", Imported: "Dir", Local: "D", Kind: ImportNamed})

		got := BindsFor(rc, byPath, self)
		assert.Equal(t, map[string]Bind{"D": {Scope: "file:src/util.rs", Name: "Dir"}}, got.Binds,
			"the reference writes D and the target declares Dir")
	})
}

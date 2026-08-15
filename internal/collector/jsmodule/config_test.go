// SPDX-License-Identifier: Apache-2.0

package jsmodule

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTree materializes a fixture tree under a temp dir and returns the root
// plus the repo-relative file list, which is what the resolver treats as the
// DISCOVERED SET.
func writeTree(t *testing.T, files map[string]string) (string, []string) {
	t.Helper()
	root := t.TempDir()
	var rel []string
	for name, body := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o750))
		require.NoError(t, os.WriteFile(full, []byte(body), 0o600))
		rel = append(rel, name)
	}
	return root, rel
}

// TestConfigIndex covers the governing-config rule and the JSONC reader. Each
// subtest is named for the failure it catches.
func TestConfigIndex(t *testing.T) {
	// A SOLUTION FILE GOVERNS NOTHING. This is the case that makes the obvious
	// "walk up to the nearest tsconfig.json" rule wrong: the nearest ancestor
	// of web/src/a.ts is web/tsconfig.json, which carries no paths at all, and
	// TypeScript does not merge a referenced project's options into the
	// referencing one.
	t.Run("solution_file_governs_nothing", func(t *testing.T) {
		root, files := writeTree(t, map[string]string{
			"web/tsconfig.json": `{"files": [], "references": [{"path": "./tsconfig.app.json"}]}`,
			"web/tsconfig.app.json": `{
				"include": ["src"],
				"compilerOptions": {"baseUrl": ".", "paths": {"@app/*": ["src/*"]}}
			}`,
			"web/src/a.ts": "export const a = 1;",
		})
		ci := newConfigIndex(root, files)

		got := ci.governing("web/src/a.ts")
		require.NotNil(t, got, "the referenced project must govern, not the solution file")
		assert.Equal(t, "web/tsconfig.app.json", got.path)
		assert.Equal(t, []string{"src/*"}, got.paths["@app/*"])

		// KNOWN-POSITIVE CONTROL: the solution file was really loaded, so the
		// assertion above is a preference between two configs and not the
		// accident of one having been skipped.
		var sawSolution bool
		for _, c := range ci.configs {
			if c.path == "web/tsconfig.json" {
				sawSolution = true
				assert.True(t, c.governsNothing)
			}
		}
		assert.True(t, sawSolution, "the solution file must be in the index")
	})

	// PATHS ARRIVING ONLY THROUGH extends.
	t.Run("paths_inherited_through_extends", func(t *testing.T) {
		root, files := writeTree(t, map[string]string{
			"tsconfig.base.json": `{"compilerOptions": {"baseUrl": ".", "paths": {"@lib/*": ["packages/lib/*"]}}}`,
			"app/tsconfig.json":  `{"extends": "../tsconfig.base.json", "include": ["**/*"]}`,
			"app/main.ts":        "export const m = 1;",
		})
		ci := newConfigIndex(root, files)

		got := ci.governing("app/main.ts")
		require.NotNil(t, got)
		assert.Equal(t, "app/tsconfig.json", got.path)
		assert.Equal(t, []string{"packages/lib/*"}, got.paths["@lib/*"],
			"paths inherit through extends")
	})

	// THE ONLY CASE THAT SEPARATES THE TWO baseUrl READINGS. baseUrl is
	// declared in cfg/tsconfig.base.json and inherited by app/tsconfig.json in
	// a DIFFERENT directory: resolving it against the declaring config gives
	// "cfg", resolving it against the inheriting one would give "app". On the
	// acceptance corpus both configs sit in the same directory, so the corpus
	// cannot tell these apart and this fixture is the only thing that can.
	t.Run("baseurl_resolves_against_declaring_config", func(t *testing.T) {
		root, files := writeTree(t, map[string]string{
			"cfg/tsconfig.base.json": `{"compilerOptions": {"baseUrl": ".", "paths": {"@x/*": ["x/*"]}}}`,
			"app/tsconfig.json":      `{"extends": "../cfg/tsconfig.base.json", "include": ["**/*"]}`,
			"app/main.ts":            "export const m = 1;",
		})
		ci := newConfigIndex(root, files)

		got := ci.governing("app/main.ts")
		require.NotNil(t, got)
		assert.Equal(t, "cfg", got.pathsBase,
			"baseUrl belongs to the config that DECLARED it, not the one that inherits it")
		assert.NotEqual(t, "app", got.pathsBase)
	})

	// THE INVISIBLE-FAILURE CATCHER. A tsconfig with block comments and a
	// trailing comma is what the standard tooling emits; encoding/json rejects
	// both outright. Without this case a resolver that silently reads ZERO
	// paths passes every other test in this package, because every other test
	// would then take the relative branch.
	t.Run("jsonc_comments_and_trailing_comma", func(t *testing.T) {
		root, files := writeTree(t, map[string]string{
			"web/tsconfig.json": `{
				/* Bundler mode */
				"include": ["src"],
				"compilerOptions": {
					// the alias table
					"baseUrl": ".",
					"paths": {
						"@app/*": ["src/*"],
						"@shared": ["src/shared/index.ts"],
					},
				},
			}`,
			"web/src/a.ts": "export const a = 1;",
		})
		ci := newConfigIndex(root, files)

		got := ci.governing("web/src/a.ts")
		require.NotNil(t, got, "a JSONC config must parse")
		assert.Equal(t, []string{"src/*"}, got.paths["@app/*"])
		assert.Equal(t, []string{"src/shared/index.ts"}, got.paths["@shared"])
	})

	// A '//' INSIDE A STRING IS NOT A COMMENT. The stripper walks string
	// literals so a URL-shaped or protocol-shaped value survives intact.
	t.Run("comment_stripper_honors_string_literals", func(t *testing.T) {
		root, files := writeTree(t, map[string]string{
			"tsconfig.json": `{
				"include": ["**/*"],
				"compilerOptions": {"baseUrl": ".", "paths": {"@u": ["https://x//y"]}}
			}`,
			"a.ts": "export const a = 1;",
		})
		ci := newConfigIndex(root, files)

		got := ci.governing("a.ts")
		require.NotNil(t, got)
		assert.Equal(t, []string{"https://x//y"}, got.paths["@u"],
			"a '//' inside a string literal is data, not a comment")
	})

	// EXCLUDE BEATS INCLUDE, and a file outside the config's directory is
	// never governed by it.
	t.Run("exclude_and_directory_scope", func(t *testing.T) {
		root, files := writeTree(t, map[string]string{
			"web/tsconfig.json": `{"include": ["src/**/*"], "exclude": ["src/gen/**/*"]}`,
			"web/src/a.ts":      "export const a = 1;",
			"web/src/gen/b.ts":  "export const b = 1;",
			"other/c.ts":        "export const c = 1;",
		})
		ci := newConfigIndex(root, files)

		assert.NotNil(t, ci.governing("web/src/a.ts"))
		assert.Nil(t, ci.governing("web/src/gen/b.ts"), "excluded")
		assert.Nil(t, ci.governing("other/c.ts"), "outside the config's directory")
	})
}

// TestPackageIndex covers the in-repo workspace-package table, which the
// acceptance corpus cannot exercise: it holds one package.json and no
// workspaces, so every bare specifier there is out of repo.
func TestPackageIndex(t *testing.T) {
	root, files := writeTree(t, map[string]string{
		"packages/ui/package.json": `{
			"name": "@acme/ui",
			"exports": {".": {"import": "./src/index.ts", "require": "./dist/index.cjs"},
			            "./button": "./src/button.ts",
			            "./util/*": "./src/util/*.ts"},
			"main": "./dist/index.cjs"
		}`,
		"packages/core/package.json": `{"name": "@acme/core", "module": "./src/main.ts", "main": "./dist/main.js"}`,
		"packages/bare/package.json": `{"name": "@acme/bare"}`,
	})
	pi := newPkgIndex(root, files)

	t.Run("exports_condition_prefers_import", func(t *testing.T) {
		got, ok := pi.resolve("@acme/ui")
		require.True(t, ok)
		assert.Equal(t, "packages/ui/src/index.ts", got)
	})
	t.Run("exports_subpath", func(t *testing.T) {
		got, ok := pi.resolve("@acme/ui/button")
		require.True(t, ok)
		assert.Equal(t, "packages/ui/src/button.ts", got)
	})
	t.Run("exports_wildcard_subpath", func(t *testing.T) {
		got, ok := pi.resolve("@acme/ui/util/fmt")
		require.True(t, ok)
		assert.Equal(t, "packages/ui/src/util/fmt.ts", got)
	})
	t.Run("module_beats_main", func(t *testing.T) {
		got, ok := pi.resolve("@acme/core")
		require.True(t, ok)
		assert.Equal(t, "packages/core/src/main.ts", got)
	})
	t.Run("bare_package_falls_back_to_index", func(t *testing.T) {
		got, ok := pi.resolve("@acme/bare")
		require.True(t, ok)
		assert.Equal(t, "packages/bare/index", got)
	})
	t.Run("unknown_package_is_not_in_repo", func(t *testing.T) {
		_, ok := pi.resolve("react")
		assert.False(t, ok, "node_modules is not in the discovered set")
	})
}

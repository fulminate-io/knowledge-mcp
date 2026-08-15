// SPDX-License-Identifier: Apache-2.0

package jsmodule

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// virtualResolver builds a resolver over a file set that need not exist on
// disk. Only tsconfig/jsconfig and package.json are ever read from the
// filesystem, so a fixture with neither is purely in-memory.
func virtualResolver(t *testing.T, files []string, exports map[string]FileExports) *Resolver {
	t.Helper()
	r, err := NewResolver(t.TempDir(), files, exports)
	require.NoError(t, err)
	return r
}

// declares is shorthand for a file that declares exactly these names.
func declares(names ...string) FileExports {
	d := make(map[string]bool, len(names))
	for _, n := range names {
		d[n] = true
	}
	return FileExports{Declared: d}
}

// TestResolve_Rules covers the specifier ladder, the file rules and re-export
// following from literal file sets and literal FileExports — no chunker, and no
// type beyond the shared import carrier.
func TestResolve_Rules(t *testing.T) {
	t.Run("relative_extension_inference", func(t *testing.T) {
		r := virtualResolver(t,
			[]string{"web/src/a.ts", "web/src/x.ts"},
			map[string]FileExports{"web/src/x.ts": declares("Thing")})

		got, outcome := r.Resolve("web/src/a.ts", "./x", "Thing", treesitter.ImportNamed)
		assert.Equal(t, OutcomeBound, outcome)
		assert.Equal(t, Target{File: "web/src/x.ts", Name: "Thing"}, got)
	})

	t.Run("relative_index_file", func(t *testing.T) {
		r := virtualResolver(t,
			[]string{"web/src/a.ts", "web/src/widgets/index.ts"},
			map[string]FileExports{"web/src/widgets/index.ts": declares("Widget")})

		got, outcome := r.Resolve("web/src/a.ts", "./widgets", "Widget", treesitter.ImportNamed)
		assert.Equal(t, OutcomeBound, outcome)
		assert.Equal(t, "web/src/widgets/index.ts", got.File)
	})

	// A REAL TYPESCRIPT RULE, not a convenience: a .ts file importing './x.js'
	// means './x.ts', and the substitution is tried before extension inference.
	t.Run("js_to_ts_substitution", func(t *testing.T) {
		r := virtualResolver(t,
			[]string{"web/src/a.ts", "web/src/x.ts"},
			map[string]FileExports{"web/src/x.ts": declares("Thing")})

		got, outcome := r.Resolve("web/src/a.ts", "./x.js", "Thing", treesitter.ImportNamed)
		assert.Equal(t, OutcomeBound, outcome)
		assert.Equal(t, "web/src/x.ts", got.File)
	})

	t.Run("tsconfig_path_alias", func(t *testing.T) {
		root, files := writeTree(t, map[string]string{
			"web/tsconfig.json": `{
				"include": ["src"],
				"compilerOptions": {"baseUrl": ".", "paths": {"@app/*": ["src/*"]}}
			}`,
			"web/src/a.ts":     "export const a = 1;",
			"web/src/thing.ts": "export const Thing = 1;",
		})
		r, err := NewResolver(root, files, map[string]FileExports{
			"web/src/thing.ts": declares("Thing"),
		})
		require.NoError(t, err)

		got, outcome := r.Resolve("web/src/a.ts", "@app/thing", "Thing", treesitter.ImportNamed)
		assert.Equal(t, OutcomeBound, outcome)
		assert.Equal(t, Target{File: "web/src/thing.ts", Name: "Thing"}, got)
	})

	// A bare specifier that DOES name an in-repo workspace package is bound,
	// not out of repo — the branch the acceptance corpus cannot exercise
	// because it holds one package.json and no workspaces.
	t.Run("bare_workspace_package_bound", func(t *testing.T) {
		root, files := writeTree(t, map[string]string{
			"packages/ui/package.json": `{"name": "@acme/ui", "module": "./src/index.ts"}`,
			"packages/ui/src/index.ts": "export const Button = 1;",
			"web/src/a.ts":             "export const a = 1;",
		})
		r, err := NewResolver(root, files, map[string]FileExports{
			"packages/ui/src/index.ts": declares("Button"),
		})
		require.NoError(t, err)

		got, outcome := r.Resolve("web/src/a.ts", "@acme/ui", "Button", treesitter.ImportNamed)
		assert.Equal(t, OutcomeBound, outcome)
		assert.Equal(t, Target{File: "packages/ui/src/index.ts", Name: "Button"}, got)
	})

	// THE RENAME CATCHER. `import {A as B}` asks for A at the target even
	// though the reference writes B, so the Target carries the IMPORTED name.
	t.Run("renamed_named_import_uses_imported_name", func(t *testing.T) {
		r := virtualResolver(t,
			[]string{"web/src/a.ts", "web/src/x.ts"},
			map[string]FileExports{"web/src/x.ts": declares("A")})

		got, outcome := r.Resolve("web/src/a.ts", "./x", "A", treesitter.ImportNamed)
		assert.Equal(t, OutcomeBound, outcome)
		assert.Equal(t, "A", got.Name, "the DECLARED name at the target, never the local alias")
	})

	t.Run("namespace_target_has_no_name", func(t *testing.T) {
		r := virtualResolver(t,
			[]string{"web/src/a.ts", "web/src/x.ts"},
			map[string]FileExports{"web/src/x.ts": declares("thing")})

		got, outcome := r.Resolve("web/src/a.ts", "./x", "", treesitter.ImportNamespace)
		assert.Equal(t, OutcomeBound, outcome)
		assert.Equal(t, "web/src/x.ts", got.File)
		assert.Empty(t, got.Name,
			"a namespace import renames the MODULE, so members keep their own spelling")
	})

	t.Run("default_import_uses_default_name", func(t *testing.T) {
		r := virtualResolver(t,
			[]string{"web/src/a.ts", "web/src/App.tsx"},
			map[string]FileExports{
				"web/src/App.tsx": {Declared: map[string]bool{"App": true}, DefaultName: "App"},
			})

		got, outcome := r.Resolve("web/src/a.ts", "./App", "", treesitter.ImportDefault)
		assert.Equal(t, OutcomeBound, outcome)
		assert.Equal(t, Target{File: "web/src/App.tsx", Name: "App"}, got)
	})

	// A barrel that forwards rather than declares is followed to the file that
	// actually declares the name.
	t.Run("reexport_chain_followed", func(t *testing.T) {
		r := virtualResolver(t,
			[]string{"web/src/a.ts", "web/src/lib/index.ts", "web/src/lib/thing.ts"},
			map[string]FileExports{
				"web/src/lib/index.ts": {ReExports: []treesitter.ReExport{
					{Specifier: "./thing", Local: "Thing", Imported: "Inner"},
				}},
				"web/src/lib/thing.ts": declares("Inner"),
			})

		got, outcome := r.Resolve("web/src/a.ts", "./lib", "Thing", treesitter.ImportNamed)
		assert.Equal(t, OutcomeBound, outcome)
		assert.Equal(t, Target{File: "web/src/lib/thing.ts", Name: "Inner"}, got,
			"the chain resolves to the DECLARING file under its DECLARED name")
	})

	// THE CYCLE CATCHER, whose only real assertion is that Resolve RETURNS.
	// Two barrels re-exporting from each other produce an infinite descent that
	// a single-barrel fixture never reaches, so a guard wired to the wrong
	// recursion is invisible without this case.
	t.Run("mutual_reexport_cycle_terminates", func(t *testing.T) {
		r := virtualResolver(t,
			[]string{"web/main.ts", "web/a/index.ts", "web/b/index.ts"},
			map[string]FileExports{
				"web/a/index.ts": {ReExports: []treesitter.ReExport{{Specifier: "../b"}}},
				"web/b/index.ts": {ReExports: []treesitter.ReExport{{Specifier: "../a"}}},
			})

		got, outcome := r.Resolve("web/main.ts", "./a", "NeverDeclared", treesitter.ImportNamed)
		assert.Equal(t, "web/a/index.ts", got.File,
			"a cycle degrades to the last file resolved rather than hanging")
		assert.Equal(t, OutcomeNoNamedDecls, outcome)
	})

	// REFUSED MEANS "there is no key to record a bind under", which is a
	// different thing from any of the three classified outcomes below. It is
	// built on a specifier that WOULD resolve, deliberately: a wildcard pointed
	// at a missing file would refuse for the wrong reason and prove nothing
	// about the kind arm.
	t.Run("wildcard_refused", func(t *testing.T) {
		r := virtualResolver(t,
			[]string{"web/src/a.ts", "web/src/x.ts"},
			map[string]FileExports{"web/src/x.ts": declares("Thing")})

		// KNOWN-POSITIVE CONTROL: the same specifier resolves for a kind that
		// binds a name, so the refusal below is about the KIND.
		_, boundOutcome := r.Resolve("web/src/a.ts", "./x", "Thing", treesitter.ImportNamed)
		require.Equal(t, OutcomeBound, boundOutcome)

		got, outcome := r.Resolve("web/src/a.ts", "./x", "", treesitter.ImportWildcard)
		assert.Equal(t, OutcomeRefused, outcome)
		assert.Equal(t, Target{}, got, "a wildcard binds no local name")
	})

	t.Run("side_effect_refused", func(t *testing.T) {
		r := virtualResolver(t,
			[]string{"web/src/a.ts", "web/src/x.ts"},
			map[string]FileExports{"web/src/x.ts": declares("Thing")})

		_, outcome := r.Resolve("web/src/a.ts", "./x", "", treesitter.ImportSideEffect)
		assert.Equal(t, OutcomeRefused, outcome)
	})

	// THE THREE CLASSIFIED NON-BOUND ROWS. Each asserts the Target the caller
	// turns into a bind, not merely that the outcome is "not bound": after the
	// re-ruling the failure mode is OMITTING a bind, and a row asserting only
	// the outcome would pass an implementation that returns nothing to record.

	t.Run("external_out_of_repo", func(t *testing.T) {
		r := virtualResolver(t, []string{"web/src/a.ts"}, nil)

		got, outcome := r.Resolve("web/src/a.ts", "react", "useState", treesitter.ImportNamed)
		assert.Equal(t, OutcomeOutOfRepo, outcome)
		assert.Empty(t, got.File,
			"there is NO in-repo path to name, and a synthetic one could collide "+
				"with a real repo-root file — the caller records an EMPTY scope")
	})

	t.Run("external_undiscovered_relative", func(t *testing.T) {
		r := virtualResolver(t, []string{"web/src/a.ts"}, nil)

		got, outcome := r.Resolve("web/src/a.ts", "./missing", "Thing", treesitter.ImportNamed)
		assert.Equal(t, OutcomeUndiscovered, outcome)
		assert.Equal(t, Target{File: "web/src/missing", Name: "Thing"}, got,
			"the candidate path is real and repo-relative even though nothing was "+
				"discovered at it, so the bind is scoped BY THE CANDIDATE, not empty")
	})

	// THE IDENTITY IS THE ASSERTION. A discovered file that declares nothing
	// yields exactly the bind a declaring file would, which is what proves the
	// arm does nothing special for declaration-less files — the whole point of
	// moving externality into the declaration index.
	t.Run("external_no_named_declarations", func(t *testing.T) {
		r := virtualResolver(t,
			[]string{"web/src/a.ts", "web/src/empty.ts", "web/src/full.ts"},
			map[string]FileExports{"web/src/full.ts": declares("Thing")})

		empty, emptyOutcome := r.Resolve("web/src/a.ts", "./empty", "Thing", treesitter.ImportNamed)
		assert.Equal(t, OutcomeNoNamedDecls, emptyOutcome)
		assert.Equal(t, Target{File: "web/src/empty.ts", Name: "Thing"}, empty)

		full, fullOutcome := r.Resolve("web/src/a.ts", "./full", "Thing", treesitter.ImportNamed)
		require.Equal(t, OutcomeBound, fullOutcome)
		assert.Equal(t, full.Name, empty.Name,
			"same name, so the bind a caller records differs only in the file it points at")
		assert.NotEmpty(t, empty.File,
			"and it is NOT the empty target the out-of-repo row records")
	})
}

// ticketFixture is the shared tree for TestResolve_TicketRows: one shape per
// row the ticket names, in one repository layout rather than eight.
func ticketFixture(t *testing.T) *Resolver {
	t.Helper()
	root, files := writeTree(t, map[string]string{
		"web/tsconfig.json": `{
			/* Path aliases */
			"include": ["src"],
			"compilerOptions": {
				"baseUrl": ".",
				"paths": {"@app/*": ["src/*"], "@shared": ["src/shared/index.ts"]},
			},
		}`,
		"web/src/App.tsx":              "export default function App() { return null; }",
		"web/src/App.routing.test.tsx": "import App from './App';",
		"web/src/util.ts":              "export function helper() {}",
		"web/src/shared/index.ts":      "export const shared = 1;",
		"web/src/lib/index.ts":         "export {Thing} from './thing';",
		"web/src/lib/thing.ts":         "export class Thing {}",
		"packages/ui/package.json":     `{"name": "@acme/ui", "exports": {".": {"import": "./src/index.ts"}}}`,
		"packages/ui/src/index.ts":     "export const Button = 1;",
	})
	r, err := NewResolver(root, files, map[string]FileExports{
		"web/src/App.tsx":          {Declared: map[string]bool{"App": true}, DefaultName: "App"},
		"web/src/util.ts":          declares("helper"),
		"web/src/shared/index.ts":  declares("shared"),
		"web/src/lib/thing.ts":     declares("Thing"),
		"packages/ui/src/index.ts": declares("Button"),
		"web/src/lib/index.ts": {ReExports: []treesitter.ReExport{
			{Specifier: "./thing", Local: "Thing", Imported: "Thing"},
		}},
	})
	require.NoError(t, err)
	return r
}

// TestResolve_TicketRows is one subtest per import shape the ticket names.
func TestResolve_TicketRows(t *testing.T) {
	r := ticketFixture(t)
	const importer = "web/src/App.tsx"

	// `import {helper as h} from './util'` — the local alias h never reaches
	// the resolver, which is asked for the IMPORTED name.
	t.Run("aliased_named_import", func(t *testing.T) {
		got, outcome := r.Resolve(importer, "./util", "helper", treesitter.ImportNamed)
		assert.Equal(t, OutcomeBound, outcome)
		assert.Equal(t, Target{File: "web/src/util.ts", Name: "helper"}, got)
	})

	t.Run("default_import", func(t *testing.T) {
		got, outcome := r.Resolve("web/src/main.tsx", "./App", "", treesitter.ImportDefault)
		assert.Equal(t, OutcomeBound, outcome)
		assert.Equal(t, Target{File: "web/src/App.tsx", Name: "App"}, got)
	})

	t.Run("namespace_import", func(t *testing.T) {
		got, outcome := r.Resolve(importer, "./util", "", treesitter.ImportNamespace)
		assert.Equal(t, OutcomeBound, outcome)
		assert.Equal(t, "web/src/util.ts", got.File)
		assert.Empty(t, got.Name)
	})

	t.Run("index_file", func(t *testing.T) {
		got, outcome := r.Resolve(importer, "./shared", "shared", treesitter.ImportNamed)
		assert.Equal(t, OutcomeBound, outcome)
		assert.Equal(t, "web/src/shared/index.ts", got.File)
	})

	t.Run("tsconfig_path", func(t *testing.T) {
		got, outcome := r.Resolve(importer, "@app/util", "helper", treesitter.ImportNamed)
		assert.Equal(t, OutcomeBound, outcome)
		assert.Equal(t, Target{File: "web/src/util.ts", Name: "helper"}, got)

		// The exact (non-wildcard) alias key, which TypeScript tries first.
		exact, exactOutcome := r.Resolve(importer, "@shared", "shared", treesitter.ImportNamed)
		assert.Equal(t, OutcomeBound, exactOutcome)
		assert.Equal(t, "web/src/shared/index.ts", exact.File)
	})

	// THE ROW THE ACCEPTANCE CORPUS CANNOT COVER: that corpus holds exactly one
	// package.json and no workspaces, so zero bare specifiers resolve in-repo
	// there and this fixture is the ONLY proof the branch works.
	t.Run("package_json_exports", func(t *testing.T) {
		got, outcome := r.Resolve(importer, "@acme/ui", "Button", treesitter.ImportNamed)
		assert.Equal(t, OutcomeBound, outcome)
		assert.Equal(t, Target{File: "packages/ui/src/index.ts", Name: "Button"}, got)
	})

	t.Run("reexport_chain", func(t *testing.T) {
		got, outcome := r.Resolve(importer, "./lib", "Thing", treesitter.ImportNamed)
		assert.Equal(t, OutcomeBound, outcome)
		assert.Equal(t, Target{File: "web/src/lib/thing.ts", Name: "Thing"}, got,
			"the barrel forwards; the declaring file is the target")
	})

	// THE TICKET'S HEADLINE SHAPE, reproduced from an instance the live census
	// actually observes: web/src/App.routing.test.tsx:5 imports App from
	// './App', and that must land on the component file beside it.
	t.Run("tsx_component_test_pair", func(t *testing.T) {
		got, outcome := r.Resolve("web/src/App.routing.test.tsx", "./App", "", treesitter.ImportDefault)
		assert.Equal(t, OutcomeBound, outcome)
		assert.Equal(t, Target{File: "web/src/App.tsx", Name: "App"}, got)
	})
}

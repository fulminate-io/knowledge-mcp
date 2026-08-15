// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chunkImportFixture chunks one fixture and returns its FILE-LEVEL context plus
// every edge the file emitted.
//
// The context is read off a chunk because that is where the chunker attaches
// it; every chunk of one file carries the same file context. The require on a
// non-empty chunk set is the known-positive control for the whole table: a
// fixture that produced no chunk would yield a zero-valued ChunkContext, and
// every "expected empty" assertion below would pass against it vacuously.
func chunkImportFixture(t *testing.T, path, src string) (ChunkContext, []Edge) {
	t.Helper()
	chunker := NewChunker()
	defer chunker.Close()

	res, err := chunker.ChunkFile(context.Background(), path, []byte(src))
	require.NoError(t, err)
	require.NotEmpty(t, res.Chunks,
		"fixture %s produced no chunks, so its file context cannot be read", path)
	return res.Chunks[0].Context, res.Edges
}

// bindingFor returns the single binding whose Local name matches, failing when
// the arm recorded none — the shape most of the table asserts against.
func bindingFor(t *testing.T, ctx ChunkContext, local string) ImportBinding {
	t.Helper()
	for _, b := range ctx.ImportBindings {
		if b.Local == local {
			return b
		}
	}
	t.Fatalf("no ImportBinding with Local %q; got %+v", local, ctx.ImportBindings)
	return ImportBinding{}
}

// importEdgeTargets returns the sorted ToIDs of the file's IMPORTS edges.
func importEdgeTargets(edges []Edge) []string {
	var out []string
	for _, e := range edges {
		if e.Type == EdgeImports {
			out = append(out, e.ToID)
		}
	}
	sort.Strings(out)
	return out
}

// TestImportBindings_ECMAScript covers the import-clause walk across .ts, .tsx
// and .js — each subtest on a language where its shape is legal, and both
// ECMAScript query sets exercised (queries_typescript.go serves .ts and .tsx,
// queries_javascript.go serves .js).
//
// There is deliberately NO wildcard subtest: no ECMAScript source can produce
// an ImportWildcard, so a case here would have to fabricate the record rather
// than capture it. That constant's real exercise is the module resolver's
// refusal case.
func TestImportBindings_ECMAScript(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		ctx, _ := chunkImportFixture(t, "src/a.ts", `
import A from './x';
function useIt() { return A; }
`)
		b := bindingFor(t, ctx, "A")
		assert.Equal(t, ImportDefault, b.Kind)
		assert.Equal(t, "./x", b.Specifier)
		assert.Empty(t, b.Imported, "a default import names no member of the module")
		assert.False(t, b.TypeOnly)
		assert.Equal(t, []string{"./x"}, ctx.Imports)
	})

	t.Run("named", func(t *testing.T) {
		ctx, _ := chunkImportFixture(t, "src/b.js", `
import {A} from './x';
function useIt() { return A; }
`)
		b := bindingFor(t, ctx, "A")
		assert.Equal(t, ImportNamed, b.Kind)
		assert.Equal(t, "./x", b.Specifier)
		assert.Equal(t, "A", b.Imported, "an unaliased named import is bound under its own name")
	})

	// THE CHILD-INDEX CATCHER. This is the only subtest that fails if the walk
	// reads named-child 0 as the LOCAL name: child 0 is the name in the SOURCE
	// module and child 1 is the local one, and every other named-import case
	// has them equal.
	t.Run("renamed", func(t *testing.T) {
		ctx, _ := chunkImportFixture(t, "src/c.tsx", `
import {A as B} from './x';
function useIt() { return B; }
`)
		b := bindingFor(t, ctx, "B")
		assert.Equal(t, ImportNamed, b.Kind)
		assert.Equal(t, "A", b.Imported, "the name in the SOURCE module")
		assert.Equal(t, "B", b.Local, "the name bound in THIS file")
	})

	t.Run("namespace", func(t *testing.T) {
		ctx, _ := chunkImportFixture(t, "src/d.js", `
import * as ns from './x';
function useIt() { return ns.thing; }
`)
		b := bindingFor(t, ctx, "ns")
		assert.Equal(t, ImportNamespace, b.Kind, "a namespace import binds the MODULE ITSELF")
		assert.Empty(t, b.Imported)
	})

	// A side-effect import binds no name and is recorded anyway, so the table
	// stays a faithful record of the statement list rather than a filtered one.
	t.Run("side_effect", func(t *testing.T) {
		ctx, _ := chunkImportFixture(t, "src/e.ts", `
import './side';
function useIt() { return 1; }
`)
		require.Len(t, ctx.ImportBindings, 1)
		b := ctx.ImportBindings[0]
		assert.Equal(t, ImportSideEffect, b.Kind)
		assert.Equal(t, "./side", b.Specifier)
		assert.Empty(t, b.Local, "a side-effect import binds no name")
		assert.Equal(t, []string{"./side"}, ctx.Imports,
			"it is still a dependency and still earns its one Imports entry")
	})

	// Type-only imports are CAPTURED, not skipped: a TypeScript type reference
	// is exactly what a `type` import brings in.
	t.Run("type_only", func(t *testing.T) {
		ctx, _ := chunkImportFixture(t, "src/f.ts", `
import type {C as D} from './t';
function useIt(v: D) { return v; }
`)
		b := bindingFor(t, ctx, "D")
		assert.Equal(t, ImportNamed, b.Kind)
		assert.Equal(t, "C", b.Imported)
		assert.True(t, b.TypeOnly, "the statement carries a `type` modifier")
	})

	t.Run("reexport_renamed", func(t *testing.T) {
		ctx, _ := chunkImportFixture(t, "src/g.tsx", `
export {A as B} from './z';
function useIt() { return 1; }
`)
		require.Len(t, ctx.ReExports, 1)
		assert.Equal(t, ReExport{Specifier: "./z", Local: "B", Imported: "A"}, ctx.ReExports[0])
		assert.Empty(t, ctx.ImportBindings, "a re-export binds nothing in THIS file")
		assert.Equal(t, []string{"./z"}, ctx.Imports,
			"a re-export specifier IS a dependency")
	})

	// THE DEFAULT-EXPORT CATCHER: the only subtest that fails if the export arm
	// ignores the `default` form.
	t.Run("default_export_name", func(t *testing.T) {
		ctx, _ := chunkImportFixture(t, "src/h.js", `
export default function App() { return 1; }
`)
		assert.Equal(t, "App", ctx.DefaultExportName)

		ctxIdent, _ := chunkImportFixture(t, "src/i.js", `
function App2() { return 1; }
export default App2;
`)
		assert.Equal(t, "App2", ctxIdent.DefaultExportName)

		ctxNone, _ := chunkImportFixture(t, "src/j.js", `
export function plain() { return 1; }
`)
		assert.Empty(t, ctxNone.DefaultExportName,
			"a file with no default export names nothing")
	})

	// THE ONE-EDGE-PER-STATEMENT RULE, ASSERTED IN BOTH DIRECTIONS over one
	// fixture. Either half alone is satisfiable by a wrong implementation:
	//   positive — a re-export WITH a source is a dependency and the sourceless
	//              `export {X}` is not, so exactly three IMPORTS edges exist and
	//              their targets are exactly the three specifiers;
	//   negative — the binding table is strictly larger than the edge set, which
	//              is what fails if the query regresses to per-clause matching
	//              and every clause starts appending its own specifier.
	t.Run("imports_edges_per_statement", func(t *testing.T) {
		ctx, edges := chunkImportFixture(t, "src/k.ts", `
import E, {F as G, H} from './a';
import {I as J} from './b';
const X = 1;
export {X};
export {Y} from './c';
function useIt() { return [E, G, H, J, X]; }
`)
		targets := importEdgeTargets(edges)
		assert.Equal(t, []string{"./a", "./b", "./c"}, targets,
			"one IMPORTS edge per dependency-declaring statement: two imports and "+
				"the re-export WITH a source, never the sourceless `export {X}`")
		assert.Greater(t, len(ctx.ImportBindings), len(targets),
			"binding-table entries never become edges: four bindings (E, G, H, J) "+
				"against three statements")
	})

	// THE DISPATCH CATCHER. A capture-name whitelist in the dispatch would leave
	// ctx.Imports empty for the languages whose ONLY Imports capture is @import
	// — the capture name eight languages already use — while every ECMAScript
	// subtest above still passed, so without this case the whitelist bug ships
	// green.
	//
	// THE SUBSTRING FORM IS DELIBERATE — do not tighten it to an equality. The
	// java query binds the WHOLE declaration node, so an entry is the full
	// statement text ("import com.example.alpha.Thing;") and not a bare path.
	// An equality against the bare path would be RED AGAINST CORRECT WORK. The
	// substring form holds under either shape and still goes red if the
	// dispatch drops the captures — which is the property this subtest exists
	// to protect.
	//
	// JAVA NOW HAS A REGISTERED ARM, so the binding half of this case moved to
	// GROOVY, which still has none. Java's entry to ctx.Imports is unchanged
	// (an arm owns that list and reproduces exactly what the default dispatch
	// appended), and it now ALSO records bindings — asserted positively here so
	// the case states today's behavior rather than yesterday's.
	t.Run("at_import_languages_unaffected", func(t *testing.T) {
		ctx, _ := chunkImportFixture(t, "src/Foo.java", `
import com.example.alpha.Thing;
import com.example.beta.Other;

public class Foo {
    public void run() {}
}
`)
		require.Len(t, ctx.Imports, 2,
			"an @import language records one entry per import statement")
		joined := strings.Join(ctx.Imports, "\n")
		assert.Contains(t, joined, "com.example.alpha.Thing")
		assert.Contains(t, joined, "com.example.beta.Other")
		assert.Len(t, ctx.ImportBindings, 2,
			"java's registered arm records one binding per named import")

		groovy, _ := chunkImportFixture(t, "src/Foo.groovy", `
import com.example.alpha.Thing

class Foo {
    def run() {}
}
`)
		require.NotEmpty(t, groovy.Imports,
			"the default dispatch arm stays name-blind for a language with no arm")
		assert.Empty(t, groovy.ImportBindings,
			"a language with no registered arm records no bindings")
	})
}

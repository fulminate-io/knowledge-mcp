// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// delete_args_keyset_test.go is SIDE B of a two-sided handshake locking the
// `delete` tool's read-key set.
//
// WHY TWO SIDES. delete's schema (DeleteToolDef) lives in package tools, but its
// arguments are decoded HERE, and deleteArgs is unexported — so no test in
// package tools can reflect it, and no test here may import tools (tools imports
// engine; the reverse is a cycle). SIDE A is TestResidueToolSchemas_DeclareEveryReadKey
// in package tools, whose `delete` row repeats this same list and asserts the
// published schema declares exactly it. The repetition IS the contract: a key
// added on one side and not the other fails whichever side was not updated.
//
// WHAT THIS SIDE CATCHES, claimed narrowly: NAMED-FIELD drift on deleteArgs, and
// DELETE-PATH decode-SITE-COUNT drift. A new key added INSIDE an existing
// anonymous struct is caught only by the site count changing, or by a reviewer —
// reflecting a named struct plus a literal cannot see it.

// deleteToolReadKeys is the locked read surface: deleteArgs' json tags plus the
// literal "format", which both render paths decode from an anonymous struct.
var deleteToolReadKeys = []string{
	"ids", "id", "older_than", "type", "session_id", "dry_run", "hard", "graph", "language", "repo", "account", "format",
}

// deleteDecodeSites names every function that decodes the DELETE tool's caller
// payload, and how many decodes each performs.
//
// SCOPED TO FUNCTIONS, NOT FILES, and that scoping is the point. dispatch.go
// holds four other tools' renderers, so a FILE-level count over the three files
// returns eight; a gate asserting four against eight would be permanently red
// against correct work. Scoping to renderDeleteTool's body counts one.
//
// compile_delete.go likewise holds parseHardFlag, which decodes the `hard` FIELD
// value rather than the tool payload — a separate function, so this scoping
// excludes it structurally rather than by a name filter.
var deleteDecodeSites = map[string]struct {
	file  string
	count int
}{
	"compileDelete":         {file: "compile_delete.go", count: 1},
	"dispatchDeletePreview": {file: "dispatch_delete_preview.go", count: 2},
	"renderDeleteTool":      {file: "dispatch.go", count: 1},
}

// deleteDecodeSiteTotal is the locked total across those functions.
const deleteDecodeSiteTotal = 4

// TestDeleteArgs_ReadKeySetIsLocked asserts both legs: the named-field surface
// and the delete-path decode-site count.
func TestDeleteArgs_ReadKeySetIsLocked(t *testing.T) {
	t.Run("named_fields_plus_format", func(t *testing.T) {
		rt := reflect.TypeFor[deleteArgs]()
		got := make([]string, 0, rt.NumField()+1)
		for f := range rt.Fields() {
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" || name == "-" {
				continue
			}
			got = append(got, name)
		}
		// "format" is read by both render paths from an anonymous struct, so it
		// has no deleteArgs field to reflect.
		got = append(got, "format")

		assert.ElementsMatch(t, deleteToolReadKeys, got,
			"deleteArgs' json tags plus \"format\" must equal the locked delete read-key set — "+
				"update this list AND the `delete` row of TestResidueToolSchemas_DeclareEveryReadKey together")
	})

	t.Run("decode_site_count", func(t *testing.T) {
		total := 0
		for fn, want := range deleteDecodeSites {
			got := countUnmarshalsInFunc(t, want.file, fn)
			assert.Equalf(t, want.count, got,
				"%s (%s) decodes the delete payload %d times, expected %d — a new decode site may read a key neither side of this handshake knows about",
				fn, want.file, got, want.count)
			total += got
		}
		assert.Equal(t, deleteDecodeSiteTotal, total,
			"the delete path must carry exactly this many caller-payload decode sites")
	})
}

// countUnmarshalsInFunc parses file and counts json.Unmarshal calls in the body
// of the named top-level function. Nested function declarations are separate
// FuncDecls and are therefore not counted here.
func countUnmarshalsInFunc(t *testing.T, file, fn string) int {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	require.NoErrorf(t, err, "parsing %s", file)

	for _, decl := range parsed.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name.Name != fn || fd.Body == nil {
			continue
		}
		count := 0
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Unmarshal" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "json" {
				count++
			}
			return true
		})
		return count
	}
	require.Failf(t, "function not found",
		"%s declares no top-level func %s — the delete path moved; re-derive this table", file, fn)
	return 0
}

// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"go/ast"
	goparser "go/parser"
	"go/token"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dispatchFunctions are the four helpers evalFunc tries in turn. Every builtin
// the interpreter can execute is a case in exactly one of them.
var dispatchFunctions = map[string]string{
	"evalStringFunc": "interpret_expr.go",
	"evalGraphFunc":  "interpret_expr.go",
	"evalBoolFunc":   "interpret_expr.go",
	"evalRenderFunc": "interpret_render.go",
}

// TestBuiltinTable_CoversEveryDispatchCase pins builtinTable to the DISPATCH
// rather than to anybody's memory of it.
//
// A builtin added to a dispatch switch without a table entry would be reported
// as an unknown name by the validator and refused — a correct recipe refused by
// a validator that is merely out of date, which is the worst failure a
// refuse-before-the-walk design can have. This test reads the switches
// themselves, so the drift is caught at build time instead.
func TestBuiltinTable_CoversEveryDispatchCase(t *testing.T) {
	fset := token.NewFileSet()
	found := map[string]string{}

	for fn, file := range dispatchFunctions {
		parsed, err := goparser.ParseFile(fset, file, nil, 0)
		require.NoError(t, err, "parsing %s", file)

		var body *ast.FuncDecl
		for _, decl := range parsed.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok && fd.Name.Name == fn {
				body = fd
				break
			}
		}
		require.NotNil(t, body, "dispatch helper %s not found in %s", fn, file)

		ast.Inspect(body, func(n ast.Node) bool {
			clause, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, expr := range clause.List {
				lit, ok := expr.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				name, err := strconv.Unquote(lit.Value)
				require.NoError(t, err)
				found[name] = fn
			}
			return true
		})
	}

	// KNOWN-POSITIVE: the walk must have read real switches, not zero of them.
	// Without this a parse that silently matched nothing would report a vacuous
	// pass over an empty set.
	require.GreaterOrEqual(t, len(found), 16, "the four dispatch switches carry at least 16 builtins")
	assert.Contains(t, found, "subtree_concat", "the render dispatch was read")
	assert.Contains(t, found, "has_edge", "the graph dispatch was read")

	for name, fn := range found {
		assert.Contains(t, builtinTable, name,
			"builtin %q is dispatched in %s but has no builtinTable entry, so the validator would refuse it as unknown", name, fn)
	}
	for name := range builtinTable {
		assert.Contains(t, found, name,
			"builtinTable carries %q, which no dispatch helper implements", name)
	}
}

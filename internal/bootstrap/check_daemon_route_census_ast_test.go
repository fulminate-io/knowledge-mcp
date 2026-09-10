// SPDX-License-Identifier: Apache-2.0

package bootstrap

// check_daemon_route_census_ast_test.go — the go/ast primitives the refusal
// census is built out of: the function scopes the guard derivation walks, the
// statement lists inside them, the two shapes it reads (a json.Marshal
// assignment and an `x != nil` test, both resolved through go/types rather than
// by name), the folding of a static string, and the package's own test-function
// index. Split from the walk so each file stays readable and neither approaches
// the repository's file length cap.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// funcScopes collects every function body in the file, func literals included as
// scopes of their own. It carries the BODY and nothing else: the guard
// derivation reads the statements, and the parameter names it once also needed
// went away with the provenance derivation that the mandatory-provenance rule
// replaced.
func funcScopes(file *ast.File) []*ast.BlockStmt {
	var scopes []*ast.BlockStmt
	ast.Inspect(file, func(n ast.Node) bool {
		switch fn := n.(type) {
		case *ast.FuncDecl:
			if fn.Body != nil {
				scopes = append(scopes, fn.Body)
			}
		case *ast.FuncLit:
			scopes = append(scopes, fn.Body)
		}
		return true
	})
	return scopes
}

// walkStmtLists calls visit with every statement list inside body — blocks, case
// clauses and select clauses alike — WITHOUT descending into nested func
// literals, which funcScopes hands back as scopes of their own.
func walkStmtLists(body *ast.BlockStmt, visit func([]ast.Stmt)) {
	ast.Inspect(body, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		switch s := n.(type) {
		case *ast.BlockStmt:
			visit(s.List)
		case *ast.CaseClause:
			visit(s.Body)
		case *ast.CommClause:
			visit(s.Body)
		}
		return true
	})
}

// jsonMarshalAssign reads `value, err := json.Marshal(x)` and returns the OBJECT
// the error landed in and the expression that was marshaled. The callee is
// resolved through go/types, so a second import alias of encoding/json reads the
// same as the first, which a selector-text comparison could not tell apart.
//
// NOTHING HERE REJECTS A DISCARDED ERROR, and it does not need to: the two
// couplings that follow do it. `_, _ = json.Marshal(...)` binds the blank
// identifier, which has no object at all, and no compiling source can then test
// `_ != nil` — so a marshal whose error is thrown away guards nothing by
// construction. A check for it was written first and deleted when killing it
// left the whole suite green; an unobserved guard reads as protection and is not.
func jsonMarshalAssign(assign *ast.AssignStmt, info *types.Info) (types.Object, ast.Expr, bool) {
	if len(assign.Rhs) != 1 || len(assign.Lhs) != 2 {
		return nil, nil, false
	}
	call, ok := assign.Rhs[0].(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return nil, nil, false
	}
	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return nil, nil, false
	}
	fn, ok := info.ObjectOf(sel.Sel).(*types.Func)
	if !ok || fn.FullName() != "encoding/json.Marshal" {
		return nil, nil, false
	}
	ident, ok := assign.Lhs[1].(*ast.Ident)
	if !ok {
		return nil, nil, false
	}
	return info.ObjectOf(ident), call.Args[0], true
}

// testsNonNil reports whether cond is exactly `<errObj> != nil`, by object
// identity rather than by name, so a shadowed err does not read as the outer one.
func testsNonNil(cond ast.Expr, errObj types.Object, info *types.Info) bool {
	bin, ok := cond.(*ast.BinaryExpr)
	if !ok || bin.Op != token.NEQ {
		return false
	}
	left, ok := ast.Unparen(bin.X).(*ast.Ident)
	if !ok || info.ObjectOf(left) != errObj {
		return false
	}
	right, ok := ast.Unparen(bin.Y).(*ast.Ident)
	return ok && right.Name == "nil"
}

// staticString folds a string literal, including one written as several literals
// joined by +, which is how the longer refusal messages are wrapped. Anything
// else — a const, a variable, a call — is NOT folded, and the site is refused by
// the enumeration rather than dropped.
func staticString(expr ast.Expr) (string, bool) {
	switch e := ast.Unparen(expr).(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(e.Value)
		if err != nil {
			return "", false
		}
		return s, true
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		left, ok := staticString(e.X)
		if !ok {
			return "", false
		}
		right, ok := staticString(e.Y)
		if !ok {
			return "", false
		}
		return left + right, true
	}
	return "", false
}

// exprText renders an expression as the source spells it.
func exprText(fset *token.FileSet, src []byte, expr ast.Expr) string {
	start := fset.Position(expr.Pos()).Offset
	end := fset.Position(expr.End()).Offset
	if start < 0 || end > len(src) || start >= end {
		return "<unreadable>"
	}
	return strings.Join(strings.Fields(string(src[start:end])), " ")
}

// packageTestFuncs is the set of test function names declared in this package.
func packageTestFuncs(t *testing.T) map[string]bool {
	t.Helper()
	paths, err := filepath.Glob("*_test.go")
	require.NoError(t, err)
	require.NotEmpty(t, paths, "control: the package's own test files must be readable from the test's working directory")

	fset := token.NewFileSet()
	names := map[string]bool{}
	for _, path := range paths {
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		require.NoError(t, err, "parse %s", path)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			names[fn.Name.Name] = true
		}
	}
	return names
}

// censusReadSource reads one source file of this package for the AST censuses.
func censusReadSource(path string) ([]byte, error) {
	return os.ReadFile(path) //nolint:gosec // a test reading a source file of its own package
}

// SPDX-License-Identifier: Apache-2.0

// Package topology / dead_code_reflect.go — per-function risk detection
// for the dead_code analyzer (client-side).
//
// RTA is conservative on dynamic dispatch: reflective invocation,
// //go:linkname-redirected names, and assembly stubs all undermine its
// "unreachable" conclusion. We flag these specifically rather than
// silently surface them as definitive dead code.
package topology

import (
	"go/ast"
	"strings"
)

// detectReflectionRisk inspects each dead function's AST body to flag
// cases where RTA's "unreachable" conclusion may be wrong.
func detectReflectionRisk(rows []deadCodeRow, deadFuncs []deadFunc) []reviewFlag {
	flags := make([]reviewFlag, len(rows))
	for i, df := range deadFuncs {
		if i >= len(flags) {
			break
		}
		flags[i] = classifyDeadFunc(df)
	}
	return flags
}

func classifyDeadFunc(df deadFunc) reviewFlag {
	if df.Func == nil {
		return reviewFlagNone
	}
	if df.Func.Blocks == nil {
		return reviewFlagAssembly
	}
	decl := findFuncDecl(df)
	if decl == nil {
		return reviewFlagNone
	}
	if hasLinknameDirective(decl) {
		return reviewFlagLinkname
	}
	if hasReflectInvocation(decl) {
		return reviewFlagReflect
	}
	return reviewFlagNone
}

func findFuncDecl(df deadFunc) *ast.FuncDecl {
	if df.Pkg == nil {
		return nil
	}
	target := df.Func.Pos()
	for _, file := range df.Pkg.Syntax {
		for _, decl := range file.Decls {
			fnDecl, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if fnDecl.Name != nil && fnDecl.Name.Pos() == target {
				return fnDecl
			}
		}
	}
	return nil
}

func hasLinknameDirective(decl *ast.FuncDecl) bool {
	if decl == nil || decl.Doc == nil {
		return false
	}
	for _, c := range decl.Doc.List {
		if strings.HasPrefix(c.Text, "//go:linkname") {
			return true
		}
	}
	return false
}

func hasReflectInvocation(decl *ast.FuncDecl) bool {
	if decl == nil || decl.Body == nil {
		return false
	}
	found := false
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if ident.Name != "reflect" {
			return true
		}
		if isReflectInvocationName(sel.Sel.Name) {
			found = true
			return false
		}
		return true
	})
	return found
}

func isReflectInvocationName(name string) bool {
	switch name {
	case "ValueOf", "MakeFunc", "Indirect", "MethodByName", "Call", "CallSlice":
		return true
	}
	return false
}

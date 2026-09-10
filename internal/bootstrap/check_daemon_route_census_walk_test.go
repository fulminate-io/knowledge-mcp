// SPDX-License-Identifier: Apache-2.0

package bootstrap

// check_daemon_route_census_walk_test.go — WHETHER AN ARM CAN BE DRIVEN, AND
// WHOSE VALUE DECIDES THAT.
//
// Three of the routing file's refusals report a json.Marshal failing, and no
// test can drive them: json.Marshal does not fail on the values reached there.
// A row may therefore say "unreachable" instead of naming a test — which is a
// claim about the code, so the census DERIVES it rather than believing it.
//
// IDENTITY, NOT ADJACENCY. An earlier derivation asked only whether a
// json.Marshal had run before the refusal, which a single dead
// `_, _ = json.Marshal(struct{}{})` launders into a false unreachability claim.
// This one requires the refusal to CARRY the very error variable the Marshal
// assigned, inside the `if err != nil` that tests that same variable. Both
// halves are read through go/types object identity, so a second import alias of
// encoding/json, or a shadowed err, cannot slip past a name comparison.
//
// AND THE PROVENANCE OF THE MARSHALED VALUE, WHICH IS NOW A SUBTRACTION. Round
// four tried to decide whether a marshaled local was really the function's own
// value: it treated an identifier assigned from a composite literal as built
// here, and a reviewer laundered that by filling an empty map literal from the
// caller's parameter in a copy loop. That derivation is gone. Only a composite
// literal WRITTEN AT THE CALL SITE is unreachable on the code alone; a marshal
// of any IDENTIFIER, however that identifier was filled, requires the row to
// name the test that pins what can be in it. There is no classification left to
// launder, and the cost is one named test.

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"
)

// marshalGuard records that a refusal carries the error returned by a
// json.Marshal, together with what that Marshal was handed.
type marshalGuard struct {
	guarded     bool
	valueKind   string
	valueText   string
	marshalLine int
}

// The provenance verdicts for a marshaled value.
const (
	marshalValueLiteral     = "a composite literal written at the call site"
	marshalValueOpenLiteral = "a composite literal reading identifiers, whose contents this census cannot see"
	marshalValueIdentifier  = "an identifier, whose contents this census cannot see"
	marshalValueUnknown     = "an expression this census cannot trace to its construction"
)

// marshalGuards derives, for every refusal site in the file, whether it carries
// the error of a json.Marshal and what that Marshal encoded. The result is keyed
// by the position of the expression the enumeration reports, so a wrapped
// construction and a bare relay of the same error are both reachable from it.
func marshalGuards(file *ast.File, info *types.Info, fset *token.FileSet, src []byte) map[token.Pos]marshalGuard {
	guards := map[token.Pos]marshalGuard{}
	for _, body := range funcScopes(file) {
		walkStmtLists(body, func(list []ast.Stmt) {
			for i, stmt := range list {
				if assign, ok := stmt.(*ast.AssignStmt); ok && i+1 < len(list) {
					if errObj, value, ok := jsonMarshalAssign(assign, info); ok {
						if ifs, ok := list[i+1].(*ast.IfStmt); ok && testsNonNil(ifs.Cond, errObj, info) {
							markGuarded(guards, ifs.Body, errObj, value, info, fset, src)
						}
					}
				}
				if ifs, ok := stmt.(*ast.IfStmt); ok {
					if init, ok := ifs.Init.(*ast.AssignStmt); ok {
						if errObj, value, ok := jsonMarshalAssign(init, info); ok && testsNonNil(ifs.Cond, errObj, info) {
							markGuarded(guards, ifs.Body, errObj, value, info, fset, src)
						}
					}
				}
			}
		})
	}
	return guards
}

// markGuarded records the guard on every refusal inside body that carries
// errObj: a call handed the error as an argument, which is the wrapping shape,
// and a bare return of the error itself, which is the relay shape.
func markGuarded(
	guards map[token.Pos]marshalGuard, body *ast.BlockStmt, errObj types.Object, value ast.Expr,
	info *types.Info, fset *token.FileSet, src []byte,
) {
	guard := marshalGuard{
		guarded:     true,
		valueKind:   classifyMarshalValue(value, info),
		valueText:   exprText(fset, src, value),
		marshalLine: fset.Position(value.Pos()).Line,
	}
	carries := func(expr ast.Expr) bool {
		ident, ok := ast.Unparen(expr).(*ast.Ident)
		return ok && info.ObjectOf(ident) == errObj
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			if slices.ContainsFunc(node.Args, carries) {
				guards[node.Pos()] = guard
			}
		case *ast.ReturnStmt:
			for _, operand := range node.Results {
				if carries(operand) {
					guards[operand.Pos()] = guard
				}
			}
		}
		return true
	})
}

// classifyMarshalValue answers whose value the Marshal encoded: an identifier is
// a name whose contents came from somewhere this census does not follow, a
// composite literal is the function's own value ONLY IF ITS LEAVES ARE CONSTANTS,
// and anything else it will not vouch for at all.
//
// THE LITERAL EXEMPTION USED TO READ THE SHAPE AND NOTHING ELSE, which exempted a
// literal whatever it read. Wrapping the caller's own parameter as
// `json.Marshal(map[string]any{"a": args})` made the marshaled bytes entirely
// caller-controlled while classifying as the function's own value, and the row
// could then drop its provenance test — with the census not merely silent but
// NAMING that removal as the remedy. So the exemption is now about the CONTENTS:
// a literal every leaf of which is a constant expression carries nothing a caller
// could have put there, and a literal carrying any identifier owes the provenance
// test on the same terms as a bare identifier does.
func classifyMarshalValue(value ast.Expr, info *types.Info) string {
	switch v := ast.Unparen(value).(type) {
	case *ast.CompositeLit:
		return literalProvenance(v, info)
	case *ast.UnaryExpr:
		if v.Op == token.AND {
			if lit, ok := ast.Unparen(v.X).(*ast.CompositeLit); ok {
				return literalProvenance(lit, info)
			}
		}
	case *ast.Ident:
		if _, ok := info.ObjectOf(v).(*types.Var); ok {
			return marshalValueIdentifier
		}
	}
	return marshalValueUnknown
}

// literalProvenance answers a composite literal by what its leaves read.
func literalProvenance(lit *ast.CompositeLit, info *types.Info) string {
	if constantLiteral(lit, info) {
		return marshalValueLiteral
	}
	return marshalValueOpenLiteral
}

// constantLiteral reports whether every leaf of a composite literal is a constant
// expression: a literal, a named constant, or a nested literal of those.
//
// A STRUCT LITERAL'S KEY IS A FIELD NAME, not a value, so it is not read — which
// is decided from the literal's own TYPE rather than from whether go/types
// recorded the key, so a map literal's key is checked like any other leaf.
func constantLiteral(lit *ast.CompositeLit, info *types.Info) bool {
	_, keysAreFields := underlyingOf(info.TypeOf(lit)).(*types.Struct)
	for _, elt := range lit.Elts {
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			if !keysAreFields && !constantExpr(kv.Key, info) {
				return false
			}
			if !constantExpr(kv.Value, info) {
				return false
			}
			continue
		}
		if !constantExpr(elt, info) {
			return false
		}
	}
	return true
}

// constantExpr reports whether one leaf carries a value no caller could have put
// there: a constant go/types folded, or a nested literal of such leaves.
func constantExpr(expr ast.Expr, info *types.Info) bool {
	switch e := ast.Unparen(expr).(type) {
	case *ast.CompositeLit:
		return constantLiteral(e, info)
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			if lit, ok := ast.Unparen(e.X).(*ast.CompositeLit); ok {
				return constantLiteral(lit, info)
			}
		}
	}
	tv, ok := info.Types[ast.Unparen(expr)]
	return ok && tv.Value != nil
}

// underlyingOf reads a type's underlying type, nil-safe.
func underlyingOf(t types.Type) types.Type {
	if t == nil {
		return nil
	}
	return t.Underlying()
}

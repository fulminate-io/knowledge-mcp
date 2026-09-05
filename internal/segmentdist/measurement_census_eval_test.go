// SPDX-License-Identifier: Apache-2.0

package segmentdist

// measurement_census_eval_test.go is the EVALUATOR third of the measurement-gate
// census: function-scoped binding collection, exact constant folding, the
// upper-bound widening, and the expression renderers. measurement_census_scope_test.go
// parses; measurement_census_test.go gates. Three files rather than one only because
// lefthook caps a staged Go file at 500 lines.

import (
	"fmt"
	"go/ast"
	"go/token"
	"strconv"
	"strings"
)

// censusCollectLocals records function-scoped integer bindings for one unit.
func (p *censusPkg) censusCollectLocals(u *censusUnit) {
	record := func(name string, val ast.Expr) {
		if name == "" || name == "_" {
			return
		}
		v, ok := p.constFold(val, u)
		if !ok {
			// Not a constant, but possibly bounded: `n := 3 + rng.Intn(3)` is at most
			// 5, and a ceiling is all a threshold comparison needs.
			if b, bok := p.censusUpperBound(val, u); bok {
				u.bounds[name] = max(u.bounds[name], b)
			}
			return
		}
		if prev, seen := u.locals[name]; seen && prev != v {
			u.ambiguous[name] = true
			return
		}
		u.locals[name] = v
	}
	ast.Inspect(u.body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.GenDecl:
			// Grouped const and var blocks: an ast pattern for `const $N = $V`
			// matches a single spec and misses every grouped one, which is how a
			// 2040-document corpus went unseen. Walking specs sees both shapes.
			for _, spec := range s.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if i < len(vs.Values) {
						record(name.Name, vs.Values[i])
					}
				}
			}
		case *ast.AssignStmt:
			if len(s.Lhs) != len(s.Rhs) {
				return true
			}
			for i, lhs := range s.Lhs {
				if id, ok := lhs.(*ast.Ident); ok {
					record(id.Name, s.Rhs[i])
				}
			}
		}
		return true
	})
}

// constFold evaluates an integer expression exactly. SIZES LIVE IN VAR AS WELL AS
// CONST and are written as CONSTANT EXPRESSIONS, so this handles both and does the
// arithmetic rather than matching a literal.
func (p *censusPkg) constFold(e ast.Expr, u *censusUnit) (int, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.INT {
			return 0, false
		}
		v, err := strconv.ParseInt(strings.ReplaceAll(x.Value, "_", ""), 0, 64)
		if err != nil {
			return 0, false
		}
		return int(v), true
	case *ast.ParenExpr:
		return p.constFold(x.X, u)
	case *ast.UnaryExpr:
		v, ok := p.constFold(x.X, u)
		if !ok {
			return 0, false
		}
		if x.Op == token.SUB {
			return -v, true
		}
		return v, ok
	case *ast.Ident:
		if u != nil {
			if u.ambiguous[x.Name] {
				return 0, false
			}
			if v, ok := u.locals[x.Name]; ok {
				return v, true
			}
		}
		v, ok := p.pkgConsts[x.Name]
		return v, ok
	case *ast.SelectorExpr:
		v, ok := censusExternalConsts[censusExprString(x)]
		return v, ok
	case *ast.BinaryExpr:
		l, lok := p.constFold(x.X, u)
		r, rok := p.constFold(x.Y, u)
		if !lok || !rok {
			return 0, false
		}
		switch x.Op {
		case token.ADD:
			return l + r, true
		case token.SUB:
			return l - r, true
		case token.MUL:
			return l * r, true
		case token.QUO:
			if r == 0 {
				return 0, false
			}
			return l / r, true
		case token.REM:
			if r == 0 {
				return 0, false
			}
			return l % r, true
		case token.SHL:
			return l << r, true
		case token.SHR:
			return l >> r, true
		}
	}
	return 0, false
}

// censusUpperBound is constFold widened to an UPPER BOUND, and it is the only place
// the census accepts a size that is not a compile-time constant. The rule is a
// bound, never a guess: a pseudo-random draw is bounded by its own argument
// (rand's IntN/Intn/UintN return values in [0,k)), so `3 + rng.Intn(3)` is at most
// 5 and the census can decide it without knowing the seed. An expression with no
// derivable bound resolves to nothing and its leg site FATALS.
func (p *censusPkg) censusUpperBound(e ast.Expr, u *censusUnit) (int, bool) {
	if v, ok := p.constFold(e, u); ok {
		return v, true
	}
	switch x := e.(type) {
	case *ast.Ident:
		if u != nil {
			if v, ok := u.bounds[x.Name]; ok {
				return v, true
			}
		}
	case *ast.ParenExpr:
		return p.censusUpperBound(x.X, u)
	case *ast.BinaryExpr:
		l, lok := p.censusUpperBound(x.X, u)
		r, rok := p.censusUpperBound(x.Y, u)
		if !lok || !rok || l < 0 || r < 0 {
			return 0, false
		}
		switch x.Op {
		case token.ADD:
			return l + r, true
		case token.MUL:
			return l * r, true
		}
	case *ast.CallExpr:
		if fn, ok := x.Fun.(*ast.Ident); ok && fn.Name == "len" && len(x.Args) == 1 {
			switch arg := x.Args[0].(type) {
			case *ast.CompositeLit:
				return len(arg.Elts), true
			case *ast.Ident:
				if u != nil {
					if n, ok := u.lens[arg.Name]; ok {
						return n, true
					}
				}
			}
			return 0, false
		}
		sel, ok := x.Fun.(*ast.SelectorExpr)
		if !ok || len(x.Args) != 1 {
			return 0, false
		}
		switch sel.Sel.Name {
		case "IntN", "Intn", "UintN", "Uintn", "N":
			k, kok := p.censusUpperBound(x.Args[0], u)
			if !kok || k <= 0 {
				return 0, false
			}
			return k - 1, true
		}
	}
	return 0, false
}

// censusTypeString renders a type expression for comparison.
func censusTypeString(e ast.Expr) string { return censusExprString(e) }

// censusExprString renders an expression as source-ish text, enough to compare
// selectors and types by name.
func censusExprString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.StarExpr:
		return "*" + censusExprString(x.X)
	case *ast.SelectorExpr:
		return censusExprString(x.X) + "." + x.Sel.Name
	case *ast.ArrayType:
		if x.Len == nil {
			return "[]" + censusExprString(x.Elt)
		}
		return "[" + censusExprString(x.Len) + "]" + censusExprString(x.Elt)
	case *ast.IndexExpr:
		return censusExprString(x.X) + "[" + censusExprString(x.Index) + "]"
	case *ast.IndexListExpr:
		// A generic instantiation with more than one type argument. Every build sink
		// in this package is one (`*distManager[Q, S]`), so rendering it as an
		// unknown node type is how the derived sink set came back empty.
		parts := make([]string, 0, len(x.Indices))
		for _, idx := range x.Indices {
			parts = append(parts, censusExprString(idx))
		}
		return censusExprString(x.X) + "[" + strings.Join(parts, ", ") + "]"
	case *ast.CallExpr:
		return censusExprString(x.Fun) + "(...)"
	case *ast.ParenExpr:
		return censusExprString(x.X)
	case *ast.BasicLit:
		return x.Value
	case *ast.BinaryExpr:
		return censusExprString(x.X) + x.Op.String() + censusExprString(x.Y)
	}
	return fmt.Sprintf("%T", e)
}

// censusContainsFuncLit reports whether an expression encloses a function literal.
func censusContainsFuncLit(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			found = true
		}
		return !found
	})
	return found
}

// SPDX-License-Identifier: Apache-2.0

package bootstrap

// check_daemon_route_census_keys_test.go — HOW A REFUSAL SITE IS KEYED.
//
// The enumeration in check_daemon_route_census_types_test.go decides WHICH
// expressions are refusals, by the result they fill. This file decides what each
// one is CALLED in the declared table, which is the half a reviewer beat four
// times: every earlier answer was a spelling, and a spelling can always be
// written another way.
//
// So there is no whitelist here. A call is keyed by the function go/types says
// it reaches — fmt.Errorf and errors.New by their static message, which is the
// text a reader recognizes, and every other function by its name, so a
// package-local error constructor and a pass-through of a standard library call
// are both keyed rather than walked past. A composite literal is keyed by its
// type. An identifier is resolved to its object: a package-level var is a
// sentinel keyed by its declaration, and a local is keyed by the one expression
// the function assigns into it, one hop.
//
// AND WHAT IS REFUSED IS REFUSED BY LINE, never dropped. A message that is not a
// static literal cannot be keyed. A call into the errors package that is not
// errors.New builds or transforms an error in a way this census does not
// classify. An error that flows in from a PARAMETER was constructed by a caller,
// so there is nothing in this file to declare. A local the function assigns in
// several places has no single construction to name. Each is a red, because the
// alternative — skipping it — is exactly how a refusal becomes deletable with
// the suite green.

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
)

// classify keys one expression by how the error it yields was built. depth
// bounds the one hop it takes through a local identifier to that identifier's
// assignment, so a self-referential assignment cannot spin.
func (w *refusalWalk) classify(expr ast.Expr, fr censusFrame, depth int) (key, kind, refusal string) {
	if depth > 1 {
		return "", "", fmt.Sprintf(censusUntraceable, w.render(expr))
	}
	switch e := ast.Unparen(expr).(type) {
	case *ast.CallExpr:
		return w.classifyCall(e)
	case *ast.CompositeLit:
		return w.typedLiteral(e)
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			if lit, ok := ast.Unparen(e.X).(*ast.CompositeLit); ok {
				return w.typedLiteral(lit)
			}
		}
	case *ast.Ident:
		return w.classifyIdent(e, fr, depth)
	case *ast.SelectorExpr:
		return w.classifySelector(e)
	}
	return "", "", fmt.Sprintf(censusUntraceable, w.render(expr))
}

// classifyCall keys a call: a message constructor by its message, anything else
// by the function it reaches.
func (w *refusalWalk) classifyCall(call *ast.CallExpr) (key, kind, refusal string) {
	if tv, ok := w.info.Types[call.Fun]; ok && tv.IsType() {
		return "", "", fmt.Sprintf(censusUntraceable, w.render(call))
	}
	fn := w.callee(call)
	if fn == nil {
		return "", "", fmt.Sprintf(censusUnnamedCallee, w.render(call))
	}
	switch fn.FullName() {
	case "fmt.Errorf", "errors.New":
		// NOTHING HERE REJECTS A ZERO-ARGUMENT CONSTRUCTOR, and it does not need
		// to: both take a first parameter that is not variadic, so `fmt.Errorf()`
		// and `errors.New()` do not compile and the census can never be handed
		// one. The check was written first and deleted when killing it left the
		// whole suite green; an unobserved guard reads as protection and is not.
		message, ok := staticString(call.Args[0])
		if !ok {
			return "", "", fmt.Sprintf(censusNonStaticMessage, fn.FullName(), w.render(call.Args[0]))
		}
		return message, constructionMessage, ""
	}
	if fn.Pkg() != nil && fn.Pkg().Path() == "errors" {
		return "", "", fmt.Sprintf(censusUnclassifiedConstructor, fn.FullName())
	}
	if fn.Pkg() == w.pkg {
		return "the error " + w.renderFunc(fn) + " returns", constructionLocalCall, ""
	}
	return "the error " + w.renderFunc(fn) + " returns", constructionRelay, ""
}

// typedLiteral keys a composite literal of a type used as an error.
func (w *refusalWalk) typedLiteral(lit *ast.CompositeLit) (key, kind, refusal string) {
	typ := w.info.TypeOf(lit)
	if typ == nil {
		return "", "", fmt.Sprintf(censusUntraceable, w.render(lit))
	}
	return "a " + types.TypeString(typ, w.qualifier()) + " value", constructionTypedLit, ""
}

// classifyIdent keys an identifier by what built it: a package-level sentinel by
// its declaration, a local by the one expression the function assigns it.
func (w *refusalWalk) classifyIdent(ident *ast.Ident, fr censusFrame, depth int) (key, kind, refusal string) {
	v, ok := w.info.ObjectOf(ident).(*types.Var)
	if !ok {
		return "", "", fmt.Sprintf(censusUntraceable, ident.Name)
	}
	// A package-level var is a sentinel, and one declared in ANOTHER package is
	// a sentinel too: io.EOF handed back here is as much a declared refusal of
	// this file as one declared beside it, and the reader needs it in the table.
	if v.Pkg() != nil && v.Parent() == v.Pkg().Scope() {
		return "the package-level sentinel " + w.renderVar(v), constructionSentinel, ""
	}
	if isParameter(v, fr.sig) {
		return "", "", fmt.Sprintf(censusFromParameter, ident.Name, fr.name)
	}
	assigned := w.assignedExprs(v, fr)
	switch len(assigned) {
	case 0:
		return "", "", fmt.Sprintf(censusNoConstruction, ident.Name, fr.name)
	case 1:
		return w.classify(assigned[0], fr, depth+1)
	default:
		return "", "", fmt.Sprintf(censusManyConstructions, ident.Name, fr.name, len(assigned))
	}
}

// classifySelector keys a qualified name. A package-level var reached through
// its package is a sentinel exactly as a local one is — io.EOF handed back here
// is as much a declared refusal of this file as a sentinel declared beside it.
// Anything else
// behind a selector, a struct field above all, has no construction this census
// can name.
func (w *refusalWalk) classifySelector(sel *ast.SelectorExpr) (key, kind, refusal string) {
	v, ok := w.info.ObjectOf(sel.Sel).(*types.Var)
	if !ok || v.Pkg() == nil || v.Parent() != v.Pkg().Scope() {
		return "", "", fmt.Sprintf(censusUntraceable, w.render(sel))
	}
	return "the package-level sentinel " + w.renderVar(v), constructionSentinel, ""
}

// isParameter reports whether v is one of sig's parameters. A NAMED RESULT is
// deliberately not one: a result is assigned inside the body and the walk can
// see what built it, while a parameter's construction happened in a caller.
func isParameter(v *types.Var, sig *types.Signature) bool {
	if sig == nil {
		return false
	}
	for param := range sig.Params().Variables() {
		if param == v {
			return true
		}
	}
	return sig.Recv() == v
}

// assignedExprs collects every expression the frame's body writes into v.
func (w *refusalWalk) assignedExprs(v *types.Var, fr censusFrame) []ast.Expr {
	if fr.body == nil {
		return nil
	}
	var out []ast.Expr
	record := func(names []*ast.Ident, values []ast.Expr) {
		for i, name := range names {
			if w.info.ObjectOf(name) != v {
				continue
			}
			switch {
			case len(values) == len(names):
				out = append(out, values[i])
			case len(values) == 1:
				out = append(out, values[0])
			}
		}
	}
	ast.Inspect(fr.body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			names := make([]*ast.Ident, 0, len(s.Lhs))
			for _, lhs := range s.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok {
					ident = ast.NewIdent("_")
				}
				names = append(names, ident)
			}
			record(names, s.Rhs)
		case *ast.ValueSpec:
			record(s.Names, s.Values)
		}
		return true
	})
	return out
}

// callee resolves the function a call reaches, method values included.
func (w *refusalWalk) callee(call *ast.CallExpr) *types.Func {
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		if fn, ok := w.info.ObjectOf(fun).(*types.Func); ok {
			return fn
		}
	case *ast.SelectorExpr:
		if sel, ok := w.info.Selections[fun]; ok {
			if fn, ok := sel.Obj().(*types.Func); ok {
				return fn
			}
		}
		if fn, ok := w.info.ObjectOf(fun.Sel).(*types.Func); ok {
			return fn
		}
	}
	return nil
}

// qualifier spells a package as a reader would: bare inside this package, by
// package name outside it.
func (w *refusalWalk) qualifier() types.Qualifier {
	return func(p *types.Package) string {
		if p == w.pkg {
			return ""
		}
		return p.Name()
	}
}

// renderFunc names a function for a census key: the bare name inside this
// package, and a receiver-qualified or package-qualified name outside it.
func (w *refusalWalk) renderFunc(fn *types.Func) string {
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		return "(" + types.TypeString(sig.Recv().Type(), w.qualifier()) + ")." + fn.Name()
	}
	if fn.Pkg() == nil || fn.Pkg() == w.pkg {
		return fn.Name()
	}
	return fn.Pkg().Name() + "." + fn.Name()
}

// renderVar names a package-level variable for a census key: bare inside this
// package, package-qualified outside it.
func (w *refusalWalk) renderVar(v *types.Var) string {
	if v.Pkg() == nil || v.Pkg() == w.pkg {
		return v.Name()
	}
	return v.Pkg().Name() + "." + v.Name()
}

// render spells an expression as the source under test writes it.
func (w *refusalWalk) render(expr ast.Expr) string {
	return exprText(w.fset, w.src, expr)
}

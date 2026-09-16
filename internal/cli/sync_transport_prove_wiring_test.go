// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestBuildSyncTransport_BothReturnPathsCarryProveOnRefusal asserts
// structurally what the behavioral test cannot assert on every host.
//
// BuildSyncTransport has TWO returns — the machine-bearer branch and the
// credential-store branch — and a partial edit enables the recovery on one and
// forgets the other. The forgotten one would most likely be the machine-bearer
// path, which is exactly the headless population least likely to have a daemon
// running and therefore the one that most needs to prove for itself.
//
// The source assertion complements the behavioral fixture: both constructions
// must receive the option from the shared helper, which opens the executable
// and binds the real challenge responder.
func TestBuildSyncTransport_BothReturnPathsCarryProveOnRefusal(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "sync_transport.go", nil, 0)
	if err != nil {
		t.Fatalf("parse sync_transport.go: %v", err)
	}

	var fn, proof *ast.FuncDecl
	ast.Inspect(file, func(n ast.Node) bool {
		d, ok := n.(*ast.FuncDecl)
		if ok && d.Name.Name == "BuildSyncTransport" && d.Recv == nil {
			fn = d
		}
		if ok && d.Name.Name == "syncTransportProof" && d.Recv == nil {
			proof = d
		}
		return true
	})
	// KNOWN-POSITIVE CONTROL: a census that could not find the function would
	// otherwise report the same silence as a function with no calls at all.
	if fn == nil {
		t.Fatalf("BuildSyncTransport was not found in sync_transport.go, so this census examined nothing")
	}

	var constructions [][]ast.Expr
	var bindsProof bool
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if assignment, ok := n.(*ast.AssignStmt); ok && len(assignment.Lhs) == 1 && len(assignment.Rhs) == 1 {
			name, named := assignment.Lhs[0].(*ast.Ident)
			call, called := assignment.Rhs[0].(*ast.CallExpr)
			if named && called {
				helper, direct := call.Fun.(*ast.Ident)
				bindsProof = bindsProof || (name.Name == "prove" && direct && helper.Name == "syncTransportProof")
			}
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch {
		case pkg.Name == "auth" && sel.Sel.Name == "NewSyncTransport":
			constructions = append(constructions, call.Args)
		}
		return true
	})

	if len(constructions) != 2 {
		t.Fatalf("expected BuildSyncTransport to construct a transport on exactly TWO return paths, found %d; the shape this test guards has changed and the assertion below no longer means what it says", len(constructions))
	}
	for i, args := range constructions {
		if len(args) != 3 {
			t.Errorf("return path %d constructs its transport with %d arguments and therefore carries NO prove-on-refusal option; that path's users stay refused forever on a machine that runs no daemon", i+1, len(args))
			continue
		}
		option, ok := args[2].(*ast.Ident)
		if !ok || option.Name != "prove" {
			t.Errorf("return path %d does not pass the shared proof option", i+1)
		}
	}
	if !bindsProof || proof == nil {
		t.Fatal("BuildSyncTransport must bind prove from the existing syncTransportProof helper")
	}
	var opensSelf, returnsResponder bool
	ast.Inspect(proof.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
				if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "clientver" && selector.Sel.Name == "OpenSelf" {
					opensSelf = true
				}
			}
		}
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			return true
		}
		call, ok := ret.Results[0].(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := selector.X.(*ast.Ident)
		if !ok || pkg.Name != "auth" || selector.Sel.Name != "WithProveOnRefusal" {
			return true
		}
		responder, ok := call.Args[0].(*ast.SelectorExpr)
		if ok {
			pkg, ok := responder.X.(*ast.Ident)
			returnsResponder = ok && pkg.Name == "clientver" && responder.Sel.Name == "AnswerChallenge"
		}
		return true
	})
	if !opensSelf || !returnsResponder {
		t.Error("syncTransportProof must open the executable and return proof wired to clientver.AnswerChallenge")
	}
}

// TestDesktopAccountTransportCarriesProveOnRefusal asserts structurally what no
// behavioral test in this package can.
//
// The Desktop account route builds its own transport rather than going through
// BuildSyncTransport, so it does not inherit the pin above. Every behavioral
// test replaces desktopAccountTransport or injects desktopAuth.transport, which
// means the production construction is the one line nothing observes: dropping
// the proof option there leaves the whole suite green and leaves a Desktop user
// on a machine that runs no daemon refused forever.
func TestDesktopAccountTransportCarriesProveOnRefusal(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "desktop_auth.go", nil, 0)
	if err != nil {
		t.Fatalf("parse desktop_auth.go: %v", err)
	}

	var literal *ast.FuncLit
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || spec.Names[0].Name != "desktopAccountTransport" || len(spec.Values) != 1 {
			return true
		}
		if fn, ok := spec.Values[0].(*ast.FuncLit); ok {
			literal = fn
		}
		return true
	})
	// KNOWN-POSITIVE CONTROL: a census that could not find the construction
	// would otherwise report the same silence as a construction with no proof.
	if literal == nil {
		t.Fatalf("desktopAccountTransport was not found as a function literal in desktop_auth.go, so this census examined nothing")
	}

	var constructions [][]ast.Expr
	ast.Inspect(literal.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "auth" && sel.Sel.Name == "NewSyncTransport" {
			constructions = append(constructions, call.Args)
		}
		return true
	})
	if len(constructions) != 1 {
		t.Fatalf("expected desktopAccountTransport to construct exactly ONE transport, found %d; the shape this test guards has changed and the assertion below no longer means what it says", len(constructions))
	}
	for _, arg := range constructions[0] {
		call, ok := arg.(*ast.CallExpr)
		if !ok {
			continue
		}
		if helper, ok := call.Fun.(*ast.Ident); ok && helper.Name == "syncTransportProof" {
			return
		}
	}
	t.Error("the Desktop account transport is constructed without a syncTransportProof() argument, so a Desktop user on a machine that runs no daemon can never answer the version challenge")
}

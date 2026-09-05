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
// The credential-store branch cannot be driven end to end from a test binary,
// because this repo deliberately refuses the real credential store there — a
// test that reached it would read and write the developer's own keychain. That
// guard is not weakened to make a fixture convenient. So the both-paths
// property is asserted where it IS observable on every host: in the source,
// over every transport construction in that function.
func TestBuildSyncTransport_BothReturnPathsCarryProveOnRefusal(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "sync_transport.go", nil, 0)
	if err != nil {
		t.Fatalf("parse sync_transport.go: %v", err)
	}

	var fn *ast.FuncDecl
	ast.Inspect(file, func(n ast.Node) bool {
		d, ok := n.(*ast.FuncDecl)
		if ok && d.Name.Name == "BuildSyncTransport" && d.Recv == nil {
			fn = d
		}
		return true
	})
	// KNOWN-POSITIVE CONTROL: a census that could not find the function would
	// otherwise report the same silence as a function with no calls at all.
	if fn == nil {
		t.Fatalf("BuildSyncTransport was not found in sync_transport.go, so this census examined nothing")
	}

	var constructions [][]ast.Expr
	var opensSelf bool
	ast.Inspect(fn.Body, func(n ast.Node) bool {
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
		case pkg.Name == "clientver" && sel.Sel.Name == "OpenSelf":
			opensSelf = true
		}
		return true
	})

	if len(constructions) != 2 {
		t.Fatalf("expected BuildSyncTransport to construct a transport on exactly TWO return paths, found %d; the shape this test guards has changed and the assertion below no longer means what it says", len(constructions))
	}
	for i, args := range constructions {
		if len(args) < 3 {
			t.Errorf("return path %d constructs its transport with %d arguments and therefore carries NO prove-on-refusal option; that path's users stay refused forever on a machine that runs no daemon", i+1, len(args))
		}
	}
	// The handle the proof reads from is opened here too, so the recovery
	// cannot fail for a reason that has nothing to do with the gateway.
	if !opensSelf {
		t.Errorf("BuildSyncTransport does not open the executable handle, so every proof it drives would fail with an unopened-handle error")
	}
}

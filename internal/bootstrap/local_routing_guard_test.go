// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// local_routing_guard_test.go is the structural lock that keeps the local
// graph client sync-only: it scans the real source of
// cmd/knowledge/internal/bootstrap and cmd/knowledge/internal/tools
// and FAILS if any non-test .go file captures the BARE LOCAL graph client
// (`c.local` or the dialLocal result `tcp`) into an Execute/CRUD consumer
// outside the allowlist. The local server is sync-only when logged in: every
// graph read/write must route through the login-aware Router (c.router /
// GraphCaller()), never the bare local handle — otherwise a cloud-only
// (logged-in, no local server) daemon dials :15022 and fails.
//
// What the detector catches (intra-function, by design):
//   - direct call-argument capture: `workercrud.New(c.local)`
//   - simple same-function aliasing:  `x := c.local; workercrud.New(x)`
//   - method-value capture:           `fn := c.local.Execute`
//
// Residual boundary (NOT caught — documented, not a gap to fix here): a
// bare-local threaded through a STRUCT FIELD, RETURNED from a helper, or passed
// ACROSS function boundaries defeats this intra-function scan. Doing so requires
// deliberate multi-line indirection across scopes; the guard's job is to stop
// the one-line re-introduction of the recurring bug, not to prove whole-program
// non-escape. Deeper struct-field / return-threading indirection is the residual
// boundary.
//
// Allowlist (legitimate bare-local sites that MUST NOT flag):
//   - graphclient.NewRouter(tcp, ...)          — the Router IS the local wrapper
//   - startKeepaliveFn(tcp, ...)               — gated liveness keepalive
//   - c.local.Healthy()/.HealthyCtx()/.Status()— liveness probes (method calls)
//   - graphClientCaller{gc: c.local}           — the sync-seam composite literal
//   - MCPClientConfig{Client: c.local}         — liveness field on the MCP client
//   - func (c *client) LocalLiveness() ... { return c.local } — the narrowed
//     liveness accessor return
//
// prune is NO LONGER allowlisted — the auto-prune wiring was DELETED in Phase A
// (prune.go removed). If a future prune.Run(c.local) reappears, the guard flags it.

// bareLocalCallAllowlist is the set of callee function names a bare-local
// expression MAY legitimately be passed to as a call argument.
var bareLocalCallAllowlist = map[string]struct{}{
	"NewRouter":        {}, // graphclient.NewRouter(tcp, ...) — wraps the local handle in the Router
	"startKeepaliveFn": {}, // gated liveness keepalive over the bare local
}

// livenessSelectors are the method names that are pure liveness probes — a
// selector on the bare local naming one of these is allowed (both as a call and
// as a value).
var livenessSelectors = map[string]struct{}{
	"Healthy":    {},
	"HealthyCtx": {},
	"Status":     {},
}

// isBareLocalExpr reports whether e is one of the canonical bare-local seed
// expressions: the `c.local` selector or the bare `tcp` dial result identifier.
func isBareLocalExpr(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name == "tcp"
	case *ast.SelectorExpr:
		id, ok := x.X.(*ast.Ident)
		return ok && id.Name == "c" && x.Sel.Name == "local"
	}
	return false
}

// exprIsTrackedLocal reports whether e is a bare-local seed expression OR a
// local identifier currently tracked as an alias of the bare local.
func exprIsTrackedLocal(e ast.Expr, aliases map[string]struct{}) bool {
	if isBareLocalExpr(e) {
		return true
	}
	if id, ok := e.(*ast.Ident); ok {
		_, tracked := aliases[id.Name]
		return tracked
	}
	return false
}

// guardViolation is one flagged bare-local capture.
type guardViolation struct {
	pos  token.Position
	kind string // "call-arg" | "method-value"
	desc string
}

// scanFuncBody walks a single function body, tracking same-function aliases of
// the bare local and flagging non-allowlisted captures. The walk is two-pass-ish
// in one ast.Inspect: assignments seed/extend the alias set BEFORE their
// surrounding statements are flagged, which is sufficient for the straight-line
// `x := c.local; sink(x)` shape this guard targets.
func scanFuncBody(fset *token.FileSet, body *ast.BlockStmt, sink func(guardViolation)) {
	aliases := map[string]struct{}{}

	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			// Track `lhs := c.local` / `lhs = x` (x a tracked alias) as a new alias.
			for i, rhs := range node.Rhs {
				if i >= len(node.Lhs) {
					break
				}
				if exprIsTrackedLocal(rhs, aliases) {
					if lhsID, ok := node.Lhs[i].(*ast.Ident); ok && lhsID.Name != "_" {
						aliases[lhsID.Name] = struct{}{}
					}
				}
				// Method-value capture: `fn := c.local.Execute` — RHS is a
				// SelectorExpr (taken as a value, not called) on a tracked local
				// naming a NON-liveness method.
				if sel, ok := rhs.(*ast.SelectorExpr); ok && exprIsTrackedLocal(sel.X, aliases) {
					if _, liveness := livenessSelectors[sel.Sel.Name]; !liveness {
						sink(guardViolation{
							pos:  fset.Position(sel.Pos()),
							kind: "method-value",
							desc: "bare-local method value captured: ." + sel.Sel.Name,
						})
					}
				}
			}
		case *ast.CallExpr:
			// Liveness method CALL on the bare local (c.local.Healthy()) is allowed
			// and must not be treated as a bare-local argument-capture.
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok && exprIsTrackedLocal(sel.X, aliases) {
				if _, liveness := livenessSelectors[sel.Sel.Name]; liveness {
					return true
				}
			}
			calleeName := calleeName(node.Fun)
			if _, ok := bareLocalCallAllowlist[calleeName]; ok {
				return true // allowlisted callee — bare-local args permitted.
			}
			for _, arg := range node.Args {
				if exprIsTrackedLocal(arg, aliases) {
					sink(guardViolation{
						pos:  fset.Position(arg.Pos()),
						kind: "call-arg",
						desc: "bare-local passed to non-allowlisted callee " + calleeName,
					})
				}
			}
		}
		return true
	})
}

// calleeName returns the trailing identifier of a call's Fun expression:
// `workercrud.New` → "New", `startKeepaliveFn` → "startKeepaliveFn".
func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// scanDirForBareLocal parses every non-test .go file under dir and returns all
// guard violations. The accessor return (LocalLiveness) and composite-literal
// captures (graphClientCaller{gc: c.local}, MCPClientConfig{Client: c.local})
// are NOT call expressions and so are never reached by the call-arg / method-
// value flagging — they are structurally outside the detector and need no
// explicit exemption.
func scanDirForBareLocal(t *testing.T, dir string) []guardViolation {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse dir %s: %v", dir, err)
	}
	var out []guardViolation
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				scanFuncBody(fset, fn.Body, func(v guardViolation) {
					out = append(out, v)
				})
			}
		}
	}
	return out
}

// TestNoBareLocalExecuteCapture is the live structural lock: scan the real
// bootstrap/ and tools/ source and assert ZERO bare-local Execute/CRUD captures
// outside the allowlist. Goes red the moment a routing fix is reverted (a
// subsystem grabs c.local instead of c.router).
func TestNoBareLocalExecuteCapture(t *testing.T) {
	// The test runs with CWD = the bootstrap package dir.
	dirs := []string{".", filepath.Join("..", "tools")}
	for _, dir := range dirs {
		violations := scanDirForBareLocal(t, dir)
		for _, v := range violations {
			t.Errorf("bare-local capture (%s) at %s: %s", v.kind, v.pos, v.desc)
		}
	}
}

// TestBareLocalDetector_SelfCheck PROVES the detector's FAIL path (acceptance
// criterion 3) without touching production source: parse synthetic in-memory
// snippets and assert the detector's verdict on each evasion shape.
func TestBareLocalDetector_SelfCheck(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantFlag bool
	}{
		{
			name:     "direct-arg violation",
			body:     `c.workerCRUD = workercrud.New(c.local)`,
			wantFlag: true,
		},
		{
			name:     "same-function alias violation",
			body:     "x := c.local\n\tc.workerCRUD = workercrud.New(x)",
			wantFlag: true,
		},
		{
			name:     "method-value-capture violation",
			body:     `fn := c.local.Execute`,
			wantFlag: true,
		},
		{
			name:     "tcp direct-arg violation",
			body:     `c.graphTypeCRUD = graphtypecrud.New(tcp)`,
			wantFlag: true,
		},
		{
			name:     "allowlisted liveness method call",
			body:     `_ = c.local.Healthy()`,
			wantFlag: false,
		},
		{
			name:     "allowlisted composite-literal sync seam",
			body:     `_ = graphClientCaller{gc: c.local}`,
			wantFlag: false,
		},
		{
			name:     "allowlisted NewRouter construction",
			body:     `router := graphclient.NewRouter(tcp, endpoint, ts, as)`,
			wantFlag: false,
		},
		{
			name:     "allowlisted keepalive",
			body:     `startKeepaliveFn(tcp, ctx)`,
			wantFlag: false,
		},
		{
			name:     "allowlisted liveness method-value capture",
			body:     `fn := c.local.Healthy`,
			wantFlag: false,
		},
		{
			name:     "routed call (c.router) not flagged",
			body:     `c.workerCRUD = workercrud.New(c.router)`,
			wantFlag: false,
		},
		{
			name:     "accessor return not flagged",
			body:     `return c.local`,
			wantFlag: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			src := "package p\nfunc f(c *client) {\n\t" + tc.body + "\n}\n"
			file, err := parser.ParseFile(fset, "synthetic.go", src, 0)
			if err != nil {
				t.Fatalf("parse synthetic snippet: %v", err)
			}
			var flagged bool
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				scanFuncBody(fset, fn.Body, func(guardViolation) { flagged = true })
			}
			if flagged != tc.wantFlag {
				t.Errorf("detector verdict = flagged:%v, want flagged:%v for body %q", flagged, tc.wantFlag, tc.body)
			}
		})
	}
}

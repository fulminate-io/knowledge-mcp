// SPDX-License-Identifier: Apache-2.0

package calibration

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// unverifiedMarker is the declaration every environment-gated test in this
// package must carry.
//
// WHY IT EXISTS. Three tests here assert the harness's END-TO-END properties and
// all three are gated on environment variables that appear in NO Makefile and NO
// CI file, so they skip in every standing run. A skip reads as a pass to every
// gate above it: a criterion asserting go test's exit status goes green on a
// skipped test exactly as it does on a passing one. The properties are therefore
// unproven, and nothing in the output said so.
//
// The repair is not to un-gate a test that genuinely needs a network, a daemon
// and a mirror worktree — it is to make the UNPROVEN CLAIM VISIBLE AT THE SITE
// THAT CLAIMS IT, so a reader of the source or of a verbose run learns which
// properties this package does not actually verify here.
const unverifiedMarker = "UNVERIFIED IN CI:"

// TestEveryGatedTestDeclaresWhatGoesUnverified is ALWAYS ON and drives from an
// ENUMERATION of the package's own skip sites rather than from a hand-written
// list of test names.
//
// THE DISTINCTION IS THE WHOLE POINT. A hand-written table checked against
// another hand-written table can only detect DISAGREEMENT between the two; it
// cannot detect a MISSING entry, because a test absent from both lists is absent
// from the comparison too. Walking every t.Skip call in the package means a new
// gated test cannot be added without either declaring what it leaves unverified
// or turning this red.
func TestEveryGatedTestDeclaresWhatGoesUnverified(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}

	skipSites := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		// ParseComments is REQUIRED, not incidental: the declaration belongs in
		// the doc comment, which is where a reader looks, and without this mode
		// FuncDecl.Doc is nil and the scan reads only the body.
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, e.Name(), src, parser.ParseComments)
		if perr != nil {
			t.Fatalf("parse %s: %v", e.Name(), perr)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			skips := countSkipCalls(fn.Body)
			if skips == 0 {
				continue
			}
			skipSites += skips
			// Span the DOC COMMENT as well as the body. FuncDecl.Pos() is the
			// `func` keyword, so a declaration written where a reader actually
			// looks — above the function — sits outside that range.
			start := fn.Pos()
			if fn.Doc != nil {
				start = fn.Doc.Pos()
			}
			body := string(src[fset.Position(start).Offset:fset.Position(fn.End()).Offset])
			if !strings.Contains(body, unverifiedMarker) {
				t.Errorf("%s:%d %s can skip (%d skip sites) but never declares what that leaves unverified — add a %q line naming the property, so a reader learns which end-to-end claim this package does not actually prove",
					e.Name(), fset.Position(fn.Pos()).Line, fn.Name.Name, skips, unverifiedMarker)
			}
		}
	}

	// KNOWN-POSITIVE CONTROL. Without it, a walk that matched nothing — a
	// mis-set working directory, a selector that stopped recognizing the call
	// shape — is indistinguishable from a package with no gated tests at all.
	if skipSites == 0 {
		t.Fatal("no t.Skip call site was found anywhere in this package, so this scan measured nothing; the walk is broken, because this package has environment-gated tests")
	}
	// Stated as a count rather than as a verdict: on the failure path the errors
	// above are the verdict, and a line here claiming every test declares its
	// gap would contradict them.
	t.Logf("gated-test declaration scan: examined %d skip sites across this package", skipSites)
}

// countSkipCalls counts t.Skip / t.Skipf / t.SkipNow calls in a body.
func countSkipCalls(body *ast.BlockStmt) int {
	n := 0
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !strings.HasPrefix(sel.Sel.Name, "Skip") {
			return true
		}
		if recv, ok := sel.X.(*ast.Ident); ok && recv.Name == "t" {
			n++
		}
		return true
	})
	return n
}

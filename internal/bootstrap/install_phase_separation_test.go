// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// TestInstallPath_FetchIsNeverInterleavedWithCommit asserts the STRUCTURAL
// property the staged install exists to hold: no function in this package both
// fetches a release asset and commits one into place.
//
// That interleaving is the defect's shape. A function that downloads and then
// renames, called once per binary, commits the first binary before it has even
// tried to fetch the second — so any failure on the second leaves a new server
// beside an old client. Separating fetch from commit at the FUNCTION boundary is
// what makes the three-phase ordering structural rather than a convention the
// next edit can quietly undo.
//
// It is a source-shape assertion rather than a behavioral one because the
// behaviour it protects only manifests on a failure the behavioral tests
// already cover; what this adds is that the SHAPE cannot come back.
func TestInstallPath_FetchIsNeverInterleavedWithCommit(t *testing.T) {
	const (
		fetchCall  = "downloadAsset"
		commitCall = "os.Rename"
	)

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return strings.HasSuffix(fi.Name(), ".go") && !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package source: %v", err)
	}

	var examined, sawFetch, sawCommit int
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				examined++
				fetches := functionCalls(fn.Body, fetchCall)
				commits := functionCalls(fn.Body, commitCall)
				if fetches {
					sawFetch++
				}
				if commits {
					sawCommit++
				}
				if fetches && commits {
					t.Errorf("%s: %s both calls %s and calls %s — a function that fetches an asset and then commits one into place commits the first binary before it has tried to fetch the second, which is exactly the shape that leaves a multi-binary install half-swapped",
						path, fn.Name.Name, fetchCall, commitCall)
				}
			}
		}
	}

	// KNOWN-POSITIVE CONTROLS. Without them a parse that matched nothing — a
	// renamed helper, a moved file, a walk rooted at the wrong directory —
	// would report the same clean green as a correctly separated package.
	if examined == 0 {
		t.Fatalf("this census examined zero function declarations, so it asserted nothing; the parse or the directory is wrong")
	}
	if sawFetch == 0 {
		t.Fatalf("no function in this package calls %s, so the fetch half of this assertion matched nothing and the green above is vacuous", fetchCall)
	}
	if sawCommit == 0 {
		t.Fatalf("no function in this package calls %s, so the commit half of this assertion matched nothing and the green above is vacuous", commitCall)
	}
	t.Logf("phase-separation census: %d functions examined, %d fetch, %d commit, 0 both", examined, sawFetch, sawCommit)
}

// functionCalls reports whether body contains a call whose callee renders as
// name — either a bare identifier or a selector like "os.Rename".
func functionCalls(body *ast.BlockStmt, name string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			if fn.Name == name {
				found = true
			}
		case *ast.SelectorExpr:
			if x, ok := fn.X.(*ast.Ident); ok && x.Name+"."+fn.Sel.Name == name {
				found = true
			}
		}
		return true
	})
	return found
}

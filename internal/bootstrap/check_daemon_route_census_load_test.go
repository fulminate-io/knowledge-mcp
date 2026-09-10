// SPDX-License-Identifier: Apache-2.0

package bootstrap

// check_daemon_route_census_load_test.go — THE TYPE-CHECKING CONTEXT THE
// REFUSAL CENSUS RUNS IN.
//
// The census asks go/types what fills each error result of the routing file, so
// it needs the routing file type-checked IN ITS PACKAGE: the other files declare
// half the identifiers it mentions. This file loads that package once through
// go/packages, keeps its other files parsed and its direct imports resolved, and
// re-type-checks on demand with a caller-supplied body standing in for the
// routing file. That substitution is what lets the shape table point the census
// at a mutated copy without writing one to disk, and the one-time load is what
// keeps twenty-odd mutations from costing twenty-odd go command invocations.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

// censusPackage is the type-checking context the enumeration runs in: the
// package's other files parsed once, and its direct imports already resolved.
//
// IT IS BUILT ONCE AND REUSED. Loading the package through go/packages shells
// out to the go command and reads export data for every import, which is the
// expensive half; type-checking the parsed files again is the cheap half. The
// shape table runs the census over twenty-odd mutated copies of one file, so
// paying the expensive half once and re-checking per mutation is the difference
// between a test that runs and a test nobody will keep.
type censusPackage struct {
	fset     *token.FileSet
	others   []*ast.File
	routeAbs string
	routeSrc []byte
	importer types.Importer
	path     string
	sizes    types.Sizes
}

// censusImporter answers go/types from the *types.Package values go/packages
// already resolved from export data. A path it does not hold is an error rather
// than a silently empty package, because an empty package would make every
// selector through it untyped and the enumeration would go quiet.
type censusImporter map[string]*types.Package

func (c censusImporter) Import(path string) (*types.Package, error) {
	if pkg, ok := c[path]; ok && pkg != nil {
		return pkg, nil
	}
	return nil, fmt.Errorf("the census loaded no types for %q — the package under test does not import it, "+
		"so a source change that adds the import needs the census's package load to see it first", path)
}

// loadCensusPackage is the once-per-process load.
var loadCensusPackage = sync.OnceValues(func() (*censusPackage, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes | packages.NeedTypesSizes,
		Dir: ".",
		// Pin the external driver off and build Env by appending to the process
		// environment, for the reason topology/dead_code_rta.go documents: a
		// gopackagesdriver merely present on PATH would change what this loads
		// and make the census depend on the machine rather than the repository.
		Env: append(os.Environ(), "GOPACKAGESDRIVER=off"),
	}
	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		return nil, fmt.Errorf("load this package for the refusal census: %w", err)
	}
	if len(pkgs) != 1 {
		return nil, fmt.Errorf("loading %q returned %d packages, and the census governs exactly one", ".", len(pkgs))
	}
	pkg := pkgs[0]
	if len(pkg.Errors) > 0 {
		return nil, fmt.Errorf("this package does not load cleanly, so the census cannot type-check it: %v", pkg.Errors)
	}

	imports := censusImporter{}
	for path, dep := range pkg.Imports {
		imports[path] = dep.Types
	}

	cp := &censusPackage{
		fset:     token.NewFileSet(),
		importer: imports,
		path:     pkg.PkgPath,
		sizes:    pkg.TypesSizes,
	}
	for _, path := range pkg.CompiledGoFiles {
		if filepath.Base(path) == routeSourceFile {
			cp.routeAbs = path
			src, readErr := censusReadSource(path)
			if readErr != nil {
				return nil, readErr
			}
			cp.routeSrc = src
			continue
		}
		file, parseErr := parser.ParseFile(cp.fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return nil, fmt.Errorf("parse %s: %w", path, parseErr)
		}
		cp.others = append(cp.others, file)
	}
	if cp.routeAbs == "" {
		return nil, fmt.Errorf("%s is not one of this package's compiled files, so the census has nothing to govern", routeSourceFile)
	}
	return cp, nil
})

// check type-checks the package with src standing in for routeSourceFile, and
// returns the routing file's syntax and the type information for it.
func (cp *censusPackage) check(src []byte) (*ast.File, *types.Info, *types.Package, error) {
	if src == nil {
		src = cp.routeSrc
	}
	route, err := parser.ParseFile(cp.fset, cp.routeAbs, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse %s: %w", routeSourceFile, err)
	}
	files := make([]*ast.File, 0, len(cp.others)+1)
	files = append(files, cp.others...)
	files = append(files, route)

	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	var failures []string
	conf := types.Config{
		Importer: cp.importer,
		Sizes:    cp.sizes,
		Error:    func(err error) { failures = append(failures, err.Error()) },
	}
	pkg, _ := conf.Check(cp.path, cp.fset, files, info)
	if len(failures) > 0 {
		return nil, nil, nil, fmt.Errorf("type-check %s with this %s: %s", cp.path, routeSourceFile, strings.Join(failures, "; "))
	}
	return route, info, pkg, nil
}

// TestRefusalCensusRefusesAnImportItsPackageLoadDoesNotHold observes the
// importer's fail-loud arm, which nothing else reaches.
//
// WHY IT NEEDS ITS OWN TEST rather than a row in the shape table. The table
// asserts that every mutated copy still type-checks, because a red there must be
// the census and not a broken splice. This mutation is the one whose whole point
// is that the type-check REFUSES: a source change adding an import the package
// load does not hold gets an error, not an empty package. An empty package would
// make every selector through it untyped and the enumeration would go quiet on
// whatever that import touched, which is the silence this census exists to
// forbid. Killing the arm — answering types.NewPackage instead of the error —
// left the whole suite green until this test existed.
func TestRefusalCensusRefusesAnImportItsPackageLoadDoesNotHold(t *testing.T) {
	source, err := censusReadSource(routeSourceFile)
	require.NoError(t, err)
	tests := packageTestFuncs(t)

	// CONTROL, same run and same function: the file as it stands type-checks and
	// enumerates, so the error below is the added import and not a census that
	// cannot load its package at all.
	_, arms, err := auditRefusalCensus(nil, refusalRows, tests)
	require.NoError(t, err)
	require.NotEmpty(t, arms, "control: the unmutated file must enumerate")

	// net/url is imported by this package's TEST files and by no other file of
	// it, so the package load — which reads the non-test package — does not hold
	// it, while the import path is an ordinary one an author might add.
	mutated := spliceBefore(t, string(source), "\t\"strings\"", "\t\"net/url\"\n") + `
func probeURLRefusal(raw string) error {
	_, err := url.Parse(raw)
	return err
}
`
	_, _, err = auditRefusalCensus([]byte(mutated), refusalRows, tests)
	require.Error(t, err, "an import the census's package load does not hold must be refused, not answered with an empty package")
	assert.Contains(t, err.Error(), `the census loaded no types for "net/url"`)
}

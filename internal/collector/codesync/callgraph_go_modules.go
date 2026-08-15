// SPDX-License-Identifier: Apache-2.0

package codesync

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// GoCallGraph is everything the RTA merge needs from the precise analysis: what
// it found, what it actually LOOKED AT, and how much of it failed to build.
//
// CoveredFiles is the field that makes the merge's drop rule honest. Without it
// the merge can only assume the analysis owns every Go caller in the repo, and
// deletes the tree-sitter CALLS edges of files it never loaded — build-tag
// excluded files, Go files under testdata directories the go tool ignores by
// design, and any module that fails to type-check — with nothing to replace
// them.
type GoCallGraph struct {
	// CallMap maps a caller decl key to its callee decl keys.
	CallMap map[string][]string
	// CoveredFiles holds every repo-relative Go file the analysis loaded.
	CoveredFiles map[string]bool
	// PackageErrs is len(pkg.Errors) summed across every visited package.
	PackageErrs int
}

// BuildGoCallGraph loads every Go module named in moduleDirs, builds SSA, runs
// VTA, and returns the merged call map plus the set of files the analysis
// actually covered.
//
// A decl key is "<repo-relative declaring file>:<symbol>" — "<file>:FuncName"
// for a function and "<file>:ReceiverType.MethodName" for a method. It is
// derived from the SSA declaring position, so it is byte-identical to the node
// ID the collector's chunker builds for the same declaration, and the merge's
// lookup is an identity rather than a guess. moduleDirs entries are
// repo-relative and slash-separated; the relativizer is built from rootDir, so
// every key and every covered file is relative to the REPO, never to a module.
//
// WHY PER-MODULE RATHER THAN ONE LOAD. A single packages.Load(Dir=rootDir,
// "./...") reaches only the root module: a `./...` pattern does not cross a
// go.work workspace boundary, so on a repo whose root go.mod is a thin
// generated-code module the analysis saw a handful of packages and missed every
// real one. A multi-pattern single Load cannot substitute either — with a nested
// module and no go.work, packages.Load(Dir=root, "./...", "./sub/...") returns a
// synthetic error package reading `pattern ./sub/...: directory prefix sub does
// not contain main module or its selected dependencies`.
//
// THE LOOP IS SERIAL, DELIBERATELY. packages.Load already parallelizes list and
// type-checking internally, and each module's ssa.Program is large, so running
// modules concurrently multiplies peak RSS to save wall clock on a background
// collect. Do not add an errgroup here.
//
// ctx is checked at coarse boundaries — before the loop and once per module —
// because packages.Load and ssa.Build do not honor context internally, so a hung
// load cannot be aborted mid-call.
func BuildGoCallGraph(ctx context.Context, rootDir string, moduleDirs []string) (GoCallGraph, error) {
	out := GoCallGraph{
		CallMap:      make(map[string][]string),
		CoveredFiles: make(map[string]bool),
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	// No indexed Go files means no module was discovered: nothing is analyzed,
	// and the coverage-gated drop therefore drops nothing.
	if len(moduleDirs) == 0 {
		return out, nil
	}

	rel := newRepoRelativizer(rootDir)
	// A package that is a dependency of two modules is analyzed under both, so
	// the merge dedupes caller→callee pairs rather than appending duplicates.
	seen := make(map[string]map[string]bool)

	for _, md := range moduleDirs {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if err := loadModuleCallEdges(ctx, rootDir, md, rel, &out, seen); err != nil {
			return out, err
		}
	}

	if out.PackageErrs > 0 {
		slog.Warn("BuildGoCallGraph: package errors", "count", out.PackageErrs)
	}
	slog.Info("BuildGoCallGraph",
		"modules", len(moduleDirs),
		"callers", len(out.CallMap),
		"covered_files", len(out.CoveredFiles),
		"package_errors", out.PackageErrs,
	)
	return out, nil
}

// loadModuleCallEdges analyzes ONE module and merges its results into out.
//
// Tests: true is not an optional extra. packages.Config.Tests defaults false, so
// no _test.go file was ever analyzed, which left the entire test-code call graph
// permanently unanalyzed — and therefore, under the coverage-gated drop,
// permanently tree-sitter-only.
//
// packages.Visit walks the roots AND their dependencies rather than the roots
// alone, because an in-repo dependency package is genuinely analyzed and
// genuinely covered: loading one module covers the in-repo packages it imports.
func loadModuleCallEdges(
	ctx context.Context,
	rootDir, moduleDir string,
	rel repoRelativizer,
	out *GoCallGraph,
	seen map[string]map[string]bool,
) error {
	cfg := &packages.Config{
		Context: ctx,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedDeps |
			packages.NeedImports,
		Dir:   filepath.Join(rootDir, filepath.FromSlash(moduleDir)),
		Tests: true,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		// The module is named so a failure says WHICH module failed rather than
		// leaving an operator to guess across a workspace.
		return fmt.Errorf("load packages in module %s: %w", moduleDir, err)
	}

	packages.Visit(pkgs, nil, func(p *packages.Package) {
		out.PackageErrs += len(p.Errors)
		for _, f := range p.GoFiles {
			if r, ok := rel.rel(f); ok {
				out.CoveredFiles[r] = true
			}
		}
		for _, f := range p.CompiledGoFiles {
			if r, ok := rel.rel(f); ok {
				out.CoveredFiles[r] = true
			}
		}
	})

	prog, _ := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	prog.Build()

	funcSet := repoDeclaredFunctions(prog, rel)
	if len(funcSet) == 0 {
		return nil
	}

	for caller, callees := range extractCallEdges(vta.CallGraph(funcSet, nil), rel) {
		if _, ok := seen[caller]; !ok {
			seen[caller] = make(map[string]bool)
		}
		for _, callee := range callees {
			if seen[caller][callee] {
				continue
			}
			seen[caller][callee] = true
			out.CallMap[caller] = append(out.CallMap[caller], callee)
		}
	}
	return nil
}

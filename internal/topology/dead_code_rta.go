// SPDX-License-Identifier: Apache-2.0

// Package topology / dead_code_rta.go — RTA pipeline that loads a Go
// module from disk, builds SSA, computes reachability via the rta package,
// and returns the unreachable source-level functions.
//
// Relocated client-side from pkg/topology/. The server
// is filesystem-blind; the RTA pipeline (packages.Load, SSA build,
// callgraph) requires reading every .go file under the repo root, so
// it now runs entirely inside the cmd/knowledge client binary.
//
// Mirrors the canonical cmd/deadcode pipeline (golang.org/x/tools v0.43.0
// cmd/deadcode/deadcode.go lines 117-313):
//
//  1. packages.Load with LoadAllSyntax | NeedModule, Tests=tests, Dir=repoRoot
//  2. ssautil.AllPackages -> prog.Build
//  3. ssautil.MainPackages -> roots = main.init + main.main per main package
//  4. rta.Analyze(roots, false) -> result.Reachable
//  5. Walk every initial package's source files, collect FuncDecls,
//     map to *ssa.Function via TypesInfo.Defs + prog.FuncValue, and
//     emit any function whose source position is not in reachablePosn.
//
// Position-based de-duplication matches cmd/deadcode: with Tests=true the
// same source declaration may exist as multiple *ssa.Function instances
// (p, p [p.test], p.test) and any one being reachable means all are.
package topology

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"

	"golang.org/x/tools/go/callgraph/rta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// deadFunc captures one unreachable source-level function plus the
// position metadata needed by the mapping pass. Func is kept so the
// reflection-detection pass can inspect the function's AST body and
// discriminate "address-taken via reflect" from "definitely dead."
type deadFunc struct {
	Func *ssa.Function
	Pkg  *packages.Package
	Pos  token.Position
}

// runRTA loads repoRoot's Go module, builds the SSA call graph, runs RTA
// reachability, and returns every source-level FuncDecl that is not in
// the reachable set. When the load step fails or the package set has
// errors, runRTA returns (nil, nil, "<diagnostic>", nil) — the analyzer
// treats a non-empty diagnostic as "skip cleanly with an info log" rather
// than a hard error so dream cycles don't spam logs across non-Go repos.
//
// The bool argument controls whether test packages participate in the
// analysis. The canonical default is true.
//
// THIS ANALYZER IS THE ONLY THING IN THE REPOSITORY THAT NEEDS A GO TOOLCHAIN
// ON THE MACHINE. packages.Load shells out to the go command, so a host without
// one cannot run dead-code analysis — and that is the whole of the exposure.
// Nothing on the collect path reads a toolchain: code collection is a pure
// function of repo bytes, identical with or without a Go installation. Keep it
// that way; a toolchain dependency introduced anywhere else would make a
// collected graph depend on which machine collected it.
func runRTA(ctx context.Context, repoRoot string, tests bool) ([]deadFunc, *ssa.Program, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, "", fmt.Errorf("topology/dead_code: %w", err)
	}

	cfg := &packages.Config{
		Mode:    packages.LoadAllSyntax | packages.NeedModule,
		Tests:   tests,
		Dir:     repoRoot,
		Context: ctx,
		// go/packages defaults a nil Env to the process environment and will
		// otherwise honor an external driver — including one found merely by
		// looking up gopackagesdriver on PATH — which would silently change
		// what this analyzer loads and make its answer depend on the machine
		// rather than on the repository. Pin the driver off, and build Env by
		// appending to os.Environ() rather than replacing it, so the rest of
		// the environment the loader needs is preserved.
		Env: append(os.Environ(), "GOPACKAGESDRIVER=off"),
	}
	initial, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, nil, fmt.Sprintf("packages.Load failed: %v", err), nil
	}
	if len(initial) == 0 {
		return nil, nil, "no packages found", nil
	}
	if packages.PrintErrors(initial) > 0 {
		return nil, nil, "package load failed (broken build)", nil
	}

	if err := ctx.Err(); err != nil {
		return nil, nil, "", fmt.Errorf("topology/dead_code: %w", err)
	}

	prog, pkgs := ssautil.AllPackages(initial, ssa.InstantiateGenerics)
	prog.Build()

	roots := collectRoots(prog, pkgs)
	if len(roots) == 0 {
		return nil, nil, "no entry points (no main / test packages)", nil
	}

	if err := ctx.Err(); err != nil {
		return nil, nil, "", fmt.Errorf("topology/dead_code: %w", err)
	}

	res := rta.Analyze(roots, false)
	if res == nil {
		return nil, nil, "rta analysis returned nil", nil
	}

	reachablePosn := buildReachablePosn(prog, res)
	moduleFilter := buildModuleFilter(initial)
	deadFuncs := collectDeadFuncs(initial, prog, reachablePosn, moduleFilter)
	return deadFuncs, prog, "", nil
}

// buildModuleFilter returns a predicate that accepts only packages whose
// module path matches one of the modules in initial.
func buildModuleFilter(initial []*packages.Package) func(*packages.Package) bool {
	mods := make(map[string]bool)
	for _, p := range initial {
		if p.Module != nil && p.Module.Path != "" {
			mods[p.Module.Path] = true
		}
	}
	if len(mods) == 0 {
		return func(_ *packages.Package) bool { return true }
	}
	return func(p *packages.Package) bool {
		if p == nil || p.Module == nil {
			return false
		}
		return mods[p.Module.Path]
	}
}

// collectRoots returns the RTA entry points: every main package's
// init+main pair. Test packages produced by Tests=true synthesize their
// own *.test main package whose init+main are picked up by
// ssautil.MainPackages.
func collectRoots(prog *ssa.Program, pkgs []*ssa.Package) []*ssa.Function {
	mains := ssautil.MainPackages(prog.AllPackages())
	if len(mains) == 0 {
		mains = ssautil.MainPackages(pkgs)
	}
	roots := make([]*ssa.Function, 0, 2*len(mains))
	for _, m := range mains {
		if init := m.Func("init"); init != nil {
			roots = append(roots, init)
		}
		if main := m.Func("main"); main != nil {
			roots = append(roots, main)
		}
	}
	return roots
}

// buildReachablePosn keys liveness by file position so the same source
// declaration's multiple *ssa.Function variants (p vs p.test) don't
// surface as separate dead entries.
func buildReachablePosn(prog *ssa.Program, res *rta.Result) map[token.Position]bool {
	out := make(map[token.Position]bool, len(res.Reachable))
	for fn := range res.Reachable {
		if fn.Pos().IsValid() || fn.Name() == "init" {
			out[prog.Fset.Position(fn.Pos())] = true
		}
	}
	return out
}

// collectDeadFuncs walks every initial package's syntax trees, finds
// every top-level FuncDecl, looks up its *ssa.Function via TypesInfo.Defs
// and prog.FuncValue, and emits a deadFunc when the function's source
// position is not in reachablePosn.
func collectDeadFuncs(initial []*packages.Package, prog *ssa.Program, reachablePosn map[token.Position]bool, accept func(*packages.Package) bool) []deadFunc {
	var dead []deadFunc
	seen := make(map[token.Position]bool)
	packages.Visit(initial, nil, func(p *packages.Package) {
		if p.TypesInfo == nil {
			return
		}
		if accept != nil && !accept(p) {
			return
		}
		for _, file := range p.Syntax {
			collectDeadFromFile(p, file, prog, reachablePosn, seen, &dead)
		}
	})
	return dead
}

// collectDeadFromFile processes a single ast.File: walks its declarations,
// resolves each FuncDecl to its *ssa.Function, and appends to dead any
// function whose source position is not in reachablePosn.
func collectDeadFromFile(
	p *packages.Package,
	file *ast.File,
	prog *ssa.Program,
	reachablePosn map[token.Position]bool,
	seen map[token.Position]bool,
	dead *[]deadFunc,
) {
	for _, decl := range file.Decls {
		fnDecl, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		obj, ok := p.TypesInfo.Defs[fnDecl.Name].(*types.Func)
		if !ok || obj == nil {
			continue
		}
		fn := prog.FuncValue(obj)
		if fn == nil {
			continue
		}
		posn := prog.Fset.Position(fn.Pos())
		if !posn.IsValid() {
			continue
		}
		if reachablePosn[posn] {
			continue
		}
		if seen[posn] {
			continue
		}
		seen[posn] = true
		*dead = append(*dead, deadFunc{Func: fn, Pkg: p, Pos: posn})
	}
}

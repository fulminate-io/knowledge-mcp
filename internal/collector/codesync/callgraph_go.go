// SPDX-License-Identifier: Apache-2.0

package codesync

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// BuildGoCallGraph loads all Go packages under rootDir, builds SSA, runs VTA,
// and returns a map of callerQualifiedName → []calleeQualifiedName using the
// same naming convention as tree-sitter: "pkgName.FuncName" for functions and
// "pkgName.ReceiverType.MethodName" for methods.
//
// ctx is checked at coarse boundaries (before package load, before SSA build,
// before VTA, before edge extraction) so callers can cancel between phases.
// packages.Load and ssa.Build do not honor context internally, so a hung
// load cannot be aborted mid-call.
func BuildGoCallGraph(ctx context.Context, rootDir string) (map[string][]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	moduleName, err := readModuleName(rootDir)
	if err != nil {
		slog.Warn("BuildGoCallGraph: could not read module name", "error", err)
	}

	cfg := &packages.Config{
		Context: ctx,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedDeps |
			packages.NeedImports,
		Dir: rootDir,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, fmt.Errorf("load packages: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var errCount int
	for _, pkg := range pkgs {
		errCount += len(pkg.Errors)
	}
	if errCount > 0 {
		slog.Warn("BuildGoCallGraph: package errors", "count", errCount)
	}

	prog, ssaPkgs := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	prog.Build()

	funcSet := make(map[*ssa.Function]bool)
	for _, p := range ssaPkgs {
		if p == nil {
			continue
		}
		for _, m := range p.Members {
			if fn, ok := m.(*ssa.Function); ok {
				funcSet[fn] = true
			}
		}
	}

	if len(funcSet) == 0 {
		return nil, nil
	}

	cg := vta.CallGraph(funcSet, nil)

	result := extractCallEdges(cg, moduleName)
	slog.Info("BuildGoCallGraph", "callers", len(result), "funcs", len(funcSet))
	return result, nil
}

// extractCallEdges walks the VTA call graph and builds a caller→callees map
// using the tree-sitter naming convention.
func extractCallEdges(cg *callgraph.Graph, moduleName string) map[string][]string {
	result := make(map[string][]string)
	seen := make(map[string]map[string]bool)

	for fn, node := range cg.Nodes {
		if fn == nil || fn.Pkg == nil {
			continue
		}
		callerKey := formatCallGraphName(fn, moduleName)
		if callerKey == "" {
			continue
		}
		for _, edge := range node.Out {
			callee := edge.Callee.Func
			if callee == nil || callee.Pkg == nil {
				continue
			}
			calleeKey := formatCallGraphName(callee, moduleName)
			if calleeKey == "" {
				continue
			}
			if _, ok := seen[callerKey]; !ok {
				seen[callerKey] = make(map[string]bool)
			}
			if !seen[callerKey][calleeKey] {
				seen[callerKey][calleeKey] = true
				result[callerKey] = append(result[callerKey], calleeKey)
			}
		}
	}
	return result
}

// formatCallGraphName returns a qualified name matching the tree-sitter naming
// convention: "pkgName.FuncName" for functions and "pkgName.ReceiverType.MethodName"
// for methods. Returns "" for functions from external packages or synthetic functions.
func formatCallGraphName(fn *ssa.Function, moduleName string) string {
	if fn.Pkg == nil {
		return ""
	}
	pkgPath := fn.Pkg.Pkg.Path()
	pkgName := normalizeCallGraphName(pkgPath, moduleName)
	if pkgName == "" {
		return ""
	}

	if recv := fn.Signature.Recv(); recv != nil {
		typeName := recv.Type().String()
		if idx := strings.LastIndex(typeName, "."); idx >= 0 {
			typeName = typeName[idx+1:]
		}
		typeName = strings.TrimPrefix(typeName, "*")
		typeName = strings.TrimSuffix(typeName, "]")
		// Strip any generic type parameters.
		if idx := strings.Index(typeName, "["); idx >= 0 {
			typeName = typeName[:idx]
		}
		return pkgName + "." + typeName + "." + fn.Name()
	}
	return pkgName + "." + fn.Name()
}

// normalizeCallGraphName strips the module path prefix from a fully-qualified
// Go package path, returning just the last path component as used in tree-sitter
// symbol names. For example, given moduleName "github.com/fulminate-io/knowledge-mcp"
// and pkgPath "github.com/fulminate-io/knowledge-mcp/internal/kgtypes", it returns "kgtypes".
// For the root package it returns the last element of the module path.
// Returns "" for packages outside the module.
func normalizeCallGraphName(pkgPath, moduleName string) string {
	if moduleName == "" {
		if idx := strings.LastIndex(pkgPath, "/"); idx >= 0 {
			return pkgPath[idx+1:]
		}
		return pkgPath
	}
	if pkgPath == moduleName {
		if idx := strings.LastIndex(moduleName, "/"); idx >= 0 {
			return moduleName[idx+1:]
		}
		return moduleName
	}
	prefix := moduleName + "/"
	if !strings.HasPrefix(pkgPath, prefix) {
		return ""
	}
	rel := pkgPath[len(prefix):]
	if idx := strings.LastIndex(rel, "/"); idx >= 0 {
		return rel[idx+1:]
	}
	return rel
}

// readModuleName reads the module name from the go.mod file in rootDir.
func readModuleName(rootDir string) (string, error) {
	modPath := filepath.Join(rootDir, "go.mod")
	f, err := os.Open(modPath)
	if err != nil {
		return "", fmt.Errorf("open go.mod: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if mod, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(mod), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading go.mod: %w", err)
	}
	return "", fmt.Errorf("module directive not found in go.mod")
}

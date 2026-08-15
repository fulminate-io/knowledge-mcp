// SPDX-License-Identifier: Apache-2.0

// Package graph / dsm_matrix.go — package aggregation helpers for
// DSMAnalyzer (dsm.go). Contains the file-level -> package-level IMPORTS
// aggregation and the independent cycle check over the aggregated package
// DAG. Split from dsm.go to keep the analyzer's pipeline file under the
// soft line limit.
package graph

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"gonum.org/v1/gonum/graph/simple"
	"gonum.org/v1/gonum/graph/topo"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// buildPackageGraph fetches every NodeFile in the code graph (one wire
// browse) plus every IMPORTS edge incident to that file set (a bulk wire
// edge read), resolves owning packages via filepath.Dir, and aggregates the
// forward IMPORTS edges to the package level. Returns:
//
//   - pkgs: sorted slice of local package paths (no module prefix)
//   - deps: forward adjacency deps[fromPkg][toPkg] = import edge count
//   - reps: representative files reps[fromPkg][toPkg] = up to 5 file
//     paths carrying at least one import of toPkg (Finding evidence)
//
// IMPORTS edges that resolve to external packages (stdlib, third-party) are
// silently skipped. Self-imports and intra-package imports are also skipped.
func buildPackageGraph(ctx context.Context, req foundation.Request, modulePath string) (
	pkgs []string, deps map[string]map[string]int, reps map[string]map[string][]string, err error,
) {
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	files, err := foundation.FetchNodesByType(ctx, req.Caller, req.Graph, req.Name, kgtypes.NodeFile)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("query files: %w", err)
	}

	pkgSet := make(map[string]struct{})
	deps = make(map[string]map[string]int)
	reps = make(map[string]map[string][]string)

	// Build the file → package map and the file-ID set for the bulk edge
	// read.
	fileIDs := make([]string, 0, len(files))
	pkgOfFile := make(map[string]string, len(files))
	for _, f := range files {
		if f == nil {
			continue
		}
		fromPkg := packageOf(f.Id)
		if fromPkg == "" || fromPkg == "." {
			continue
		}
		pkgSet[fromPkg] = struct{}{}
		fileIDs = append(fileIDs, f.Id)
		pkgOfFile[f.Id] = fromPkg
	}

	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}

	// A bulk IMPORTS-edge read over the whole file set, then group each
	// forward edge into the per-package accumulators — the wire twin of the
	// prior per-file IterEdges walk.
	edges, err := foundation.FetchEdges(ctx, req.Caller, req.Graph, req.Name, fileIDs, []kgtypes.EdgeType{kgtypes.EdgeImports})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("query imports: %w", err)
	}
	for i := range edges {
		foldImportEdge(&edges[i], pkgOfFile, modulePath, pkgSet, deps, reps)
	}

	pkgs = make([]string, 0, len(pkgSet))
	for p := range pkgSet {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	return pkgs, deps, reps, nil
}

// foldImportEdge folds one forward IMPORTS edge (file → import literal) into
// the shared pkg/dep/rep accumulators. The edge's FromId is the source file;
// its ToId is the raw import path the chunker recorded.
func foldImportEdge(
	e *knowledgev1.Edge,
	pkgOfFile map[string]string,
	modulePath string,
	pkgSet map[string]struct{},
	deps map[string]map[string]int,
	reps map[string]map[string][]string,
) {
	fromPkg, ok := pkgOfFile[e.FromId]
	if !ok {
		return
	}
	toPkg, ok := stripModulePrefix(e.ToId, modulePath)
	if !ok {
		return
	}
	if toPkg == "" || toPkg == fromPkg {
		return
	}
	pkgSet[toPkg] = struct{}{}
	if deps[fromPkg] == nil {
		deps[fromPkg] = make(map[string]int)
		reps[fromPkg] = make(map[string][]string)
	}
	deps[fromPkg][toPkg]++
	if len(reps[fromPkg][toPkg]) < 5 && !dsmContainsString(reps[fromPkg][toPkg], e.FromId) {
		reps[fromPkg][toPkg] = append(reps[fromPkg][toPkg], e.FromId)
	}
}

// packageOf returns the owning package (directory) for a file node ID.
// Mirrors the hierarchy builder's filepath.Dir contract.
func packageOf(fileID string) string {
	dir := filepath.Dir(fileID)
	if dir == "" {
		return "."
	}
	return dir
}

// stripModulePrefix turns a raw import literal into a local package path.
// Returns ("", false) for external imports (stdlib, third-party) or anything
// outside the module. The module path itself becomes ".".
func stripModulePrefix(importPath, modulePath string) (string, bool) {
	if modulePath == "" {
		return "", false
	}
	if importPath == modulePath {
		return ".", true
	}
	prefix := modulePath + "/"
	if !strings.HasPrefix(importPath, prefix) {
		return "", false
	}
	return strings.TrimPrefix(importPath, prefix), true
}

// dsmContainsString is a tiny helper kept local so we don't collide with
// helpers elsewhere in the package.
func dsmContainsString(haystack []string, needle string) bool {
	return slices.Contains(haystack, needle)
}

// invertDeps converts a forward dependency map into a reverse adjacency map
// where importedBy[P] = sorted slice of packages Q such that Q imports P.
func invertDeps(deps map[string]map[string]int) map[string][]string {
	importedBy := make(map[string][]string, len(deps))
	for fromPkg, targets := range deps {
		for toPkg := range targets {
			importedBy[toPkg] = append(importedBy[toPkg], fromPkg)
		}
	}
	for k := range importedBy {
		sort.Strings(importedBy[k])
	}
	return importedBy
}

// detectPackageCycles runs Tarjan's SCC over the aggregated package DAG
// independently of the LayerProvider. Per OQ-7 the user's config file may be
// wrong, so we always re-check cycles against the ground truth in the
// package matrix.
func detectPackageCycles(pkgs []string, deps map[string]map[string]int) []dsmCycle {
	g := simple.NewDirectedGraph()
	idOf := make(map[string]int64, len(pkgs))
	nameOf := make(map[int64]string, len(pkgs))
	for _, p := range pkgs {
		n := g.NewNode()
		g.AddNode(n)
		idOf[p] = n.ID()
		nameOf[n.ID()] = p
	}
	for fromPkg, targets := range deps {
		fromID, ok := idOf[fromPkg]
		if !ok {
			continue
		}
		for toPkg := range targets {
			toID, tok := idOf[toPkg]
			if !tok || fromID == toID {
				continue
			}
			g.SetEdge(g.NewEdge(simple.Node(fromID), simple.Node(toID)))
		}
	}
	sccs := topo.TarjanSCC(g)
	var out []dsmCycle
	for _, scc := range sccs {
		if len(scc) < 2 {
			continue
		}
		members := make([]string, 0, len(scc))
		for _, n := range scc {
			if name, ok := nameOf[n.ID()]; ok {
				members = append(members, name)
			}
		}
		sort.Strings(members)
		out = append(out, dsmCycle{Members: members, LayerIndex: -1})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Members[0] < out[j].Members[0]
	})
	return out
}

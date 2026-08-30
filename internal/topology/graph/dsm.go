// SPDX-License-Identifier: Apache-2.0

// Package graph / dsm.go — DSMAnalyzer builds a package-level dependency
// structure matrix from IMPORTS edges in a code graph, compares it against
// an expected Layering supplied by a LayerProvider chain, and emits Findings
// for every upward import violation plus any cycles detected independently
// of the layer source. Go-only v1.
//
// Pipeline: code-only guard -> module path read (from the local
// <RepoRoot>/go.mod) -> package aggregation (over the wire) -> layer provider
// chain (ConfigFile -> PathHeuristic) -> violation + cycle detection ->
// Finding emission. See dsm_matrix.go and dsm_layers.go for helpers.
//
// Filesystem reads: this analyzer runs on the client where the working repo
// is local, so it reads the module path from <RepoRoot>/go.mod and the
// optional layer config from <RepoRoot>/.knowledge/topology_layers.yaml
// directly off disk. These are backend-neutral filesystem reads — there is
// no store, no graph metadata, and no server reference on this path.
package graph

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

type dsmViolation struct {
	FromPkg   string
	ToPkg     string
	FromLayer int
	ToLayer   int
	Files     []string
}

type dsmCycle struct {
	Members    []string
	LayerIndex int
}

// DSMAnalyzer detects layering violations in a code graph.
type DSMAnalyzer struct{}

// Name returns the analyzer's stable identifier.
func (DSMAnalyzer) Name() string { return "dsm" }

// Run executes the DSM pipeline.
func (DSMAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/dsm: %w", err)
	}
	if req.Graph != kgtypes.GraphCode {
		slog.Info("topology/dsm: skipping non-code graph",
			"graph_type", req.Graph, "name", req.Name)
		return nil, nil
	}
	if req.Caller == nil {
		return nil, fmt.Errorf("topology/dsm: req.Caller must not be nil")
	}

	modulePath, err := readModulePath(req.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("topology/dsm: read module path: %w", err)
	}
	if modulePath == "" {
		// No go.mod under RepoRoot, which now means exactly one thing: the
		// dispatcher resolves the walk root from the repo argument, so the root is
		// never unset here and the branch narrows to a genuinely non-Go tree. DSM
		// only handles Go today, and that inability is STATED rather than returned
		// as an empty result, which would render as "this repo has no layering
		// violations". Nothing is retried or computed another way.
		return []foundation.Finding{dsmNoGoModuleFinding(req.RepoRoot)}, nil
	}

	pkgs, deps, reps, err := buildPackageGraph(ctx, req, modulePath)
	if err != nil {
		return nil, fmt.Errorf("topology/dsm: build package graph: %w", err)
	}
	if len(pkgs) == 0 {
		slog.Info("topology/dsm: no packages found",
			"name", req.Name, "module_path", modulePath)
		return nil, nil
	}

	importedBy := invertDeps(deps)
	layering, err := resolveLayering(ctx, req.RepoRoot, pkgs, importedBy)
	if err != nil {
		return nil, fmt.Errorf("topology/dsm: resolve layering: %w", err)
	}
	if layering == nil {
		slog.Info("topology/dsm: no provider yielded a layering",
			"name", req.Name, "pkg_count", len(pkgs))
		return nil, nil
	}

	violations, cycles := detectViolations(pkgs, deps, reps, layering)
	extraCycles := detectPackageCycles(pkgs, deps)
	cycles = mergeCycles(cycles, extraCycles)

	findings := buildDSMFindings(violations, cycles, layering.Source)
	return foundation.TruncateTopK(findings, req.TopK), nil
}

// DSMNoGoModuleTitle titles the one informational finding dsm emits for a tree
// that carries no go.mod. It names the analyzer and the reason class, following
// the disclosure shape corpus_scan established; the resolved root rides in the
// summary as payload.
const DSMNoGoModuleTitle = "dsm: not a Go module"

// dsmNoGoModuleFinding states the absent-module fact AND the root it was looked
// for under. The root is not decoration: without it a reader cannot tell a
// genuinely non-Go tree from a mis-resolved one, which is the ambiguity the
// resolved walk root exists to remove.
func dsmNoGoModuleFinding(repoRoot string) foundation.Finding {
	return foundation.Finding{
		Algorithm: "dsm",
		Severity:  foundation.SeverityNotice,
		Title:     DSMNoGoModuleTitle,
		Summary: fmt.Sprintf("no go.mod with a module directive under %s, and this analyzer models Go packages only — "+
			"so it reports nothing about this tree's layering rather than reporting none", repoRoot),
		Evidence: []string{repoRoot},
	}
}

// readModulePath opens <rootDir>/go.mod and returns the `module ...`
// directive. Re-declared here (copied verbatim from the client codesync
// collector's readModulePath) because dsm must not import the collector
// package — the same precedent as dead_code re-declaring its own helpers.
// Returns ("", nil) on a missing file / non-Go repo so the caller skips
// gracefully rather than erroring.
func readModulePath(rootDir string) (string, error) {
	if rootDir == "" {
		return "", nil
	}
	f, err := os.Open(filepath.Join(rootDir, "go.mod"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "module ") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "module"))
		rest = strings.TrimPrefix(rest, `"`)
		rest = strings.TrimSuffix(rest, `"`)
		if idx := strings.Index(rest, "//"); idx >= 0 {
			rest = strings.TrimSpace(rest[:idx])
		}
		if rest == "" {
			return "", nil
		}
		return rest, nil
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", nil
}

// resolveLayering runs the CompositeProvider chain. Layer config (when
// present) is read from <RepoRoot>/.knowledge/topology_layers.yaml off disk
// by ConfigFileProvider.
func resolveLayering(ctx context.Context, repoRoot string, pkgs []string, importedBy map[string][]string) (*Layering, error) {
	providers := []LayerProvider{
		ConfigFileProvider{RepoRoot: repoRoot},
		PathHeuristicProvider{},
	}
	return CompositeProvider{Providers: providers}.Layers(ctx, pkgs, importedBy)
}

// detectViolations walks the package graph and flags every upward import.
func detectViolations(pkgs []string, deps map[string]map[string]int, reps map[string]map[string][]string, layering *Layering) ([]dsmViolation, []dsmCycle) {
	pkgLayer := assignLayers(pkgs, layering)
	var violations []dsmViolation
	for fromPkg, targets := range deps {
		fromL := pkgLayer[fromPkg]
		for toPkg := range targets {
			toL := pkgLayer[toPkg]
			if toL <= fromL {
				continue
			}
			var files []string
			if reps[fromPkg] != nil {
				files = append([]string(nil), reps[fromPkg][toPkg]...)
			}
			violations = append(violations, dsmViolation{
				FromPkg: fromPkg, ToPkg: toPkg,
				FromLayer: fromL, ToLayer: toL,
				Files: files,
			})
		}
	}
	sort.SliceStable(violations, func(i, j int) bool {
		if violations[i].FromPkg != violations[j].FromPkg {
			return violations[i].FromPkg < violations[j].FromPkg
		}
		return violations[i].ToPkg < violations[j].ToPkg
	})

	var cycles []dsmCycle
	for _, l := range layering.Layers {
		if len(l.Packages) > 1 && strings.HasPrefix(l.Name, "scc-") {
			members := append([]string(nil), l.Packages...)
			sort.Strings(members)
			cycles = append(cycles, dsmCycle{Members: members, LayerIndex: l.Index})
		}
	}
	sort.SliceStable(cycles, func(i, j int) bool {
		return cycles[i].Members[0] < cycles[j].Members[0]
	})
	return violations, cycles
}

// assignLayers builds the pkg->layer index, with an "unclassified" tier above the highest declared layer.
func assignLayers(pkgs []string, layering *Layering) map[string]int {
	pkgLayer := make(map[string]int, len(pkgs))
	highest := -1
	for _, l := range layering.Layers {
		if l.Index > highest {
			highest = l.Index
		}
		for _, p := range l.Packages {
			pkgLayer[p] = l.Index
		}
	}
	unclassified := highest + 1
	for _, p := range pkgs {
		if _, ok := pkgLayer[p]; !ok {
			pkgLayer[p] = unclassified
		}
	}
	return pkgLayer
}

// mergeCycles deduplicates cycle lists by canonicalized member sets.
func mergeCycles(a, b []dsmCycle) []dsmCycle {
	seen := make(map[string]bool, len(a)+len(b))
	var out []dsmCycle
	for _, list := range [][]dsmCycle{a, b} {
		for _, c := range list {
			key := strings.Join(c.Members, "\x00")
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, c)
		}
	}
	return out
}

// buildDSMFindings converts internal records into foundation.Finding values.
func buildDSMFindings(violations []dsmViolation, cycles []dsmCycle, source string) []foundation.Finding {
	severity := foundation.SeverityNotice
	if strings.Contains(source, "config-file") {
		severity = foundation.SeverityWarning
	}
	findings := make([]foundation.Finding, 0, len(violations)+len(cycles))
	for _, v := range violations {
		findings = append(findings, buildViolationFinding(v, severity, source))
	}
	for _, c := range cycles {
		findings = append(findings, buildDSMCycleFinding(c, source))
	}
	return findings
}

func buildViolationFinding(v dsmViolation, severity foundation.Severity, source string) foundation.Finding {
	title := fmt.Sprintf("DSM violation: %s imports %s (layer %d -> %d)",
		v.FromPkg, v.ToPkg, v.FromLayer, v.ToLayer)
	summary := fmt.Sprintf(
		"Package %q (layer %d) imports %q (layer %d), violating the declared layering (source: %s). Representative files: %s",
		v.FromPkg, v.FromLayer, v.ToPkg, v.ToLayer, source, joinOrNone(v.Files),
	)
	evidence := make([]string, 0, len(v.Files)+2)
	evidence = append(evidence, v.FromPkg, v.ToPkg)
	evidence = append(evidence, v.Files...)
	return foundation.Finding{
		Algorithm: "dsm",
		Severity:  severity,
		Title:     title,
		Summary:   summary,
		Evidence:  evidence,
		Metrics: map[string]float64{
			"from_layer": float64(v.FromLayer),
			"to_layer":   float64(v.ToLayer),
			"delta":      float64(v.ToLayer - v.FromLayer),
		},
	}
}

func buildDSMCycleFinding(c dsmCycle, source string) foundation.Finding {
	title := fmt.Sprintf("DSM cycle: %d-package dependency loop", len(c.Members))
	summary := fmt.Sprintf(
		"Package cycle detected (source: %s): %s. Cycles prevent a clean layering and must be broken before DSM analysis can enforce invariants.",
		source, strings.Join(c.Members, " -> "),
	)
	return foundation.Finding{
		Algorithm: "dsm",
		Severity:  foundation.SeverityCritical,
		Title:     title,
		Summary:   summary,
		Evidence:  append([]string(nil), c.Members...),
		Metrics: map[string]float64{
			"size": float64(len(c.Members)),
		},
	}
}

func joinOrNone(files []string) string {
	if len(files) == 0 {
		return "(none)"
	}
	return strings.Join(files, ", ")
}

func init() {
	foundation.Register(DSMAnalyzer{})
}

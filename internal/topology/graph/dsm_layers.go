// SPDX-License-Identifier: Apache-2.0

// Package graph / dsm_layers.go — layer-source pluggability for DSMAnalyzer.
// A Layering is an ordered list of package groups where index 0 is the
// "base" (no upward allowed) and higher indices depend on lower ones.
// LayerProvider is the extension point; ConfigFileProvider,
// PathHeuristicProvider, and CompositeProvider are the concrete
// implementations.
package graph

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"gonum.org/v1/gonum/graph"
	"gonum.org/v1/gonum/graph/simple"
	"gonum.org/v1/gonum/graph/topo"
	"gopkg.in/yaml.v3"
)

// Layering is the decision surface every LayerProvider returns. Layers
// are ordered bottom-up (Layers[0] is the base). Source names the
// provider chain that produced the layering.
type Layering struct {
	Layers []Layer
	Source string
}

// Layer is one tier in a Layering. Index == slice position after
// normalization. Packages is a list of local package paths (no module
// prefix).
type Layer struct {
	Name     string
	Index    int
	Packages []string
}

// LayerProvider infers or loads a Layering. importedBy is the reverse
// adjacency of the package DAG: importedBy[P] lists every package Q such
// that Q imports P. Returning (nil, nil) means "not applicable — try the
// next provider in the chain".
type LayerProvider interface {
	Layers(ctx context.Context, pkgs []string, importedBy map[string][]string) (*Layering, error)
}

// ConfigFileProvider loads a declarative layering from
// <RepoRoot>/.knowledge/topology_layers.yaml. The analyzer runs on the
// client where the working repo is local, so the layer config is read
// directly off disk — a backend-neutral filesystem read with no store or
// server reference.
type ConfigFileProvider struct {
	RepoRoot string
}

type configFileYAML struct {
	Source string `yaml:"source"`
	Layers []struct {
		Name     string   `yaml:"name"`
		Packages []string `yaml:"packages"`
	} `yaml:"layers"`
}

// Layers reads the YAML body from <RepoRoot>/.knowledge/topology_layers.yaml.
// Missing file → (nil, nil) — the "not applicable, try next provider"
// semantics that let the heuristic provider take over. Malformed or
// empty-after-parse body → error.
func (p ConfigFileProvider) Layers(ctx context.Context, _ []string, _ map[string][]string) (*Layering, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := readLayerConfig(p.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("topology/dsm: read layer_config: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var raw configFileYAML
	if err := yaml.Unmarshal([]byte(data), &raw); err != nil {
		return nil, fmt.Errorf("topology/dsm: parse layer_config: %w", err)
	}
	if len(raw.Layers) == 0 {
		return nil, fmt.Errorf("topology/dsm: layer_config declares no layers")
	}
	out := &Layering{Source: "config-file"}
	for i, l := range raw.Layers {
		out.Layers = append(out.Layers, Layer{
			Name:     l.Name,
			Index:    i,
			Packages: append([]string(nil), l.Packages...),
		})
	}
	return out, nil
}

// readLayerConfig reads <rootDir>/.knowledge/topology_layers.yaml and
// returns the raw body as a string. Re-declared here (copied verbatim from
// the client codesync collector's readLayerConfig) because dsm must not
// import the collector package — the same precedent as dead_code
// re-declaring its own helpers. Missing file → ("", nil).
func readLayerConfig(rootDir string) (string, error) {
	if rootDir == "" {
		return "", nil
	}
	data, err := os.ReadFile(filepath.Join(rootDir, ".knowledge", "topology_layers.yaml"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// wellKnownBottom/wellKnownTop encode hardcoded basename tiebreakers
// layered ON TOP of the topo sort. These are general Go conventions,
// not repo specifics. See OQ-1 resolution.
var wellKnownBottom = map[string]struct{}{
	"store": {}, "base": {}, "util": {}, "core": {},
}

var wellKnownTop = map[string]struct{}{
	"cmd": {}, "main": {},
}

// PathHeuristicProvider infers a layering via topological sort of the
// package DAG, with well-known-name tiebreakers applied afterward.
// Zero-value usable.
type PathHeuristicProvider struct{}

// Layers topo-sorts the package DAG built from importedBy, emits one
// Layer per topo position, and pins well-known names to top/bottom.
// SCCs surface as "scc-N" layers so the cycle pass can flag them.
func (PathHeuristicProvider) Layers(ctx context.Context, pkgs []string, importedBy map[string][]string) (*Layering, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(pkgs) == 0 {
		return nil, nil
	}
	g := simple.NewDirectedGraph()
	idOf := make(map[string]int64, len(pkgs))
	names := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		n := g.NewNode()
		g.AddNode(n)
		idOf[p] = n.ID()
		names = append(names, p)
	}
	// Forward edge in our DAG: fromPkg -> toPkg means fromPkg depends
	// on toPkg. importedBy[P] lists packages Q that import P, i.e.,
	// Q -> P is an edge.
	for toPkg, importers := range importedBy {
		toID, ok := idOf[toPkg]
		if !ok {
			continue
		}
		for _, fromPkg := range importers {
			fromID, fromOK := idOf[fromPkg]
			if !fromOK || fromID == toID {
				continue
			}
			g.SetEdge(g.NewEdge(simple.Node(fromID), simple.Node(toID)))
		}
	}
	sorted, err := topo.Sort(g)
	var unorderable topo.Unorderable
	if err != nil && !errors.As(err, &unorderable) {
		return nil, fmt.Errorf("topology/dsm: topo sort: %w", err)
	}
	layers := heuristicLayersFromSort(sorted, unorderable, names)
	applyWellKnownNameRules(layers, pkgs)
	return &Layering{Source: "path-heuristic", Layers: normalizeLayerIndices(layers)}, nil
}

// heuristicLayersFromSort builds Layers from topo.Sort output. topo.Sort
// returns sinks last, so we reverse to make layer 0 the bottom. SCC
// members (surfaced via Unorderable) become "scc-N" layers.
func heuristicLayersFromSort(sorted []graph.Node, unorderable topo.Unorderable, names []string) []Layer {
	groups := make([][]string, 0, len(sorted))
	sccIdx := 0
	for _, n := range sorted {
		if n == nil {
			if sccIdx < len(unorderable) {
				groups = append(groups, sccMembers(unorderable[sccIdx], names))
				sccIdx++
			}
			continue
		}
		groups = append(groups, []string{names[n.ID()]})
	}
	for sccIdx < len(unorderable) {
		groups = append(groups, sccMembers(unorderable[sccIdx], names))
		sccIdx++
	}
	out := make([]Layer, 0, len(groups))
	for i := len(groups) - 1; i >= 0; i-- {
		idx := len(groups) - 1 - i
		name := fmt.Sprintf("layer-%d", idx)
		if len(groups[i]) > 1 {
			name = fmt.Sprintf("scc-%d", idx)
		}
		out = append(out, Layer{Name: name, Index: idx, Packages: groups[i]})
	}
	return out
}

// sccMembers extracts the names of graph.Nodes in an SCC, sorted.
func sccMembers(scc []graph.Node, names []string) []string {
	out := make([]string, 0, len(scc))
	for _, m := range scc {
		out = append(out, names[m.ID()])
	}
	sort.Strings(out)
	return out
}

// applyWellKnownNameRules moves packages matching wellKnownBottom/Top
// to the corresponding layers IN-PLACE. Without this the heuristic is
// toothless — topo sort is always clean by construction.
func applyWellKnownNameRules(layers []Layer, allPkgs []string) {
	if len(layers) == 0 {
		return
	}
	bottomIdx := 0
	topIdx := len(layers) - 1
	for _, p := range allPkgs {
		base := strings.ToLower(filepath.Base(p))
		if _, ok := wellKnownBottom[base]; ok {
			movePackage(layers, p, bottomIdx)
			continue
		}
		if _, ok := wellKnownTop[base]; ok {
			movePackage(layers, p, topIdx)
		}
	}
}

// movePackage removes pkg from any layer that holds it and appends it
// to layers[targetIdx]. No-op when already on the target.
func movePackage(layers []Layer, pkg string, targetIdx int) {
	if targetIdx < 0 || targetIdx >= len(layers) {
		return
	}
	for i := range layers {
		if i == targetIdx {
			continue
		}
		for j, p := range layers[i].Packages {
			if p != pkg {
				continue
			}
			layers[i].Packages = append(layers[i].Packages[:j], layers[i].Packages[j+1:]...)
			if slices.Contains(layers[targetIdx].Packages, pkg) {
				return
			}
			layers[targetIdx].Packages = append(layers[targetIdx].Packages, pkg)
			return
		}
	}
	if slices.Contains(layers[targetIdx].Packages, pkg) {
		return
	}
	layers[targetIdx].Packages = append(layers[targetIdx].Packages, pkg)
}

// normalizeLayerIndices drops empty layers, re-numbers Index, and
// sorts each layer's Packages for deterministic output.
func normalizeLayerIndices(layers []Layer) []Layer {
	out := make([]Layer, 0, len(layers))
	for _, l := range layers {
		if len(l.Packages) == 0 {
			continue
		}
		sort.Strings(l.Packages)
		l.Index = len(out)
		out = append(out, l)
	}
	return out
}

// CompositeProvider chains LayerProviders. The first non-nil result
// becomes the base; follow-ups contribute only packages the base did
// not declare (config-file-then-heuristic priority). A provider
// returning an error aborts the chain.
type CompositeProvider struct {
	Providers []LayerProvider
}

// Layers runs the chain and merges results per the docstring above.
func (c CompositeProvider) Layers(ctx context.Context, pkgs []string, importedBy map[string][]string) (*Layering, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var base *Layering
	sources := make([]string, 0, len(c.Providers))
	for _, p := range c.Providers {
		got, err := p.Layers(ctx, pkgs, importedBy)
		if err != nil {
			return nil, err
		}
		if got == nil || len(got.Layers) == 0 {
			continue
		}
		if base == nil {
			base = &Layering{
				Source: got.Source,
				Layers: append([]Layer(nil), got.Layers...),
			}
			sources = append(sources, got.Source)
			continue
		}
		mergeUnclassified(base, got, pkgs)
		sources = append(sources, got.Source)
	}
	if base == nil {
		return nil, nil
	}
	if len(sources) > 1 {
		base.Source = strings.Join(sources, "+")
	}
	base.Layers = normalizeLayerIndices(base.Layers)
	return base, nil
}

// mergeUnclassified adds packages from secondary that are missing in
// base. Base grows to accommodate secondary's highest layer index if
// needed. Packages not present in the known pkgs set are skipped.
func mergeUnclassified(base, secondary *Layering, pkgs []string) {
	declared := make(map[string]bool)
	for _, l := range base.Layers {
		for _, p := range l.Packages {
			declared[p] = true
		}
	}
	known := make(map[string]bool, len(pkgs))
	for _, p := range pkgs {
		known[p] = true
	}
	for i, l := range secondary.Layers {
		for j := len(base.Layers); j <= i; j++ {
			base.Layers = append(base.Layers, Layer{
				Name:  fmt.Sprintf("layer-%d", j),
				Index: j,
			})
		}
		for _, p := range l.Packages {
			if declared[p] || !known[p] {
				continue
			}
			base.Layers[i].Packages = append(base.Layers[i].Packages, p)
			declared[p] = true
		}
	}
}

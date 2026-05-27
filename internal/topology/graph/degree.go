// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// degree.go implements the three "degree distribution" analyzers:
//
//   - FanInAnalyzer       — ranks nodes by incoming edge count (popular hubs).
//   - FanOutAnalyzer      — ranks nodes by outgoing edge count (god-objects).
//   - DegreeTotalAnalyzer — ranks nodes by fan-in + fan-out.
//
// All three share a single computeDegrees pass that materializes the
// wire-built GonumGraph, fetches every edge incident to the materialized
// node set in ONE bulk wire read (foundation.FetchEdges — the N+1 guard),
// and groups the edges per node in memory to produce the per-edge-type
// breakdown that goes into the Finding.Metrics map. The legacy per-node
// scoped.IterEdges loop becomes one bulk fetch plus an in-memory group-by.
//
// Top-K finding construction is augmented with two shared helpers from
// helpers.go: ResolveNodeName for human-readable titles and sampleNeighbors
// for representative Evidence beyond the primary node ID. The neighbor
// direction depends on which analyzer is asking — see directionForDegree.

// humanMetric maps each analyzer's stable name to a human-readable
// phrase used in finding titles and summaries.
var humanMetric = map[string]string{
	"fan_in":       "fan-in",
	"fan_out":      "fan-out",
	"degree_total": "degree",
}

// directionForDegree maps each degree analyzer to the neighbor-sampling
// direction it uses when populating Finding.Evidence. fan_in pulls
// in-neighbors (who depends on this node), fan_out pulls out-neighbors
// (what this node depends on), degree_total mixes both.
var directionForDegree = map[string]string{
	"fan_in":       "in",
	"fan_out":      "out",
	"degree_total": "both",
}

// degreeRow captures the per-node aggregate counts and per-edge-type
// breakdown computed by computeDegrees. One row per materialized node.
type degreeRow struct {
	StringID  string
	FanIn     int
	FanOut    int
	InByType  map[kgtypes.EdgeType]int
	OutByType map[kgtypes.EdgeType]int
}

// total returns the combined in-degree + out-degree for the row.
func (r degreeRow) total() int { return r.FanIn + r.FanOut }

// computeDegrees materializes the graph, fetches every incident edge in
// ONE bulk wire read, and returns one degreeRow per node containing both
// the aggregate fan-in/fan-out counts and the per-edge-type breakdown.
//
// Bulk-edge group-by: the single FetchEdges call returns every edge whose
// endpoints are in the materialized node set. We group each edge against
// its FromId (out-degree of the source) and ToId (in-degree of the
// destination) in memory, so the per-edge-type breakdown is built without
// any per-node fan-out query.
func computeDegrees(ctx context.Context, req foundation.Request) ([]degreeRow, error) {
	g, err := foundation.NewGonumGraph(ctx, req.Caller, req.Graph, req.Name, req.Subset)
	if err != nil {
		return nil, fmt.Errorf("topology/degree: build graph: %w", err)
	}

	// One row per materialized node, indexed by stringID so we can bump
	// the in/out-by-type counters as we group the bulk edge read.
	ids := make([]string, 0)
	rowByID := make(map[string]*degreeRow)
	nodes := g.Nodes()
	for nodes.Next() {
		stringID, ok := g.StringID(nodes.Node().ID())
		if !ok {
			continue
		}
		ids = append(ids, stringID)
		rowByID[stringID] = &degreeRow{
			StringID:  stringID,
			InByType:  make(map[kgtypes.EdgeType]int),
			OutByType: make(map[kgtypes.EdgeType]int),
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/degree: %w", err)
	}

	edges, err := foundation.FetchEdges(ctx, req.Caller, req.Graph, req.Name, ids, nil)
	if err != nil {
		return nil, fmt.Errorf("topology/degree: fetch edges: %w", err)
	}
	for i := range edges {
		e := &edges[i]
		et := kgtypes.EdgeType(e.Type)
		if fromRow, ok := rowByID[e.FromId]; ok {
			fromRow.FanOut++
			fromRow.OutByType[et]++
		}
		if toRow, ok := rowByID[e.ToId]; ok {
			toRow.FanIn++
			toRow.InByType[et]++
		}
	}

	rows := make([]degreeRow, 0, len(rowByID))
	for _, r := range rowByID {
		rows = append(rows, *r)
	}
	return rows, nil
}

// runDegree is the shared analyzer body. It computes degrees once,
// extracts the score for the analyzer at hand via scorer, ranks via
// TopK, and builds one Finding per top-ranked row. Per-finding name
// resolution and neighbor sampling are performed lazily for the top-K
// rows only — analyzers run an extra wire read per surfaced finding,
// not per node in the graph.
func runDegree(ctx context.Context, req foundation.Request, name string, scorer func(degreeRow) int) ([]foundation.Finding, error) {
	rows, err := computeDegrees(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	allScores := make([]float64, len(rows))
	items := make([]foundation.ScoredItem, len(rows))
	for i, r := range rows {
		s := float64(scorer(r))
		allScores[i] = s
		items[i] = foundation.ScoredItem{ID: r.StringID, Score: s}
	}
	sort.Float64s(allScores)

	k := req.TopK
	if k <= 0 {
		k = defaultTopK
	}
	top := foundation.TopK(items, k)

	rowByID := make(map[string]degreeRow, len(rows))
	for _, r := range rows {
		rowByID[r.StringID] = r
	}

	dir := directionForDegree[name]
	findings := make([]foundation.Finding, 0, len(top))
	for _, it := range top {
		row, ok := rowByID[it.ID]
		if !ok {
			continue
		}
		display := ResolveNodeName(ctx, req.Caller, req.Graph, req.Name, row.StringID)
		neighbors := sampleNeighbors(ctx, req.Caller, req.Graph, req.Name, row.StringID, dir)
		findings = append(findings, buildDegreeFinding(name, row, display, neighbors, it.Score, allScores, len(rows)))
	}
	return findings, nil
}

// buildDegreeFinding constructs a single Finding for one degreeRow. It
// records the percentile-derived severity, merges the per-edge-type
// breakdown into the Metrics map under "edge:" keys, and prepends the
// caller-supplied neighbors to the Evidence slice (after the primary
// node ID) so downstream renderers have representative context.
func buildDegreeFinding(name string, row degreeRow, displayName string, neighbors []string, score float64, allScores []float64, totalNodes int) foundation.Finding {
	pct := foundation.Percentile(allScores, score)
	sev := foundation.SeverityFromPercentile(pct)
	human := humanMetric[name]

	metrics := map[string]float64{
		"fan_in":       float64(row.FanIn),
		"fan_out":      float64(row.FanOut),
		"degree_total": float64(row.total()),
		"percentile":   pct,
	}
	switch name {
	case "fan_in":
		for t, c := range row.InByType {
			metrics["edge:"+string(t)] = float64(c)
		}
	case "fan_out":
		for t, c := range row.OutByType {
			metrics["edge:"+string(t)] = float64(c)
		}
	case "degree_total":
		for t, c := range row.InByType {
			metrics["edge:"+string(t)] += float64(c)
		}
		for t, c := range row.OutByType {
			metrics["edge:"+string(t)] += float64(c)
		}
	}

	evidence := make([]string, 0, 1+len(neighbors))
	evidence = append(evidence, row.StringID)
	evidence = append(evidence, neighbors...)

	return foundation.Finding{
		Algorithm: name,
		Severity:  sev,
		Title:     fmt.Sprintf("High %s: %s", human, displayName),
		Summary:   fmt.Sprintf("%s = %d (%s). Top %.2f%% of %d nodes.", human, int(score), formatBreakdown(metrics), pct, totalNodes),
		Evidence:  evidence,
		Metrics:   metrics,
	}
}

// formatBreakdown renders the "edge:*" entries of metrics as a compact,
// deterministic string for inclusion in the Finding.Summary.
func formatBreakdown(metrics map[string]float64) string {
	const prefix = "edge:"
	keys := make([]string, 0, len(metrics))
	for k := range metrics {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return "no typed edges"
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("edges: ")
	for i, k := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s=%d", k[len(prefix):], int(metrics[k]))
	}
	return b.String()
}

// FanInAnalyzer ranks nodes by their incoming edge count.
type FanInAnalyzer struct{}

// Name returns the analyzer's stable identifier.
func (FanInAnalyzer) Name() string { return "fan_in" }

// Run executes the analyzer against req and returns ranked findings.
func (a FanInAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	return runDegree(ctx, req, a.Name(), func(r degreeRow) int { return r.FanIn })
}

// FanOutAnalyzer ranks nodes by their outgoing edge count.
type FanOutAnalyzer struct{}

// Name returns the analyzer's stable identifier.
func (FanOutAnalyzer) Name() string { return "fan_out" }

// Run executes the analyzer against req and returns ranked findings.
func (a FanOutAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	return runDegree(ctx, req, a.Name(), func(r degreeRow) int { return r.FanOut })
}

// DegreeTotalAnalyzer ranks nodes by their combined in/out edge count.
type DegreeTotalAnalyzer struct{}

// Name returns the analyzer's stable identifier.
func (DegreeTotalAnalyzer) Name() string { return "degree_total" }

// Run executes the analyzer against req and returns ranked findings.
func (a DegreeTotalAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	return runDegree(ctx, req, a.Name(), func(r degreeRow) int { return r.total() })
}

func init() {
	foundation.Register(FanInAnalyzer{})
	foundation.Register(FanOutAnalyzer{})
	foundation.Register(DegreeTotalAnalyzer{})
}

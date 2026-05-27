// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"context"
	"fmt"
	"sort"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// articulation.go is the top-level ArticulationAnalyzer that ties
// together findArticulationPoints (articulation_dfs.go) and
// computeBlastScore (articulation_blast.go) into a single
// foundation.Analyzer registered as "articulation".
//
// Pipeline:
//
//  1. Materialize the request graph via NewGonumGraph.
//  2. Get an undirected view from GonumGraph.Undirected().
//  3. Run the iterative DFS to find articulation point IDs AND
//     capture per-AP split-component sizes in the returned
//     articulationState.
//  4. For each articulation point, compute its LARGEST-STRANDING
//     blast score in O(1) via computeBlastScore.
//  5. Build one Finding per articulation point. Severity is derived
//     from the percentile rank of stranded_nodes across the
//     population of all articulation points in this graph (so the
//     worst SPOFs in the graph float to "critical" and the marginal
//     ones land at "info"). This is the one place in the structural
//     analyzer family where we use Percentile / SeverityFromPercentile,
//     because blast radius is meaningful only relative to the graph
//     it was measured in.

// ArticulationAnalyzer surfaces articulation points (cut vertices) in
// a graph and ranks them by blast radius. Zero value usable.
type ArticulationAnalyzer struct{}

// Name returns the analyzer's stable identifier.
func (ArticulationAnalyzer) Name() string { return "articulation" }

// Run materializes the request graph, runs the iterative
// Hopcroft-Tarjan DFS to find articulation points, computes a
// LARGEST-STRANDING blast score for each, and emits one Finding per
// articulation point ranked by stranded_nodes descending.
func (a ArticulationAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/articulation: %w", err)
	}

	g, err := foundation.NewGonumGraph(ctx, req.Caller, req.Graph, req.Name, req.Subset)
	if err != nil {
		return nil, fmt.Errorf("topology/articulation: build graph: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/articulation: %w", err)
	}

	undirected := g.Undirected()
	totalNodes := g.Nodes().Len()

	state, apIDs := findArticulationPoints(ctx, undirected, totalNodes)
	if len(apIDs) == 0 {
		return nil, nil
	}

	type scored struct {
		stringID string
		score    blastScore
	}
	scoredAPs := make([]scored, 0, len(apIDs))
	allStranded := make([]float64, 0, len(apIDs))
	for _, intID := range apIDs {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("topology/articulation: %w", err)
		}
		stringID, ok := g.StringID(intID)
		if !ok {
			continue
		}
		score := computeBlastScore(state, intID)
		scoredAPs = append(scoredAPs, scored{stringID: stringID, score: score})
		allStranded = append(allStranded, float64(score.StrandedNodes))
	}
	sort.Float64s(allStranded)

	findings := make([]foundation.Finding, 0, len(scoredAPs))
	for _, s := range scoredAPs {
		findings = append(findings, buildArticulationFinding(s.stringID, s.score, allStranded))
	}

	sort.SliceStable(findings, func(i, j int) bool {
		si := findings[i].Metrics["stranded_nodes"]
		sj := findings[j].Metrics["stranded_nodes"]
		if si != sj {
			return si > sj
		}
		return primaryEvidence(findings[i]) < primaryEvidence(findings[j])
	})

	return findings, nil
}

// buildArticulationFinding constructs one Finding for a single
// articulation point. The Severity is derived from the percentile
// rank of this AP's stranded_nodes within the population of all APs
// in this graph — see the file-level docs for the rationale.
func buildArticulationFinding(stringID string, score blastScore, allStranded []float64) foundation.Finding {
	pct := foundation.Percentile(allStranded, float64(score.StrandedNodes))
	severity := foundation.SeverityFromPercentile(pct)

	title := fmt.Sprintf("Articulation point: %s", stringID)
	summary := fmt.Sprintf(
		"Removing %s would disconnect the graph into %d components, "+
			"stranding %d of %d reachable nodes (largest surviving "+
			"component: %d nodes; percentile blast: %.1f).",
		stringID,
		score.ComponentCount,
		score.StrandedNodes,
		score.TotalReachable,
		score.LargestComponent,
		pct,
	)

	return foundation.Finding{
		Algorithm: "articulation",
		Severity:  severity,
		Title:     title,
		Summary:   summary,
		Evidence:  []string{stringID},
		Metrics: map[string]float64{
			"stranded_nodes":    float64(score.StrandedNodes),
			"largest_component": float64(score.LargestComponent),
			"total_reachable":   float64(score.TotalReachable),
			"component_count":   float64(score.ComponentCount),
			"percentile":        pct,
		},
	}
}

// init self-registers the Articulation analyzer with the foundation
// registry so callers can look it up by name without importing this
// file directly.
func init() {
	foundation.Register(ArticulationAnalyzer{})
}

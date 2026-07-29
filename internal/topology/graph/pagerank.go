// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"context"
	"fmt"
	"sort"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// pagerank.go implements PageRankAnalyzer — a thin wrapper around a
// from-scratch power-iteration kernel (pagerank_incremental.go) over the
// wire-built GonumGraph. It surfaces the top-K most globally-central nodes
// in a directed graph.
//
// PageRank intuition: a node's score is high when other high-scoring
// nodes link to it. The damping factor (default 0.85, the original
// Brin/Page value) controls how often a random walker follows a link
// vs teleporting to a uniformly-random node. The convergence tolerance
// (default 1e-6) terminates power iteration when the 2-norm of the
// score-vector delta drops below the threshold.
//
// Both knobs are read from Request.Extra so callers can override per
// run without forcing a new field onto Request itself:
//
//	req.Extra = map[string]string{"damping": "0.95", "tolerance": "1e-8"}
//
// Invalid or out-of-range values fall back to the defaults via
// foundation.ExtraFloat. Damping must be strictly between 0 and 1 for
// the algorithm to make physical sense; tolerance must be strictly
// positive and (sanity) below 1.
//
// This analyzer is the unweighted variant — every edge counts as weight 1.
// It is built via NewGonumGraphUnweighted so the underlying
// WeightedDirectedGraph carries unit weights only. The edge-weight signal
// lives in the separate `pagerank_weighted` analyzer.
//
// Findings include the primary node ID plus up to 5 in-neighbors (who
// depends on this node) sampled via the shared sampleNeighbors helper —
// PageRank is fundamentally an in-link-driven score so showing the
// dependers gives the most useful context.

// PageRankAnalyzer ranks nodes by their global PageRank centrality.
// Zero-value usable; analyzers self-register via init().
type PageRankAnalyzer struct{}

// Name returns the analyzer's stable identifier.
func (PageRankAnalyzer) Name() string { return "pagerank" }

// Run materializes the request graph as an unweighted view, runs the
// full power iteration, and emits the top-K nodes as Finding values. A
// run that cannot converge within the iteration cap returns a
// convergence-failure error rather than an unsettled score vector.
func (a PageRankAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/pagerank: %w", err)
	}

	damping := foundation.ExtraFloat(req, "damping", 0.85, func(v float64) bool { return v > 0 && v < 1 })
	tolerance := foundation.ExtraFloat(req, "tolerance", 1e-6, func(v float64) bool { return v > 0 && v < 1 })
	k := req.TopK
	if k <= 0 {
		k = defaultTopK
	}

	g, err := foundation.NewGonumGraphUnweighted(ctx, req.Caller, req.Graph, req.Name, req.Subset)
	if err != nil {
		return nil, fmt.Errorf("topology/pagerank: build graph: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/pagerank: %w", err)
	}
	if g.Nodes().Len() == 0 {
		return nil, nil
	}

	scores, err := runFullPowerIteration(ctx, g, damping, tolerance, dfprMaxIterations)
	if err != nil {
		return nil, err
	}
	if len(scores) == 0 {
		return nil, nil
	}

	items, allScores := pageRankScoredItems(g, scores)
	sort.Float64s(allScores)

	top := foundation.TopK(items, k)
	return assemblePageRankFindings(ctx, req, top, items, allScores, damping, tolerance), nil
}

// pageRankScoredItems materializes (stringID, score) pairs from the gonum
// int64-keyed score map and returns the parallel slices the analyzer
// needs for ranking + percentile lookup.
func pageRankScoredItems(g *foundation.GonumGraph, scores map[int64]float64) ([]foundation.ScoredItem, []float64) {
	items := make([]foundation.ScoredItem, 0, len(scores))
	allScores := make([]float64, 0, len(scores))
	for intID, score := range scores {
		stringID, ok := g.StringID(intID)
		if !ok {
			continue
		}
		items = append(items, foundation.ScoredItem{ID: stringID, Score: score})
		allScores = append(allScores, score)
	}
	return items, allScores
}

// assemblePageRankFindings produces a Finding per top-K node. Extracted
// from Run to keep the dispatch path under the line cap.
func assemblePageRankFindings(
	ctx context.Context,
	req foundation.Request,
	top []foundation.ScoredItem,
	items []foundation.ScoredItem,
	allScores []float64,
	damping, tolerance float64,
) []foundation.Finding {
	findings := make([]foundation.Finding, 0, len(top))
	for i, item := range top {
		pct := foundation.Percentile(allScores, item.Score)
		sev := foundation.SeverityFromPercentile(pct)
		display := ResolveNodeName(ctx, req.Caller, req.Graph, req.Name, item.ID)
		neighbors := sampleNeighbors(ctx, req.Caller, req.Graph, req.Name, item.ID, "in")

		evidence := make([]string, 0, 1+len(neighbors))
		evidence = append(evidence, item.ID)
		evidence = append(evidence, neighbors...)

		findings = append(findings, foundation.Finding{
			Algorithm: "pagerank",
			Severity:  sev,
			Title:     fmt.Sprintf("High-centrality node: %s", display),
			Summary: fmt.Sprintf(
				"PageRank %.6f, ranks #%d of %d. Top %.2f%% of nodes by PageRank (damping=%.3f).",
				item.Score, i+1, len(items), pct, damping,
			),
			Evidence: evidence,
			Metrics: map[string]float64{
				"pagerank":   item.Score,
				"rank":       float64(i + 1),
				"percentile": pct,
				"damping":    damping,
				"tolerance":  tolerance,
			},
		})
	}
	return findings
}

func init() {
	foundation.Register(PageRankAnalyzer{})
}

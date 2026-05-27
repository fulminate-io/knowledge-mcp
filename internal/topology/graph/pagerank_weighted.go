// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"context"
	"fmt"
	"sort"

	"gonum.org/v1/gonum/graph/network"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// pagerank_weighted.go implements WeightedPageRankAnalyzer — a sibling of
// PageRankAnalyzer that lets edge weights influence the score. It is
// registered as a separate analyzer (`pagerank_weighted`) so callers can
// pick the variant they want without forcing every existing PageRank
// consumer to opt in to the new signal.
//
// How the weight gets there: an edge's Weight carries a relative strength
// signal — for code CALLS edges that's the per-callsite count from
// tree-sitter, preserved through the Go RTA merge. The graph serializer
// round-trip keeps the weight intact across save/load. The wire-backed
// gonum builder materializes those weights via SetWeightedEdge with a
// 0 → 1.0 fallback for any edge that was never weighted (legacy graphs,
// non-Go languages whose chunkers don't emit call counts, hand-built
// knowledge edges).
//
// Weighted PageRank intuition: gonum's network.PageRankSparse auto-
// detects a graph.WeightedDirected and switches into edge-weighted mode,
// where the random-walker follows an outgoing edge with probability
// proportional to its weight (normalized over the source node's outgoing
// weight sum). A heavily-called helper therefore receives more random-
// walker mass than a function called once, which is exactly the "hot
// helper" signal weighted PageRank is meant to surface.
//
// Damping and tolerance are read from Request.Extra exactly the same way
// the unweighted analyzer reads them — see pagerank.go for the contract.
//
// Limitations:
//   - Non-Go/TS code graphs default to Weight=1 because their chunkers
//     don't yet emit call counts. Weighted PageRank still runs there,
//     but it produces the same ranking as unweighted PageRank because
//     every edge has identical weight.
//   - Knowledge graphs and cloud graphs likewise have no semantic notion
//     of edge weight today, so weighted PageRank degenerates to its
//     unweighted equivalent over those graphs.

// WeightedPageRankAnalyzer ranks nodes by their weighted PageRank
// centrality, taking edge weight into account. Zero-value usable;
// analyzers self-register via init().
type WeightedPageRankAnalyzer struct{}

// Name returns the analyzer's stable identifier.
func (WeightedPageRankAnalyzer) Name() string { return "pagerank_weighted" }

// Run materializes the request graph (weight-aware), runs gonum's
// PageRankSparse over the embedded simple.WeightedDirectedGraph (which
// auto-switches into edge-weighted mode), and emits the top-K nodes as
// Finding values.
func (a WeightedPageRankAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/pagerank_weighted: %w", err)
	}

	g, err := foundation.NewGonumGraph(ctx, req.Caller, req.Graph, req.Name, req.Subset)
	if err != nil {
		return nil, fmt.Errorf("topology/pagerank_weighted: build graph: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/pagerank_weighted: %w", err)
	}

	// Empty graph: gonum's PageRankSparse panics on a zero-length node
	// set ("mat: zero length in matrix dimension"). Guard before calling.
	if g.Nodes().Len() == 0 {
		return nil, nil
	}

	damping := foundation.ExtraFloat(req, "damping", 0.85, func(v float64) bool { return v > 0 && v < 1 })
	tolerance := foundation.ExtraFloat(req, "tolerance", 1e-6, func(v float64) bool { return v > 0 && v < 1 })

	scores := network.PageRankSparse(g.WeightedDirectedGraph, damping, tolerance)
	if len(scores) == 0 {
		return nil, nil
	}

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
	sort.Float64s(allScores)

	k := req.TopK
	if k <= 0 {
		k = defaultTopK
	}
	top := foundation.TopK(items, k)

	return assembleWeightedPageRankFindings(ctx, req, top, items, allScores, damping, tolerance), nil
}

// assembleWeightedPageRankFindings builds one Finding per top-K node.
// Extracted from Run to keep the dispatch body under the line cap.
func assembleWeightedPageRankFindings(
	ctx context.Context,
	req foundation.Request,
	top, items []foundation.ScoredItem,
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
			Algorithm: "pagerank_weighted",
			Severity:  sev,
			Title:     fmt.Sprintf("High-centrality node (weighted): %s", display),
			Summary: fmt.Sprintf(
				"Weighted PageRank %.6f, ranks #%d of %d. Top %.2f%% of nodes by weighted PageRank (damping=%.3f).",
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
	foundation.Register(WeightedPageRankAnalyzer{})
}

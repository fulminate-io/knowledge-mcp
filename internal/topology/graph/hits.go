// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"context"
	"fmt"
	"sort"

	"gonum.org/v1/gonum/graph/network"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// hits.go implements the two HITS (Hyperlink-Induced Topic Search)
// analyzers — HITSHubAnalyzer and HITSAuthorityAnalyzer — over the bounded
// HITS power iteration in hits_iteration.go. Only the HubAuthority score
// pair is borrowed from gonum; the iteration itself is owned here so it
// terminates on every input (see that file for why).
//
// HITS intuition (Kleinberg, 1999):
//   - A good HUB is a page that points to many good authorities.
//   - A good AUTHORITY is a page pointed to by many good hubs.
//
// The two scores are computed jointly via mutually-reinforcing power
// iteration until both converge below the requested 2-norm tolerance.
//
// In this codebase the same intuition maps onto:
//   - HUBS:        nodes whose outgoing edges point to lots of valuable
//     things (god-objects, central decision documents, root
//     manifests that aggregate downstream context).
//   - AUTHORITIES: nodes that lots of valuable things point at (popular
//     utility functions, foundational types, canonical
//     knowledge nodes that everyone references).
//
// Both analyzers share a single runHITSScored body that materializes the
// graph, runs the HITS iteration, and projects the per-node HubAuthority
// pair down to whichever score the caller asked for. The selector closure
// + direction string distinguish the two analyzers cleanly with no
// duplication.
//
// Convergence tolerance is read from req.Extra["tolerance"] (default
// 1e-8 — HITS converges faster than PageRank in practice but the tighter
// default keeps tied scores numerically stable). Invalid values fall
// back to the default via foundation.ExtraFloat. There is no damping
// parameter; HITS does not have one.
//
// Findings include the primary node ID plus up to 5 representative
// neighbors. For hubs we sample out-neighbors (the authorities the hub
// points at); for authorities we sample in-neighbors (the hubs that
// point at the authority). The direction is the most informative for
// each role.

// HITSHubAnalyzer ranks nodes by their HITS hub score. Zero-value usable.
type HITSHubAnalyzer struct{}

// HITSAuthorityAnalyzer ranks nodes by their HITS authority score.
// Zero-value usable.
type HITSAuthorityAnalyzer struct{}

// Name returns the analyzer's stable identifier.
func (HITSHubAnalyzer) Name() string { return "hits_hubs" }

// Name returns the analyzer's stable identifier.
func (HITSAuthorityAnalyzer) Name() string { return "hits_authorities" }

// Run materializes the request graph, runs HITS, and emits findings for
// the top-K hub-scoring nodes.
func (a HITSHubAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	return runHITSScored(ctx, req, "hits_hubs", "hub", "out",
		func(ha network.HubAuthority) float64 { return ha.Hub })
}

// Run materializes the request graph, runs HITS, and emits findings for
// the top-K authority-scoring nodes.
func (a HITSAuthorityAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	return runHITSScored(ctx, req, "hits_authorities", "authority", "in",
		func(ha network.HubAuthority) float64 { return ha.Authority })
}

// computeHITSScores materializes the graph, runs the bounded HITS
// iteration, and returns the HITS score map plus the int→string ID
// mapping. Returns (nil, nil, nil) on empty-graph input so the caller
// short-circuits cleanly, and a convergence-failure error if the
// iteration hits its cap.
func computeHITSScores(ctx context.Context, req foundation.Request, name string, tolerance float64) (map[int64]network.HubAuthority, map[int64]string, error) {
	g, gerr := foundation.NewGonumGraph(ctx, req.Caller, req.Graph, req.Name, req.Subset)
	if gerr != nil {
		return nil, nil, fmt.Errorf("topology/%s: build graph: %w", name, gerr)
	}
	// No nodes means no scores; nothing below has anything to rank.
	if g.Nodes().Len() == 0 {
		return nil, nil, nil
	}
	scores, err := hitsScores(ctx, g.WeightedDirectedGraph, tolerance, hitsMaxIterations, name)
	if err != nil {
		return nil, nil, err
	}
	stringIDByInt := make(map[int64]string, len(scores))
	for intID := range scores {
		if sid, ok := g.StringID(intID); ok {
			stringIDByInt[intID] = sid
		}
	}
	return scores, stringIDByInt, nil
}

// runHITSScored is the shared body for both HITS analyzers. The selector
// extracts either Hub or Authority from gonum's HubAuthority struct, the
// kind word goes into the human-readable Title and Summary, and the
// direction controls when neighbors get sampled into Evidence.
func runHITSScored(
	ctx context.Context,
	req foundation.Request,
	name, kind, direction string,
	selector func(network.HubAuthority) float64,
) ([]foundation.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/%s: %w", name, err)
	}

	tolerance := foundation.ExtraFloat(req, "tolerance", 1e-8, func(v float64) bool { return v > 0 && v < 1 })

	scores, stringIDByInt, err := computeHITSScores(ctx, req, name, tolerance)
	if err != nil {
		return nil, err
	}
	if len(scores) == 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/%s: %w", name, err)
	}

	items := make([]foundation.ScoredItem, 0, len(scores))
	allScores := make([]float64, 0, len(scores))
	for intID, ha := range scores {
		stringID, ok := stringIDByInt[intID]
		if !ok {
			continue
		}
		s := selector(ha)
		items = append(items, foundation.ScoredItem{ID: stringID, Score: s})
		allScores = append(allScores, s)
	}
	sort.Float64s(allScores)

	k := req.TopK
	if k <= 0 {
		k = defaultTopK
	}
	top := foundation.TopK(items, k)

	return assembleHITSFindings(ctx, req, top, items, allScores, name, kind, direction, tolerance), nil
}

// assembleHITSFindings builds one Finding per top-K HITS node. Extracted
// from runHITSScored to keep the dispatch body under the line cap.
//
// A zero score means the node carries no signal on this axis at all — it
// points at nothing (hub) or nothing points at it (authority) — so it never
// becomes a "high-hub"/"high-authority" finding no matter how few nodes
// outrank it. Without that skip an edgeless graph, or any graph with fewer
// than K scoring nodes, reports zero-score nodes at whatever severity their
// percentile implies. Percentiles are still computed over the full score
// set, so the surviving findings keep their true standing.
func assembleHITSFindings(
	ctx context.Context,
	req foundation.Request,
	top, items []foundation.ScoredItem,
	allScores []float64,
	name, kind, direction string,
	tolerance float64,
) []foundation.Finding {
	findings := make([]foundation.Finding, 0, len(top))
	for i, item := range top {
		if item.Score == 0 {
			continue
		}
		pct := foundation.Percentile(allScores, item.Score)
		sev := foundation.SeverityFromPercentile(pct)
		display := ResolveNodeName(ctx, req.Caller, req.Graph, req.Name, item.ID)
		neighbors := sampleNeighbors(ctx, req.Caller, req.Graph, req.Name, item.ID, direction)

		evidence := make([]string, 0, 1+len(neighbors))
		evidence = append(evidence, item.ID)
		evidence = append(evidence, neighbors...)

		findings = append(findings, foundation.Finding{
			Algorithm: name,
			Severity:  sev,
			Title:     fmt.Sprintf("High-%s node: %s", kind, display),
			Summary: fmt.Sprintf(
				"HITS %s score %.6f, ranks #%d of %d. Top %.2f%% by %s score (tolerance=%g).",
				kind, item.Score, i+1, len(items), pct, kind, tolerance,
			),
			Evidence: evidence,
			Metrics: map[string]float64{
				"score":      item.Score,
				"rank":       float64(i + 1),
				"percentile": pct,
				"tolerance":  tolerance,
			},
		})
	}
	return findings
}

func init() {
	foundation.Register(HITSHubAnalyzer{})
	foundation.Register(HITSAuthorityAnalyzer{})
}

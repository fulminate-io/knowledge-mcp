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

// blast_radius_bfs.go implements the per-seed reverse BFS used by
// BlastRadiusAnalyzer.Run plus the helpers that turn a completed walk
// into a Finding. The reverse walk reads incoming dependents per frontier
// layer in a bulk wire edge fetch and the node-by-id + redundancy reads
// go through the shared foundation read-helpers — there is no in-process
// store anywhere on this path.

// blastImpact accumulates per-node contributions during the reverse BFS.
// nodeID maps to (depth, weighted_contribution). The depth is recorded
// for the layered summary in buildBlastFinding; the contribution is the
// redundancy_factor / depth term added to the global blast_score.
type blastImpact struct {
	depth        int
	contribution float64
}

// runBlastFromSeed runs one reverse BFS from a seed node and returns the
// derived Finding. Honors maxDepth, applies redundancy weighting per
// intermediate node, and tracks a visited set to prevent cycles. Returns
// an error only on wire-side failures so the analyzer's outer loop can
// abort cleanly.
func runBlastFromSeed(
	ctx context.Context,
	req foundation.Request,
	seed string,
	edgeTypes []kgtypes.EdgeType,
	maxDepth int,
) (foundation.Finding, error) {
	visited := make(map[string]blastImpact, 32)
	frontier := []string{seed}
	visited[seed] = blastImpact{depth: 0, contribution: 0}

	for depth := 1; depth <= maxDepth && len(frontier) > 0; depth++ {
		if cerr := ctx.Err(); cerr != nil {
			return foundation.Finding{}, fmt.Errorf("topology/blast_radius: %w", cerr)
		}
		next, err := expandLayer(ctx, req, frontier, edgeTypes, depth, visited)
		if err != nil {
			return foundation.Finding{}, err
		}
		frontier = next
	}

	return buildBlastFinding(ctx, req, seed, visited, maxDepth)
}

// expandLayer walks one BFS layer: fetch every edge incident to the whole
// frontier in a bulk wire read, keep the incoming edges (ToId in the
// frontier → FromId is a dependent), register newly-discovered dependents
// in the visited map (with their depth and redundancy-weighted
// contribution), and return the next frontier. The redundancy lookup is
// performed against the *dependent* node — that's the intermediate that
// would have to fail along with the seed for the blast to propagate, and
// its replicas / fan-in count is the relevant load-bearing signal.
func expandLayer(
	ctx context.Context,
	req foundation.Request,
	frontier []string,
	edgeTypes []kgtypes.EdgeType,
	depth int,
	visited map[string]blastImpact,
) ([]string, error) {
	dependentsByNode, err := incomingDependents(ctx, req, frontier, edgeTypes)
	if err != nil {
		return nil, err
	}
	next := make([]string, 0, len(frontier))
	for _, nodeID := range frontier {
		for _, depID := range dependentsByNode[nodeID] {
			if _, seen := visited[depID]; seen {
				continue
			}
			factor, ferr := lookupAndWeight(ctx, req, depID)
			if ferr != nil {
				return nil, ferr
			}
			contribution := factor / float64(depth)
			visited[depID] = blastImpact{depth: depth, contribution: contribution}
			next = append(next, depID)
		}
	}
	return next, nil
}

// incomingDependents returns, for every node in the frontier, the IDs of
// the nodes that have an incoming edge of one of the requested types
// pointing AT it. This is the reverse direction: a bulk FetchEdges over
// the frontier, keeping edges whose ToId is in the frontier and returning
// their FromId. The per-frontier-node grouping preserves the layered BFS
// semantics while avoiding a per-node wire round-trip within the layer.
func incomingDependents(
	ctx context.Context,
	req foundation.Request,
	frontier []string,
	edgeTypes []kgtypes.EdgeType,
) (map[string][]string, error) {
	edges, err := foundation.FetchEdges(ctx, req.Caller, req.Graph, req.Name, frontier, edgeTypes)
	if err != nil {
		return nil, fmt.Errorf("topology/blast_radius: incoming on frontier: %w", err)
	}
	frontierSet := make(map[string]struct{}, len(frontier))
	for _, id := range frontier {
		frontierSet[id] = struct{}{}
	}
	out := make(map[string][]string, len(frontier))
	seen := make(map[string]map[string]struct{}, len(frontier))
	for i := range edges {
		e := &edges[i]
		// Reverse: an edge FROM dependent TO a frontier node means the
		// dependent relies on the frontier node.
		if _, ok := frontierSet[e.ToId]; !ok {
			continue
		}
		dep := e.FromId
		if dep == "" || dep == e.ToId {
			continue
		}
		dedup, ok := seen[e.ToId]
		if !ok {
			dedup = make(map[string]struct{})
			seen[e.ToId] = dedup
		}
		if _, dup := dedup[dep]; dup {
			continue
		}
		dedup[dep] = struct{}{}
		out[e.ToId] = append(out[e.ToId], dep)
	}
	return out, nil
}

// lookupAndWeight resolves a node by ID and runs it through the
// redundancy registry. A node that the graph cannot find is treated as
// fully load-bearing (factor 1.0) — missing nodes shouldn't crash the
// BFS, they should be invisible to the weighting pass.
func lookupAndWeight(ctx context.Context, req foundation.Request, nodeID string) (float64, error) {
	n, ok, err := foundation.FetchNodeByID(ctx, req.Caller, req.Graph, req.Name, nodeID)
	if err != nil {
		return defaultRedundancyFactor, fmt.Errorf("topology/blast_radius: lookup %s: %w", nodeID, err)
	}
	if !ok || n == nil {
		return defaultRedundancyFactor, nil
	}
	return redundancyFactor(ctx, req, n)
}

// buildBlastFinding turns a completed BFS map into a Finding. Computes
// the aggregate score, the per-layer count, and the top-K most-impactful
// dependents (sorted by contribution descending). Severity defaults to
// SeverityInfo here; the multi-seed severity pass in Run rewrites it
// when there are at least 2 findings to rank.
func buildBlastFinding(
	ctx context.Context,
	req foundation.Request,
	seed string,
	visited map[string]blastImpact,
	maxDepth int,
) (foundation.Finding, error) {
	totalAffected := 0
	score := 0.0
	maxObserved := 0
	layerCounts := map[int]int{}
	for id, imp := range visited {
		if id == seed {
			continue
		}
		totalAffected++
		score += imp.contribution
		if imp.depth > maxObserved {
			maxObserved = imp.depth
		}
		layerCounts[imp.depth]++
	}

	display := ResolveNodeName(ctx, req.Caller, req.Graph, req.Name, seed)
	topImpacts := topImpactedNodes(visited, seed, blastEvidenceMax)

	evidence := make([]string, 0, 1+len(topImpacts))
	evidence = append(evidence, seed)
	for _, ti := range topImpacts {
		evidence = append(evidence, ti.ID)
	}

	summary := fmt.Sprintf(
		"%d resources affected across %d layers (max depth %d, cap %d). Redundancy-weighted impact: %.3f. %s",
		totalAffected, len(layerCounts), maxObserved, maxDepth, score, formatLayerCounts(layerCounts),
	)

	return foundation.Finding{
		Algorithm: "blast_radius",
		Severity:  foundation.SeverityInfo,
		Title:     fmt.Sprintf("Blast radius: %s", display),
		Summary:   summary,
		Evidence:  evidence,
		Metrics: map[string]float64{
			"blast_score":                score,
			"total_affected":             float64(totalAffected),
			"max_depth":                  float64(maxObserved),
			"max_depth_cap":              float64(maxDepth),
			"redundancy_weighted_impact": score,
			"layer_count":                float64(len(layerCounts)),
		},
	}, nil
}

// topImpactedNodes returns the top-N dependents from a visited map,
// sorted by descending contribution with a stable ID-ascending tiebreak.
// Excludes the seed itself. Mirrors the TopK helper's contract but
// operates over the impact map directly to avoid allocating an
// intermediate ScoredItem slice for what is typically a tiny result.
func topImpactedNodes(visited map[string]blastImpact, seed string, n int) []foundation.ScoredItem {
	if n <= 0 || len(visited) == 0 {
		return nil
	}
	items := make([]foundation.ScoredItem, 0, len(visited))
	for id, imp := range visited {
		if id == seed {
			continue
		}
		items = append(items, foundation.ScoredItem{ID: id, Score: imp.contribution})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		return items[i].ID < items[j].ID
	})
	if len(items) > n {
		items = items[:n]
	}
	return items
}

// formatLayerCounts renders the per-layer dependent count as a stable,
// sorted string for the Finding summary. "Layer 1: 3, Layer 2: 5".
// Returns "" for an empty map so callers can omit the prefix entirely.
func formatLayerCounts(layers map[int]int) string {
	if len(layers) == 0 {
		return ""
	}
	depths := make([]int, 0, len(layers))
	for d := range layers {
		depths = append(depths, d)
	}
	sort.Ints(depths)
	var b strings.Builder
	for i, d := range depths {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "Layer %d: %d", d, layers[d])
	}
	return b.String()
}

// seedsFromPageRank composes blast_radius with the registered pagerank
// analyzer. When the seedless mode is requested, run pagerank with TopK=k
// against the same graph and harvest each finding's primary evidence
// (the source node ID). Returns an error if pagerank is not registered
// or if the sub-run fails — the caller treats both as user-visible
// failures so the operator knows why no findings came back.
func seedsFromPageRank(ctx context.Context, req foundation.Request, k int) ([]string, error) {
	pr, ok := foundation.Get("pagerank")
	if !ok {
		return nil, fmt.Errorf("topology/blast_radius: seedless mode requires pagerank analyzer")
	}
	subReq := req
	subReq.TopK = k
	// Clear Extra so the sub-run doesn't try to interpret blast-radius
	// knobs as pagerank knobs.
	subReq.Extra = nil
	findings, err := pr.Run(ctx, subReq)
	if err != nil {
		return nil, fmt.Errorf("topology/blast_radius: pagerank seed selection failed: %w", err)
	}
	seeds := make([]string, 0, len(findings))
	for _, f := range findings {
		if len(f.Evidence) > 0 && f.Evidence[0] != "" {
			seeds = append(seeds, f.Evidence[0])
		}
	}
	return seeds, nil
}

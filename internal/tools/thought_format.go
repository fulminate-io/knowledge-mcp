// SPDX-License-Identifier: Apache-2.0

// thought_format.go — render helpers for the reflective surface
// dispatched by InterceptThoughts. Each helper takes a parsed args
// struct, runs the client-side reflective function over the
// GraphClient, and shapes the result into a TextResult or jsonResult.
//
// Function-shape note: these are free functions, not methods on
// *Handler. The migration from the server-side handler form to the
// client-side intercept form is the structural change BCN4 v2 makes.

package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// handleReflectPersonality renders the personality profile.
func handleReflectPersonality(ctx context.Context, deps ClientDeps, a queryReflectArgs) kgtools.ToolResult {
	clusters, profile := fetchClusterContext(ctx, deps)
	report := clientthought.ReflectPersonality(clusters, profile, a.Cluster)
	if a.Format == "json" {
		return jsonResult(report)
	}
	return textResult(renderPersonality(report))
}

// renderPersonality is the text body shared with the JSON-disabled
// path. Mirrors the pre-BCN4 server-side formatter.
func renderPersonality(report clientthought.PersonalityReport) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Personality Profile (%d clusters)\n\n", report.ClusterCount)
	if len(report.TopStubborn) > 0 {
		sb.WriteString("## Most Stubborn (resistant to external influence)\n")
		for _, p := range report.TopStubborn {
			fmt.Fprintf(&sb, "  %s -> %s: %.3f\n", sanitizeLabel(p.LabelA), sanitizeLabel(p.LabelB), p.Scalar)
		}
		sb.WriteString("\n")
	}
	if len(report.TopGullible) > 0 {
		sb.WriteString("## Most Open (receptive to external influence)\n")
		for _, p := range report.TopGullible {
			fmt.Fprintf(&sb, "  %s -> %s: %.3f\n", sanitizeLabel(p.LabelA), sanitizeLabel(p.LabelB), p.Scalar)
		}
	}
	return sb.String()
}

// sanitizeLabel collapses whitespace + strips markdown markers so
// cluster labels (which are derived from thought SymbolNames and can
// carry arbitrary content) don't break the rendered table.
func sanitizeLabel(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	s = strings.TrimSpace(s)
	if len(s) > 60 {
		s = s[:60] + "..."
	}
	return s
}

// handleReflectInfluence ranks the top-N most influential thoughts.
func handleReflectInfluence(ctx context.Context, deps ClientDeps, a queryReflectArgs) kgtools.ToolResult {
	gc := deps.GraphClient()
	if gc == nil {
		return errorResult("influence: graph client unavailable")
	}
	_, profile := fetchClusterContext(ctx, deps)
	limit := a.Limit
	if limit <= 0 {
		limit = 10
	}
	reports, err := clientthought.ReflectInfluence(ctx, gc, limit, profile)
	if err != nil {
		return errorResult("influence computation failed: " + err.Error())
	}
	if a.Format == "json" {
		return jsonResult(reports)
	}
	if len(reports) == 0 {
		return textResult("No thoughts to analyze.")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Most Influential Thoughts (top %d)\n\n", len(reports))
	for i, r := range reports {
		fmt.Fprintf(&sb, "%d. **%s** (influence: %.4f)\n", i+1, r.Node.SymbolName, r.InfluenceScore)
		fmt.Fprintf(&sb, "   valence:%.2f mag:%.2f | %s\n\n", r.Properties.Valence, r.Properties.Magnitude, r.ThoughtID)
	}
	return textResult(sb.String())
}

// handleReflectTensions surfaces pairs of connected thoughts with
// opposing valence.
func handleReflectTensions(ctx context.Context, deps ClientDeps, a queryReflectArgs) kgtools.ToolResult {
	gc := deps.GraphClient()
	if gc == nil {
		return errorResult("tensions: graph client unavailable")
	}
	tensions, err := clientthought.ReflectTensions(ctx, gc)
	if err != nil {
		return errorResult("tension detection failed: " + err.Error())
	}
	if a.Format == "json" {
		return jsonResult(tensions)
	}
	if len(tensions) == 0 {
		return textResult("No unresolved tensions found. Connected thoughts are in agreement.")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Unresolved Tensions (%d found)\n\n", len(tensions))
	for i, t := range tensions {
		fmt.Fprintf(&sb, "%d. **%s** (v:%.2f) vs **%s** (v:%.2f) -- delta: %.2f\n",
			i+1, t.ThoughtA.SymbolName, t.PropertiesA.Valence,
			t.ThoughtB.SymbolName, t.PropertiesB.Valence, t.ValenceDelta)
	}
	return textResult(sb.String())
}

// handleReflectBlindSpots surfaces clusters with little evidence.
func handleReflectBlindSpots(ctx context.Context, deps ClientDeps, a queryReflectArgs) kgtools.ToolResult {
	gc := deps.GraphClient()
	if gc == nil {
		return errorResult("blind_spots: graph client unavailable")
	}
	clusters, _ := fetchClusterContext(ctx, deps)
	// ReflectBlindSpots needs the thought adjacency for bridge detection;
	// reuse the same fetch path the loop uses.
	_, adj, err := clientthought.FetchThoughtAdjacency(ctx, gc)
	if err != nil {
		return errorResult("blind_spots: adjacency fetch failed: " + err.Error())
	}
	spots := clientthought.ReflectBlindSpots(ctx, gc, clusters, adj)
	if a.Format == "json" {
		return jsonResult(spots)
	}
	if len(spots) == 0 {
		return textResult("No blind spots found. All clusters have adequate evidence.")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Blind Spots (%d clusters with low evidence)\n\n", len(spots))
	for _, sp := range spots {
		fmt.Fprintf(&sb, "- **%s** (%d thoughts, %d charges, avg mag: %.2f)\n",
			sp.Cluster.Label, sp.Cluster.Size, sp.ChargeCount, sp.AvgMagnitude)
		if len(sp.BridgeThoughts) > 0 {
			uncharged := 0
			for _, bt := range sp.BridgeThoughts {
				if !bt.HasCharges {
					uncharged++
				}
			}
			fmt.Fprintf(&sb, "  Bridge thoughts: %d (%d without evidence)\n", len(sp.BridgeThoughts), uncharged)
			for _, bt := range sp.BridgeThoughts {
				icon := "~"
				if !bt.HasCharges {
					icon = "!"
				}
				fmt.Fprintf(&sb, "    [%s] %s (internal: %.0f%%, %s)\n",
					icon, bt.Name, bt.InternalFraction*100, bt.ThoughtID)
			}
		}
	}
	return textResult(sb.String())
}

// handleReflectSummary renders the overall thought-graph summary.
func handleReflectSummary(ctx context.Context, deps ClientDeps, a queryReflectArgs) kgtools.ToolResult {
	gc := deps.GraphClient()
	if gc == nil {
		return errorResult("summary: graph client unavailable")
	}
	clusters, _ := fetchClusterContext(ctx, deps)
	summary := clientthought.ReflectSummary(ctx, gc, clusters)
	if a.Format == "json" {
		return jsonResult(summary)
	}
	var sb strings.Builder
	sb.WriteString("# Thought Graph Summary\n\n")
	fmt.Fprintf(&sb, "- Thoughts: %d\n", summary.TotalThoughts)
	fmt.Fprintf(&sb, "- Charges: %d\n", summary.TotalCharges)
	fmt.Fprintf(&sb, "- Sessions: %d\n", summary.TotalSessions)
	fmt.Fprintf(&sb, "- Clusters: %d\n", summary.ClusterCount)
	fmt.Fprintf(&sb, "- Avg valence: %.3f\n", summary.AvgValence)
	fmt.Fprintf(&sb, "- Avg magnitude: %.3f\n\n", summary.AvgMagnitude)
	if len(summary.TopClusters) > 0 {
		sb.WriteString("## Top Clusters\n")
		for _, c := range summary.TopClusters {
			fmt.Fprintf(&sb, "  - %s (%d thoughts, avg v:%.2f m:%.2f)\n",
				c.Label, c.Size, c.AvgValence, c.AvgMagnitude)
		}
		sb.WriteString("\n")
	}
	if len(summary.RecentThoughts) > 0 {
		sb.WriteString("## Recent Thoughts\n")
		for _, t := range summary.RecentThoughts {
			fmt.Fprintf(&sb, "  - [%s] %s (%s)\n", t.Status, t.SymbolName, time.Unix(0, t.CreatedAt).Format("2006-01-02 15:04"))
		}
	}
	return textResult(sb.String())
}

// handleReflectEvolution surfaces the scalar evolution between two
// clusters over time.
func handleReflectEvolution(ctx context.Context, deps ClientDeps, a queryReflectArgs) kgtools.ToolResult {
	if a.ClusterA == "" || a.ClusterB == "" {
		return errorResult("evolution mode requires cluster_a and cluster_b")
	}
	gc := deps.GraphClient()
	if gc == nil {
		return errorResult("evolution: graph client unavailable")
	}
	clusters, _ := fetchClusterContext(ctx, deps)
	snapshots, err := clientthought.ComputeScalarEvolution(ctx, gc, clusters, a.ClusterA, a.ClusterB, 30, nil)
	if err != nil {
		return errorResult("evolution failed: " + err.Error())
	}
	if a.Format == "json" {
		return jsonResult(map[string]any{
			"cluster_a": a.ClusterA,
			"cluster_b": a.ClusterB,
			"snapshots": snapshots,
		})
	}
	if len(snapshots) == 0 {
		return textResult("No evolution data available for these clusters.")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Scalar Evolution: %s -> %s\n\n", a.ClusterA, a.ClusterB)
	for _, snap := range snapshots {
		fmt.Fprintf(&sb, "  %s: scalar=%.3f\n", snap.Timestamp.Format("2006-01-02"), snap.Scalar)
	}
	return textResult(sb.String())
}

// handleReflectClusters surfaces all detected clusters (thought-only).
func handleReflectClusters(ctx context.Context, deps ClientDeps, a queryReflectArgs) kgtools.ToolResult {
	gc := deps.GraphClient()
	if gc == nil {
		return errorResult("clusters: graph client unavailable")
	}
	clusters, err := clientthought.DetectThoughtClusters(ctx, gc, 0.5)
	if err != nil {
		return errorResult("cluster detection failed: " + err.Error())
	}
	if a.Format == "json" {
		return jsonResult(clusters)
	}
	return textResult(formatAllClusters(clusters))
}

// handleRecallClusters serves thoughts(recall, mode:"clusters"[,
// all_types]). For all_types, runs DetectAllClusters (every node type
// minus proxies). For the thought-only variant, runs
// DetectThoughtClusters. The pre-BCN4 implementation overlaid server
// recall results on the cluster topology; the BCN4 v2 path is
// cluster-only — clients call thoughts(recall) (no mode) for text-mode
// recall and use cluster IDs from this output to cross-reference.
func handleRecallClusters(ctx context.Context, deps ClientDeps, allTypes bool, format string) kgtools.ToolResult {
	gc := deps.GraphClient()
	if gc == nil {
		return errorResult("recall(clusters): graph client unavailable")
	}
	if allTypes {
		clusters, err := clientthought.DetectAllClusters(ctx, gc, 0.5)
		if err != nil {
			return errorResult("all-types cluster detection failed: " + err.Error())
		}
		if format == "json" {
			return jsonResult(clusters)
		}
		return textResult(formatAllClusters(clusters))
	}
	// Thought-only clusters mode: just surface the cluster topology.
	// The pre-BCN4 implementation overlaid recall results on clusters,
	// but the recall+overlay path is not part of the reflective
	// surface's primary use case — clients call query(mode:"clusters")
	// for cluster-only views and thoughts(recall) for text-mode recall.
	clusters, err := clientthought.DetectThoughtClusters(ctx, gc, 0.5)
	if err != nil {
		return errorResult("cluster detection failed: " + err.Error())
	}
	if format == "json" {
		return jsonResult(clusters)
	}
	return textResult(formatAllClusters(clusters))
}

// formatAllClusters renders the cluster topology directly. Mirrors the
// pre-BCN4 server-side formatter that lived in tools_thought_query.go.
func formatAllClusters(clusters []clientthought.ThoughtCluster) string {
	if len(clusters) == 0 {
		return "No clusters detected."
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d clusters:\n\n", len(clusters))
	for _, c := range clusters {
		fmt.Fprintf(&sb, "## %s (%d nodes, avg valence: %.2f, avg magnitude: %.2f)\n",
			c.Label, c.Size, c.AvgValence, c.AvgMagnitude)
		fmt.Fprintf(&sb, "  ID: %s\n", c.ID)
		fmt.Fprintf(&sb, "  Members: %d nodes\n", len(c.ThoughtIDs))
		sb.WriteString("\n")
	}
	return sb.String()
}

// formatPropagationResult mirrors the pre-BCN4 server-side message
// shape returned by handlePropagate.
func formatPropagationResult(r clientthought.PropagationResult) string {
	return fmt.Sprintf(
		"Propagation complete: thoughts_processed=%d components=%d iterations=%d converged=%v",
		r.ThoughtsProcessed, r.Components, r.Iterations, r.Converged,
	)
}

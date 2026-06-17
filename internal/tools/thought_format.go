// SPDX-License-Identifier: Apache-2.0

// thought_format.go — render helpers for the reflective surface
// dispatched by InterceptThoughts. Each helper takes a parsed args
// struct, runs the client-side reflective function over the
// GraphClient, and shapes the result into a TextResult or jsonResult.
//
// Function-shape note: these are free functions, not methods on
// *Handler. The migration from the server-side handler form to the
// client-side intercept form is the structural change made here.

package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// coldClusterStateMessage is the LOUD cold-case report the persisted-cluster
// surfaces return when the corpus is non-empty but no node carries cluster_id yet
// (the hourly propagation loop has not completed a pass). It is deliberately
// distinct from a healthy empty graph so a cold daemon is never mistaken for "no
// thoughts" / "no clusters detected".
const coldClusterStateMessage = "Reflection has not completed a pass yet — cluster_id metadata is not yet " +
	"populated. Clusters and personality appear after the next propagation tick " +
	"(hourly; the daemon must be logged in). This is the cold state, not an empty graph."

// handleReflectPersonality renders the personality profile. Cold case (non-empty
// corpus, no persisted cluster_id yet) → an explicit not-yet-computed report.
func handleReflectPersonality(ctx context.Context, deps ClientDeps, a queryReflectArgs) kgtools.ToolResult {
	clusters, profile, cold := fetchClusterContext(ctx, deps)
	if cold {
		return textResult(coldClusterStateMessage)
	}
	// Prefer persisted topic-summary text over the member-SymbolName label wherever
	// a topic doc exists (lever-produced); unsummarized clusters keep their label.
	clientthought.ApplyTopicLabels(ctx, deps.GraphCaller(), clusters, profile)
	report := clientthought.ReflectPersonality(clusters, profile, a.Cluster)
	// granularity:"topic" rolls the cluster-pair rows up to topic-pairs for display
	// (labels become topic summaries; scalars unchanged). Empty/"cluster" leaves the
	// default per-cluster report byte-identical.
	if a.Granularity == "topic" {
		g := clientthought.TopicGroupingByClusterID(ctx, deps.GraphCaller())
		report = clientthought.RollupPersonalityTopics(report, g)
	}
	if a.Format == "json" {
		return jsonResult(report)
	}
	return textResult(renderPersonality(report))
}

// renderPersonality is the text body shared with the JSON-disabled
// path. Mirrors the prior server-side formatter.
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

// writeInfluenceRow renders one influence report row in the shared per-row
// format used by both the evidenced and the backfill sections: rank, symbol
// name, influence score, then a valence/magnitude/charge-annotation line. The
// charge count is annotated on every row so the surface doubles as a backfill
// worklist; a zero-charge row carries the loud backfill marker (structurally
// central but unsupported by evidence).
func writeInfluenceRow(sb *strings.Builder, i int, r clientthought.InfluenceReport) {
	fmt.Fprintf(sb, "%d. **%s** (influence: %.4f)\n", i+1, r.Node.SymbolName, r.InfluenceScore)
	chargeAnnot := fmt.Sprintf("%d charges", r.Properties.ChargeCount)
	if r.Properties.ChargeCount == 0 {
		chargeAnnot = "! 0 charges — backfill candidate"
	}
	fmt.Fprintf(sb, "   valence:%.2f mag:%.2f | %s | %s\n\n",
		r.Properties.Valence, r.Properties.Magnitude, chargeAnnot, r.ThoughtID)
}

// handleReflectInfluence renders the evidence-aware two-section influence
// ranking: the evidenced top-N first, then the labeled zero-charge backfill
// candidates.
func handleReflectInfluence(ctx context.Context, deps ClientDeps, a queryReflectArgs) kgtools.ToolResult {
	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("influence: graph client unavailable")
	}
	_, profile, _ := fetchClusterContext(ctx, deps)
	limit := a.Limit
	if limit <= 0 {
		limit = 10
	}
	ranking, err := clientthought.ReflectInfluence(ctx, gc, limit, profile, a.Sort)
	if err != nil {
		return errorResult("influence computation failed: " + err.Error())
	}
	if a.Format == "json" {
		return jsonResult(ranking)
	}
	if len(ranking.Evidenced) == 0 && len(ranking.BackfillCandidates) == 0 {
		return textResult("No thoughts to analyze.")
	}
	var sb strings.Builder
	// Evidenced section: the charged thoughts ranked by influence×(1+chargeWeight).
	// When empty (no charged thoughts) the surface is honestly all-backfill — say so
	// rather than omitting the header.
	if len(ranking.Evidenced) > 0 {
		fmt.Fprintf(&sb, "# Most Influential Thoughts (evidenced, top %d)\n\n", len(ranking.Evidenced))
		for i, r := range ranking.Evidenced {
			writeInfluenceRow(&sb, i, r)
		}
	} else {
		sb.WriteString("# Most Influential Thoughts (evidenced)\n\nNo charged thoughts — every influential thought is an unevidenced backfill candidate.\n\n")
	}
	// Backfill section: only when zero-charge hubs are present. The explainer keeps a
	// consumer from reading near-uniform eigenvector mass as evidence.
	if len(ranking.BackfillCandidates) > 0 {
		sb.WriteString("## Influential but unevidenced (backfill candidates)\n\n")
		sb.WriteString("Structurally central but carrying zero charges — the backfill worklist, not evidence.\n\n")
		for i, r := range ranking.BackfillCandidates {
			writeInfluenceRow(&sb, i, r)
		}
	}
	return textResult(sb.String())
}

// handleReflectTensions surfaces pairs of connected thoughts with
// opposing valence.
func handleReflectTensions(ctx context.Context, deps ClientDeps, a queryReflectArgs) kgtools.ToolResult {
	gc := deps.GraphCaller()
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
	// Totals header: candidateCount is the sum of PairCount across the shown
	// representatives (how many raw qualifying pairs they collapse); clusterPairs is
	// the number of representatives shown (one per cluster-pair); shown is the
	// rendered count. The slice is already collapsed + ranked + capped by
	// ReflectTensions, so clusterPairs == shown here.
	candidateCount := 0
	for _, t := range tensions {
		candidateCount += t.PairCount
	}
	clusterPairs := len(tensions)
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Unresolved Tensions — %d candidate tensions across %d cluster-pairs, showing top %d\n\n",
		candidateCount, clusterPairs, len(tensions))
	for i, t := range tensions {
		fmt.Fprintf(&sb, "%d. **%s** (v:%.2f, %d charges) vs **%s** (v:%.2f, %d charges) -- delta: %.2f\n",
			i+1, t.ThoughtA.SymbolName, t.PropertiesA.Valence, t.PropertiesA.ChargeCount,
			t.ThoughtB.SymbolName, t.PropertiesB.Valence, t.PropertiesB.ChargeCount, t.ValenceDelta)
		fmt.Fprintf(&sb, "   via %s\n", tensionProvenanceLabel(t))
		if t.PairCount > 1 {
			fmt.Fprintf(&sb, "   collapses %d similar pairs\n", t.PairCount)
		}
	}
	return textResult(sb.String())
}

// tensionProvenanceLabel renders the linking edge's provenance for a tension row:
// the edge type, annotated [human] when no machine Method is present (machine
// methods never reach a tension report — they are pre-filtered — so a non-empty
// Method here is an unexpected provenance and is shown verbatim).
func tensionProvenanceLabel(t clientthought.TensionReport) string {
	edgeType := t.EdgeType
	if edgeType == "" {
		edgeType = "relates-to"
	}
	if t.Method == "" {
		return edgeType + "[human]"
	}
	return edgeType + "[" + t.Method + "]"
}

// blindSpotColdMessage is the not-yet-computed report query(mode:blind_spots)
// returns when the reflection loop has not completed a tick (report.Computed=false,
// including right after a daemon restart). The faceted report is produced by the
// background propagation loop and served from cache O(1) — the on-demand call
// NEVER recomputes synchronously, so a cold cache returns this rather than blocking
// on a full pass.
const blindSpotColdMessage = "Reflection has not completed a pass yet — the blind-spots report is not yet " +
	"computed. The faceted epistemic-risk diagnostic appears after the next propagation tick " +
	"(hourly; the daemon must be logged in). This is the cold state, not an empty result."

// handleReflectBlindSpots serves the loop's cached faceted epistemic-risk report
// in O(1): it reads the BlindSpotProvider seam (the live PropagationLoop's
// GetBlindSpots) and renders it. It issues NO graph reads on this path — no
// adjacency fetch, no influence pass, no charge/node hydrate — because the
// background tick already computed the report. A nil provider (reflection loop not
// running) or a not-yet-computed report (Computed=false) returns a clear message,
// never a synchronous recompute.
func handleReflectBlindSpots(_ context.Context, deps ClientDeps, a queryReflectArgs) kgtools.ToolResult {
	provider := deps.BlindSpotProvider()
	if provider == nil {
		return textResult("blind_spots: the reflection loop is not running in this process — " +
			"no cached report to serve. Start the daemon with the propagation runtime enabled.")
	}
	report := provider.GetBlindSpots()
	if !report.Computed {
		return textResult(blindSpotColdMessage)
	}
	if a.Format == "json" {
		return jsonResult(report)
	}
	return textResult(renderBlindSpots(report))
}

// handleReflectSummary renders the overall thought-graph summary.
func handleReflectSummary(ctx context.Context, deps ClientDeps, a queryReflectArgs) kgtools.ToolResult {
	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("summary: graph client unavailable")
	}
	clusters, _, _ := fetchClusterContext(ctx, deps)
	// Prefer persisted topic-summary text over the member-SymbolName label wherever
	// a topic doc exists (lever-produced); unsummarized clusters keep their label.
	clientthought.ApplyTopicLabels(ctx, gc, clusters, nil)
	summary := clientthought.ReflectSummary(ctx, gc, clusters)
	// granularity:"topic" rolls clusters sharing a topic into one TopClusters row
	// (Size summed, valence/magnitude size-weighted, label = topic summary). Empty/
	// "cluster" leaves the default per-cluster TopClusters byte-identical.
	if a.Granularity == "topic" {
		g := clientthought.TopicGroupingByClusterID(ctx, gc)
		rolled := clientthought.RollupSummaryTopics(clusters, g)
		if len(rolled) > 5 {
			rolled = rolled[:5]
		}
		summary.TopClusters = rolled
	}
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
	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("evolution: graph client unavailable")
	}
	clusters, _, _ := fetchClusterContext(ctx, deps)
	snapshots, err := clientthought.ComputeScalarEvolution(ctx, gc, clusters, a.ClusterA, a.ClusterB, 30, clientthought.BuildEvidenceAdj(ctx, gc, clusters))
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
	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("clusters: graph client unavailable")
	}
	clusters, err := clientthought.DetectPersistedClusters(ctx, gc)
	if errors.Is(err, clientthought.ErrClustersNotComputed) {
		return textResult(coldClusterStateMessage)
	}
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
// DetectThoughtClusters. The prior implementation overlaid server
// recall results on the cluster topology; the current path is
// cluster-only — clients call thoughts(recall) (no mode) for text-mode
// recall and use cluster IDs from this output to cross-reference.
func handleRecallClusters(ctx context.Context, deps ClientDeps, allTypes bool, format string) kgtools.ToolResult {
	gc := deps.GraphCaller()
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
	// Thought-only clusters mode: surface the loop-persisted cluster topology.
	// The prior implementation overlaid recall results on clusters,
	// but the recall+overlay path is not part of the reflective
	// surface's primary use case — clients call query(mode:"clusters")
	// for cluster-only views and thoughts(recall) for text-mode recall.
	// Reads persisted cluster_id state (DetectPersistedClusters), not a live
	// recompute, to stay within the tool ceiling.
	clusters, err := clientthought.DetectPersistedClusters(ctx, gc)
	if err != nil {
		return errorResult("cluster detection failed: " + err.Error())
	}
	if format == "json" {
		return jsonResult(clusters)
	}
	return textResult(formatAllClusters(clusters))
}

// formatAllClusters renders the cluster topology directly. Mirrors the
// prior server-side formatter that lived in tools_thought_query.go.
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

// formatPropagationResult mirrors the prior server-side message shape returned by
// handlePropagate, reporting convergence PER COMPONENT — never a bare global
// converged flag, so one slow clique no longer masks the converged majority.
func formatPropagationResult(r clientthought.PropagationResult) string {
	var sb strings.Builder
	fmt.Fprintf(&sb,
		"Propagation complete: thoughts_processed=%d components=%d iterations=%d — %d of %d components converged",
		r.ThoughtsProcessed, r.Components, r.Iterations, r.ComponentsConverged, r.Components,
	)
	for _, nc := range r.NonConverged {
		fmt.Fprintf(&sb, "\n  non-converged: size %d, residual Δ=%.4f (valence) / %.4f (magnitude)",
			nc.Size, nc.ValenceResidual, nc.MagnitudeResidual)
	}
	if r.NonConvergedOmitted > 0 {
		fmt.Fprintf(&sb, "\n  and %d more non-converged components", r.NonConvergedOmitted)
	}
	return sb.String()
}

// SPDX-License-Identifier: Apache-2.0

// thought_format_blindspots.go — the faceted blind-spots renderer, split from
// thought_format.go to keep that file under the 500-line limit. renderBlindSpots
// turns the loop-computed BlindSpotReport into the per-facet text surface the
// handler serves; the handler itself (handleReflectBlindSpots) stays in
// thought_format.go beside the other reflect handlers.

package tools

import (
	"fmt"
	"strings"

	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// renderBlindSpots renders the faceted epistemic-risk report as text: a header
// with the total-thoughts-considered count, then one section per non-empty facet.
// Each item shows the thought Name, its backing signal values
// (magnitude/consistency/charges/influence), the per-facet Reason, and the
// ThoughtID. Belief reversal additionally carries a topic/cluster-level view
// (facet.Groups) rendered as a sub-list of pooled reversals. When every facet is
// empty the surface reports no blind spots found rather than an empty body.
// Mirrors renderPersonality's shared-body shape.
func renderBlindSpots(report clientthought.BlindSpotReport) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Blind Spots — epistemic-risk facets over %d thoughts\n\n", report.TotalThoughts)
	if len(report.Facets) == 0 {
		sb.WriteString("No epistemic-risk blind spots found.\n")
		return sb.String()
	}
	for _, facet := range report.Facets {
		fmt.Fprintf(&sb, "## %s (%d)\n", facet.Title, len(facet.Items))
		for _, it := range facet.Items {
			fmt.Fprintf(&sb, "- **%s** (mag:%.2f, consistency:%.2f, %d charges, influence:%.4f)\n",
				sanitizeLabel(it.Name), it.Magnitude, it.Consistency, it.ChargeCount, it.Influence)
			fmt.Fprintf(&sb, "  %s (%s)\n", it.Reason, it.ThoughtID)
		}
		renderBlindSpotGroups(&sb, facet.Groups)
		sb.WriteString("\n")
	}
	return sb.String()
}

// renderBlindSpotGroups renders the topic/cluster-pooled belief-reversal view as a
// labeled sub-list under its facet: each group shows the topic/cluster label, the
// old→recent net-polarity swing, the member count, and the member thoughts driving
// the reversal. No-op when there are no groups (every facet but belief reversal).
func renderBlindSpotGroups(sb *strings.Builder, groups []clientthought.BlindSpotGroup) {
	if len(groups) == 0 {
		return
	}
	fmt.Fprintf(sb, "### Topic/cluster-level reversals (%d)\n", len(groups))
	for _, g := range groups {
		fmt.Fprintf(sb, "- **%s** (old net:%+.1f → recent net:%+.1f, %d members)\n",
			sanitizeLabel(g.Label), g.OldNet, g.RecentNet, g.MemberCount)
		fmt.Fprintf(sb, "  %s [%s] (%s)\n", g.Reason, strings.Join(g.Members, ", "), g.Key)
	}
}

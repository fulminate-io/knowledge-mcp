// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// renderTicketPlans renders the Plans section for a ticket assembly.
// Ported from cmd/knowledge-server/tools/tools_assemble_containers.go:226
// with the store reads swapped for wire-shape FetchNode + IterEdges calls.
func renderTicketPlans(ctx context.Context, gc GraphCaller, plans []*knowledgev1.Node) string {
	if len(plans) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n## Plans\n\n")
	for _, p := range plans {
		statusLabel := p.Status
		if statusLabel == "" {
			statusLabel = "active"
		}
		fmt.Fprintf(&sb, "- [%s] %s — ID: %s\n", statusLabel, p.SymbolName, p.Id)
		// Show phase summary.
		phaseEdges, _ := IterEdges(ctx, gc, p.Id, kgwire.OutgoingEdges, kgtypes.EdgeKGContains)
		total, done := 0, 0
		for _, e := range phaseEdges {
			pn, err := FetchNode(ctx, gc, e.ToId)
			if err != nil || pn == nil || kgtypes.NodeType(pn.Type) != kgtypes.NodePhase {
				continue
			}
			total++
			if pn.Status == kgtypes.StatusCompleted {
				done++
			}
		}
		if total > 0 {
			fmt.Fprintf(&sb, "  %d/%d phases completed\n", done, total)
		}
	}
	return sb.String()
}

// renderTicketResearch renders the Research section for a ticket assembly.
// Ported from cmd/knowledge-server/tools/tools_assemble_containers.go:261.
func renderTicketResearch(ctx context.Context, gc GraphCaller, researches []*knowledgev1.Node) string {
	if len(researches) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n## Research\n\n")
	for _, r := range researches {
		statusLabel := r.Status
		if statusLabel == "" {
			statusLabel = "active"
		}
		qEdges, _ := IterEdges(ctx, gc, r.Id, kgwire.OutgoingEdges, kgtypes.EdgeKGContains)
		qCount := 0
		for _, e := range qEdges {
			qn, err := FetchNode(ctx, gc, e.ToId)
			if err == nil && qn != nil && kgtypes.NodeType(qn.Type) == kgtypes.NodeQuestion {
				qCount++
			}
		}
		fmt.Fprintf(&sb, "- [%s] %s (%d questions) — ID: %s\n", statusLabel, r.SymbolName, qCount, r.Id)
	}
	return sb.String()
}

// renderTicketDecisions renders the Linked Decisions section for a
// ticket assembly. Verbatim port of
// cmd/knowledge-server/tools/tools_assemble_containers.go:287 — pure
// formatter over an already-fetched slice.
func renderTicketDecisions(decisions []*knowledgev1.Node) string {
	if len(decisions) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n## Linked Decisions\n\n")
	for _, d := range decisions {
		fmt.Fprintf(&sb, "- %s — ID: %s\n", d.SymbolName, d.Id)
		if choice := kgtypes.Value(d, "choice"); choice != "" {
			fmt.Fprintf(&sb, "  Choice: %s\n", truncate(choice, 100))
		}
	}
	return sb.String()
}

// renderTicketFindings renders the Linked Findings section for a
// ticket assembly. Verbatim port of
// cmd/knowledge-server/tools/tools_assemble_containers.go:303.
func renderTicketFindings(findings []*knowledgev1.Node) string {
	if len(findings) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n## Linked Findings\n\n")
	for _, f := range findings {
		fmt.Fprintf(&sb, "- %s — ID: %s\n", f.SymbolName, f.Id)
		if f.Description != "" {
			fmt.Fprintf(&sb, "  %s\n", truncate(f.Description, 120))
		}
	}
	return sb.String()
}

// renderProjectTickets renders the Tickets section for a project
// container assembly. Ported from
// cmd/knowledge-server/tools/tools_assemble_containers.go:360 with
// store reads swapped for wire-shape calls.
func renderProjectTickets(ctx context.Context, gc GraphCaller, tickets []*knowledgev1.Node) string {
	if len(tickets) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n## Tickets\n\n")
	for _, t := range tickets {
		statusLabel := t.Status
		if statusLabel == "" {
			statusLabel = "active"
		}
		priority := kgtypes.Value(t, "priority")
		if priority != "" {
			fmt.Fprintf(&sb, "- [%s] [%s] %s — ID: %s\n", statusLabel, priority, t.SymbolName, t.Id)
		} else {
			fmt.Fprintf(&sb, "- [%s] %s — ID: %s\n", statusLabel, t.SymbolName, t.Id)
		}

		// Summary of child plans and research.
		gcEdges, _ := IterEdges(ctx, gc, t.Id, kgwire.OutgoingEdges, kgtypes.EdgeKGContains)
		planCount, researchCount := 0, 0
		for _, e := range gcEdges {
			gcn, err := FetchNode(ctx, gc, e.ToId)
			if err != nil || gcn == nil {
				continue
			}
			switch kgtypes.NodeType(gcn.Type) {
			case kgtypes.NodePlan:
				planCount++
			case kgtypes.NodeResearch:
				researchCount++
			}
		}
		if planCount > 0 || researchCount > 0 {
			fmt.Fprintf(&sb, "  ")
			if planCount > 0 {
				fmt.Fprintf(&sb, "%d plan(s)", planCount)
			}
			if planCount > 0 && researchCount > 0 {
				fmt.Fprintf(&sb, ", ")
			}
			if researchCount > 0 {
				fmt.Fprintf(&sb, "%d research node(s)", researchCount)
			}
			fmt.Fprintf(&sb, "\n")
		}
	}
	return sb.String()
}

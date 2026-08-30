// SPDX-License-Identifier: Apache-2.0

package render

import (
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// countChildrenOfType counts a parent's already-indexed contains children of one
// node type. It replaces what used to be an IterEdges plus a FetchNode per
// grandchild at each of these call sites — reads issued solely to compute an
// integer, which is the worst shape of N+1 in this file.
func countChildrenOfType(childIndex map[string][]*knowledgev1.Node, parentID string, typ kgtypes.NodeType) int {
	n := 0
	for _, c := range childIndex[parentID] {
		if kgtypes.NodeType(c.Type) == typ {
			n++
		}
	}
	return n
}

// renderTicketPlans renders the Plans section for a ticket assembly.
// Ported from cmd/knowledge-server/tools/tools_assemble_containers.go:226; each
// plan's phases are read from the caller's prefetched index rather than fetched
// per plan, so this function issues no wire call.
func renderTicketPlans(plans []*knowledgev1.Node, childIndex map[string][]*knowledgev1.Node) string {
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
		total, done := 0, 0
		for _, pn := range childIndex[p.Id] {
			if kgtypes.NodeType(pn.Type) != kgtypes.NodePhase {
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
// Ported from cmd/knowledge-server/tools/tools_assemble_containers.go:261; the
// per-research question count comes from the prefetched index.
func renderTicketResearch(researches []*knowledgev1.Node, childIndex map[string][]*knowledgev1.Node) string {
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
		qCount := countChildrenOfType(childIndex, r.Id, kgtypes.NodeQuestion)
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
// cmd/knowledge-server/tools/tools_assemble_containers.go:360; each ticket's
// plan and research counts come from the caller's prefetched index, so this
// function issues no wire call. It previously hydrated EVERY grandchild of
// every ticket in full to compute two integers.
func renderProjectTickets(tickets []*knowledgev1.Node, childIndex map[string][]*knowledgev1.Node) string {
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
		planCount := countChildrenOfType(childIndex, t.Id, kgtypes.NodePlan)
		researchCount := countChildrenOfType(childIndex, t.Id, kgtypes.NodeResearch)
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

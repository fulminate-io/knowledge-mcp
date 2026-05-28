// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// assembleTicket renders a NodeTicket: header (status / external_id /
// URL / priority / labels / description / ID) + Plans + Research +
// Patterns + Language patterns + Linked Decisions + Linked Findings.
//
// Ported from cmd/knowledge-server/tools/tools_assemble_containers.go:16
// as a free function with the store reads swapped for wire-shape
// FetchNode + IterEdges calls. Section ordering matches server-side
// output byte-for-byte for golden-file parity.
func assembleTicket(ctx context.Context, gc GraphCaller, node *knowledgev1.Node) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Ticket: %s\n\n", node.SymbolName)
	renderTicketHeader(node, &sb)

	// Walk contains edges to find child plans and research.
	childEdges, _ := IterEdges(ctx, gc, node.Id, kgwire.OutgoingEdges, kgtypes.EdgeKGContains)
	var plans, researches []*knowledgev1.Node
	for _, e := range childEdges {
		cn, err := FetchNode(ctx, gc, e.ToId)
		if err != nil || cn == nil {
			continue
		}
		switch kgtypes.NodeType(cn.Type) {
		case kgtypes.NodePlan:
			plans = append(plans, cn)
		case kgtypes.NodeResearch:
			researches = append(researches, cn)
		}
	}

	sb.WriteString(renderTicketPlans(ctx, gc, plans))
	sb.WriteString(renderTicketResearch(ctx, gc, researches))

	// Walk outgoing edges for linked decisions, findings, and
	// patterns. One IterEdges call (no type filter) so we can
	// dispatch on edge type below.
	outEdges, _ := IterEdges(ctx, gc, node.Id, kgwire.OutgoingEdges)
	var decisions, findings, patterns, languagePatterns []*knowledgev1.Node
	for _, e := range outEdges {
		switch kgtypes.EdgeType(e.Type) {
		case kgtypes.EdgeInformedBy, kgtypes.EdgeRelatesTo:
			ln, err := FetchNode(ctx, gc, e.ToId)
			if err != nil || ln == nil {
				continue
			}
			switch kgtypes.NodeType(ln.Type) {
			case kgtypes.NodeDecision:
				decisions = append(decisions, ln)
			case kgtypes.NodeFinding:
				findings = append(findings, ln)
			}
		case kgtypes.EdgeUses:
			// Broken-link tolerance: EdgeUses can point at pattern
			// IDs that don't resolve (v1 bogus-id handling). Skip
			// unresolved targets — they're reported via the
			// unresolved_pattern_ids metadata key in
			// renderTicketPatterns.
			pn, err := FetchNode(ctx, gc, e.ToId)
			if err != nil || pn == nil {
				continue
			}
			patterns = append(patterns, pn)
		case kgtypes.EdgeAudits:
			// Language-pattern targets. Same broken-link tolerance
			// as EdgeUses — unresolved IDs are reported via the
			// unresolved_language_patterns metadata key.
			lp, err := FetchNode(ctx, gc, e.ToId)
			if err != nil || lp == nil {
				continue
			}
			languagePatterns = append(languagePatterns, lp)
		}
	}

	renderTicketPatterns(node, patterns, &sb)
	renderLanguagePatternsSection(node, languagePatterns, &sb)
	sb.WriteString(renderTicketDecisions(decisions))
	sb.WriteString(renderTicketFindings(findings))

	return kgtools.TextResult(sb.String())
}

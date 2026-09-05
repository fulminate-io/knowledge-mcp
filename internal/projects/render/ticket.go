// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"

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

	// Depth 2: the ticket's child plans and research are depth 1, and the
	// phases and questions the sections below count are depth 2. Nothing this
	// arm renders lives deeper, and a larger depth would pull rows the render
	// discards while bringing the server's row ceiling closer.
	childIndex, byID, _, truncated := AssembleSubtree(ctx, gc, node.Id, 2)

	var plans, researches []*knowledgev1.Node
	for _, cn := range childIndex[node.Id] {
		switch kgtypes.NodeType(cn.Type) {
		case kgtypes.NodePlan:
			plans = append(plans, cn)
		case kgtypes.NodeResearch:
			researches = append(researches, cn)
		}
	}

	sb.WriteString(renderTicketPlans(plans, childIndex))
	sb.WriteString(renderTicketResearch(researches, childIndex))

	// Walk outgoing edges for linked decisions, findings, and
	// patterns. One IterEdges call (no type filter) so we can
	// dispatch on edge type below, then ONE bulk hydrate over every
	// target the four edge types name.
	outEdges, _ := IterEdges(ctx, gc, node.Id, kgwire.OutgoingEdges)
	targetIDs := make([]string, 0, len(outEdges))
	for _, e := range outEdges {
		switch kgtypes.EdgeType(e.Type) {
		case kgtypes.EdgeInformedBy, kgtypes.EdgeRelatesTo, kgtypes.EdgeUses, kgtypes.EdgeAudits:
			targetIDs = append(targetIDs, e.ToId)
		}
	}
	linked, linkedTruncated, _ := foundation.FetchNodesByIDs(ctx, gc, "", "", targetIDs, foundation.IncludeTombstones)
	truncated = truncated || linkedTruncated

	// Walk the EDGE slice, never the hydrated map: the slice carries edge order
	// and a map range would reorder every section below at random.
	var decisions, findings, patterns, languagePatterns []*knowledgev1.Node
	for _, e := range outEdges {
		// BROKEN-LINK TOLERANCE, PRESERVED. A uses or audits edge can point at
		// a pattern id that does not resolve, and an unresolved target is
		// SKIPPED here rather than reported inline — the unresolved ids are
		// surfaced separately through the unresolved_pattern_ids and
		// unresolved_language_patterns metadata keys. The condition used to be
		// a FetchNode error; with a bulk hydrate it is a miss in the map, and
		// it stays a skip.
		tgt, ok := linked[e.ToId]
		if !ok {
			continue
		}
		switch kgtypes.EdgeType(e.Type) {
		case kgtypes.EdgeInformedBy, kgtypes.EdgeRelatesTo:
			switch kgtypes.NodeType(tgt.Type) {
			case kgtypes.NodeDecision:
				decisions = append(decisions, tgt)
			case kgtypes.NodeFinding:
				findings = append(findings, tgt)
			}
		case kgtypes.EdgeUses:
			patterns = append(patterns, tgt)
		case kgtypes.EdgeAudits:
			languagePatterns = append(languagePatterns, tgt)
		}
	}

	renderTicketPatterns(node, patterns, &sb)
	renderLanguagePatternsSection(node, languagePatterns, &sb)
	sb.WriteString(renderTicketDecisions(decisions))
	sb.WriteString(renderTicketFindings(findings))

	// Two verdicts, OR'd: a clamped traversal silently shortens the plan and
	// research lists — which looks exactly like a small ticket — and a clamped
	// linked-nodes hydrate silently shortens the pattern, decision and finding
	// sections. A complete subtree with a clamped hydrate is still incomplete.
	return AppendTruncationNotice(kgtools.TextResult(sb.String()), truncated, len(byID)+len(linked))
}

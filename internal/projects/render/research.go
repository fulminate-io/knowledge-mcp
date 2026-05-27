// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// assembleResearch renders a NodeResearch: header + RenderTree walk +
// per-question Findings section + Resulting Decisions section.
//
// Ported from cmd/knowledge-server/tools/tools_assemble.go:287 with
// store reads swapped for wire-shape FetchNode + IterEdges calls.
func assembleResearch(ctx context.Context, gc GraphCaller, node *knowledgev1.Node) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Research: %s\n\n", node.SymbolName)
	RenderTree(ctx, gc, &sb, node, 0, 3)

	// For each question, find findings via reverse EdgeAnswers.
	qEdges, _ := IterEdges(ctx, gc, node.Id, kgwire.OutgoingEdges, kgtypes.EdgeKGContains)
	var hasFinding bool
	for _, e := range qEdges {
		qn, err := FetchNode(ctx, gc, e.ToId)
		if err != nil || qn == nil || kgtypes.NodeType(qn.Type) != kgtypes.NodeQuestion {
			continue
		}
		// EdgeAnswers: finding → question. Walk incoming edges on
		// the question to find findings.
		findingEdges, _ := IterEdges(ctx, gc, qn.Id, kgwire.IncomingEdges, kgtypes.EdgeAnswers)
		if len(findingEdges) == 0 {
			continue
		}
		if !hasFinding {
			fmt.Fprintf(&sb, "\n## Findings\n\n")
			hasFinding = true
		}
		fmt.Fprintf(&sb, "### Q: %s\n", qn.SymbolName)
		for _, fe := range findingEdges {
			fn, err := FetchNode(ctx, gc, fe.FromId)
			if err != nil || fn == nil {
				continue
			}
			fmt.Fprintf(&sb, "  - [%s] %s — ID: %s\n", fn.Type, fn.SymbolName, fn.Id)
			if fn.Description != "" {
				fmt.Fprintf(&sb, "    %s\n", truncate(fn.Description, 120))
			}
		}
	}

	// Decisions linked via EdgeInformedBy (incoming to this research node).
	inEdges, _ := IterEdges(ctx, gc, node.Id, kgwire.IncomingEdges, kgtypes.EdgeInformedBy)
	var decisions []*knowledgev1.Node
	for _, e := range inEdges {
		dn, err := FetchNode(ctx, gc, e.FromId)
		if err == nil && dn != nil && kgtypes.NodeType(dn.Type) == kgtypes.NodeDecision {
			decisions = append(decisions, dn)
		}
	}
	if len(decisions) > 0 {
		fmt.Fprintf(&sb, "\n## Resulting Decisions\n\n")
		for _, d := range decisions {
			fmt.Fprintf(&sb, "- %s — ID: %s\n", d.SymbolName, d.Id)
			if choice := kgtypes.Value(d, "choice"); choice != "" {
				fmt.Fprintf(&sb, "  Choice: %s\n", truncate(choice, 100))
			}
		}
	}
	return kgtools.TextResult(sb.String())
}

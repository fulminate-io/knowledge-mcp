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

// assemblePlan renders a NodePlan: header + RenderTree walk + Linked
// Research + Language patterns. Mirrors the server-side shape at
// cmd/knowledge-server/tools/tools_assemble.go:164 (renamed from
// assembleProject — the server used the legacy name).
//
// Ported as a free function with store reads swapped for wire-shape
// FetchNode + IterEdges calls. The RenderTree call uses the
// foundation port from Phase 1's render/tree.go.
func assemblePlan(ctx context.Context, gc GraphCaller, node *knowledgev1.Node) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Plan: %s\n\n", node.SymbolName)
	RenderTree(ctx, gc, &sb, node, 0, 4)

	// Follow EdgeInformedBy edges to linked research, and EdgeAudits
	// edges to language patterns. One IterEdges call (no type
	// filter) so we can dispatch on edge type below.
	outEdges, _ := IterEdges(ctx, gc, node.Id, kgwire.OutgoingEdges)
	var researchIDs []string
	var languagePatterns []*knowledgev1.Node
	for _, e := range outEdges {
		switch kgtypes.EdgeType(e.Type) {
		case kgtypes.EdgeInformedBy:
			researchIDs = append(researchIDs, e.ToId)
		case kgtypes.EdgeAudits:
			lp, err := FetchNode(ctx, gc, e.ToId)
			if err != nil || lp == nil {
				continue
			}
			languagePatterns = append(languagePatterns, lp)
		}
	}
	if len(researchIDs) > 0 {
		fmt.Fprintf(&sb, "\n## Linked Research\n\n")
		for _, rid := range researchIDs {
			rn, err := FetchNode(ctx, gc, rid)
			if err != nil || rn == nil {
				continue
			}
			fmt.Fprintf(&sb, "- [%s] %s — ID: %s\n", rn.Type, rn.SymbolName, rn.Id)
			if rn.Description != "" {
				fmt.Fprintf(&sb, "  %s\n", truncate(rn.Description, 120))
			}
		}
	}
	renderLanguagePatternsSection(node, languagePatterns, &sb)
	return kgtools.TextResult(sb.String())
}

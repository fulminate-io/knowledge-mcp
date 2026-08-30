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

// assemblePlan renders a NodePlan: header + subtree walk + Linked
// Research + Language patterns. Mirrors the server-side shape at
// cmd/knowledge-server/tools/tools_assemble.go:164 (renamed from
// assembleProject — the server used the legacy name).
//
// Ported as a free function with store reads swapped for wire-shape
// calls. The tree rides AssembleSubtree's two batched wire calls rather
// than a per-node walk, so its cost is independent of plan size.
func assemblePlan(ctx context.Context, gc GraphCaller, node *knowledgev1.Node) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Plan: %s\n\n", node.SymbolName)
	childIndex, byID, dependsOn, truncated := AssembleSubtree(ctx, gc, node.Id, 4)
	RenderTreeFromIndex(&sb, node, 0, 4, childIndex, dependsOn)

	// Follow EdgeInformedBy edges to linked research, and EdgeAudits
	// edges to language patterns. One IterEdges call (no type
	// filter) so we can dispatch on edge type below, and ONE bulk hydrate
	// shared by both sections rather than a fetch per target.
	outEdges, _ := IterEdges(ctx, gc, node.Id, kgwire.OutgoingEdges)
	var researchIDs, languagePatternIDs []string
	for _, e := range outEdges {
		switch kgtypes.EdgeType(e.Type) {
		case kgtypes.EdgeInformedBy:
			researchIDs = append(researchIDs, e.ToId)
		case kgtypes.EdgeAudits:
			languagePatternIDs = append(languagePatternIDs, e.ToId)
		}
	}
	linked, linkedTruncated, _ := FetchNodesByIDs(ctx, gc, append(append([]string{}, researchIDs...), languagePatternIDs...))
	truncated = truncated || linkedTruncated

	if len(researchIDs) > 0 {
		fmt.Fprintf(&sb, "\n## Linked Research\n\n")
		// Walk the ID slice, never the hydrated map: the slice carries edge
		// order and a map range would reorder this section at random.
		for _, rid := range researchIDs {
			rn, ok := linked[rid]
			if !ok {
				continue
			}
			fmt.Fprintf(&sb, "- [%s] %s — ID: %s\n", rn.Type, rn.SymbolName, rn.Id)
			if rn.Description != "" {
				fmt.Fprintf(&sb, "  %s\n", truncate(rn.Description, 120))
			}
		}
	}
	var languagePatterns []*knowledgev1.Node
	for _, lid := range languagePatternIDs {
		if lp, ok := linked[lid]; ok {
			languagePatterns = append(languagePatterns, lp)
		}
	}
	renderLanguagePatternsSection(node, languagePatterns, &sb)
	return AppendTruncationNotice(kgtools.TextResult(sb.String()), truncated, len(byID)+len(linked))
}

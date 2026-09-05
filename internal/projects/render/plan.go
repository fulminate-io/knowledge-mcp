// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"

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
func assemblePlan(ctx context.Context, gc GraphCaller, node *knowledgev1.Node, sectionStart, sectionEnd *int) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Plan: %s\n\n", node.SymbolName)
	childIndex, byID, dependsOn, truncated := AssembleSubtree(ctx, gc, node.Id, 4)
	// A sectioned plan's sections carry reviewer annotations, which hang off the
	// section by relates-to and so are invisible to the contains traversal above.
	// Two extra wire calls, and only when the plan HAS sections: a plan with none
	// takes the empty-input short-circuit and issues neither.
	annotations, annotationsTruncated, aerr := FetchSectionAnnotations(ctx, gc, SectionIDsOf(byID))
	if aerr != nil {
		// Degrade rather than fail the whole assemble, matching what
		// AssembleSubtree's two calls already do — the tree is still worth
		// rendering — but the verdict rides out so the caller is told the render
		// is incomplete rather than told there are no annotations.
		slog.Warn("assemble plan: annotation read failed; rendering without annotation lines", "id", node.Id, "error", aerr)
		annotations = nil
	}
	truncated = truncated || annotationsTruncated
	RenderTreeFromIndex(&sb, node, 0, 4, childIndex, dependsOn, AnnotationLines(annotations))

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
	linked, linkedTruncated, _ := foundation.FetchNodesByIDs(ctx, gc, "", "", append(append([]string{}, researchIDs...), languagePatternIDs...), foundation.IncludeTombstones)
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
	renderSectionIndex(&sb, childIndex[node.Id], annotations)
	// The section BODIES ride only when the caller asked for a range. Without one
	// the assemble stays the index-plus-tree read it has always been, which is
	// what keeps it a few hundred bytes on a plan of any size; with one it returns
	// exactly the pages asked for.
	if sectionStart != nil || sectionEnd != nil {
		if err := writeSectionRange(&sb, childIndex[node.Id], annotations, sectionStart, sectionEnd); err != nil {
			return kgtools.ErrorResult(err.Error())
		}
	}
	// TWO DISCLOSURES, TWO CAUSES. The truncation notice speaks for a server row
	// ceiling; the annotation-failure notice speaks for a read that errored. They
	// are separate because a caller acts on them differently and because the
	// truncation notice's remedy — a smaller `limit` — is not even a parameter
	// this tool accepts.
	out := AppendTruncationNotice(kgtools.TextResult(sb.String()), truncated, len(byID)+len(linked))
	return AppendAnnotationReadFailureNotice(out, aerr)
}

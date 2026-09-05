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

// assembleResearch renders a NodeResearch: header + subtree walk +
// per-question Findings section + Resulting Decisions section.
//
// Ported from cmd/knowledge-server/tools/tools_assemble.go:287 with
// store reads swapped for wire-shape calls. The tree and the question
// nodes both come out of AssembleSubtree's single traversal, so neither
// costs a per-node fetch.
func assembleResearch(ctx context.Context, gc GraphCaller, node *knowledgev1.Node) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Research: %s\n\n", node.SymbolName)
	childIndex, byID, dependsOn, truncated := AssembleSubtree(ctx, gc, node.Id, 3)
	RenderTreeFromIndex(&sb, node, 0, 3, childIndex, dependsOn, nil)

	// The questions are the node's contains children, already hydrated by the
	// traversal. Their findings arrive via reverse EdgeAnswers — ONE set-form
	// edge read over every question id rather than one read per question, and
	// one bulk hydrate over every finding rather than one fetch per edge.
	var questions []*knowledgev1.Node
	for _, qn := range childIndex[node.Id] {
		if kgtypes.NodeType(qn.Type) == kgtypes.NodeQuestion {
			questions = append(questions, qn)
		}
	}
	questionIDs := make([]string, 0, len(questions))
	for _, qn := range questions {
		questionIDs = append(questionIDs, qn.Id)
	}
	// EdgeAnswers: finding → question, so the edge ENTERS the question.
	answerEdges, _ := IterEdgesFor(ctx, gc, questionIDs, kgwire.IncomingEdges, kgtypes.EdgeAnswers)
	findingsByQuestion := make(map[string][]string, len(questions))
	findingIDs := make([]string, 0, len(answerEdges))
	for _, e := range answerEdges {
		findingsByQuestion[e.ToId] = append(findingsByQuestion[e.ToId], e.FromId)
		findingIDs = append(findingIDs, e.FromId)
	}

	// Decisions linked via EdgeInformedBy (incoming to this research node).
	inEdges, _ := IterEdges(ctx, gc, node.Id, kgwire.IncomingEdges, kgtypes.EdgeInformedBy)
	decisionIDs := make([]string, 0, len(inEdges))
	for _, e := range inEdges {
		decisionIDs = append(decisionIDs, e.FromId)
	}

	linked, linkedTruncated, _ := foundation.FetchNodesByIDs(ctx, gc, "", "", append(findingIDs, decisionIDs...), foundation.IncludeTombstones)
	truncated = truncated || linkedTruncated

	var hasFinding bool
	for _, qn := range questions {
		fids := findingsByQuestion[qn.Id]
		if len(fids) == 0 {
			continue
		}
		if !hasFinding {
			fmt.Fprintf(&sb, "\n## Findings\n\n")
			hasFinding = true
		}
		fmt.Fprintf(&sb, "### Q: %s\n", qn.SymbolName)
		for _, fid := range fids {
			fn, ok := linked[fid]
			if !ok {
				continue
			}
			fmt.Fprintf(&sb, "  - [%s] %s — ID: %s\n", fn.Type, fn.SymbolName, fn.Id)
			if fn.Description != "" {
				fmt.Fprintf(&sb, "    %s\n", truncate(fn.Description, 120))
			}
		}
	}

	var decisions []*knowledgev1.Node
	for _, did := range decisionIDs {
		if dn, ok := linked[did]; ok && kgtypes.NodeType(dn.Type) == kgtypes.NodeDecision {
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
	return AppendTruncationNotice(kgtools.TextResult(sb.String()), truncated, len(byID)+len(linked))
}

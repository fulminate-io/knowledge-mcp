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

// assembleDecision renders a NodeDecision: header + choice/rationale/
// alternatives + ID, then `## Informed By` (outgoing EdgeInformedBy)
// and `## Supporting Evidence` (incoming EdgeSupports) sections.
//
// Ported from cmd/knowledge-server/tools/tools_assemble_extra.go:21
// with the store reads swapped for wire-shape FetchNode + IterEdges
// calls.
func assembleDecision(ctx context.Context, gc GraphCaller, node *knowledgev1.Node) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Decision: %s\n\n", node.SymbolName)
	if node.Description != "" {
		fmt.Fprintf(&sb, "%s\n\n", node.Description)
	}
	if choice := kgtypes.Value(node, "choice"); choice != "" {
		fmt.Fprintf(&sb, "**Choice:** %s\n", choice)
	}
	if rationale := kgtypes.Value(node, "rationale"); rationale != "" {
		fmt.Fprintf(&sb, "**Rationale:** %s\n", rationale)
	}
	if alts := kgtypes.Value(node, "alternatives"); alts != "" {
		fmt.Fprintf(&sb, "**Alternatives:** %s\n", alts)
	}
	fmt.Fprintf(&sb, "ID: %s%s\n", node.Id, updatedSuffix(node))

	// Follow EdgeInformedBy to findings/research.
	outEdges, _ := IterEdges(ctx, gc, node.Id, kgwire.OutgoingEdges, kgtypes.EdgeInformedBy)
	var informed []*knowledgev1.Node
	for _, e := range outEdges {
		in, err := FetchNode(ctx, gc, e.ToId)
		if err == nil && in != nil {
			informed = append(informed, in)
		}
	}
	if len(informed) > 0 {
		fmt.Fprintf(&sb, "\n## Informed By\n\n")
		for _, n := range informed {
			fmt.Fprintf(&sb, "- [%s] %s — ID: %s\n", n.Type, n.SymbolName, n.Id)
		}
	}

	// Follow EdgeSupports (incoming: evidence → decision).
	inEdges, _ := IterEdges(ctx, gc, node.Id, kgwire.IncomingEdges, kgtypes.EdgeSupports)
	var supporting []*knowledgev1.Node
	for _, e := range inEdges {
		sn, err := FetchNode(ctx, gc, e.FromId)
		if err == nil && sn != nil {
			supporting = append(supporting, sn)
		}
	}
	if len(supporting) > 0 {
		fmt.Fprintf(&sb, "\n## Supporting Evidence\n\n")
		for _, s := range supporting {
			fmt.Fprintf(&sb, "- [%s] %s — ID: %s\n", s.Type, s.SymbolName, s.Id)
		}
	}
	return kgtools.TextResult(sb.String())
}

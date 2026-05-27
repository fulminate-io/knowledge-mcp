// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// assembleFallback renders an unknown / generic node type as a
// header + description + ID + outgoing/incoming edge tables. Used
// for rule, document, finding, and any node whose type doesn't
// have a dedicated renderer.
//
// Ported from cmd/knowledge-server/tools/tools_assemble_extra.go:80
// with the store reads swapped for wire-shape FetchNode + IterEdges
// calls.
func assembleFallback(ctx context.Context, gc GraphCaller, node *knowledgev1.Node) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s: %s\n", node.Type, node.SymbolName)
	if node.Description != "" {
		fmt.Fprintf(&sb, "%s\n", node.Description)
	}
	fmt.Fprintf(&sb, "ID: %s\n", node.Id)

	outEdges, _ := IterEdges(ctx, gc, node.Id, kgwire.OutgoingEdges)
	if len(outEdges) > 0 {
		fmt.Fprintf(&sb, "\n## Outgoing Edges\n\n")
		for _, e := range outEdges {
			tgt, err := FetchNode(ctx, gc, e.ToId)
			if err != nil || tgt == nil {
				fmt.Fprintf(&sb, "- [%s] → %s\n", e.Type, e.ToId)
				continue
			}
			fmt.Fprintf(&sb, "- [%s] → [%s] %s (ID: %s)\n", e.Type, tgt.Type, tgt.SymbolName, tgt.Id)
		}
	}

	inEdges, _ := IterEdges(ctx, gc, node.Id, kgwire.IncomingEdges)
	if len(inEdges) > 0 {
		fmt.Fprintf(&sb, "\n## Incoming Edges\n\n")
		for _, e := range inEdges {
			src, err := FetchNode(ctx, gc, e.FromId)
			if err != nil || src == nil {
				fmt.Fprintf(&sb, "- [%s] ← %s\n", e.Type, e.FromId)
				continue
			}
			fmt.Fprintf(&sb, "- [%s] ← [%s] %s (ID: %s)\n", e.Type, src.Type, src.SymbolName, src.Id)
		}
	}
	return kgtools.TextResult(sb.String())
}

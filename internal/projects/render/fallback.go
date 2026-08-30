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
// with the store reads swapped for wire-shape calls. Every peer named by an
// edge is hydrated in ONE bulk read rather than one read per edge, and the
// rendering loops walk the EDGE slices — never the hydrated map, whose
// iteration order is undefined and would reorder these sections at random.
//
// AN UNRESOLVED PEER STILL RENDERS ITS SHORTER RAW-ID LINE. The condition used
// to be a FetchNode error and is now a miss in the hydrated map; the two output
// shapes, resolved and unresolved, both survive.
func assembleFallback(ctx context.Context, gc GraphCaller, node *knowledgev1.Node) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s: %s\n", node.Type, node.SymbolName)
	if node.Description != "" {
		fmt.Fprintf(&sb, "%s\n", node.Description)
	}
	fmt.Fprintf(&sb, "ID: %s%s\n", node.Id, updatedSuffix(node))

	outEdges, _ := IterEdges(ctx, gc, node.Id, kgwire.OutgoingEdges)
	inEdges, _ := IterEdges(ctx, gc, node.Id, kgwire.IncomingEdges)

	peerIDs := make([]string, 0, len(outEdges)+len(inEdges))
	for _, e := range outEdges {
		peerIDs = append(peerIDs, e.ToId)
	}
	for _, e := range inEdges {
		peerIDs = append(peerIDs, e.FromId)
	}
	peers, truncated, _ := FetchNodesByIDs(ctx, gc, peerIDs)

	if len(outEdges) > 0 {
		fmt.Fprintf(&sb, "\n## Outgoing Edges\n\n")
		for _, e := range outEdges {
			tgt, ok := peers[e.ToId]
			if !ok {
				fmt.Fprintf(&sb, "- [%s] → %s\n", e.Type, e.ToId)
				continue
			}
			fmt.Fprintf(&sb, "- [%s] → [%s] %s (ID: %s)\n", e.Type, tgt.Type, tgt.SymbolName, tgt.Id)
		}
	}

	if len(inEdges) > 0 {
		fmt.Fprintf(&sb, "\n## Incoming Edges\n\n")
		for _, e := range inEdges {
			src, ok := peers[e.FromId]
			if !ok {
				fmt.Fprintf(&sb, "- [%s] ← %s\n", e.Type, e.FromId)
				continue
			}
			fmt.Fprintf(&sb, "- [%s] ← [%s] %s (ID: %s)\n", e.Type, src.Type, src.SymbolName, src.Id)
		}
	}
	// This arm renders no contains tree, so the bulk hydrate's verdict is the
	// only one it ever receives. A clamped hydrate would turn resolved peers
	// into raw-id lines without saying so.
	return AppendTruncationNotice(kgtools.TextResult(sb.String()), truncated, len(peers))
}

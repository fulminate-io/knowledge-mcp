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

// assembleProjectContainer renders a NodeProject: header + Progress
// (X/Y tickets done) + Tickets section. Mirrors the server-side
// shape at cmd/knowledge-server/tools/tools_assemble_containers.go:318
// with store reads swapped for wire-shape calls.
//
// Status canonicalization for the progress count: tickets canonically
// use StatusClosed; StatusCompleted is accepted as a fallback for
// legacy data; StatusSkipped counts as not-pending so a
// deferred-and-skipped ticket doesn't make the project look stuck.
func assembleProjectContainer(ctx context.Context, gc GraphCaller, node *knowledgev1.Node) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Project: %s\n\n", node.SymbolName)
	renderProjectHeader(node, &sb)

	// Walk contains edges to find child tickets.
	childEdges, _ := IterEdges(ctx, gc, node.Id, kgwire.OutgoingEdges, kgtypes.EdgeKGContains)
	var tickets []*knowledgev1.Node
	for _, e := range childEdges {
		cn, err := FetchNode(ctx, gc, e.ToId)
		if err != nil || cn == nil {
			continue
		}
		if kgtypes.NodeType(cn.Type) == kgtypes.NodeTicket {
			tickets = append(tickets, cn)
		}
	}

	total := len(tickets)
	done := 0
	for _, t := range tickets {
		switch t.Status {
		case kgtypes.StatusClosed, kgtypes.StatusCompleted, kgtypes.StatusSkipped:
			done++
		}
	}
	if total > 0 {
		fmt.Fprintf(&sb, "\n**Progress:** %d/%d tickets done\n", done, total)
	}

	sb.WriteString(renderProjectTickets(ctx, gc, tickets))

	return kgtools.TextResult(sb.String())
}

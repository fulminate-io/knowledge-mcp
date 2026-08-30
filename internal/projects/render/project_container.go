// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
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

	// Depth 2: the child tickets are depth 1 and the plans and research the
	// Tickets section counts are depth 2. Nothing this arm renders lives
	// deeper, and a larger depth would pull rows the render discards while
	// bringing the server's row ceiling closer.
	childIndex, byID, _, truncated := AssembleSubtree(ctx, gc, node.Id, 2)

	var tickets []*knowledgev1.Node
	for _, cn := range childIndex[node.Id] {
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

	sb.WriteString(renderProjectTickets(tickets, childIndex))

	// A clamped traversal silently shortens the rendered ticket list — which
	// looks exactly like a small project. Disclose it.
	return AppendTruncationNotice(kgtools.TextResult(sb.String()), truncated, len(byID))
}

// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// graphCounts fetches the node/edge counts for one named graph via the Stats RPC.
//
// IT RETURNS THE ERROR RATHER THAN DEGRADING TO ZEROS, and that is the whole
// point of this file. It used to answer (0, 0) on ANY failure, which rendered
// byte-identically to a genuinely empty graph — so a graph the client could not
// READ was reported as a graph with nothing IN it. That inverted the diagnosis of
// a live incident: an unreachable graph read as data loss.
func graphCounts(ctx context.Context, gc statsRPC, graph, name string) (int, int, error) {
	resp, err := gc.Stats(ctx, &knowledgev1.StatsRequest{
		// Route the instance name into the right selector field per graph family
		// (practice→Language, cloud/cicd→Account, else→Name) — resourceTarget is
		// Account-only and silently zeroed practice/other counts.
		Target: graphsel.GraphSelectorFor(kgtypes.GraphType(graph), name, false),
	})
	if err != nil {
		return 0, 0, err
	}
	stats := resp.GetGraphStats()
	return int(stats.GetNodeCount()), int(stats.GetEdgeCount()), nil
}

// graphCountRow renders ONE listing row for a named graph: its counts when they
// could be read, and a read failure naming the error when they could not.
//
// It is shared by all three listings so they cannot disagree about how an
// unreadable graph reads. An unmeasured value is never rendered as a
// measurement.
func graphCountRow(ctx context.Context, gc statsRPC, graph, name string) string {
	nodes, edges, err := graphCounts(ctx, gc, graph, name)
	if err != nil {
		return fmt.Sprintf("- **%s** — COUNT UNAVAILABLE: %v\n", name, err)
	}
	return fmt.Sprintf("- **%s** — %d nodes, %d edges\n", name, nodes, edges)
}

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
	// Route the instance name into the right selector field per graph family —
	// graphsel.InstanceField's answer and nowhere else's. The hand-built selector
	// this replaced was account-only and silently zeroed every other family's
	// counts.
	return graphCountsFor(ctx, gc, graphsel.GraphSelectorFor(kgtypes.GraphType(graph), name, false))
}

// graphCountsFor is graphCounts against a CALLER-BUILT target.
//
// IT EXISTS FOR THE ONE READ THAT ADDRESSES A GRAPH graphsel WILL NOT ADDRESS:
// the legacy practice enumeration, which lists the pre-singleton graphs by name.
// Practice is a singleton now, so deriving its target puts no instance field on
// the selector and every legacy row would report the COMBINED graph's counts
// under a different graph's name — the same number repeated down the table,
// which reads as a working enumeration.
func graphCountsFor(ctx context.Context, gc statsRPC, target *knowledgev1.GraphSelector) (int, int, error) {
	resp, err := gc.Stats(ctx, &knowledgev1.StatsRequest{Target: target})
	if err != nil {
		return 0, 0, err
	}
	stats := resp.GetGraphStats()
	return int(stats.GetNodeCount()), int(stats.GetEdgeCount()), nil
}

// graphCountRowFor renders ONE listing row for a named graph against a
// CALLER-BUILT target: its counts when they could be read, and a read failure
// naming the error when they could not. An unmeasured value is never rendered as
// a measurement.
//
// IT IS THE ONLY ROW RENDERER LEFT, and the derive-your-own-target twin beside it
// is gone rather than kept inert. Three listings shared that twin — the legacy
// practice enumeration and the two per-account resource listings — and both
// resource listings went with the account-keyed families they served, leaving the
// one caller that cannot derive its target at all (see graphCountsFor). A second
// listing that CAN derive one re-adds the twin along with the reason for it.
func graphCountRowFor(ctx context.Context, gc statsRPC, label string, target *knowledgev1.GraphSelector) string {
	nodes, edges, err := graphCountsFor(ctx, gc, target)
	if err != nil {
		return fmt.Sprintf("- **%s** — COUNT UNAVAILABLE: %v\n", label, err)
	}
	return fmt.Sprintf("- **%s** — %d nodes, %d edges\n", label, nodes, edges)
}

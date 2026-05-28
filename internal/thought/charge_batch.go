// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// chargeMapForThoughts wraps fetchChargesFor as a single source of
// truth for the bulk per-thought charge map. Issues EXACTLY ONE
// gc.Call("thoughts", {operation:"charges_for"}) regardless of
// len(thoughtIDs) — load-bearing invariant the Phase 8 acceptance
// gate pins.
//
// Cost shape: 1 wire round trip. Server-side the call resolves N
// IterEdges (per-thought) + 1 IterateAll(NodeCharge) sweep, returning
// {thoughtID: []charge_node}. Missing/tombstoned charges are silently
// dropped at the server side.
//
// Empty thoughtIDs short-circuits — no wire call — to keep callers
// cheap when a cluster/component has no thoughts (T3-C advisory).
func chargeMapForThoughts(ctx context.Context, gc *graphclient.GraphClient, thoughtIDs []string) map[string][]*knowledgev1.Node {
	if len(thoughtIDs) == 0 {
		return map[string][]*knowledgev1.Node{}
	}
	return fetchChargesFor(ctx, gc, thoughtIDs)
}

// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// chargeMapForThoughts wraps fetchChargesFor as a single source of
// truth for the bulk per-thought charge map. Issues AT MOST ONE bulk
// EdgeChargedBy read regardless of len(thoughtIDs) — and, when src is the
// per-pass memo and an earlier stage already built the map, NONE at all, because
// every consumer in one pass shares that single composition (see passReads).
//
// Cost shape: 1 wire round trip on the first composition of a pass. The charge
// hydrate it drives comes off the resident charge snapshot with a residual-only
// wire read. Missing/tombstoned charges are silently dropped.
//
// Empty thoughtIDs short-circuits — no wire call — to keep callers
// cheap when a cluster/component has no thoughts (T3-C advisory).
func chargeMapForThoughts(ctx context.Context, gc Caller, thoughtIDs []string, src CorpusSource) map[string][]*knowledgev1.Node {
	if len(thoughtIDs) == 0 {
		return map[string][]*knowledgev1.Node{}
	}
	return fetchChargesFor(ctx, gc, thoughtIDs, src)
}

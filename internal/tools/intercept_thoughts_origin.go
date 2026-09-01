// SPDX-License-Identifier: Apache-2.0

// intercept_thoughts_origin.go — developer-origin resolution for
// thoughts(operation:think). The origin param names the agent ROLE that
// recorded a thought; this file resolves that role to an agent node whose
// SymbolName matches (e.g. "planner") so the think path can ride an
// agent--produced-->thought hub edge.
//
// THE AGENT NODES ARE user-authored. NOTHING CREATES THEM. There is no
// seeding path in this product: a graph acquires agent nodes only if someone
// writes them, so a graph that carries none — which is every fresh graph —
// resolves every origin to "" and stamps origin metadata alone. That is the
// documented degrade below, reached by the ordinary route rather than by an
// error, and it is why this file's failure mode is silence rather than a
// stopped write.
//
// Resolution is one DRAINED NodeAgent browse (clientthought.DrainThoughtBrowse,
// NOT a capped limit:0 query — a limit<=0 browse is silently rewritten to the
// engine's browseDefaultLimit=10, which would miss agent nodes past the tenth).
// A graph can carry DUPLICATE agent SymbolNames (several nodes authored under
// one role name), so the name->id map applies the established deterministic
// tie-break: collect every id for a name, sort.Strings, take the lowest
// (ids[0]). buildAgentNameToID is the map builder used by the think-path
// resolver; it surfaces a collision count (names with >1 id) for diagnostics.
//
// This file resolves only the agent-ROLE facet (origin). The companion
// human-author facet is stamped server-side from the writing user's identity
// when one is present in the request, NOT by this client: the client think path
// deliberately adds NO author stamp of its own. A self-hosted/local server has no
// end-user identity concept, so a think there gets no author stamp — by design,
// not a gap.
//
// Graceful degrade is the contract: an origin that resolves to no agent node
// ("main", "orchestrator", "reviewer", or any value with no matching node)
// returns "" — the caller stamps origin metadata only and logs at Debug, never
// blocking the think create.

package tools

import (
	"context"
	"sort"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// resolveOriginAgentID resolves a developer-origin role to the matching agent
// node id, or "" when no agent node carries that SymbolName. An empty origin
// normalizes to "main" (the conventional default), which conventionally has no
// agent node and so degrades to "" — metadata-only, no hub edge. The lookup is one
// drained NodeAgent browse via buildAgentNameToID; on duplicate SymbolNames the
// lowest id wins (deterministic across runs regardless of browse order).
func resolveOriginAgentID(ctx context.Context, gc GraphCaller, origin string) string {
	if origin == "" {
		origin = "main"
	}
	nameToID, _, err := buildAgentNameToID(ctx, gc)
	if err != nil {
		// Resolution is best-effort: a browse failure degrades to metadata-only,
		// never blocking the think create. The caller logs at Debug.
		return ""
	}
	return nameToID[origin] // "" when no agent node carries this SymbolName.
}

// backfillBrowsePageSize is the per-page row count for the corpus-complete
// browse drains. A positive limit bypasses the engine's limit<=0 →
// browseDefaultLimit(10) cap (compile_query.go:357), which is exactly the silent
// cap these drains exist to defeat. Mirrors the thought package's
// browsePageSize (package-private there); the same 500 ≈ a low RPC count with a
// bounded per-page payload.
const backfillBrowsePageSize = 500

// buildAgentNameToID drains every NodeAgent once and builds a deterministic
// SymbolName->id map plus the count of colliding names (SymbolNames with more
// than one agent node). On a collision the lowest id wins: collect all ids for
// the name, sort.Strings, take ids[0] — a deterministic lowest-id tie-break.
// Used by resolveOriginAgentID (think path) to resolve agent names.
func buildAgentNameToID(ctx context.Context, gc GraphCaller) (map[string]string, int, error) {
	agents, err := clientthought.DrainThoughtBrowse(ctx, gc, string(kgtypes.NodeAgent), backfillBrowsePageSize)
	if err != nil {
		return nil, 0, err
	}
	byName := map[string][]string{} // agent SymbolName -> all node ids with that name.
	for _, a := range agents {
		byName[a.SymbolName] = append(byName[a.SymbolName], a.Id)
	}
	nameToID := make(map[string]string, len(byName))
	collisions := 0
	for name, ids := range byName {
		sort.Strings(ids)
		nameToID[name] = ids[0] // lowest id — deterministic tie-break.
		if len(ids) > 1 {
			collisions++
		}
	}
	return nameToID, collisions, nil
}

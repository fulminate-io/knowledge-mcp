// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"fmt"
	"sort"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// wire_adjacency.go holds the client-side composition of the whole-graph
// adjacency map — the reduction of thoughts(adjacency) off the raw
// gc.Call onto the generic Execute seam. It reproduces the server's
// handleAdjacency (a PURE topology.BuildAdjacency read) using a single bulk
// RETURN_MODE_EDGES read over the node set + (scope="all") ONE bulk
// EdgeKGContains read + client-side group-by-session sibling derivation,
// replacing both the N per-node neighbor walks the server ran internally AND the
// former per-thought session-sibling traversal.

// FetchThoughtAdjacency is the exported wrapper around fetchAdjacency
// for cmd/knowledge/internal/tools/ — InterceptThoughts needs the
// adjacency map to drive ReflectBlindSpots' bridge-detection pass
// without exporting fetchAdjacency itself (which is the lower-level
// helper the reflective bodies use internally).
func FetchThoughtAdjacency(ctx context.Context, gc Caller, src CorpusSource) ([]string, map[string][]string, error) {
	return fetchAdjacency(ctx, gc, "all", nil, src)
}

// FetchAdjacency is the exported wrapper that thoughts(adjacency) drives:
// it forwards the op's variable scope ("all" or "all_types") and optional
// thought_ids subset projection straight to fetchAdjacency, which validates
// the scope, does the ONE bulk edges read, runs session-sibling expansion for
// scope="all", and projects the subset. Kept distinct from FetchThoughtAdjacency
// (which hardcodes scope="all", subset=nil for the blind-spots fixed-shape call)
// so the op's variable shape and the reflection call stay un-conflated.
func FetchAdjacency(ctx context.Context, gc Caller, scope string, subset []string, src CorpusSource) ([]string, map[string][]string, error) {
	return fetchAdjacency(ctx, gc, scope, subset, src)
}

// adjacencyEdgeTypes is the scope="all" thought-cluster edge set the server's
// BuildAdjacencyOpts.EdgeTypes carries (tools_thought_adjacency.go:66-72).
var adjacencyEdgeTypes = []kgtypes.EdgeType{
	kgtypes.EdgeNext,
	kgtypes.EdgeBranchesFrom,
	kgtypes.EdgeRelatesTo,
	kgtypes.EdgeProduced,
	kgtypes.EdgeBecause,
}

// sessionCliqueCap is the band HALF-WIDTH K bounding the per-session sibling
// expansion in deriveSessionSiblings. A session of N members would otherwise
// emit an O(N²) full clique; instead each member is connected only to the
// members within ±K positions of it in sorted (sort.Strings) order — a
// symmetric sliding band. Per-session edge count is O(K·N) instead of O(N²).
//
// HUB-FREE by construction: every interior member gets exactly 2K sibling
// edges and NO member exceeds 2K, so no member becomes a high-degree hub. This
// is the load-bearing property — a fully-connected-core scheme would instead
// concentrate ~N edges on the core members, making them artificial influence
// hubs (the centrality artifact this band avoids).
//
// K=50 is chosen empirically against the observed session-size distribution:
// the typical session holds 1-30 members (the everyday body of the corpus),
// while a heavy tail of long-running sessions reaches dozens-to-hundreds of
// members and is what drove cluster detection to its time budget. K=50 sits
// ABOVE the typical body and BELOW the pathological tail, so for sessions of
// ≤ K+1 (=51) members the band spans the whole set and degenerates to a full
// clique — an exact no-op (zero behavior change) — and only the runaway
// long-running sessions are bounded.
const sessionCliqueCap = 50

// fetchAdjacency composes the whole-graph adjacency map CLIENT-SIDE, reproducing
// the server's handleAdjacency (a PURE topology.BuildAdjacency read — zero
// valence/propagation compute) over the generic Execute seam. One bulk
// RETURN_MODE_EDGES read over the node set replaces the N per-node neighbor
// walks; scope="all" adds the session-sibling expansion via ONE further bulk
// EdgeKGContains read + client-side group-by-session (deriveSessionSiblings) —
// NO per-thought traversal.
//
// scope="all": NodeThought set, neighbors over the 5 thought-cluster edge types,
// plus the session-sibling expansion. scope="all_types": every node EXCEPT the
// NodeProxy/NodeAgent/NodeSkill hub types (keepInAllTypesIDSet), neighbors over
// ALL edge types, NO sibling expansion. Both scopes
// build neighbors BIDIRECTIONALLY (both endpoints of each incident edge): the
// server's collectNeighbors unions forward+backward per type for "all", and
// issues store.From(id).IDs() — which with forward==nil walks BOTH directions
// (store/query.go:83) — for "all_types". Both filter neighbors to the in-scope
// idSet. subset is the optional post-walk projection.
// src: a warm CorpusSource serves the scope="all" thought-node arm from
// the resident cache; nil/cold and scope="all_types" always drain (all_types spans
// every node type, not the thought-only cache). Edges are read once PER PASS when
// src is the per-pass memo (memoAdjacencyAll) and once per call otherwise; they are
// never cached across passes.
//
// scope="all" is served through memoAdjacencyAll, which holds the UNPROJECTED
// (nodeIDs, adj) pair for the pass, so a subset request is the same composition
// followed by projectAdjacencySubset — the last thing this function does either way.
// scope="all_types" is NEVER memoized: it spans every node type rather than the
// thought-only corpus, so it stays on the uncached path.
func fetchAdjacency(ctx context.Context, gc Caller, scope string, subset []string, src CorpusSource) ([]string, map[string][]string, error) {
	if gc == nil {
		return nil, nil, nil
	}
	switch scope {
	case "all", "all_types":
	case "":
		return nil, nil, fmt.Errorf("thoughts(adjacency): 'scope' is required (want 'all' or 'all_types')")
	default:
		return nil, nil, fmt.Errorf("thoughts(adjacency): unknown scope %q (want 'all' or 'all_types')", scope)
	}

	if scope == "all" {
		nodeIDs, adj, err := memoAdjacencyAll(ctx, gc, src)
		if err != nil {
			return nil, nil, err
		}
		nodeIDs, adj = projectAdjacencySubset(nodeIDs, adj, subset)
		return nodeIDs, adj, nil
	}

	nodeIDs, adj, err := fetchAdjacencyUncached(ctx, gc, scope, src)
	if err != nil {
		return nil, nil, err
	}
	nodeIDs, adj = projectAdjacencySubset(nodeIDs, adj, subset)
	return nodeIDs, adj, nil
}

// fetchAdjacencyAllUncached is the scope="all" composition memoAdjacencyAll runs on
// a miss (and the whole read when there is no memo). Named separately from the
// scope-generic body below because the memo only ever serves scope="all".
func fetchAdjacencyAllUncached(ctx context.Context, gc Caller, src CorpusSource) ([]string, map[string][]string, error) {
	return fetchAdjacencyUncached(ctx, gc, "all", src)
}

// fetchAdjacencyUncached is the adjacency composition itself — the node-id fetch,
// the ONE bulk edges read, and (scope="all") the session-sibling union. It is the
// SINGLE implementation both the memo path and the uncached path take, so a memoized
// pass and an unmemoized one compose adjacency identically. The caller owns the
// subset projection.
func fetchAdjacencyUncached(ctx context.Context, gc Caller, scope string, src CorpusSource) ([]string, map[string][]string, error) {
	nodeIDs, err := fetchAdjacencyNodeIDs(ctx, gc, scope, src)
	if err != nil {
		return nil, nil, err
	}
	idSet := make(map[string]bool, len(nodeIDs))
	for _, id := range nodeIDs {
		idSet[id] = true
	}

	// ONE bulk edges read over the whole node set (the N+1-avoidance). scope="all"
	// filters to the 5 thought-cluster edge types; scope="all_types" reads all.
	var edgeFilter []kgtypes.EdgeType
	if scope == "all" {
		edgeFilter = adjacencyEdgeTypes
	}
	edges, err := fetchEdgesForNodeSet(ctx, gc, nodeIDs, edgeFilter)
	if err != nil {
		return nil, nil, err
	}

	adj := buildAdjacencyFromEdges(edges, idSet)

	// scope="all": session-sibling expansion via ONE bulk EdgeKGContains read +
	// pure client-side group-by-session (deriveSessionSiblings), regardless of
	// thought count — replacing the per-thought 2N traversal that was the dominant
	// reflection cost. scope="all_types" runs NO expansion.
	if scope == "all" {
		sibAdj := deriveSessionSiblings(ctx, gc, nodeIDs, idSet, src)
		for id, sibs := range sibAdj {
			adj[id] = append(adj[id], sibs...)
		}
	}

	return nodeIDs, adj, nil
}

// fetchAdjacencyNodeIDs returns the in-scope node-ID set: every NodeThought
// (scope="all") or every node except NodeProxy/NodeAgent/NodeSkill
// (scope="all_types", via keepInAllTypesIDSet). The all_types browse drains every
// node type via a hand-built empty-Selection plan in bounded offset pages and
// drops the excluded hub types client-side. Paging is required for scale: the
// hand-built plan runs
// directly through gc.Execute (bypassing applyBrowseLimitOffset), and the
// server's applyNodePage caps only when Limit>0 — so an unset Limit returns the
// WHOLE node set in one read. Setting Limit/Offset per page and draining bounds
// that read.
func fetchAdjacencyNodeIDs(ctx context.Context, gc Caller, scope string, src CorpusSource) ([]string, error) {
	if scope == "all" {
		nodes, err := fetchAllThoughtNodes(ctx, gc, src)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(nodes))
		for _, n := range nodes {
			ids = append(ids, n.Id)
		}
		return ids, nil
	}
	// all_types: drain every node type in bounded id-KEYSET pages (shared drainPages
	// cursor/termination/dedup core), then drop NodeProxy client-side. AfterId is
	// SET on every page — including page 1, where it is the empty string — because
	// presence, not value, is what selects the keyset browse; an omitted field would
	// leave this untyped Selection{} plan on the default browse order and make the
	// cursor taken from page 1 skip every lower id. Offset is never set: the two are
	// mutually exclusive and the server rejects a plan carrying both.
	// SkipTotal: this drain reads only n.Id/n.Type (keepInAllTypesIDSet) and never
	// Total, so — like drainThoughtBrowse — it drops the per-page paginating COUNT
	// on the single-layer path.
	nodes, err := drainPages(func(afterID string) ([]*knowledgev1.Node, error) {
		cursor := afterID
		resp, derr := gc.Execute(ctx, &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				Selection: &knowledgev1.Selection{},
				Limit:     int32(browsePageSize),
				AfterId:   &cursor,
				SkipTotal: true,
			}},
		})
		if derr != nil {
			return nil, derr
		}
		return engine.DecodeNodes(resp)
	}, browsePageSize)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if !keepInAllTypesIDSet(kgtypes.NodeType(n.Type)) {
			continue
		}
		ids = append(ids, n.Id)
	}
	return ids, nil
}

// keepInAllTypesIDSet is the all_types adjacency id-set membership predicate:
// it reports whether a node type may enter the cross-type clustering adjacency.
// NodeProxy is excluded (linkage scaffolding, never a real cluster member), and
// NodeAgent + NodeSkill are excluded because they are the developer-origin /
// instruction HUBS: an agent--produced-->thought (or skill) edge with both
// endpoints in the idSet would make every agent/skill node a clustering magnet,
// reinforcing genre/instruction clustering — exactly the contamination the
// origin facet must NOT leak into clustering. Dropping these ids here removes the
// hub edges STRUCTURALLY via buildAdjacencyFromEdges' both-endpoints test,
// independent of which edge types are read. Production (fetchAdjacencyNodeIDs)
// and the guard test bind to THIS function so the test exercises the real drop.
func keepInAllTypesIDSet(nodeType kgtypes.NodeType) bool {
	switch nodeType {
	case kgtypes.NodeProxy, kgtypes.NodeAgent, kgtypes.NodeSkill:
		return false
	default:
		return true
	}
}

// buildAdjacencyFromEdges projects the bulk edge set into the nodeID→neighbors
// map BIDIRECTIONALLY: each incident edge contributes its OTHER endpoint as a
// neighbor of BOTH endpoints (the server's collectNeighbors unions forward +
// backward for the typed "all" walk, and store.From(id).IDs() with forward==nil
// returns both directions for the "all_types" walk — store/query.go:83). Both
// endpoints must be in the in-scope idSet so no dangling references survive.
func buildAdjacencyFromEdges(edges []knowledgev1.Edge, idSet map[string]bool) map[string][]string {
	adj := make(map[string][]string, len(idSet))
	for i := range edges {
		e := &edges[i]
		if idSet[e.FromId] && idSet[e.ToId] {
			adj[e.FromId] = append(adj[e.FromId], e.ToId)
			adj[e.ToId] = append(adj[e.ToId], e.FromId)
		}
	}
	return adj
}

// deriveSessionSiblings reproduces the server thoughtAdjacencySessionSiblings
// SiblingExpander's contract (walk EdgeKGContains "in" to the enclosing session,
// then "out" to every co-member) for the WHOLE thought set from ONE bulk
// EdgeKGContains read + pure client-side group-by — replacing the former
// per-thought 2-traversal-per-thought session walk with a single node-SET
// RETURN_MODE_EDGES read regardless of N. Returns nodeID → session-sibling
// neighbors (to be unioned into the adjacency).
//
// EdgeKGContains is session(From)→thought(To). fetchEdgesForNodeSet's Forward=nil
// returns BOTH directions, so a session→thought edge surfaces even though the
// session node itself is not in nodeIDs (only its To endpoint, a thought, is).
// Grouping the thought endpoints by their session From-ID yields every session's
// member set; the symmetric sorted-band expansion among co-members (self-excluded,
// see the band loop below) is the sibling adjacency.
//
// POLLUTION GUARD: EdgeKGContains is ALSO the generic plan→phase→step containment
// edge, so the idSet[e.ToId] filter drops any non-thought member — a
// thought_session contains only thoughts, but the filter is robust regardless.
//
// src routes the read through memoKGContainsEdges, so the ONE bulk read this
// derivation issues is shared with FetchSessionLabelsByThought — the other consumer
// of the same edge set — instead of each paying for its own. A nil/non-memo src
// reads the wire exactly as before. A FAILED read still yields an empty sibling map
// (memoKGContainsEdges returns nil edges and memoizes nothing), so the best-effort
// contract below is unchanged.
func deriveSessionSiblings(ctx context.Context, gc Caller, nodeIDs []string, idSet map[string]bool, src CorpusSource) map[string][]string {
	sibAdj := make(map[string][]string)
	edges := memoKGContainsEdges(ctx, gc, nodeIDs, src)
	// Group thought members by their enclosing session (the From endpoint).
	bySession := make(map[string][]string)
	for i := range edges {
		e := &edges[i]
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeKGContains {
			continue
		}
		if idSet[e.ToId] { // pollution guard: only in-scope thought members.
			bySession[e.FromId] = append(bySession[e.FromId], e.ToId)
		}
	}
	// Per-session siblings via a SYMMETRIC SORTED BAND of half-width
	// sessionCliqueCap (K): instead of the full O(N²) clique, connect each member
	// only to the members within ±K positions of it in sorted order — O(K·N) edges.
	//
	// HUB-FREE (load-bearing): each interior member gets exactly 2K sibling edges
	// and NO member exceeds 2K, so degree is uniform and bounded — no member
	// becomes a high-degree hub. A fully-connected-core scheme would instead pile
	// ~N edges onto the core members, turning them into artificial influence hubs
	// (the centrality artifact this band is the fix for).
	//
	// DETERMINISM (load-bearing): sort.Strings(members) BEFORE banding. The band is
	// defined by sorted position, and the edge-read order is NOT a guaranteed-stable
	// function across ticks. Sorting the stable hex node IDs makes the band a
	// deterministic function of the member SET, so adjacency is byte-identical
	// across ticks — the invariant the incremental-clustering baseline and stable
	// cluster IDs depend on.
	//
	// SYMMETRY: |i-j| ≤ K is symmetric in i and j, so a→b implies b→a — the band is
	// undirected by construction.
	//
	// Sessions with ≤ K+1 members keep the full clique (the band is a no-op): the
	// band spans the whole member set (every |i-j| ≤ K), so the emitted edges are
	// identical to the old pairwise expansion. Within a band the members form a
	// connected chain, so a banded session stays one connected component.
	for _, members := range bySession {
		sort.Strings(members)
		for i := range members {
			lo := max(0, i-sessionCliqueCap)
			hi := min(len(members)-1, i+sessionCliqueCap)
			for j := lo; j <= hi; j++ {
				if j != i {
					sibAdj[members[i]] = append(sibAdj[members[i]], members[j])
				}
			}
		}
	}
	return sibAdj
}

// projectAdjacencySubset applies the optional thought_ids subset projection,
// matching handleAdjacency lines 90-107: filter nodeIDs to the requested set
// (preserving order) and keep only those entries in adj. A nil/empty subset is a
// no-op. adj is normalized to non-nil for the empty case.
func projectAdjacencySubset(nodeIDs []string, adj map[string][]string, subset []string) ([]string, map[string][]string) {
	if len(subset) == 0 {
		if adj == nil {
			adj = map[string][]string{}
		}
		return nodeIDs, adj
	}
	want := make(map[string]bool, len(subset))
	for _, id := range subset {
		want[id] = true
	}
	filteredIDs := make([]string, 0, len(subset))
	for _, id := range nodeIDs {
		if want[id] {
			filteredIDs = append(filteredIDs, id)
		}
	}
	filteredEdges := make(map[string][]string, len(filteredIDs))
	for _, id := range filteredIDs {
		filteredEdges[id] = adj[id]
	}
	return filteredIDs, filteredEdges
}

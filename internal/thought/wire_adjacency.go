// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"fmt"

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
func FetchThoughtAdjacency(ctx context.Context, gc Caller) ([]string, map[string][]string, error) {
	return fetchAdjacency(ctx, gc, "all", nil)
}

// FetchAdjacency is the exported wrapper that thoughts(adjacency) drives:
// it forwards the op's variable scope ("all" or "all_types") and optional
// thought_ids subset projection straight to fetchAdjacency, which validates
// the scope, does the ONE bulk edges read, runs session-sibling expansion for
// scope="all", and projects the subset. Kept distinct from FetchThoughtAdjacency
// (which hardcodes scope="all", subset=nil for the blind-spots fixed-shape call)
// so the op's variable shape and the reflection call stay un-conflated.
func FetchAdjacency(ctx context.Context, gc Caller, scope string, subset []string) ([]string, map[string][]string, error) {
	return fetchAdjacency(ctx, gc, scope, subset)
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

// tensionEdgeTypes is the set of EXPLICIT thought↔thought reasoning edges that
// count as a tension link. ReflectTensions pairs two thoughts only when an edge
// of one of these types joins them — bare co-session membership is NOT a tension.
// It mirrors the server's reflection-relevant thought↔thought edge set
// (composite_db_dirty_gen.go reflectionEdges) but is a deliberate per-module
// duplicate: the client cannot import the server store, exactly like the
// adjacencyEdgeTypes var above. Two reflectionEdges members are intentionally
// EXCLUDED — EdgeChargedBy and EdgeEvidencedBy join a thought to a CHARGE, not
// thought↔thought, so they can never form a tension pair. "contradicts" has no
// EdgeType constant (it is a documented mutate(link) relationship taken as-given
// on the wire), so it is keyed as the EdgeType("contradicts") string literal.
var tensionEdgeTypes = []kgtypes.EdgeType{
	kgtypes.EdgeNext,
	kgtypes.EdgeBranchesFrom,
	kgtypes.EdgeRelatesTo,
	kgtypes.EdgeProduced,
	kgtypes.EdgeBecause,
	kgtypes.EdgeSupports,
	kgtypes.EdgeType("contradicts"),
	kgtypes.EdgeInformedBy,
	kgtypes.EdgeSynthesizedFrom,
}

// isMachineTensionMethod reports whether an edge Method tag is one of the four
// MACHINE relates-to writer provenances — i.e. a programmatically densified or
// linked edge, never a human reasoning assertion. It references the EXISTING
// writer consts (treeLinkMethod tree_link.go, densifyMethod similarity_lever.go,
// topicSimilarityMethod similarity.go, artifactLinkMethod artifact_link_write.go)
// rather than re-spelling the string literals, so a writer-const rename breaks
// the build instead of silently slipping a machine edge back into the tension
// set. An empty Method (a human-authored mutate(link)) and any other tag are
// human → false. Used as the edge-slice pre-filter inside fetchTensionEdges:
// machine relates-to edges are clustering signal, not propositional disagreement,
// so they must never pair two thoughts as a tension.
func isMachineTensionMethod(method string) bool {
	switch method {
	case treeLinkMethod, densifyMethod, topicSimilarityMethod, artifactLinkMethod:
		return true
	default:
		return false
	}
}

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
func fetchAdjacency(ctx context.Context, gc Caller, scope string, subset []string) ([]string, map[string][]string, error) {
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

	nodeIDs, err := fetchAdjacencyNodeIDs(ctx, gc, scope)
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
		sibAdj := deriveSessionSiblings(ctx, gc, nodeIDs, idSet)
		for id, sibs := range sibAdj {
			adj[id] = append(adj[id], sibs...)
		}
	}

	nodeIDs, adj = projectAdjacencySubset(nodeIDs, adj, subset)
	return nodeIDs, adj, nil
}

// fetchTensionEdges builds the edge set ReflectTensions pairs on: thoughts joined
// ONLY by an EXPLICIT, HUMAN thought↔thought reasoning edge (tensionEdgeTypes
// minus machine-Method provenances), with NO session-sibling expansion. The
// tension predicate has TWO exclusions, both applied here:
//
//  1. NO session-sibling expansion — fetchAdjacency("all") folds in every
//     co-session pair via deriveSessionSiblings, which made unrelated thoughts
//     sharing a session read as a tension; pairing on explicit edges removes that
//     false adjacency. fetchTensionEdges never reads EdgeKGContains.
//  2. NO machine relates-to edges — every edge whose Method is one of the four
//     machine writer provenances (isMachineTensionMethod: tree-link / topic-densify
//     / topic-similarity / artifact-link) is dropped from the edge slice. Those
//     edges are clustering/densification signal, not propositional disagreement,
//     so a machine link between opposite-valence thoughts is a category error, not
//     a tension.
//
// It returns the in-scope node IDs, the HUMAN-only edge slice (machine relates-to
// edges already removed), and the in-scope idSet. ReflectTensions consumes the
// edge slice DIRECTLY so it can carry each linking edge's Method + Type into the
// report — the adjacency map alone cannot carry Method, so returning the edge
// slice (not only a neighbor map) is the smaller shape that surfaces provenance.
//
// Clustering is unaffected: it runs off fetchAdjacency, not this helper, and
// buildAdjacencyFromEdges stays byte-identical — the machine-edge drop is a slice
// pre-filter local to this function, structurally incapable of touching
// cluster-detection adjacency.
//
// Cost is the cheap half of fetchAdjacency("all"): one bulk NodeThought browse
// (fetchAllThoughtNodes) + one bulk RETURN_MODE_EDGES read filtered to
// tensionEdgeTypes (fetchEdgesForNodeSet) + a pure client-side O(edges) machine
// filter. It deliberately SKIPS the session-sibling expansion (the extra
// EdgeKGContains read + group-by) that dominates fetchAdjacency("all").
func fetchTensionEdges(ctx context.Context, gc Caller) ([]string, []*knowledgev1.Edge, map[string]bool, error) {
	if gc == nil {
		return nil, nil, nil, nil
	}

	nodes, err := fetchAllThoughtNodes(ctx, gc)
	if err != nil {
		return nil, nil, nil, err
	}
	nodeIDs := make([]string, 0, len(nodes))
	idSet := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		nodeIDs = append(nodeIDs, n.Id)
		idSet[n.Id] = true
	}

	edges, err := fetchEdgesForNodeSet(ctx, gc, nodeIDs, tensionEdgeTypes)
	if err != nil {
		return nil, nil, nil, err
	}

	// Drop machine relates-to edges (tree-link / topic-densify / topic-similarity /
	// artifact-link) — they are clustering signal, not tension signal. Collect
	// POINTERS into the read slice (never copy the Edge struct by value — it embeds
	// a protobuf MessageState/sync.Mutex, so a value copy trips copylocks).
	humanEdges := make([]*knowledgev1.Edge, 0, len(edges))
	for i := range edges {
		if isMachineTensionMethod(edges[i].GetMethod()) {
			continue
		}
		humanEdges = append(humanEdges, &edges[i])
	}
	return nodeIDs, humanEdges, idSet, nil
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
func fetchAdjacencyNodeIDs(ctx context.Context, gc Caller, scope string) ([]string, error) {
	if scope == "all" {
		nodes, err := fetchAllThoughtNodes(ctx, gc)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(nodes))
		for _, n := range nodes {
			ids = append(ids, n.Id)
		}
		return ids, nil
	}
	// all_types: drain every node type in bounded offset pages (shared drainPages
	// cursor/termination/dedup core), then drop NodeProxy client-side. applyNodePage
	// honors the per-page Limit/Offset (s[offset:] then cap at limit; offset>=len →
	// nil → clean termination), so offset paging on the empty-Selection plan works.
	nodes, err := drainPages(func(offset int) ([]*knowledgev1.Node, error) {
		resp, derr := gc.Execute(ctx, &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				Selection: &knowledgev1.Selection{},
				Limit:     int32(browsePageSize),
				Offset:    int32(offset),
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
// member set; the pairwise expansion among co-members (self-excluded) is the
// sibling adjacency.
//
// POLLUTION GUARD: EdgeKGContains is ALSO the generic plan→phase→step containment
// edge, so the idSet[e.ToId] filter drops any non-thought member — a
// thought_session contains only thoughts, but the filter is robust regardless.
func deriveSessionSiblings(ctx context.Context, gc Caller, nodeIDs []string, idSet map[string]bool) map[string][]string {
	sibAdj := make(map[string][]string)
	edges, err := fetchEdgesForNodeSet(ctx, gc, nodeIDs, []kgtypes.EdgeType{kgtypes.EdgeKGContains})
	if err != nil {
		return sibAdj
	}
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
	// Pairwise siblings per session: every ordered (a,b) with a!=b adds b to a.
	for _, members := range bySession {
		for _, a := range members {
			for _, b := range members {
				if a != b {
					sibAdj[a] = append(sibAdj[a], b)
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

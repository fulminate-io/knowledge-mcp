// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// wire_adjacency.go holds the client-side composition of the whole-graph
// adjacency map (T-GTB6 D1) — the reduction of thoughts(adjacency) off the raw
// gc.Call onto the generic Execute seam. It reproduces the server's
// handleAdjacency (a PURE topology.BuildAdjacency read) using a single bulk
// RETURN_MODE_EDGES read over the node set + (scope="all") per-thought
// session-sibling expansion, replacing the N per-node neighbor walks the server
// ran internally.

// FetchThoughtAdjacency is the exported wrapper around fetchAdjacency
// for cmd/knowledge/internal/tools/ — InterceptThoughts needs the
// adjacency map to drive ReflectBlindSpots' bridge-detection pass
// without exporting fetchAdjacency itself (which is the lower-level
// helper the reflective bodies use internally).
func FetchThoughtAdjacency(ctx context.Context, gc *graphclient.GraphClient) ([]string, map[string][]string, error) {
	return fetchAdjacency(ctx, gc, "all", nil)
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

// fetchAdjacency composes the whole-graph adjacency map CLIENT-SIDE, reproducing
// the server's handleAdjacency (a PURE topology.BuildAdjacency read — zero
// valence/propagation compute) over the generic Execute seam. One bulk
// RETURN_MODE_EDGES read over the node set replaces the N per-node neighbor
// walks; scope="all" adds the per-thought session-sibling expansion.
//
// scope="all": NodeThought set, neighbors over the 5 thought-cluster edge types,
// plus the session-sibling expansion. scope="all_types": every node EXCEPT
// NodeProxy, neighbors over ALL edge types, NO sibling expansion. Both scopes
// build neighbors BIDIRECTIONALLY (both endpoints of each incident edge): the
// server's collectNeighbors unions forward+backward per type for "all", and
// issues store.From(id).IDs() — which with forward==nil walks BOTH directions
// (store/query.go:83) — for "all_types". Both filter neighbors to the in-scope
// idSet. subset is the optional post-walk projection.
func fetchAdjacency(ctx context.Context, gc *graphclient.GraphClient, scope string, subset []string) ([]string, map[string][]string, error) {
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

	// scope="all": per-thought session-sibling expansion (the one remaining
	// per-node traverse, bounded by thought count — matches the server's
	// SiblingExpander cost). scope="all_types" runs NO expansion.
	if scope == "all" {
		for _, id := range nodeIDs {
			for _, sib := range fetchSessionSiblings(ctx, gc, id, idSet) {
				if sib != id && idSet[sib] {
					adj[id] = append(adj[id], sib)
				}
			}
		}
	}

	nodeIDs, adj = projectAdjacencySubset(nodeIDs, adj, subset)
	return nodeIDs, adj, nil
}

// fetchAdjacencyNodeIDs returns the in-scope node-ID set: every NodeThought
// (scope="all") or every node except NodeProxy (scope="all_types"). The
// all_types browse enumerates all node types (Match("") via an empty-type
// browse) and filters out proxies client-side, matching the server NodeFilter.
func fetchAdjacencyNodeIDs(ctx context.Context, gc *graphclient.GraphClient, scope string) ([]string, error) {
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
	// all_types: enumerate every node type, drop NodeProxy.
	resp, err := gc.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			Selection: &knowledgev1.Selection{},
		}},
	})
	if err != nil {
		return nil, err
	}
	nodes, derr := engine.DecodeNodes(resp)
	if derr != nil {
		return nil, derr
	}
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if kgtypes.NodeType(n.Type) == kgtypes.NodeProxy {
			continue
		}
		ids = append(ids, n.Id)
	}
	return ids, nil
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

// fetchSessionSiblings reproduces the server thoughtAdjacencySessionSiblings
// SiblingExpander (tools_thought_adjacency.go:125): walk EdgeKGContains BACKWARD
// to the enclosing thought_session, then FORWARD to every sibling thought in the
// same session, filtered to the in-scope idSet.
func fetchSessionSiblings(ctx context.Context, gc *graphclient.GraphClient, nodeID string, idSet map[string]bool) []string {
	sessions, err := fetchTraversalPeerIDs(ctx, gc, nodeID, "in", []string{string(kgtypes.EdgeKGContains)})
	if err != nil {
		return nil
	}
	var sibs []string
	for _, sid := range sessions {
		members, merr := fetchTraversalPeerIDs(ctx, gc, sid, "out", []string{string(kgtypes.EdgeKGContains)})
		if merr != nil {
			continue
		}
		for _, m := range members {
			if m != nodeID && idSet[m] {
				sibs = append(sibs, m)
			}
		}
	}
	return sibs
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

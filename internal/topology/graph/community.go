// SPDX-License-Identifier: Apache-2.0

package graph

// community.go — Generic community-detection plumbing. The Leiden algorithm
// itself lives in leiden.go, leiden_static.go, leiden_static_int.go, and
// leiden_incremental.go; this file provides the glue that materializes a
// graph snapshot over the wire into a (nodeIDs, adjacency) pair and runs
// Leiden over it.
//
// Two entry points:
//
//   - BuildAdjacency: materializes a (nodeIDs, adjacency) snapshot from the
//     wire with caller-supplied node and edge filters.
//   - Detect:         convenience wrapper that calls BuildAdjacency and
//     immediately runs RunLeiden over the result.
//
// The adjacency build reads the scoped graph's nodes (one wire browse) and
// every incident edge (one bulk wire read), then groups the edges per node
// in memory — the N+1-free twin of the legacy per-node store.From walk.

import (
	"context"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// BuildAdjacencyOpts customizes how BuildAdjacency materializes a graph
// snapshot. Every field is optional — the zero value (no filters) builds
// an adjacency map over every node in the graph using every edge type.
type BuildAdjacencyOpts struct {
	// NodeFilter, when non-nil, drops any node where the predicate
	// returns false. The dropped nodes are absent from both the returned
	// nodeIDs slice and the adjacency map's neighbor lists.
	NodeFilter func(*knowledgev1.Node) bool
	// EdgeTypes restricts the neighbor walk to the given edge types. nil
	// means "every edge type".
	EdgeTypes []kgtypes.EdgeType
}

// BuildAdjacency returns a (nodeIDs, adjacency) snapshot of the named
// graph. The returned nodeIDs slice is the set of nodes that survived
// opts.NodeFilter; adjacency[id] is the slice of neighbor IDs (also
// filtered against the same surviving set) reachable via opts.EdgeTypes.
//
// The build is two wire reads: one node browse (FetchAllNodes) and one
// bulk incident-edge read (FetchEdges over the surviving node set). The
// edges are grouped per node in memory, treating every edge as
// undirected (both endpoints gain the other as a neighbor) to match the
// legacy forward+backward store walk.
func BuildAdjacency(
	ctx context.Context,
	caller foundation.GraphCaller,
	graphType kgtypes.GraphType,
	name string,
	opts BuildAdjacencyOpts,
) ([]string, map[string][]string, error) {
	nodes, err := foundation.FetchAllNodes(ctx, caller, graphType, name)
	if err != nil {
		return nil, nil, fmt.Errorf("topology/community: list nodes: %w", err)
	}

	nodeIDs := make([]string, 0, len(nodes))
	idSet := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		if n == nil {
			continue
		}
		if opts.NodeFilter != nil && !opts.NodeFilter(n) {
			continue
		}
		nodeIDs = append(nodeIDs, n.Id)
		idSet[n.Id] = true
	}

	adj, err := buildAdjacencyEdges(ctx, caller, graphType, name, nodeIDs, idSet, opts.EdgeTypes)
	if err != nil {
		return nil, nil, err
	}
	return nodeIDs, adj, nil
}

// buildAdjacencyEdges fetches every edge incident to the surviving node
// set in ONE bulk wire read and builds the undirected adjacency map. Each
// edge contributes its other endpoint to BOTH endpoints' neighbor lists
// (filtered to the surviving set), reproducing the forward+backward walk
// the legacy store-backed collectNeighbors performed.
func buildAdjacencyEdges(
	ctx context.Context,
	caller foundation.GraphCaller,
	graphType kgtypes.GraphType,
	name string,
	nodeIDs []string,
	idSet map[string]bool,
	edgeTypes []kgtypes.EdgeType,
) (map[string][]string, error) {
	adj := make(map[string][]string, len(nodeIDs))
	for _, id := range nodeIDs {
		adj[id] = nil
	}
	if len(nodeIDs) == 0 {
		return adj, nil
	}

	edges, err := foundation.FetchEdges(ctx, caller, graphType, name, nodeIDs, edgeTypes)
	if err != nil {
		return nil, fmt.Errorf("topology/community: neighbors: %w", err)
	}
	// Dedup per node so a multi-edge pair only contributes one neighbor
	// slot, matching the surviving-set filter the store walk applied.
	seen := make(map[string]map[string]bool, len(nodeIDs))
	link := func(from, to string) {
		if from == to || !idSet[from] || !idSet[to] {
			return
		}
		s, ok := seen[from]
		if !ok {
			s = make(map[string]bool)
			seen[from] = s
		}
		if s[to] {
			return
		}
		s[to] = true
		adj[from] = append(adj[from], to)
	}
	for i := range edges {
		e := &edges[i]
		link(e.FromId, e.ToId)
		link(e.ToId, e.FromId)
	}
	return adj, nil
}

// Detect is a convenience wrapper that calls BuildAdjacency with the
// supplied options and immediately runs RunLeiden over the resulting
// snapshot. Callers that want to feed a custom adjacency map (e.g. one
// that came from somewhere other than the wire) should call RunLeiden
// directly instead.
func Detect(
	ctx context.Context,
	caller foundation.GraphCaller,
	graphType kgtypes.GraphType,
	name string,
	gamma float64,
	opts BuildAdjacencyOpts,
) ([]string, map[string]string, error) {
	nodeIDs, adj, err := BuildAdjacency(ctx, caller, graphType, name, opts)
	if err != nil {
		return nil, nil, err
	}
	return nodeIDs, RunLeiden(nodeIDs, adj, gamma), nil
}

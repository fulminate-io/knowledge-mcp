// SPDX-License-Identifier: Apache-2.0

// Package content holds the knowledge-graph "content shape" topology
// analyzers — the family whose data access is a whole-graph node-text /
// node-field scan rather than a materialized adjacency walk. It contains
// degree-histogram, structural-motif, keyword-density, content-type-ratio,
// and title-shape-distribution.
//
// Every analyzer in this package reads its nodes and edges over the wire
// through the foundation.GraphCaller seam: the former in-process
// store.IterateAll / store.Query / store.IterEdges reads become
// foundation.FetchAllNodes / FetchNodesByType / FetchEdges calls. The
// analyzer ALGORITHMS are byte-for-byte the originals from pkg/topology;
// only the node/edge SOURCE swaps to the foundation wire helpers. Each
// analyzer self-registers via init() → foundation.Register.
//
// content depends ONLY on the wire proto vocabulary (gen/knowledge/v1 +
// pkg/kgtypes), the foundation scaffolding, and the standard library — never
// the storage engine or the legacy server-side topology package.
package content

import (
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// nodeIndex is the in-memory id→node map several content analyzers build once
// from a single FetchAllNodes pass, replacing the per-node store.Query(ByID)
// round-trips the originals issued during their subtree / skeleton walks.
type nodeIndex map[string]*knowledgev1.Node

// buildNodeIndex turns a wire node slice into the id→node lookup map, skipping
// nil entries. The map is the in-memory stand-in for the scoped store handle
// the originals re-queried per node.
func buildNodeIndex(nodes []*knowledgev1.Node) nodeIndex {
	idx := make(nodeIndex, len(nodes))
	for _, n := range nodes {
		if n == nil {
			continue
		}
		idx[n.Id] = n
	}
	return idx
}

// resolveNodeName returns the display name for a node ID out of the in-memory
// index: SymbolName when set, else the raw ID. This mirrors the graph family's
// ResolveNodeName (pkg/topology/findings.go:236, which did a scoped.Query(ByID)
// and read SymbolName) but reads from the already-fetched node map so the
// content analyzers issue no extra per-node wire round-trip.
func resolveNodeName(idx nodeIndex, id string) string {
	if n, ok := idx[id]; ok && n.SymbolName != "" {
		return n.SymbolName
	}
	return id
}

// containsChildrenIndex maps a parent node ID to the IDs reachable via one
// outgoing EdgeContains edge. It is built once from a single bulk FetchEdges
// over the whole node set, replacing the per-node
// store.Query(From(id).Edge(EdgeContains).Forward().IDs()) reads the originals
// issued while walking subtrees / skeletons.
type containsChildrenIndex map[string][]string

// buildContainsIndex returns the parent→children adjacency for EdgeContains
// edges among the given node set. edges is the result of a single
// FetchEdges(ids, [EdgeContains]) call; only edges whose source is in the node
// set (idx) seed the adjacency, mirroring the originals which walked outgoing
// CONTAINS edges from nodes they had already loaded. Children are NOT sorted
// here — callers that need a stable order (structural-motif) sort by their own
// (type, id) key after lookup; content-type-ratio's BFS is order-insensitive.
func buildContainsIndex(edges []knowledgev1.Edge, idx nodeIndex) containsChildrenIndex {
	adj := make(containsChildrenIndex)
	for i := range edges {
		e := &edges[i]
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeContains {
			continue
		}
		if _, ok := idx[e.FromId]; !ok {
			continue
		}
		adj[e.FromId] = append(adj[e.FromId], e.ToId)
	}
	return adj
}

// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// metaValue reads a metadata key off a wire node, returning "" when the node
// or its metadata map is nil or the key is absent. Wire twin of the store
// node's Value(key) accessor the prior analyzers used.
func metaValue(n *knowledgev1.Node, key string) string {
	if n == nil || n.Metadata == nil {
		return ""
	}
	return n.Metadata[key]
}

// helpers.go holds the small shared helpers reused across the gonum analyzer
// family: the default top-K cap, the wire-backed node-name resolver and
// neighbor sampler (used by pagerank / hits / degree / betweenness /
// god_object to populate human-readable titles + representative evidence),
// and the pure ComputeEdgeChanges adjacency diff consumed by the Leiden
// incremental path. All node/edge reads go over the wire through the
// foundation read-helpers — there is no in-process store anywhere in this
// package.

// defaultTopK is the cap applied to ranked results when the caller did not
// specify Request.TopK. 20 matches the convention every ranked analyzer in
// the family uses.
const defaultTopK = 20

// primaryEvidence returns the first evidence node ID for a Finding, or "" if
// Evidence is empty. Used as the dedup discriminator and the stable
// tie-break key by analyzers that emit one finding per component / cycle.
func primaryEvidence(f foundation.Finding) string {
	if len(f.Evidence) == 0 {
		return ""
	}
	return f.Evidence[0]
}

// ResolveNodeName looks up a node's display name (SymbolName) for use in
// human-readable Finding titles. Falls back to the raw nodeID when the wire
// read fails or the node has no SymbolName set, so analyzers can always
// produce a non-empty title without an error path.
//
// Cost: one wire read per call. Analyzers typically invoke this once per
// top-K finding (default 20), which is acceptable. The legacy
// scoped.Query(ByID) read becomes one foundation.FetchNodeByID Execute.
func ResolveNodeName(ctx context.Context, caller foundation.GraphCaller, graphType kgtypes.GraphType, name string, nodeID string) string {
	if caller == nil || nodeID == "" {
		return nodeID
	}
	n, ok, err := foundation.FetchNodeByID(ctx, caller, graphType, name, nodeID)
	if err != nil || !ok || n == nil {
		return nodeID
	}
	if n.SymbolName != "" {
		return n.SymbolName
	}
	return nodeID
}

// sampleNeighborsCap is the fixed number of representative neighbor IDs every
// ranked analyzer samples into a Finding's Evidence beyond the primary node.
// 5 matches the cap every analyzer in the family uses.
const sampleNeighborsCap = 5

// sampleNeighbors returns up to sampleNeighborsCap neighbor IDs for nodeID in
// the given direction. direction is one of "in" (incoming edges → who depends
// on this node), "out" (outgoing edges → what this node depends on), or "both"
// (combined). Used to populate Finding.Evidence with representative context
// beyond the primary node ID.
//
// When direction is "both", the result interleaves in- and out-neighbors up
// to the cap with no preference for direction. Duplicates are deduped so a
// node that appears in both directions only takes one slot. Returns nil on any
// error. The legacy per-direction scoped.IterEdges reads become a bulk
// foundation.FetchEdges over the single node ID, filtered by edge direction in
// memory.
func sampleNeighbors(ctx context.Context, caller foundation.GraphCaller, graphType kgtypes.GraphType, name string, nodeID string, direction string) []string {
	if caller == nil || nodeID == "" {
		return nil
	}
	edges, err := foundation.FetchEdges(ctx, caller, graphType, name, []string{nodeID}, nil)
	if err != nil {
		return nil
	}

	out := make([]string, 0, sampleNeighborsCap)
	seen := make(map[string]struct{}, sampleNeighborsCap)
	add := func(id string) bool {
		if id == "" || id == nodeID {
			return false
		}
		if _, dup := seen[id]; dup {
			return false
		}
		seen[id] = struct{}{}
		out = append(out, id)
		return len(out) >= sampleNeighborsCap
	}

	if direction == "in" || direction == "both" {
		for i := range edges {
			e := &edges[i]
			if e.ToId == nodeID {
				if add(e.FromId) {
					return out
				}
			}
		}
	}
	if direction == "out" || direction == "both" {
		for i := range edges {
			e := &edges[i]
			if e.FromId == nodeID {
				if add(e.ToId) {
					return out
				}
			}
		}
	}
	return out
}

// ComputeEdgeChanges returns the symmetric difference between two adjacency
// maps. Edges are treated as undirected: each (from, to) pair is
// canonicalized to (min, max) so that (a→b) and (b→a) are counted as the same
// edge. Returns EdgeChange{From, To, Removed: true} for edges present in
// oldAdj but absent from newAdj, and EdgeChange{From, To, Removed: false} for
// edges present in newAdj but absent from oldAdj.
//
// This helper is the bridge between two adjacency snapshots and the Dynamic
// Frontier incremental update path on LeidenState — callers build snapshots,
// diff them via ComputeEdgeChanges, and feed the result to
// LeidenState.UpdateIncremental.
func ComputeEdgeChanges(oldAdj, newAdj map[string][]string) []EdgeChange {
	canonicalize := func(a, b string) [2]string {
		if a > b {
			a, b = b, a
		}
		return [2]string{a, b}
	}

	buildEdgeSet := func(adj map[string][]string) map[[2]string]bool {
		edges := make(map[[2]string]bool)
		for from, neighbors := range adj {
			for _, to := range neighbors {
				edges[canonicalize(from, to)] = true
			}
		}
		return edges
	}

	oldEdges := buildEdgeSet(oldAdj)
	newEdges := buildEdgeSet(newAdj)

	var changes []EdgeChange
	for edge := range oldEdges {
		if !newEdges[edge] {
			changes = append(changes, EdgeChange{From: edge[0], To: edge[1], Removed: true})
		}
	}
	for edge := range newEdges {
		if !oldEdges[edge] {
			changes = append(changes, EdgeChange{From: edge[0], To: edge[1], Removed: false})
		}
	}
	return changes
}

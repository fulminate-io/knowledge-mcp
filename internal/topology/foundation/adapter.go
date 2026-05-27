// SPDX-License-Identifier: Apache-2.0

package foundation

import (
	"context"
	"fmt"

	"gonum.org/v1/gonum/graph/simple"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// adapter.go builds the in-memory GonumGraph from the wire. The struct and its
// read methods (NodeID/StringID/IterateNodesByType/Undirected) live in
// graph_methods.go; this file owns the two-pass materialize that swaps the
// former in-process store reads for two Execute round-trips over a GraphCaller.
//
// The legacy server-side adapter kept a per-graph cache keyed on the graph's
// last-sync-commit hash so analyzers in one dream pass could share a built
// graph. That cache has no wire equivalent — there is no read-helper that
// returns a graph's commit-hash metadata, and adding one would buy nothing
// over the two-Execute build. The graph is therefore materialized fresh on
// every NewGonumGraph call. The cost is bounded: ONE node-browse Execute plus
// ONE bulk-edge Execute per build, never a per-node fan-out.

// NewGonumGraph materializes a snapshot of (graphType, name) into an in-memory
// gonum weighted directed graph by fetching the nodes and edges over the wire
// through caller. The optional subset predicate filters which nodes
// participate; pass nil to include every node. Edge weights are taken from the
// fetched edge Weight, with a 0 → 1.0 fallback so edges that were never
// weighted (e.g. legacy graphs, or non-Go languages whose chunkers don't emit
// call counts) still participate at the unweighted baseline.
//
// The function performs two passes:
//  1. Node materialization — fetch every node in the scoped graph over the
//     wire (one Execute), apply the subset filter, assign a gonum int64 ID,
//     populate the bidi maps, and add the node to the embedded
//     WeightedDirectedGraph.
//  2. Edge materialization — fetch every edge incident to the materialized
//     node set in ONE bulk Execute and SetWeightedEdge any pair where both
//     endpoints survived the subset filter.
//
// Errors from the wire are wrapped with "topology: ..." context and returned
// without a partial graph (we never return a non-nil result alongside a
// non-nil error).
func NewGonumGraph(ctx context.Context, caller GraphCaller, graphType kgtypes.GraphType, name string, subset func(*knowledgev1.Node) bool) (*GonumGraph, error) {
	return newGonumGraph(ctx, caller, graphType, name, subset, false)
}

// NewGonumGraphUnweighted is identical to NewGonumGraph but forces every
// materialized edge weight to 1.0, preserving the unweighted PageRank / HITS
// semantics for analyzers that explicitly opt out of the new edge-weight
// signal (e.g. the existing PageRankAnalyzer).
func NewGonumGraphUnweighted(ctx context.Context, caller GraphCaller, graphType kgtypes.GraphType, name string, subset func(*knowledgev1.Node) bool) (*GonumGraph, error) {
	return newGonumGraph(ctx, caller, graphType, name, subset, true)
}

func newGonumGraph(
	ctx context.Context,
	caller GraphCaller,
	graphType kgtypes.GraphType,
	name string,
	subset func(*knowledgev1.Node) bool,
	unweighted bool,
) (*GonumGraph, error) {
	if caller == nil {
		return nil, fmt.Errorf("topology: NewGonumGraph: graph caller unavailable")
	}

	g := &GonumGraph{
		// self=0 (self-loops are dropped before SetWeightedEdge anyway)
		// absent=0 (an absent edge weighs 0 — analyzers should check
		// existence via HasEdgeFromTo before reading Weight).
		WeightedDirectedGraph: simple.NewWeightedDirectedGraph(0, 0),
		stringToInt:           make(map[string]int64),
		intToString:           make(map[int64]string),
		nodeType:              make(map[int64]kgtypes.NodeType),
		unweighted:            unweighted,
	}

	if err := g.materializeNodes(ctx, caller, graphType, name, subset); err != nil {
		return nil, err
	}
	if err := g.materializeEdges(ctx, caller, graphType, name); err != nil {
		return nil, err
	}
	return g, nil
}

// materializeNodes runs pass 1 of the build: fetch all nodes in the scoped
// graph over the wire (one Execute via FetchAllNodes), apply the subset
// filter, and add survivors to the embedded WeightedDirectedGraph with a
// freshly assigned gonum int64 ID. The nextID assignment order follows the
// wire result order, matching the prior IterateAll order semantics.
func (g *GonumGraph) materializeNodes(ctx context.Context, caller GraphCaller, graphType kgtypes.GraphType, name string, subset func(*knowledgev1.Node) bool) error {
	nodes, err := FetchAllNodes(ctx, caller, graphType, name)
	if err != nil {
		return fmt.Errorf("topology: materialize nodes %s/%s: %w", graphType, name, err)
	}
	for _, n := range nodes {
		if n == nil {
			continue
		}
		if subset != nil && !subset(n) {
			continue
		}
		id := g.nextID
		g.nextID++
		g.stringToInt[n.Id] = id
		g.intToString[id] = n.Id
		g.nodeType[id] = kgtypes.NodeType(n.Type)
		g.AddNode(simple.Node(id))
	}
	return nil
}

// materializeEdges runs pass 2 of the build: fetch every edge incident to the
// materialized node set in ONE bulk Execute (FetchEdges) and SetWeightedEdge
// any pair where both endpoints survived the subset filter. Edges that
// reference an unmaterialized node (because the subset predicate excluded it)
// are silently dropped. This is the N+1 avoidance — a single bulk edge read
// over the whole node set, never a per-node edge fetch.
//
// Self-loops (edges where source == target) are also silently dropped. Two
// reasons: (1) gonum's simple.WeightedDirectedGraph PANICS on SetWeightedEdge
// for a self-edge, so accepting them here would crash every analyzer the
// instant a real-world graph contains a self-referential node; (2) the SCC
// analyzer's policy is "exclude self-loops on all graphs", and enforcing the
// policy at the adapter layer means every downstream analyzer (cycles,
// articulation, degree, etc.) gets a consistent self-loop-free view without
// each one having to repeat the filter.
//
// Edge weights are taken from the fetched edge Weight. A 0 weight is treated
// as the unweighted baseline (1.0); analyzers that want strict unweighted
// semantics should construct the graph via NewGonumGraphUnweighted, which
// forces every edge to weight 1.0 regardless of the stored value.
func (g *GonumGraph) materializeEdges(ctx context.Context, caller GraphCaller, graphType kgtypes.GraphType, name string) error {
	ids := make([]string, 0, len(g.stringToInt))
	for s := range g.stringToInt {
		ids = append(ids, s)
	}
	if len(ids) == 0 {
		return nil
	}
	edges, err := FetchEdges(ctx, caller, graphType, name, ids, nil)
	if err != nil {
		return fmt.Errorf("topology: materialize edges: %w", err)
	}
	for i := range edges {
		e := &edges[i]
		fromInt, ok := g.stringToInt[e.FromId]
		if !ok {
			continue
		}
		toInt, ok := g.stringToInt[e.ToId]
		if !ok {
			continue
		}
		if toInt == fromInt {
			// Self-loop: drop unconditionally. See doc comment.
			continue
		}
		weight := e.Weight
		if g.unweighted || weight == 0 {
			weight = 1
		}
		g.SetWeightedEdge(g.NewWeightedEdge(simple.Node(fromInt), simple.Node(toInt), weight))
	}
	return nil
}

// SPDX-License-Identifier: Apache-2.0

package foundation

import (
	"gonum.org/v1/gonum/graph"
	"gonum.org/v1/gonum/graph/simple"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// Compile-time assertion that GonumGraph satisfies graph.Directed. The
// interface methods (Node, Nodes, From, To, HasEdgeBetween, HasEdgeFromTo,
// Edge) are inherited from the embedded *simple.WeightedDirectedGraph via
// Go method promotion — we do not implement them ourselves.
// WeightedDirectedGraph also satisfies graph.WeightedDirected, which is
// what gonum's PageRankSparse auto-detects to switch into edge-weighted
// mode.
var _ graph.Directed = (*GonumGraph)(nil)

// GonumGraph is a topology-friendly view of a wire-fetched graph. It wraps
// gonum's simple.WeightedDirectedGraph (which gives us all of the
// graph.Directed and graph.WeightedDirected surface area for free via
// method promotion) and adds the bidirectional string↔int64 ID mapping
// every analyzer needs to translate gonum results back to node IDs.
//
// GonumGraph is built once via NewGonumGraph (weight-aware) or
// NewGonumGraphUnweighted (forces every edge to weight 1.0) from a
// GraphCaller; after construction it is read-only as far as topology
// analyzers are concerned. The struct is intentionally not safe for
// concurrent mutation — analyzers that need parallelism should snapshot
// what they need under their own synchronization.
type GonumGraph struct {
	*simple.WeightedDirectedGraph

	// stringToInt maps a node ID to the gonum int64 ID assigned by
	// NewGonumGraph. Lookup is O(1) and is the preferred way for analyzers
	// to translate "I have a node ID, what's its gonum node?".
	stringToInt map[string]int64
	// intToString is the reverse mapping. Analyzers walk gonum nodes and
	// use this to surface node IDs in Findings.
	intToString map[int64]string
	// nodeType records the NodeType for each materialized node so analyzers
	// can iterate "all function declarations" without re-querying the wire.
	nodeType map[int64]kgtypes.NodeType
	// nextID is the monotonically increasing gonum int64 ID counter.
	nextID int64
	// unweighted forces every materialized edge weight to 1.0 regardless of
	// what the underlying edge weight reports. Set by NewGonumGraphUnweighted
	// to preserve the v1 unweighted PageRank semantics; left false by
	// NewGonumGraph.
	unweighted bool
}

// NodeID returns the gonum int64 ID for a node ID and whether the node was
// materialized into the graph (i.e., survived the subset filter).
func (g *GonumGraph) NodeID(stringID string) (int64, bool) {
	id, ok := g.stringToInt[stringID]
	return id, ok
}

// StringID returns the node ID for a gonum int64 ID and whether the node
// exists in the graph.
func (g *GonumGraph) StringID(intID int64) (string, bool) {
	s, ok := g.intToString[intID]
	return s, ok
}

// IterateNodesByType walks every materialized node whose NodeType equals t and
// invokes fn with the gonum int64 ID and the corresponding node string ID.
// Iteration stops early if fn returns false. Map order is undefined — callers
// that need a stable order must sort the results themselves.
//
// This helper exists so type-specific analyzers (e.g. "find god-object
// function declarations") can restrict the walk without re-querying the wire
// or scanning every node in the gonum graph.
func (g *GonumGraph) IterateNodesByType(t kgtypes.NodeType, fn func(intID int64, stringID string) bool) {
	for intID, nodeType := range g.nodeType {
		if nodeType != t {
			continue
		}
		stringID := g.intToString[intID]
		if !fn(intID, stringID) {
			return
		}
	}
}

// Undirected returns an undirected view of the same underlying gonum graph.
// The view is backed by gonum's `graph.Undirect` adapter — it does not copy
// or duplicate edges, it merges incoming and outgoing neighbors on demand.
// This unblocks articulation-points / biconnected-components analyzers that
// need an undirected graph without forcing the adapter to maintain a parallel
// `*simple.UndirectedGraph`.
func (g *GonumGraph) Undirected() graph.Undirected {
	return graph.Undirect{G: g.WeightedDirectedGraph}
}

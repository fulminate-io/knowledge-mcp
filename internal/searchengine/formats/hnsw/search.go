// SPDX-License-Identifier: Apache-2.0

package hnsw

// Default binary HNSW parameters. Mirror the server's binary_factory.go defaults
// so the client-built graph matches the server's tuning.
const (
	defaultVecBytes       = 32  // 256-bit ubinary
	defaultM              = 32  // out-degree per layer
	defaultEfConstruction = 200 // build-time beam width
	defaultEfSearch       = 50  // query-time beam width
)

// graphHit is the graph's neutral result: an external ID and a higher-is-better
// similarity score. The format adapter (format.go) maps it to searchengine.Hit.
type graphHit struct {
	externalID string
	score      float64
}

// neighborsAt returns node id's neighbor run at the given layer, or nil when
// layer exceeds that node's own maxLevel. Together with nodeCount this is the
// whole topology seam the shared traversal needs — see traverse.go.
func (h *binaryGraph) neighborsAt(id uint32, layer int) []uint32 {
	node := &h.nodes[id]
	if layer >= len(node.neighbors) {
		return nil
	}
	return node.neighbors[layer]
}

// search runs the HNSW query. It is a ONE-LINE DELEGATION to traverse.go's
// searchTopK, which both graph forms share: duplicating the descent, the beam
// and the score formula onto the mapped reader is exactly the drift this split
// exists to prevent.
//
// This replaces the server's BinaryIndex.Search deletedIDs skip with the
// engine's liveDocs accept filter: same over-fetch-then-filter shape, but the
// liveness decision lives in the engine, not in graph-mutating state.
//
// THE accept PARAMETER IS UNIFORM HERE AND MUST STAY. After the publish flip
// every PRODUCTION search runs on mappedGraph, so this method survives only as
// the BASELINE the bit-identity and recall proofs compare against — and those
// tests pass nil because they compare unfiltered rankings. Specialising this
// signature would make the two forms' search asymmetric at exactly the seam the
// shared traversal exists to keep identical, and would silently weaken the
// comparison the baseline is for. Same disposition, and same reasoning, as the
// align mirror: a linter observation about local uniformity, against a shape
// the design deliberately keeps symmetric.
//
//nolint:unparam // see above: the baseline must keep mappedGraph.search's shape
func (h *binaryGraph) search(query []byte, k int, accept func(externalID string) bool) []graphHit {
	return searchTopK(&h.vectorBlock, h, h.maxLevel, h.entryPoint, query, h.efSearch, k, accept,
		func(id uint32) string { return h.nodes[id].externalID })
}

// nodeCount returns the number of indexed nodes.
func (h *binaryGraph) nodeCount() int { return len(h.nodes) }

// setEfSearch sets the default beam width for queries.
func (h *binaryGraph) setEfSearch(ef int) { h.efSearch = ef }

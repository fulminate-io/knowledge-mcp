// SPDX-License-Identifier: Apache-2.0

package hnsw

import "sort"

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

// search runs the HNSW query: descend the upper layers greedily to an entry
// point, then beam-search layer 0 at efSearch (>= k). The accept predicate gates
// results — an id for which accept returns false is skipped from the RESULTS
// (the graph is never mutated). A nil accept admits every id. Returns up to k
// hits sorted nearest-first, with the Hamming distance mapped to a 0..1
// similarity (1 - dist/totalBits).
//
// This replaces the server's BinaryIndex.Search deletedIDs skip with the engine's
// liveDocs accept filter: same over-fetch-then-filter shape, but the liveness
// decision lives in the engine, not in graph-mutating state.
func (h *binaryGraph) search(query []byte, k int, accept func(externalID string) bool) []graphHit {
	if len(h.nodes) == 0 || k <= 0 {
		return nil
	}

	efSearch := max(h.efSearch, k)

	ep := h.entryPoint
	for l := h.maxLevel; l > 0; l-- {
		ep = h.greedyClosest(query, ep, l)
	}

	results := h.searchLayer(query, []uint32{ep}, efSearch, 0)

	items := make([]heapItem, results.Len())
	copy(items, *results)
	sort.Slice(items, func(i, j int) bool {
		return items[i].dist < items[j].dist
	})

	totalBits := float64(h.vecBytes * 8)
	out := make([]graphHit, 0, min(k, len(items)))
	for _, item := range items {
		if len(out) >= k {
			break
		}
		extID := h.nodes[item.id].externalID
		if accept != nil && !accept(extID) {
			continue
		}
		out = append(out, graphHit{
			externalID: extID,
			score:      1.0 - float64(item.dist)/totalBits,
		})
	}
	return out
}

// ids returns every externalID the graph indexes, in internal-ID order (stable).
func (h *binaryGraph) ids() []string {
	out := make([]string, len(h.nodes))
	for i := range h.nodes {
		out[i] = h.nodes[i].externalID
	}
	return out
}

// rangeVectors yields every (externalID, vector) pair in internal-ID order. The
// yielded slice aliases the graph's flat vector array — callers that retain it
// (e.g. Merge re-inserting into a fresh graph, which copies on Insert) must not
// mutate it. Package-internal accessor for the format's Lucene-style Merge.
func (h *binaryGraph) rangeVectors(yield func(externalID string, vec []byte)) {
	for i := range h.nodes {
		yield(h.nodes[i].externalID, h.nodeVector(uint32(i)))
	}
}

// nodeCount returns the number of indexed nodes.
func (h *binaryGraph) nodeCount() int { return len(h.nodes) }

// setEfSearch sets the default beam width for queries.
func (h *binaryGraph) setEfSearch(ef int) { h.efSearch = ef }

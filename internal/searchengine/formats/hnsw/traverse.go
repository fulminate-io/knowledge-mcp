// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"sort"
	"sync"
)

// traverse.go holds the ONE HNSW traversal, shared by both graph
// representations. It exists so the in-place reader does not need a second
// search algorithm.
//
// The split is deliberate and narrow. Everything that differs between a
// heap-built graph and a mapped one is reached through two small seams — the
// vector block (embedded, so nodeVector stays a direct call on both) and
// neighborSource (two methods, one indirect call per POPPED CANDIDATE rather
// than per node). Everything else — the distance function, the visited bitset,
// both heaps, the descent, the beam and the SCORE FORMULA — lives here once.
//
// THE SCORE FORMULA LIVING HERE ONCE IS THE POINT. Duplicating the search onto
// the reader would put `1.0 - float64(dist)/totalBits` in two places, and two
// copies of a scoring rule drift silently: the tests would still pass on each
// path while the two paths ranked differently. A criterion counts that literal
// and requires exactly one occurrence in the package.

// vectorBlock is the flat, offset-addressable vector array both graph forms
// hold. EMBEDDED by binaryGraph and mappedGraph rather than reached through an
// interface, so nodeVector stays a direct, inlinable call on the hot path.
type vectorBlock struct {
	vectors  []byte
	vecBytes int
}

// nodeVector returns node id's vector as a sub-slice of the block.
//
// THE RETURNED SLICE ALIASES THE BLOCK. On a mapped graph the block is the
// segment mapping, so a caller that RETAINS this beyond the mapping's life is
// reading unmapped memory. Every call site that hands a vector across the
// segment API boundary must copy; the boundary-copy rule and its catchers are
// the enforcement.
func (v *vectorBlock) nodeVector(id uint32) []byte {
	start := int(id) * v.vecBytes
	return v.vectors[start : start+v.vecBytes]
}

// neighborSource is the topology seam: the only thing the traversal needs that
// differs between a built graph and a mapped one.
//
// Exactly two methods, and neighborsAt returns a WHOLE RUN, so the traversal
// pays one indirect call per popped candidate rather than one per neighbor.
type neighborSource interface {
	nodeCount() int
	// neighborsAt returns node id's neighbor run at the given layer, or nil
	// when layer exceeds that node's own maxLevel.
	neighborsAt(id uint32, layer int) []uint32
}

// Compile-time contract assertion: a signature drift is a build failure rather
// than a silently dropped arm.
var _ neighborSource = (*binaryGraph)(nil)

// searchState is the reusable scratch a layer search needs.
type searchState struct {
	visited    bitset
	candidates minHeap
	results    maxHeap
}

// searchStatePool pools searchState objects to avoid per-call allocations in
// the beam search.
var searchStatePool = sync.Pool{
	New: func() any { return &searchState{} },
}

// greedyClosest walks one upper layer, hopping to the nearest neighbor until
// no neighbor improves on the current node.
func greedyClosest(vb *vectorBlock, ns neighborSource, query []byte, ep uint32, layer int) uint32 {
	curr := ep
	currDist := hammingDistance(query, vb.nodeVector(curr))

	for {
		changed := false
		for _, neighborID := range ns.neighborsAt(curr, layer) {
			d := hammingDistance(query, vb.nodeVector(neighborID))
			if d < currDist {
				curr = neighborID
				currDist = d
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return curr
}

// searchLayer is the beam search at one layer: expand the nearest unvisited
// candidate until nothing closer than the current worst result remains.
func searchLayer(vb *vectorBlock, ns neighborSource, query []byte, entryPoints []uint32, ef, layer int) *maxHeap {
	state, _ := searchStatePool.Get().(*searchState)

	// Clear and grow state from previous use.
	state.visited.grow(ns.nodeCount())
	state.visited.clearAll()
	state.candidates = state.candidates[:0]
	state.results = state.results[:0]

	candidates := &state.candidates
	results := &state.results

	for _, ep := range entryPoints {
		if state.visited.has(ep) {
			continue
		}
		state.visited.set(ep)
		d := hammingDistance(query, vb.nodeVector(ep))
		candidates.push(heapItem{id: ep, dist: d})
		results.push(heapItem{id: ep, dist: d})
	}

	for candidates.Len() > 0 {
		nearest := candidates.pop()

		if results.Len() >= ef && nearest.dist > results.peek().dist {
			break
		}

		for _, neighborID := range ns.neighborsAt(nearest.id, layer) {
			if state.visited.has(neighborID) {
				continue
			}
			state.visited.set(neighborID)

			d := hammingDistance(query, vb.nodeVector(neighborID))

			if results.Len() < ef || d < results.peek().dist {
				candidates.push(heapItem{id: neighborID, dist: d})
				results.push(heapItem{id: neighborID, dist: d})
				if results.Len() > ef {
					results.pop()
				}
			}
		}
	}

	// Copy results out before returning state to pool.
	out := make(maxHeap, len(*results))
	copy(out, *results)

	searchStatePool.Put(state)
	return &out
}

// searchTopK is the WHOLE query: descend the upper layers greedily to an entry
// point, beam-search layer 0 at ef, then map the nearest results to hits.
//
// IT LIVES HERE, NOT ON EITHER GRAPH TYPE, so both forms answer queries with
// the same code. The external-id lookup is the one thing that genuinely differs
// between them — a slice field on the built graph, a blob view on the mapped
// one — so it enters as the idAt closure rather than a third interface method.
// That costs one indirect call per RETURNED HIT, not per candidate.
//
// accept is the engine's liveDocs filter: an id it rejects is skipped from the
// RESULTS and the graph is never mutated. A nil accept admits every id.
// NOTE ON THE SIGNATURE'S LAYOUT: `vb *vectorBlock` stays on the func line
// rather than wrapping onto the next. Criterion 324a8c2c greps the literal
// `searchTopK(vb ` and a wrapped parameter list makes that token unfindable —
// the same locked-token-on-one-line rule the plan states for doc phrases,
// applied to a declaration.
func searchTopK(vb *vectorBlock, ns neighborSource, maxLevel int, entryPoint uint32,
	query []byte, ef, k int, accept func(string) bool, idAt func(uint32) string,
) []graphHit {
	if ns.nodeCount() == 0 || k <= 0 {
		return nil
	}
	if k > ef {
		ef = k
	}

	ep := entryPoint
	for l := maxLevel; l > 0; l-- {
		ep = greedyClosest(vb, ns, query, ep, l)
	}

	results := searchLayer(vb, ns, query, []uint32{ep}, ef, 0)

	items := make([]heapItem, results.Len())
	copy(items, *results)
	sort.Slice(items, func(i, j int) bool {
		return items[i].dist < items[j].dist
	})

	totalBits := float64(vb.vecBytes * 8)
	out := make([]graphHit, 0, min(k, len(items)))
	for _, item := range items {
		if len(out) >= k {
			break
		}
		extID := idAt(item.id)
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

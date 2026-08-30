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
// than per node). Everything else — the visited bitset, both heaps, the descent
// and the beam — lives here once.
//
// WHAT DIFFERS BY DTYPE LIVES IN dtype.go, and there is exactly one copy of each
// half. The metric is resolved per segment into vectorBlock.distance and called
// through that field; the score normalization is scoreForDtype. Neither is
// branched on here. Duplicating the search onto the reader would have put the
// scoring rule in two places, and two copies of a scoring rule drift silently:
// the tests would still pass on each path while the two paths ranked
// differently. A criterion counts the ubinary formula's literal and requires
// exactly one occurrence in the package.

// vectorBlock is the flat, offset-addressable vector array both graph forms
// hold. EMBEDDED by binaryGraph and mappedGraph rather than reached through an
// interface, so nodeVector stays a direct, inlinable call on the hot path.
type vectorBlock struct {
	vectors  []byte
	vecBytes int
	// dtype says how the block's bytes are to be READ — dtypeUbinary or
	// dtypeFloat32 — and therefore which metric ranks them. It rides on the
	// vector block rather than on either graph type so the one traversal reads
	// it identically from a built graph and a mapped one, exactly as vecBytes
	// already does. Zero value is dtypeUbinary, which is what every segment
	// written before the tag existed carries.
	dtype byte
	// distance is the metric for THIS block's dtype, resolved once by setDtype
	// (dtype.go) at open or construction. The traversal calls it directly rather
	// than branching on dtype per distance, because the per-distance fixed cost
	// is the term that already dominates at narrow widths. Set it only through
	// setDtype — the tag and the metric must not drift apart.
	distance distanceFn
	// nodeDistance is the same metric between two STORED vectors, used by the
	// build path's neighbor selection and re-prune. Resolved by the same
	// setDtype: a build that selected neighbors under a different metric than
	// the search uses would degrade recall with nothing reporting a fault.
	nodeDistance nodeDistanceFn
	// batchScore is the same metric over a whole run of ids in one call, for the
	// batched neighbor scoring. Resolved by the same setDtype so it cannot
	// disagree with distance about what this block's dtype means.
	batchScore batchScoreFn
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
	// batchScratch is EMBEDDED so a layer search's two-pass neighbor scoring
	// reuses the same pooled object as its heaps and bitset — one pool Get per
	// layer search covers every buffer it needs. See batch.go.
	batchScratch
}

// searchStatePool pools searchState objects to avoid per-call allocations in
// the beam search.
var searchStatePool = sync.Pool{
	New: func() any { return &searchState{} },
}

// greedyClosest walks one upper layer, hopping to the nearest neighbor until
// no neighbor improves on the current node.
//
// THE WHOLE HOP IS SCORED IN ONE CALL. The descent has no visited bitset — it
// re-reads the run of whichever node it moved to — so pass 1 is the run itself
// and there is nothing to filter. bs is supplied by the caller and reused across
// every layer and hop of one descent, so the batching costs no allocation here.
//
// THE COMPARISON ORDER IS UNCHANGED: currDist still updates as the run is
// walked, and the ids are visited in run order, so this picks exactly the node
// the one-at-a-time loop picked. Only the distance COMPUTATION moved.
func greedyClosest(vb *vectorBlock, ns neighborSource, q *preparedQuery, ep uint32, layer int, bs *batchScratch) uint32 {
	curr := ep
	currDist := vb.distance(q, vb.nodeVector(curr))

	for {
		run := ns.neighborsAt(curr, layer)
		if len(run) == 0 {
			break
		}
		bs.prepare(len(run))
		bs.ids = append(bs.ids, run...)
		dists := scoreCollected(vb, q, bs)

		changed := false
		for i, d := range dists {
			if d < currDist {
				curr = bs.ids[i]
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
func searchLayer(vb *vectorBlock, ns neighborSource, q *preparedQuery, entryPoints []uint32, ef, layer int) *maxHeap {
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
		d := vb.distance(q, vb.nodeVector(ep))
		candidates.push(heapItem{id: ep, dist: d})
		results.push(heapItem{id: ep, dist: d})
	}

	for candidates.Len() > 0 {
		nearest := candidates.pop()

		if results.Len() >= ef && nearest.dist > results.peek().dist {
			break
		}

		// PASS 1 — collect the unvisited neighbors, marking each as it is taken.
		// PASS 2 — score the whole collected run in one call (batch.go).
		collectUnvisited(state, ns.neighborsAt(nearest.id, layer))
		dists := scoreCollected(vb, q, &state.batchScratch)

		// THE ADMISSION LOOP IS UNCHANGED, AND THAT IS WHY RANKING IS IDENTICAL.
		// Only the distance COMPUTATION was hoisted; the pushes still happen one
		// at a time, in run order, against the heaps' evolving state. Hoisting
		// the admission decisions too would change which candidates are admitted,
		// because each test reads a results heap the previous push may have
		// altered — a parity test asserts the batched and unbatched paths rank
		// the same graph identically.
		for i, d := range dists {
			neighborID := state.ids[i]
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
// rather than wrapping onto the next. A criterion greps the literal
// `searchTopK(vb ` and a wrapped parameter list makes that token unfindable —
// the same locked-token-on-one-line rule the plan states for doc phrases,
// applied to a declaration.
func searchTopK(vb *vectorBlock, ns neighborSource, maxLevel int, entryPoint uint32,
	query []byte, ef, k int, accept func(string) bool, idAt func(uint32) string,
) []graphHit {
	// A SEGMENT WITH NOTHING TO READ IS ANSWERED BEFORE THE QUERY IS EXAMINED.
	// This ordering is deliberate and was arrived at by getting it wrong: the
	// width check below used to run first, which made an all-empty segment refuse
	// correctly-sized queries.
	//
	// THE REASON IS THAT AN EMPTY SEGMENT'S WIDTH IS INVENTED. batchVecBytes has
	// no vector to derive a width from when every document's vector is absent, so
	// it returns the package default and the sealed segment reports a vecBytes
	// that describes nothing. Comparing a real query against that placeholder
	// rejects correct callers for disagreeing with a number that was made up, and
	// the path is ordinary rather than exotic — the engine seals all-empty batches
	// while embedding is still draining.
	//
	// Nothing is weakened by answering first: with no vectors there is no read to
	// get wrong, and no metric is ever invoked. Every segment that actually holds
	// vectors still passes through the guard below.
	if ns.nodeCount() == 0 || k <= 0 {
		return nil
	}

	// THE QUERY IS VALIDATED AND CONVERTED ONCE, HERE. This is the only place
	// every vector-holding search passes through, so it is where the width
	// refusal belongs: both dtypes get the same check from the same line, and
	// neither arm can be reached with a query it would misread.
	q := vb.prepareQuery(query)

	if k > ef {
		ef = k
	}

	// ONE SCRATCH FOR THE WHOLE DESCENT, taken here rather than inside
	// greedyClosest so the pool is touched once per search instead of once per
	// layer. It sits after the early return above for the same reason the query
	// preparation does: an empty segment does no work and should acquire nothing.
	bs, _ := batchScratchPool.Get().(*batchScratch)

	ep := entryPoint
	for l := maxLevel; l > 0; l-- {
		ep = greedyClosest(vb, ns, &q, ep, l, bs)
	}
	batchScratchPool.Put(bs)

	results := searchLayer(vb, ns, &q, []uint32{ep}, ef, 0)

	items := make([]heapItem, results.Len())
	copy(items, *results)
	sort.Slice(items, func(i, j int) bool {
		return items[i].dist < items[j].dist
	})

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
			score:      scoreForDtype(vb.dtype, item.dist, vb.vecBytes),
		})
	}
	return out
}

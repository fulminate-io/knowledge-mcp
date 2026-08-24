// SPDX-License-Identifier: Apache-2.0

// Package hnsw is the client-side binary HNSW SegmentFormat for the segmented
// search engine. The pure graph algorithm (graph construction + beam search over
// Hamming distance) was originally copied from the server's binary HNSW with the
// store/server coupling removed: the graph here returns plain (externalID, score)
// hits, and the searchengine adapter (format.go) owns conversion to/from
// searchengine.Hit / searchengine.Document. Server-side search has since been
// retired, so this client format is the sole HNSW implementation. This package
// imports only the stdlib + searchengine — NO store, NO server internals, NO simd.
package hnsw

import (
	"math"
	"math/rand/v2"
	"slices"
)

// binaryGraph is an in-memory HNSW index for binary vector similarity search.
// Uses Hamming distance instead of cosine distance. Copied from the server's
// BinaryIndex with the store coupling removed.
type binaryGraph struct {
	nodes []hnswNode // indexed by internal uint32 ID
	// vectorBlock is EMBEDDED, not a field: nodeVector must stay a direct,
	// inlinable call on the hot path, and mappedGraph embeds the same type so
	// the shared traversal reads both forms identically. Promotion keeps
	// h.vectors and h.vecBytes reading exactly as before.
	vectorBlock
	m              int               // max neighbors per layer (e.g. 32)
	mMax0          int               // max neighbors at layer 0 (2*M)
	efConstruction int               // beam width during insert
	efSearch       int               // default beam width during search
	ml             float64           // level generation multiplier: 1/ln(M)
	maxLevel       int               // current highest layer in the index
	entryPoint     uint32            // entry node internal ID
	idMap          map[string]uint32 // external ID → internal ID
	rng            *rand.Rand

	// pruneScratch is a reusable candidate heap for addBidirectionalEdge's per-edge
	// neighbor prune. A binaryGraph is built by exactly ONE goroutine (the serial
	// builder; the across-segment concurrency is one graph per goroutine), so this
	// per-graph buffer is never shared across goroutines — it recycles the build's
	// hottest allocation with zero contention. selectNeighborsHeuristic sorts it in
	// place and returns a fresh slice without retaining it, so reuse is safe and
	// leaves the selected neighbors byte-identical.
	pruneScratch maxHeap

	// neighborArena backs every node's per-layer neighbor list from a handful of
	// large []uint32 slabs instead of one tiny make per list. Each (node,layer)
	// list draws a contiguous cap-(mLayer+1) window (arena.alloc); the +1 absorbs
	// addBidirectionalEdge's transient overflow append before the re-prune
	// truncates it back, so that append stays in-cap and never crosses into an
	// adjacent window. Slabs are append-only and never regrown, so a handed-out
	// window's backing array is stable for the whole build. Per-graph + single
	// goroutine (the build is serial; cross-segment concurrency is one graph per
	// goroutine), like pruneScratch — NOT a sync.Pool. Storage only: it relocates
	// the backing array, never the ids/order/length, so Encode stays byte-identical.
	neighborArena neighborArena

	// headerArena backs each node's neighbors [][]uint32 header (one per node,
	// length level+1) from shared slabs, the same append-only non-moving slab
	// strategy as neighborArena. A header is never appended to (a node's level is
	// fixed at insert), so an exact-length window suffices. Storage only — the
	// header still holds the same per-layer windows in the same order.
	headerArena headerArena
}

// headerSlabSize is the [][]uint32 capacity of each header-arena slab.
const headerSlabSize = 1024

// headerArena is the [][]uint32 analog of neighborArena: an append-only slab
// allocator handing out fixed-length neighbor-header windows with a stable
// backing array for the graph's lifetime.
type headerArena struct {
	slabs [][][]uint32
	cur   [][]uint32
	off   int
}

// alloc returns a length-n header window (each element nil) backed by the arena.
func (a *headerArena) alloc(n int) [][]uint32 {
	if n > headerSlabSize {
		s := make([][]uint32, n)
		a.slabs = append(a.slabs, s)
		return s
	}
	if a.cur == nil || a.off+n > len(a.cur) {
		a.cur = make([][]uint32, headerSlabSize)
		a.slabs = append(a.slabs, a.cur)
		a.off = 0
	}
	w := a.cur[a.off : a.off+n : a.off+n]
	a.off += n
	return w
}

// neighborSlabSize is the uint32 capacity of each neighbor-arena slab. A slab
// holds many neighbor windows (a layer-0 window is mMax0+1 ≈ 65 uint32s at the
// default M=32), so one 8192-uint32 slab backs ~126 layer-0 lists — amortizing the
// per-list allocation down to a per-slab one.
const neighborSlabSize = 8192

// neighborArena is an append-only slab allocator for []uint32 neighbor windows.
// It hands out contiguous len-0 windows carved sequentially from the current
// slab; when a request does not fit the slab's remaining tail, it seals the slab
// and starts a fresh one. Slabs are never regrown or moved, so every window it
// returns keeps a stable backing array for the graph's lifetime.
type neighborArena struct {
	slabs [][]uint32
	cur   []uint32 // current slab
	off   int      // next free offset within cur
}

// alloc returns a len-0, cap-capL window backed by the arena. capL must be > 0.
func (a *neighborArena) alloc(capL int) []uint32 {
	if capL > neighborSlabSize {
		// A window larger than a slab gets its own exact-fit slab (never happens at
		// default M, where the largest window is mMax0+1; defensive for huge M).
		s := make([]uint32, capL)
		a.slabs = append(a.slabs, s)
		return s[:0:capL]
	}
	if a.cur == nil || a.off+capL > len(a.cur) {
		a.cur = make([]uint32, neighborSlabSize)
		a.slabs = append(a.slabs, a.cur)
		a.off = 0
	}
	w := a.cur[a.off : a.off : a.off+capL]
	a.off += capL
	return w
}

// newBinaryGraph constructs an empty graph with the given HNSW parameters.
func newBinaryGraph(vecBytes, m, efConstruction int) *binaryGraph {
	return &binaryGraph{
		vectorBlock:    vectorBlock{vecBytes: vecBytes},
		m:              m,
		mMax0:          m * 2,
		efConstruction: efConstruction,
		efSearch:       defaultEfSearch,
		ml:             1.0 / math.Log(float64(m)),
		maxLevel:       -1,
		idMap:          make(map[string]uint32),
		rng:            newRand(),
	}
}

// randomLevel draws a random layer for a node from the inverse-log distribution.
func (h *binaryGraph) randomLevel() int {
	r := h.rng.Float64()
	if r == 0 {
		r = 1e-18
	}
	return int(math.Floor(-math.Log(r) * h.ml))
}

// Insert adds externalID's vector to the graph, building its layered neighbor
// lists. Re-inserting an existing externalID overwrites its vector in place.
func (h *binaryGraph) Insert(externalID string, vec []byte) {
	if existingID, exists := h.idMap[externalID]; exists {
		copy(h.nodeVector(existingID), vec)
		return
	}

	internalID := uint32(len(h.nodes))
	level := h.randomLevel()

	// Draw the per-layer header from the header arena (length level+1, elements
	// nil). The arena hands out non-overlapping, never-reused regions of a
	// zero-valued slab, so every element is already nil — no explicit nil-fill
	// needed. The per-layer windows are filled below from neighborArena.
	neighbors := h.headerArena.alloc(level + 1)

	h.nodes = append(h.nodes, hnswNode{
		externalID: externalID,
		maxLevel:   level,
		neighbors:  neighbors,
	})
	h.vectors = append(h.vectors, vec...)
	h.idMap[externalID] = internalID

	if h.maxLevel == -1 {
		h.maxLevel = level
		h.entryPoint = internalID
		return
	}

	ep := h.entryPoint
	for l := h.maxLevel; l > level; l-- {
		ep = greedyClosest(&h.vectorBlock, h, vec, ep, l)
	}

	topLayer := min(level, h.maxLevel)
	entryPoints := []uint32{ep}

	for l := topLayer; l >= 0; l-- {
		results := searchLayer(&h.vectorBlock, h, vec, entryPoints, h.efConstruction, l)

		mLayer := h.m
		if l == 0 {
			mLayer = h.mMax0
		}
		// Draw this node's layer-l neighbor window from the arena (cap mLayer+1 so a
		// later overflow append in addBidirectionalEdge stays in-cap). topLayer =
		// min(level, maxLevel) ≤ level, so l ≤ level holds for every iteration here
		// — the window is always stored, never a wasted transient. The returned slice
		// IS h.nodes[internalID].neighbors[l]; reusing it as entryPoints is a
		// read-only alias, safe because addBidirectionalEdge below only appends to the
		// NEIGHBORS' lists, never to this node's own list.
		dst := h.neighborArena.alloc(mLayer + 1)
		selectedNeighbors := h.selectNeighborsHeuristic(vec, results, mLayer, dst)

		if l <= level {
			h.nodes[internalID].neighbors[l] = selectedNeighbors
		}

		for _, neighborID := range selectedNeighbors {
			h.addBidirectionalEdge(internalID, neighborID, l, mLayer)
		}

		entryPoints = selectedNeighbors
	}

	if level > h.maxLevel {
		h.maxLevel = level
		h.entryPoint = internalID
	}
}

// addBidirectionalEdge adds an edge from neighborID back to nodeID, pruning the
// neighbor list back to mLayer entries by the selection heuristic if it overflows.
func (h *binaryGraph) addBidirectionalEdge(nodeID, neighborID uint32, layer, mLayer int) {
	node := &h.nodes[neighborID]
	if layer >= len(node.neighbors) {
		return
	}

	if slices.Contains(node.neighbors[layer], nodeID) {
		return
	}

	node.neighbors[layer] = append(node.neighbors[layer], nodeID)

	if len(node.neighbors[layer]) > mLayer {
		nVec := h.nodeVector(neighborID)
		// Reuse the per-graph scratch heap (reset to len 0, capacity carried forward)
		// instead of allocating a fresh candidate heap per prune — this is the build's
		// hottest allocation path (addBidirectionalEdge fires on every overflowing
		// edge insertion). The graph is single-goroutine-built, so the scratch is
		// uncontended.
		candidates := &h.pruneScratch
		*candidates = (*candidates)[:0]
		for _, id := range node.neighbors[layer] {
			d := hammingDistance(nVec, h.nodeVector(id))
			candidates.push(heapItem{id: id, dist: d})
		}
		// Re-prune in place: pruneScratch above already holds an independent copy of
		// the candidates, so the heuristic can overwrite node.neighbors[layer]'s own
		// window (passed as dst). The window keeps its arena backing across the
		// re-prune — no fresh allocation, no arena balloon. (A non-arena list — the
		// rare nil-first-append on a high layer — works identically: dst is
		// truncated and refilled regardless of where it is backed.)
		node.neighbors[layer] = h.selectNeighborsHeuristic(nVec, candidates, mLayer, node.neighbors[layer])
	}
}

// selectNeighborsHeuristic picks up to m neighbors from candidates using the
// HNSW diversity heuristic, backfilling with nearest unselected if short.
//
// dst is the caller-owned arena window the selected ids are written into (cap
// m+1, the +1 reserved for addBidirectionalEdge's later overflow append). It is
// truncated to len 0 and re-filled here, so the SAME window is reused across a
// re-prune (no fresh allocation, no arena balloon). The candidates heap must
// already be a copy independent of dst (the re-prune path copies node.neighbors
// into pruneScratch before calling), since dst is overwritten in place.
func (h *binaryGraph) selectNeighborsHeuristic(_ []byte, candidates *maxHeap, m int, dst []uint32) []uint32 {
	if candidates.Len() <= m {
		result := dst[:0]
		for i := range candidates.Len() {
			result = append(result, (*candidates)[i].id)
		}
		return result
	}

	// Sort the candidate heap IN PLACE (it is a disposable heap: searchLayer's
	// freshly-copied `out`, or the per-graph prune scratch — never shared across
	// goroutines), avoiding a per-call scratch-slice copy that the build's hottest
	// path (addBidirectionalEdge → here) otherwise repeats per edge. The in-place
	// sort touches the SAME elements in the SAME total order as a copy would, so the
	// selected neighbor list is byte-identical.
	//
	// slices.SortFunc (not sort.Slice) avoids reflect.Swapper boxing and its per-call
	// allocation on this hot path. Total-order comparator: distance first, then
	// internal id as the secondary key — an exact total order (heapItem.id is the
	// unique build ordinal and dist is integer-popcount Hamming, no float rounding),
	// so equal-distance ties resolve identically every run rather than landing in the
	// non-stable sort's input-dependent order, keeping Encode byte-reproducible.
	items := *candidates
	slices.SortFunc(items, func(a, b heapItem) int {
		if a.dist != b.dist {
			if a.dist < b.dist {
				return -1
			}
			return 1
		}
		switch {
		case a.id < b.id:
			return -1
		case a.id > b.id:
			return 1
		default:
			return 0
		}
	})

	selected := dst[:0]
	for _, item := range items {
		if len(selected) >= m {
			break
		}

		good := true
		for _, selID := range selected {
			distToSelected := hammingDistance(h.nodeVector(item.id), h.nodeVector(selID))
			if distToSelected < item.dist {
				good = false
				break
			}
		}
		if good {
			selected = append(selected, item.id)
		}
	}

	if len(selected) < m {
		// Backfill nearest unselected. `selected` is bounded by m (≈ mLayer, a few
		// dozen), so a linear membership scan is cheaper than allocating a
		// map[uint32]bool per call on this hot path — and selection order is
		// unchanged, so the result stays byte-identical.
		for _, item := range items {
			if len(selected) >= m {
				break
			}
			if !slices.Contains(selected, item.id) {
				selected = append(selected, item.id)
			}
		}
	}

	return selected
}

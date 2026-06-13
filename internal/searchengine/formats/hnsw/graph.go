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
	"sort"
	"sync"
)

// searchState holds the three allocations reused across searchLayer calls via
// searchStatePool.
type searchState struct {
	visited    bitset
	candidates minHeap
	results    maxHeap
}

// searchStatePool pools searchState objects to avoid per-call allocations in
// searchLayer.
var searchStatePool = sync.Pool{
	New: func() any { return &searchState{} },
}

// binaryGraph is an in-memory HNSW index for binary vector similarity search.
// Uses Hamming distance instead of cosine distance. Copied from the server's
// BinaryIndex with the store coupling removed.
type binaryGraph struct {
	nodes          []hnswNode        // indexed by internal uint32 ID
	vectors        []byte            // contiguous flat array: node i at [i*vecBytes : (i+1)*vecBytes]
	vecBytes       int               // bytes per vector (e.g. 32 for 256-bit)
	m              int               // max neighbors per layer (e.g. 32)
	mMax0          int               // max neighbors at layer 0 (2*M)
	efConstruction int               // beam width during insert
	efSearch       int               // default beam width during search
	ml             float64           // level generation multiplier: 1/ln(M)
	maxLevel       int               // current highest layer in the index
	entryPoint     uint32            // entry node internal ID
	idMap          map[string]uint32 // external ID → internal ID
	rng            *rand.Rand
}

// newBinaryGraph constructs an empty graph with the given HNSW parameters.
func newBinaryGraph(vecBytes, m, efConstruction int) *binaryGraph {
	return &binaryGraph{
		vecBytes:       vecBytes,
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

// nodeVector slices the binary vector for node id by computed byte offset.
func (h *binaryGraph) nodeVector(id uint32) []byte {
	start := int(id) * h.vecBytes
	return h.vectors[start : start+h.vecBytes]
}

// vectorByID returns the stored binary vector for an external id, or (nil,false)
// if the id is not indexed. It composes the two existing private members — the
// idMap external→internal lookup and nodeVector's internal-id offset read — since
// no existing method reads a vector by EXTERNAL id. The returned slice is a view
// into h.vectors (not a copy), matching nodeVector's own sub-slice contract; the
// caller treats it read-only (feeding it straight into Search as a query vector).
func (h *binaryGraph) vectorByID(externalID string) ([]byte, bool) {
	id, ok := h.idMap[externalID]
	if !ok {
		return nil, false
	}
	return h.nodeVector(id), true
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

	neighbors := make([][]uint32, level+1)
	for i := range neighbors {
		neighbors[i] = nil
	}

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
		ep = h.greedyClosest(vec, ep, l)
	}

	topLayer := min(level, h.maxLevel)
	entryPoints := []uint32{ep}

	for l := topLayer; l >= 0; l-- {
		results := h.searchLayer(vec, entryPoints, h.efConstruction, l)

		mLayer := h.m
		if l == 0 {
			mLayer = h.mMax0
		}
		selectedNeighbors := h.selectNeighborsHeuristic(vec, results, mLayer)

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
		candidates := &maxHeap{}
		for _, id := range node.neighbors[layer] {
			d := hammingDistance(nVec, h.nodeVector(id))
			candidates.push(heapItem{id: id, dist: d})
		}
		node.neighbors[layer] = h.selectNeighborsHeuristic(nVec, candidates, mLayer)
	}
}

// greedyClosest hill-climbs to the nearest node at a layer from entry point ep.
func (h *binaryGraph) greedyClosest(query []byte, ep uint32, layer int) uint32 {
	curr := ep
	currDist := hammingDistance(query, h.nodeVector(curr))

	for {
		changed := false
		node := &h.nodes[curr]
		if layer >= len(node.neighbors) {
			break
		}
		for _, neighborID := range node.neighbors[layer] {
			d := hammingDistance(query, h.nodeVector(neighborID))
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

// searchLayer runs bounded beam search at a single layer, returning up to ef
// nearest candidates as a maxHeap. Reuses a pooled searchState.
func (h *binaryGraph) searchLayer(query []byte, entryPoints []uint32, ef, layer int) *maxHeap {
	state, _ := searchStatePool.Get().(*searchState)

	// Clear and grow state from previous use.
	state.visited.grow(len(h.nodes))
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
		d := hammingDistance(query, h.nodeVector(ep))
		candidates.push(heapItem{id: ep, dist: d})
		results.push(heapItem{id: ep, dist: d})
	}

	for candidates.Len() > 0 {
		nearest := candidates.pop()

		if results.Len() >= ef && nearest.dist > results.peek().dist {
			break
		}

		node := &h.nodes[nearest.id]
		if layer >= len(node.neighbors) {
			continue
		}
		for _, neighborID := range node.neighbors[layer] {
			if state.visited.has(neighborID) {
				continue
			}
			state.visited.set(neighborID)

			d := hammingDistance(query, h.nodeVector(neighborID))

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

// selectNeighborsHeuristic picks up to m neighbors from candidates using the
// HNSW diversity heuristic, backfilling with nearest unselected if short.
func (h *binaryGraph) selectNeighborsHeuristic(_ []byte, candidates *maxHeap, m int) []uint32 {
	if candidates.Len() <= m {
		result := make([]uint32, candidates.Len())
		for i := range result {
			result[i] = (*candidates)[i].id
		}
		return result
	}

	items := make([]heapItem, candidates.Len())
	copy(items, *candidates)
	sort.Slice(items, func(i, j int) bool {
		return items[i].dist < items[j].dist
	})

	selected := make([]uint32, 0, m)
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
		selectedSet := make(map[uint32]bool, len(selected))
		for _, id := range selected {
			selectedSet[id] = true
		}
		for _, item := range items {
			if len(selected) >= m {
				break
			}
			if !selectedSet[item.id] {
				selected = append(selected, item.id)
				selectedSet[item.id] = true
			}
		}
	}

	return selected
}

// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"math"
	"runtime"
	"slices"
	"sync"
)

// binaryParallelBuilder wraps a binaryGraph with per-node locks for concurrent
// insertion. Copied from the server's binary_parallel.go.
type binaryParallelBuilder struct {
	h        *binaryGraph
	nodeMu   []sync.Mutex
	globalMu sync.RWMutex // protects h.maxLevel, h.entryPoint
}

// buildBinaryHNSWParallel constructs a binary HNSW graph using concurrent
// workers (runtime.NumCPU when workers <= 0). This IS the in-tree NumCPU
// worker-pool primitive for HNSW construction — the heavy graph-building work
// the format's Build delegates to.
func buildBinaryHNSWParallel(items []binaryBuildItem, vecBytes, m, efConstruction, workers int) *binaryGraph {
	n := len(items)
	if n == 0 {
		return newBinaryGraph(vecBytes, m, efConstruction)
	}
	if workers <= 0 {
		workers = runtime.NumCPU()
	}

	h := &binaryGraph{
		vecBytes:       vecBytes,
		m:              m,
		mMax0:          m * 2,
		efConstruction: efConstruction,
		efSearch:       defaultEfSearch,
		ml:             1.0 / math.Log(float64(m)),
		maxLevel:       -1,
		idMap:          make(map[string]uint32, n),
		nodes:          make([]hnswNode, n),
		vectors:        make([]byte, n*vecBytes),
		rng:            newRand(),
	}

	// Step 1: Pre-allocate all nodes, assign levels, copy vectors.
	for i, item := range items {
		level := h.randomLevel()
		h.nodes[i] = hnswNode{
			externalID: item.id,
			maxLevel:   level,
			neighbors:  make([][]uint32, level+1),
		}
		copy(h.vectors[i*vecBytes:(i+1)*vecBytes], item.vec)
		h.idMap[item.id] = uint32(i)

		if level > h.maxLevel {
			h.maxLevel = level
			h.entryPoint = uint32(i)
		}
	}

	if n == 1 {
		return h
	}

	// Step 2: Connect nodes concurrently.
	pb := &binaryParallelBuilder{
		h:      h,
		nodeMu: make([]sync.Mutex, n),
	}

	ch := make(chan uint32, n)
	for i := range n {
		if uint32(i) != h.entryPoint {
			ch <- uint32(i)
		}
	}
	close(ch)

	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		goWithRecover("buildBinaryHNSWParallel.connectNode", func() {
			defer wg.Done()
			for id := range ch {
				pb.connectNode(id)
			}
		})
	}
	wg.Wait()

	return h
}

// connectNode performs the HNSW insertion algorithm for a single node.
func (pb *binaryParallelBuilder) connectNode(internalID uint32) {
	h := pb.h
	node := &h.nodes[internalID]
	vec := h.nodeVector(internalID)
	level := node.maxLevel

	pb.globalMu.RLock()
	ep := h.entryPoint
	currentMaxLevel := h.maxLevel
	pb.globalMu.RUnlock()

	for l := currentMaxLevel; l > level; l-- {
		ep = pb.greedyClosest(vec, ep, l)
	}

	topLayer := min(level, currentMaxLevel)
	entryPoints := []uint32{ep}

	for l := topLayer; l >= 0; l-- {
		results := pb.searchLayer(vec, entryPoints, h.efConstruction, l)

		mLayer := h.m
		if l == 0 {
			mLayer = h.mMax0
		}
		selectedNeighbors := h.selectNeighborsHeuristic(vec, results, mLayer)

		if l <= level {
			pb.nodeMu[internalID].Lock()
			node.neighbors[l] = selectedNeighbors
			pb.nodeMu[internalID].Unlock()
		}

		for _, neighborID := range selectedNeighbors {
			pb.addBidirectionalEdge(internalID, neighborID, l, mLayer)
		}

		entryPoints = selectedNeighbors
	}

	if level > currentMaxLevel {
		pb.globalMu.Lock()
		if level > h.maxLevel {
			h.maxLevel = level
			h.entryPoint = internalID
		}
		pb.globalMu.Unlock()
	}
}

// greedyClosest finds the nearest node at a given layer with per-node locking.
func (pb *binaryParallelBuilder) greedyClosest(query []byte, ep uint32, layer int) uint32 {
	h := pb.h
	curr := ep
	currDist := hammingDistance(query, h.nodeVector(curr))

	for {
		changed := false

		pb.nodeMu[curr].Lock()
		var neighbors []uint32
		if layer < len(h.nodes[curr].neighbors) {
			neighbors = make([]uint32, len(h.nodes[curr].neighbors[layer]))
			copy(neighbors, h.nodes[curr].neighbors[layer])
		}
		pb.nodeMu[curr].Unlock()

		for _, neighborID := range neighbors {
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

// searchLayer performs beam search at a single layer with per-node locking.
func (pb *binaryParallelBuilder) searchLayer(query []byte, entryPoints []uint32, ef, layer int) *maxHeap {
	h := pb.h
	visited := newBitset(len(h.nodes))

	candidates := &minHeap{}
	results := &maxHeap{}

	for _, ep := range entryPoints {
		if visited.has(ep) {
			continue
		}
		visited.set(ep)
		d := hammingDistance(query, h.nodeVector(ep))
		candidates.push(heapItem{id: ep, dist: d})
		results.push(heapItem{id: ep, dist: d})
	}

	for candidates.Len() > 0 {
		nearest := candidates.pop()

		if results.Len() >= ef && nearest.dist > results.peek().dist {
			break
		}

		pb.nodeMu[nearest.id].Lock()
		var neighbors []uint32
		if layer < len(h.nodes[nearest.id].neighbors) {
			neighbors = make([]uint32, len(h.nodes[nearest.id].neighbors[layer]))
			copy(neighbors, h.nodes[nearest.id].neighbors[layer])
		}
		pb.nodeMu[nearest.id].Unlock()

		for _, neighborID := range neighbors {
			if visited.has(neighborID) {
				continue
			}
			visited.set(neighborID)

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

	return results
}

// addBidirectionalEdge adds an edge from neighborID back to nodeID with locking.
func (pb *binaryParallelBuilder) addBidirectionalEdge(nodeID, neighborID uint32, layer, mLayer int) {
	h := pb.h

	pb.nodeMu[neighborID].Lock()
	defer pb.nodeMu[neighborID].Unlock()

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

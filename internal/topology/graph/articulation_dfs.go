// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"context"

	"gonum.org/v1/gonum/graph"
)

// articulation_dfs.go contains the pure ITERATIVE Hopcroft-Tarjan
// articulation-points DFS used by the ArticulationAnalyzer. The file
// is REQUIRED to be iterative — there must be no recursive helper
// function. The reason is purely operational: pathological graphs in
// our codebase (deep import chains, long Helm release histories,
// multi-thousand-node dependency walks) routinely exceed Go's
// per-goroutine stack limit when a recursive DFS reaches depths in the
// 50k-100k range. Go grows goroutine stacks dynamically, but each
// frame for a closure-heavy DFS is large enough that the runtime hits
// the 1GB hard cap on long chains well before the algorithm finishes.
//
// The iterative algorithm uses an EXPLICIT STACK of dfsFrame values,
// each representing one "function call" in the recursive analog. The
// outer loop pops a frame, processes the next unvisited child if any
// (push a new frame), or pops the frame and propagates low-link state
// upward when all children are exhausted. The shape mirrors a classic
// Hopcroft-Tarjan but every recursive jump is rewritten as a stack push.
//
// In addition to articulation point detection, this file ALSO captures
// per-AP split-component sizes during the DFS so computeBlastScore can
// score each AP in O(1) instead of O(V+E). The earlier per-AP BFS
// approach made the deep-chain stress test (10k nodes) overshoot its
// 30s budget by 25 seconds. The DFS-time capture exploits a standard
// Hopcroft-Tarjan property: when low[child] >= disc[parent] fires, the
// stranded component on AP removal is exactly the child's DFS subtree.
//
// Public surface: findArticulationPoints(g, totalNodes) returns the
// populated articulationState (so computeBlastScore can read
// precomputed component sizes) and a slice of articulation point
// gonum int64 IDs in undefined order.
//
// Complexity: O(V + E) time, O(V) space.

// dfsFrame represents one "stack frame" of the iterative DFS.
type dfsFrame struct {
	nodeID       int64
	parent       int64
	neighbors    graph.Nodes
	children     int
	pendingChild int64
	hasPending   bool
}

// articulationState holds the per-DFS arrays the algorithm threads
// through every frame. Allocated once per findArticulationPoints call
// and reused across DFS root walks (a disconnected graph triggers
// multiple DFS roots).
type articulationState struct {
	disc           []int64
	low            []int64
	subtreeSize    []int
	timer          int64
	articulation   map[int64]struct{}
	componentSizes map[int64][]int
	componentTotal map[int64]int
	rootOf         map[int64]int64
	idIndex        map[int64]int
}

// findArticulationPoints runs an iterative Hopcroft-Tarjan DFS over
// the undirected view of g and returns the populated articulationState
// plus a slice of articulation point gonum int64 IDs in undefined
// order. totalNodes is the materialized node count from the
// GonumGraph; we use it to size the disc/low arrays without
// re-iterating g.Nodes().
func findArticulationPoints(ctx context.Context, g graph.Undirected, totalNodes int) (*articulationState, []int64) {
	state := &articulationState{
		disc:           make([]int64, totalNodes),
		low:            make([]int64, totalNodes),
		subtreeSize:    make([]int, totalNodes),
		articulation:   make(map[int64]struct{}),
		componentSizes: make(map[int64][]int),
		componentTotal: make(map[int64]int),
		rootOf:         make(map[int64]int64, totalNodes),
		idIndex:        make(map[int64]int, totalNodes),
	}
	for i := range state.disc {
		state.disc[i] = -1
		state.low[i] = -1
	}

	nodes := g.Nodes()
	idx := 0
	for nodes.Next() {
		state.idIndex[nodes.Node().ID()] = idx
		idx++
	}

	for rootID := range state.idIndex {
		if ctx.Err() != nil {
			break
		}
		if state.disc[state.idIndex[rootID]] != -1 {
			continue
		}
		runDFSFromRoot(ctx, g, state, rootID)
	}

	out := make([]int64, 0, len(state.articulation))
	for id := range state.articulation {
		out = append(out, id)
	}
	return state, out
}

// runDFSFromRoot performs one iterative DFS walk starting at rootID,
// updating state in place. The outer for loop manages an explicit
// stack of dfsFrame values; each iteration handles three cases: (1)
// resume after a child subtree finished, (2) pull the next neighbor
// and either push a child frame or update low via a back edge, (3)
// pop the frame when its neighbor iterator is exhausted.
func runDFSFromRoot(ctx context.Context, g graph.Undirected, state *articulationState, rootID int64) {
	rootIdx := state.idIndex[rootID]
	state.disc[rootIdx] = state.timer
	state.low[rootIdx] = state.timer
	state.subtreeSize[rootIdx] = 1
	state.rootOf[rootID] = rootID
	state.timer++

	stack := []dfsFrame{{
		nodeID:    rootID,
		parent:    -1,
		neighbors: g.From(rootID),
	}}

	steps := 0
	for len(stack) > 0 {
		steps++
		if steps&1023 == 0 && ctx.Err() != nil {
			return
		}
		topIdx := len(stack) - 1
		top := &stack[topIdx]

		if top.hasPending {
			applyChildReturn(state, top)
		}

		if !top.neighbors.Next() {
			if top.parent == -1 {
				state.componentTotal[top.nodeID] = state.subtreeSize[state.idIndex[top.nodeID]]
			}
			stack = stack[:topIdx]
			continue
		}
		neighbor := top.neighbors.Node().ID()
		if neighbor == top.parent {
			continue
		}
		nbIdx := state.idIndex[neighbor]
		if state.disc[nbIdx] != -1 {
			if state.disc[nbIdx] < state.low[state.idIndex[top.nodeID]] {
				state.low[state.idIndex[top.nodeID]] = state.disc[nbIdx]
			}
			continue
		}
		state.disc[nbIdx] = state.timer
		state.low[nbIdx] = state.timer
		state.subtreeSize[nbIdx] = 1
		state.rootOf[neighbor] = rootID
		state.timer++
		top.children++
		top.pendingChild = neighbor
		top.hasPending = true
		stack = append(stack, dfsFrame{
			nodeID:    neighbor,
			parent:    top.nodeID,
			neighbors: g.From(neighbor),
		})
	}
}

// applyChildReturn handles the bookkeeping that, in a recursive DFS,
// would happen "after the recursive call returns": propagate the
// child's low link up to the parent, fold the child's subtree size
// into the parent's, and if the parent is a non-root node whose low
// link is now >= the parent's discovery time, mark the parent as an
// articulation point AND record the child's subtree size as a
// stranded component. Root frames apply the >= 2 children rule and
// record EACH of their children's subtree sizes.
func applyChildReturn(state *articulationState, top *dfsFrame) {
	parentIdx := state.idIndex[top.nodeID]
	childIdx := state.idIndex[top.pendingChild]
	if state.low[childIdx] < state.low[parentIdx] {
		state.low[parentIdx] = state.low[childIdx]
	}
	state.subtreeSize[parentIdx] += state.subtreeSize[childIdx]

	if top.parent != -1 && state.low[childIdx] >= state.disc[parentIdx] {
		state.articulation[top.nodeID] = struct{}{}
		state.componentSizes[top.nodeID] = append(
			state.componentSizes[top.nodeID],
			state.subtreeSize[childIdx],
		)
	}
	if top.parent == -1 {
		if top.children >= 2 {
			state.articulation[top.nodeID] = struct{}{}
		}
		state.componentSizes[top.nodeID] = append(
			state.componentSizes[top.nodeID],
			state.subtreeSize[childIdx],
		)
	}
	top.hasPending = false
	top.pendingChild = 0
}

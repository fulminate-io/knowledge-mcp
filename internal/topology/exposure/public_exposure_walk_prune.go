// SPDX-License-Identifier: Apache-2.0

package exposure

// public_exposure_walk_prune.go houses the min-heap and pruneToTopN helper
// that caps a scored-path slice at the analyzer's top-N limit without
// allocating a full sort. Split out of public_exposure_walk.go so the
// BFS file stays under the 300-line soft cap.

import "container/heap"

// pruneToTopN retains the top-N scored paths by composite score using a
// min-heap. Callers pass scored paths already computed by the scoring
// module; this helper is a pure transform that never touches the scoring
// function. Returns the input unchanged when len(paths) <= n, so callers
// can always use the return value without a length check.
//
// Note: output ordering is NOT stable — the heap surfaces a set, not a
// sorted list. Callers that need determinism should sort the result by
// composite score descending; scorePaths already does this before
// pruneToTopN is invoked, but the heap re-scrambles the tail.
func pruneToTopN(paths []scoredPath, n int) []scoredPath {
	if n <= 0 || len(paths) <= n {
		return paths
	}
	h := &scoredPathMinHeap{}
	heap.Init(h)
	for _, p := range paths {
		if h.Len() < n {
			heap.Push(h, p)
			continue
		}
		if (*h)[0].CompositeScore < p.CompositeScore {
			heap.Pop(h)
			heap.Push(h, p)
		}
	}
	out := make([]scoredPath, h.Len())
	for i := h.Len() - 1; i >= 0; i-- {
		sp, ok := heap.Pop(h).(scoredPath)
		if !ok {
			// Unreachable: the heap is typed as scoredPathMinHeap so every
			// Pop value originates from Push(scoredPath). Defensive bail-out
			// keeps the comma-ok form that satisfies errcheck.
			return out[i+1:]
		}
		out[i] = sp
	}
	return out
}

// scoredPathMinHeap is a heap.Interface sorted by ascending
// CompositeScore, used by pruneToTopN to shed the weakest paths first.
type scoredPathMinHeap []scoredPath

func (h scoredPathMinHeap) Len() int           { return len(h) }
func (h scoredPathMinHeap) Less(i, j int) bool { return h[i].CompositeScore < h[j].CompositeScore }
func (h scoredPathMinHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *scoredPathMinHeap) Push(x any) {
	sp, ok := x.(scoredPath)
	if !ok {
		// Unreachable: pruneToTopN always pushes scoredPath. Defensive
		// comma-ok cast satisfies errcheck without breaking the
		// heap.Interface contract.
		return
	}
	*h = append(*h, sp)
}
func (h *scoredPathMinHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

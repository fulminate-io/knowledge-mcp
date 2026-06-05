// SPDX-License-Identifier: Apache-2.0

package bm25

// scoredDoc pairs a segment-local internal doc ID with its BM25 score. Ported
// from cmd/knowledge-server/internal/index/bm25/heap.go — pure top-k machinery.
type scoredDoc struct {
	id    uint32
	score float64
}

// scoredDocHeap is a min-heap of scoredDoc ordered by score, used for efficient
// top-k selection (the root is the smallest score, so it is the first to evict
// once the heap is full).
type scoredDocHeap []scoredDoc

func (h scoredDocHeap) Len() int           { return len(h) }
func (h scoredDocHeap) Less(i, j int) bool { return h[i].score < h[j].score } // min-heap
func (h scoredDocHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *scoredDocHeap) Push(x any)        { v, _ := x.(scoredDoc); *h = append(*h, v) }

// Pop removes and returns the minimum-score document from the heap.
func (h *scoredDocHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

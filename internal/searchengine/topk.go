package searchengine

import "sort"

// HitHeap is a bounded MIN-heap of Hit keyed by Score. Because Score is
// higher-is-better, the minimum (worst) kept hit sits at the root and is the one
// evicted when a better hit arrives once the heap is full. Hand-rolled
// siftUp/siftDown (no container/heap boilerplate) — the merge is small and the
// idiom is clearer inline.
//
// EXPORTED FOR THE ONE SHAPE IT IS, not as a general utility: "stream an
// unbounded number of scored candidates past a k-sized window and keep the best
// k". mergeTopK below is that shape over per-segment search hits; the thought
// package's within-topic kNN densifier is the same shape over co-member
// similarities, and it previously built each member's whole candidate list and
// sorted it. A caller constructs the heap with make(HitHeap, 0, k) and drives it
// with Offer.
type HitHeap []Hit

func (h HitHeap) less(i, j int) bool { return h[i].Score < h[j].Score }

func (h HitHeap) siftUp(i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if !h.less(i, parent) {
			break
		}
		h[i], h[parent] = h[parent], h[i]
		i = parent
	}
}

func (h HitHeap) siftDown(i int) {
	n := len(h)
	for {
		left := 2*i + 1
		if left >= n {
			break
		}
		smallest := left
		if right := left + 1; right < n && h.less(right, left) {
			smallest = right
		}
		if !h.less(smallest, i) {
			break
		}
		h[i], h[smallest] = h[smallest], h[i]
		i = smallest
	}
}

// Offer admits hit into a bounded heap of capacity k. While under capacity it
// pushes; once full it replaces the root only if hit beats the current minimum.
//
// THE COMPARISON IS STRICT, so a newcomer that TIES the worst kept hit is
// REJECTED and the incumbent survives — ties are broken by ARRIVAL ORDER, first
// offered wins. A caller that needs a tie broken by ID instead gets it for free
// by offering in ascending ID order, which is what makes this interchangeable
// with a "sort by score descending, then by ID ascending, then cut" selection.
func (h *HitHeap) Offer(hit Hit, k int) {
	if k <= 0 {
		return
	}
	if len(*h) < k {
		*h = append(*h, hit)
		h.siftUp(len(*h) - 1)
		return
	}
	if hit.Score > (*h)[0].Score {
		(*h)[0] = hit
		h.siftDown(0)
	}
}

// mergeTopK merges per-segment hit slices into the global top-k, sorted by Score
// descending (ties broken by ID ascending for determinism). O(total*log k), no
// full sort of the union. Returns fewer than k results when the union is smaller.
func mergeTopK(perSegment [][]Hit, k int) []Hit {
	if k <= 0 {
		return nil
	}
	h := make(HitHeap, 0, k)
	for _, hits := range perSegment {
		for _, hit := range hits {
			h.Offer(hit, k)
		}
	}
	out := []Hit(h)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	return out
}

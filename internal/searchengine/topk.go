package searchengine

import "sort"

// hitHeap is a bounded MIN-heap of Hit keyed by Score. Because Score is
// higher-is-better, the minimum (worst) kept hit sits at the root and is the one
// evicted when a better hit arrives once the heap is full. Hand-rolled
// siftUp/siftDown (no container/heap boilerplate) — the merge is small and the
// idiom is clearer inline.
type hitHeap []Hit

func (h hitHeap) less(i, j int) bool { return h[i].Score < h[j].Score }

func (h hitHeap) siftUp(i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if !h.less(i, parent) {
			break
		}
		h[i], h[parent] = h[parent], h[i]
		i = parent
	}
}

func (h hitHeap) siftDown(i int) {
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

// offer admits hit into a bounded heap of capacity k. While under capacity it
// pushes; once full it replaces the root only if hit beats the current minimum.
func (h *hitHeap) offer(hit Hit, k int) {
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
	h := make(hitHeap, 0, k)
	for _, hits := range perSegment {
		for _, hit := range hits {
			h.offer(hit, k)
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

// SPDX-License-Identifier: Apache-2.0

package hnsw

// heapItem holds a node's internal ID and its distance from the query.
type heapItem struct {
	id   uint32
	dist float32
}

// minHeap returns nearest items first (for candidate set C in HNSW search).
// Uses typed inline heap operations — no interface boxing.
type minHeap []heapItem

func (h minHeap) Len() int           { return len(h) }
func (h minHeap) Less(i, j int) bool { return h[i].dist < h[j].dist }

func (h *minHeap) push(item heapItem) {
	*h = append(*h, item)
	h.siftUp(len(*h) - 1)
}

func (h *minHeap) pop() heapItem {
	old := *h
	n := len(old)
	item := old[0]
	old[0] = old[n-1]
	*h = old[:n-1]
	h.siftDown(0)
	return item
}

func (h *minHeap) siftUp(i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if (*h)[parent].dist <= (*h)[i].dist {
			break
		}
		(*h)[parent], (*h)[i] = (*h)[i], (*h)[parent]
		i = parent
	}
}

func (h *minHeap) siftDown(i int) {
	n := len(*h)
	for {
		left := 2*i + 1
		if left >= n {
			break
		}
		smallest := left
		if right := left + 1; right < n && (*h)[right].dist < (*h)[left].dist {
			smallest = right
		}
		if (*h)[i].dist <= (*h)[smallest].dist {
			break
		}
		(*h)[i], (*h)[smallest] = (*h)[smallest], (*h)[i]
		i = smallest
	}
}

// bitset is a compact set of uint32 IDs backed by a []uint64 bitmap.
// Used to replace map[uint32]bool for visited nodes in HNSW search.
type bitset []uint64

func (b bitset) set(i uint32)      { b[i/64] |= 1 << (i % 64) }
func (b bitset) has(i uint32) bool { return b[i/64]&(1<<(i%64)) != 0 }

func (b bitset) clearAll() {
	clear(b)
}

func (b *bitset) grow(n int) {
	need := (n + 63) / 64
	if need > len(*b) {
		*b = append(*b, make([]uint64, need-len(*b))...)
	}
}

// maxHeap returns furthest items first (for result set W, bounded to ef).
// Uses typed inline heap operations — no interface boxing.
type maxHeap []heapItem

func (h maxHeap) Len() int           { return len(h) }
func (h maxHeap) Less(i, j int) bool { return h[i].dist > h[j].dist }

func (h *maxHeap) push(item heapItem) {
	*h = append(*h, item)
	h.siftUp(len(*h) - 1)
}

func (h *maxHeap) pop() heapItem { //nolint:unparam // return value used by some callers, evict-only callers ignore it
	old := *h
	n := len(old)
	item := old[0]
	old[0] = old[n-1]
	*h = old[:n-1]
	h.siftDown(0)
	return item
}

func (h maxHeap) peek() heapItem { return h[0] }

func (h *maxHeap) siftUp(i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if (*h)[parent].dist >= (*h)[i].dist {
			break
		}
		(*h)[parent], (*h)[i] = (*h)[i], (*h)[parent]
		i = parent
	}
}

func (h *maxHeap) siftDown(i int) {
	n := len(*h)
	for {
		left := 2*i + 1
		if left >= n {
			break
		}
		largest := left
		if right := left + 1; right < n && (*h)[right].dist > (*h)[left].dist {
			largest = right
		}
		if (*h)[i].dist >= (*h)[largest].dist {
			break
		}
		(*h)[i], (*h)[largest] = (*h)[largest], (*h)[i]
		i = largest
	}
}

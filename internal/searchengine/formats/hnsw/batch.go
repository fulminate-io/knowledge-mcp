// SPDX-License-Identifier: Apache-2.0

package hnsw

import "sync"

// batch.go owns the TWO-PASS NEIGHBOR SCORING and nothing else — it lives apart
// from traverse.go so that file stays well inside the package's 500-line cap.
//
// WHY TWO PASSES. The traversal used to score one neighbor at a time, which left
// the fused gather kernel's advantage unclaimed: veckernel's own measurements
// record an UNFUSED batch running 1.5-1.7x slower than the fused one, and the
// fusion is only reachable when a caller hands the kernel a whole run of ids at
// once. Holding a query chunk in registers across four candidate rows is what
// that buys. Batching also amortizes the fixed per-distance term — measured at
// 38% of the dim-256 cost on arm64 and 50% on amd64 — which no amount of kernel
// arithmetic can remove, and it gives prefetch somewhere to hide its latency,
// which a one-at-a-time loop does not.
//
// WHAT THIS FILE DELIBERATELY DOES NOT DO: branch on dtype. The per-segment
// batchScore resolved by setDtype (dtype.go) already knows which metric this
// block uses and already handles both arms, so pass 2 is ONE call. A dtype
// branch here would be a second dispatch site, which is exactly the drift the
// phase-1 rework removed.

// batchScratch is the reusable buffer pair the two-pass scoring needs: the ids
// collected in pass 1 and the distances written by pass 2.
//
// IT IS SCRATCH, NOT STATE. Nothing in it survives a call; it exists so a run of
// up to 2*M neighbors is scored without allocating per hop. searchState embeds
// one, so a layer search reuses the pooled buffers it already holds; callers that
// have no searchState (the greedy descent) take one from batchScratchPool for
// the whole descent rather than per hop.
type batchScratch struct {
	ids  []uint32
	dist []float32
}

// prepare sizes both buffers for a run of at most n ids and empties the id list.
//
// dist is sized to n rather than to the eventual id count because pass 1 has not
// run yet — n is the run length, and the surviving ids are a subset of it. The
// scorer is then handed exactly dist[:len(ids)], which is what keeps
// DotF32Gather's "dst must not be shorter than ids" precondition satisfied by
// construction rather than by a check at the call site.
func (b *batchScratch) prepare(n int) {
	if cap(b.ids) < n {
		b.ids = make([]uint32, 0, n)
	}
	b.ids = b.ids[:0]
	if cap(b.dist) < n {
		b.dist = make([]float32, n)
	}
	b.dist = b.dist[:n]
}

// batchScratchPool supplies scratch to callers that have no searchState of their
// own. A caller takes ONE for a whole descent and reuses it across layers and
// hops, so the pool is touched once per search rather than once per run.
var batchScratchPool = sync.Pool{
	New: func() any { return &batchScratch{} },
}

// scoreCollected is PASS 2: score every id collected in b.ids against the
// prepared query in ONE call, and return the distances aligned to b.ids.
//
// The returned slice ALIASES b.dist and is only valid until the next prepare, so
// callers consume it before collecting the next run. Lower is nearer for both
// dtypes — batchScore writes the same convention the scalar metric does, and a
// test gates that the two agree on values and on ordering.
func scoreCollected(vb *vectorBlock, q *preparedQuery, b *batchScratch) []float32 {
	if len(b.ids) == 0 {
		return nil
	}
	dst := b.dist[:len(b.ids)]
	vb.batchScore(dst, q, vb, b.ids)
	return dst
}

// collectUnvisited is PASS 1 for a beam-search expansion: walk the neighbor run,
// skip ids already visited, MARK each survivor visited, and collect it.
//
// THE VISITED BIT IS SET HERE, IN PASS 1, AND THAT ORDERING IS LOAD-BEARING.
// Deferring it to pass 2 would let the same id appear twice inside one collected
// run — scored twice and pushed twice onto both heaps. It could not happen in
// the one-at-a-time loop, because there the mark and the score were the same
// step, so no existing test asserts against it. Keeping the mark in pass 1 is
// what preserves that guarantee across the restructure.
func collectUnvisited(state *searchState, run []uint32) {
	state.prepare(len(run))
	for _, id := range run {
		if state.visited.has(id) {
			continue
		}
		state.visited.set(id)
		state.ids = append(state.ids, id)
	}
}

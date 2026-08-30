// SPDX-License-Identifier: Apache-2.0

package veckernel

import "fmt"

// kernel.go is the package's entire public compute surface: two functions, the
// two shapes an HNSW traversal actually has.

// DotF32 returns the dot product of a and b.
//
// Voyage-class embeddings are pre-normalized, so cosine similarity IS the dot
// product — this package deliberately exposes no separate cosine entry point,
// because a second entry point that divides by norms nobody needs is a second
// kernel to hand-write, hand-verify and keep in agreement per tier.
//
// PANICS when the lengths differ, naming both. A length mismatch is a caller
// defect and the assembly tiers do not bounds-check; silently truncating to the
// shorter slice would return a wrong distance, and a wrong distance ranks wrong
// without ever looking like an error.
//
// Non-finite inputs are NOT screened. See the package doc for the policy and
// nonfinite_test.go for the measurements it was written from.
func DotF32(a, b []float32) float32 {
	if len(a) != len(b) {
		panic(fmt.Sprintf("veckernel: DotF32 length mismatch: len(a)=%d len(b)=%d", len(a), len(b)))
	}
	if len(a) == 0 {
		return 0
	}
	return active.dot(a, b)
}

// DotF32Gather writes dst[i] = DotF32(query, block[ids[i]*dim:][:dim]).
//
// THE ABI IS AN ID LIST OVER A FLAT BLOCK, and that is a measured constraint
// rather than a taste. Two alternatives were measured in the kernel shoot-out
// and both lose:
//
//   - A CONTIGUOUS-BUFFER batch (m adjacent vectors) forces the caller to gather
//     first, because graph neighbors are arbitrary scattered node ids. That copy
//     measured 25.8 / 67.5 / 204.9 ns per distance at 512 / 1024 / 2048 dims —
//     several times the per-call overhead it was meant to amortize.
//   - A `func(id uint32) []float32` accessor cannot be called from assembly, so
//     the per-row loop is forced back into Go and the assembly can only ever see
//     ONE ROW AT A TIME. That forfeits four-row fusion — holding a query chunk in
//     a register across four candidate rows — which the shoot-out measured as
//     the decisive factor, worth 1.5-1.7x, larger than the language or
//     call-boundary differences the gap was first attributed to.
//
// A flat block plus an id list is exactly the shape the traversal's vector block
// already has, so wiring costs a typed view and no copy at all.
//
// PANICS, naming the offending value, on: a non-positive dim, a query whose
// length is not dim, a dst shorter than ids, a block that is not a whole number
// of dim-wide vectors, or an id whose vector would run past the block. The
// assembly tiers compute row addresses themselves and do not bounds-check, so
// this validation is the only thing between a bad id and a wild read.
func DotF32Gather(dst, query, block []float32, dim int, ids []uint32) {
	if dim <= 0 {
		panic(fmt.Sprintf("veckernel: DotF32Gather dim must be positive, got %d", dim))
	}
	if len(query) != dim {
		panic(fmt.Sprintf("veckernel: DotF32Gather len(query)=%d does not match dim=%d", len(query), dim))
	}
	if len(dst) < len(ids) {
		panic(fmt.Sprintf("veckernel: DotF32Gather len(dst)=%d is shorter than len(ids)=%d", len(dst), len(ids)))
	}
	if len(block)%dim != 0 {
		panic(fmt.Sprintf("veckernel: DotF32Gather len(block)=%d is not a multiple of dim=%d", len(block), dim))
	}
	rows := len(block) / dim
	for _, id := range ids {
		if int(id) >= rows {
			panic(fmt.Sprintf("veckernel: DotF32Gather id %d out of range: block holds %d rows of dim %d",
				id, rows, dim))
		}
	}
	if len(ids) == 0 {
		return
	}
	active.gather(dst[:len(ids)], query, block, dim, ids)
}

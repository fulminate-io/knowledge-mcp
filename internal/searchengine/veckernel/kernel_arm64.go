// SPDX-License-Identifier: Apache-2.0

//go:build arm64 && !veckernel_noasm

package veckernel

import "golang.org/x/sys/cpu"

// kernel_arm64.go is the Go side of the AArch64 Advanced SIMD arm: the assembly
// declarations, the slice-to-pointer shims, and the CPU gate.

//go:noescape
func dotF32NEON(a, b *float32, n int) float32

// dotF32x4NEON scores ONE query against FOUR rows in a single pass, holding
// each query chunk in a register across all four.
//
// This four-row fusion is the arm's reason for existing. The shoot-out measured
// an unfused batch — one that re-reads the query per row — losing 1.5-1.7x to a
// fused one, a larger effect than the language or call-boundary differences the
// gap was first attributed to. A batch ABI that cannot express four rows at once
// cannot reach this number.
//
//go:noescape
func dotF32x4NEON(q, r0, r1, r2, r3, p0, p1, p2, p3 *float32, n int) (d0, d1, d2, d3 float32)

// neonDot is the scalar entry point. DotF32 has already rejected mismatched
// lengths and short-circuited the empty case, so &a[0] and &b[0] are live.
func neonDot(a, b []float32) float32 {
	return dotF32NEON(&a[0], &b[0], len(a))
}

// neonGather walks the id list in groups of four through the fused kernel and
// finishes the remainder one row at a time.
//
// NO COPY HAPPENS HERE. Each row is a pointer computed straight into the caller's
// block; the ids may be arbitrarily scattered and it costs nothing extra, which
// is the property the contiguous-buffer ABI could not have.
//
// DotF32Gather has already validated dim, the query length, dst's length and
// every id against the block, so the indexing below cannot run off the end.
func neonGather(dst, query, block []float32, dim int, ids []uint32) {
	q := &query[0]

	// THE SCHEDULE QUESTION IS ASKED ONCE, HERE, not per group. prefetchTargets
	// must stay inlinable (see its doc), and it only does so while the fast path
	// carries no schedule logic; branching on this loop-invariant bool is what
	// keeps the general case reachable without paying for it every group.
	pfDefault := pfScheduleIsDefault()

	i := 0
	for ; i+4 <= len(ids); i += 4 {
		r0 := &block[int(ids[i+0])*dim]
		r1 := &block[int(ids[i+1])*dim]
		r2 := &block[int(ids[i+2])*dim]
		r3 := &block[int(ids[i+3])*dim]
		var p0, p1, p2, p3 *float32
		if pfDefault {
			p0, p1, p2, p3 = prefetchTargets(block, dim, ids, i, r0, r1, r2, r3)
		} else {
			p0, p1, p2, p3 = prefetchTargetsScheduled(block, dim, ids, i, r0, r1, r2, r3)
		}
		dst[i+0], dst[i+1], dst[i+2], dst[i+3] = dotF32x4NEON(q, r0, r1, r2, r3, p0, p1, p2, p3, dim)
	}
	// The group remainder: 1, 2 or 3 trailing ids. A neighbor run is whatever
	// length the graph made it, so this path runs on real traversals constantly
	// rather than only at the edges — the gather agreement tests sweep id-list
	// lengths across every residue for that reason.
	for ; i < len(ids); i++ {
		off := int(ids[i]) * dim
		dst[i] = dotF32NEON(q, &block[off], dim)
	}
}

// asmArms reports the assembly tiers compiled into this binary, each carrying
// whether THIS CPU can execute it.
//
// An unsupported tier stays in the table rather than being dropped. Dropping it
// would make "this CPU lacks the feature" and "this build has no such kernel"
// indistinguishable at the force seam, and those are different bugs with
// different fixes.
//
// ASIMD is architecturally mandatory in ARMv8-A and x/sys/cpu's minimal-feature
// path sets HasASIMD unconditionally on arm64, so in practice this gate always
// opens. It is still WRITTEN as a gate rather than assumed, because what the
// package sells is that the tier which ran is the tier that is reported — and a
// gate asserted at every call site costs nothing to keep honest.
func asmArms() []arm {
	return []arm{{
		name:      TierNEON,
		dot:       neonDot,
		gather:    neonGather,
		supported: cpu.ARM64.HasASIMD,
		why:       "CPU does not report Advanced SIMD (ASIMD/NEON) support",
	}}
}

// SPDX-License-Identifier: Apache-2.0

//go:build amd64 && !veckernel_noasm

package veckernel

import "golang.org/x/sys/cpu"

// kernel_amd64.go is the Go side of the two amd64 arms: the slice-to-pointer
// shims over the avo-generated kernels, the CPU gates, and the ORDER dispatch
// prefers them in.
//
// The assembly itself is generated — see avogen/ and dot_avx_amd64.s. avo is a
// generator-only dependency in its own module; the .s and stub .go files are
// committed, so an ordinary build never downloads it.

// -- AVX2/FMA ---------------------------------------------------------------

// avx2Dot is the scalar entry point. DotF32 has already rejected mismatched
// lengths and short-circuited the empty case, so &a[0] and &b[0] are live.
func avx2Dot(a, b []float32) float32 {
	return dotF32AVX2(&a[0], &b[0], len(a))
}

// avx2Gather walks the id list in groups of four through the fused kernel and
// finishes the remainder one row at a time.
//
// NO COPY HAPPENS HERE. Each row is a pointer computed straight into the
// caller's block; the ids may be arbitrarily scattered and it costs nothing
// extra, which is the property the contiguous-buffer ABI could not have.
//
// DotF32Gather has already validated dim, the query length, dst's length and
// every id against the block, so the indexing below cannot run off the end.
func avx2Gather(dst, query, block []float32, dim int, ids []uint32) {
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
		dst[i+0], dst[i+1], dst[i+2], dst[i+3] = dotF32x4AVX2(q, r0, r1, r2, r3, p0, p1, p2, p3, dim)
	}
	// The group remainder: 1, 2 or 3 trailing ids. A neighbor run is whatever
	// length the graph made it, so this path runs on real traversals constantly
	// rather than only at the edges — the gather agreement tests sweep id-list
	// lengths across every residue for that reason.
	for ; i < len(ids); i++ {
		off := int(ids[i]) * dim
		dst[i] = dotF32AVX2(q, &block[off], dim)
	}
}

// -- AVX-512F ---------------------------------------------------------------

func avx512Dot(a, b []float32) float32 {
	return dotF32AVX512(&a[0], &b[0], len(a))
}

func avx512Gather(dst, query, block []float32, dim int, ids []uint32) {
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
		dst[i+0], dst[i+1], dst[i+2], dst[i+3] = dotF32x4AVX512(q, r0, r1, r2, r3, p0, p1, p2, p3, dim)
	}
	for ; i < len(ids); i++ {
		off := int(ids[i]) * dim
		dst[i] = dotF32AVX512(q, &block[off], dim)
	}
}

// -- feature gates ----------------------------------------------------------

// AVX2 NEEDS THREE BITS, NOT ONE. The kernel issues VFMADD231PS, which is FMA3
// — a separate CPUID feature from AVX2 that a handful of parts (and a good many
// virtual CPU models) advertise independently. Gating on HasAVX2 alone would
// dispatch to a kernel whose first multiply-add is an illegal instruction.
// x/sys/cpu only sets HasAVX once the OS has enabled YMM state, so the OS half
// of the check rides along with it.
func avx2Supported() bool {
	return cpu.X86.HasAVX && cpu.X86.HasAVX2 && cpu.X86.HasFMA
}

// AVX-512 NEEDS AVX + AVX512F + FMA3 AND NO MORE, which is not a guess: avo
// computes the requirement from the instructions it emitted and writes it above
// each function in dot_avx_amd64.s — "Requires: AVX, AVX512F, FMA3, SSE". FMA3
// is in that list because the scalar remainder uses the VEX-encoded VFMADD231SS
// rather than an EVEX form, so the tier is not AVX512F-only however much its
// name suggests it.
//
// NO AVX512DQ, and the generated kernel is written to keep that true: the fold
// uses VEXTRACTF64X4 rather than the DQ-only VEXTRACTF32X8, and the accumulators
// are zeroed with VPXORD rather than the DQ-only VXORPS-on-ZMM. Requiring more
// bits than the kernel uses would decline the tier on silicon that can run it;
// requiring fewer would fault. x/sys/cpu sets HasAVX512F only after confirming
// the OS has enabled ZMM state via XCR0, so the OS half rides along.
func avx512Supported() bool {
	return cpu.X86.HasAVX && cpu.X86.HasAVX512F && cpu.X86.HasFMA
}

// -- dispatch order ---------------------------------------------------------

// amd64PreferAVX512 is the DISPATCH PREFERENCE for amd64: true puts the AVX-512
// tier ahead of AVX2 in the order asmArms returns, false puts AVX2 ahead.
//
// ITS VALUE IS A MEASUREMENT AND NOTHING ELSE. dispatchPolicy in pins.go is the
// rule it implements: preference between two tiers a machine can both execute is
// decided by which is FASTER IN THE TRAVERSE BENCHMARK ON THAT MACHINE CLASS,
// never by which has the wider register. The standing gate is
// TestDispatchPreferenceIsMeasured, which re-measures both tiers on the host and
// fails when the preferred one loses by more than dispatchPreferenceMargin — so
// flipping this constant without a measurement behind it is caught rather than
// merely discouraged.
//
// SOURCE OF THE CURRENT VALUE: measured 2026-08-25 on a GCE c3-standard-8, an
// Intel Xeon Platinum 8481C (Sapphire Rapids), both tiers timed in the same
// session on the same instance through the forced-tier seam, on a 420 MiB corpus
// — 4x that part's 105 MiB L3 — with software prefetch enabled in both gathers.
// Traverse ns/distance, AVX-512 against AVX2:
//
//	dim  256    13.3 vs  14.9   AVX-512 by 11%
//	dim  512    24.4 vs  27.5   AVX-512 by 11%
//	dim 1024    64.0 vs  74.7   AVX-512 by 14%
//	dim 2048    90.3 vs 108.7   AVX-512 by 17%
//
// AVX-512 wins at every width, so true. The lead is steady and if anything grows
// with width, which is the opposite of the shape the first measurements showed.
//
// THOSE FIRST MEASUREMENTS WERE TAKEN ON A CORPUS THAT FIT IN CACHE, and the
// difference is why the corpus is now sized against the host's LLC. The old
// 128 MiB corpus was 1.22x this part's L3, so the traverse was largely
// L3-resident: it reported 39.2 ns at dim 1024 — an implied ~104 GB/s per core,
// which is a cache figure wearing a kernel's name — and 267.9 ns at dim 2048,
// making the AVX-512 lead look like it COLLAPSED from 21% to 5% at the widest
// width. Out of cache there is no collapse. The preference never flipped, but it
// was being read off cells that were measuring the wrong thing.
//
// THE FREQUENCY PENALTY dispatchPolicy warns about did not appear on this part.
// It is a hazard of older server silicon, and the policy remains a measurement
// rather than a conclusion so that a machine class where it DOES bite gets its
// own answer instead of inheriting Sapphire Rapids'.
//
// THIS CONSTANT ONLY GOVERNS ClassAMD64AVX512. On ClassAMD64NoAVX512 the AVX-512
// tier cannot execute at all, so there is no preference to express and this
// value is never consulted.
const amd64PreferAVX512 = true

// asmArms reports the assembly tiers compiled into this binary, PREFERRED FIRST,
// each carrying whether THIS CPU can execute it.
//
// An unsupported tier stays in the table rather than being dropped. Dropping it
// would make "this CPU lacks AVX-512" and "this build has no AVX-512 kernel"
// indistinguishable at the force seam, and those are different bugs with
// different fixes.
//
// BOTH TIERS ARE FIRST CLASS. AVX2 is not a fallback rung below AVX-512: a large
// installed base of modern client silicon has AVX-512 fused off entirely, so AVX2
// is the tier most machines will actually run, and treating it as a fallback
// would leave the most-executed kernel the least-benchmarked one. Whichever
// order amd64PreferAVX512 selects, both are pinned, both are benchmarked and both
// are agreement-graded.
func asmArms() []arm {
	avx512 := arm{
		name:      TierAVX512,
		dot:       avx512Dot,
		gather:    avx512Gather,
		supported: avx512Supported(),
		why:       "CPU does not report the AVX + AVX-512F + FMA3 set this kernel issues (or the OS has not enabled ZMM state)",
	}
	avx2 := arm{
		name:      TierAVX2,
		dot:       avx2Dot,
		gather:    avx2Gather,
		supported: avx2Supported(),
		why:       "CPU does not report the AVX + AVX2 + FMA3 trio this kernel issues",
	}

	if amd64PreferAVX512 {
		return []arm{avx512, avx2}
	}
	return []arm{avx2, avx512}
}

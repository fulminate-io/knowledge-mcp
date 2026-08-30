// SPDX-License-Identifier: Apache-2.0

package veckernel

// reference.go holds the portable arm: a four-way-unrolled float32 dot in
// plain Go, plus the id-list gather built on it.
//
// THIS IS THE REFERENCE, not a consolation prize. Two jobs, and the second is
// the reason it must stay simple enough to read as obviously-correct:
//
//  1. It is what runs wherever no assembly arm exists — every architecture
//     this project has not hand-written a kernel for, and any build carrying
//     the veckernel_noasm tag.
//  2. It is the ORACLE the assembly arms are graded against. The agreement
//     gates compare each assembly arm to this function on seeded corpora, so a
//     clever rewrite here silently redefines "correct" for the whole package.
//
// FOUR ACCUMULATORS, NOT ONE, and that is load-bearing twice over. It is where
// the measured 2.7-3.6x over a naive serial loop comes from — four independent
// dependency chains instead of one serialized add — and it is why this function
// does NOT produce the same bits as a textbook serial dot. Float addition is
// not associative, so a different accumulator count is a different answer in the
// low bits. That is precisely why the agreement gates are scale-relative rather
// than exact; see the package doc.
func dotF32Unroll4(a, b []float32) float32 {
	n := len(a)
	n = min(n, len(b))
	// Re-slice both to n so the compiler can drop the per-iteration bounds
	// checks; without this the unrolled body pays four of them.
	a = a[:n]
	b = b[:n]

	var s0, s1, s2, s3 float32
	i := 0
	for ; i+4 <= n; i += 4 {
		s0 += a[i] * b[i]
		s1 += a[i+1] * b[i+1]
		s2 += a[i+2] * b[i+2]
		s3 += a[i+3] * b[i+3]
	}
	// The remainder loop. Every dim that is not a multiple of four lands here,
	// which is what the tail-exhaustion suite sweeps dim 1..300 to execute.
	for ; i < n; i++ {
		s0 += a[i] * b[i]
	}
	return (s0 + s1) + (s2 + s3)
}

// gatherUnroll4 is the reference id-list batch: one dotF32Unroll4 per id.
//
// It performs NO fusion — each row re-reads the query from memory. That is the
// honest reference shape, and the gap between it and the assembly gather is
// exactly the fusion win the benchmarks report.
//
// Callers reach this through DotF32Gather, which has already validated dim, the
// query length, dst's capacity and every id. It re-derives nothing.
func gatherUnroll4(dst, query, block []float32, dim int, ids []uint32) {
	for i, id := range ids {
		off := int(id) * dim
		dst[i] = dotF32Unroll4(query, block[off:off+dim:off+dim])
	}
}

// dotF64Exact is the INDEPENDENT oracle: the same mathematical dot accumulated
// serially in float64.
//
// It exists because grading the assembly arm against dotF32Unroll4 alone is an
// identity check in disguise — the two would have to be wrong in different ways
// to disagree, and a shared misunderstanding of what the dot IS would pass
// silently on both. float64 has 29 more mantissa bits than the float32 inputs
// carry, so for any input whose products are exactly representable this returns
// the exact answer, and for the rest its error is ~2^-29 of the accumulated
// scale — three orders of magnitude below the float32 arms it grades.
//
// Test-only in practice; it is in the non-test file so that the doc above sits
// beside the reference it explains, and so a future arm can be graded without
// reaching into the test package.
func dotF64Exact(a, b []float32) float64 {
	n := len(a)
	n = min(n, len(b))
	var s float64
	for i := 0; i < n; i++ {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// scaleOf returns the sum of the absolute products, the magnitude the
// accumulator actually passes through.
//
// THIS IS THE DENOMINATOR THE AGREEMENT GATES DIVIDE BY, and choosing it over
// |result| is the whole reason those gates are meetable. A dot of two random
// unit-ish vectors cancels almost completely: the running accumulator visits
// values of order sum|a_i*b_i| while the RESULT is near zero, so relative error
// measured against the result is unbounded and a literal relative tolerance is
// not merely tight, it is unsatisfiable by any correct implementation. Measured
// against the scale the accumulator traversed, the error of a k-accumulator
// float32 dot is bounded by roughly (n/k)*eps, which is what the gates assert.
func scaleOf(a, b []float32) float64 {
	n := len(a)
	n = min(n, len(b))
	var s float64
	for i := 0; i < n; i++ {
		p := float64(a[i]) * float64(b[i])
		if p < 0 {
			p = -p
		}
		s += p
	}
	return s
}

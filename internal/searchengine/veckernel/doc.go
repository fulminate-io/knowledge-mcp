// SPDX-License-Identifier: Apache-2.0

// Package veckernel computes float32 dot products fast, and says which
// implementation did it.
//
// It exists for one caller shape: the distance function inside a graph vector
// index. Embeddings from the providers this project uses are pre-normalized, so
// cosine similarity IS the dot product, and the whole package is that single
// operation in two shapes — one vector against one, and one vector against a
// list of node ids.
//
// # The two entry points
//
//	DotF32(a, b []float32) float32
//	DotF32Gather(dst, query, block []float32, dim int, ids []uint32)
//
// The gather form takes an ID LIST OVER A FLAT BLOCK, where block[id*dim:][:dim]
// is node id's vector. That is not a stylistic choice; two alternatives were
// measured and both lose. A contiguous-buffer batch forces the caller to copy
// scattered neighbors into a staging buffer first, which measured 25.8 / 67.5 /
// 204.9 ns per distance at 512 / 1024 / 2048 dims — several times the per-call
// overhead the batching was meant to amortize. An accessor closure
// (func(id) []float32) cannot be called from assembly, so the per-row loop is
// forced back into Go and the assembly can only see one row at a time, which
// forfeits the four-row fusion worth 1.5-1.7x. See DotF32Gather.
//
// # Tiers, and why the active one is always reported
//
// The package compiles one or more TIERS and dispatches to the preferred one
// this CPU supports. Kernel() names the tier that is actually running, Tiers()
// lists them all with a reason for any this machine cannot execute, and every
// test in the package asserts on those rather than trusting dispatch.
//
// THAT IS THE POINT OF THE PACKAGE AS MUCH AS THE SPEED IS. A kernel library
// that dispatches silently cannot be distinguished from one that has quietly
// declined its SIMD path, and a declined SIMD path returns correct, slow results
// forever without a single test going red. The measurement work that preceded
// this package found precisely that in a third-party library: its batched kernel
// silently fell back to scalar at the widest contemplated dimension, at exactly
// this project's neighbor count, and it was visible only because the harness
// checked the dispatch flag instead of believing it.
//
// Set VECKERNEL_FORCE (see ForceEnv) to pin a tier by name; an unrecognized
// value panics at init naming the value and listing the vocabulary. Build with
// the veckernel_noasm tag to remove assembly from the binary entirely.
//
// # Architecture status
//
//   - arm64: a hand-written Advanced SIMD tier, with a fused four-row gather.
//   - amd64: TWO tiers, AVX2/FMA and AVX-512F, both avo-generated and both with
//     the same fused four-row gather. They are TWO FIRST-CLASS TIERS, not a
//     ladder: preference between them is decided by MEASUREMENT per machine
//     class, because wide vectors can downclock a core into being net slower,
//     and because a large installed base of client silicon has AVX-512 fused off
//     entirely — which makes AVX2 the most-executed tier regardless of which is
//     quicker where both run. Forcing a tier this CPU lacks reports the missing
//     feature; forcing one this binary lacks — an arm64 tier on an amd64 build —
//     reports "not built into this binary" rather than "no such tier".
//   - everything else: the portable reference, reported as such.
//
// # Non-finite inputs: the policy
//
// NaN and Inf are NOT screened. A per-element check for non-finite values would
// cost more than the multiply-add it guards, on the hottest loop in the index.
// This is a documented precondition, not a silent coercion: bad values propagate
// VISIBLY as NaN or Inf out, rather than being replaced by a plausible-looking
// number. Validation belongs at ingest, once per vector, not once per distance.
//
// What the tiers actually do, measured rather than reasoned about, and asserted
// by the package's tests on every tier and through both entry points:
//
//   - Any NaN in either input yields NaN.
//   - A single +Inf or -Inf term yields +Inf or -Inf respectively.
//   - Inf and -Inf both present yields NaN.
//   - Inf multiplied by zero yields NaN.
//   - A dot whose true value exceeds float32 range yields an infinity, never a
//     wrapped or clamped finite number.
//
// # Overflow, and the one thing the package does not promise
//
// The tiers accumulate in DIFFERENT GROUPINGS — the reference holds four running
// sums, the arm64 tier sixteen lanes, AVX2 thirty-two and AVX-512 sixty-four —
// so a partial sum can overflow to infinity in one tier while the equivalent
// partial sum in another stays finite, even when the true total is in range. An
// infinity is not recoverable once it appears, so the tiers are NOT REQUIRED TO
// AGREE on inputs whose partial sums overflow.
//
// THAT RESERVATION IS NOW A DEMONSTRATION. It was written when arm64 was the
// only assembly tier and three constructions aimed at the gap all AGREED,
// because the arm64 tier's fold re-sums its lanes in a way that reproduces the
// reference's overflow rather than dodging it — and it was kept anyway on the
// grounds that a tier with a different lane count could break it. The amd64
// tiers are that tier. Measured on linux/amd64, Intel Xeon Platinum 8481C, at
// dim 20 with three large terms at indices 0, 4 and 8 whose true total is
// 1.7999999627338342e+38:
//
//	go-unroll4      +Inf      indices 0, 4 and 8 are all congruent to 0 mod 4, so
//	                          all three land in accumulator 0 and the first two
//	                          overflow it before the third can cancel them
//	amd64-avx2      1.8e+38   dim 20 is two 8-float passes plus four scalars, so
//	                          indices 0 and 8 land in the SAME lane on successive
//	                          passes and cancel during accumulation
//	amd64-avx512    1.8e+38   dim 20 is one 16-float pass plus four scalars, so
//	                          the three sit in lanes 0, 4 and 8 of one register
//	                          and the fold's first step — adding the upper eight
//	                          lanes onto the lower eight — cancels 0 against 8
//
// Each of those three readings PREDICTS the value beside it, and the run
// produced exactly those values; the mechanism is checked against the output,
// not asserted alongside it.
//
// The two assembly tiers return the answer and the REFERENCE is the one that
// overflows, which is the reservation's own scenario arriving from the direction
// nobody expected. TestProbePartialOverflowDivergence carries the constructions,
// asserts nothing about agreement, and logs its verdict on every run, so this
// record moves with the tiers instead of an outcome measured on one architecture
// being restated as a property of all of them.
//
// # Agreement between tiers, and why the tolerance is not a relative one
//
// The tiers do not produce identical bits and cannot be made to. Float addition
// is not associative, so a different accumulator count is a different answer in
// the low bits; and a fused multiply-add rounds once where a separate multiply
// and add round twice, so the arm64 tier's arithmetic differs from the
// reference's term by term, by design.
//
// Agreement is therefore graded SCALE-RELATIVE: the difference is measured
// against sum|a_i*b_i|, the magnitude the accumulator actually traverses, with a
// tolerance of 1e-4. A LITERAL RELATIVE TOLERANCE IS UNMEETABLE — not merely
// tight, but unsatisfiable by any correct float32 implementation. For two random
// embedding vectors the dot cancels almost completely: the running sum visits
// values of order sum|a_i*b_i| while the RESULT sits near zero, so error divided
// by |result| is unbounded. Divided by the scale, the error of a k-accumulator
// float32 dot is bounded by roughly (n/k) * 2^-24, which at the widest
// production width is ~3e-5 for the reference, ~8e-6 for arm64, ~4e-6 for AVX2
// and ~2e-6 for AVX-512. The tolerance is set by the LOOSEST tier, which is the
// reference and always will be: every assembly tier splits the sum across more
// lanes and therefore lands strictly tighter.
//
// Numeric agreement is paired with TOP-8 RANKING AGREEMENT, because ranking is
// what a search index actually ships and it is not implied by the numeric bound:
// two tiers can agree to 1e-4 of scale on every candidate and still swap two
// neighbors closer together than that.
//
// The scale-relative form has one KNOWN BLIND SPOT, pinned by a test rather than
// left to be rediscovered: on strongly-canceling data it cannot see a uniform
// multiplicative error, because the quantity it divides by is not the quantity
// being perturbed. A uniform scaling preserves ranking exactly, so the property
// the index depends on survives it; non-uniform errors, the ones that actually
// reorder, are caught by the ranking gate.
//
// Both graders also require the arithmetic to stay in NORMAL float32 range.
// Below it, float32 trades mantissa bits for exponent range and stops having a
// relative precision at all, so a scale-relative bound divides by a number that
// has itself gone tiny. Fuzzing found this: at length two with a subnormal
// scale the two tiers differed by 1.9e-4 while both were behaving correctly.
//
// # Verification
//
// Beyond the usual suites, every gate in this package is driven against
// deliberately WRONG kernels in the same run and must reject each one. An
// assertion of the form "the tiers agree" is an equality, and an equality passes
// identically whether its comparator works or is wired to nothing.
//
// See README.md for how to run the tests, the fuzzer and the benchmarks on each
// architecture, and for the pinned performance floors.
package veckernel

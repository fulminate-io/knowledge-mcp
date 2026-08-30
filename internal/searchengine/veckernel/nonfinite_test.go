// SPDX-License-Identifier: Apache-2.0

package veckernel

import (
	"math"
	"testing"
)

// nonfinite_test.go is the rest of TEST CLASS (c): the non-finite policy stated
// in the package doc, tested against what the arms ACTUALLY do rather than
// against what IEEE-754 reasoning predicts they should.
//
// The distinction matters. The policy was written from this file's output, not
// the other way round: the overflow clause in the package doc says the arms are
// not REQUIRED to agree once a partial sum overflows, and the probe below is the
// evidence for how hard it was to make them disagree.

var (
	inf32 = float32(math.Inf(1))
	nan32 = float32(math.NaN())
)

// TestNonFinitePropagation pins the propagation policy on every arm and on both
// entry points. These are the cases the package doc states as behavior.
func TestNonFinitePropagation(t *testing.T) {
	cases := []struct {
		name  string
		dim   int
		mut   func(a, b []float32)
		check func(t *testing.T, label string, got float32)
	}{
		{"nan-in-main-loop", 20, func(a, _ []float32) { a[0] = nan32 }, wantNaN},
		{"nan-in-4-loop", 20, func(a, _ []float32) { a[17] = nan32 }, wantNaN},
		{"nan-in-scalar-tail", 21, func(a, _ []float32) { a[20] = nan32 }, wantNaN},
		{"nan-in-b", 20, func(_, b []float32) { b[3] = nan32 }, wantNaN},
		{"nan-below-any-vector-loop", 3, func(a, _ []float32) { a[2] = nan32 }, wantNaN},
		{"posinf", 20, func(a, _ []float32) { a[0] = inf32 }, wantInf(1)},
		{"neginf", 20, func(a, _ []float32) { a[0] = -inf32 }, wantInf(-1)},
		{"posinf-in-scalar-tail", 21, func(a, _ []float32) { a[20] = inf32 }, wantInf(1)},
		{"both-infinities", 20, func(a, _ []float32) { a[0], a[19] = inf32, -inf32 }, wantNaN},
		{"inf-times-zero", 20, func(a, b []float32) { a[0], b[0] = inf32, 0 }, wantNaN},
	}

	for _, arm := range testArms() {
		t.Run(arm.name, func(t *testing.T) {
			for _, tc := range cases {
				a, b := rep(1, tc.dim), rep(1, tc.dim)
				tc.mut(a, b)

				tc.check(t, "dot/"+tc.name, arm.dot(a, b))

				// The same policy must hold through the gather, which on the
				// assembly arm is a SEPARATE kernel with its own accumulators
				// and its own fold — a propagation bug can live in one and not
				// the other.
				block := make([]float32, 4*tc.dim)
				for row := range 4 {
					copy(block[row*tc.dim:], b)
				}
				dst := make([]float32, 4)
				arm.gather(dst, a, block, tc.dim, []uint32{0, 1, 2, 3})
				for i := range dst {
					tc.check(t, "gather/"+tc.name, dst[i])
				}
			}
		})
	}
}

func wantNaN(t *testing.T, label string, got float32) {
	t.Helper()
	if !math.IsNaN(float64(got)) {
		t.Errorf("%s: got %v, want NaN", label, got)
	}
}

func wantInf(sign int) func(*testing.T, string, float32) {
	return func(t *testing.T, label string, got float32) {
		t.Helper()
		if !math.IsInf(float64(got), sign) {
			t.Errorf("%s: got %v, want %+v Inf", label, got, sign)
		}
	}
}

// TestOverflowProducesInfinity pins the one overflow behavior the package DOES
// promise: a dot whose true value exceeds float32 range returns an infinity
// rather than a wrapped or clamped finite number.
func TestOverflowProducesInfinity(t *testing.T) {
	for _, arm := range testArms() {
		t.Run(arm.name, func(t *testing.T) {
			for _, dim := range []int{4, 17, 20, 64} {
				a, b := rep(2.5e38, dim), rep(1, dim)
				got := arm.dot(a, b)
				if !math.IsInf(float64(got), 1) {
					t.Errorf("dim=%d: got %v, want +Inf (exact value is %v, far above the "+
						"float32 ceiling)", dim, got, dotF64Exact(a, b))
				}
			}
		})
	}
}

// TestProbePartialOverflowDivergence is the EVIDENCE for the package doc's
// hedge, not a gate — it asserts nothing about agreement and is expected to
// pass whichever way the arms behave.
//
// The doc says the arms are not required to agree when a PARTIAL sum overflows
// while the true total stays in range, because they group terms differently: the
// reference puts index i in accumulator i%4, while each assembly arm puts it in
// one of its own lanes — sixteen on arm64, thirty-two on AVX2, sixty-four on
// AVX-512 — chosen by a different rule, and an intermediate +Inf is unrecoverable
// where a finite partial sum would have cancelled.
//
// Three constructions are driven below, each aimed at that gap. The recorded
// result at authoring on darwin/arm64 was that ALL THREE agreed across both arms
// then compiled — the assembly's accumulator fold re-sums the lanes in a way
// that reproduces the reference's overflow rather than avoiding it. The hedge
// stays in the doc anyway: agreement is a property of the particular arms
// present, measured, not one the package structure guarantees, and an arm with a
// different lane count could break it. THE CONSTRUCTIONS ARE NOT TUNED TO ANY
// ONE LADDER — they are aimed at the reference's accumulator assignment, which
// every arm must disagree with somehow — so they stay meaningful as arms are
// added. The probe logs its verdict on every run, so whoever adds an arm sees
// the change instead of inheriting a stale claim.
func TestProbePartialOverflowDivergence(t *testing.T) {
	const big = 3e37

	constructions := []struct {
		name string
		dim  int
		fill func(a, b []float32)
	}{
		{
			// Positives all land in reference accumulator 0, negatives in
			// accumulator 1; the true total is zero.
			name: "ref-acc0-positive-acc1-negative",
			dim:  64,
			fill: func(a, b []float32) {
				for i := range a {
					b[i] = 1
					switch i % 4 {
					case 0:
						a[i] = big
					case 1:
						a[i] = -big
					}
				}
			},
		},
		{
			// THREE TERMS AT INDICES 0, 4 AND 8 — all congruent to 0 mod 4, so
			// the REFERENCE puts every one of them in accumulator 0, where the
			// first two overflow it to +Inf before the third can cancel them.
			//
			// Each assembly arm distributes those three indices differently,
			// because each has a different vector width: arm64's 16-float pass
			// puts them in three separate 4-lane vectors, AVX-512's 16-float
			// lanes put them in lanes 0, 4 and 8 of one accumulator, and AVX2's
			// 8-float pass puts indices 0 and 8 in the SAME lane on successive
			// passes while index 4 sits in a different lane. Whether the +Inf
			// appears at all therefore depends on the arm, which is exactly the
			// divergence this probe exists to look for.
			name: "same-ref-acc-different-asm-acc",
			dim:  20,
			fill: func(a, b []float32) {
				for i := range a {
					b[i] = 1
				}
				a[0], a[4] = 1.8e38, 1.8e38
				a[8] = -1.8e38
			},
		},
		{
			// Alternating signs at full production width, where the reference
			// accumulates 512 terms per accumulator and the assembly arms fewer
			// per lane the wider they are: 128 on arm64's sixteen lanes, 64 on
			// AVX2's thirty-two, 32 on AVX-512's sixty-four.
			name: "alternating-signs-dim2048",
			dim:  2048,
			fill: func(a, b []float32) {
				for i := range a {
					b[i] = 1
					if i%2 == 0 {
						a[i] = big
					} else {
						a[i] = -big
					}
				}
			},
		},
	}

	for _, c := range constructions {
		a, b := make([]float32, c.dim), make([]float32, c.dim)
		c.fill(a, b)

		results := map[string]float32{}
		for _, arm := range testArms() {
			results[arm.name] = arm.dot(a, b)
		}
		t.Logf("%-34s dim=%4d exact=%-14v arms=%v", c.name, c.dim, dotF64Exact(a, b), results)

		var first *float32
		diverged := false
		for _, v := range results {
			if first == nil {
				v := v
				first = &v
				continue
			}
			if (math.IsNaN(float64(v)) != math.IsNaN(float64(*first))) || (!math.IsNaN(float64(v)) && v != *first) {
				diverged = true
			}
		}
		if diverged {
			t.Logf("  DIVERGED — the package doc's overflow hedge is now demonstrated, not just reserved")
		} else {
			t.Logf("  agreed — hedge remains reserved rather than demonstrated")
		}
	}
}

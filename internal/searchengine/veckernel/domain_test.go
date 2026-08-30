// SPDX-License-Identifier: Apache-2.0

package veckernel

import (
	"math"
	"testing"
)

// domain_test.go is TEST CLASS (c): the value domain — zeros, hand-pinned exact
// answers, near-total cancellation, denormals, non-finite inputs, and unaligned
// sub-slices.

// TestExactPinnedValues grades every arm against answers derived from the
// DEFINITION of a dot product, not from any implementation of one.
//
// This is the control for the identity hazard that the oracle alone does not
// close. dotF64Exact is a better dot than the arms under test, but it is still a
// dot somebody here wrote; if the whole file misunderstood the operation, the
// oracle would agree with the arms and everything would pass. These expectations
// come from arithmetic done by hand, and every one is exactly representable in
// float32 so "close enough" does not enter into it.
func TestExactPinnedValues(t *testing.T) {
	cases := []struct {
		name string
		a, b []float32
		want float32
	}{
		{"ones-dot-ones-n7", rep(1, 7), rep(1, 7), 7},
		{"ones-dot-ones-n1000", rep(1, 1000), rep(1, 1000), 1000},
		{"twos-dot-threes-n64", rep(2, 64), rep(3, 64), 384}, // 64 * 6
		{"ramp-dot-ones-n10", ramp(10), rep(1, 10), 55},      // 1+2+...+10
		{"ramp-dot-ramp-n4", ramp(4), ramp(4), 30},           // 1+4+9+16
		{"single-element", []float32{3}, []float32{7}, 21},
		{"powers-of-two", []float32{0.5, 0.25, 0.125}, []float32{2, 4, 8}, 3}, // 1+1+1
		{"negatives", rep(-1, 33), rep(2, 33), -66},
	}

	for _, a := range testArms() {
		t.Run(a.name, func(t *testing.T) {
			for _, tc := range cases {
				if got := a.dot(tc.a, tc.b); got != tc.want {
					t.Errorf("%s: got %v, want exactly %v", tc.name, got, tc.want)
				}
			}
		})
	}
}

func TestZeros(t *testing.T) {
	for _, a := range testArms() {
		t.Run(a.name, func(t *testing.T) {
			for _, dim := range []int{1, 3, 4, 5, 15, 16, 17, 256, 1024} {
				x, y := seededPair(uint64(dim), dim)
				zero := make([]float32, dim)

				if got := a.dot(zero, y); got != 0 {
					t.Errorf("dim=%d: zero-dot-random = %v, want exactly 0", dim, got)
				}
				if got := a.dot(x, zero); got != 0 {
					t.Errorf("dim=%d: random-dot-zero = %v, want exactly 0", dim, got)
				}
				if got := a.dot(zero, zero); got != 0 {
					t.Errorf("dim=%d: zero-dot-zero = %v, want exactly 0", dim, got)
				}
			}
		})
	}
}

// TestNearCancellation drives inputs whose true dot is many orders of magnitude
// below the magnitudes the accumulator passes through — the case that makes a
// literal relative tolerance unmeetable and the scale-relative gate necessary.
//
// It also asserts that fact directly: on the canceling corpus, the arms are
// graded scale-relative AND the test records how bad a literal relative bound
// would have been, so the tolerance choice stays evidenced rather than asserted.
func TestNearCancellation(t *testing.T) {
	for _, a := range testArms() {
		t.Run(a.name, func(t *testing.T) {
			for _, dim := range []int{16, 64, 256, 1024, 2048} {
				x := make([]float32, dim)
				y := make([]float32, dim)
				// Pairs that cancel exactly except for one small survivor: the
				// accumulator visits +/-1e7 while the answer is 1.
				for i := 0; i+1 < dim; i += 2 {
					x[i], y[i] = 1e7, 1
					x[i+1], y[i+1] = -1e7, 1
				}
				x[dim-1], y[dim-1] = 1, 1

				got := float64(a.dot(x, y))
				want := dotF64Exact(x, y)
				scale := scaleOf(x, y)

				if err := gradeScalarAgainstOracle(a.name, got, x, y); err != nil {
					t.Error(err)
				}
				if want != 0 {
					literalRel := math.Abs(got-want) / math.Abs(want)
					t.Logf("dim=%4d: scale=%.3e |exact|=%.3e literal-relative-error=%.3e scale-relative-error=%.3e",
						dim, scale, math.Abs(want), literalRel, math.Abs(got-want)/scale)
				}
			}
		})
	}
}

// TestDenormals checks the arms do not silently flush subnormal float32 values
// to zero. A kernel running under flush-to-zero and a kernel running IEEE
// produce different answers on subnormal data, and nothing else in the suite
// would notice: normal-range corpora never produce a subnormal intermediate.
func TestDenormals(t *testing.T) {
	const sub = 7e-40 // subnormal: below the 1.18e-38 smallest normal float32

	for _, a := range testArms() {
		t.Run(a.name, func(t *testing.T) {
			// Subnormal times a large normal lands back in normal range, so the
			// answer is representable and any flush shows up as a plain wrong
			// number rather than as an underflow.
			for _, dim := range []int{1, 4, 7, 16, 17, 64} {
				x := rep(sub, dim)
				y := rep(1e30, dim)
				got := float64(a.dot(x, y))
				if err := gradeScalarAgainstOracle(a.name, got, x, y); err != nil {
					t.Errorf("dim=%d: %v", dim, err)
				}
				if got == 0 {
					t.Errorf("dim=%d: subnormal input flushed to zero (got exactly 0, oracle %v)",
						dim, dotF64Exact(x, y))
				}
			}
		})
	}
}

// TestSubSliceAlignment runs every arm on operands whose data pointers sit at
// each of sixteen byte offsets inside a backing array.
//
// Every arm is written with unaligned-tolerant loads — AArch64's are tolerant
// architecturally, and the amd64 arms issue VMOVUPS and read their second
// operand as an unaligned memory operand of VFMADD231PS — so this is not
// expected to fail, which is exactly why it is worth pinning. The vector block a
// real traversal hands in is a sub-slice of a mapped segment at an arbitrary
// offset; if a future arm ever adopts an alignment-sensitive instruction (the
// aligned VMOVAPS is one keystroke away from VMOVUPS) or an align-to-boundary
// prologue, this is the test that goes red instead of a production query
// returning quiet nonsense — or, on amd64, faulting in the field.
func TestSubSliceAlignment(t *testing.T) {
	const dim = 257 // not a multiple of any loop stride, so tails run too

	for _, a := range testArms() {
		t.Run(a.name, func(t *testing.T) {
			for offA := range 16 {
				for offB := range 16 {
					backA, backB := seededPair(uint64(offA*16+offB), dim+16)
					x := backA[offA : offA+dim]
					y := backB[offB : offB+dim]
					if err := gradeAgainstOracle(a.name, a.dot, x, y); err != nil {
						t.Errorf("offA=%d offB=%d: %v", offA, offB, err)
					}
				}
			}
		})
	}
}

func rep(v float32, n int) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func ramp(n int) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(i + 1)
	}
	return out
}

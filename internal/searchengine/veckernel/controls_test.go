// SPDX-License-Identifier: Apache-2.0

package veckernel

import "testing"

// controls_test.go is the KNOWN-POSITIVE suite: every grader in gates_test.go is
// run against deliberately WRONG kernels and must reject each one.
//
// WHY THIS FILE EXISTS. Every agreement assertion in this package has the shape
// "the arms agree" — an equality, and an equality passes identically whether the
// comparator is working or wired to nothing. A dropped tail, a comparator that
// always returns nil, a tolerance loose enough to admit anything: all three look
// exactly like a clean run. The only way to tell them apart is to drive the same
// graders, in the same run, with inputs they MUST reject.
//
// EACH CONTROL DECLARES ITS OWN CORPUS AND ITS OWN DIMS, and neither is a
// detail. A control driven where its defect is not present cannot fire, and a
// control that cannot fire proves nothing while passing — the exact vacuity this
// file exists to detect. "Drops the whole tail" is genuinely CORRECT at dim 256,
// so demanding a rejection there would be demanding a false positive; it lists
// only the dims whose width leaves a remainder. Every corpus below is chosen so
// the defect's size relative to the scale is computable in advance rather than
// left to a random draw.

// wrongKernel is a deliberately broken dot, the defect it stands for, the corpus
// that exposes it, and the dims at which the defect is actually present.
type wrongKernel struct {
	name   string
	defect string
	dot    dotFunc
	corpus func(dim int) (a, b []float32)
	// dims are the widths at which this defect EXISTS. The gate must reject at
	// every one of them; anything less means the gate is blind.
	dims []int
}

// onesCorpus makes every product equal to 1, so dropping k terms shifts the
// result by exactly k against a scale of exactly dim — a defect size known
// before the test runs rather than sampled.
func onesCorpus(dim int) ([]float32, []float32) { return rep(1, dim), rep(1, dim) }

// alternatingCorpus makes a shift by one position change the answer by ~2
// against a scale of dim, which is what an indexing defect needs to be visible.
// The ones corpus cannot see an off-by-one at all: shifted ones are still ones.
func alternatingCorpus(dim int) ([]float32, []float32) {
	a := make([]float32, dim)
	for i := range a {
		if i%2 == 0 {
			a[i] = 1
		} else {
			a[i] = -1
		}
	}
	return a, rep(1, dim)
}

// positiveRandom is a NON-CANCELING corpus: every product is positive, so
// |result| is the same order as the scale. It is what a multiplicative defect
// needs to be visible scale-relative — see TestScaleRelativeGateBlindSpot.
func positiveRandom(dim int) ([]float32, []float32) {
	a, b := seededPair(uint64(dim)*13+5, dim)
	for i := range a {
		if a[i] < 0 {
			a[i] = -a[i]
		}
		if b[i] < 0 {
			b[i] = -b[i]
		}
		a[i] += 0.5
		b[i] += 0.5
	}
	return a, b
}

// subnormalCorpus pairs subnormal values with a large normal multiplier, so the
// true product lands back in normal range and a flush-to-zero shows up as a
// plainly wrong number rather than as an underflow.
func subnormalCorpus(dim int) ([]float32, []float32) { return rep(7e-40, dim), rep(1e30, dim) }

// allDims are the widths the controls sweep by default: a mix of below-a-vector,
// odd, prime-ish, and both production extremes.
var allDims = []int{5, 17, 63, 256, 1023, 2048}

// remainderDims are allDims minus the multiples of 16 — the widths at which a
// kernel that only handles whole 16-float groups actually loses data.
var remainderDims = []int{5, 17, 63, 1023}

func wrongKernels() []wrongKernel {
	return []wrongKernel{
		{
			name:   "drops-last-element",
			defect: "a remainder loop that runs one iteration short — the classic tail bug",
			dot: func(a, b []float32) float32 {
				if len(a) == 0 {
					return 0
				}
				return dotF32Unroll4(a[:len(a)-1], b[:len(b)-1])
			},
			// Ones corpus: the defect is exactly 1/dim of the scale, which is
			// 4.9e-4 at the widest dim here — comfortably above the 1e-4 gate.
			corpus: onesCorpus,
			dims:   allDims,
		},
		{
			name:   "drops-the-whole-tail",
			defect: "a kernel that handles only whole 16-float groups and forgets the remainder",
			dot: func(a, b []float32) float32 {
				n := len(a) - len(a)%16
				return dotF32Unroll4(a[:n], b[:n])
			},
			corpus: onesCorpus,
			// ONLY the widths with a remainder. At dim 256 and 2048 this kernel
			// is correct, and a gate that "caught" it there would be reporting a
			// defect that is not present.
			dims: remainderDims,
		},
		{
			name:   "off-by-one-index",
			defect: "b indexed one element out of step with a — a gather addressing bug",
			dot: func(a, b []float32) float32 {
				if len(a) < 2 {
					return 0
				}
				return dotF32Unroll4(a[1:], b[:len(b)-1])
			},
			corpus: alternatingCorpus,
			dims:   allDims,
		},
		{
			name:   "scaled-by-1.001",
			defect: "a fold that multiplies by a not-quite-one constant; catches a tolerance set too loose",
			dot: func(a, b []float32) float32 {
				return dotF32Unroll4(a, b) * 1.001
			},
			// MUST be non-canceling. On signed-random data the result is orders
			// of magnitude below the scale, so a 0.1% error on the RESULT is
			// rounding-noise-sized against the SCALE and the gate correctly-but-
			// uselessly accepts it. That blind spot is pinned and bounded by
			// TestScaleRelativeGateBlindSpot rather than papered over here.
			corpus: positiveRandom,
			dims:   allDims,
		},
		{
			name:   "flushes-denormals",
			defect: "a kernel running under flush-to-zero where the reference runs IEEE",
			dot: func(a, b []float32) float32 {
				const smallestNormal = 1.1754944e-38
				fa := make([]float32, len(a))
				for i := range a {
					if v := a[i]; v < smallestNormal && v > -smallestNormal {
						fa[i] = 0
					} else {
						fa[i] = v
					}
				}
				return dotF32Unroll4(fa, b)
			},
			corpus: subnormalCorpus,
			dims:   allDims,
		},
	}
}

// TestOracleGateRejectsWrongKernels drives gradeAgainstOracle with each broken
// kernel at every dim where its defect exists. All must be rejected.
func TestOracleGateRejectsWrongKernels(t *testing.T) {
	for _, wk := range wrongKernels() {
		t.Run(wk.name, func(t *testing.T) {
			for _, dim := range wk.dims {
				x, y := wk.corpus(dim)
				if err := gradeAgainstOracle("wrong", wk.dot, x, y); err == nil {
					t.Errorf("GATE IS BLIND at dim=%d: gradeAgainstOracle accepted a kernel that %s",
						dim, wk.defect)
				}
			}
			t.Logf("rejected at all %d dims %v (defect: %s)", len(wk.dims), wk.dims, wk.defect)
		})
	}
}

// TestArmAgreementGateRejectsWrongKernels does the same for the arm-vs-arm
// grader, a separate code path with its own denominator.
func TestArmAgreementGateRejectsWrongKernels(t *testing.T) {
	for _, wk := range wrongKernels() {
		t.Run(wk.name, func(t *testing.T) {
			for _, dim := range wk.dims {
				x, y := wk.corpus(dim)
				err := gradeArmsAgree("wrong", wk.dot, TierReference, dotF32Unroll4, x, y)
				if err == nil {
					t.Errorf("GATE IS BLIND at dim=%d: gradeArmsAgree accepted a kernel that %s",
						dim, wk.defect)
				}
			}
		})
	}
}

// TestTailDropIsGenuinelyAbsentAtAlignedDims is the other half of the
// remainderDims claim above.
//
// The tail-dropping control skips dims 256 and 2048 because the defect is not
// present there. That is an assertion about the SIMULATED kernel, and an
// unchecked one would let a mis-specified control quietly shrink its own
// coverage — "it doesn't fire there" is what a blind gate says too. So the claim
// is checked: at those widths the tail-dropping kernel must agree EXACTLY with
// the reference, because it is doing identical work.
func TestTailDropIsGenuinelyAbsentAtAlignedDims(t *testing.T) {
	dropTail := func(a, b []float32) float32 {
		n := len(a) - len(a)%16
		return dotF32Unroll4(a[:n], b[:n])
	}
	for _, dim := range []int{16, 256, 1024, 2048} {
		x, y := onesCorpus(dim)
		if got, want := dropTail(x, y), dotF32Unroll4(x, y); got != want {
			t.Errorf("dim=%d is a multiple of 16, so the tail-dropping kernel must be identical "+
				"to the reference; got %v want %v", dim, got, want)
		}
	}
}

// TestGateAcceptsCorrectKernels is the negative control on the controls: the
// graders must NOT reject a genuinely correct kernel.
//
// Without it, a grader that rejects everything would pass every test above while
// making the whole suite meaningless — the mirror image of a blind gate, and
// just as invisible.
func TestGateAcceptsCorrectKernels(t *testing.T) {
	corpora := map[string]func(int) ([]float32, []float32){
		"signed-random":       func(d int) ([]float32, []float32) { return seededPair(uint64(d)*13+5, d) },
		"ones":                onesCorpus,
		"alternating":         alternatingCorpus,
		"positive-random":     positiveRandom,
		"subnormal-times-big": subnormalCorpus,
	}
	for _, a := range testArms() {
		for cname, corpus := range corpora {
			for _, dim := range allDims {
				x, y := corpus(dim)
				if err := gradeAgainstOracle(a.name, a.dot, x, y); err != nil {
					t.Errorf("grader rejected the CORRECT kernel %s on %s at dim %d: %v",
						a.name, cname, dim, err)
				}
			}
		}
	}
}

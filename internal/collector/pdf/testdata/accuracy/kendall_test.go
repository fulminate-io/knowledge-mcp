package accuracy

import "testing"

// TestNormalizedKendallTau_Identity verifies that the identity
// permutation [0,1,2,3] returns +1.0.
func TestNormalizedKendallTau_Identity(t *testing.T) {
	t.Parallel()
	got := NormalizedKendallTau([]int{0, 1, 2, 3})
	if got != 1.0 {
		t.Errorf("identity: got %v want 1.0", got)
	}
}

// TestNormalizedKendallTau_FullyReversed verifies that the reversed
// permutation [2,1,0] returns -1.0. Per absorbed reviewer finding T3#4:
// this synthetic permutation is what TestAccuracySelfTest_ReversedOrder
// would have needed if we kept it (single-paragraph onepage.pdf
// produces n=1, where NormalizedKendallTau floors to 0.0). Testing
// the helper directly is the cleanest path; the harness's
// ReversedOrder self-test is intentionally not authored.
func TestNormalizedKendallTau_FullyReversed(t *testing.T) {
	t.Parallel()
	got := NormalizedKendallTau([]int{2, 1, 0})
	if got != -1.0 {
		t.Errorf("reversed [2,1,0]: got %v want -1.0", got)
	}
	// Larger reversed permutation [4,3,2,1,0] also -1.0.
	got = NormalizedKendallTau([]int{4, 3, 2, 1, 0})
	if got != -1.0 {
		t.Errorf("reversed [4,3,2,1,0]: got %v want -1.0", got)
	}
}

// TestNormalizedKendallTau_NLessThan2 verifies the n<2 floor: empty
// and single-element permutations return 0.0.
func TestNormalizedKendallTau_NLessThan2(t *testing.T) {
	t.Parallel()
	if got := NormalizedKendallTau(nil); got != 0 {
		t.Errorf("nil: got %v want 0", got)
	}
	if got := NormalizedKendallTau([]int{}); got != 0 {
		t.Errorf("empty: got %v want 0", got)
	}
	if got := NormalizedKendallTau([]int{0}); got != 0 {
		t.Errorf("single: got %v want 0", got)
	}
}

// TestNormalizedKendallTau_SingleSwap verifies a single-swap
// permutation [1,0,2,3]: 1 inversion out of 6 pairs → tau = 1 - 2/6 ≈ 0.667.
func TestNormalizedKendallTau_SingleSwap(t *testing.T) {
	t.Parallel()
	got := NormalizedKendallTau([]int{1, 0, 2, 3})
	want := 1.0 - 2.0/6.0
	if absDiff(got, want) > 1e-9 {
		t.Errorf("single-swap: got %v want %v", got, want)
	}
}

func absDiff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}

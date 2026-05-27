package accuracy

// NormalizedKendallTau computes the normalized Kendall-tau over a
// permutation of [0, len(perm)). The output is in [-1, 1]:
//
//	+1.0 when perm is the identity (sorted ascending)
//	 0.0 when n < 2 (no pairs to compare)
//	-1.0 when perm is fully reversed
//
// Algorithm: simple O(n^2) inversion count. tau = 1 - 2*inv/(n*(n-1)/2).
//
// Per locked decision #7: simple inversion-count, canonical order
// = index in the golden chunks array. Caller passes a slice of
// integer indices indicating where each actual chunk lands in the
// golden ordering; identity-mapping gives +1.0, full-reversal
// gives -1.0.
//
// Sign convention: the corpus harness wants "low divergence" to be
// good. The harness derives a divergence value as
// (1 - NormalizedKendallTau)/2 in [0, 1] and compares against
// reading_order_kendall_tau_max — see the harness scoring loop.
func NormalizedKendallTau(perm []int) float64 {
	n := len(perm)
	if n < 2 {
		return 0
	}
	inversions := 0
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if perm[i] > perm[j] {
				inversions++
			}
		}
	}
	totalPairs := n * (n - 1) / 2
	return 1 - 2*float64(inversions)/float64(totalPairs)
}

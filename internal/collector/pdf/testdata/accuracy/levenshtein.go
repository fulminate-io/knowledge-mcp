package accuracy

// WordLevenshtein computes the classic word-level Levenshtein edit
// distance between two token slices. Two-row DP, O(N×M) time,
// O(min(N,M)) space.
//
// Mirror shape: collector/pdf/font/poppler_compat_test.go:104
// (T3 ticket) — same algorithm, exported here so the T9 corpus
// harness and any future cross-validation test can share the
// implementation instead of re-spelling it.
func WordLevenshtein(a, b []string) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min3(
				prev[j]+1,      // deletion
				cur[j-1]+1,     // insertion
				prev[j-1]+cost, // substitution
			)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// WordEditDistanceRatio returns WordLevenshtein(a,b) divided by
// max(len(a), len(b)). Returns 0 when both slices are empty.
//
// Mirror shape: collector/pdf/font/poppler_compat_test.go:90.
func WordEditDistanceRatio(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	maxLen := len(a)
	if len(b) > maxLen {
		maxLen = len(b)
	}
	if maxLen == 0 {
		return 0
	}
	return float64(WordLevenshtein(a, b)) / float64(maxLen)
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

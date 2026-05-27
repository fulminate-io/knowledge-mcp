// SPDX-License-Identifier: Apache-2.0

package foundation

import "sort"

// percentile.go provides shared ranking helpers used by every topology
// analyzer. It has zero dependencies beyond the standard library so the
// helpers can be unit-tested in isolation and reused freely across the
// degree, structural, code, and cloud analyzer families.
//
// The two primitives are:
//
//   - Percentile(sortedScores, value) — converts a raw analyzer score
//     into a 0..100 percentile rank against a population of scores. Used
//     by analyzers to translate "this node has fan-in 47" into "this is
//     in the top 2% of the graph."
//
//   - SeverityFromPercentile(p) — maps that percentile rank onto the
//     four-level Severity ladder declared in finding.go. Centralizing
//     the thresholds in one place keeps every analyzer's notion of
//     "critical" consistent.
//
// TopK is a small companion that ranks (id, score) pairs and returns the
// k highest, with a stable tie-break by ID. Analyzers build a slice of
// ScoredItem from their per-node measurements, call TopK to pick the
// findings to surface, and then attach percentile-derived severity to
// each one.

// ScoredItem is a typed (id, score) pair returned and consumed by TopK.
// Analyzers build slices of ScoredItem from their per-node measurements
// (degree, betweenness, etc.) and feed them to TopK to obtain a ranked
// shortlist.
type ScoredItem struct {
	// ID is the node ID the score belongs to.
	ID string
	// Score is the analyzer-specific numeric output (degree, centrality,
	// reach count, etc.). Higher values are ranked first by TopK.
	Score float64
}

// Percentile returns the percentile rank (0..100) of value within
// sortedScores. The input slice MUST be pre-sorted in ascending order;
// the helper does not sort defensively because every analyzer already
// has the sorted slice on hand from its TopK pass.
//
// The rank is computed against the position of the FIRST element greater
// than or equal to value (i.e. sort.SearchFloat64s returns a stable
// "leftmost insertion point"). For tied values this means a value equal
// to multiple elements receives the percentile of the first occurrence —
// stable, deterministic, and matches how analysts intuitively interpret
// "where does this score sit in the distribution":
//
//   - Empty input → 0.
//   - value smaller than every element → 0.
//   - value larger than every element → 100.
//   - value equal to one or more elements → 100*idx/len where idx is the
//     index of the first matching element.
//
// Example: sortedScores=[1,2,3,4,5], value=3 → idx=2, rank=40.
// Example: sortedScores=[5,5,5,5,5], value=5 → idx=0, rank=0.
func Percentile(sortedScores []float64, value float64) float64 {
	n := len(sortedScores)
	if n == 0 {
		return 0
	}
	// Past-the-end means value is strictly greater than the max.
	if value > sortedScores[n-1] {
		return 100
	}
	idx := sort.SearchFloat64s(sortedScores, value)
	if idx >= n {
		return 100
	}
	return 100 * float64(idx) / float64(n)
}

// SeverityFromPercentile maps a percentile rank (0..100) onto the
// Severity ladder declared in finding.go. The thresholds are inclusive
// on the high side ("top 1%" means p ≥ 99):
//
//   - p ≥ 99 → SeverityCritical (top 1%)
//   - p ≥ 95 → SeverityWarning  (top 5%)
//   - p ≥ 80 → SeverityNotice   (top 20%)
//   - else   → SeverityInfo
//
// Centralizing the thresholds here means every analyzer surfaces the
// same urgency for the same relative position in its distribution; future
// rebalancing only needs to touch this one function.
func SeverityFromPercentile(p float64) Severity {
	switch {
	case p >= 99:
		return SeverityCritical
	case p >= 95:
		return SeverityWarning
	case p >= 80:
		return SeverityNotice
	default:
		return SeverityInfo
	}
}

// TopK returns the k highest-scoring items from items, sorted by Score
// in descending order. Ties on Score are broken by ID ascending so that
// the output is deterministic across runs. The function is non-mutating:
// the input slice is left untouched and a fresh slice is returned.
//
// Boundary behavior:
//
//   - len(items) == 0 → returns nil.
//   - k <= 0          → returns nil.
//   - k >= len(items) → returns a fully sorted copy of items.
//   - otherwise       → returns the top k items sorted desc by Score.
func TopK(items []ScoredItem, k int) []ScoredItem {
	if len(items) == 0 || k <= 0 {
		return nil
	}
	// Copy first so the caller's slice is not mutated by the in-place sort.
	sorted := make([]ScoredItem, len(items))
	copy(sorted, items)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Score != sorted[j].Score {
			return sorted[i].Score > sorted[j].Score
		}
		return sorted[i].ID < sorted[j].ID
	})
	if k >= len(sorted) {
		return sorted
	}
	return sorted[:k]
}

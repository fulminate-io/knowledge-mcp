// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// resultWithScore builds a minimal search-mode ThoughtResult carrying a score.
func resultWithScore(id string, score float64) ThoughtResult {
	return ThoughtResult{Node: &knowledgev1.Node{Id: id, SymbolName: id}, Score: score}
}

// TestFormatRecallResults_RRFFooterNotRelevance is Phase 4 Step 2's criterion:
// with didRerank=false on a semantic (non-zero Score) path, FormatRecallResults
// preserves the input RRF order AND the footer labels the scale RRF-rank — never
// relevance. Asserts raw RRF is never presented as a relevance score.
func TestFormatRecallResults_RRFFooterNotRelevance(t *testing.T) {
	// Above the RRF floor so no floor banner muddies the footer assertion.
	results := []ThoughtResult{
		resultWithScore("a", 0.50),
		resultWithScore("b", 0.30),
	}
	out := FormatRecallResults(results, "search", false /* didRerank */)

	// RRF order preserved (a before b).
	idxA := strings.Index(out, "1. a")
	idxB := strings.Index(out, "2. b")
	require.GreaterOrEqual(t, idxA, 0, "row a rendered")
	require.GreaterOrEqual(t, idxB, 0, "row b rendered")
	assert.Less(t, idxA, idxB, "RRF order preserved")

	// Footer labels the scale as RRF-rank, NOT relevance.
	assert.Contains(t, out, "RRF rank", "footer discloses the RRF-rank scale")
	assert.NotContains(t, out, "reranked relevance", "RRF path must NOT claim relevance")
}

// TestFormatRecallResults_RerankedFooter asserts the didRerank=true footer labels
// the score as reranked relevance.
func TestFormatRecallResults_RerankedFooter(t *testing.T) {
	out := FormatRecallResults([]ThoughtResult{resultWithScore("a", 0.70)}, "search", true)
	assert.Contains(t, out, "reranked relevance", "didRerank=true footer claims reranked relevance")
	assert.NotContains(t, out, "RRF rank")
}

// TestFormatRecallResults_FloorBanner is Phase 4 Step 3's criterion: a best score
// at/near the RRF floor (or reranked relevance below threshold) renders the
// explicit floor banner; above-threshold renders none; ALL result rows remain
// present in every case (non-suppression).
func TestFormatRecallResults_FloorBanner(t *testing.T) {
	const bannerFragment = "No strongly related thoughts"

	cases := []struct {
		name       string
		results    []ThoughtResult
		didRerank  bool
		wantBanner bool
	}{
		{
			name:       "RRF floor (2/61) renders banner",
			results:    []ThoughtResult{resultWithScore("a", 2.0/61.0), resultWithScore("b", 1.0/61.0)},
			didRerank:  false,
			wantBanner: true,
		},
		{
			name:       "RRF above floor renders no banner",
			results:    []ThoughtResult{resultWithScore("a", 0.50), resultWithScore("b", 0.20)},
			didRerank:  false,
			wantBanner: false,
		},
		{
			name:       "reranked relevance below threshold renders banner",
			results:    []ThoughtResult{resultWithScore("a", 0.20), resultWithScore("b", 0.10)},
			didRerank:  true,
			wantBanner: true,
		},
		{
			name:       "reranked relevance above threshold renders no banner",
			results:    []ThoughtResult{resultWithScore("a", 0.60), resultWithScore("b", 0.40)},
			didRerank:  true,
			wantBanner: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := FormatRecallResults(tc.results, "search", tc.didRerank)
			if tc.wantBanner {
				assert.Contains(t, out, bannerFragment, "floor banner expected")
			} else {
				assert.NotContains(t, out, bannerFragment, "no floor banner expected")
			}
			// Non-suppression: count line reports the full set AND every row renders.
			assert.Contains(t, out, "Found 2 thoughts:", "count line reports the full result set")
			for _, r := range tc.results {
				assert.Contains(t, out, r.Node.GetId(), "every result row is rendered (non-suppression)")
			}
		})
	}
}

// SPDX-License-Identifier: Apache-2.0

package tools

// search_mode_contract_test.go covers the mode vocabulary as PURE functions.
// None of these need the environment discipline the InterceptSearch-level tests
// carry, precisely because the contract takes its inputs as arguments — that is
// most of why it was extracted.

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeSegmentSearchMode(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"", "hybrid"},         // absent mode means the declared default arm.
		{"temporal", "recent"}, // declared alias, honored in exactly one place.
		{"recent", "recent"},
		{"hybrid", "hybrid"},
		{"text", "text"},
		{"vector", "vector"},
		{"nonsense", "nonsense"}, // unknown modes pass through untouched.
	} {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, normalizeSegmentSearchMode(tc.in))
		})
	}
}

func TestSegmentSearchClaimMode(t *testing.T) {
	for _, tc := range []struct {
		name          string
		mode          string
		hasText       bool
		hasIDSelector bool
		wantMode      string
		wantClaimed   bool
	}{
		{"explicit text claims even with an id selector", "text", true, true, "text", true},
		{"explicit hybrid claims even with an id selector", "hybrid", true, true, "hybrid", true},
		{"temporal normalizes and claims", "temporal", true, false, "recent", true},
		{"recent claims with no text", "recent", false, false, "recent", true},
		{"default mode with text claims as hybrid", "", true, false, "hybrid", true},
		{"default mode with an id selector declines", "", true, true, "", false},
		{"default mode without text declines", "", false, false, "", false},
		{"unrecognized mode declines", "nonsense", true, false, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotMode, gotClaimed := segmentSearchClaimMode(tc.mode, tc.hasText, tc.hasIDSelector)
			assert.Equal(t, tc.wantMode, gotMode)
			assert.Equal(t, tc.wantClaimed, gotClaimed)
		})
	}
}

// TestSegmentSearchEngineArms asserts the resolved arms AND the label each
// resolution produces — the two are one contract, and checking the arms without
// the label is how a footer starts describing a retrieval that did not happen.
func TestSegmentSearchEngineArms(t *testing.T) {
	vec := []byte{1, 2, 3}

	t.Run("hybrid keeps both arms", func(t *testing.T) {
		text, gotVec := segmentSearchEngineArms("hybrid", "q", vec)
		assert.Equal(t, "q", text)
		assert.Equal(t, vec, gotVec)
		assert.Equal(t, "vector+text", segmentSearchModeLabel(text != "", len(gotVec) > 0))
	})

	t.Run("text nils the vector", func(t *testing.T) {
		text, gotVec := segmentSearchEngineArms("text", "q", vec)
		assert.Equal(t, "q", text)
		assert.Empty(t, gotVec, "the HNSW arm is skipped by starving it of a vector")
		assert.Equal(t, "BM25-only", segmentSearchModeLabel(text != "", len(gotVec) > 0))
	})

	t.Run("vector empties the engine text", func(t *testing.T) {
		text, gotVec := segmentSearchEngineArms("vector", "q", vec)
		assert.Empty(t, text, "the BM25 arm is skipped by starving it of tokens")
		assert.Equal(t, vec, gotVec)
		assert.Equal(t, "vector", segmentSearchModeLabel(text != "", len(gotVec) > 0))
	})

	t.Run("no embedder yields BM25-only under hybrid", func(t *testing.T) {
		text, gotVec := segmentSearchEngineArms("hybrid", "q", nil)
		assert.Equal(t, "q", text)
		assert.Empty(t, gotVec)
		assert.Equal(t, "BM25-only", segmentSearchModeLabel(text != "", len(gotVec) > 0),
			"an install with no embedder ran BM25 only, whatever mode was asked for")
	})
}

func TestSearchRerankActive(t *testing.T) {
	rerankFalse := false
	rerankTrue := true
	for _, tc := range []struct {
		name        string
		hasKey      bool
		rerankParam *bool
		bm25Only    bool
		want        bool
	}{
		// The anti-blanket control: without this row a predicate hardwired to
		// false would satisfy every other row in the table.
		{"keyed, no opt-out, fused arm", true, nil, false, true},
		{"keyed, explicit rerank true", true, &rerankTrue, false, true},
		{"mode suppression beats the key", true, nil, true, false},
		{"explicit opt-out beats the key", true, &rerankFalse, false, false},
		{"no key never reranks", false, nil, false, false},
		{"no key, explicit rerank true, still no rerank", false, &rerankTrue, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, searchRerankActive(tc.hasKey, tc.rerankParam, tc.bm25Only))
		})
	}
}

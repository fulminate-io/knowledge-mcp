// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// bitStableRepeats is how many times each query is replayed against the same
// sealed segment. Go re-randomizes map iteration on EVERY range, so an
// order-sensitive accumulation does not fail on the first repeat — it fails on
// whichever repeat happens to draw a different term order. Measured on the real
// corpus: the varying queries diverged in 51, 107 and 176 of 200 repeats, so 200
// is the count at which a real wobble is reliably observed.
const bitStableRepeats = 200

// bitStableQueries are the multi-term queries the stability test replays. FOUR
// of them, and each deliberately long, for two measured reasons.
//
// First, one query is not enough: of four real multi-term queries measured
// against the live corpus, three varied across 200 repeats and the fourth varied
// 0 of 200. A single-query fixture is therefore an unreliable detector.
//
// Second, the wobble needs at least three addends on the same document. Search
// iterates terms in the outer loop and this segment's fields in a FIXED inner
// order, so a document's score is a sum over (term, field) pairs and only the
// term axis reorders. IEEE addition is commutative, so a document touched by a
// single pair — or by two pairs that merely swap — is bit-stable no matter what
// order the terms arrive in. Long queries over the manyTermDocs vocabulary are
// what make documents carry two or more query terms, and therefore four or more
// addends, often enough to expose the reordering.
var bitStableQueries = []string{
	"term0007 term0031 term0064 term0097 term0128 term0155 term0193 term0221 term0256 term0288 term0317 term0349",
	"term0011 term0042 term0073 term0106 term0139 term0171 term0204 term0233 term0267 term0299 term0333 term0366",
	"term0019 term0055 term0088 term0119 term0147 term0182 term0212 term0245 term0279 term0308 term0341 term0374",
	"term0402 term0417 term0433 term0448 term0461 term0479 term0492 term0507 term0523 term0541 term0566 term0588",
}

// TestSearchScoresBitStable proves bm25Segment.Search is score-deterministic:
// the same query against the same sealed segment must produce bit-identical
// scores, in the same order, on every call.
//
// This is the prerequisite for every score-equality gate the offset reader is
// held to. "Same hits, same scores, same order" is not a statable property while
// Search's own scores drift in the last ULP between runs of identical code, so
// this test guards the guarantee the rest of the work is measured against.
func TestSearchScoresBitStable(t *testing.T) {
	docs := manyTermDocs(400)
	seg := buildBM25(t, docs)
	stats := Format{}.AggregateStats([]searchengine.Segment[Query, *CorpusStats]{seg})
	require.NotNil(t, stats)

	const k = 50

	for qi, text := range bitStableQueries {
		q := NewQuery(text)
		first := seg.Search(q, stats, k, nil)
		require.NotEmpty(t, first, "query %d matched nothing — the fixture cannot detect a wobble it never scores", qi)

		for rep := 1; rep < bitStableRepeats; rep++ {
			got := seg.Search(q, stats, k, nil)
			require.Len(t, got, len(first),
				"query %d repeat %d returned a different hit count", qi, rep)
			for hi := range first {
				require.Equal(t, first[hi].ID, got[hi].ID,
					"query %d repeat %d: hit %d resolved to a different document", qi, rep, hi)
				// Compared as IEEE bit patterns, never with a tolerance. The
				// defect this guards is a last-ULP difference, which every
				// tolerance-based comparison passes; math.Float64bits states the
				// bit-identity the test is named for directly.
				require.Equal(t, math.Float64bits(first[hi].Score), math.Float64bits(got[hi].Score),
					"query %d repeat %d: hit %d (%s) scored %v, first call scored %v",
					qi, rep, hi, got[hi].ID, got[hi].Score, first[hi].Score)
			}
		}
	}
}

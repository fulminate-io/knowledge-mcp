// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// merge_equivalence_window_test.go — a merged segment ANSWERS THE SAME QUERIES as a
// from-scratch build over the same documents, across the shapes the block-index
// defect lived in and well past them.
//
// WHY THIS ROW EXISTS BESIDE THE STRUCTURAL ONE. badBlockIdx and ValidateSegment
// both ask whether the blob is READABLE. Neither asks whether it holds the right
// answers: a writer that silently dropped a block, or wrote a block's payload twice,
// can produce a perfectly self-consistent segment that has lost terms. This row is
// the other question, and it is asked with BIT-IDENTICAL scores rather than a
// tolerance — the two corpora contain the same documents, so the same query has one
// correct BM25 score and any drift is a defect rather than a rounding difference.
//
// THE SCRATCH BUILD IS THE EXTERNAL EXPECTATION. Comparing a merge to itself, or to
// its constituents searched one at a time, would let a merge that lost a term agree
// with its own mistake; the sealed build over the union of the documents is produced
// by a different code path (Build, not the streaming merge) and is what the merged
// segment owes agreement to.

// equivalenceQueries are the queries every shape below is compared on: a term from
// the head of each field's vocabulary, one from the middle of the range where the
// three-block window sits, and a two-term query that scores across fields.
var equivalenceQueries = []string{
	"sym000000", "word0000", "word0064", "word0065", "word0089",
	"sym000199 word0032", "kw0",
}

// TestMergedSegmentAnswersAsAScratchBuildDoes is the equivalence row over the window
// the writer defect lived in (65..96 terms) and past it, on 2, 3, 5 and 8
// constituents.
func TestMergedSegmentAnswersAsAScratchBuildDoes(t *testing.T) {
	for _, terms := range []int{64, 65, 80, 96, 97, 512, 4096} {
		for _, constituents := range []int{2, 3, 5, 8} {
			t.Run(fmt.Sprintf("terms=%d/constituents=%d", terms, constituents), func(t *testing.T) {
				shape := map[string]int{
					searchengine.FieldSymbolName:  200,
					searchengine.FieldSummary:     terms,
					searchengine.FieldDescription: terms,
					searchengine.FieldContent:     terms,
				}
				requireMergeAnswersLikeScratch(t, shape, constituents, 2)
			})
		}
	}
}

// TestMergedSegmentAnswersAsAScratchBuildDoes_UnequalFields covers the layout the
// sweep above cannot reach: five dictionaries of different block counts in one
// segment, which is where a block index landing inside a neighbour's run region
// was observed.
func TestMergedSegmentAnswersAsAScratchBuildDoes_UnequalFields(t *testing.T) {
	shape := map[string]int{
		searchengine.FieldSymbolName:  96,
		searchengine.FieldSummary:     65,
		searchengine.FieldKeywords:    64,
		searchengine.FieldDescription: 97,
		searchengine.FieldContent:     129,
	}
	for _, constituents := range []int{2, 3, 5, 8} {
		t.Run(fmt.Sprintf("constituents=%d", constituents), func(t *testing.T) {
			requireMergeAnswersLikeScratch(t, shape, constituents, 2)
		})
	}
}

// requireMergeAnswersLikeScratch merges constituents built over one shape and
// requires the result to agree with a from-scratch build over the same documents:
// the same members, the same corpus statistics, and the same hits in the same order
// with identical scores.
func requireMergeAnswersLikeScratch(t *testing.T, shape map[string]int, constituents, docsPer int) {
	t.Helper()

	ins := make([]*mappedSegment, 0, constituents)
	all := make([]searchengine.Document, 0, constituents*docsPer)
	for c := range constituents {
		docs := shapeDocs(fmt.Sprintf("c%d", c), docsPer, shape)
		all = append(all, docs...)
		ins = append(ins, buildBM25(t, docs))
	}

	blob, err := mergeToBytes(t, ins, nil, dictBlocked)
	require.NoError(t, err)
	require.NoError(t, ValidateSegment("", blob), "the merged blob must be readable before its answers can be compared")
	merged, err := openSegmentV2(blob)
	require.NoError(t, err)

	scratchIface, _, err := Format{}.Build(all)
	require.NoError(t, err)
	scratch := scratchIface.(*mappedSegment)

	require.ElementsMatch(t, scratch.IDs(), merged.IDs(),
		"the merge must carry every document the scratch build does and no other")

	mergedStats := Format{}.AggregateStats([]searchengine.Segment[Query, *CorpusStats]{merged})
	scratchStats := Format{}.AggregateStats([]searchengine.Segment[Query, *CorpusStats]{scratch})
	require.Equal(t, scratchStats.TotalDocs, mergedStats.TotalDocs)

	for _, text := range equivalenceQueries {
		q := NewQuery(text)
		got := merged.Search(q, mergedStats, 50, nil)
		want := scratch.Search(q, scratchStats, 50, nil)
		require.Len(t, got, len(want), "query %q hit count", text)
		for i := range want {
			require.Equal(t, want[i].ID, got[i].ID, "query %q rank %d id", text, i)
			// BIT-IDENTICAL, not within a delta: same documents, same corpus, one
			// correct score. A tolerance here would hide a merge that carried a
			// different document length or a different document frequency.
			//
			// COMPARED AS BITS RATHER THAN AS FLOATS, which is what makes that claim
			// literal — and what keeps it out of the float-comparison class a
			// tolerance-shaped assertion belongs to. Two float64s that differ in the
			// last ulp are two different numbers here, because the two corpora are the
			// same corpus and the score has one correct value.
			require.Equal(t, math.Float64bits(want[i].Score), math.Float64bits(got[i].Score),
				"query %q rank %d score: %v vs %v", text, i, want[i].Score, got[i].Score)
		}
	}
	// THE QUERY SET IS NOT VACUOUS: at least one of them must actually hit, or the
	// loop above compares two empty slices and proves nothing.
	require.NotEmpty(t, merged.Search(NewQuery(equivalenceQueries[0]), mergedStats, 50, nil),
		"the fixture's first query must match something, or this row asserts nothing")
}

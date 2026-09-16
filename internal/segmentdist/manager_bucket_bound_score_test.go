// SPDX-License-Identifier: Apache-2.0

// manager_bucket_bound_score_test.go — THE CORPUS STATISTICS A CONSOLIDATION
// PUBLISHES, read through the scores they derive.
//
// manager_bucket_bound_equivalence_test.go asks whether a consolidation moves the
// answer on a corpus it holds CONSTANT, and its fixture deletes nothing: the
// constituents' documents and the output's are the same set, so a snapshot that
// carried its predecessor's statistics forward instead of re-folding them would
// publish the same totalDocs, the same per-term document frequency and therefore
// the same scores. That row cannot see the fold at all.
//
// THE FOLD'S INPUTS ONLY CHANGE WHEN A CONSOLIDATION DROPS SOMETHING. A deleted
// document keeps its row in the segment it was written to and loses only its live
// bit, so it is still counted by a fold over that segment — and it is physically
// absent from the consolidated output, because the merge takes live members only.
// So a census over a corpus with deletes MUST lower totalDocs and move every IDF,
// and a carried statistics object does not.
//
// THE EXPECTATION IS AN INDEPENDENT ENGINE, never the pre-census reading: the
// scores are SUPPOSED to move here, so the pre-census hit list is the wrong answer
// key. A fresh engine sealed over exactly the surviving documents is what the
// consolidated engine must agree with.

package segmentdist

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
)

const (
	// scoreTerms is COPRIME WITH scoreDeleteEvery, and that is the fixture's own
	// precondition rather than a detail: a term's documents are i, i+scoreTerms,
	// i+2*scoreTerms, ..., so a term count sharing a factor with the delete stride
	// makes i mod scoreDeleteEvery CONSTANT within a term and deletes every document
	// of some terms while sparing every document of others.
	scoreTerms = 11
	// scoreDocs leaves 48 documents per term, so more than equivK of them survive the
	// deletes and the compared hit lists are filled by term matchers rather than by
	// the documents that carry only the corpus-wide token, which tie in length groups.
	scoreDocs = scoreTerms * 48
	// scoreDeleteEvery deletes a third of the corpus — enough that totalDocs and
	// every document frequency move by a margin no rounding can hide.
	scoreDeleteEvery = 3
	// scoreBuckets is the partition count the census is driven at. Each segment
	// holds one document and therefore occupies exactly one partition, so offering
	// each partition its own segments closes the constituency guard by construction.
	scoreBuckets = 8
)

// scoreDoc gives every document matching a term its OWN filler length, so BM25
// length normalisation separates them and the hit lists compared below carry no
// ties — for the reason equivDoc's comment gives at length.
func scoreDoc(i int) searchengine.Document {
	var filler strings.Builder
	for f := range i / scoreTerms {
		filler.WriteString(fmt.Sprintf(" filler%03d", f))
	}
	return searchengine.Document{
		ID: fmt.Sprintf("score-%05d", i),
		Fields: map[string]string{
			searchengine.FieldContent: fmt.Sprintf("shared term%02d score%05d%s", i%scoreTerms, i, filler.String()),
			searchengine.FieldSummary: fmt.Sprintf("term%02d", i%scoreTerms),
		},
	}
}

// newScoreEngine builds a merge-disabled BM25 engine sealing one segment per call.
func newScoreEngine(t *testing.T) *searchengine.SegmentedIndex[bm25.Query, *bm25.CorpusStats] {
	t.Helper()
	e := searchengine.New[bm25.Query, *bm25.CorpusStats](bm25.New(), searchengine.Options{
		MinSegmentDocs:     1,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
	})
	t.Cleanup(e.Close)
	return e
}

// consolidateEveryPartition drives the census the resident bound drives — one
// ReplaceBucket per partition against that partition's own segments — over EVERY
// partition, so no segment is left holding the dead rows the comparison is about.
func consolidateEveryPartition(
	t *testing.T, e *searchengine.SegmentedIndex[bm25.Query, *bm25.CorpusStats], bucketCount int,
) {
	t.Helper()
	spans := e.SegmentSpans(bucketCount)
	for bucket := range bucketCount {
		constituents := make([]searchengine.SegmentID, 0, len(spans))
		for id, buckets := range spans {
			if slices.Contains(buckets, bucket) {
				constituents = append(constituents, id)
			}
		}
		require.NotEmpty(t, constituents,
			"PRECONDITION: partition %d of %d holds no segment, so the census is shorter than it looks",
			bucket, bucketCount)
		_, err := e.ReplaceBucket(bucket, bucketCount, constituents, nil, nil)
		require.NoError(t, err)
	}
	require.LessOrEqual(t, e.ResidentSegmentCount(), bucketCount,
		"ANTI-VACUITY: after a census over every partition the set must be the outputs alone; a leftover "+
			"segment still holds its dead rows and would make the comparison below assert nothing about the fold")
}

// TestConsolidationScoresMatchAFreshEngineOverTheSurvivors is the SCORE-level
// observer of the statistics re-fold.
func TestConsolidationScoresMatchAFreshEngineOverTheSurvivors(t *testing.T) {
	queries := make([]string, 0, scoreTerms)
	for term := range scoreTerms {
		queries = append(queries, fmt.Sprintf("shared term%02d", term))
	}

	consolidated := newScoreEngine(t)
	fresh := newScoreEngine(t)
	deleted := 0
	for i := range scoreDocs {
		d := scoreDoc(i)
		_, err := consolidated.AddSealAndSupersede([]searchengine.Document{d})
		require.NoError(t, err)
		if i%scoreDeleteEvery == 0 {
			deleted++
			continue
		}
		// The fresh engine is sealed over the SURVIVORS only, and it never sees the
		// deleted documents at all — which is the state a correct consolidation
		// leaves the other engine in.
		_, err = fresh.AddSealAndSupersede([]searchengine.Document{d})
		require.NoError(t, err)
	}
	require.Positive(t, deleted, "PRECONDITION: the corpus must carry deletes, or the fold has nothing to drop")
	for i := range scoreDocs {
		if i%scoreDeleteEvery == 0 {
			consolidated.Delete(scoreDoc(i).ID)
		}
	}
	require.Equal(t, scoreDocs-deleted, fresh.DistinctResidentDocCount())
	require.Equal(t, scoreDocs, consolidated.DistinctResidentDocCount(),
		"PRECONDITION: a delete clears a live bit and removes no route entry, so the counts differ BEFORE the census")

	// THE EXPECTATION MUST NOT ALREADY BE THE PRE-CENSUS ANSWER, and both sides of
	// this comparison are readings of a CORRECT engine over a corpus of its own, so
	// it is a property of the fixture rather than of the subject: if a corpus with
	// its dead rows still in the fold answered as an engine built over the survivors
	// does, the equality at the end would hold whether the statistics were re-taken
	// or carried.
	want := bm25Results(t, fresh, queries)
	require.NotEqual(t, want, bm25Results(t, consolidated, queries),
		"ANTI-VACUITY: before the census the consolidated engine still counts the deleted rows in every "+
			"document frequency, so it must NOT already answer as the survivors-only engine does")

	consolidateEveryPartition(t, consolidated, scoreBuckets)

	require.Equal(t, scoreDocs-deleted, consolidated.DistinctResidentDocCount(),
		"the census must have dropped exactly the deleted documents")
	require.Equal(t, want, bm25Results(t, consolidated, queries),
		"a consolidation that drops dead rows must publish the corpus statistics of the corpus it now holds: "+
			"same documents, same order, same scores as an engine built over exactly those survivors. A snapshot "+
			"that carried its predecessor's statistics keeps the dead rows in totalDocs and in every document "+
			"frequency, and every score here is wrong by that much")
}

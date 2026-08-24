// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"container/heap"
	"slices"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// Search returns up to k hits for the pre-tokenized query, scored with
// CORPUS-GLOBAL IDF + avg-doc-len drawn from the threaded stats (NOT per-segment
// stats — that is the load-bearing correctness difference). It accumulates
// per-(field,term) postings into a segment-local scores scratch indexed by the
// segment's OWN internal docID, filters dead ids via accept(externalID), and
// selects the top-k via a min-heap.
//
// The scoring is the map-resident reader's, unchanged: the same sorted term
// order, the same idf, the same fieldData.scoreField and the same top-k heap.
// Only where the postings come from differs — views into the blob instead of a
// hydrated map — which is what makes the two readers provably score-identical.
func (s *mappedSegment) Search(q Query, stats *CorpusStats, k int, accept func(searchengine.ExternalID) bool) []searchengine.Hit {
	n := s.docCount
	if n == 0 || k <= 0 || len(q.tokens) == 0 {
		return nil
	}
	var totalDocs int64
	if stats != nil {
		totalDocs = stats.TotalDocs
	}
	if totalDocs == 0 {
		// No corpus stats → IDF is undefined; nothing to score against.
		return nil
	}

	scores := make([]float64, n)
	touched := make([]uint32, 0, n)

	for _, term := range sortedQueryTerms(q) {
		idfVal := idf(term, totalDocs, stats)
		if idfVal <= 0 {
			continue
		}
		for _, mf := range s.fields {
			touched = accumulateMappedField(mf, term, idfVal, stats, scores, touched)
		}
	}

	return s.collectTopK(scores, touched, k, accept)
}

// accumulateMappedField adds BM25F scores from one field's posting run for a
// term. avgDocLen is the CORPUS-GLOBAL per-field average from stats.FieldAvgLen,
// NOT this segment's local average, so length normalization matches a
// single-index baseline.
func accumulateMappedField(
	mf *mappedField, term string, idfVal float64, stats *CorpusStats,
	scores []float64, touched []uint32,
) []uint32 {
	docIDs, tfs, ok := mf.lookup(term)
	if !ok || len(docIDs) == 0 {
		return touched
	}
	docLens := mf.lengths
	avgDocLen := 0.0
	if stats != nil {
		avgDocLen = stats.FieldAvgLen[mf.config.Name]
	}
	for i, docID := range docIDs {
		if int(docID) >= len(docLens) || int(docID) >= len(scores) {
			continue // defensive bounds guard
		}
		if scores[docID] == 0 {
			touched = append(touched, docID)
		}
		scores[docID] += mf.scoreField(int(tfs[i]), int(docLens[docID]), idfVal, k1, avgDocLen)
	}
	return touched
}

// collectTopK extracts the top-k live results from scores using a min-heap,
// resolving each segment-local docID back to its external id and skipping ids the
// accept (liveDocs) filter rejects.
//
// The accept filter is handed a VIEW of the member id — it only ever looks the id
// up, never retains it — but every id that leaves in a Hit is COPIED off the
// blob. A returned view would be read long after the caller could have released
// the bytes, which is the use-after-release this rule exists to prevent.
func (s *mappedSegment) collectTopK(scores []float64, touched []uint32, k int, accept func(searchengine.ExternalID) bool) []searchengine.Hit {
	if len(touched) == 0 {
		return nil
	}
	// Sized from what can actually land in the heap rather than from k+1: k is a
	// caller-supplied limit, so k+1 is an allocation size the caller controls and
	// can overflow int, and the heap never holds more than one entry per touched
	// doc. The max(0, ...) floor keeps a non-positive k from asking for a
	// negative size.
	h := make(scoredDocHeap, 0, max(0, min(k, len(touched))))
	for _, docID := range touched {
		if int(docID) >= s.docCount {
			continue
		}
		if accept != nil && !accept(s.member(int(docID))) {
			continue
		}
		score := scores[docID]
		if len(h) < k {
			heap.Push(&h, scoredDoc{id: docID, score: score})
		} else if score > h[0].score {
			h[0] = scoredDoc{id: docID, score: score}
			heap.Fix(&h, 0)
		}
	}
	results := make([]searchengine.Hit, len(h))
	for i := range slices.Backward(h) {
		doc, _ := heap.Pop(&h).(scoredDoc)
		results[i] = searchengine.Hit{ID: strings.Clone(s.member(int(doc.id))), Score: doc.score}
	}
	return results
}

// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"container/heap"
	"math"
	"slices"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// k1 is the BM25 term-frequency saturation parameter. Matches the server
// (bm25_core.go:48 sets idx.k1 = 1.2) so scores are identical.
const k1 = 1.2

// bm25Segment is the concrete immutable Segment the BM25 Format owns (mirrors
// hnsw *hnswSegment). It holds SEALED per-field postings keyed by segment-local
// internal docID, the members slice (segment-local docID → external id), and the
// per-segment per-term document frequency that AggregateStats folds into corpus-
// global IDF. Never mutated after Build/Decode — the engine's liveDocs bitset
// carries deletions; the segment itself stays all-members, which is what makes
// the read path lock-free and parallel-safe.
type bm25Segment struct {
	// fields is parallel to defaultFieldConfigs order; each holds that field's
	// immutable posting lists + doc-length stats for THIS segment.
	fields []*fieldData
	// fieldByName indexes fields by config name for O(1) lookup during AggregateStats.
	fieldByName map[string]*fieldData
	// members maps a segment-local internal docID to its external id. Its length
	// is the segment's doc count. This is the IDs() source and the accept() key.
	members []searchengine.ExternalID
	// docFreq is the per-segment document frequency: term → number of THIS
	// segment's documents containing the term (across all fields, deduped). Summed
	// across segments by AggregateStats to form corpus-global DocFreq.
	docFreq map[string]int64
}

// docCount returns the number of documents indexed in this segment.
func (s *bm25Segment) docCount() int { return len(s.members) }

// Search returns up to k hits for the pre-tokenized query, scored with
// CORPUS-GLOBAL IDF + avg-doc-len drawn from the threaded stats (NOT per-segment
// stats — that is the load-bearing correctness difference). It accumulates
// per-(field,term) postings into a segment-local scores scratch indexed by the
// segment's OWN internal docID (sized to docCount, not corpus N), filters dead
// ids via accept(externalID), and selects the top-k via a min-heap. Ported from
// the server search/accumulateField/collectTopK, adapted to segment-local space.
func (s *bm25Segment) Search(q Query, stats *CorpusStats, k int, accept func(searchengine.ExternalID) bool) []searchengine.Hit {
	n := s.docCount()
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

	for term := range q.tokens {
		idfVal := idf(term, totalDocs, stats)
		if idfVal <= 0 {
			continue
		}
		for _, fd := range s.fields {
			touched = s.accumulateField(fd, term, idfVal, stats, scores, touched)
		}
	}

	return s.collectTopK(scores, touched, k, accept)
}

// accumulateField adds BM25F scores from one field's posting list for a term.
// avgDocLen is the CORPUS-GLOBAL per-field average from stats.FieldAvgLen (so the
// length normalization matches a single-index baseline), NOT this segment's local
// average. Ported from the server accumulateField (bm25_core.go:309).
func (s *bm25Segment) accumulateField(
	fd *fieldData, term string, idfVal float64, stats *CorpusStats,
	scores []float64, touched []uint32,
) []uint32 {
	entries, ok := fd.postings[term]
	if !ok || len(entries) == 0 {
		return touched
	}
	docLens := fd.docLengths
	avgDocLen := 0.0
	if stats != nil {
		avgDocLen = stats.FieldAvgLen[fd.config.Name]
	}
	for _, p := range entries {
		if int(p.docID) >= len(docLens) || int(p.docID) >= len(scores) {
			continue // defensive bounds guard
		}
		if scores[p.docID] == 0 {
			touched = append(touched, p.docID)
		}
		scores[p.docID] += fd.scoreField(int(p.tf), docLens[p.docID], idfVal, k1, avgDocLen)
	}
	return touched
}

// collectTopK extracts the top-k live results from scores using a min-heap,
// resolving each segment-local docID back to its external id and skipping ids the
// accept (liveDocs) filter rejects. Ported from the server collectTopK
// (bm25_core.go:342), emitting searchengine.Hit and honoring the accept filter.
func (s *bm25Segment) collectTopK(scores []float64, touched []uint32, k int, accept func(searchengine.ExternalID) bool) []searchengine.Hit {
	if len(touched) == 0 {
		return nil
	}
	h := make(scoredDocHeap, 0, k+1)
	for _, docID := range touched {
		if int(docID) >= len(s.members) {
			continue
		}
		if accept != nil && !accept(s.members[docID]) {
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
		results[i] = searchengine.Hit{ID: s.members[doc.id], Score: doc.score}
	}
	return results
}

// idf computes the inverse document frequency for a term using CORPUS-GLOBAL DF
// from stats. Uses the BM25 variant ln((N - n + 0.5)/(n + 0.5) + 1). Ported
// VERBATIM from the server idf (bm25_core.go:253), reading docFreq from the
// threaded *CorpusStats rather than the index's inline globalDF.
func idf(term string, totalDocs int64, stats *CorpusStats) float64 {
	N := float64(totalDocs)
	if stats == nil {
		return math.Log((N+0.5)/0.5 + 1)
	}
	c, ok := stats.DocFreq[term]
	if !ok {
		return math.Log((N+0.5)/0.5 + 1)
	}
	n := float64(c)
	return math.Log((N-n+0.5)/(n+0.5) + 1)
}

// IDs lists every ExternalID the segment indexes (live or dead), in stable
// segment-local-docID order. The engine builds its externalID→segment route map
// from this. Returns a copy so the caller cannot mutate the segment's members.
func (s *bm25Segment) IDs() []searchengine.ExternalID {
	return slices.Clone(s.members)
}

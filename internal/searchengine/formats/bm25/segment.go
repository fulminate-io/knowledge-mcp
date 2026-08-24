// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"container/heap"
	"maps"
	"math"
	"slices"
	"sort"
	"unsafe"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// k1 is the BM25 term-frequency saturation parameter. Matches the server
// (bm25_core.go:48 sets idx.k1 = 1.2) so scores are identical.
const k1 = 1.2

// bm25Segment is the BUILD-TIME ACCUMULATOR, not a published segment. Build and
// Merge assemble one of these — SEALED per-field postings keyed by segment-local
// internal docID, the members slice (segment-local docID → external id), and the
// per-segment per-term document frequency — and then immediately encode it and
// publish a *mappedSegment over the resulting bytes. The accumulator is dropped
// at that point, so it is never what the engine holds and never what a query
// reads; keeping the string-keyed maps resident is precisely the cost the
// offset-addressed layout exists to remove.
//
// It still implements Search, and that is deliberate: it is the reference the
// offset reader is proven identical to, so a change to one that does not hold
// for the other shows up as a failing equality gate rather than as drift.
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

// segmentDocFreq answers a document-frequency probe from the accumulator's own
// map, so the same lazy CorpusStats resolution works over an accumulator and
// over a mapped segment — which is what lets the two readers be scored against
// one identical stats object in the equality proof.
func (s *bm25Segment) segmentDocFreq(term string) int64 { return s.docFreq[term] }

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

	for _, term := range sortedQueryTerms(q) {
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

// sortedQueryTerms returns the query's terms in sorted order. Search must not
// range q.tokens directly: Go map iteration order is randomized per range and
// float addition is not associative, so the accumulation order decides the last
// ULP of every multi-term score. Sorting makes scores reproducible run to run,
// which is what lets the offset reader be proven score-identical to the map one.
//
// sort.Strings for byte-determinism is already this package's idiom for the same
// reason — serial.go sorts the docFreq and posting keys before emit so two
// writers converge on one content hash.
func sortedQueryTerms(q Query) []string {
	terms := slices.Collect(maps.Keys(q.tokens))
	sort.Strings(terms)
	return terms
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
	// Sized from what can actually land in the heap rather than from k+1. k is a
	// caller-supplied limit, so the addition is an allocation size the caller
	// controls and can overflow int (CWE-190); the heap also never holds more
	// than one entry per touched doc, so bounding the hint by the touched set
	// keeps a huge k from asking for a huge allocation as well.
	// The max(0, ...) floor keeps a non-positive k — which retains nothing, and
	// which the old k+1 turned into a 0 capacity at k == -1 — from asking for a
	// negative one.
	h := make(scoredDocHeap, 0, max(0, min(k, len(touched))))
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
// VERBATIM from the server idf (bm25_core.go:253), reading document frequency
// from the threaded *CorpusStats rather than the index's inline globalDF.
//
// A term the corpus does not contain resolves to n = 0, and the formula then
// reduces to ln((N + 0.5)/0.5 + 1) with both operands exact — the identical
// value, bit for bit, that the earlier explicit term-absent branch returned. The
// branch is therefore gone rather than merely equivalent-in-the-limit.
func idf(term string, totalDocs int64, stats *CorpusStats) float64 {
	N := float64(totalDocs)
	if stats == nil {
		return math.Log((N+0.5)/0.5 + 1)
	}
	n := float64(stats.docFreqOf(term))
	return math.Log((N-n+0.5)/(n+0.5) + 1)
}

// IDs lists every ExternalID the segment indexes (live or dead), in stable
// segment-local-docID order. The engine builds its externalID→segment route map
// from this. Returns a copy so the caller cannot mutate the segment's members.
func (s *bm25Segment) IDs() []searchengine.ExternalID {
	return slices.Clone(s.members)
}

// bm25MapEntryOverheadBytes models the per-entry cost of a Go map beyond the
// key's own bytes: bucket slot, key header and value word. One declaration, so
// the two map terms below cannot drift apart.
const bm25MapEntryOverheadBytes = 48

// HeapBytes models the Go heap this build accumulator holds — see
// searchengine.Segment.HeapBytes, which documents that the number is an
// estimate rather than a measurement.
//
// UNLIKE THE MAPPED PAYLOAD, THIS ONE IS GENUINELY LARGE AND SCALES WITH THE
// CORPUS. A bm25Segment is the in-memory accumulator the builder fills before
// sealing: its postings maps, per-document length slices, member ids and
// document-frequency map are all ordinary heap. It is NEVER PUBLISHED — Build
// encodes it and the engine imports the mapped form — so it does not normally
// reach a residency budget. It reports honestly anyway, because a payload that
// under-reports because it "should not be resident" is the silent zero this
// interface exists to prevent.
//
// The model sums, in order: the member id strings, the docFreq map, and per
// field the postings map (term bytes plus the posting slice each term owns)
// and the docLengths slice.
func (s *bm25Segment) HeapBytes() int64 {
	var n int64
	for _, id := range s.members {
		n += int64(len(id)) + int64(unsafe.Sizeof(""))
	}
	for term := range s.docFreq {
		n += int64(len(term)) + bm25MapEntryOverheadBytes
	}
	for _, f := range s.fields {
		if f == nil {
			continue
		}
		for term, plist := range f.postings {
			n += int64(len(term)) + bm25MapEntryOverheadBytes +
				int64(len(plist))*int64(unsafe.Sizeof(posting{}))
		}
		n += int64(len(f.docLengths)) * int64(unsafe.Sizeof(int(0)))
	}
	return n
}

// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"math"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// sampleDocs returns a small deterministic corpus exercising every field.
func sampleDocs() []searchengine.Document {
	return []searchengine.Document{
		{ID: "a", Fields: map[string]string{
			searchengine.FieldSymbolName: "syncClusters",
			searchengine.FieldSummary:    "synchronize the cluster membership",
			searchengine.FieldKeywords:   "cluster sync membership",
		}},
		{ID: "b", Fields: map[string]string{
			searchengine.FieldSymbolName:  "parseJSON",
			searchengine.FieldSummary:     "parse a json document into tokens",
			searchengine.FieldDescription: "the json parser walks the cluster of tokens",
		}},
		{ID: "c", Fields: map[string]string{
			searchengine.FieldSymbolName: "computeScore",
			searchengine.FieldSummary:    "compute the bm25 relevance score for a query",
			searchengine.FieldContent:    "scoring uses term frequency and inverse document frequency",
		}},
		{ID: "d", Fields: map[string]string{
			searchengine.FieldSymbolName: "clusterRouter",
			searchengine.FieldKeywords:   "cluster routing membership cluster",
		}},
	}
}

// buildOne builds a single sealed segment from docs and its matching corpus stats.
func buildOne(t *testing.T, docs []searchengine.Document) (*mappedSegment, *CorpusStats) {
	t.Helper()
	segIface, err := Format{}.Build(docs)
	require.NoError(t, err)
	seg := segIface.(*mappedSegment)
	stats := Format{}.AggregateStats([]searchengine.Segment[Query, *CorpusStats]{seg})
	return seg, stats
}

// buildAccumulator seals the BUILD-TIME map-shaped accumulator without publishing
// it. Build normally encodes this and hands back the offset reader; the tests
// that need the map form — the independent scoring oracle, and the equality proof
// the offset reader is held to — reach it through here.
func buildAccumulator(t *testing.T, docs []searchengine.Document) *bm25Segment {
	t.Helper()
	return buildSegment(tokenizeDocsParallel(docs, numWorkers()))
}

// staticDocFreq answers document-frequency probes from a fixed map, so a test can
// pin corpus statistics without building a segment to hold them.
type staticDocFreq map[string]int64

func (s staticDocFreq) segmentDocFreq(term string) int64 { return s[term] }

// docFreqSnapshot resolves every term's corpus-global frequency through the lazy
// path, giving a comparable map for two stats objects that must agree.
func docFreqSnapshot(stats *CorpusStats, terms []string) map[string]int64 {
	out := make(map[string]int64, len(terms))
	for _, term := range terms {
		out[term] = stats.docFreqOf(term)
	}
	return out
}

// corpusTerms lists every term the docs index, via the build-time accumulator's
// own docFreq map. It is the fixture-derived term set the DF comparisons run
// over — deriving it from one of the two stats objects under comparison would
// make the assertion an identity.
func corpusTerms(t *testing.T, docs []searchengine.Document) []string {
	t.Helper()
	acc := buildAccumulator(t, docs)
	require.NotEmpty(t, acc.docFreq, "fixture indexes no terms — a DF comparison over it would be vacuous")
	return sortedKeys(acc.docFreq)
}

// referenceScore recomputes the BM25F score for one (doc, query) pair directly
// from the segment + stats using the same formula, giving an INDEPENDENT oracle:
// if Search matches this, the scoring path is faithful to the BM25F math.
func referenceScore(seg *bm25Segment, stats *CorpusStats, q Query, docID uint32) float64 {
	total := 0.0
	for term := range q.tokens {
		idfVal := idf(term, stats.TotalDocs, stats)
		if idfVal <= 0 {
			continue
		}
		for _, fd := range seg.fields {
			for _, p := range fd.postings[term] {
				if p.docID != docID {
					continue
				}
				avg := stats.FieldAvgLen[fd.config.Name]
				total += fd.scoreField(int(p.tf), fd.docLengths[docID], idfVal, k1, avg)
			}
		}
	}
	return total
}

// TestSingleSegmentSearchMatchesReference is Phase 1 Step 3's criterion: a single
// segment supplied a CorpusStats matching that single segment returns ranked
// ids+scores equal to an independent reference computation of the BM25F formula
// over the same docs (the server bm25.Index computes the identical formula; the
// reference here IS that formula, isolated from the engine's accumulation path).
func TestSingleSegmentSearchMatchesReference(t *testing.T) {
	docs := sampleDocs()
	seg, stats := buildOne(t, docs)
	// The oracle reads posting lists directly, so it runs off the build-time
	// accumulator; both are scored under the SAME stats object so document
	// frequency cannot differ between them.
	acc := buildAccumulator(t, docs)

	for _, qText := range []string{"cluster", "json parser", "score query", "membership cluster"} {
		q := NewQuery(qText)

		hits := seg.Search(q, stats, len(docs), nil)

		// Independent reference: score every member, sort desc (ties by id asc).
		type sd struct {
			id    string
			score float64
		}
		ref := make([]sd, 0, len(acc.members))
		for docID, id := range acc.members {
			sc := referenceScore(acc, stats, q, uint32(docID))
			if sc > 0 {
				ref = append(ref, sd{id: id, score: sc})
			}
		}
		sort.Slice(ref, func(i, j int) bool {
			if ref[i].score != ref[j].score {
				return ref[i].score > ref[j].score
			}
			return ref[i].id < ref[j].id
		})

		require.Len(t, hits, len(ref), "query %q: hit count must match reference", qText)
		for i := range ref {
			require.Equal(t, ref[i].id, hits[i].ID, "query %q rank %d id", qText, i)
			require.InDelta(t, ref[i].score, hits[i].Score, 1e-9, "query %q rank %d score", qText, i)
		}
	}
}

// TestEncodeDecodeRoundTrip asserts Build→Encode→Decode reconstructs an equivalent
// segment: same IDs and same Search results under a fixed CorpusStats.
func TestEncodeDecodeRoundTrip(t *testing.T) {
	docs := sampleDocs()
	seg, stats := buildOne(t, docs)

	blob, err := seg.Encode()
	require.NoError(t, err)
	decIface, err := Format{}.Decode(blob)
	require.NoError(t, err)
	dec := decIface.(*mappedSegment)

	require.ElementsMatch(t, seg.IDs(), dec.IDs())
	// Re-aggregate over the decoded segment; stats must be identical.
	decStats := Format{}.AggregateStats([]searchengine.Segment[Query, *CorpusStats]{dec})
	require.Equal(t, stats.TotalDocs, decStats.TotalDocs)
	terms := corpusTerms(t, docs)
	require.Equal(t, docFreqSnapshot(stats, terms), docFreqSnapshot(decStats, terms))
	for name, avg := range stats.FieldAvgLen {
		require.InDelta(t, avg, decStats.FieldAvgLen[name], 1e-12, "field %s avg len", name)
	}

	for _, qText := range []string{"cluster", "json", "score"} {
		q := NewQuery(qText)
		before := seg.Search(q, stats, len(docs), nil)
		after := dec.Search(q, decStats, len(docs), nil)
		require.Len(t, after, len(before))
		for i := range before {
			require.Equal(t, before[i].ID, after[i].ID, "q %q rank %d", qText, i)
			require.InDelta(t, before[i].Score, after[i].Score, 1e-12, "q %q rank %d score", qText, i)
		}
	}
}

// TestNewQueryTokenizesOnce asserts NewQuery tokenizes the query string once into
// the tokens map (Phase 1 Step 2 criterion).
func TestNewQueryTokenizesOnce(t *testing.T) {
	q := NewQuery("syncClusters membership")
	require.Equal(t, tokenize("syncClusters membership"), q.tokens)
	require.NotEmpty(t, q.tokens)
}

// TestEmptyAndDefensive covers the contract's defensiveness requirements: an
// empty batch builds a searchable zero-hit segment; a doc with no indexable
// fields is dropped; a nil-stats / zero-totalDocs Search returns nil.
func TestEmptyAndDefensive(t *testing.T) {
	segIface, err := Format{}.Build(nil)
	require.NoError(t, err)
	seg := segIface.(*mappedSegment)
	require.Empty(t, seg.IDs())
	require.Nil(t, seg.Search(NewQuery("anything"), newCorpusStats(), 10, nil))

	// A doc with only unknown keys contributes nothing.
	segIface2, err := Format{}.Build([]searchengine.Document{
		{ID: "x", Fields: map[string]string{"not_a_field": "ignored"}},
		{ID: "y", Fields: map[string]string{searchengine.FieldSummary: "real text here"}},
	})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"y"}, segIface2.IDs())
}

// TestMergeUnionMatchesScratch is Phase 1 Step 4's criterion: Merge of N segments
// with accept filters yields one all-live segment whose member set is the union of
// accepted ids and whose Search (under merged-corpus stats) matches a from-scratch
// single-segment build over exactly the surviving docs.
func TestMergeUnionMatchesScratch(t *testing.T) {
	docs := sampleDocs()
	// Split into two segments: {a,b} and {c,d}.
	seg1Iface, err := Format{}.Build(docs[:2])
	require.NoError(t, err)
	seg2Iface, err := Format{}.Build(docs[2:])
	require.NoError(t, err)

	// accept drops "b" from seg1 (kill it) and keeps everything else.
	accept := []func(searchengine.ExternalID) bool{
		func(id searchengine.ExternalID) bool { return id != "b" },
		func(searchengine.ExternalID) bool { return true },
	}
	mergedIface, err := mergeSegments(t,
		[]searchengine.Segment[Query, *CorpusStats]{seg1Iface, seg2Iface}, accept)
	require.NoError(t, err)
	merged := mergedIface.(*mappedSegment)

	// Member set == union of accepted ids (a, c, d — b dropped).
	require.ElementsMatch(t, []string{"a", "c", "d"}, merged.IDs())

	// From-scratch build over the surviving docs.
	surviving := []searchengine.Document{docs[0], docs[2], docs[3]} // a, c, d
	scratch, scratchStats := buildOne(t, surviving)
	mergedStats := Format{}.AggregateStats([]searchengine.Segment[Query, *CorpusStats]{merged})

	// Stats must be identical (same N, DF, field avg lengths).
	require.Equal(t, scratchStats.TotalDocs, mergedStats.TotalDocs)
	survivingTerms := corpusTerms(t, surviving)
	require.Equal(t, docFreqSnapshot(scratchStats, survivingTerms), docFreqSnapshot(mergedStats, survivingTerms))
	for name, avg := range scratchStats.FieldAvgLen {
		require.InDelta(t, avg, mergedStats.FieldAvgLen[name], 1e-12, "field %s avg len", name)
	}

	// Search results must match the from-scratch build for every query.
	for _, qText := range []string{"cluster", "score", "membership cluster", "json"} {
		q := NewQuery(qText)
		m := merged.Search(q, mergedStats, 10, nil)
		s := scratch.Search(q, scratchStats, 10, nil)
		require.Len(t, m, len(s), "q %q hit count", qText)
		for i := range s {
			require.Equal(t, s[i].ID, m[i].ID, "q %q rank %d id", qText, i)
			require.InDelta(t, s[i].Score, m[i].Score, 1e-9, "q %q rank %d score", qText, i)
		}
	}
}

// TestBM25EmptySegmentRoundTrip pins that BM25 is safe by construction: it writes
// its serial version unconditionally (no empty-graph version fork like HNSW had),
// so an empty (all-dead) BM25 segment Encodes and Decodes with no error and yields
// an empty, searchable zero-hit segment. This confirms the HNSW empty-segment fix
// needs no analogous BM25 code change.
func TestBM25EmptySegmentRoundTrip(t *testing.T) {
	// Merge over zero inputs yields an empty segment (the all-dead consolidation
	// shape — every member dropped by accept reduces to this).
	segIface, err := mergeSegments(t, nil, nil)
	require.NoError(t, err)
	require.Empty(t, segIface.IDs())

	blob, err := segIface.Encode()
	require.NoError(t, err)

	decIface, err := Format{}.Decode(blob)
	require.NoError(t, err, "empty BM25 segment must decode cleanly")
	require.Empty(t, decIface.IDs())
	require.Nil(t, decIface.Search(NewQuery("anything"), newCorpusStats(), 10, nil))
}

// idfSanity guards the IDF formula against silent drift: a term in every doc has
// near-zero idf, a rare term has higher idf.
func TestIDFMonotonic(t *testing.T) {
	stats := newCorpusStats()
	stats.TotalDocs = 100
	stats.attach(staticDocFreq{"common": 100, "rare": 1})
	common := idf("common", stats.TotalDocs, stats)
	rare := idf("rare", stats.TotalDocs, stats)
	require.Greater(t, rare, common, "rare term must have higher idf than common term")
	require.False(t, math.IsNaN(rare) || math.IsNaN(common))
}

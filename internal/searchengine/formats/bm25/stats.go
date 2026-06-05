// SPDX-License-Identifier: Apache-2.0

package bm25

// Query is the concrete query type the BM25 SegmentFormat understands (the
// engine's Q parameter). The server BM25 takes a raw query string and tokenizes
// it inside every Search; the engine fans one Search out to every segment, so
// tokenizing once up front (NewQuery) and handing the pre-tokenized map to each
// segment avoids re-tokenizing the same query N times.
type Query struct {
	// tokens is the query's term→frequency map (the tokenize() output). Unexported:
	// callers build a Query via NewQuery and never inspect the tokens directly.
	tokens map[string]int
}

// NewQuery tokenizes text ONCE into a Query. The resulting Query is handed to
// every segment's Search by the engine fan-out, so the (potentially expensive)
// tokenization is paid a single time per query regardless of segment count.
func NewQuery(text string) Query {
	return Query{tokens: tokenize(text)}
}

// CorpusStats carries the corpus-WIDE statistics a correct BM25 score needs:
// total document count N, per-term document frequency (for IDF), and per-field
// average document length (for length normalization). It is the engine's S
// parameter for this format.
//
// There is no server equivalent: the server keeps globalDF + per-field
// totalTokens inline in its single mutable Index. In the segmented engine each
// sealed segment carries only its OWN per-segment counts; AggregateStats folds
// those per-segment counts into one corpus-global *CorpusStats, which the engine
// caches (recomputed on segment-set change) and threads into every segment's
// Search. Using corpus-global IDF — not per-segment IDF — is what makes
// cross-segment ranking correct.
type CorpusStats struct {
	// TotalDocs is the corpus-global document count N (sum of per-segment docCount).
	TotalDocs int64
	// DocFreq maps a term to the number of documents across the WHOLE corpus that
	// contain it (sum of per-segment doc-frequency). Drives IDF.
	DocFreq map[string]int64
	// FieldAvgLen maps a field name to the corpus-global average document length
	// for that field (sum of per-segment field tokens / TotalDocs). Drives BM25
	// length normalization. A field absent from the map (or TotalDocs == 0) yields
	// avgDocLen 0, which disables normalization for that term contribution.
	FieldAvgLen map[string]float64
}

// newCorpusStats allocates an empty CorpusStats with initialized maps.
func newCorpusStats() *CorpusStats {
	return &CorpusStats{
		DocFreq:     make(map[string]int64),
		FieldAvgLen: make(map[string]float64),
	}
}

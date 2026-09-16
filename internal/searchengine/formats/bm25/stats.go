// SPDX-License-Identifier: Apache-2.0

package bm25

import "sync"

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
// the cheap ones — doc counts and per-field token totals, both read straight
// from segment headers — into one corpus-global *CorpusStats, which the engine
// caches (recomputed on segment-set change) and threads into every segment's
// Search. Using corpus-global IDF — not per-segment IDF — is what makes
// cross-segment ranking correct.
//
// Document frequency is NOT folded. Summing every segment's whole dictionary at
// aggregation time would page in each segment's docFreq section before the first
// query could be served, and AggregateStats runs inside the engine's publish CAS
// retry loops, so a lost CAS would re-pay the whole fold. Instead a term's
// frequency is resolved by docFreqOf on first use and memoized.
type CorpusStats struct {
	// TotalDocs is the corpus-global document count N (sum of per-segment docCount).
	TotalDocs int64
	// FieldAvgLen maps a field name to the corpus-global average document length
	// for that field (sum of per-segment field tokens / TotalDocs). Drives BM25
	// length normalization. A field absent from the map (or TotalDocs == 0) yields
	// avgDocLen 0, which disables normalization for that term contribution.
	FieldAvgLen map[string]float64

	// fieldTokens is the RAW per-field token total FieldAvgLen is derived from, kept
	// because an average cannot be extended and a total can. AppendStats derives the
	// next generation's averages from this plus the one segment being appended; from
	// the averages alone it would have to re-fold every resident segment, which is
	// the O(resident) publish cost this field exists to remove.
	fieldTokens map[string]int64

	// probes are the segments a document-frequency question is answered from.
	// There is no corpus-wide DocFreq map: AggregateStats reads segment HEADERS
	// only, and a term's frequency is resolved on first use by probing each
	// segment's own sorted dictionary.
	//
	// IT IS A CHAIN AND NOT A SLICE, and that is what makes an incremental publish
	// O(1) rather than O(resident). A slice cannot be extended for the next
	// generation without either copying it — one allocation per resident segment on
	// every publish, the cost this change exists to remove — or appending into shared
	// spare capacity, which writes memory a published snapshot is concurrently
	// reading. A chain node is written once at construction and never afterwards, so
	// every generation shares its predecessor's nodes by reference under exactly the
	// copy-on-write contract the engine's route map states for its flat base.
	probes *probeChain
	// mu guards memo. The engine fans Search out across one goroutine per
	// segment, so the memo is written concurrently on a cold term.
	mu sync.Mutex
	// memo caches resolved corpus-global frequencies for the life of THIS stats
	// object. That is the life of one segment-set generation, NOT of the process:
	// every add, import or merge builds a fresh stats object and the memo goes
	// with it, which is also why a memo can never go stale.
	memo map[string]int64
}

// docFreqProbe is a source of per-segment document frequency: how many of one
// segment's documents contain a term. A mapped segment answers by binary-
// searching its own dictionary; the build-time accumulator answers from its map.
type docFreqProbe interface {
	segmentDocFreq(term string) int64
}

// probeChain is one immutable node of the probe list: a segment, and the list that
// preceded it. Nothing is ever written to a node after it is constructed, so a
// generation derived by appending shares its whole predecessor list by reference
// and allocates exactly one node.
type probeChain struct {
	probe docFreqProbe
	prev  *probeChain
}

// newCorpusStats allocates an empty CorpusStats with initialized maps.
func newCorpusStats() *CorpusStats {
	return &CorpusStats{
		FieldAvgLen: make(map[string]float64),
		fieldTokens: make(map[string]int64),
		memo:        make(map[string]int64),
	}
}

// attach registers a segment as a source of document frequency. Holding the
// segment keeps it reachable, which is exactly the lifetime the stats object
// needs: a probe must not outlive the bytes it reads.
func (s *CorpusStats) attach(p docFreqProbe) { s.probes = &probeChain{probe: p, prev: s.probes} }

// addSegmentCounts folds ONE segment's header counts into this stats object: its
// document count, its probe registration and its per-field token totals. It is the
// single definition the whole-set fold and the incremental append both run, so the
// two cannot answer differently for the same segments.
//
// IT MUTATES THE RECEIVER AND IS THEREFORE CALLED ONLY DURING CONSTRUCTION, before
// the object is reachable from a published snapshot. A published CorpusStats is
// immutable but for its memo.
func (s *CorpusStats) addSegmentCounts(ms *mappedSegment) {
	s.TotalDocs += int64(ms.docCount)
	s.attach(ms)
	for _, mf := range ms.fields {
		s.fieldTokens[mf.config.Name] += mf.totalTokens
	}
}

// deriveAvgLen recomputes FieldAvgLen from the raw token totals. It costs one
// division per FIELD — a handful — and never touches a segment.
func (s *CorpusStats) deriveAvgLen() {
	if s.TotalDocs <= 0 {
		return
	}
	for name, total := range s.fieldTokens {
		s.FieldAvgLen[name] = float64(total) / float64(s.TotalDocs)
	}
}

// docFreqOf returns the CORPUS-GLOBAL number of documents containing term — the
// same value the eager per-segment fold summed, resolved on demand instead.
//
// The first query to use a term pays one dictionary probe per segment; every
// later query for that term within the same segment-set generation is free. The
// memo's ceiling is the corpus vocabulary, the same ceiling the eager map had,
// but it is populated only with terms someone actually asked for.
func (s *CorpusStats) docFreqOf(term string) int64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if df, ok := s.memo[term]; ok {
		return df
	}
	var df int64
	for node := s.probes; node != nil; node = node.prev {
		df += node.probe.segmentDocFreq(term)
	}
	if s.memo == nil {
		s.memo = make(map[string]int64)
	}
	s.memo[term] = df
	return df
}

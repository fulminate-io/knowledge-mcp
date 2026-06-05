// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"runtime"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// indexedFields lists the BM25F field names in defaultFieldConfigs order. Build
// reads ONLY these documented Document.Fields keys (defensively — a missing key
// is skipped). Content-exclusion by graph type is the CLIENT ADAPTER's concern
// (Phase 4), not the format's: the format indexes whatever Fields it is given.
var indexedFields = []string{
	searchengine.FieldSymbolName,
	searchengine.FieldSummary,
	searchengine.FieldKeywords,
	searchengine.FieldDescription,
	searchengine.FieldContent,
}

// docFieldTokens holds one document's per-field tokenization output: external id
// plus fieldName → (term → freq). Produced by the parallel tokenize pass.
type docFieldTokens struct {
	id     searchengine.ExternalID
	fields map[string]map[string]int
}

// tokenizeDocsParallel tokenizes each document's indexed fields across NumCPU
// goroutines, mirroring the server tokenizeFieldsParallel (build_internal.go:17)
// and the HNSW format's delegation to a parallel builder. A document contributing
// zero indexable fields yields a nil fields map and is dropped by buildSegment.
func tokenizeDocsParallel(docs []searchengine.Document, workers int) []docFieldTokens {
	results := make([]docFieldTokens, len(docs))
	if workers < 1 {
		workers = 1
	}
	var wg sync.WaitGroup
	chunkSize := (len(docs) + workers - 1) / workers
	for w := 0; w < workers; w++ {
		start := w * chunkSize
		end := min(start+chunkSize, len(docs))
		if start >= end {
			break
		}
		wg.Add(1)
		goWithRecover("tokenizeDocsParallel", func() {
			defer wg.Done()
			for i := start; i < end; i++ {
				results[i] = tokenizeOne(docs[i])
			}
		})
	}
	wg.Wait()
	return results
}

// tokenizeOne tokenizes one document's indexed fields, reading the documented
// keys defensively (a missing/empty field is skipped). Returns a result whose
// fields map is nil when the document has no indexable text.
func tokenizeOne(d searchengine.Document) docFieldTokens {
	var fields map[string]map[string]int
	for _, name := range indexedFields {
		text := d.Fields[name]
		if text == "" {
			continue
		}
		toks := tokenize(text)
		if len(toks) == 0 {
			continue
		}
		if fields == nil {
			fields = make(map[string]map[string]int, len(indexedFields))
		}
		fields[name] = toks
	}
	return docFieldTokens{id: d.ID, fields: fields}
}

// buildSegment seals an immutable *bm25Segment from parallel-tokenized docs in a
// SINGLE pass: assign each indexable doc a segment-local internal docID, append
// its per-field postings + doc lengths, accumulate per-field totalTokens, and
// roll up the per-segment per-term document frequency (deduped across fields) so
// AggregateStats is a fold, not a re-walk. Mirrors buildFieldBM25Index
// (build_internal.go:66) but produces a plain immutable struct (no atomics).
func buildSegment(results []docFieldTokens) *bm25Segment {
	configs := defaultFieldConfigs
	fields := make([]*fieldData, len(configs))
	byName := make(map[string]*fieldData, len(configs))
	for i, cfg := range configs {
		fd := &fieldData{config: cfg, postings: make(map[string][]posting)}
		fields[i] = fd
		byName[cfg.Name] = fd
	}

	members := make([]searchengine.ExternalID, 0, len(results))
	docFreq := make(map[string]int64)

	for _, r := range results {
		if r.fields == nil {
			continue // no indexable fields → not a member
		}
		docID := uint32(len(members))
		members = append(members, r.id)

		// Grow every field's docLengths to fit this docID (filled with 0 for
		// fields the doc does not contribute to).
		for _, fd := range fields {
			fd.docLengths = append(fd.docLengths, 0)
		}

		uniqueTerms := make(map[string]struct{})
		for fieldName, tokens := range r.fields {
			fd := byName[fieldName]
			if fd == nil {
				continue
			}
			docLen := 0
			for term, freq := range tokens {
				fd.postings[term] = append(fd.postings[term], posting{docID: docID, tf: clampTF(freq)})
				docLen += freq
				uniqueTerms[term] = struct{}{}
			}
			fd.docLengths[docID] = docLen
			fd.totalTokens += int64(docLen)
		}
		for term := range uniqueTerms {
			docFreq[term]++
		}
	}

	return &bm25Segment{
		fields:      fields,
		fieldByName: byName,
		members:     members,
		docFreq:     docFreq,
	}
}

// clampTF caps a term frequency to the uint16 range the posting stores.
func clampTF(freq int) uint16 {
	if freq > 65535 {
		return 65535
	}
	if freq < 0 {
		return 0
	}
	return uint16(freq)
}

// numWorkers returns the parallel-tokenize worker count (NumCPU, floored at 1).
func numWorkers() int {
	return max(runtime.NumCPU(), 1)
}

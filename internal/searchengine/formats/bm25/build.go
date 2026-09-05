// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"log/slog"
	"runtime"
	"runtime/debug"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// degradeTokenizePanic is the fixed-vocabulary census class for a document
// dropped because tokenizing it panicked. This is the single authoritative
// declaration of the class name; every other site cites the const, never the
// literal. The vocabulary is the format's own — segmentdist stores the census
// and the operator surfaces render it, but neither interprets the key.
const degradeTokenizePanic = "tokenize_panic"

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
//
// The second return is the per-class census of documents LOST to a panic. It is
// NIL when nothing was dropped, so an empty census and no census are one state —
// the same contract collector.renderDegraded holds itself to at
// collector/composition.go:171-174.
func tokenizeDocsParallel(docs []searchengine.Document, workers int) ([]docFieldTokens, map[string]int) {
	return tokenizeDocsParallelWith(docs, workers, tokenizeOne)
}

// tokenizeDocsParallelWith is tokenizeDocsParallel with the per-document unit of
// work taken as a parameter. THE SEAM EXISTS BECAUSE A TEST CANNOT OTHERWISE
// REACH THE RECOVERY BOUNDARY: with the guarded per-part lowering in place no
// production input panics inside tokenize, so a test driving a real drifting
// rune would prove nothing about the recovery SCOPE. It is an unexported
// parameter on an unexported function — nothing here is reachable or swappable
// from outside this package.
func tokenizeDocsParallelWith(
	docs []searchengine.Document,
	workers int,
	one func(searchengine.Document) docFieldTokens,
) ([]docFieldTokens, map[string]int) {
	results := make([]docFieldTokens, len(docs))
	if workers < 1 {
		workers = 1
	}
	// One counter per worker, written only by the worker that owns it — the same
	// disjoint-slot discipline the results slice already uses, so counting costs
	// no shared mutex. Sized after the clamp so a caller passing 0 cannot index a
	// zero-length slice.
	dropped := make([]int, workers)
	var wg sync.WaitGroup
	chunkSize := (len(docs) + workers - 1) / workers
	for w := 0; w < workers; w++ {
		start := w * chunkSize
		end := min(start+chunkSize, len(docs))
		if start >= end {
			break
		}
		wg.Add(1)
		// The OUTER recover STAYS. It is the process-crash guard this codebase's
		// Go practice mandates for every spawned goroutine (goWithRecover is the
		// helper; a bare `go func()` is the smell). The INNER per-document
		// recover in tokenizeDocRecovered is what bounds the blast radius to the
		// one document that panicked. Neither is redundant with the other, so a
		// later reader removes neither.
		goWithRecover("tokenizeDocsParallel", func() {
			defer wg.Done()
			for i := start; i < end; i++ {
				if !tokenizeDocRecovered(one, docs[i], &results[i]) {
					dropped[w]++
				}
			}
		})
	}
	wg.Wait()
	total := 0
	for _, n := range dropped {
		total += n
	}
	if total == 0 {
		return results, nil
	}
	return results, map[string]int{degradeTokenizePanic: total}
}

// tokenizeDocRecovered runs ONE document's tokenization under its own recover
// and reports whether that document survived. The caller counts a false into the
// degrade census; the document's slot keeps the zero value, which is the same
// non-member disposition buildSegment already gives a document with no indexable
// fields. No state exists in which a document is both counted as dropped and
// indexed: one return value decides both.
//
// THE RESET IS LOAD-BEARING. tokenizeOne fills its fields map incrementally, so
// a panic partway through one document would otherwise leave a half-tokenized
// document that buildSegment indexes as a member with fields missing — a
// silently wrong document is worse than a counted absent one.
func tokenizeDocRecovered(
	one func(searchengine.Document) docFieldTokens,
	d searchengine.Document,
	out *docFieldTokens,
) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("goroutine panic",
				"site", "tokenizeDocRecovered",
				"doc_id", d.ID,
				"err", r,
				"stack", string(debug.Stack()))
			*out = docFieldTokens{}
			ok = false
		}
	}()
	*out = one(d)
	return true
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
		// docLengths reaches exactly one entry per indexable doc (len(members) ≤
		// len(results)); preallocate its capacity to that known upper bound so the
		// per-doc grow-by-one append below (one entry per field per doc) never
		// reallocates the backing array. Length stays 0 — the values appended and
		// the final length are unchanged, so Encode is byte-identical.
		fd := &fieldData{config: cfg, postings: make(map[string][]posting), docLengths: make([]int, 0, len(results))}
		fields[i] = fd
		byName[cfg.Name] = fd
	}

	members := make([]searchengine.ExternalID, 0, len(results))
	docFreq := make(map[string]int64)

	// One per-segment scratch set, cleared per doc, instead of a fresh map per doc.
	// uniqueTerms only needs to dedup THIS doc's terms before the docFreq bump
	// below, so its contents never outlive a single iteration — reuse + clear is
	// semantically identical to a fresh map and drops the dominant per-doc
	// allocation (one map header + bucket-growth per indexable doc).
	uniqueTerms := make(map[string]struct{})
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

		clear(uniqueTerms)
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

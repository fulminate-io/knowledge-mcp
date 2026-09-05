// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// formatName tags every shipped BM25 SegmentBlob for routing, and it CARRIES THE
// LAYOUT VERSION deliberately.
//
// The distribution layer filters every list, seed, backstop and prune path on
// exact format-name equality, so a version-carrying name makes the old and new
// layouts two disjoint families: a client on one never sees the other's metas.
// Without that, two clients of the same account at different versions each
// reject and rebuild the other's segments, forever — each one's rebuild is the
// other's next rejection. Sharing one name is what makes that loop possible, and
// this is what breaks it.
const formatName = "bm25v2"

// Format is the BM25F SegmentFormat for the segmented engine, generic over
// [Query, *CorpusStats]: the query is a pre-tokenized bm25.Query and the corpus
// statistics are *CorpusStats (corpus-global IDF + per-field average doc length).
// The format owns its concrete Segment type (*mappedSegment) so MergeTo
// type-asserts its inputs and reads their internals (posting runs) directly — no
// Document retention.
//
// IT SATISFIES SegmentFormat's CONCURRENCY OBLIGATION by being a STATELESS VALUE
// TYPE: no fields, value receivers, every per-call allocation local. The engine
// drives one Format value from several harvest goroutines at once. Per-call
// uniqueness of the on-disk destination is no longer this format's problem: the
// engine creates one file per MergeTo call and passes it in, so two concurrent
// merges cannot collide on the filesystem either.
type Format struct{}

// New returns the BM25 SegmentFormat ready to hand to searchengine.New.
func New() Format { return Format{} }

// Compile-time contract assertions (mirror hnsw format.go:26). The published
// Segment is the offset-addressed reader: Build encodes its accumulator and
// hands back one of these, so every segment the engine holds is offset-shaped
// and there is exactly one resident representation.
var (
	_ searchengine.SegmentFormat[Query, *CorpusStats] = Format{}
	_ searchengine.Segment[Query, *CorpusStats]       = (*mappedSegment)(nil)
)

// Name identifies the format for SegmentBlob.Format routing.
func (Format) Name() string { return formatName }

// Build seals an immutable BM25 segment from a batch of documents. It reads the
// five documented Fields keys DEFENSIVELY (a missing/empty field is skipped; a
// doc with zero indexable fields contributes nothing), tokenizes the fields in
// parallel across NumCPU, and seals the postings + per-segment stats in one pass.
// An all-empty batch yields an empty (searchable, zero-hit) segment.
//
// The sealed accumulator is encoded immediately and the OFFSET-ADDRESSED reader
// over those bytes is what is returned; the map-shaped accumulator is dropped
// here and never published.
func (Format) Build(docs []searchengine.Document) (searchengine.Segment[Query, *CorpusStats], searchengine.BuildReport, error) {
	results, degraded := tokenizeDocsParallel(docs, numWorkers())
	seg, err := publishSegment(buildSegment(results))
	return seg, searchengine.BuildReport{Degraded: degraded}, err
}

// publishSegment encodes a build-time accumulator and opens the result, which is
// the only way a segment becomes visible to the engine.
func publishSegment(acc *bm25Segment) (searchengine.Segment[Query, *CorpusStats], error) {
	blob, err := encodeSegmentV2(acc, defaultDictKind)
	if err != nil {
		return nil, err
	}
	seg, err := openSegmentV2(blob)
	if err != nil {
		return nil, err
	}
	return seg, nil
}

// Decode reconstructs a segment from its blob. A decoded segment is
// indistinguishable from a freshly built one (postings + stats survive the round
// trip), so it is fully merge-eligible — the contract's Decode-reconstructs-
// concrete requirement.
//
// Only the current serial version is accepted. There is no converter for the
// superseded map-shaped layout: segments are a derived cache with a production
// heal path, so the migration is to reject and let that path rebuild.
func (Format) Decode(blob []byte) (searchengine.Segment[Query, *CorpusStats], error) {
	seg, err := openSegmentV2(blob)
	if err != nil {
		return nil, fmt.Errorf("bm25 decode: %w", err)
	}
	return seg, nil
}

// MergeTo consolidates segs into dst and reports the merged segment's byte
// length, without ever holding the merged segment in memory.
//
// WHAT IS STREAMED, PRECISELY. The inputs are read through dictionary cursors
// and the output is written to dst at absolute offsets, so no writer state grows
// with the merged segment: a cursor per input dictionary, one reused posting
// buffer, one reused string buffer, and a fixed set of coalescing windows. There
// is no read-back and no output-sized allocation anywhere on this path, which is
// what distinguishes MergeTo from Merge — Merge reads its temp file back into a
// single output-sized blob and returns that as the segment's payload.
//
// WHAT REMAINS RESIDENT, stated rather than glossed: the merge holds the winner
// map and the id remap, which are per-DOCUMENT and scale with the member count
// rather than with the output's size. This is not a claim that a merge allocates
// nothing — it is the narrower and true claim that nothing it allocates is sized
// by the segment it produces.
//
// THIS PACKAGE STILL CREATES NO MAPPINGS AND CONTAINS NO PLATFORM CODE. dst is
// an interface the engine supplies; mapping the finished file is the distribution
// layer's job, and taking a sink rather than a mapping is what keeps that true.
//
// OWNERSHIP: dst belongs to the caller. This does not truncate, close, stat,
// unlink or map it, and it leaves dst in place on the error path — a format that
// cleaned up after itself would hide an engine that forgot to.
func (Format) MergeTo(dst searchengine.MergeSink, segs []searchengine.Segment[Query, *CorpusStats], accept []func(searchengine.ExternalID) bool) (int64, error) {
	if defaultDictKind > dictHash {
		return 0, fmt.Errorf("bm25 merge: unknown dictionary kind %d", defaultDictKind)
	}
	ins := make([]*mappedSegment, len(segs))
	for i, s := range segs {
		ms, ok := s.(*mappedSegment)
		if !ok {
			return 0, fmt.Errorf("bm25 merge: input %d is %T, not *mappedSegment", i, s)
		}
		ins[i] = ms
	}
	return streamMergeToFile(dst, ins, accept, defaultDictKind)
}

// mergeSlot identifies the ONE copy of an external id that a merge keeps: which
// input segment it came from and its doc id within that segment. Both halves are
// needed — an id can repeat inside a single constituent as well as across two.
type mergeSlot struct {
	seg   int
	docID int
}

// resolveMergeWinners walks the inputs' members exactly as the merge will and
// records, per surviving external id, the LAST slot that would have contributed it.
// The merge then keeps only those slots, so the consolidated segment holds one
// member per id no matter how many constituents carried it.
//
// ONE SLOT PER ID, decided before anything is spliced. A merge that appended a
// member slot per surviving copy would give constituents that share an id two
// slots for it — the index carrying two docs for one id while the engine's route
// map records one, which is the retrieval defect in its BM25 form. The choice is
// LAST-WINS across the whole input, matching the hnsw leg and the route map's own
// last-append-wins semantics. See dedupeItemsByID (hnsw/format.go) for what
// last-wins does and does not guarantee.
//
// The ids are VIEWS into the inputs' blobs, which outlive the merge that reads them.
func resolveMergeWinners(
	ins []*mappedSegment, accept []func(searchengine.ExternalID) bool,
) map[searchengine.ExternalID]mergeSlot {
	winner := make(map[searchengine.ExternalID]mergeSlot)
	for i, ms := range ins {
		keep := acceptFor(accept, i)
		for oldID := range ms.docCount {
			extID := ms.member(oldID)
			if keep != nil && !keep(extID) {
				continue
			}
			winner[extID] = mergeSlot{seg: i, docID: oldID}
		}
	}
	return winner
}

// winsSlot reports whether (segIdx, docID) is the ONE slot resolveMergeWinners chose
// to carry extID into the merged segment. An id with no recorded winner is passed
// through unchanged, so the predicate is inert on inputs that share nothing.
func winsSlot(winner map[searchengine.ExternalID]mergeSlot, extID searchengine.ExternalID, segIdx, docID int) bool {
	w, ok := winner[extID]
	if !ok {
		return true
	}
	return w.seg == segIdx && w.docID == docID
}

// AggregateStats folds the corpus-global statistics that segment HEADERS already
// carry: doc counts and per-field total tokens, divided into the per-field
// average document length. It reads a few hundred bytes per segment and touches
// no dictionary, no posting run and no term.
//
// Document frequency is deliberately NOT summed here. Folding it would page in
// every segment's docFreq section before the first query could be served, and
// this runs inside the engine's publish CAS retry loops — so a lost CAS would
// re-pay the whole fold, lengthening the very window that produces the retry.
// Each segment is instead ATTACHED as a probe, and CorpusStats.docFreqOf answers
// a term's frequency on first use by binary-searching those dictionaries.
func (Format) AggregateStats(segs []searchengine.Segment[Query, *CorpusStats]) *CorpusStats {
	stats := newCorpusStats()
	fieldTokenTotals := make(map[string]int64)

	for _, s := range segs {
		ms, ok := s.(*mappedSegment)
		if !ok {
			continue
		}
		stats.TotalDocs += int64(ms.docCount)
		stats.attach(ms)
		for _, mf := range ms.fields {
			fieldTokenTotals[mf.config.Name] += mf.totalTokens
		}
	}

	if stats.TotalDocs > 0 {
		for name, total := range fieldTokenTotals {
			stats.FieldAvgLen[name] = float64(total) / float64(stats.TotalDocs)
		}
	}
	return stats
}

// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// formatName tags every shipped BM25 SegmentBlob for routing.
const formatName = "bm25"

// Format is the BM25F SegmentFormat for the segmented engine, generic over
// [Query, *CorpusStats]: the query is a pre-tokenized bm25.Query and the corpus
// statistics are *CorpusStats (corpus-global IDF + per-field average doc length).
// The format owns its concrete Segment type (*bm25Segment) so Merge type-asserts
// its inputs and reads their internals (postings) directly — no Document retention.
type Format struct{}

// New returns the BM25 SegmentFormat ready to hand to searchengine.New.
func New() Format { return Format{} }

// Compile-time contract assertions (mirror hnsw format.go:26).
var (
	_ searchengine.SegmentFormat[Query, *CorpusStats] = Format{}
	_ searchengine.Segment[Query, *CorpusStats]       = (*bm25Segment)(nil)
)

// Name identifies the format for SegmentBlob.Format routing.
func (Format) Name() string { return formatName }

// Build seals an immutable BM25 segment from a batch of documents. It reads the
// five documented Fields keys DEFENSIVELY (a missing/empty field is skipped; a
// doc with zero indexable fields contributes nothing), tokenizes the fields in
// parallel across NumCPU, and seals the postings + per-segment stats in one pass.
// An all-empty batch yields an empty (searchable, zero-hit) segment.
func (Format) Build(docs []searchengine.Document) (searchengine.Segment[Query, *CorpusStats], error) {
	results := tokenizeDocsParallel(docs, numWorkers())
	return buildSegment(results), nil
}

// Decode reconstructs a *bm25Segment from its blob. A decoded segment is
// indistinguishable from a freshly built one (postings + stats survive the round
// trip), so it is fully merge-eligible — the contract's Decode-reconstructs-
// concrete requirement.
func (Format) Decode(blob []byte) (searchengine.Segment[Query, *CorpusStats], error) {
	seg, err := decodeSegment(blob)
	if err != nil {
		return nil, fmt.Errorf("bm25 decode: %w", err)
	}
	return seg, nil
}

// Merge consolidates several BM25 segments into one all-live segment, Lucene-style.
// It type-asserts each input to *bm25Segment, keeps only members for which
// accept[i](id) is true, and CONCATENATES the survivors' live postings into one
// fresh segment, re-numbering internal docIDs into a single contiguous space.
// Unlike an HNSW graph (whose neighbor links force re-insertion), BM25 postings
// splice cleanly — only the docID needs remapping. The result is a single
// consolidated segment in which every surviving member is live; the engine drops
// the inputs' liveDocs. Per-segment doc frequency is recomputed from the merged
// postings so AggregateStats over the consolidated set stays correct.
func (Format) Merge(segs []searchengine.Segment[Query, *CorpusStats], accept []func(searchengine.ExternalID) bool) (searchengine.Segment[Query, *CorpusStats], error) {
	configs := defaultFieldConfigs
	out := make([]*fieldData, len(configs))
	byName := make(map[string]*fieldData, len(configs))
	for i, cfg := range configs {
		fd := &fieldData{config: cfg, postings: make(map[string][]posting)}
		out[i] = fd
		byName[cfg.Name] = fd
	}
	var members []searchengine.ExternalID
	docFreq := make(map[string]int64)

	// ONE SLOT PER ID, decided before anything is spliced. mergeSegment appends a
	// member slot per surviving copy, so constituents that share an id would otherwise
	// yield two slots for it — the index carrying two docs for one id while the
	// engine's route map records one, which is the retrieval defect in its BM25 form.
	// Resolving the winner up front keeps mergeSegment a straight per-doc copy and
	// makes the choice LAST-WINS across the whole input, matching the hnsw leg and the
	// route map's own last-append-wins semantics. See dedupeItemsByID (hnsw/format.go)
	// for what last-wins does and does not guarantee.
	winner, err := resolveMergeWinners(segs, accept)
	if err != nil {
		return nil, err
	}

	for i, s := range segs {
		bs, ok := s.(*bm25Segment)
		if !ok {
			return nil, fmt.Errorf("bm25 merge: input %d is %T, not *bm25Segment", i, s)
		}
		var keep func(searchengine.ExternalID) bool
		if i < len(accept) {
			keep = accept[i]
		}
		mergeSegment(bs, i, keep, winner, out, byName, &members, docFreq)
	}

	return &bm25Segment{fields: out, fieldByName: byName, members: members, docFreq: docFreq}, nil
}

// mergeSlot identifies the ONE copy of an external id that a merge keeps: which
// input segment it came from and its doc id within that segment. Both halves are
// needed — an id can repeat inside a single constituent as well as across two.
type mergeSlot struct {
	seg   int
	docID int
}

// resolveMergeWinners walks the inputs' members exactly as mergeSegment will and
// records, per surviving external id, the LAST slot that would have contributed it.
// mergeSegment then splices only those slots, so the consolidated segment holds one
// member per id no matter how many constituents carried it.
func resolveMergeWinners(
	segs []searchengine.Segment[Query, *CorpusStats], accept []func(searchengine.ExternalID) bool,
) (map[searchengine.ExternalID]mergeSlot, error) {
	winner := make(map[searchengine.ExternalID]mergeSlot)
	for i, s := range segs {
		bs, ok := s.(*bm25Segment)
		if !ok {
			return nil, fmt.Errorf("bm25 merge: input %d is %T, not *bm25Segment", i, s)
		}
		var keep func(searchengine.ExternalID) bool
		if i < len(accept) {
			keep = accept[i]
		}
		for oldID, extID := range bs.members {
			if keep != nil && !keep(extID) {
				continue
			}
			winner[extID] = mergeSlot{seg: i, docID: oldID}
		}
	}
	return winner, nil
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

// mergeSegment splices one input segment's LIVE members into the merge output,
// re-numbering its segment-local docIDs into the consolidated space. It walks the
// input's members in stable order, assigns each surviving member a new contiguous
// docID, copies that doc's per-field postings + doc lengths under the new id, and
// rolls up document frequency from the live, deduped terms.
//
// segIdx is this input's position in the merge, and winner names the single slot
// kept per external id (resolveMergeWinners). A member whose winning slot is not
// THIS one is skipped, so an id carried by several constituents contributes exactly
// one member to the output.
func mergeSegment(
	bs *bm25Segment, segIdx int, keep func(searchengine.ExternalID) bool,
	winner map[searchengine.ExternalID]mergeSlot,
	out []*fieldData, byName map[string]*fieldData,
	members *[]searchengine.ExternalID, docFreq map[string]int64,
) {
	// Build per-(input-docID) the field→postings the doc contributed, so the splice
	// is a per-doc copy. Invert the input's term→postings into docID→(field,term,tf).
	type tref struct {
		field string
		term  string
		tf    uint16
	}
	perDoc := make([][]tref, len(bs.members))
	for _, fd := range bs.fields {
		for term, posts := range fd.postings {
			for _, p := range posts {
				if int(p.docID) < len(perDoc) {
					perDoc[p.docID] = append(perDoc[p.docID], tref{field: fd.config.Name, term: term, tf: p.tf})
				}
			}
		}
	}

	for oldID, extID := range bs.members {
		if keep != nil && !keep(extID) {
			continue
		}
		// Not the winning copy of this id — another slot (a later constituent, or a
		// later doc in this one) carries it into the output.
		if !winsSlot(winner, extID, segIdx, oldID) {
			continue
		}
		newID := uint32(len(*members))
		*members = append(*members, extID)
		for _, fd := range out {
			fd.docLengths = append(fd.docLengths, 0)
		}
		uniqueTerms := make(map[string]struct{})
		// Per-field doc length for this doc is carried over from the input's
		// docLengths so length normalization is preserved exactly.
		for _, fd := range bs.fields {
			if oldID < len(fd.docLengths) {
				byName[fd.config.Name].docLengths[newID] = fd.docLengths[oldID]
				byName[fd.config.Name].totalTokens += int64(fd.docLengths[oldID])
			}
		}
		for _, ref := range perDoc[oldID] {
			fd := byName[ref.field]
			if fd == nil {
				continue
			}
			fd.postings[ref.term] = append(fd.postings[ref.term], posting{docID: newID, tf: ref.tf})
			uniqueTerms[ref.term] = struct{}{}
		}
		for term := range uniqueTerms {
			docFreq[term]++
		}
	}
}

// AggregateStats folds each segment's pre-rolled per-segment statistics into one
// corpus-global *CorpusStats: sum per-term document frequency, sum per-field total
// tokens, sum doc counts, then divide field totals by the corpus doc count for the
// per-field average doc length. O(segments × vocab) — a SUM over the pre-rolled
// rollups, NOT a per-posting re-walk (the engine recomputes S on every set change).
func (Format) AggregateStats(segs []searchengine.Segment[Query, *CorpusStats]) *CorpusStats {
	stats := newCorpusStats()
	fieldTokenTotals := make(map[string]int64)

	for _, s := range segs {
		bs, ok := s.(*bm25Segment)
		if !ok {
			continue
		}
		stats.TotalDocs += int64(bs.docCount())
		for term, df := range bs.docFreq {
			stats.DocFreq[term] += df
		}
		for _, fd := range bs.fields {
			fieldTokenTotals[fd.config.Name] += fd.totalTokens
		}
	}

	if stats.TotalDocs > 0 {
		for name, total := range fieldTokenTotals {
			stats.FieldAvgLen[name] = float64(total) / float64(stats.TotalDocs)
		}
	}
	return stats
}

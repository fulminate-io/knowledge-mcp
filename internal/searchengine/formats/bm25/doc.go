// SPDX-License-Identifier: Apache-2.0

// Package bm25 is the client-hosted BM25F SegmentFormat for the segmented search
// engine (cmd/knowledge/internal/searchengine). It ports the authoritative server
// BM25 algorithm (cmd/knowledge-server/internal/index/bm25) onto the engine's
// SegmentFormat[bm25.Query, *bm25.CorpusStats] contract: each sealed segment is an
// immutable postings shard carrying its own per-segment statistics (doc frequency,
// per-field total tokens, doc count), and corpus-global IDF + average document
// length are threaded in at Search time via *CorpusStats (folded across the whole
// segment set by AggregateStats). Cross-segment BM25 scoring is only correct with
// corpus-global IDF — per-segment IDF skews ranking (the Lucene CollectionStatistics
// problem) — so this format is the engine's first real exercise of the S path.
//
// A segment on disk is an OFFSET-ADDRESSED blob: every section is located by an
// absolute offset from the blob start, so opening one parses a header and a
// field table and nothing else, and a query resolves terms, posting runs,
// document lengths and member ids as views into the bytes. The string-keyed maps
// that form was replaced by cost roughly twice the on-disk size in Go heap, all
// of it scanned by the GC and unreclaimable; the bytes now live wherever the
// caller put them, and when that is a file mapping they are evictable OS page
// cache instead. That is a RELOCATION of the cost, not an elimination of it: a
// query must read the whole posting list of each of its terms, so the working
// set is not a small fraction of the corpus.
//
// DECLARED CONSTRAINT — the format is LITTLE-ENDIAN ONLY. Posting runs, document
// lengths and the member-offset array are read as typed views over the blob, and
// those casts are host-endian. Every shipped target is little-endian, so there is
// deliberately no byte-swapping read path; a big-endian host is unsupported
// rather than silently mis-served.
//
// The package is import-clean (stdlib + own subpkgs); it imports the parent
// searchengine package only for the neutral Document/Hit/Segment contract types,
// and it contains NO platform code and creates no mappings — a caller hands it
// bytes. Server-side search is retired, so this client BM25 format is the sole
// BM25 search index.
package bm25

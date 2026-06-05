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
// The package is import-clean (stdlib + own subpkgs); it imports the parent
// searchengine package only for the neutral Document/Hit/Segment contract types.
// Server-side search is retired, so this client BM25 format is the sole BM25
// search index.
package bm25

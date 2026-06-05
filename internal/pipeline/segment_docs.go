// SPDX-License-Identifier: Apache-2.0

package pipeline

import "github.com/fulminate-io/knowledge-mcp/internal/searchengine"

// segment_docs.go holds the shared searchengine.Document builders for the two
// segment formats (HNSW vectors + BM25 fields). Both the embed-writeback ship
// path (worker_embed.go: shipEmbedHNSW / shipEmbedBM25) and the segment_rebuild
// driver (cmd/knowledge/internal/tools) build their Documents through these, so
// the doc-assembly shape stays identical across the two callers. The builders
// are EXPORTED because the rebuild driver lives in a sibling package; they are
// pure in-memory slice transforms (no I/O, no Pipeline state).

// SegmentDoc is the minimal per-node carrier the BM25 document builder consumes:
// a node ID plus its server-composed per-field BM25 text. It is the generalized
// surface that lets BuildBM25Documents serve BOTH the embed-writeback path
// (which maps []EmbedWork → []SegmentDoc) AND the rebuild driver (which has no
// EmbedWork — it reads Bm25Fields straight off the scan items).
type SegmentDoc struct {
	NodeID string
	Fields map[string]string
}

// BuildHNSWDocuments builds the HNSW searchengine.Document slice from the
// embedder's per-id vector map, in the given id order. An id with no vector (or
// an empty vector) is skipped. HNSW ignores Document.Fields — only Vector
// matters — so each doc carries only {ID, Vector}.
//
// Extracted verbatim from shipEmbedHNSW's inline build body so the embed-ship
// path and the rebuild driver assemble HNSW docs identically.
func BuildHNSWDocuments(vectors map[string][]byte, ids []string) []searchengine.Document {
	docs := make([]searchengine.Document, 0, len(ids))
	for _, id := range ids {
		v, ok := vectors[id]
		if !ok || len(v) == 0 {
			continue
		}
		docs = append(docs, searchengine.Document{ID: id, Vector: v})
	}
	return docs
}

// BuildBM25Documents builds the BM25 searchengine.Document slice from the
// per-node {NodeID, Fields} carriers. A doc with an empty Fields map is skipped
// (nothing indexable). BM25 ignores Document.Vector — only Fields matters.
//
// Extracted from shipEmbedBM25's inline build body, generalized off EmbedWork to
// the minimal SegmentDoc carrier so the rebuild driver (no EmbedWork) can call
// it. The embed-ship path applies its "only index nodes that embedded this tick"
// vector-presence gate BEFORE calling this (when mapping []EmbedWork →
// []SegmentDoc); this builder only assembles.
func BuildBM25Documents(items []SegmentDoc) []searchengine.Document {
	docs := make([]searchengine.Document, 0, len(items))
	for _, it := range items {
		if len(it.Fields) == 0 {
			continue
		}
		docs = append(docs, searchengine.Document{ID: it.NodeID, Fields: it.Fields})
	}
	return docs
}

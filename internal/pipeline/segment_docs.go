// SPDX-License-Identifier: Apache-2.0

package pipeline

import "github.com/fulminate-io/knowledge-mcp/internal/searchengine"

// segment_docs.go holds the shared searchengine.Document builders for the two
// segment formats (HNSW vectors + BM25 fields). THREE callers build their
// Documents through these, so the doc-assembly shape stays identical across all
// of them: the embed-writeback vector ship (worker_embed.go: shipEmbedHNSW), the
// BM25 arm's CorpusDelta drain (collector_bm25.go), and the segment_rebuild driver
// (cmd/knowledge/internal/tools). The builders are EXPORTED because the rebuild
// driver lives in a sibling package; they are pure in-memory slice transforms (no
// I/O, no Pipeline state).

// SegmentDoc is the minimal per-node carrier the BM25 document builder consumes:
// a node ID plus its server-composed per-field BM25 text. It is the generalized
// surface that lets BuildBM25Documents serve BOTH the BM25 arm (which maps a
// CorpusDelta page's bm25_items → []SegmentDoc) AND the rebuild driver (which
// reads Bm25Fields straight off the scan items).
type SegmentDoc struct {
	NodeID string
	Fields map[string]string
}

// BuildHNSWDocuments builds the HNSW searchengine.Document slice from the
// embedder's per-id vector map, in the given id order, tagging every document
// with the representation its vectors are in. An id with no vector (or an empty
// vector) is skipped. HNSW ignores Document.Fields — only Vector and Dtype
// matter — so each doc carries only {ID, Vector, Dtype}.
//
// THE DTYPE IS A PARAMETER RATHER THAN A DEFAULT, deliberately. The vector
// format derives a segment's dtype from the documents it is handed, so whatever
// this builder writes is what the sealed segment is tagged as and therefore
// which metric ranks it. Defaulting it here would put the old hard-coded
// ubinary back one layer up, where it would be even harder to see: a caller
// shipping float32 bytes would get a ubinary segment and no error. Making every
// caller name the representation keeps that decision at the site that knows it.
//
// Extracted verbatim from shipEmbedHNSW's inline build body so the embed-ship
// path and the rebuild driver assemble HNSW docs identically.
func BuildHNSWDocuments(vectors map[string][]byte, ids []string, dtype string) []searchengine.Document {
	docs := make([]searchengine.Document, 0, len(ids))
	for _, id := range ids {
		v, ok := vectors[id]
		if !ok || len(v) == 0 {
			continue
		}
		docs = append(docs, searchengine.Document{ID: id, Vector: v, Dtype: dtype})
	}
	return docs
}

// BuildBM25Documents builds the BM25 searchengine.Document slice from the
// per-node {NodeID, Fields} carriers. A doc with an empty Fields map is skipped
// (nothing indexable). BM25 ignores Document.Vector — only Fields matters.
//
// IT ONLY ASSEMBLES — every caller applies its own admission rule BEFORE calling.
// The BM25 arm's rule is the server's composition (a row the server declined to
// compose carries no entry, and a tombstoned row goes to the delete seam instead);
// the rebuild driver's is vector possession. There is deliberately no gate here,
// so a new caller cannot inherit one that does not fit it.
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

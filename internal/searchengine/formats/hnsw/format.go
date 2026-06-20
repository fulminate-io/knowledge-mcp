// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// formatName tags every shipped HNSW SegmentBlob for routing.
const formatName = "hnsw"

// Format is the binary-HNSW SegmentFormat for the segmented engine. It is generic
// over [[]byte, struct{}]: the query is a raw binary vector and there are no
// corpus-wide statistics (HNSW search needs none — AggregateStats is a no-op).
// The format owns its concrete Segment type (*hnswSegment) so Merge type-asserts
// its inputs and reads their internals (vectors) directly — no Document retention.
// There is no build-variant state: the HNSW builder is deterministic everywhere
// (the byte-reproducible serial path is the ONLY builder), so Format carries no
// fields.
type Format struct{}

// New returns the HNSW SegmentFormat ready to hand to searchengine.New. Build is
// byte-reproducible (fixed PCG seed + stable sorted-by-id serial insertion): the
// same node set always produces the same blob → the same content hash, so two
// writers' segments dedup to one copy and exact-match recall is recovered.
func New() Format { return Format{} }

// NewDeterministic is retained as an alias for New() — the HNSW builder is
// deterministic unconditionally now, so there is no separate variant. Kept so the
// segment_rebuild wiring that named the deterministic builder explicitly still
// reads clearly at its call sites.
func NewDeterministic() Format { return Format{} }

// Compile-time contract assertions.
var (
	_ searchengine.SegmentFormat[[]byte, struct{}] = Format{}
	_ searchengine.Segment[[]byte, struct{}]       = (*hnswSegment)(nil)
)

// hnswSegment is the concrete immutable Segment the format owns: a sealed binary
// HNSW graph with inline vectors (so it both searches and merges from its own
// internals). Never mutated after construction — the engine's liveDocs bitset
// carries deletions; the graph itself stays all-members.
type hnswSegment struct {
	graph *binaryGraph
}

// Name identifies the format for SegmentBlob.Format routing.
func (Format) Name() string { return formatName }

// Build seals an immutable HNSW segment from a batch of live documents. It reads
// Document.Vector (ignoring Document.Fields — HNSW indexes vectors, not text) and
// DEFENSIVELY skips documents whose Vector is empty (contract: formats tolerate
// absent data, never panic). Graph construction is the byte-reproducible serial
// builder (fixed PCG seed + stable sorted-by-id insertion), so identical inputs
// yield a byte-identical blob. An all-empty batch yields an empty (searchable,
// zero-hit) segment.
func (Format) Build(docs []searchengine.Document) (searchengine.Segment[[]byte, struct{}], error) {
	items := make([]binaryBuildItem, 0, len(docs))
	for _, d := range docs {
		if len(d.Vector) == 0 {
			continue
		}
		items = append(items, binaryBuildItem{id: d.ID, vec: d.Vector})
	}
	graph := buildBinaryHNSWSerialDeterministic(items, defaultVecBytes, defaultM, defaultEfConstruction)
	graph.setEfSearch(defaultEfSearch)
	return &hnswSegment{graph: graph}, nil
}

// Decode reconstructs an hnswSegment from a v2 blob. A decoded segment is
// indistinguishable from a freshly built one (inline vectors survive the round
// trip), so it is fully merge-eligible — the contract's Decode-reconstructs-
// concrete requirement.
func (Format) Decode(blob []byte) (searchengine.Segment[[]byte, struct{}], error) {
	graph, err := decodeGraph(blob)
	if err != nil {
		return nil, fmt.Errorf("hnsw decode: %w", err)
	}
	graph.setEfSearch(defaultEfSearch)
	return &hnswSegment{graph: graph}, nil
}

// Merge consolidates several HNSW segments into one all-live segment, Lucene-style.
// It type-asserts each input to *hnswSegment (the format owns its concrete type),
// iterates each segment's (externalID, vector) pairs, keeps only members for which
// accept[i](id) is true, and re-INSERTS the survivors into a fresh graph. An HNSW
// graph cannot be spliced (neighbor links are internal-id-relative), so re-add is
// the only correct merge — this is exactly how Lucene merges HNSW. The result is a
// single all-live consolidated segment; the engine drops the inputs' liveDocs.
//
// Serial re-insertion is correct here: Merge runs on the engine's single
// background merge goroutine (at most one merge in flight) and Insert is not
// internally parallel-safe across a single growing graph.
func (Format) Merge(segs []searchengine.Segment[[]byte, struct{}], accept []func(searchengine.ExternalID) bool) (searchengine.Segment[[]byte, struct{}], error) {
	merged := newBinaryGraph(defaultVecBytes, defaultM, defaultEfConstruction)
	merged.setEfSearch(defaultEfSearch)
	for i, s := range segs {
		hs, ok := s.(*hnswSegment)
		if !ok {
			return nil, fmt.Errorf("hnsw merge: input %d is %T, not *hnswSegment", i, s)
		}
		var keep func(searchengine.ExternalID) bool
		if i < len(accept) {
			keep = accept[i]
		}
		hs.graph.rangeVectors(func(externalID string, vec []byte) {
			if keep != nil && !keep(externalID) {
				return
			}
			merged.Insert(externalID, vec)
		})
	}
	return &hnswSegment{graph: merged}, nil
}

// AggregateStats is a no-op for HNSW — binary search needs no corpus-wide stats.
func (Format) AggregateStats([]searchengine.Segment[[]byte, struct{}]) struct{} {
	return struct{}{}
}

// Search runs the HNSW query and maps each graph hit to a searchengine.Hit. The
// accept liveDocs filter is threaded straight into the graph's over-fetch-then-
// filter loop: a dead id is skipped from RESULTS, the graph is never mutated.
func (s *hnswSegment) Search(q []byte, _ struct{}, k int, accept func(searchengine.ExternalID) bool) []searchengine.Hit {
	graphHits := s.graph.search(q, k, accept)
	hits := make([]searchengine.Hit, 0, len(graphHits))
	for _, gh := range graphHits {
		hits = append(hits, searchengine.Hit{ID: gh.externalID, Score: gh.score})
	}
	return hits
}

// IDs lists every ExternalID the segment indexes (live or dead), in a stable
// internal-ID order. The engine builds its externalID→segment route map from this.
func (s *hnswSegment) IDs() []searchengine.ExternalID {
	return s.graph.ids()
}

// VectorByID returns the stored binary vector for an external id, or (nil,false)
// when the id is not in this segment. It is the by-id stored-vector read the
// "similar" search mode resolves a query vector from. Deliberately NOT on the
// shared searchengine.Segment interface (segment.go) — that interface is also
// satisfied by bm25Segment, which has no vectors; the engine's SegmentedIndex
// reaches this concrete method via a runtime type-assert, so bm25 segments cleanly
// resolve (nil,false) instead of being forced to implement an unsatisfiable accessor.
func (s *hnswSegment) VectorByID(externalID string) ([]byte, bool) {
	return s.graph.vectorByID(externalID)
}

// Encode serializes the segment to a v2 blob (topology + inline vectors) for
// shipping/persistence.
func (s *hnswSegment) Encode() ([]byte, error) {
	return s.graph.encode(), nil
}

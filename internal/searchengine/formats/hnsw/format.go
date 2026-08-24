// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"fmt"
	"slices"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// formatName tags every shipped HNSW SegmentBlob for routing, and it CARRIES THE
// LAYOUT VERSION deliberately.
//
// The distribution layer filters every list, seed, backstop and prune path on
// exact format-name equality, so a version-carrying name makes the old and new
// layouts two disjoint families: a client on one never sees the other's metas.
// Without that, two clients of the same account at different versions each
// reject and rebuild the other's segments, forever — each one's rebuild is the
// other's next rejection. Sharing one name is what makes that loop possible, and
// this is what breaks it.
const formatName = "hnswv3"

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
	graph *mappedGraph
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
	return publishGraph(graph)
}

// publishGraph is the ONE seal-and-open path: it encodes a freshly built graph
// to v3 bytes and reopens them as the mapped payload the engine will hold.
//
// EVERY PRODUCER GOES THROUGH IT — Build and Merge both — so no path can publish
// a heap-resident graph and quietly reintroduce the per-node Go structure this
// format exists to remove. The round trip is not waste: the bytes are the
// segment's canonical form (its id is their sha256), so building them here is
// work Encode would otherwise do later anyway.
func publishGraph(h *binaryGraph) (searchengine.Segment[[]byte, struct{}], error) {
	blob, err := encodeGraphV3(h)
	if err != nil {
		return nil, fmt.Errorf("hnsw publish: %w", err)
	}
	graph, err := openGraphV3(blob)
	if err != nil {
		return nil, fmt.Errorf("hnsw publish: reopening freshly written bytes: %w", err)
	}
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
// the only correct merge — this is exactly how Lucene merges HNSW.
//
// Construction goes through the SAME byte-reproducible serial builder Build uses,
// which buys two properties the caller depends on. The merge CONVERGES: repeating
// it over the same survivors yields the same bytes and therefore the same content
// hash, so one writer re-running its work republishes an identical segment instead
// of a fresh generation. And it is ORDER-INDEPENDENT: the builder sorts by id, so
// the result does not depend on which order the inputs were visited in, which is
// what makes a repeated consolidation idempotent even when the survivor set is
// assembled differently.
//
// Collecting the survivors first and building once is what enables both. Inserting
// into a graph as each input is walked would make the result depend on input order
// and on a per-call random seed.
func (Format) Merge(segs []searchengine.Segment[[]byte, struct{}], accept []func(searchengine.ExternalID) bool) (searchengine.Segment[[]byte, struct{}], error) {
	var items []binaryBuildItem
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
			items = append(items, binaryBuildItem{id: externalID, vec: vec})
		})
	}
	merged := buildBinaryHNSWSerialDeterministic(dedupeItemsByID(items), defaultVecBytes, defaultM, defaultEfConstruction)
	return publishGraph(merged)
}

// dedupeItemsByID collapses the collected merge items to ONE per external id,
// keeping the LAST occurrence and preserving the surviving order.
//
// WITHOUT THIS THE MERGE IS THE DEFECT. Constituents that share an id both pass the
// accept predicate — same id, both live, same partition — so both copies reach the
// builder and the graph ends up holding two nodes for one id, while the engine's
// route map records a single ordinal. Every membership check still passes and
// retrieval collapses: measured at recall 0.417 even when the query vector is exactly
// what the engine stored for that id.
//
// WHY LAST rather than first, and what it does and does not promise. The caller hands
// segments in a deliberate order — resident constituents sorted by segment id, then
// the freshly built segment appended LAST — so keeping the last copy yields exactly
// two guarantees:
//   - Where a FRESH WRITE exists for an id, it wins. That is semantic newest-wins,
//     and it is the case the transient write window produces.
//   - Between two RESIDENT layers with no supersession between them, the winner is
//     whichever sorted later by segment id: ARBITRARY, but STABLE across runs and
//     across shuffled input order.
//
// It is NOT newest-wins overall, and describing it that way would be wrong for the
// second case — which is precisely the case that produced this defect, where two
// imported layers arrived all-live with no supersession ever having run between them.
func dedupeItemsByID(items []binaryBuildItem) []binaryBuildItem {
	lastAt := make(map[string]int, len(items))
	for i, it := range items {
		lastAt[it.id] = i
	}
	if len(lastAt) == len(items) {
		return items // no id repeated across the constituents — the ordinary merge.
	}
	out := make([]binaryBuildItem, 0, len(lastAt))
	for i, it := range items {
		if lastAt[it.id] == i {
			out = append(out, it)
		}
	}
	return out
}

// AggregateStats is a no-op for HNSW — binary search needs no corpus-wide stats.
func (Format) AggregateStats([]searchengine.Segment[[]byte, struct{}]) struct{} {
	return struct{}{}
}

// Search runs the HNSW query and maps each graph hit to a searchengine.Hit. The
// accept liveDocs filter is threaded straight into the graph's over-fetch-then-
// filter loop: a dead id is skipped from RESULTS, the graph is never mutated.
//
// THE ID IS COPIED HERE. graphHit.externalID is a VIEW over the segment mapping,
// and a Hit outlives the search call — the engine merges hits across segments
// and hands them to a caller who may hold them past an eviction. Handing back a
// view would be a use-after-unmap that reads plausible garbage rather than
// crashing, which is the silent-degradation failure this whole format change is
// written to avoid.
func (s *hnswSegment) Search(q []byte, _ struct{}, k int, accept func(searchengine.ExternalID) bool) []searchengine.Hit {
	graphHits := s.graph.search(q, k, accept)
	hits := make([]searchengine.Hit, 0, len(graphHits))
	for _, gh := range graphHits {
		hits = append(hits, searchengine.Hit{ID: strings.Clone(gh.externalID), Score: gh.score})
	}
	return hits
}

// IDs lists every ExternalID the segment indexes (live or dead), in a stable
// internal-ID order. The engine builds its externalID→segment route map from this.
//
// EVERY ID IS COPIED. The engine holds this route map for the life of the
// entry — longer than any single mapping is guaranteed to live — so a view
// would pin, or outlive, the bytes it was read from.
func (s *hnswSegment) IDs() []searchengine.ExternalID {
	views := s.graph.ids()
	out := make([]searchengine.ExternalID, len(views))
	for i, v := range views {
		out[i] = strings.Clone(v)
	}
	return out
}

// VectorByID returns the stored binary vector for an external id, or (nil,false)
// when the id is not in this segment. It is the by-id stored-vector read the
// "similar" search mode resolves a query vector from. Deliberately NOT on the
// shared searchengine.Segment interface (segment.go) — that interface is also
// satisfied by bm25Segment, which has no vectors; the engine's SegmentedIndex
// reaches this concrete method via a runtime type-assert, so bm25 segments cleanly
// resolve (nil,false) instead of being forced to implement an unsatisfiable accessor.
//
// THE VECTOR IS COPIED. nodeVector returns a sub-slice of the segment mapping,
// and this value is handed to a caller who uses it as a QUERY against other
// segments — outliving this segment's residency entirely.
func (s *hnswSegment) VectorByID(externalID string) ([]byte, bool) {
	vec, ok := s.graph.vectorByID(externalID)
	if !ok {
		return nil, false
	}
	return slices.Clone(vec), true
}

// Encode returns the segment's own bytes. The blob IS the encoded form, so this
// is an identity rather than a re-serialization, and the segment id (sha256 of
// the bytes) is trivially stable across a decode/encode round trip.
func (s *hnswSegment) Encode() ([]byte, error) { return s.graph.blob, nil }

// hnswSegmentHeapBytes models the Go heap one sealed HNSW segment holds: the
// hnswSegment struct and the graph handle it points at. Written as a small
// fixed estimate because, once the graph is read in place, the topology and
// vector bytes live in the segment mapping — page cache, not Go heap — and the
// segment keeps no per-node Go structure of its own.
//
// KNOWN LOW, AND THE ERROR OVER-ADMITS: measured against a real corpus this
// estimate under-charges per-segment heap (~145.6 B observed vs 64 modeled,
// ~0.68% of a 1 GiB budget at 50k segments), so the admission meter lets in
// slightly MORE than the budget intends — not the conservative direction. If
// this constant is ever re-pinned, pin it to a measured-or-higher value;
// re-pinning changes admission behaviour and needs its own gate.
const hnswSegmentHeapBytes int64 = 64

// HeapBytes models the Go heap this sealed segment holds — see
// searchengine.Segment.HeapBytes, which documents that the number is an
// estimate rather than a measurement.
//
// The graph's bytes are deliberately excluded: they are the segment mapping,
// which is page cache rather than Go heap, so charging them to a heap budget
// would meter memory the garbage collector never sees.
func (s *hnswSegment) HeapBytes() int64 { return hnswSegmentHeapBytes }

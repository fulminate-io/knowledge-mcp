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
// The format owns its concrete Segment type (*hnswSegment) so MergeTo type-asserts
// its inputs and reads their internals (vectors) directly — no Document retention.
// There is no build-variant state: the HNSW builder is deterministic everywhere
// (the byte-reproducible serial path is the ONLY builder), so Format carries no
// fields.
//
// IT SATISFIES SegmentFormat's CONCURRENCY OBLIGATION by being a STATELESS VALUE
// TYPE: no fields, value receivers, every per-call allocation local. The engine
// drives one Format value from several harvest goroutines at once, so a mutable
// receiver here would race silently.
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
//
// THE WIDTH AND THE DTYPE BOTH COME FROM THE DOCUMENTS, never from a constant
// this format chose. batchVecBytes derives the width and batchBuildDtype the
// representation, and each REFUSES a batch that mixes its own — see their docs
// for the corruption a fixed value was measured to cause. The empty-vector skip
// above is a separate rule and survives untouched: it is the tolerate-absent-
// data contract, not a width or dtype judgement.
//
// THEY ARE DERIVED AS A PAIR, over the SAME post-skip items, because deriving
// one and fixing the other is what this call used to do: the width tracked the
// documents while the dtype was pinned to ubinary, so a float32 batch sealed at
// its correct width and was then ranked by Hamming distance over IEEE bit
// patterns — a segment that is byte-correct, length-correct and ordered wrong,
// with nothing anywhere reporting a problem.
// THE ZERO BuildReport IS AN ANSWER, NOT A PLACEHOLDER. This build drops
// nothing it was asked to index: a document with no vector is not
// vector-indexable INPUT, and the loop below skips it before any indexing
// decision is taken, so there is no contained loss for a census to name. A
// future HNSW degrade class populates the field; until one exists, reporting an
// empty census is the truthful answer rather than a stub.
func (Format) Build(docs []searchengine.Document) (searchengine.Segment[[]byte, struct{}], searchengine.BuildReport, error) {
	items := make([]binaryBuildItem, 0, len(docs))
	for _, d := range docs {
		if len(d.Vector) == 0 {
			continue
		}
		items = append(items, binaryBuildItem{id: d.ID, vec: d.Vector, dtype: d.Dtype})
	}
	vecBytes, err := batchVecBytes(items)
	if err != nil {
		return nil, searchengine.BuildReport{}, err
	}
	dtype, err := batchBuildDtype(items)
	if err != nil {
		return nil, searchengine.BuildReport{}, fmt.Errorf("hnsw build: %w", err)
	}
	graph := buildBinaryHNSWSerialDeterministic(items, vecBytes, dtype, defaultM, defaultEfConstruction)
	seg, err := publishGraph(graph)
	return seg, searchengine.BuildReport{}, err
}

// publishGraph is the ONE seal-and-open path: it encodes a freshly built graph
// to v3 bytes and reopens them as the mapped payload the engine will hold.
//
// EVERY BUILD GOES THROUGH IT, so no build path can publish a heap-resident
// graph and quietly reintroduce the per-node Go structure this format exists to
// remove. A MERGE no longer comes through here: MergeTo writes its graph
// straight to the engine's destination and never reopens it, which is how the
// merge's retention of that output reaches zero. The round trip is not waste: the bytes are the
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

// MergeTo consolidates segs into dst and reports the merged segment's byte
// length. The consolidation is shared with Build's own graph construction via
// mergeToGraph; what differs is the ending — a direct write to dst instead of an
// encode-and-reopen.
//
// WHAT THIS REMOVES, AND WHAT IT DOES NOT. It removes the encoder's
// output-sized buffer — encodeGraphV3To writes the blob to dst instead of
// materializing it — and it takes the merge's retention of that output to zero,
// because nothing here holds the encoded bytes after they are written.
//
// IT DOES NOT MAKE AN HNSW MERGE ALLOCATION-FREE IN THE OUTPUT'S SIZE, and
// saying otherwise would be false. This format cannot splice graphs: neighbor
// links are internal-id-relative, so a merge re-inserts every survivor into a
// fresh binaryGraph, and that graph's vector block alone is output-sized and must
// be fully resident before a byte can be emitted. The peak drops by roughly the
// encode buffer; it does not reach zero. The zero-output-sized-allocation merge
// property is bm25's, by that format's algorithm rather than by any extra care
// taken here.
//
// OWNERSHIP: dst belongs to the caller. This does not truncate, close, stat,
// unlink or map it, and it leaves dst in place on the error path.
func (Format) MergeTo(dst searchengine.MergeSink, segs []searchengine.Segment[[]byte, struct{}], accept []func(searchengine.ExternalID) bool) (int64, error) {
	merged, err := mergeToGraph(segs, accept)
	if err != nil {
		return 0, err
	}
	return encodeGraphV3To(dst, merged)
}

// mergeToGraph is the consolidation MergeTo runs: collect the survivors, derive
// width and dtype as a pair, check the per-dtype seal target, and build one graph
// through the byte-reproducible serial builder.
//
// IT IS ITS OWN FUNCTION so the consolidation stays separable from the emission.
// The convergence and order-independence properties MergeTo documents are
// properties of THIS body, and the dtype derivation in particular is the step
// whose loss once turned a merge of two float32 segments into a ubinary one.
func mergeToGraph(segs []searchengine.Segment[[]byte, struct{}], accept []func(searchengine.ExternalID) bool) (*binaryGraph, error) {
	var items []binaryBuildItem
	hss := make([]*hnswSegment, 0, len(segs))
	for i, s := range segs {
		hs, ok := s.(*hnswSegment)
		if !ok {
			return nil, fmt.Errorf("hnsw merge: input %d is %T, not *hnswSegment", i, s)
		}
		hss = append(hss, hs)
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
	survivors := dedupeItemsByID(items)
	// WIDTH AND DTYPE ARE DERIVED AS A PAIR, from the constituents themselves.
	// Deriving only the width and passing a fixed dtype is what made a merge of
	// two float32 segments produce a ubinary one: every vector byte preserved,
	// the metric that ranks them replaced, and nothing anywhere reporting a
	// problem.
	vecBytes, err := batchVecBytes(survivors)
	if err != nil {
		return nil, fmt.Errorf("hnsw merge: %w", err)
	}
	dtype, err := batchDtype(hss)
	if err != nil {
		return nil, fmt.Errorf("hnsw merge: %w", err)
	}
	// THE PER-DTYPE SEAL TARGET IS CHECKED HERE, BEFORE THE BUILD, because this is
	// where the u32 blob ceiling actually bites. Segments seal at a fraction of the
	// hard ceiling (v3SealSafetyDivisor) so that consolidating that many full
	// segments still fits; a consolidation of MORE than that can exceed it. Left
	// unchecked, the cost is paid twice over: the builder does the full O(n log n)
	// insertion work first and only then does encodeGraphV3 refuse, reporting a
	// byte count rather than the thing the operator can act on — how many nodes
	// this width admits per segment.
	if maxNodes := maxNodesPerSegment(vecBytes); len(survivors) > maxNodes {
		return nil, fmt.Errorf(
			"hnsw merge: %d survivors at %d bytes per vector exceed the %d-node per-segment ceiling a u32 offset can address; merge fewer constituents at this width",
			len(survivors), vecBytes, maxNodes)
	}
	return buildBinaryHNSWSerialDeterministic(survivors, vecBytes, dtype, defaultM, defaultEfConstruction), nil
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

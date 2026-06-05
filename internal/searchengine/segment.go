package searchengine

// SegmentFormat is the index-agnostic codec the engine drives. Q is the query
// type a format understands (BM25: bm25.Query; HNSW: []byte) and S is the
// corpus-wide statistics a search needs (BM25: *bm25.CorpusStats for IDF; HNSW:
// struct{}{}). The engine is generic over both and owns no format internals.
type SegmentFormat[Q, S any] interface {
	// Name identifies the format (used to tag SegmentBlob.Format for routing).
	Name() string
	// Build seals an immutable Segment from a batch of live documents.
	Build(docs []Document) (Segment[Q, S], error)
	// Decode reconstructs a Segment from its encoded bytes. A decoded segment is
	// indistinguishable from a freshly built one and is fully merge-eligible.
	Decode(blob []byte) (Segment[Q, S], error)
	// Merge consolidates several segments into one all-live segment, Lucene-style.
	// It reads the LIVE INDEXED data directly from each segs[i] (the format owns
	// its concrete Segment type and type-asserts the inputs to read their
	// internals — vectors/postings — so no original Documents are required and
	// DECODED/PULLED segments merge fine; the Segment interface needs no accessor).
	// accept has the same length and ordering as segs: accept[i] gates segs[i],
	// keeping only members for which accept[i](id) is true. The result is a single
	// consolidated segment in which every surviving member is live.
	Merge(segs []Segment[Q, S], accept []func(ExternalID) bool) (Segment[Q, S], error)
	// AggregateStats computes the corpus-wide stats over the current segment set.
	// BM25 sums document frequencies for IDF; HNSW returns struct{}{}.
	AggregateStats(segs []Segment[Q, S]) S
}

// Segment is one immutable, sealed index shard. Sealed segments are never
// mutated, which is what makes the read path lock-free and parallel-safe.
type Segment[Q, S any] interface {
	// Search returns up to k hits for q. stats is the cached corpus statistics
	// and accept is the liveDocs filter — Search must exclude any ExternalID for
	// which accept returns false.
	Search(q Q, stats S, k int, accept func(ExternalID) bool) []Hit
	// IDs lists every ExternalID the segment indexes (live or dead), in a stable
	// order. The engine uses this to build the externalID→segment route map.
	IDs() []ExternalID
	// Encode serializes the segment to bytes for shipping/persistence.
	Encode() ([]byte, error)
}

// SegmentID is the content hash that names a segment blob.
type SegmentID = string

// SegmentBlob is the shippable form of a segment: opaque bytes plus the routing
// metadata the distribution layer needs. It is a CLIENT engine type; the server
// stores it opaquely and stamps/orders Generation.
type SegmentBlob struct {
	ID         SegmentID
	Format     string
	Generation uint64
	Bytes      []byte
}

// SegmentMeta is the lightweight descriptor the distribution layer lists without
// fetching bytes. DocCount/DeadCount feed delta selection + merge metrics.
type SegmentMeta struct {
	ID         SegmentID
	Format     string
	Generation uint64
	DocCount   int
	DeadCount  int
}

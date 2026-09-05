package searchengine

import "io"

// SegmentFormat is the index-agnostic codec the engine drives. Q is the query
// type a format understands (BM25: bm25.Query; HNSW: []byte) and S is the
// corpus-wide statistics a search needs (BM25: *bm25.CorpusStats for IDF; HNSW:
// struct{}{}). The engine is generic over both and owns no format internals.
//
// BUILD AND MERGE MUST BE SAFE FOR CONCURRENT USE. The engine holds ONE format
// value per index and calls it from several goroutines at once: ReplaceBucketGroup
// harvests its partitions on a bounded worker pool, and every worker drives the
// SAME format instance. An implementation carrying mutable per-call scratch state
// on its receiver would race there, and it would race SILENTLY — the harvest's
// output is content-hashed, so corruption surfaces as a mismatched segment id long
// after the fact rather than as a panic at the write.
//
// THE SATISFYING SHAPE IS A STATELESS VALUE TYPE, which is what both production
// formats are: `func (Format) Build(...)` / `func (Format) MergeTo(...)` on an empty
// struct, with all per-call state local. A format needing scratch space allocates
// it per call or guards it; it does not hang it off the receiver. A merge no
// longer has to arrange per-call uniqueness for the file it writes: the engine
// creates one destination per MergeTo call and passes it in, which is the same
// requirement satisfied one layer up.
type SegmentFormat[Q, S any] interface {
	// Name identifies the format (used to tag SegmentBlob.Format for routing).
	Name() string
	// Build seals an immutable Segment from a batch of live documents. The
	// BuildReport carries anything the format CONTAINED while building — see
	// BuildReport; a format with nothing to report returns the zero value.
	Build(docs []Document) (Segment[Q, S], BuildReport, error)
	// Decode reconstructs a Segment from its encoded bytes. A decoded segment is
	// indistinguishable from a freshly built one and is fully merge-eligible.
	Decode(blob []byte) (Segment[Q, S], error)
	// MergeTo consolidates several segments into one all-live segment,
	// Lucene-style, writing it into dst and reporting its byte length rather than
	// materializing it.
	//
	// It reads the LIVE INDEXED data directly from each segs[i] (the format owns
	// its concrete Segment type and type-asserts the inputs to read their
	// internals — vectors/postings — so no original Documents are required and
	// DECODED/PULLED segments merge fine; the Segment interface needs no accessor).
	// accept has the same length and ordering as segs: accept[i] gates segs[i],
	// keeping only members for which accept[i](id) is true. The result is a single
	// consolidated segment in which every surviving member is live.
	//
	// IT WRITES AND REPORTS A LENGTH; IT OWNS NOTHING ELSE. It does not Truncate,
	// Close, Stat, unlink or map dst. The ENGINE creates the destination, sizes it
	// from the returned n, maps it, decodes over the mapping and disposes of the
	// file — on the success path and on every error path. A format that cleaned up
	// after itself here would hide an engine that forgot to.
	//
	// n IS AUTHORITATIVE OVER THE DESTINATION'S SIZE. A format may leave dst longer
	// than n: writing at an aligned offset can advance a writer's tail past the last
	// byte that carries content. The engine's Truncate to n is what makes the file
	// exactly the segment, so no caller may substitute a Stat for n.
	//
	// THE CONCURRENCY RULE ABOVE BINDS HERE VERBATIM, with one sharper obligation:
	// each concurrent call is given its OWN dst, so an implementation must not
	// retain dst on its receiver any more than it may retain other per-call state.
	MergeTo(dst MergeSink, segs []Segment[Q, S], accept []func(ExternalID) bool) (n int64, err error)
	// AggregateStats computes the corpus-wide stats over the current segment set.
	// BM25 sums document frequencies for IDF; HNSW returns struct{}{}.
	AggregateStats(segs []Segment[Q, S]) S
}

// BuildReport is what a format tells the engine about a build BESIDES the
// segment it produced.
//
// IT IS A VALUE, NOT AN ERROR. A build that dropped one document still produced
// a valid segment for all the rest, so the drop cannot ride the error return
// without discarding work that succeeded. A format with nothing to report
// returns the ZERO VALUE, whose Degraded is nil — empty and absent are one
// state, so a clean build is indistinguishable from one by a format that has no
// degrade classes at all.
//
// Degraded is the SAME fixed-vocabulary shape collectorwire.CollectResult.Degraded
// uses on the collect path (class name → count), so an operator reads one census
// vocabulary across the product rather than two. The engine does not interpret
// the keys: the vocabulary belongs to the format that produced it.
type BuildReport struct {
	Degraded map[string]int
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
	// HeapBytes reports the Go heap this payload holds, in bytes.
	//
	// THE NUMBER IS A MODELED ESTIMATE, NOT A MEASUREMENT. Go exposes no
	// per-object heap query, so every implementation returns a documented
	// formula over what it knows it holds. A caller reading it as an exact
	// byte count would be wrong; it is accurate in ORDER, not to the byte.
	//
	// A MAPPED payload returns its own fixed struct size and nothing more:
	// its blob lives in the page cache, which is evictable, shared and
	// invisible to the garbage collector, so charging those bytes to a heap
	// budget would meter memory the heap does not hold.
	//
	// It is on the interface rather than behind a type-assert on purpose. An
	// optional accessor lets a new format contribute heap while reporting
	// zero, and a silent zero in a memory budget is precisely the failure this
	// method exists to prevent — the compiler now refuses a payload that has
	// not declared its cost.
	HeapBytes() int64
}

// SegmentID is the content hash that names a segment blob.
type SegmentID = string

// SegmentBlob is the shippable form of a segment: opaque bytes plus the routing
// metadata the distribution layer needs. It is a CLIENT engine type; the server
// stores it opaquely and stamps/orders Generation. DocCount is the segment's live
// doc count (carried alongside the bytes so the server can persist it for the
// segment-coverage levers — read back via the ListDelta metas without decoding).
// THE STORED FILE IS Envelope FOLLOWED BY Bytes, AND THAT IS TRUE OF EVERY BLOB
// HOWEVER IT WAS PRODUCED. Bytes is the FORMAT PAYLOAD only and never contains an
// envelope; Envelope is the supersession prefix and is nil when there is no
// record. A blob read from the L2 cache has both fields set as subslices of the
// SAME mapping, so the split costs no copy.
//
// THE UNIFORMITY IS THE DESIGN. The rejected alternative — leaving the whole
// stored file in Bytes for blobs read from disk while engine-produced blobs
// carried a split — would give one field two meanings depending on provenance,
// and every consumer would have to know where its blob came from before it could
// read it. That the cache FILENAME names the payload and not the whole file is
// the closely-related statement recorded on diskSegmentCache; this is its
// field-level expression.
type SegmentBlob struct {
	ID         SegmentID
	Format     string
	Generation uint64
	DocCount   int
	// Bytes is the format payload alone. See the type's own paragraph.
	Bytes []byte
	// Envelope is the supersession prefix that precedes Bytes in the stored file,
	// nil when this blob records no supersession.
	Envelope []byte
	// Release frees the resources backing Bytes when they are a MAPPING rather
	// than a heap copy. Nil means Bytes are heap-owned and nothing needs
	// freeing. It is NOT called by whoever receives the blob: the engine hands
	// it to a cleanup keyed on the entry's reachability, because a reader can
	// still be walking a pre-swap snapshot when an entry is swapped out.
	Release func()
	// keepAlive pins the resident entry whose mapping backs Bytes, and is nil
	// when Bytes are heap-owned. The cleanup that frees a mapping observes the
	// ENTRY's reachability, and holding a byte slice does not make the entry
	// reachable — so a blob that outlives its entry would otherwise be reading
	// unmapped memory. Keeping the reference IN the struct means every copy of
	// the blob carries the guarantee with it.
	keepAlive any
}

// PinsMapping reports whether this blob carries the reference that keeps a
// mapping-backed payload alive.
//
// IT EXISTS BECAUSE THE PROPERTY IS OTHERWISE UNOBSERVABLE FROM OUTSIDE, and an
// unobservable property is one no gate can hold. A holder that retains a blob's
// Bytes but rebuilds the struct around them — which any other package must do,
// since keepAlive is unexported and cannot be set from outside — silently drops
// the pin. The bytes stay readable for as long as the entry happens to remain
// reachable, so the defect is invisible until a collection lands between the
// retention and the read.
//
// A caller that retains a blob past the lifetime of whatever handed it over is
// the case this is for: it asserts the thing it retained still pins its payload.
// It is not a health check on a blob in flight — a heap-backed blob correctly
// reports false, because heap bytes need no pin.
func (b SegmentBlob) PinsMapping() bool { return b.keepAlive != nil }

// MergeSink is the destination a format writes a merged segment into. The engine
// supplies it; an *os.File satisfies it, and so does a slice-backed adapter.
//
// IT CARRIES ReaderAt AS WELL AS WriterAt, and the second half is not decoration.
// A format whose emitted sections advance out of file order cannot accumulate a
// running checksum over what it writes, so it re-reads the finished range in
// bounded chunks to compute one. hnsw's encoder is exactly that shape: four
// cursors ascend independently while the footer CRC must cover every byte
// including the backpatched header.
//
// A FORMAT WRITES AND REPORTS A LENGTH; IT OWNS NOTHING ELSE. It does not
// Truncate, Close, Stat, unlink or map — the engine created the destination and
// the engine disposes of it, on the success path and on every error path.
type MergeSink interface {
	io.WriterAt
	io.ReaderAt
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

// MergeResult is the supersession event a completed background merge surfaces to
// its owner via Options.OnMerge: Removed is the set of superseded constituent
// segment ids the merge consolidated away, and Merged is the shippable blob of
// the single all-live consolidated segment that replaced them. The owner uses
// this to reclaim the superseded constituents' L2 disk files (Put the merged
// blob, then Remove the constituents). It lives engine-side because doMerge
// builds it from engine-internal entries; segmentdist (which consumes it) already
// imports searchengine, so the engine never imports segmentdist.
type MergeResult struct {
	Removed []SegmentID
	Merged  SegmentBlob
}

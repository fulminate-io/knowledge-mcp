package searchengine

import "context"

// The three distribution interfaces below name the boundary between the client
// engine and the server blob store. All three are IMPLEMENTED AND WIRED — the
// client engine pulls segments through them on EVERY search
// (segmentdist.Manager.Search -> distManager.load -> SegmentSource.List/Fetch ->
// SegmentedIndex.Import):
//   - SegmentStore  — the SERVER-side blob store, installed by the server at
//     startup via a resolver hook. It stamps/orders generations and persists
//     bytes opaquely; it has no engine and never decodes a segment.
//   - SegmentSource — the CLIENT pull seam, rpcSegmentSource over the
//     SegmentService wire (../../segmentdist/source.go).
//   - SegmentCache  — the CLIENT L2 disk cache, diskSegmentCache, content-
//     addressed under the segments cache dir (../../segmentdist/cache.go).
//
// The source of truth is PER-SOURCE (the SegmentSource is pluggable):
//   - RPC/CLOUD source (logged in): the SERVER is the source of truth for shipped
//     segments; the local cache is an L2 accelerator and is wipe-safe (a fresh
//     process re-lists from generation 0 and re-fetches).
//   - OSS-LOCAL source (not logged in): the L2 disk cache is AUTHORITATIVE and NO
//     SegmentService is consulted. Segment identity is the content-hash, ordering is
//     the local manifest (cache.Keys()), and a lost/cold cache heals by rebuilding
//     from the local embedded node graph — never a server Fetch.
// Server-side search is retired in BOTH regimes — these client segments are the ONLY
// search index, so a graph with no built/shipped segments is unsearchable until its
// segments are (re)built.

// SegmentStore is the SERVER-side opaque blob store. The server stamps/orders
// Generation, persists the bytes, and serves list/fetch — it has NO engine and
// never decodes a segment. graphKey scopes blobs to a logical index.
type SegmentStore interface {
	Put(ctx context.Context, graphKey string, blobs []SegmentBlob) error
	List(ctx context.Context, graphKey string, sinceGen uint64) ([]SegmentMeta, error)
	Get(ctx context.Context, graphKey string, ids []SegmentID) ([]SegmentBlob, error)
}

// SegmentSource is the CLIENT-side pull seam (RPC-backed). The client lists the
// delta (gen > last-seen) and fetches the blobs it is missing, then Imports them.
// Both legs take the caller ctx so a cold/partial-L2 search that drives a Fetch is
// cancellable (a search ctx cancel / shutdown unwinds the in-flight RPC).
type SegmentSource interface {
	List(ctx context.Context, sinceGen uint64) ([]SegmentMeta, error)
	Fetch(ctx context.Context, ids []SegmentID) ([]SegmentBlob, error)
}

// SegmentCache is the CLIENT-side L2 disk cache. A cache hit skips the network
// on the next Source.Fetch.
type SegmentCache interface {
	Get(id SegmentID) ([]byte, bool)
	Put(id SegmentID, b []byte)
	// GetMapped returns the cached blob as a read-only MEMORY MAPPING plus the
	// closure that frees it, rather than as a heap copy. It is what keeps a
	// resident segment's bytes in the OS page cache instead of the Go heap.
	//
	// The two failure modes are deliberately distinct, and conflating them is
	// the bug this signature exists to prevent. ok=false with a nil error is a
	// genuine MISS — the id is not cached, and the caller should fetch it. A
	// non-nil error means the id IS cached but could not be mapped, which is a
	// condition to report: answering it as a miss would send the caller down a
	// heap-reading path and hide a broken mapping seam behind a slow one.
	GetMapped(id SegmentID) (data []byte, release func(), ok bool, err error)
}

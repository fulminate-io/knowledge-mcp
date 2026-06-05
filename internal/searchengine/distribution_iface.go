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
// The server is the SOURCE OF TRUTH for shipped segments; the local cache is an
// L2 accelerator and is wipe-safe (a fresh process re-lists from generation 0
// and re-fetches). Server-side search is retired — these client segments are
// the ONLY search index, so a graph with no shipped segments is unsearchable
// until its segments are (re)built and shipped.

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
type SegmentSource interface {
	List(ctx context.Context, sinceGen uint64) ([]SegmentMeta, error)
	Fetch(ids []SegmentID) ([]SegmentBlob, error)
}

// SegmentCache is the CLIENT-side L2 disk cache. A cache hit skips the network
// on the next Source.Fetch.
type SegmentCache interface {
	Get(id SegmentID) ([]byte, bool)
	Put(id SegmentID, b []byte)
}

// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// localSegmentSource is the OSS-local segmentSource: it satisfies the full
// segmentSource seam (searchengine.SegmentSource List/Fetch plus the ship/prune/
// publish legs) over the L2 disk cache ALONE, issuing ZERO SegmentService RPC. It
// is the pluggable-backend second implementation, selected by the capability gate
// when the caller is not cloud-capable (not logged in) — the OSS path, where there
// is no server segment store to consult. rpcSegmentSource remains the logged-in/
// cloud implementation; the two share no server code (their bodies are disjoint),
// which is why an L2-only sibling is a distinct impl rather than an extension of
// the RPC one.
//
// SOURCE OF TRUTH: on this path the L2 cache is authoritative. Segment identity is
// the content-hash (already the blob id — no server generation ordering, decouple
// #1), the local manifest is cache.Keys(), and a lost/cold L2 heals by rebuilding
// from the local embedded node graph (the bootstrap heal), never by a server Fetch.
type localSegmentSource struct {
	cache  segmentL2Cache
	format string
}

var _ segmentSource = (*localSegmentSource)(nil)

// newLocalSegmentSource builds the OSS-local source over one graph+format's L2
// cache. format tags the metas List stamps; the per-(graph,format) cache root
// (graphCacheDirFor) means cache.Keys() already returns only this format's ids, so
// the tag is exact.
func newLocalSegmentSource(cache segmentL2Cache, format string) *localSegmentSource {
	return &localSegmentSource{cache: cache, format: format}
}

// List returns one meta per L2-resident id (cache.Keys()), stamped with this
// source's format and Generation 0. sinceGen is IGNORED: the OSS load() is L2-only
// (Phase 3) so it never Lists, and the remaining OSS List callers —
// ensureShippedSeeded (ship-seed), liveSetSubsetOfList0 (prune subset gate), and
// the coverage probes — all pass sinceGen=0 and want the full L2 set. DocCount is 0
// deliberately: the OSS coverage probes disarm on an unknown/zero denominator (they
// have no server-shipped doc count to compare against), and the ship-seed/subset
// gates consume only the id set. Zero network.
func (s *localSegmentSource) List(_ context.Context, _ uint64) ([]searchengine.SegmentMeta, error) {
	keys := s.cache.Keys()
	metas := make([]searchengine.SegmentMeta, 0, len(keys))
	for _, id := range keys {
		metas = append(metas, searchengine.SegmentMeta{ID: id, Format: s.format, Generation: 0, DocCount: 0, DeadCount: 0})
	}
	return metas, nil
}

// Fetch serves the requested ids from the L2 cache (cache.Get) and SILENTLY OMITS
// misses — there is no server to fall back to, and a miss is tolerated by the
// L2-first reload(tolerateMisses=true) and by the load path. Zero network.
func (s *localSegmentSource) Fetch(_ context.Context, ids []searchengine.SegmentID) ([]searchengine.SegmentBlob, error) {
	blobs := make([]searchengine.SegmentBlob, 0, len(ids))
	for _, id := range ids {
		if b, ok := s.cache.Get(id); ok {
			blobs = append(blobs, searchengine.SegmentBlob{ID: id, Bytes: b})
		}
	}
	return blobs, nil
}

// Ship stamps the input blobs locally — the content-hash id IS the identity
// (decouple #1: no server generation ordering) — and returns the metas the
// manager's shipNew consumes (shipNew owns the cache.Put from the returned metas).
// No network. Mirrors the inMemSegmentService.blobMeta shape
// (Id/Format/Generation/DocCount), stamping Generation 0.
func (s *localSegmentSource) Ship(_ context.Context, blobs []*knowledgev1.SegmentBlobProto) ([]*knowledgev1.SegmentMetaProto, error) {
	metas := make([]*knowledgev1.SegmentMetaProto, 0, len(blobs))
	for _, b := range blobs {
		metas = append(metas, &knowledgev1.SegmentMetaProto{
			Id:         b.GetId(),
			Format:     b.GetFormat(),
			Generation: 0,
			DocCount:   b.GetDocCount(),
		})
	}
	return metas, nil
}

// Prune is a no-op on the OSS path (0, nil): there is no server refcount-GC; local
// reclaim is reclaimMerged + PruneCache (Phase 4). reconcilePrune is not on the OSS
// production path.
func (s *localSegmentSource) Prune(_ []searchengine.SegmentID) (int, error) {
	return 0, nil
}

// PublishManifest is a no-op on the OSS path (0, nil): there is no server manifest
// swap/reap; local reclaim (reclaimMerged + PruneCache, Phase 4) replaces it. The
// digests (incl. their doc_count) are ignored — the OSS L2 path stores no manifest.
func (s *localSegmentSource) PublishManifest(_ string, _ []segmentDigest) (int, error) {
	return 0, nil
}

// verifiesCompletenessServerSide is false for the OSS-local source: there is no
// server/agent HEAD-verify (PublishManifest is a no-op), so the publish path keeps
// the client-side subset-completeness check.
func (s *localSegmentSource) verifiesCompletenessServerSide() bool { return false }

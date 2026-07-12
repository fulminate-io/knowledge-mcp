// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// ShippedManifestSnapshot returns the graph's shipped HNSW-format segment metas —
// the single shared probe the read-side reconcile / heal consumers derive their
// answers from instead of each issuing its own List(0). It routes through the
// login-gated newSegmentSource, so the snapshot follows whichever source the graph
// runs on:
//
//   - CLOUD (logged-in): List(0) is the GCS agent MANIFEST/read (the manifest
//     digests + their doc_counts).
//   - OSS (not logged in): List(0) is the L2-local source's set. localSegmentSource.List
//     stamps DocCount=0, so the doc-count-bearing consumers (the coverage column)
//     do NOT read their denominator from this snapshot on the OSS path — they take
//     the L2-resident path instead (see ShippedSegmentDocCount). The presence answer
//     (HasShippedFromSnapshot) is still valid on both paths.
//
// It does NOT Fetch any blob and does NOT touch the per-graph engines/maps, so it is
// safe on the embed-drain / reconcile edge without disturbing resident state. The
// derived answers — presence (HasShippedFromSnapshot), HNSW doc-count
// (ShippedDocCountFromSnapshot), and the ratio-disarm probe (shippedDocCountForRatio
// in manager_backstop.go) — all consume one snapshot, so a single healNeedsRebuild /
// reconcile pass over a graph spends ONE read where it previously spent two-three.
//
// PASS nil for the cache on the LOGGED-IN path only would be dead work, but this
// call cannot know the login state before constructing the source, so it builds the
// per-graph cache and hands it in: on the OSS path localSegmentSource consumes it
// (its List reads cache.Keys()); on the cloud path the GCS source ignores it.
func (m *Manager) ShippedManifestSnapshot(
	ctx context.Context, gt kgtypes.GraphType, name string,
) ([]searchengine.SegmentMeta, error) {
	target := graphSelector(gt, name)
	format := hnsw.New().Name()
	cache := newDiskSegmentCache(graphCacheDirFor(m.cacheDir, gt, name, format), m.maxBytes)
	source := m.newSegmentSource(cache, gt, name, target, format)
	return source.List(ctx, 0)
}

// HasShippedFromSnapshot is the snapshot-derived presence answer: true when the
// server holds at least one segment meta for the graph. The body HasShippedSegments
// previously inlined, lifted to operate on a passed-in snapshot.
func (m *Manager) HasShippedFromSnapshot(snapshot []searchengine.SegmentMeta) bool {
	return len(snapshot) > 0
}

// ShippedDocCountFromSnapshot is the snapshot-derived coverage answer: the body
// ShippedSegmentDocCount previously inlined, lifted VERBATIM onto a passed-in
// snapshot (the disarm rules are unchanged — see ShippedSegmentDocCount's contract).
func (m *Manager) ShippedDocCountFromSnapshot(
	snapshot []searchengine.SegmentMeta,
) (covered int, anyUnknown bool) {
	hnswFormat := hnsw.New().Name()
	for _, meta := range snapshot {
		if meta.Format != hnswFormat {
			continue
		}
		if meta.DocCount == 0 {
			anyUnknown = true
			continue
		}
		covered += meta.DocCount
	}
	return covered, anyUnknown
}

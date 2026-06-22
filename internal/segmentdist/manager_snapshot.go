// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// ShippedManifestSnapshot issues ONE ListDelta(sinceGen=0) for the graph and
// returns the raw segment metas — the single shared probe the read-side reconcile /
// heal consumers derive their answers from instead of each issuing its own List(0).
// It does NOT Fetch any blob (ListDeltaResponse carries only Metas) and does NOT
// touch the per-graph engines/maps, so it is safe on the embed-drain / reconcile
// edge without disturbing resident state. A fresh rpcSegmentSource is built per call
// (no engine, no cache) — strictly the presence list.
//
// The three derived answers — presence (HasShippedFromSnapshot), HNSW doc-count
// (ShippedDocCountFromSnapshot), and the ratio-disarm probe (shippedDocCountForRatio
// in manager_backstop.go) — all consume one snapshot, so a single healNeedsRebuild /
// reconcile pass over a graph spends ONE List(0) where it previously spent two-three.
func (m *Manager) ShippedManifestSnapshot(
	ctx context.Context, gt kgtypes.GraphType, name string,
) ([]searchengine.SegmentMeta, error) {
	source := newRPCSegmentSource(m.caller, graphSelector(gt, name), m.writerID, context.Background())
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

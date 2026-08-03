// SPDX-License-Identifier: Apache-2.0

// deps_segment_cache_drop.go — the whole-graph L2 teardown seam manage(drop_graph)
// drives after the server-side drop succeeds. It sits beside the sibling segment
// seams in deps_segments.go rather than inside it because the verb is different:
// SegmentPruner reclaims orphans WITHIN a live graph, this removes a dead graph's
// footprint entirely.

package tools

import (
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// DropGraphCacheReport is the tools-local mirror of the segmentdist-native report,
// carrying what the teardown actually removed so the handler can say so instead of
// inferring a count. Formats names the per-format directories a graph directory was
// removed from (e.g. bm25, hnsw, rebuildstate); Files and Bytes are the totals
// across all of them.
type DropGraphCacheReport struct {
	Formats []string
	Files   int
	Bytes   int64
}

// SegmentCacheDropper is the narrow seam manage(drop_graph) drives to remove a
// dropped graph's local L2 artifacts. *segmentdist.Manager satisfies it via the
// bootstrap client_segment_dropcache.go adapter — the ONLY place the tools-local
// and segmentdist-native vocabularies meet. The target crosses this seam as an
// already-imported kgtypes.GraphType plus a name, and the report is the mirror
// struct above, so tools never imports segmentdist (this file references
// *segmentdist.Manager in PROSE only, never in a signature or a var _ assertion) —
// the same intra-client decoupling the sibling seams in deps_segments.go keep.
//
// A graph with nothing cached locally is a clean zero report and a nil error, not
// a failure: never having loaded a graph is the ordinary case, not an anomaly.
type SegmentCacheDropper interface {
	DropGraphCache(gt kgtypes.GraphType, name string) (DropGraphCacheReport, error)
}

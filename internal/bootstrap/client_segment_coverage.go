// SPDX-License-Identifier: Apache-2.0

// client_segment_coverage.go — the manage(status) SEGMENT-COVERAGE adapter.
//
// SPLIT OUT OF client_segment.go, unchanged, when that file reached the repo's
// hard 500-line cap. The seam is the one this package's siblings already use:
// client_segment.go keeps the segment manager's lifecycle and the other consumer
// seams, and this file keeps the one adapter whose job is a VOCABULARY MAPPING
// rather than a pass-through — see the type's own doc below for why that mapping
// has to live in the composition root.

package bootstrap

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// segmentCoverageAdapter is the ONE place the backstop's record crosses from the
// segment package into the tools carrier.
//
// IT EXISTS BECAUSE THE IMPORT CANNOT GO THE OTHER WAY: the segment package's own
// in-package tests import tools, so tools may not import the segment package in
// production, and therefore cannot name segmentdist.RepairState. bootstrap is the
// composition root and already imports both, so the conversion belongs here rather
// than inverting either layer. Every other method is a straight pass-through.
type segmentCoverageAdapter struct{ mgr *segmentdist.Manager }

func (a segmentCoverageAdapter) ShippedSegmentDocCount(
	ctx context.Context, gt kgtypes.GraphType, name string,
) (int, error) {
	return a.mgr.ShippedSegmentDocCount(ctx, gt, name)
}

func (a segmentCoverageAdapter) ResidentDocCount(gt kgtypes.GraphType, name string) int {
	return a.mgr.ResidentDocCount(gt, name)
}

// LoadRebuildState / LoadMergeWatermark forward this client's own consumer
// positions, which the status row renders as "how long since each advanced".
// Both are local record reads on the Manager — no RPC.
func (a segmentCoverageAdapter) LoadRebuildState(
	gt kgtypes.GraphType, name string,
) (int64, []searchengine.ExternalID, error) {
	return a.mgr.LoadRebuildState(gt, name)
}

func (a segmentCoverageAdapter) LoadMergeWatermark(gt kgtypes.GraphType, name string) (int64, error) {
	return a.mgr.LoadMergeWatermark(gt, name)
}

func (a segmentCoverageAdapter) LiveResidentDocCount(gt kgtypes.GraphType, name string) int {
	return a.mgr.LiveResidentDocCount(gt, name)
}

// BM25DegradeCounts forwards this client's accumulated per-graph BM25 drop
// census. A local record read on the Manager — no RPC — like its neighbors.
func (a segmentCoverageAdapter) BM25DegradeCounts(gt kgtypes.GraphType, name string) map[string]int {
	return a.mgr.BM25DegradeCounts(gt, name)
}

func (a segmentCoverageAdapter) RepairVerification(
	gt kgtypes.GraphType, name string,
) (tools.RepairVerification, bool) {
	st, ok := a.mgr.RepairStateCached(gt, name)
	if !ok {
		return tools.RepairVerification{}, false
	}
	return tools.RepairVerification{
		Residue:         st.Residue,
		Converged:       st.Converged,
		Scanned:         st.Scanned,
		VerifiedAtNanos: st.VerifiedAtNanos,
	}, true
}
